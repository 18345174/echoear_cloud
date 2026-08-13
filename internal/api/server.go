package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"github.com/18345174/echoear_cloud/internal/config"
	"github.com/18345174/echoear_cloud/internal/database"
)

const (
	hapiAlgorithm      = "RSA-OAEP-256+A256GCM"
	hapiBodyMax        = 32 * 1024
	hapiCiphertextMax  = 16 * 1024
	apiContractVersion = 15
)

type Server struct {
	db      *database.DB
	config  config.Config
	router  *gin.Engine
	tunnel  *hapiTunnel
	tickets *accessTicketSigner
}

func NewServer(db *database.DB, cfg config.Config) *Server {
	s := &Server{db: db, config: cfg, tunnel: newHapiTunnel(db), tickets: newAccessTicketSigner(cfg.AccessTicketSigningKey)}
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(gin.Logger())
	corsConfig := cors.Config{
		AllowOrigins:     cfg.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "Accept", "X-EchoEar-Session"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: !contains(cfg.AllowedOrigins, "*"),
		MaxAge:           12 * time.Hour,
	}
	router.Use(cors.New(corsConfig))
	router.GET("/healthz", s.health)
	router.GET("/api/v1/health", s.health)

	api := router.Group("/api/v1")
	auth := api.Group("/auth")
	auth.POST("/login", s.login)
	auth.Use(s.requireSession())
	auth.POST("/logout", s.logout)
	auth.GET("/me", s.me)
	auth.PUT("/password", s.changePassword)
	auth.POST("/register", s.requireAdmin(), s.register)

	admin := api.Group("/admin")
	admin.Use(s.requireSession(), s.requireAdmin())
	admin.GET("/users", s.adminListUsers)
	admin.PATCH("/users/:user_id", s.adminUpdateUser)
	admin.DELETE("/users/:user_id", s.adminDeleteUser)
	admin.GET("/audit", s.adminListAudit)

	echoear := api.Group("/echoear")
	echoear.POST("/devices/token", s.deviceToken)
	echoear.POST("/devices/presence", s.devicePresence)
	echoear.Use(s.requireSession())
	echoear.POST("/devices/bind", s.bindDevice)
	echoear.GET("/devices", s.listDevices)
	echoear.GET("/devices/active", s.activeDevices)
	echoear.GET("/devices/pending-reset", s.pendingReset)
	echoear.GET("/devices/:device_uid", s.getDevice)
	echoear.PATCH("/devices/:device_uid", s.patchDevice)
	echoear.POST("/devices/:device_uid/heartbeat", s.heartbeatDevice)
	echoear.POST("/devices/:device_uid/revoke", s.revokeDevice)
	echoear.POST("/devices/:device_uid/pending-reset/ack", s.ackPendingReset)
	echoear.POST("/devices/:device_uid/rotate-pair", s.rotatePair)
	echoear.POST("/agents/register", s.registerAgent)
	echoear.GET("/agents", s.listAgents)
	echoear.GET("/access-ticket-key", s.accessTicketKey)
	echoear.POST("/agents/:agent_id/access-ticket", s.createAccessTicket)
	echoear.POST("/agents/:agent_id/access-usage", s.recordAgentAccessUsage)
	echoear.POST("/agents/:agent_id/shares", s.createAgentShare)
	echoear.GET("/shares", s.listAgentShares)
	echoear.PATCH("/shares/:share_id", s.updateAgentShare)
	echoear.POST("/shares/:share_id/accept", s.acceptAgentShare)
	echoear.POST("/shares/:share_id/decline", s.declineAgentShare)
	echoear.POST("/shares/:share_id/revoke", s.revokeAgentShare)
	echoear.POST("/agents/:agent_id/hapi/connection", s.requestHapiConnection)
	echoear.GET("/agents/me/hapi/commands", s.claimHapiCommands)
	echoear.POST("/agents/me/hapi/commands/:command_id/ack", s.ackHapiCommand)
	echoear.GET("/agents/:agent_id/hapi/responses/:request_id", s.getHapiResponse)
	echoear.PUT("/agents/:agent_id/hapi/responses/:request_id", s.putHapiResponse)
	echoear.GET("/agents/:agent_id/hapi/tunnel", s.hapiTunnel)
	echoear.PUT("/agents/me/preferred-device", s.setPreferredDevice)
	echoear.GET("/settings", s.getSettings)
	echoear.PUT("/settings", s.putSettings)
	echoear.POST("/pairing/claim", s.claimPairing)
	echoear.POST("/pairing/confirm", s.confirmPairing)

	hapiGateway := api.Group("/echoear/agents/:agent_id/hapi/gateway/:request_id")
	hapiGateway.Use(s.requireGatewaySession())
	hapiGateway.Any("/*path", s.hapiGateway)
	s.router = router
	return s
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) health(c *gin.Context) {
	if err := s.db.PingContext(c.Request.Context()); err != nil {
		fail(c, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	ok(c, "ok", gin.H{"service": "echoear_cloud", "contract_version": apiContractVersion})
}

func (s *Server) requireSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		parts := strings.SplitN(strings.TrimSpace(c.GetHeader("Authorization")), " ", 2)
		if len(parts) != 2 || parts[0] != "Session" || strings.TrimSpace(parts[1]) == "" {
			fail(c, http.StatusUnauthorized, "未提供有效会话")
			c.Abort()
			return
		}
		s.requireSessionToken(c, strings.TrimSpace(parts[1]))
	}
}

func (s *Server) requireGatewaySession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimSpace(c.GetHeader("X-EchoEar-Session"))
		if token == "" {
			fail(c, http.StatusUnauthorized, "未提供有效会话")
			c.Abort()
			return
		}
		s.requireSessionToken(c, token)
	}
}

func (s *Server) requireSessionToken(c *gin.Context, token string) {
	session, err := s.db.SessionByToken(token)
	if err != nil {
		fail(c, http.StatusInternalServerError, "会话校验失败")
		c.Abort()
		return
	}
	if session == nil {
		fail(c, http.StatusUnauthorized, "会话无效或已过期")
		c.Abort()
		return
	}
	if time.Since(session.LastSeenAt) >= 5*time.Minute {
		_ = s.db.TouchSession(token)
	}
	c.Set("session", session)
	c.Set("session_id", token)
	c.Set("user_id", session.UserID)
	c.Next()
}

func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := currentSession(c)
		if session == nil {
			fail(c, http.StatusUnauthorized, "未提供有效会话")
			c.Abort()
			return
		}
		if session.Role != database.RoleAdmin {
			fail(c, http.StatusForbidden, "仅管理员可以注册账号")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) login(c *gin.Context) {
	var request struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	var user struct {
		ID                                   int64
		Username, PasswordHash, Role, Status string
		PasswordChanged                      bool
	}
	err := s.db.QueryRow(`SELECT id,username,password_hash,role,status,password_changed FROM users WHERE LOWER(username)=LOWER($1)`, strings.TrimSpace(request.Username)).
		Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Status, &user.PasswordChanged)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil) {
		fail(c, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "服务器错误")
		return
	}
	if user.Status != database.UserStatusActive {
		fail(c, http.StatusForbidden, "账号已被禁用或删除")
		return
	}
	token, expiresAt, err := s.db.CreateSession(user.ID, c.ClientIP(), c.Request.UserAgent(), s.config.SessionTTL)
	if err != nil {
		fail(c, http.StatusInternalServerError, "创建登录会话失败")
		return
	}
	_, _ = s.db.Exec(`UPDATE users SET last_login_at=NOW(),updated_at=NOW() WHERE id=$1`, user.ID)
	ok(c, "登录成功", gin.H{
		"user_id": user.ID, "session_id": token, "session_expires_at": expiresAt,
		"username": user.Username, "role": user.Role, "status": user.Status,
		"password_changed": boolInt(user.PasswordChanged), "feature_codes": featureCodes(user.Role),
	})
}

func (s *Server) logout(c *gin.Context) {
	_ = s.db.RevokeSession(c.GetString("session_id"))
	ok(c, "登出成功", nil)
}

func (s *Server) me(c *gin.Context) {
	session := currentSession(c)
	var email string
	var passwordChanged bool
	var createdAt, updatedAt time.Time
	var lastLoginAt sql.NullTime
	if err := s.db.QueryRow(`SELECT email,password_changed,created_at,updated_at,last_login_at FROM users WHERE id=$1`, session.UserID).
		Scan(&email, &passwordChanged, &createdAt, &updatedAt, &lastLoginAt); err != nil {
		fail(c, http.StatusInternalServerError, "读取用户信息失败")
		return
	}
	data := gin.H{
		"user_id": session.UserID, "username": session.Username, "role": session.Role, "status": session.Status,
		"email": email, "password_changed": boolInt(passwordChanged), "created_at": createdAt,
		"updated_at": updatedAt, "session_expires_at": session.ExpiresAt,
		"feature_codes": featureCodes(session.Role),
	}
	if lastLoginAt.Valid {
		data["last_login_at"] = lastLoginAt.Time
	} else {
		data["last_login_at"] = ""
	}
	ok(c, "success", data)
}

func (s *Server) changePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if c.ShouldBindJSON(&request) != nil || strings.TrimSpace(request.OldPassword) == "" || len(strings.TrimSpace(request.NewPassword)) < 6 {
		fail(c, http.StatusBadRequest, "原密码不能为空，新密码至少需要6位")
		return
	}
	userID := currentUserID(c)
	var currentHash string
	if err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&currentHash); err != nil {
		fail(c, http.StatusInternalServerError, "读取用户信息失败")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(currentHash), []byte(request.OldPassword)) != nil {
		fail(c, http.StatusBadRequest, "原密码错误")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(request.NewPassword)), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash=$2,password_changed=true,updated_at=NOW() WHERE id=$1`, userID, string(hash)); err != nil {
		fail(c, http.StatusInternalServerError, "修改密码失败")
		return
	}
	ok(c, "success", nil)
}

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Role     string `json:"role"`
}

type registerInput struct {
	Username string
	Password string
	Email    string
	Role     string
}

func normalizeRegistration(request registerRequest) (registerInput, string) {
	username := strings.TrimSpace(request.Username)
	usernameLength := len([]rune(username))
	if usernameLength < 3 || usernameLength > 64 {
		return registerInput{}, "用户名长度须为3到64个字符"
	}
	if len(request.Password) < 6 || len(request.Password) > 72 || strings.TrimSpace(request.Password) == "" {
		return registerInput{}, "密码长度须为6到72个字节"
	}
	email := strings.TrimSpace(request.Email)
	if len([]rune(email)) > 254 {
		return registerInput{}, "邮箱长度不能超过254个字符"
	}
	role := strings.ToLower(strings.TrimSpace(request.Role))
	if role == "" {
		role = database.RoleUser
	}
	if role != database.RoleUser && role != database.RoleAdmin {
		return registerInput{}, "角色须为user或admin"
	}
	return registerInput{Username: username, Password: request.Password, Email: email, Role: role}, ""
}

func (s *Server) register(c *gin.Context) {
	var request registerRequest
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	input, validationMessage := normalizeRegistration(request)
	if validationMessage != "" {
		fail(c, http.StatusBadRequest, validationMessage)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	user, err := s.db.CreateUser(c.Request.Context(), database.CreateUserInput{
		Username: input.Username, PasswordHash: string(hash), Email: input.Email, Role: input.Role,
	})
	if errors.Is(err, database.ErrUsernameExists) {
		fail(c, http.StatusConflict, "用户名已存在")
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "注册账号失败")
		return
	}
	ok(c, "注册成功", gin.H{
		"user_id": user.ID, "username": user.Username, "email": user.Email, "role": user.Role,
		"password_changed": boolInt(user.PasswordChanged), "created_at": user.CreatedAt, "updated_at": user.UpdatedAt,
	})
}

func featureCodes(role string) []string {
	codes := []string{"echoear.use", "echoear.agent.share"}
	if role == database.RoleAdmin {
		codes = append(codes, "echoear.account.register", "echoear.account.manage", "echoear.agent.share.audit")
	}
	return codes
}

type bindRequest struct {
	DeviceUID    string          `json:"device_uid" binding:"required"`
	DisplayName  string          `json:"display_name"`
	Hostname     string          `json:"hostname"`
	FWVersion    string          `json:"fw_version"`
	LastIP       string          `json:"last_ip"`
	PairPublicID string          `json:"pair_public_id"`
	PairSecret   string          `json:"pair_secret"`
	Capabilities json.RawMessage `json:"capabilities"`
	LanHint      json.RawMessage `json:"lan_hint"`
}

func (s *Server) bindDevice(c *gin.Context) {
	var request bindRequest
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	item, conflict, err := s.db.BindDevice(c.Request.Context(), currentUserID(c), database.DeviceInput{
		DeviceUID: request.DeviceUID, DisplayName: request.DisplayName, Hostname: request.Hostname,
		FWVersion: request.FWVersion, LastIP: request.LastIP, PairPublicID: request.PairPublicID,
		PairSecret: request.PairSecret, Capabilities: request.Capabilities, LanHint: request.LanHint,
	})
	if conflict {
		fail(c, http.StatusConflict, "设备已被其他用户绑定")
		return
	}
	if err != nil {
		if strings.Contains(err.Error(), "last_ip") || strings.Contains(err.Error(), "required") {
			fail(c, http.StatusBadRequest, err.Error())
			return
		}
		fail(c, http.StatusInternalServerError, "绑定设备失败")
		return
	}
	tokens, err := s.db.IssueDeviceTokens(currentUserID(c), request.DeviceUID)
	if err != nil || tokens == nil {
		fail(c, http.StatusInternalServerError, "绑定成功但签发设备凭证失败")
		return
	}
	ok(c, "绑定成功", gin.H{
		"device": item, "refresh_token": tokens.RefreshToken, "refresh_expires_at": tokens.RefreshExpiresAt,
		"access_token": tokens.AccessToken, "access_expires_at": tokens.AccessExpiresAt,
		"access_expires_in": tokens.AccessExpiresIn, "token_type": tokens.TokenType,
		"cloud_base_url": s.config.PublicBaseURL,
	})
}

func (s *Server) listDevices(c *gin.Context) {
	items, err := s.db.ListDevices(currentUserID(c), c.Query("bind_state"), truthy(c.Query("include_revoked")))
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询设备失败")
		return
	}
	ok(c, "ok", gin.H{"devices": items})
}

func (s *Server) activeDevices(c *gin.Context) {
	items, err := s.db.ListDevices(currentUserID(c), "active", false)
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询可用设备失败")
		return
	}
	preferred, err := s.db.PreferredDeviceUID(currentUserID(c), c.Query("agent_id"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询默认设备失败")
		return
	}
	var preferredValue any
	if preferred != "" {
		preferredValue = preferred
	}
	ok(c, "ok", gin.H{"preferred_device_uid": preferredValue, "devices": items})
}

func (s *Server) getDevice(c *gin.Context) {
	item, err := s.db.DeviceForUser(currentUserID(c), c.Param("device_uid"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询设备失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) patchDevice(c *gin.Context) {
	var request struct {
		DisplayName  *string         `json:"display_name"`
		Hostname     *string         `json:"hostname"`
		FWVersion    *string         `json:"fw_version"`
		Capabilities json.RawMessage `json:"capabilities"`
		LanHint      json.RawMessage `json:"lan_hint"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	item, err := s.db.PatchDevice(currentUserID(c), c.Param("device_uid"), database.DevicePatch{
		DisplayName: request.DisplayName, Hostname: request.Hostname, FWVersion: request.FWVersion,
		Capabilities: request.Capabilities, LanHint: request.LanHint,
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "更新设备失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在")
		return
	}
	ok(c, "更新成功", item)
}

func (s *Server) heartbeatDevice(c *gin.Context) {
	request, valid := heartbeatRequest(c)
	if !valid {
		return
	}
	item, err := s.db.HeartbeatDevice(currentUserID(c), c.Param("device_uid"), request)
	if err != nil {
		if strings.Contains(err.Error(), "last_ip") {
			fail(c, http.StatusBadRequest, "last_ip 无效")
			return
		}
		fail(c, http.StatusInternalServerError, "心跳更新失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在或未激活")
		return
	}
	ok(c, "ok", item)
}

func (s *Server) deviceToken(c *gin.Context) {
	var request struct {
		DeviceUID    string `json:"device_uid" binding:"required"`
		RefreshToken string `json:"refresh_token" binding:"required"`
	}
	if c.ShouldBindJSON(&request) != nil {
		fail(c, http.StatusBadRequest, "请求参数错误")
		return
	}
	tokens, err := s.db.ExchangeRefresh(request.DeviceUID, request.RefreshToken)
	if err != nil {
		status := http.StatusUnauthorized
		message := "refresh_token 无效"
		if strings.Contains(err.Error(), "required") {
			status = http.StatusBadRequest
			message = err.Error()
		}
		if strings.Contains(err.Error(), "expired") {
			message = "refresh_token 已过期"
		}
		fail(c, status, message)
		return
	}
	ok(c, "ok", tokens)
}

func (s *Server) devicePresence(c *gin.Context) {
	parts := strings.SplitN(strings.TrimSpace(c.GetHeader("Authorization")), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		fail(c, http.StatusUnauthorized, "需要 Bearer access_token")
		return
	}
	request, valid := heartbeatRequest(c)
	if !valid {
		return
	}
	item, err := s.db.PresenceByAccess(strings.TrimSpace(parts[1]), request)
	if err != nil {
		if strings.Contains(err.Error(), "last_ip") {
			fail(c, http.StatusBadRequest, "last_ip 无效")
			return
		}
		fail(c, http.StatusInternalServerError, "上报失败")
		return
	}
	if item == nil {
		fail(c, http.StatusUnauthorized, "access_token 无效或已过期")
		return
	}
	ok(c, "ok", gin.H{"bound": true, "device_uid": item.DeviceUID})
}

func (s *Server) revokeDevice(c *gin.Context) {
	var request struct {
		Pending *bool `json:"pending_factory_reset"`
	}
	_ = c.ShouldBindJSON(&request)
	pending := true
	if request.Pending != nil {
		pending = *request.Pending
	}
	item, err := s.db.RevokeDevice(currentUserID(c), c.Param("device_uid"), pending)
	if err != nil {
		fail(c, http.StatusInternalServerError, "解绑失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在或已解绑")
		return
	}
	ok(c, "已解绑", item)
}

func (s *Server) pendingReset(c *gin.Context) {
	items, err := s.db.ListPendingReset(currentUserID(c))
	if err != nil {
		fail(c, http.StatusInternalServerError, "查询失败")
		return
	}
	ok(c, "ok", gin.H{"devices": items})
}

func (s *Server) ackPendingReset(c *gin.Context) {
	item, err := s.db.AckPendingReset(currentUserID(c), c.Param("device_uid"))
	if err != nil {
		fail(c, http.StatusInternalServerError, "确认失败")
		return
	}
	if item == nil {
		fail(c, http.StatusNotFound, "设备不存在")
		return
	}
	ok(c, "ok", item)
}
