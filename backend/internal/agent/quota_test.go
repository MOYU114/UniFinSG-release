package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimiterReadsEnvLimits(t *testing.T) {
	t.Setenv("ASSISTANT_RPM", "2")
	t.Setenv("ASSISTANT_DAILY_LIMIT", "3")
	r := newRateLimiter(nil)
	if r.perMinute != 2 || r.perDay != 3 {
		t.Fatalf("limits = %d/%d, want 2/3", r.perMinute, r.perDay)
	}
	for i := 0; i < 2; i++ {
		if ok, _, _ := r.allow(context.Background(), 1); !ok {
			t.Fatalf("request %d rejected", i)
		}
	}
	if ok, reason, _ := r.allow(context.Background(), 1); ok || reason != "rate_limited" {
		t.Fatalf("expected rate_limited after 2 requests, got %v %s", ok, reason)
	}
}

func TestRateLimiterFallsBackOnInvalidEnv(t *testing.T) {
	for _, raw := range []string{"", "abc", "0", "-4"} {
		t.Setenv("ASSISTANT_RPM", raw)
		t.Setenv("ASSISTANT_DAILY_LIMIT", raw)
		r := newRateLimiter(nil)
		if r.perMinute != defaultPerMinute || r.perDay != defaultPerDay {
			t.Fatalf("%q: limits = %d/%d, want defaults", raw, r.perMinute, r.perDay)
		}
	}
}

func TestQuotaUsageTracksAndClampsCounts(t *testing.T) {
	t.Setenv("ASSISTANT_RPM", "2")
	t.Setenv("ASSISTANT_DAILY_LIMIT", "3")
	r := newRateLimiter(nil)
	u := r.usage(context.Background(), 1)
	if u.DailyRemaining != 3 || u.DailyUsed != 0 || u.MinuteRemaining != 2 {
		t.Fatalf("fresh usage = %+v", u)
	}
	if _, err := time.Parse(time.RFC3339, u.ResetsAt); err != nil {
		t.Fatalf("resets_at %q: %v", u.ResetsAt, err)
	}
	for i := 0; i < 4; i++ {
		_, _, _ = r.allow(context.Background(), 1)
	}
	u = r.usage(context.Background(), 1)
	if u.MinuteRemaining != 0 || u.DailyUsed != 2 || u.DailyRemaining != 1 {
		t.Fatalf("usage after rejected requests = %+v", u)
	}
	if other := r.usage(context.Background(), 2); other.DailyRemaining != 3 {
		t.Fatalf("usage leaked across users: %+v", other)
	}
}

func TestAskDoneIncludesQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, err := NewHandler(nil, nil, testProvider{snapshot: testSnapshot()}, testStreamer{text: "You have S$800.00 remaining."})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/users/1/assistant/ask", strings.NewReader(`{"question":"How am I doing?"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("auth_user_id", int64(1))
	h.ask(c)
	body := w.Body.String()
	idx := strings.Index(body, "event: done\ndata: ")
	if idx < 0 {
		t.Fatalf("no done event in %q", body)
	}
	line := strings.SplitN(body[idx+len("event: done\ndata: "):], "\n", 2)[0]
	var done struct {
		Quota *QuotaUsage `json:"quota"`
	}
	if err := json.Unmarshal([]byte(line), &done); err != nil {
		t.Fatal(err)
	}
	if done.Quota == nil || done.Quota.DailyUsed != 1 || done.Quota.DailyRemaining != done.Quota.DailyLimit-1 {
		t.Fatalf("done quota = %+v", done.Quota)
	}
}
