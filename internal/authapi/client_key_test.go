package authapi

import (
	"net/http/httptest"
	"testing"
)

func TestGatewayClientsHaveIndependentLoginLimits(t *testing.T) {
	first := httptest.NewRequest("POST", "/api/auth/login", nil)
	first.RemoteAddr = "172.20.0.2:5000"
	first.Header.Set("X-Video-Viewer-Client-IP", "192.0.2.1")
	second := first.Clone(first.Context())
	second.Header.Set("X-Video-Viewer-Client-IP", "192.0.2.2")
	limiter := newLoginLimiter()
	for i := 0; i < loginMaxAttempts; i++ {
		limiter.fail(clientKey(first, "user"))
	}
	if limiter.allow(clientKey(first, "user")) {
		t.Fatal("first client was not limited")
	}
	if !limiter.allow(clientKey(second, "user")) {
		t.Fatal("second client was blocked")
	}
}
