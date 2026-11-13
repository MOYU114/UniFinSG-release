package credits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/llm"
)

type fakeStore struct {
	used map[string]int
	fail bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{used: map[string]int{}}
}

func storeKey(userID int64, day string) string {
	return fmt.Sprintf("%d:%s", userID, day)
}

func (f *fakeStore) Used(_ context.Context, userID int64, day string) (int, error) {
	if f.fail {
		return 0, errors.New("redis down")
	}
	return f.used[storeKey(userID, day)], nil
}

func (f *fakeStore) Add(_ context.Context, userID int64, day string, delta int) (int, error) {
	if f.fail {
		return 0, errors.New("redis down")
	}
	key := storeKey(userID, day)
	f.used[key] += delta
	if f.used[key] < 0 {
		f.used[key] = 0
	}
	return f.used[key], nil
}

func testConfig() Config {
	return Config{DailyGrant: 100, Costs: Costs{Chat: 1, Tool: 2, Vision: 5}}
}

func atSGT(hour, minute int) time.Time {
	return time.Date(2026, 11, 5, hour, minute, 0, 0, sgt)
}

func TestReserveUsesHighestPossibleTier(t *testing.T) {
	store := newFakeStore()
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	store.used[storeKey(1, "20261105")] = 98

	if _, err := service.Reserve(context.Background(), 1, ModePlatform, false); err != nil {
		t.Fatalf("2 credits should fit in 2 remaining: %v", err)
	}
	if _, err := service.Reserve(context.Background(), 1, ModePlatform, true); !errors.Is(err, ErrExhausted) {
		t.Fatalf("vision precheck should fail with 2 remaining: %v", err)
	}
	store.used[storeKey(1, "20261105")] = 99
	if _, err := service.Reserve(context.Background(), 1, ModePlatform, false); !errors.Is(err, ErrExhausted) {
		t.Fatalf("tool precheck should fail with 1 remaining: %v", err)
	}
}

func TestSettleChargesActualTier(t *testing.T) {
	store := newFakeStore()
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }

	chat, _ := service.Reserve(context.Background(), 1, ModePlatform, false)
	if charged, err := chat.Settle(context.Background(), TierChat); err != nil || charged != 1 {
		t.Fatalf("chat settlement: %d %v", charged, err)
	}
	tool, _ := service.Reserve(context.Background(), 1, ModePlatform, false)
	if charged, err := tool.Settle(context.Background(), TierTool); err != nil || charged != 2 {
		t.Fatalf("tool settlement: %d %v", charged, err)
	}
	vision, _ := service.Reserve(context.Background(), 1, ModePlatform, true)
	if charged, err := vision.Settle(context.Background(), TierVision); err != nil || charged != 5 {
		t.Fatalf("vision settlement: %d %v", charged, err)
	}
	if used := store.used[storeKey(1, "20261105")]; used != 8 {
		t.Fatalf("unexpected total used %d", used)
	}
}

func TestFailureBeforeOutputDoesNotCharge(t *testing.T) {
	store := newFakeStore()
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	reservation, err := service.Reserve(context.Background(), 1, ModePlatform, true)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := reservation.Release(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if used := store.used[storeKey(1, "20261105")]; used != 0 {
		t.Fatalf("failed turn was charged: %d", used)
	}
}

func TestReleaseRefundsASettledTurn(t *testing.T) {
	store := newFakeStore()
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	reservation, _ := service.Reserve(context.Background(), 1, ModePlatform, false)
	if _, err := reservation.Settle(context.Background(), TierTool); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := reservation.Release(context.Background()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if used := store.used[storeKey(1, "20261105")]; used != 0 {
		t.Fatalf("refund failed: %d", used)
	}
}

func TestBYOKNeverChecksOrCharges(t *testing.T) {
	store := newFakeStore()
	store.fail = true
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }

	reservation, err := service.Reserve(context.Background(), 1, ModeBYOK, true)
	if err != nil {
		t.Fatalf("byok reserve should not touch the store: %v", err)
	}
	if charged, err := reservation.Settle(context.Background(), TierVision); err != nil || charged != 0 {
		t.Fatalf("byok settle: %d %v", charged, err)
	}
	snapshot, err := service.Snapshot(context.Background(), 1, ModeBYOK)
	if err != nil {
		t.Fatalf("byok snapshot: %v", err)
	}
	if snapshot.Mode != ModeBYOK || snapshot.DailyGrant != nil || snapshot.Used != nil ||
		snapshot.Remaining != nil || snapshot.ResetsAt != nil || snapshot.Costs != nil {
		t.Fatalf("byok snapshot must be null: %+v", snapshot)
	}
}

func TestPlatformStoreUnavailable(t *testing.T) {
	store := newFakeStore()
	store.fail = true
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	if _, err := service.Reserve(context.Background(), 1, ModePlatform, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := service.Snapshot(context.Background(), 1, ModePlatform); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestSGTCalendarDayRollover(t *testing.T) {
	store := newFakeStore()
	service := NewService(store, testConfig())
	now := atSGT(23, 59)
	service.now = func() time.Time { return now }
	reservation, err := service.Reserve(context.Background(), 1, ModePlatform, false)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, err := reservation.Settle(context.Background(), TierChat); err != nil {
		t.Fatalf("settle: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), 1, ModePlatform)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if *snapshot.Used != 1 || *snapshot.Remaining != 99 {
		t.Fatalf("unexpected snapshot %+v", snapshot)
	}
	if *snapshot.ResetsAt != "2026-11-06T00:00:00+08:00" {
		t.Fatalf("unexpected reset time %q", *snapshot.ResetsAt)
	}

	now = atSGT(0, 1).AddDate(0, 0, 1) // 2026-11-06 00:01 SGT
	snapshot, err = service.Snapshot(context.Background(), 1, ModePlatform)
	if err != nil {
		t.Fatalf("snapshot after midnight: %v", err)
	}
	if *snapshot.Used != 0 || *snapshot.Remaining != 100 {
		t.Fatalf("the counter did not roll over: %+v", snapshot)
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	store := newFakeStore()
	store.used[storeKey(1, "20261105")] = 13
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	snapshot, err := service.Snapshot(context.Background(), 1, ModePlatform)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, _ := json.Marshal(snapshot)
	want := `{"mode":"platform","daily_grant":100,"used":13,"remaining":87,"resets_at":"2026-11-06T00:00:00+08:00","costs":{"chat":1,"tool":2,"vision":5}}`
	if string(raw) != want {
		t.Fatalf("unexpected JSON\n got: %s\nwant: %s", raw, want)
	}

	byok, _ := service.Snapshot(context.Background(), 1, ModeBYOK)
	raw, _ = json.Marshal(byok)
	want = `{"mode":"byok","daily_grant":null,"used":null,"remaining":null,"resets_at":null,"costs":null}`
	if string(raw) != want {
		t.Fatalf("unexpected BYOK JSON\n got: %s\nwant: %s", raw, want)
	}
}

type staticMode struct {
	mode string
	err  error
}

func (s staticMode) ModeForUser(context.Context, int64) (string, error) { return s.mode, s.err }

func creditsRouter(service *Service, modes ModeResolver, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := newTestHandler(service, modes, func(c *gin.Context) {
		c.Set("auth_user_id", userID)
		c.Next()
	}, func(c *gin.Context) { c.Next() })
	handler.RegisterRoutes(router.Group("/api"))
	return router
}

func TestCreditsEndpoint(t *testing.T) {
	store := newFakeStore()
	store.used[storeKey(1, "20261105")] = 13
	service := NewService(store, testConfig())
	service.now = func() time.Time { return atSGT(12, 0) }
	router := creditsRouter(service, staticMode{mode: ModePlatform}, 1)

	request := httptest.NewRequest(http.MethodGet, "/api/users/1/agent/credits", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"remaining":87`) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/users/2/agent/credits", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for another user, got %d", response.Code)
	}
}

func TestCreditsEndpointReportsMissingModel(t *testing.T) {
	service := NewService(newFakeStore(), testConfig())
	router := creditsRouter(service, staticMode{err: llm.ErrNotConfigured}, 1)
	request := httptest.NewRequest(http.MethodGet, "/api/users/1/agent/credits", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "llm_not_configured") {
		t.Fatalf("unexpected response %d: %s", response.Code, response.Body.String())
	}
}

func TestWriteErrorExhaustedShape(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	WriteError(context, &ExhaustedError{ResetsAt: resetsAt(atSGT(0, 0))})
	if response.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), "credits_exhausted") ||
		!strings.Contains(response.Body.String(), "2026-11-06T00:00:00+08:00") {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
}
