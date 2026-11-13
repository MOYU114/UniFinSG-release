package eval

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/config"
	"unichatgo/internal/redis"
)

func TestRedisRateLimitIsAtomicAndExpires(t *testing.T) {
	cache := evalRedis(t)
	service := NewService(nil, cache)
	address := evalTestIP(t, cache)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var allowedCount atomic.Int64
	var wg sync.WaitGroup
	failures := make(chan error, 200)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, retry, err := service.AllowIP(ctx, address)
			if err != nil {
				failures <- err
				return
			}
			if retry < 1 || retry > 60 {
				failures <- fmt.Errorf("invalid Retry-After %d", retry)
			}
			if allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if allowedCount.Load() != 60 {
		t.Fatalf("concurrent requests exceeded/lost quota: %d", allowedCount.Load())
	}
	key := rateLimitKey(address)
	if count, err := cache.Raw().Get(ctx, key).Int(); err != nil || count != 200 {
		t.Fatalf("atomic counter: %d %v", count, err)
	}
	if ttl, err := cache.Raw().PTTL(ctx, key).Result(); err != nil || ttl <= 0 || ttl > time.Minute {
		t.Fatalf("counter missing expiration: %s %v", ttl, err)
	}
	if allowed, _, err := service.AllowIP(ctx, evalTestIP(t, cache)); err != nil || !allowed {
		t.Fatalf("another IP inherited quota: %v", err)
	}
	if err := cache.Raw().PExpire(ctx, key, time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if allowed, _, err := service.AllowIP(ctx, address); err != nil || !allowed {
		t.Fatalf("expired quota was not reset: %v", err)
	}
	if count, err := cache.Raw().Get(ctx, key).Int(); err != nil || count != 1 {
		t.Fatalf("expired counter did not restart: %d %v", count, err)
	}
}

func TestRedisHTTPUsesSocketIPUnlessTrustedResolverInjected(t *testing.T) {
	cache := evalRedis(t)
	address := evalTestIP(t, cache)
	remote := net.JoinHostPort(address.String(), "3456")
	router := gin.New()
	NewHTTPHandler(NewService(nil, cache)).RegisterRoutes(router.Group("/api"))
	for i := 0; i < 61; i++ {
		request := httptest.NewRequest("POST", "/api/eval/events", strings.NewReader(`{}`))
		request.RemoteAddr = remote
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("192.0.2.%d", i+1))
		request.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i+1))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if i < 60 {
			assertEvalError(t, response, 400, "validation_failed")
		} else {
			assertEvalError(t, response, 429, "rate_limited")
			if retry, err := strconv.Atoi(response.Header().Get("Retry-After")); err != nil || retry < 1 || retry > 60 {
				t.Fatalf("missing/invalid Retry-After: %q", response.Header().Get("Retry-After"))
			}
		}
	}
	trustedIP := evalTestIP(t, cache)
	trusted := gin.New()
	NewHTTPHandler(NewService(nil, cache), WithTrustedClientIPResolver(func(*http.Request) (string, error) {
		return trustedIP.String(), nil
	})).RegisterRoutes(trusted.Group("/api"))
	assertEvalError(t, evalRequest(trusted, "POST", "/api/eval/events", `{}`, "", remote), 400, "validation_failed")
	if rateLimitKey(netip.MustParseAddr("192.0.2.1")) != rateLimitKey(netip.MustParseAddr("::ffff:192.0.2.1")) {
		t.Fatal("IPv4-mapped IPv6 bypassed IP identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cache.Del(ctx, rateLimitKey(address), rateLimitKey(trustedIP)); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	assertEvalError(t, evalRequest(router, "POST", "/api/eval/events", `{}`, "", remote), 503, "unavailable")
}

func evalRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_EVAL_ADDR")
	if addr == "" {
		t.Skip("set TEST_REDIS_EVAL_ADDR to the isolated evaluation Redis instance")
	}
	host, portValue, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal("invalid TEST_REDIS_EVAL_ADDR")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("invalid evaluation Redis test port")
	}
	cache, err := redis.NewRedisClient(&config.Config{
		Redis: config.RedisConfig{Host: host, Port: port},
	})
	if err != nil {
		t.Fatalf("connect evaluation Redis: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func evalTestIP(t *testing.T, cache *redis.Client) netip.Addr {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	copy(bytes[:4], []byte{0x20, 0x01, 0x0d, 0xb8})
	address := netip.AddrFrom16(bytes)
	t.Cleanup(func() {
		// Delete only this test's unpredictable key; never FLUSHDB.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = cache.Del(ctx, rateLimitKey(address))
	})
	return address
}
