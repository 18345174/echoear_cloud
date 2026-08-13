package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
	send          chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
}

type hapiTunnel struct {
	mu       sync.RWMutex
	db       *database.DB
	agents   map[tunnelKey]*tunnelPeer
	mobiles  map[tunnelKey]map[string]*tunnelPeer
	gateways map[tunnelKey]map[string]*gatewayRequest
}

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
		mobiles:  make(map[tunnelKey]map[string]*tunnelPeer),
		gateways: make(map[tunnelKey]map[string]*gatewayRequest),
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
		if access == nil || !s.db.AccessRequestValid(userID, access, requestID) {
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
		gateway: role == "agent" && c.Query("gateway") == "1",
		send:    make(chan []byte, 8), closed: make(chan struct{}),
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
	defer t.mu.Unlock()
	if peer.role == "agent" {
		if t.agents[peer.key] == peer {
			delete(t.agents, peer.key)
			for _, gateway := range t.gateways[peer.key] {
				gateway.close()
			}
			delete(t.gateways, peer.key)
			for _, mobile := range t.mobiles[peer.key] {
				t.sendControl(mobile, tunnelMessage{Type: "agent_offline"})
			}
		}
		return
	}
	if peers := t.mobiles[peer.key]; peers != nil {
		delete(peers, peer.connectionID)
		if len(peers) == 0 {
			delete(t.mobiles, peer.key)
		}
	}
	if agent := t.agents[peer.key]; agent != nil {
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
			(message.Type != "data" && (peer.role != "agent" || message.Type != "gateway_data")) {
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
	return err == nil && access != nil && access.UserID == peer.key.userID && access.AgentID == peer.key.agentID && t.db.AccessRequestValid(peer.subjectUserID, access, peer.requestID)
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
