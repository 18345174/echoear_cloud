package api

import (
	"encoding/json"
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
