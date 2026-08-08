package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/18345174/echoear_cloud/internal/database"
)

type sharePolicy struct {
	AllowedFlavors     []string            `json:"allowed_flavors"`
	AllowedModels      map[string][]string `json:"allowed_models"`
	WorkspaceRoots     []string            `json:"workspace_roots"`
	TaskPermissions    map[string]bool     `json:"task_permissions"`
	DirectoryBrowse    bool                `json:"directory_browse"`
	Upload             bool                `json:"upload"`
	Download           bool                `json:"download"`
	Terminal           bool                `json:"terminal"`
	ToolApproval       bool                `json:"tool_approval"`
	SettingsChange     bool                `json:"settings_change"`
	ModelChange        bool                `json:"model_change"`
	MaxPermissionMode  string              `json:"max_permission_mode"`
	MaxConcurrentTasks int                 `json:"max_concurrent_tasks"`
	MaxTasksPerDay     int                 `json:"max_tasks_per_day"`
}

func normalizeSharePolicy(raw json.RawMessage) (json.RawMessage, string) {
	var value sharePolicy
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil, "分享权限格式无效"
	}
	allowedModes := map[string]bool{"default": true, "acceptEdits": true, "plan": true, "auto": true, "bypassPermissions": true}
	if !allowedModes[value.MaxPermissionMode] {
		return nil, "最大权限模式无效"
	}
	if value.MaxConcurrentTasks < 1 || value.MaxConcurrentTasks > 20 {
		return nil, "并发任务数须为1到20"
	}
	if value.MaxTasksPerDay < 1 || value.MaxTasksPerDay > 1000 {
		return nil, "每日任务数须为1到1000"
	}
	if len(value.AllowedFlavors) == 0 || len(value.WorkspaceRoots) == 0 {
		return nil, "至少需要一个 AI 类型和工作目录"
	}
	if value.AllowedModels == nil {
		value.AllowedModels = map[string][]string{}
	}
	if value.TaskPermissions == nil {
		value.TaskPermissions = map[string]bool{}
	}
	for _, flavor := range value.AllowedFlavors {
		if strings.TrimSpace(flavor) == "" || len(value.AllowedModels[flavor]) == 0 {
			return nil, "每个 AI 类型必须指定允许的模型"
		}
	}
	for _, root := range value.WorkspaceRoots {
		if strings.TrimSpace(root) == "" || len(root) > 1024 {
			return nil, "工作目录无效"
		}
	}
	normalized, _ := json.Marshal(value)
	return normalized, ""
}

func (s *Server) adminListUsers(c *gin.Context) {
	items, err := s.db.ListUsers(truthy(c.Query("include_deleted")))
	if err != nil {
		fail(c, 500, "查询用户失败")
		return
	}
	ok(c, "ok", gin.H{"users": items})
}

func (s *Server) adminUpdateUser(c *gin.Context) {
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID < 1 {
		fail(c, 400, "user_id 无效")
		return
	}
	var request struct {
		Status string `json:"status"`
		Role   string `json:"role"`
		Reason string `json:"reason"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, 400, "请求参数错误")
		return
	}
	item, err := s.db.UpdateUserLifecycle(c.Request.Context(), currentUserID(c), targetID, strings.TrimSpace(request.Status), strings.TrimSpace(request.Role), request.Reason)
	if errors.Is(err, database.ErrSelfManagement) {
		fail(c, 409, "不能禁用或删除当前管理员账号")
		return
	}
	if errors.Is(err, database.ErrLastActiveAdmin) {
		fail(c, 409, "不能修改最后一个有效管理员")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "用户不存在")
		return
	}
	if err != nil {
		fail(c, 400, "更新用户失败")
		return
	}
	ok(c, "用户已更新", item)
}

func (s *Server) adminDeleteUser(c *gin.Context) {
	targetID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || targetID < 1 {
		fail(c, 400, "user_id 无效")
		return
	}
	var request struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&request)
	item, err := s.db.UpdateUserLifecycle(c.Request.Context(), currentUserID(c), targetID, database.UserStatusDeleted, "", request.Reason)
	if errors.Is(err, database.ErrSelfManagement) {
		fail(c, 409, "不能删除当前管理员账号")
		return
	}
	if errors.Is(err, database.ErrLastActiveAdmin) {
		fail(c, 409, "不能删除最后一个有效管理员")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "用户不存在")
		return
	}
	if err != nil {
		fail(c, 500, "删除用户失败")
		return
	}
	ok(c, "用户已删除", item)
}

func (s *Server) adminListAudit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := s.db.ListAudit(limit)
	if err != nil {
		fail(c, 500, "查询审计记录失败")
		return
	}
	ok(c, "ok", gin.H{"audit": items})
}

func (s *Server) createAgentShare(c *gin.Context) {
	var request struct {
		GranteeUsername string          `json:"grantee_username"`
		ValidFrom       *time.Time      `json:"valid_from"`
		ValidUntil      *time.Time      `json:"valid_until"`
		Policy          json.RawMessage `json:"policy"`
	}
	if c.ShouldBindJSON(&request) != nil || strings.TrimSpace(request.GranteeUsername) == "" {
		fail(c, 400, "请求参数错误")
		return
	}
	policy, message := normalizeSharePolicy(request.Policy)
	if message != "" {
		fail(c, 400, message)
		return
	}
	validFrom := time.Now().UTC()
	if request.ValidFrom != nil {
		validFrom = request.ValidFrom.UTC()
	}
	if request.ValidUntil != nil && !request.ValidUntil.After(validFrom) {
		fail(c, 400, "结束时间必须晚于开始时间")
		return
	}
	item, err := s.db.CreateShare(c.Request.Context(), currentUserID(c), c.Param("agent_id"), request.GranteeUsername, validFrom, request.ValidUntil, policy)
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "Agent 或接收账号不存在")
		return
	}
	if errors.Is(err, database.ErrOpenShareExists) {
		fail(c, 409, "已存在待处理或有效分享")
		return
	}
	if err != nil {
		stage, sqlState, constraint := database.ShareCreateFailureInfo(err)
		errorCode := "share.create." + stage
		if sqlState != "" {
			errorCode += "." + sqlState
		}
		log.Printf("create share failed owner=%d agent=%q grantee=%q stage=%s sqlstate=%s constraint=%s: %v",
			currentUserID(c), c.Param("agent_id"), strings.TrimSpace(request.GranteeUsername), stage, sqlState, constraint, err)
		details, _ := json.Marshal(gin.H{"stage": stage, "sqlstate": sqlState, "constraint": constraint})
		actor := currentUserID(c)
		_ = s.db.AddAudit(database.AuditInput{
			ActorUserID: &actor,
			Action:      "share.create",
			TargetType:  "agent",
			TargetID:    c.Param("agent_id"),
			Outcome:     "failed",
			Reason:      errorCode,
			Details:     details,
			IPAddress:   c.ClientIP(),
			UserAgent:   c.Request.UserAgent(),
		})
		fail(c, 500, fmt.Sprintf("创建分享失败（错误码 %s）", errorCode))
		return
	}
	ok(c, "分享邀请已创建", item)
}

func (s *Server) listAgentShares(c *gin.Context) {
	admin := truthy(c.Query("all")) && currentSession(c).Role == database.RoleAdmin
	items, err := s.db.ListShares(currentUserID(c), admin)
	if err != nil {
		fail(c, 500, "查询分享失败")
		return
	}
	ok(c, "ok", gin.H{"shares": items})
}

func (s *Server) updateAgentShare(c *gin.Context) {
	var request struct {
		ValidFrom  *time.Time      `json:"valid_from"`
		ValidUntil *time.Time      `json:"valid_until"`
		Policy     json.RawMessage `json:"policy"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, 400, "请求参数错误")
		return
	}
	policy, message := normalizeSharePolicy(request.Policy)
	if message != "" {
		fail(c, 400, message)
		return
	}
	item, err := s.db.TransitionShare(c.Request.Context(), currentUserID(c), c.Param("share_id"), "update", "", policy, request.ValidFrom, request.ValidUntil, false)
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "分享不存在或无权修改")
		return
	}
	if err != nil {
		fail(c, 500, "更新分享失败")
		return
	}
	ok(c, "分享权限已更新", item)
}

func (s *Server) acceptAgentShare(c *gin.Context)  { s.transitionAgentShare(c, "accept") }
func (s *Server) declineAgentShare(c *gin.Context) { s.transitionAgentShare(c, "decline") }
func (s *Server) revokeAgentShare(c *gin.Context)  { s.transitionAgentShare(c, "revoke") }
func (s *Server) transitionAgentShare(c *gin.Context, action string) {
	var request struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&request)
	admin := action == "revoke" && currentSession(c).Role == database.RoleAdmin
	item, err := s.db.TransitionShare(c.Request.Context(), currentUserID(c), c.Param("share_id"), action, request.Reason, nil, nil, nil, admin)
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 404, "分享不存在、状态已变化或无权操作")
		return
	}
	if err != nil {
		fail(c, 500, "更新分享状态失败")
		return
	}
	ok(c, "分享状态已更新", item)
}

func (s *Server) accessTicketKey(c *gin.Context) { ok(c, "ok", s.tickets.keyDocument()) }

func (s *Server) issueAccessTicket(c *gin.Context, access *database.AgentAccess, requestID string) (string, *accessTicketClaims, error) {
	ticketID, namespace, expiresAt, err := s.db.CreateAccessLease(currentUserID(c), access, 5*time.Minute, requestID)
	if err != nil {
		return "", nil, err
	}
	owner := access.AccessType == "owner"
	policy := access.Policy
	if owner {
		policy = json.RawMessage(`{"owner":true}`)
	}
	claims := makeTicketClaims(ticketID, requestID, currentUserID(c), access.UserID, access.AgentID, access.PublicID, access.ShareID, access.PolicyVersion, namespace, owner, policy, expiresAt)
	ticket, err := s.tickets.sign(claims)
	if err != nil {
		_ = s.db.RevokeAccessLease(ticketID)
		return "", nil, err
	}
	actor := currentUserID(c)
	agentID := access.ID
	ownerID := access.UserID
	var shareID *string
	if access.ShareID != "" {
		shareID = &access.ShareID
	}
	_ = s.db.AddAudit(database.AuditInput{ActorUserID: &actor, Action: "agent.ticket.issue", TargetType: "agent", TargetID: access.PublicID, OwnerUserID: &ownerID, AgentID: &agentID, ShareID: shareID, IPAddress: c.ClientIP(), UserAgent: c.Request.UserAgent()})
	return ticket, &claims, nil
}

func (s *Server) createAccessTicket(c *gin.Context) {
	var request struct {
		RequestID string `json:"request_id"`
	}
	if c.Request.ContentLength > 0 && c.ShouldBindJSON(&request) != nil {
		fail(c, 400, "访问票据参数错误")
		return
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if !validIdentifier(request.RequestID, 8, 128) {
		fail(c, 400, "request_id 无效")
		return
	}
	access, err := s.db.ResolveAgentAccess(currentUserID(c), c.Param("agent_id"))
	if err != nil {
		fail(c, 500, "访问校验失败")
		return
	}
	if access == nil {
		fail(c, 404, "Agent 不存在或没有访问权限")
		return
	}
	ticket, claims, err := s.issueAccessTicket(c, access, request.RequestID)
	if err != nil {
		fail(c, 429, "访问限制已生效: "+err.Error())
		return
	}
	ok(c, "ok", gin.H{"access_ticket": ticket, "claims": claims, "verification_key": s.tickets.keyDocument()})
}

func (s *Server) recordAgentAccessUsage(c *gin.Context) {
	var request struct {
		RequestID       string `json:"request_id"`
		EventID         string `json:"event_id"`
		TasksCreated    int    `json:"tasks_created"`
		BytesUploaded   int64  `json:"bytes_uploaded"`
		BytesDownloaded int64  `json:"bytes_downloaded"`
	}
	if c.ShouldBindJSON(&request) != nil || !validIdentifier(request.RequestID, 8, 128) || !validIdentifier(request.EventID, 8, 128) ||
		request.TasksCreated < 0 || request.TasksCreated > 1 || request.BytesUploaded < 0 || request.BytesDownloaded < 0 ||
		(request.TasksCreated == 0 && request.BytesUploaded == 0 && request.BytesDownloaded == 0) {
		fail(c, 400, "共享使用量参数错误")
		return
	}
	usage, err := s.db.RecordShareUsage(c.Request.Context(), currentUserID(c), c.Param("agent_id"), request.RequestID,
		request.EventID, request.TasksCreated, request.BytesUploaded, request.BytesDownloaded)
	if errors.Is(err, database.ErrShareDailyLimit) {
		fail(c, 429, "共享 Agent 今日任务额度已用完")
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		fail(c, 403, "共享访问租约无效")
		return
	}
	if err != nil {
		fail(c, 500, "记录共享使用量失败")
		return
	}
	ok(c, "ok", usage)
}
