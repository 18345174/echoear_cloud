package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAccessTicketSignerUsesConfiguredSeed(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index)
	}
	signer := newAccessTicketSigner(base64.StdEncoding.EncodeToString(seed))
	claims := makeTicketClaims("ticket-1", "hapi-request-12345678", 7, 3, "agent-1", "public-1", "share-1", 4, "share-share-1", false, json.RawMessage(`{"allowed_flavors":["codex"]}`), time.Now().UTC().Add(time.Minute))
	ticket, err := signer.sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected ticket: %s", ticket)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(signer.public, []byte(parts[0]+"."+parts[1]), signature) {
		t.Fatal("ticket signature did not verify")
	}
	if claims.RequestID != "hapi-request-12345678" {
		t.Fatalf("request ID was not bound into ticket claims: %q", claims.RequestID)
	}
	second := newAccessTicketSigner(base64.RawURLEncoding.EncodeToString(seed))
	if signer.kid != second.kid || !signer.public.Equal(second.public) {
		t.Fatal("configured seed did not produce a stable verification key")
	}
}

func TestNormalizeSharePolicy(t *testing.T) {
	raw := json.RawMessage(`{
		"allowed_flavors":["codex"],"allowed_models":{"codex":["gpt-5"]},
		"workspace_roots":["/work"],"task_permissions":{"create":true},
		"max_permission_mode":"default","max_concurrent_tasks":1,"max_tasks_per_day":20
	}`)
	if normalized, message := normalizeSharePolicy(raw); message != "" || len(normalized) == 0 {
		t.Fatalf("valid policy rejected: %q", message)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"allowed_flavors":["codex"],"allowed_models":{},"workspace_roots":["/work"],"max_permission_mode":"default","max_concurrent_tasks":1,"max_tasks_per_day":20}`),
		json.RawMessage(`{"allowed_flavors":["codex"],"allowed_models":{"codex":["gpt-5"]},"workspace_roots":["/work"],"max_permission_mode":"default","max_concurrent_tasks":1,"max_tasks_per_day":0}`),
	} {
		if _, message := normalizeSharePolicy(invalid); message == "" {
			t.Fatalf("invalid policy accepted: %s", invalid)
		}
	}
}
