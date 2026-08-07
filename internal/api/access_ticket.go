package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type accessTicketSigner struct {
	private ed25519.PrivateKey
	public  ed25519.PublicKey
	kid     string
}

type accessTicketClaims struct {
	Version       int             `json:"version"`
	TicketID      string          `json:"jti"`
	RequestID     string          `json:"request_id"`
	SubjectUserID int64           `json:"sub"`
	OwnerUserID   int64           `json:"owner_user_id"`
	AgentID       string          `json:"agent_id"`
	AgentPublicID string          `json:"agent_public_id"`
	ShareID       string          `json:"share_id,omitempty"`
	PolicyVersion int             `json:"policy_version"`
	Namespace     string          `json:"namespace"`
	Owner         bool            `json:"owner"`
	Policy        json.RawMessage `json:"policy"`
	IssuedAt      int64           `json:"iat"`
	ExpiresAt     int64           `json:"exp"`
}

func newAccessTicketSigner(encodedSeed string) *accessTicketSigner {
	var seed []byte
	if value, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encodedSeed)); err == nil && len(value) == ed25519.SeedSize {
		seed = value
	} else if value, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedSeed)); err == nil && len(value) == ed25519.SeedSize {
		seed = value
	} else {
		seed = make([]byte, ed25519.SeedSize)
		_, _ = rand.Read(seed)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	kidRaw := public[:8]
	return &accessTicketSigner{private: private, public: public, kid: base64.RawURLEncoding.EncodeToString(kidRaw)}
}

func (s *accessTicketSigner) sign(claims accessTicketClaims) (string, error) {
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "typ": "EAT", "kid": s.kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedHeader + "." + encodedPayload
	signature := ed25519.Sign(s.private, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *accessTicketSigner) keyDocument() map[string]any {
	return map[string]any{"algorithm": "Ed25519", "key_id": s.kid, "public_key": base64.RawURLEncoding.EncodeToString(s.public)}
}

func makeTicketClaims(ticketID, requestID string, userID, ownerID int64, agentID, publicID, shareID string, policyVersion int, namespace string, owner bool, policy json.RawMessage, expiresAt time.Time) accessTicketClaims {
	return accessTicketClaims{Version: 1, TicketID: ticketID, RequestID: requestID, SubjectUserID: userID, OwnerUserID: ownerID, AgentID: agentID,
		AgentPublicID: publicID, ShareID: shareID, PolicyVersion: policyVersion, Namespace: namespace, Owner: owner,
		Policy: policy, IssuedAt: time.Now().UTC().Unix(), ExpiresAt: expiresAt.Unix()}
}
