package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/18345174/echoear_cloud/internal/database"
)

type agentRegisterRequest struct {
	AgentID            string          `json:"agent_id" binding:"required"`
	HostName           string          `json:"host_name"`
	Platform           string          `json:"platform"`
	AppVersion         string          `json:"app_version"`
	PreferredDeviceUID *string         `json:"preferred_device_uid"`
	LanBaseURL         string          `json:"lan_base_url"`
	PublicKey          string          `json:"public_key"`
	KeyID              string          `json:"key_id"`
	KeyAlgorithm       string          `json:"key_algorithm"`
	Capabilities       json.RawMessage `json:"capabilities"`
}

type hapiEnvelope struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation"`
	TaskID       string `json:"task_id"`
	RequestID    string `json:"request_id"`
	Algorithm    string `json:"algorithm"`
	KeyID        string `json:"key_id"`
	EncryptedKey string `json:"encrypted_key"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	AAD          string `json:"aad"`
}

type encryptedBlob struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	AAD        string `json:"aad"`
}

func (s *Server) registerAgent(c *gin.Context) {
	var request agentRegisterRequest
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if message := validateAgentEncryption(request); message != "" {
		fail(c, http.StatusBadRequest, message)
		return
	}
	item, err := s.db.RegisterAgent(currentUserID(c), database.AgentInput{
		AgentID: request.AgentID, HostName: request.HostName, Platform: request.Platform, AppVersion: request.AppVersion,
		PreferredDeviceUID: request.PreferredDeviceUID, LanBaseURL: request.LanBaseURL,
		PublicKey: request.PublicKey, KeyID: request.KeyID, KeyAlgorithm: request.KeyAlgorithm,
		Capabilities: sanitizeCapabilities(request.Capabilities),
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "注册 Agent 失败")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) listAgents(c *gin.Context) {
	items, err := s.db.ListAccessibleAgents(currentUserID(c))
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询 Agent 失败")
		return
	}
	ok(c, "ok", gin.H{"agents": items})
}

func (s *Server) requestHapiConnection(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, hapiBodyMax)
	agentID := strings.TrimSpace(c.Param("agent_id"))
	var request struct {
		RequestID string       `json:"request_id"`
		Envelope  hapiEnvelope `json:"envelope"`
	}
	if !validIdentifier(agentID, 1, 128) || c.ShouldBindJSON(&request) != nil || !validIdentifier(request.RequestID, 8, 128) {
		fail(c, http.StatusBadRequest, "hapi 连接参数错误")
		return
	}
	access, err := s.db.ResolveAgentAccess(currentUserID(c), agentID)
	if err != nil || access == nil {
		fail(c, http.StatusNotFound, "Agent 不存在")
		return
	}
	agent := &access.Agent
	if !agent.Online {
		fail(c, http.StatusConflict, "Agent 当前离线")
		return
	}
	if message := validateEnvelope(request.Envelope, agent, request.RequestID); message != "" {
		fail(c, http.StatusBadRequest, message)
		return
	}
	ticket, claims, err := s.issueAccessTicket(c, access, request.RequestID)
	if err != nil {
		fail(c, http.StatusTooManyRequests, "访问限制已生效: "+err.Error())
		return
	}
	payload, _ := json.Marshal(gin.H{"request_id": request.RequestID, "envelope": request.Envelope, "access_ticket": ticket, "access": claims})
	fastPayload, fastErr := s.tunnel.requestDiscovery(
		c.Request.Context(),
		tunnelKey{userID: access.UserID, agentID: agent.AgentID},
		request.RequestID,
		payload,
		2*time.Second,
	)
	if fastErr == nil {
		var response struct {
			EncryptedPayload encryptedBlob `json:"encrypted_payload"`
			Error            string        `json:"error"`
		}
		if json.Unmarshal(fastPayload, &response) != nil || strings.TrimSpace(response.Error) != "" {
			_ = s.db.RevokeAccessLease(claims.TicketID)
			fail(c, http.StatusServiceUnavailable, "电脑拒绝了 hapi 连接请求")
			return
		}
		if message := validateEncryptedBlob(response.EncryptedPayload); message != "" {
			_ = s.db.RevokeAccessLease(claims.TicketID)
			fail(c, http.StatusBadGateway, "电脑返回的 hapi 连接信息无效")
			return
		}
		raw, _ := json.Marshal(response.EncryptedPayload)
		if _, err := s.db.SaveHapiResponse(access.UserID, agent.AgentID, request.RequestID, raw); err != nil {
			_ = s.db.RevokeAccessLease(claims.TicketID)
			fail(c, http.StatusInternalServerError, "保存 hapi 连接响应失败")
			return
		}
		ok(c, "ok", gin.H{"request_id": request.RequestID, "encrypted_payload": response.EncryptedPayload})
		return
	}
	if errors.Is(fastErr, context.Canceled) || errors.Is(fastErr, context.DeadlineExceeded) {
		_ = s.db.RevokeAccessLease(claims.TicketID)
		return
	}
	item, err := s.db.EnqueueHapiConnection(access.UserID, agent.AgentID, request.RequestID, payload)
	if err != nil {
		_ = s.db.RevokeAccessLease(claims.TicketID)
		fail(c, http.StatusInternalServerError, "下发 hapi 连接请求失败")
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"code": 0, "message": "accepted", "data": item})
}

func (s *Server) claimHapiCommands(c *gin.Context) {
	agentID := strings.TrimSpace(c.Query("agent_id"))
	if agentID == "" {
		agentID = "default"
	}
	items, err := s.db.ClaimHapiConnections(c.Request.Context(), currentUserID(c), agentID, 20)
	if err != nil {
		fail(c, http.StatusInternalServerError, "拉取命令失败")
		return
	}
	ok(c, "ok", gin.H{"commands": items})
}

func (s *Server) ackHapiCommand(c *gin.Context) {
	commandID, err := strconv.ParseInt(strings.TrimSpace(c.Param("command_id")), 10, 64)
	if err != nil || commandID <= 0 {
		fail(c, http.StatusBadRequest, "command_id 无效")
		return
	}
	var request struct {
		Status string          `json:"status" binding:"required"`
		Result json.RawMessage `json:"result"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	status := strings.ToLower(strings.TrimSpace(request.Status))
	if status != "completed" && status != "rejected" {
		fail(c, http.StatusBadRequest, "status 须为 completed 或 rejected")
		return
	}
	item, err := s.db.CompleteHapiConnection(currentUserID(c), commandID, status, request.Result)
	if err != nil {
		fail(c, http.StatusInternalServerError, "更新命令结果失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "命令不存在或状态已变化")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) putHapiResponse(c *gin.Context) {
	agentID, requestID := strings.TrimSpace(c.Param("agent_id")), strings.TrimSpace(c.Param("request_id"))
	var request struct {
		EncryptedPayload encryptedBlob `json:"encrypted_payload"`
	}
	if !validIdentifier(requestID, 8, 128) || c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "加密响应参数错误")
		return
	}
	if agent, err := s.db.AgentForUser(currentUserID(c), agentID); err != nil || agent == nil {
		fail(c, http.StatusNotFound, "Agent 不存在")
		return
	}
	if message := validateEncryptedBlob(request.EncryptedPayload); message != "" {
		fail(c, http.StatusBadRequest, message)
		return
	}
	payload, _ := json.Marshal(request.EncryptedPayload)
	item, err := s.db.SaveHapiResponse(currentUserID(c), agentID, requestID, payload)
	if err != nil {
		fail(c, http.StatusInternalServerError, "保存加密响应失败")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) getHapiResponse(c *gin.Context) {
	agentID, requestID := strings.TrimSpace(c.Param("agent_id")), strings.TrimSpace(c.Param("request_id"))
	access, err := s.db.ResolveAgentAccess(currentUserID(c), agentID)
	if err != nil || access == nil {
		fail(c, http.StatusNotFound, "Agent 不存在")
		return
	}
	item, err := s.db.HapiResponse(access.UserID, access.AgentID, requestID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询加密响应失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "响应尚未就绪")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) setPreferredDevice(c *gin.Context) {
	var request struct {
		DeviceUID string `json:"device_uid" binding:"required"`
		AgentID   string `json:"agent_id"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	if err := s.db.SetPreferredDevice(currentUserID(c), request.AgentID, request.DeviceUID); err != nil {
		if err == sql.ErrNoRows {
			fail(c, http.StatusNotFound, "设备或 Agent 不存在")
			return
		}
		fail(c, http.StatusInternalServerError, "设置默认设备失败")
		return
	}
	ok(c, "已更新默认设备", nil)
}

func (s *Server) getSettings(c *gin.Context) {
	item, err := s.db.GetSettings(currentUserID(c))
	if err != nil {
		fail(c, http.StatusInternalServerError, "读取设置失败")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) putSettings(c *gin.Context) {
	var request struct {
		NotifyEnabled *bool           `json:"notify_enabled"`
		Locale        *string         `json:"locale"`
		STTPreference *string         `json:"stt_preference"`
		ExtraJSON     json.RawMessage `json:"extra_json"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	item, err := s.db.PutSettings(currentUserID(c), database.SettingsInput{
		NotifyEnabled: request.NotifyEnabled, Locale: request.Locale, STTPreference: request.STTPreference, ExtraJSON: request.ExtraJSON,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "保存设置失败")
		return
	}
	ok(c, "已保存", item)
}

func (s *Server) rotatePair(c *gin.Context) {
	var request struct {
		PairPublicID string `json:"pair_public_id"`
		PairSecret   string `json:"pair_secret"`
	}
	_ = c.ShouldBindJSON(&request)
	item, secret, err := s.db.RotatePair(currentUserID(c), c.Param("device_uid"), request.PairPublicID, request.PairSecret)
	if err != nil {
		fail(c, http.StatusInternalServerError, "轮换配对材料失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在或未激活")
		return
	}
	ok(c, "已轮换配对材料", gin.H{"device": item, "pair_secret": secret})
}

func (s *Server) claimPairing(c *gin.Context) {
	var request struct {
		DeviceUID  string `json:"device_uid" binding:"required"`
		TTLSeconds int    `json:"ttl_seconds"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	item, err := s.db.CreatePairingClaim(currentUserID(c), request.DeviceUID, request.TTLSeconds)
	if err == sql.ErrNoRows {
		fail(c, http.StatusNotFound, "设备不存在或未激活")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "创建配对码失败")
		return
	}
	ok(c, "配对码已生成", item)
}

func (s *Server) confirmPairing(c *gin.Context) {
	var request struct {
		ClaimCode string `json:"claim_code" binding:"required"`
		AgentID   string `json:"agent_id"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	device, claim, err := s.db.ConfirmPairingClaim(c.Request.Context(), currentUserID(c), request.ClaimCode, request.AgentID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "确认配对失败")
		return
	}
	if device == nil || claim == nil {
		fail(c, http.StatusNotFound, "配对码无效、已过期或已使用")
		return
	}
	ok(c, "配对成功", gin.H{"device": device, "claim_code": claim.ClaimCode, "agent_id": claim.ConsumedByAgentID})
}

func validateAgentEncryption(request agentRegisterRequest) string {
	key, keyID, algorithm := strings.TrimSpace(request.PublicKey), strings.TrimSpace(request.KeyID), strings.TrimSpace(request.KeyAlgorithm)
	if key == "" && keyID == "" && algorithm == "" {
		return ""
	}
	if key == "" || keyID == "" || algorithm == "" {
		return "hapi 连接加密参数不完整"
	}
	if algorithm != hapiAlgorithm {
		return "不支持的 hapi 连接加密算法"
	}
	if len(key) > 8192 || !strings.Contains(key, "BEGIN PUBLIC KEY") {
		return "Agent 公钥无效"
	}
	if !validIdentifier(keyID, 8, 128) {
		return "key_id 无效"
	}
	return ""
}

func validateEnvelope(value hapiEnvelope, agent *database.Agent, requestID string) string {
	if value.Version != 2 || value.Operation != "hapi_connection" || value.Algorithm != hapiAlgorithm {
		return "hapi 连接信封版本或算法无效"
	}
	if value.RequestID != requestID || value.KeyID == "" || value.KeyID != agent.KeyID || agent.KeyAlgorithm != hapiAlgorithm || agent.PublicKey == "" {
		return "hapi 连接信封与 Agent 密钥不匹配"
	}
	wantAAD := "echoear-control-v2|" + agent.AgentID + "|hapi_connection||" + requestID + "|" + value.KeyID
	if value.AAD != wantAAD {
		return "hapi 连接信封 AAD 无效"
	}
	if len(value.EncryptedKey) < 32 || len(value.EncryptedKey) > 2048 {
		return "hapi 连接信封密钥无效"
	}
	if decoded, err := base64.StdEncoding.DecodeString(value.Nonce); err != nil || len(decoded) != 12 {
		return "hapi 连接信封 nonce 无效"
	}
	if decoded, err := base64.StdEncoding.DecodeString(value.Ciphertext); err != nil || len(decoded) < 17 || len(decoded) > hapiCiphertextMax {
		return "hapi 连接信封 ciphertext 无效"
	}
	return ""
}

func validateEncryptedBlob(value encryptedBlob) string {
	if value.Version != 1 || value.AAD == "" {
		return "加密响应版本或 AAD 无效"
	}
	if decoded, err := base64.StdEncoding.DecodeString(value.Nonce); err != nil || len(decoded) != 12 {
		return "加密响应 nonce 无效"
	}
	if decoded, err := base64.StdEncoding.DecodeString(value.Ciphertext); err != nil || len(decoded) < 17 || len(decoded) > hapiCiphertextMax {
		return "加密响应 ciphertext 无效"
	}
	return ""
}

func sanitizeCapabilities(raw json.RawMessage) json.RawMessage {
	var input struct {
		Hapi             bool   `json:"hapi"`
		HapiVersion      string `json:"hapi_version"`
		RuntimeReady     bool   `json:"runtime_ready"`
		RuntimeStarting  bool   `json:"runtime_starting"`
		RuntimeFailed    bool   `json:"runtime_failed"`
		CloudTunnelReady bool   `json:"cloud_tunnel_ready"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &input) != nil {
		return json.RawMessage(`{}`)
	}
	version := []rune(strings.TrimSpace(input.HapiVersion))
	if len(version) > 24 {
		version = version[:24]
	}
	value, _ := json.Marshal(gin.H{
		"hapi": input.Hapi, "hapi_version": string(version), "runtime_ready": input.RuntimeReady,
		"runtime_starting": input.RuntimeStarting, "runtime_failed": input.RuntimeFailed,
		"cloud_tunnel_ready": input.CloudTunnelReady,
	})
	return value
}

func validIdentifier(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
