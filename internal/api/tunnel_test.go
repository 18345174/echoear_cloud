package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTunnelMessageTraceFieldsRoundTrip(t *testing.T) {
	raw, err := json.Marshal(tunnelMessage{
		Type:              "data",
		TraceID:           "trace-cloud-1234",
		PhoneTunnelSentAt: 1_000,
		CloudReceivedAt:   1_010,
		CloudForwardedAt:  1_012,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded tunnelMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TraceID != "trace-cloud-1234" || decoded.PhoneTunnelSentAt != 1_000 ||
		decoded.CloudReceivedAt != 1_010 || decoded.CloudForwardedAt != 1_012 {
		t.Fatalf("unexpected timing fields: %+v", decoded)
	}
}

func TestGatewayRequestHeadersPreserveHapiAuthAndStripCloudSession(t *testing.T) {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer hapi-token")
	headers.Set("X-EchoEar-Session", "cloud-session")
	headers.Set("Connection", "keep-alive")
	headers.Set("Accept", "text/event-stream")

	forwarded := gatewayRequestHeaders(headers)
	if forwarded["Authorization"] != "Bearer hapi-token" || forwarded["Accept"] != "text/event-stream" {
		t.Fatalf("expected HAPI headers to pass through: %#v", forwarded)
	}
	if _, ok := forwarded["X-Echoear-Session"]; ok {
		t.Fatalf("cloud session leaked to local HAPI: %#v", forwarded)
	}
	if _, ok := forwarded["Connection"]; ok {
		t.Fatalf("hop header leaked to local HAPI: %#v", forwarded)
	}
}

func TestGatewayRequiresAgentCapability(t *testing.T) {
	key := tunnelKey{userID: 1, agentID: "agent-1"}
	tunnel := newHapiTunnel(nil)
	tunnel.agents[key] = &tunnelPeer{gateway: false}
	request := &gatewayRequest{key: key, connectionID: "gateway-1"}
	if tunnel.attachGateway(request) {
		t.Fatal("legacy Agent must not accept same-host gateway requests")
	}
	tunnel.agents[key].gateway = true
	if !tunnel.attachGateway(request) {
		t.Fatal("gateway-capable Agent should accept same-host requests")
	}
}
