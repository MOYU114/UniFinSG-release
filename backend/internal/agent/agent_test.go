package agent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/insight"
)

type testStreamer struct{ text string }

func (s testStreamer) Stream(_ context.Context, _ []ChatMessage, emit func(string) error) error {
	return emit(s.text)
}

type testProvider struct {
	snapshot    *insight.Snapshot
	err         error
	panicAfford bool
}

func (p testProvider) Snapshot(context.Context, int64, string) (*insight.Snapshot, error) {
	return p.snapshot, p.err
}
func (p testProvider) Affordability(s *insight.Snapshot, amount int64, c insight.Category) insight.Affordability {
	if p.panicAfford {
		panic("bad input")
	}
	return insight.Affordability{AmountCents: amount, Category: c, RemainingAfterCents: s.RemainingCents - amount}
}

func testSnapshot() *insight.Snapshot {
	return &insight.Snapshot{Month: "2026-11", Hash: "sha256:test", TotalBudgetCents: 100000, TotalSpentCents: 20000, RemainingCents: 80000, Categories: []insight.CategoryStat{{Category: insight.CategoryFood, BudgetCents: 50000, SpentCents: 20000, Status: insight.StatusOK}}}
}

func TestParseQueryUsesSafeCategoryAndAmountLimit(t *testing.T) {
	q := ParseQuery("Can I afford S$80 headphone?")
	if q.Intent != IntentAffordability || q.AmountCents != 8000 || q.Category != CategoryStudyDigital {
		t.Fatalf("unexpected query: %#v", q)
	}
	q = ParseQuery("Can I afford S$99,999,999,999?")
	if q.AmountCents != 0 || q.Category != CategoryEntertainmentOther {
		t.Fatalf("unsafe amount/category: %#v", q)
	}
}

func TestSafeAffordabilityRecoversProviderPanic(t *testing.T) {
	value, ok := safeAffordability(testProvider{panicAfford: true}, testSnapshot(), 100, insight.CategoryFood)
	if ok || value.AmountCents != 0 {
		t.Fatalf("panic should be converted to an absent affordability: %#v %v", value, ok)
	}
}

func TestStoreHistoryLimitAndMessageIDs(t *testing.T) {
	s := newStore(nil)
	for i := 0; i < 30; i++ {
		if _, err := s.saveTurn(context.Background(), 1, "q", "a", true, false); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.history(context.Background(), 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 50 {
		t.Fatalf("history length=%d", len(items))
	}
	if items[0].ID >= items[len(items)-1].ID {
		t.Fatalf("history is not ordered: %#v", items[:2])
	}
}

func TestNewHandlerRejectsNilProvider(t *testing.T) {
	if _, err := NewHandler(nil, nil, nil, nil); err == nil {
		t.Fatal("nil provider should be rejected")
	}
}

func TestLocalRateLimiterWarnWindow(t *testing.T) {
	r := newRateLimiter(nil)
	for i := 0; i < 5; i++ {
		ok, _, err := r.allow(context.Background(), 1)
		if err != nil || !ok {
			t.Fatalf("request %d rejected", i)
		}
	}
	ok, reason, _ := r.allow(context.Background(), 1)
	if ok || reason != "rate_limited" {
		t.Fatalf("expected rate limit, got %v %s", ok, reason)
	}
	r.release(context.Background(), 1)
	if ok, _, _ := r.allow(context.Background(), 1); ok {
		t.Fatal("release should only undo a successful reservation")
	}
}

func TestRetryAfterUsesWindowRemainder(t *testing.T) {
	r := newRateLimiter(nil)
	minute := r.retryAfter(1, "rate_limited")
	if minute < 1 || minute > 60 {
		t.Fatalf("minute Retry-After=%d, want 1..60", minute)
	}
	day := r.retryAfter(1, "quota_exceeded")
	if day < 1 || day > 24*60*60 {
		t.Fatalf("daily Retry-After=%d, want 1..86400", day)
	}
}

func TestAskSSEEventOrder(t *testing.T) {
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
	for _, event := range []string{"event: ack", "event: facts", "event: stream", "event: done"} {
		if !strings.Contains(body, event) {
			t.Fatalf("missing %s in %q", event, body)
		}
	}
	if strings.Index(body, "event: ack") > strings.Index(body, "event: facts") || strings.Index(body, "event: facts") > strings.Index(body, "event: stream") || strings.Index(body, "event: stream") > strings.Index(body, "event: done") {
		t.Fatalf("events out of order: %q", body)
	}
}

func TestPromptRedactsHistoricalUserQuestion(t *testing.T) {
	msgs := buildPrompt(Facts{}, []HistoryMessage{{Role: "user", Content: "Can I afford S$80 at Secret Merchant, email me@example.com?"}}, "Can I afford S$80 on food?")
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "me@example.com") {
			t.Fatalf("PII leaked into prompt: %q", msg.Content)
		}
	}
}
