package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStaleGatewaySessionUsesMachineReadableConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	gateway := &gatewayRequest{
		connectionID: "gateway-1",
		frames:       make(chan gatewayFrame, 1),
		closed:       make(chan struct{}),
	}
	gateway.frames <- gatewayFrame{
		Type:    "response_error",
		ID:      gateway.connectionID,
		Message: gatewaySessionInvalid,
	}

	(&Server{}).streamGatewayResponse(context, gateway)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	if got := recorder.Header().Get(gatewayErrorHeader); got != gatewayErrorSessionInvalid {
		t.Fatalf("%s = %q, want %q", gatewayErrorHeader, got, gatewayErrorSessionInvalid)
	}
}
