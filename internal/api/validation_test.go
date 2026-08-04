package api

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/18345174/echoear_cloud/internal/database"
)

func TestValidIdentifier(t *testing.T) {
	for _, value := range []string{"default", "mac-agent_01", "request.id"} {
		if !validIdentifier(value, 1, 128) {
			t.Fatalf("valid identifier rejected: %s", value)
		}
	}
	for _, value := range []string{"", "space value", "../../folder"} {
		if validIdentifier(value, 1, 128) {
			t.Fatalf("invalid identifier accepted: %s", value)
		}
	}
}

func TestSanitizeCapabilitiesDropsUntrustedFields(t *testing.T) {
	raw := sanitizeCapabilities(json.RawMessage(`{
		"hapi":true,"hapi_version":"1.2.3","runtime_ready":true,
		"runtime_starting":false,"runtime_failed":false,"cloud_tunnel_ready":true,
		"workspace_path":"/Users/private"
	}`))
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if value["runtime_ready"] != true || value["cloud_tunnel_ready"] != true {
		t.Fatalf("readiness lost: %s", raw)
	}
	if _, exists := value["workspace_path"]; exists {
		t.Fatalf("private field leaked: %s", raw)
	}
}

func TestValidateEnvelopeAcceptsStandardBase64(t *testing.T) {
	requestID := "hapi-request-12345678"
	keyID := "key-12345678"
	agent := &database.Agent{
		AgentID:      "default",
		PublicKey:    "public-key",
		KeyID:        keyID,
		KeyAlgorithm: hapiAlgorithm,
	}
	value := hapiEnvelope{
		Version:      2,
		Operation:    "hapi_connection",
		RequestID:    requestID,
		Algorithm:    hapiAlgorithm,
		KeyID:        keyID,
		EncryptedKey: base64.StdEncoding.EncodeToString(make([]byte, 256)),
		Nonce:        base64.StdEncoding.EncodeToString(make([]byte, 12)),
		Ciphertext:   base64.StdEncoding.EncodeToString(make([]byte, 17)),
		AAD:          "echoear-control-v2|default|hapi_connection||" + requestID + "|" + keyID,
	}

	if message := validateEnvelope(value, agent, requestID); message != "" {
		t.Fatalf("standard Base64 envelope rejected: %s", message)
	}
}

func TestValidateEncryptedBlobAcceptsStandardBase64(t *testing.T) {
	value := encryptedBlob{
		Version:    1,
		Nonce:      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		Ciphertext: base64.StdEncoding.EncodeToString(make([]byte, 17)),
		AAD:        "echoear-response-v1|default|hapi_connection|hapi-request-12345678",
	}

	if message := validateEncryptedBlob(value); message != "" {
		t.Fatalf("standard Base64 response rejected: %s", message)
	}
}
