package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestV2RoutesOmitDeprecatedChatSurface locks the P0 contract: production
// registers RegisterV2Routes, so the inherited token/conversation/upload
// endpoints must not be reachable, while the retained capability probe stays
// online.
func TestV2RoutesOmitDeprecatedChatSurface(t *testing.T) {
	_, _, handler := newTestServer(t)

	v2 := gin.New()
	handler.RegisterV2Routes(v2)
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/users/1/token"},
		{http.MethodGet, "/api/users/1/token"},
		{http.MethodDelete, "/api/users/1/token"},
		{http.MethodPost, "/api/users/1/conversation/session-list"},
		{http.MethodPost, "/api/users/1/conversation/start"},
		{http.MethodPost, "/api/users/1/conversation/msg"},
		{http.MethodGet, "/api/users/1/conversation/sessions/1/messages"},
		{http.MethodDelete, "/api/users/1/conversation/sessions/1"},
		{http.MethodPost, "/api/users/1/conversation/requests/abc/resolve"},
		{http.MethodPost, "/api/users/1/uploads"},
	} {
		response := httptest.NewRecorder()
		v2.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s must be offline in v2, got %d", test.method, test.path, response.Code)
		}
	}

	response := httptest.NewRecorder()
	v2.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities probe must stay online, got %d", response.Code)
	}

	// The deprecated entry point still registers the retained handlers for the
	// legacy regression suite; auth middleware answers 401 rather than 404.
	legacy := gin.New()
	handler.RegisterRoutes(legacy)
	response = httptest.NewRecorder()
	legacy.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/users/1/conversation/start", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("legacy route registration changed: got %d", response.Code)
	}
}
