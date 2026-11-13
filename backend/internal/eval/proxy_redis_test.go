package eval

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRedisHTTPTrustedProxyPreservesClientQuotas(t *testing.T) {
	cache := evalRedis(t)
	peer, first, second := evalTestIP(t, cache), evalTestIP(t, cache), evalTestIP(t, cache)
	resolve, err := NewTrustedProxyIPResolver(peer.String() + "/128,10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewHTTPHandler(NewService(nil, cache), WithTrustedClientIPResolver(resolve)).RegisterRoutes(router.Group("/api"))
	post := func(remote string, values ...string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/eval/events", strings.NewReader(`{}`))
		request.RemoteAddr = net.JoinHostPort(remote, "3456")
		for _, value := range values {
			request.Header.Add("X-Forwarded-For", value)
		}
		request.Header.Set("X-Real-IP", "192.0.2.200")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	for i := 0; i < 61; i++ {
		// The leftmost value is attacker-controlled and changes on every
		// request. The trusted chain still resolves the same actual client.
		response := post(peer.String(), fmt.Sprintf("198.51.100.%d,%s", i+1, first), "10.0.0.8")
		if i < 60 {
			assertEvalError(t, response, 400, "validation_failed")
		} else {
			assertEvalError(t, response, 429, "rate_limited")
		}
	}
	// Both requests reach the same socket peer; the second client's first
	// request must not inherit the first client's exhausted allowance.
	assertEvalError(t, post(peer.String(), second.String(), "10.0.0.8"), 400, "validation_failed")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if count, err := cache.Raw().Get(ctx, rateLimitKey(first)).Int(); err != nil || count != 61 {
		t.Fatalf("forged left entries changed the client's counter: count=%d err=%v", count, err)
	}
	if count, err := cache.Raw().Get(ctx, rateLimitKey(second)).Int(); err != nil || count != 1 {
		t.Fatalf("second client did not receive an independent counter: count=%d err=%v", count, err)
	}
	if count, err := cache.Raw().Exists(ctx, rateLimitKey(peer)).Result(); err != nil || count != 0 {
		t.Fatalf("valid forwarded requests shared the proxy bucket: count=%d err=%v", count, err)
	}
	for _, claimed := range []string{first.String(), second.String()} {
		assertEvalError(t, post(peer.String(), claimed+",unknown"), 400, "validation_failed")
	}
	if count, err := cache.Raw().Get(ctx, rateLimitKey(peer)).Int(); err != nil || count != 2 {
		t.Fatalf("malformed suffixes did not share the fixed peer bucket: count=%d err=%v", count, err)
	}
	// A directly connected untrusted client cannot select the exhausted first
	// client's identity by supplying any forwarding headers.
	assertEvalError(t, post(second.String(), first.String()), 400, "validation_failed")
	if count, err := cache.Raw().Get(ctx, rateLimitKey(second)).Int(); err != nil || count != 2 {
		t.Fatalf("untrusted socket accepted a forged forwarding header: count=%d err=%v", count, err)
	}
}
