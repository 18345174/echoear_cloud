package api

import (
	"encoding/json"
	"testing"
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
