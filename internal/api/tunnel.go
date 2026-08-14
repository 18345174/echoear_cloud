package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/18345174/echoear_cloud/internal/database"
)

const (
	tunnelMaxMessage = 24 << 20
	tunnelWriteWait  = 10 * time.Second
	tunnelPongWait   = 70 * time.Second
	tunnelPingPeriod = 25 * time.Second
)

type tunnelKey struct {
	userID  int64
	agentID string
}

type tunnelPeer struct {
	conn          *websocket.Conn
	key           tunnelKey
	role          string
	connectionID  string
	requestID     string
	sessionID     string
	subjectUserID int64
	accessID      string
	gateway       bool
	discovery     bool
	send          chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
}

type hapiTunnel struct {
	mu          sync.RWMutex
	db          *database.DB
	agents      map[tunnelKey]*tunnelPeer
	mobiles     map[tunnelKey]map[string]*tunnelPeer
	gateways    map[tunnelKey]map[string]*gatewayRequest
	discoveries map[tunnelKey]map[string]chan discoveryResult
}

type discoveryResult struct {
	payload json.RawMessage
	err     error
}

var (
	errDiscoveryUnsupported = errors.New("agent does not support realtime discovery")
	errDiscoveryUnavailable = errors.New("agent realtime discovery unavailable")
)

type tunnelMessage struct {
	Type              string          `json:"type"`
	ConnectionID      string          `json:"connection_id,omitempty"`
	RequestID         string          `json:"request_id,omitempty"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	TraceID           string          `json:"trace_id,omitempty"`
	PhoneTunnelSentAt int64           `json:"phone_tunnel_sent_at,omitempty"`
	CloudReceivedAt   int64           `json:"cloud_received_at,omitempty"`
	CloudForwardedAt  int64           `json:"cloud_forwarded_at,omitempty"`
}

func newHapiTunnel(db *database.DB) *hapiTunnel {
	return &hapiTunnel{
		db: db, agents: make(map[tunnelKey]*tunnelPeer),
		mobiles:     make(map[tunnelKey]map[string]*tunnelPeer),
		gateways:    make(map[tunnelKey]map[string]*gatewayRequest),
		discoveries: make(map[tunnelKey]map[string]chan discoveryResult),
	}
}

var tunnelUpgrader = websocket.Upgrader{
	ReadBufferSize: 16 * 1024, WriteBufferSize: 16 * 1024,
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) hapiTunnel(c *gin.Context) {
	userID, agentID := currentUserID(c), strings.TrimSpace(c.Param("agent_id"))
	if !validIdentifier(agentID, 1, 128) {
		fail(c, http.StatusBadRequest, "agent_id 无效")
		return
	}
	role, requestID := strings.ToLower(strings.TrimSpace(c.Query("role"))), strings.TrimSpace(c.Query("request_id"))
	if role != "agent" && role != "mobile" {
		fail(c, http.StatusBadRequest, "role 须为 agent 或 mobile")
		return
	}
	if role == "mobile" && !validIdentifier(requestID, 8, 128) {
		fail(c, http.StatusBadRequest, "request_id 无效")
		return
	}
	resourceUserID, resourceAgentID, accessID := userID, agentID, agentID
	if role == "agent" {
		agent, err := s.db.AgentForUser(userID, agentID)
		if err != nil {
			fail(c, http.StatusInternalServerError, "Agent 校验失败")
			return
		}
		if agent == nil {
			fail(c, http.StatusNotFound, "Agent 不存在")
			return
		}
	} else {
		access, err := s.db.ResolveAgentAccess(userID, agentID)
		if err != nil {
			fail(c, http.StatusInternalServerError, "Agent 校验失败")
			return
		}
		if access == nil || !s.db.RenewAccessRequest(userID, access, requestID, hapiAccessSessionTTL) {
			fail(c, http.StatusForbidden, "Agent 访问票据无效或已过期")
			return
		}
		resourceUserID, resourceAgentID = access.UserID, access.AgentID
	}
	conn, err := tunnelUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	peer := &tunnelPeer{
		conn: conn, key: tunnelKey{userID: resourceUserID, agentID: resourceAgentID}, role: role,
		connectionID: randomTunnelID(), requestID: requestID, sessionID: c.GetString("session_id"),
		subjectUserID: userID, accessID: accessID,
		gateway:   role == "agent" && c.Query("gateway") == "1",
		discovery: role == "agent" && c.Query("discovery") == "1",
		send:      make(chan []byte, 8), closed: make(chan struct{}),
	}
	if !s.tunnel.attach(peer) {
		peer.close()
		return
	}
	defer s.tunnel.detach(peer)
	go s.tunnel.writeLoop(peer)
	s.tunnel.sendControl(peer, tunnelMessage{Type: "ready", ConnectionID: peer.connectionID})
	if role == "mobile" {
		state := "agent_offline"
		if s.tunnel.agent(peer.key) != nil {
			state = "agent_online"
		}
		s.tunnel.sendControl(peer, tunnelMessage{Type: state})
	}
	s.tunnel.readLoop(peer)
}

func (t *hapiTunnel) attach(peer *tunnelPeer) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if peer.role == "agent" {
		if previous := t.agents[peer.key]; previous != nil {
			previous.close()
		}
		for _, gateway := range t.gateways[peer.key] {
			gateway.close()
		}
		delete(t.gateways, peer.key)
		t.agents[peer.key] = peer
		for _, mobile := range t.mobiles[peer.key] {
			t.sendControl(mobile, tunnelMessage{Type: "agent_online"})
		}
		return true
	}
	if t.mobiles[peer.key] == nil {
		t.mobiles[peer.key] = make(map[string]*tunnelPeer)
	}
	if len(t.mobiles[peer.key]) >= 4 {
		return false
	}
	t.mobiles[peer.key][peer.connectionID] = peer
	return true
}

func (t *hapiTunnel) detach(peer *tunnelPeer) {
	peer.close()
	t.mu.Lock()
	if peer.role == "agent" {
		if t.agents[peer.key] == peer {
			delete(t.agents, peer.key)
			gateways := t.gateways[peer.key]
			mobiles := t.mobiles[peer.key]
			discoveries := t.discoveries[peer.key]
			delete(t.gateways, peer.key)
			delete(t.discoveries, peer.key)
			t.mu.Unlock()
			for _, gateway := range gateways {
				gateway.close()
			}
			for _, mobile := range mobiles {
				t.sendControl(mobile, tunnelMessage{Type: "agent_offline"})
			}
			for _, waiter := range discoveries {
				select {
				case waiter <- discoveryResult{err: errDiscoveryUnavailable}:
				default:
				}
			}
			return
		}
		t.mu.Unlock()
		return
	}
	if peers := t.mobiles[peer.key]; peers != nil {
		delete(peers, peer.connectionID)
		if len(peers) == 0 {
			delete(t.mobiles, peer.key)
		}
	}
	agent := t.agents[peer.key]
	t.mu.Unlock()
	if agent != nil {
		t.sendControl(agent, tunnelMessage{Type: "disconnect", ConnectionID: peer.connectionID, RequestID: peer.requestID})
	}
}

func (t *hapiTunnel) readLoop(peer *tunnelPeer) {
	peer.conn.SetReadLimit(tunnelMaxMessage)
	_ = peer.conn.SetReadDeadline(time.Now().Add(tunnelPongWait))
	peer.conn.SetPongHandler(func(string) error { return peer.conn.SetReadDeadline(time.Now().Add(tunnelPongWait)) })
	for {
		messageType, raw, err := peer.conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var message tunnelMessage
		if json.Unmarshal(raw, &message) != nil || len(message.Payload) == 0 ||
			(message.Type != "data" && (peer.role != "agent" ||
				(message.Type != "gateway_data" && message.Type != "connection_response" && message.Type != "connection_error"))) {
			t.sendControl(peer, tunnelMessage{Type: "invalid_frame"})
			continue
		}
		if peer.role == "mobile" {
			cloudReceivedAt := time.Now().UnixMilli()
			agent := t.agent(peer.key)
			if agent == nil {
				t.sendControl(peer, tunnelMessage{Type: "agent_offline"})
				continue
			}
			traceID := strings.TrimSpace(message.TraceID)
			if traceID != "" && !validIdentifier(traceID, 8, 128) {
				traceID = ""
			}
			phoneTunnelSentAt := message.PhoneTunnelSentAt
			if traceID == "" || phoneTunnelSentAt <= 0 {
				phoneTunnelSentAt = 0
			}
			t.sendControl(agent, tunnelMessage{
				Type: "data", ConnectionID: peer.connectionID, RequestID: peer.requestID, Payload: message.Payload,
				TraceID: traceID, PhoneTunnelSentAt: phoneTunnelSentAt,
				CloudReceivedAt: cloudReceivedAt, CloudForwardedAt: time.Now().UnixMilli(),
			})
			continue
		}
		connectionID := strings.TrimSpace(message.ConnectionID)
		if message.Type == "connection_response" || message.Type == "connection_error" {
			t.deliverDiscovery(peer, strings.TrimSpace(message.RequestID), message.Payload)
			continue
		}
		if message.Type == "gateway_data" {
			t.deliverGateway(peer.key, connectionID, message.Payload)
			continue
		}
		mobile := t.mobile(peer.key, connectionID)
		if mobile == nil {
			continue
		}
		t.sendControl(mobile, tunnelMessage{Type: "data", Payload: message.Payload})
	}
}

func (t *hapiTunnel) requestDiscovery(
	ctx context.Context,
	key tunnelKey,
	requestID string,
	payload json.RawMessage,
	timeout time.Duration,
) (json.RawMessage, error) {
	waiter := make(chan discoveryResult, 1)
	t.mu.Lock()
	agent := t.agents[key]
	if agent == nil || !agent.discovery {
		t.mu.Unlock()
		return nil, errDiscoveryUnsupported
	}
	if t.discoveries[key] == nil {
		t.discoveries[key] = make(map[string]chan discoveryResult)
	}
	if _, exists := t.discoveries[key][requestID]; exists {
		t.mu.Unlock()
		return nil, errDiscoveryUnavailable
	}
	t.discoveries[key][requestID] = waiter
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		if pending := t.discoveries[key]; pending != nil {
			if pending[requestID] == waiter {
				delete(pending, requestID)
			}
			if len(pending) == 0 {
				delete(t.discoveries, key)
			}
		}
		t.mu.Unlock()
	}()

	if !t.sendControl(agent, tunnelMessage{Type: "connection_request", RequestID: requestID, Payload: payload}) {
		return nil, errDiscoveryUnavailable
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-waiter:
		return result.payload, result.err
	case <-timer.C:
		return nil, errDiscoveryUnavailable
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *hapiTunnel) deliverDiscovery(peer *tunnelPeer, requestID string, payload json.RawMessage) {
	if !validIdentifier(requestID, 8, 128) {
		return
	}
	t.mu.RLock()
	if t.agents[peer.key] != peer {
		t.mu.RUnlock()
		return
	}
	waiter := t.discoveries[peer.key][requestID]
	t.mu.RUnlock()
	if waiter != nil {
		select {
		case waiter <- discoveryResult{payload: append(json.RawMessage(nil), payload...)}:
		default:
		}
	}
}

func (t *hapiTunnel) writeLoop(peer *tunnelPeer) {
	ticker := time.NewTicker(tunnelPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case raw, ok := <-peer.send:
			if !ok {
				return
			}
			_ = peer.conn.SetWriteDeadline(time.Now().Add(tunnelWriteWait))
			if peer.conn.WriteMessage(websocket.TextMessage, raw) != nil {
				peer.close()
				return
			}
		case <-ticker.C:
			if !t.sessionAuthorized(peer) {
				peer.close()
				return
			}
			_ = peer.conn.SetWriteDeadline(time.Now().Add(tunnelWriteWait))
			if peer.conn.WriteMessage(websocket.PingMessage, nil) != nil {
				peer.close()
				return
			}
		case <-peer.closed:
			return
		}
	}
}

func (t *hapiTunnel) sessionAuthorized(peer *tunnelPeer) bool {
	session, err := t.db.SessionByToken(peer.sessionID)
	if err != nil || session == nil || session.UserID != peer.subjectUserID {
		return false
	}
	if peer.role == "agent" {
		return session.UserID == peer.key.userID
	}
	access, err := t.db.ResolveAgentAccess(peer.subjectUserID, peer.accessID)
	return err == nil && access != nil && access.UserID == peer.key.userID && access.AgentID == peer.key.agentID && t.db.RenewAccessRequest(peer.subjectUserID, access, peer.requestID, hapiAccessSessionTTL)
}

func (t *hapiTunnel) sendControl(peer *tunnelPeer, message tunnelMessage) bool {
	raw, err := json.Marshal(message)
	if err != nil {
		return false
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case peer.send <- raw:
		return true
	case <-peer.closed:
		return false
	case <-timer.C:
		peer.close()
		return false
	}
}

func (t *hapiTunnel) agent(key tunnelKey) *tunnelPeer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.agents[key]
}
func (t *hapiTunnel) mobile(key tunnelKey, id string) *tunnelPeer {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.mobiles[key][id]
}
func (peer *tunnelPeer) close() {
	peer.closeOnce.Do(func() { close(peer.closed); _ = peer.conn.Close() })
}

func randomTunnelID() string {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return base64.RawURLEncoding.EncodeToString([]byte(time.Now().String()))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
