package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	gatewayMaxBody       = 72 << 20
	gatewayInlineBody    = 6 << 20
	gatewayRequestChunk  = 192 << 10
	gatewayResponseWait  = 40 * time.Second
	gatewaySessionPeriod = 25 * time.Second
)

var gatewayHopHeaders = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {},
	"proxy-authorization": {}, "te": {}, "trailer": {},
	"transfer-encoding": {}, "upgrade": {}, "content-length": {},
	"x-echoear-session": {},
}

type gatewayRequest struct {
	key           tunnelKey
	connectionID  string
	requestID     string
	sessionID     string
	subjectUserID int64
	accessID      string
	frames        chan gatewayFrame
	closed        chan struct{}
	closeOnce     sync.Once
}

type gatewayFrame struct {
	Type          string            `json:"type"`
	ID            string            `json:"id,omitempty"`
	Method        string            `json:"method,omitempty"`
	Path          string            `json:"path,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Body          string            `json:"body,omitempty"`
	ContentLength int               `json:"content_length,omitempty"`
	Status        int               `json:"status,omitempty"`
	Message       string            `json:"message,omitempty"`
}

func (s *Server) hapiGateway(c *gin.Context) {
	userID := currentUserID(c)
	accessID := strings.TrimSpace(c.Param("agent_id"))
	requestID := strings.TrimSpace(c.Param("request_id"))
	path := c.Param("path")
	if !validIdentifier(accessID, 1, 128) || !validIdentifier(requestID, 8, 128) ||
		!strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 8192 {
		fail(c, http.StatusBadRequest, "HAPI 网关参数无效")
		return
	}
	access, err := s.db.ResolveAgentAccess(userID, accessID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "Agent 校验失败")
		return
	}
	if access == nil || !s.db.AccessRequestValid(userID, access, requestID) {
		fail(c, http.StatusForbidden, "Agent 访问票据无效或已过期")
		return
	}
	key := tunnelKey{userID: access.UserID, agentID: access.AgentID}
	gateway := &gatewayRequest{
		key: key, connectionID: randomTunnelID(), requestID: requestID,
		sessionID: c.GetString("session_id"), subjectUserID: userID, accessID: accessID,
		frames: make(chan gatewayFrame, 32), closed: make(chan struct{}),
	}
	if !s.tunnel.attachGateway(gateway) {
		fail(c, http.StatusServiceUnavailable, "电脑云网关当前离线")
		return
	}
	defer s.tunnel.detachGateway(gateway)

	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, gatewayMaxBody+1))
	if err != nil || len(body) > gatewayMaxBody {
		fail(c, http.StatusRequestEntityTooLarge, "HAPI 请求内容过大")
		return
	}
	requestPath := path
	if c.Request.URL.RawQuery != "" {
		requestPath += "?" + c.Request.URL.RawQuery
	}
	frame := gatewayFrame{
		Type: "request", ID: gateway.connectionID, Method: c.Request.Method,
		Path: requestPath, Headers: gatewayRequestHeaders(c.Request.Header),
	}
	if len(body) <= gatewayInlineBody {
		frame.Body = base64.StdEncoding.EncodeToString(body)
		if !s.tunnel.sendGatewayFrame(gateway, frame) {
			fail(c, http.StatusServiceUnavailable, "电脑云网关当前离线")
			return
		}
	} else {
		frame.Type = "request_start"
		frame.ContentLength = len(body)
		if !s.tunnel.sendGatewayFrame(gateway, frame) {
			fail(c, http.StatusServiceUnavailable, "电脑云网关当前离线")
			return
		}
		for offset := 0; offset < len(body); offset += gatewayRequestChunk {
			end := min(offset+gatewayRequestChunk, len(body))
			if !s.tunnel.sendGatewayFrame(gateway, gatewayFrame{
				Type: "request_chunk", ID: gateway.connectionID,
				Body: base64.StdEncoding.EncodeToString(body[offset:end]),
			}) {
				fail(c, http.StatusServiceUnavailable, "电脑云网关当前离线")
				return
			}
		}
		if !s.tunnel.sendGatewayFrame(gateway, gatewayFrame{Type: "request_end", ID: gateway.connectionID}) {
			fail(c, http.StatusServiceUnavailable, "电脑云网关当前离线")
			return
		}
	}
	s.streamGatewayResponse(c, gateway)
}

func (s *Server) streamGatewayResponse(c *gin.Context, gateway *gatewayRequest) {
	startTimer := time.NewTimer(gatewayResponseWait)
	defer startTimer.Stop()
	startTimeout := startTimer.C
	authorizationTicker := time.NewTicker(gatewaySessionPeriod)
	defer authorizationTicker.Stop()
	started := false
	for {
		select {
		case frame := <-gateway.frames:
			if frame.ID != "" && frame.ID != gateway.connectionID {
				continue
			}
			switch frame.Type {
			case "response_start":
				if started {
					continue
				}
				started = true
				startTimer.Stop()
				startTimeout = nil
				copyGatewayResponseHeaders(c.Writer.Header(), frame.Headers)
				status := frame.Status
				if status < 100 || status > 599 {
					status = http.StatusBadGateway
				}
				c.Status(status)
				c.Writer.WriteHeaderNow()
			case "response_chunk":
				if !started {
					continue
				}
				chunk, err := base64.StdEncoding.DecodeString(frame.Body)
				if err != nil {
					return
				}
				if _, err := c.Writer.Write(chunk); err != nil {
					return
				}
				c.Writer.Flush()
			case "response_end":
				if !started {
					c.Status(http.StatusNoContent)
				}
				return
			case "response_error":
				if !started {
					fail(c, http.StatusBadGateway, strings.TrimSpace(frame.Message))
				}
				return
			}
		case <-authorizationTicker.C:
			if !s.tunnel.gatewayAuthorized(gateway) {
				if !started {
					fail(c, http.StatusUnauthorized, "HAPI 网关会话已失效")
				}
				return
			}
		case <-startTimeout:
			if !started {
				fail(c, http.StatusGatewayTimeout, "电脑未及时响应 HAPI 请求")
			}
			return
		case <-gateway.closed:
			if !started {
				fail(c, http.StatusServiceUnavailable, "电脑云网关连接已断开")
			}
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}

func gatewayRequestHeaders(headers http.Header) map[string]string {
	result := make(map[string]string)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if _, excluded := gatewayHopHeaders[lower]; excluded {
			continue
		}
		value := strings.Join(values, ", ")
		if len(value) <= 16*1024 {
			result[name] = value
		}
	}
	return result
}

func copyGatewayResponseHeaders(target http.Header, headers map[string]string) {
	for name, value := range headers {
		lower := strings.ToLower(name)
		if _, excluded := gatewayHopHeaders[lower]; excluded || strings.HasPrefix(lower, "access-control-") || len(value) > 16*1024 {
			continue
		}
		target.Set(name, value)
	}
}

func (t *hapiTunnel) attachGateway(gateway *gatewayRequest) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	agent := t.agents[gateway.key]
	if agent == nil || !agent.gateway {
		return false
	}
	if t.gateways[gateway.key] == nil {
		t.gateways[gateway.key] = make(map[string]*gatewayRequest)
	}
	if len(t.gateways[gateway.key]) >= 128 {
		return false
	}
	t.gateways[gateway.key][gateway.connectionID] = gateway
	return true
}

func (t *hapiTunnel) detachGateway(gateway *gatewayRequest) {
	t.mu.Lock()
	if requests := t.gateways[gateway.key]; requests != nil {
		delete(requests, gateway.connectionID)
		if len(requests) == 0 {
			delete(t.gateways, gateway.key)
		}
	}
	agent := t.agents[gateway.key]
	t.mu.Unlock()
	gateway.close()
	if agent != nil {
		t.sendControl(agent, tunnelMessage{
			Type: "disconnect", ConnectionID: gateway.connectionID, RequestID: gateway.requestID,
		})
	}
}

func (t *hapiTunnel) sendGatewayFrame(gateway *gatewayRequest, frame gatewayFrame) bool {
	payload, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	agent := t.agent(gateway.key)
	if agent == nil {
		return false
	}
	return t.sendControl(agent, tunnelMessage{
		Type: "gateway_data", ConnectionID: gateway.connectionID,
		RequestID: gateway.requestID, Payload: payload,
		CloudReceivedAt: time.Now().UnixMilli(), CloudForwardedAt: time.Now().UnixMilli(),
	})
}

func (t *hapiTunnel) deliverGateway(key tunnelKey, connectionID string, payload json.RawMessage) {
	var frame gatewayFrame
	if json.Unmarshal(payload, &frame) != nil {
		return
	}
	t.mu.RLock()
	gateway := t.gateways[key][connectionID]
	t.mu.RUnlock()
	if gateway == nil {
		return
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case gateway.frames <- frame:
	case <-gateway.closed:
	case <-timer.C:
		gateway.close()
	}
}

func (t *hapiTunnel) gatewayAuthorized(gateway *gatewayRequest) bool {
	session, err := t.db.SessionByToken(gateway.sessionID)
	if err != nil || session == nil || session.UserID != gateway.subjectUserID {
		return false
	}
	access, err := t.db.ResolveAgentAccess(gateway.subjectUserID, gateway.accessID)
	return err == nil && access != nil && access.UserID == gateway.key.userID &&
		access.AgentID == gateway.key.agentID &&
		t.db.AccessRequestValid(gateway.subjectUserID, access, gateway.requestID)
}

func (gateway *gatewayRequest) close() {
	gateway.closeOnce.Do(func() { close(gateway.closed) })
}
