package finance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

// This integration suite owns one initially empty MySQL database. Run it
// separately from the migration lifecycle suite, which has the same contract.
func TestMySQLFinanceIntegration(t *testing.T) {
	db := financeMySQLDatabase(t, "TEST_MYSQL_FINANCE_DSN")
	fixed := time.Date(2026, 10, 31, 16, 30, 0, 123456000, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return fixed }))
	authService := auth.NewService(db, nil, time.Hour)
	router := gin.New()
	NewHTTPHandler(service, authService).RegisterRoutes(router.Group("/api"))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	newUser := func(name string) (int64, string) {
		t.Helper()
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
			VALUES (?, 'integration-test-only', ?)`, name, fixed.UTC())
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		token, err := authService.IssueToken(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return id, token
	}

	t.Run("profile HTTP contract and isolation", func(t *testing.T) {
		alice, token := newUser("finance_profile_alice")
		bob, _ := newUser("finance_profile_bob")
		path := fmt.Sprintf("/api/users/%d/profile", alice)
		assertFinanceError(t, financeRequest(t, router, "GET", path, nil, "", false), 401, "unauthenticated")
		assertFinanceError(t, financeRequest(t, router, "GET", path, nil, token, false), 404, "not_found")
		budgetPath := fmt.Sprintf("/api/users/%d/budgets/2026-11", alice)
		assertFinanceError(t, financeRequest(t, router, "GET", budgetPath, nil, token, false), 409, "profile_required")

		for _, endpoint := range []string{"profile", "budgets/2026-11"} {
			for _, method := range []string{"GET", "PUT"} {
				response := financeRequest(t, router, method, fmt.Sprintf("/api/users/%d/%s", bob, endpoint),
					map[string]any{}, token, false)
				assertFinanceError(t, response, 403, "forbidden")
			}
		}
		body := map[string]any{
			"monthly_disposable_cents": int64(120000), "housing": "on_campus", "user_id": bob,
		}
		assertFinanceError(t, financeRequest(t, router, "PUT", path, body, token, true), 403, "csrf_invalid")
		response := financeRequest(t, router, "PUT", path, body, token, false)
		var profile Profile
		decodeFinanceResponse(t, response, &profile)
		if profile.MonthlyDisposableCents != 120000 || profile.DefaultBudgetCents != 120000 ||
			profile.Housing != "on_campus" || profile.UpdatedAt.Format(time.RFC3339Nano) != "2026-11-01T00:30:00.123456+08:00" {
			t.Fatalf("unexpected saved profile: %+v", profile)
		}
		var foreignCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_profiles WHERE user_id = ?`, bob).Scan(&foreignCount); err != nil || foreignCount != 0 {
			t.Fatalf("body user_id affected another user: count=%d err=%v", foreignCount, err)
		}
		var reread Profile
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path, nil, token, false), &reread)
		if !reflect.DeepEqual(profile, reread) {
			t.Fatalf("profile round trip: saved=%+v read=%+v", profile, reread)
		}
		body["default_budget_cents"] = int64(0)
		response = financeRequest(t, router, "PUT", path, body, token, false)
		decodeFinanceResponse(t, response, &profile)
		if profile.DefaultBudgetCents != 0 {
			t.Fatal("explicit zero profile default was replaced")
		}
		var zeroBudget Budget
		decodeFinanceResponse(t, financeRequest(t, router, "GET", budgetPath, nil, token, false), &zeroBudget)
		if !zeroBudget.Generated || zeroBudget.TotalCents != 0 || zeroBudget.Source != "profile_default" || len(zeroBudget.Categories) != 6 {
			t.Fatalf("zero default budget: %+v", zeroBudget)
		}
		body["default_budget_cents"] = int64(90000)
		decodeFinanceResponse(t, financeRequest(t, router, "PUT", path, body, token, false), &profile)
		decodeFinanceResponse(t, financeRequest(t, router, "GET", budgetPath, nil, token, false), &zeroBudget)
		if zeroBudget.Generated || zeroBudget.TotalCents != 0 {
			t.Fatalf("profile update rewrote saved budget: %+v", zeroBudget)
		}

		for _, invalidBody := range []any{
			map[string]any{"housing": "on_campus"},
			map[string]any{"monthly_disposable_cents": 0, "housing": "on_campus"},
			map[string]any{"monthly_disposable_cents": 1, "housing": "invalid"},
			map[string]any{"monthly_disposable_cents": 1, "housing": "on_campus", "default_budget_cents": -1},
		} {
			assertFinanceError(t, financeRequest(t, router, "PUT", path, invalidBody, token, false), 400, "validation_failed")
		}
		for _, raw := range []string{"null", "[]", "{", `{} {}`, `{"monthly_disposable_cents":1.5}`, `{"monthly_disposable_cents":9223372036854775808}`} {
			request := httptest.NewRequest("PUT", path, bytes.NewBufferString(raw))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertFinanceError(t, response, 400, "bad_request")
		}
	})

	t.Run("budget generation provenance and month isolation", func(t *testing.T) {
		id, token := newUser("finance_budget_owner")
		_, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(120000), Housing: "with_family", DefaultBudgetCents: financeInt64(100003),
		})
		if err != nil {
			t.Fatal(err)
		}
		// A future saved budget must not become the source for an earlier month.
		future, err := service.SaveBudget(ctx, id, "2027-01", financeBudgetInput(600, 9000, [6]int64{100, 100, 100, 100, 100, 100}))
		if err != nil {
			t.Fatal(err)
		}
		september, err := service.GetOrCreateBudget(ctx, id, "2026-09")
		if err != nil {
			t.Fatal(err)
		}
		if september.TotalCents != 100003 || september.CategorySumCents != 100003 || september.UnallocatedCents != 0 ||
			!september.Generated || september.Source != "profile_default" || september.WarnBP != 8000 {
			t.Fatalf("default generation: %+v", september)
		}
		if september.Categories[2].BudgetCents != 0 {
			t.Fatal("zero-weight housing received a rounding remainder")
		}
		november, err := service.GetOrCreateBudget(ctx, id, "2026-11")
		if err != nil {
			t.Fatal(err)
		}
		if !november.Generated || november.Source != "copied_from_2026-09" ||
			!reflect.DeepEqual(november.Categories, september.Categories) {
			t.Fatalf("latest prior month copy: %+v", november)
		}
		readAgain, err := service.GetOrCreateBudget(ctx, id, "2026-11")
		if err != nil || readAgain.Generated || readAgain.Source != november.Source {
			t.Fatalf("persisted provenance: budget=%+v err=%v", readAgain, err)
		}
		saved, err := service.SaveBudget(ctx, id, "2026-11", financeBudgetInput(50, 10000, [6]int64{11, 12, 13, 14, 15, 16}))
		if err != nil {
			t.Fatal(err)
		}
		if saved.Generated || saved.Source != "saved" || saved.CategorySumCents != 81 || saved.UnallocatedCents != -31 || saved.WarnBP != 10000 {
			t.Fatalf("explicit saved budget: %+v", saved)
		}
		for _, expected := range []*Budget{september, future} {
			actual, err := service.GetOrCreateBudget(ctx, id, expected.Month)
			if err != nil || actual.TotalCents != expected.TotalCents || !reflect.DeepEqual(actual.Categories, expected.Categories) {
				t.Fatalf("another month changed: expected=%+v actual=%+v err=%v", expected, actual, err)
			}
		}
		december, err := service.GetOrCreateBudget(ctx, id, "2026-12")
		if err != nil || december.Source != "copied_from_2026-11" || december.TotalCents != 50 || december.WarnBP != 10000 {
			t.Fatalf("copy updated preceding month: budget=%+v err=%v", december, err)
		}
		for _, month := range []string{"2026-1", "2026-13", "0000-01", "2026-11-01"} {
			path := fmt.Sprintf("/api/users/%d/budgets/%s", id, month)
			assertFinanceError(t, financeRequest(t, router, "GET", path, nil, token, false), 400, "bad_request")
		}
		path := fmt.Sprintf("/api/users/%d/budgets/2026-11", id)
		for _, body := range []any{
			map[string]any{},
			financeBudgetInput(-1, 8000, [6]int64{}),
			financeBudgetInput(0, 10001, [6]int64{}),
			financeBudgetInput(0, 8000, [6]int64{math.MaxInt64, 1}),
		} {
			assertFinanceError(t, financeRequest(t, router, "PUT", path, body, token, false), 400, "validation_failed")
		}
		large, err := service.SaveBudget(ctx, id, "2028-01", financeBudgetInput(math.MaxInt64, 8000, [6]int64{math.MaxInt64}))
		if err != nil || large.TotalCents != math.MaxInt64 || large.UnallocatedCents != 0 {
			t.Fatalf("full int64 range was restricted: budget=%+v err=%v", large, err)
		}
	})

	t.Run("budget HTTP cookies and forged body identity", func(t *testing.T) {
		alice, token := newUser("finance_http_budget_alice")
		bob, bobToken := newUser("finance_http_budget_bob")
		path := fmt.Sprintf("/api/users/%d/budgets/2026-11", alice)
		body := map[string]any{
			"user_id": bob, "total_cents": int64(100),
			"categories": financeBudgetInput(100, 8000, [6]int64{20, 20, 0, 20, 20, 20}).Categories,
		}
		assertFinanceError(t, financeRequest(t, router, "PUT", path, body, token, true), 403, "csrf_invalid")
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("PUT", path, bytes.NewReader(raw))
		request.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		request.AddCookie(&http.Cookie{Name: "csrf_token", Value: "finance-test-csrf"})
		request.Header.Set("X-CSRF-Token", "finance-test-csrf")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var budget Budget
		decodeFinanceResponse(t, response, &budget)
		if budget.TotalCents != 100 || budget.WarnBP != 8000 || budget.Generated || budget.Source != "saved" {
			t.Fatalf("budget response: %+v", budget)
		}
		for i, category := range budget.Categories {
			if category.Category != categoryOrder[i] {
				t.Fatalf("category order: %+v", budget.Categories)
			}
		}
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path, nil, token, false), &budget)
		bobPath := fmt.Sprintf("/api/users/%d/budgets/2026-11", bob)
		assertFinanceError(t, financeRequest(t, router, "GET", bobPath, nil, bobToken, false), 409, "profile_required")
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ?`, bob).Scan(&count); err != nil || count != 0 {
			t.Fatalf("body user_id wrote another user's budget: count=%d err=%v", count, err)
		}
	})

	t.Run("concurrent generation creates one complete budget", func(t *testing.T) {
		id, _ := newUser("finance_concurrent_generate")
		_, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(12345), Housing: "off_campus",
		})
		if err != nil {
			t.Fatal(err)
		}
		var generated atomic.Int32
		runFinanceConcurrent(t, 12, func(int) error {
			budget, err := service.GetOrCreateBudget(ctx, id, "2026-11")
			if err != nil {
				return err
			}
			if budget.Generated {
				generated.Add(1)
			}
			if budget.TotalCents != 12345 || budget.CategorySumCents != 12345 || len(budget.Categories) != 6 {
				return fmt.Errorf("incomplete generated budget: %+v", budget)
			}
			return nil
		})
		if generated.Load() != 1 {
			t.Fatalf("generated responses=%d, want one", generated.Load())
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ? AND month = ?`, id, "2026-11").Scan(&count); err != nil || count != 1 {
			t.Fatalf("duplicate budgets: count=%d err=%v", count, err)
		}
	})

	t.Run("existing budget reads do not wait for an owner or budget writer", func(t *testing.T) {
		id, _ := newUser("finance_unlocked_budget_read")
		if _, err := service.SaveBudget(ctx, id, "2026-11", financeBudgetInput(600, 8000,
			[6]int64{100, 100, 100, 100, 100, 100})); err != nil {
			t.Fatal(err)
		}
		writer, err := service.beginUserWrite(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Rollback()
		if _, err := writer.ExecContext(ctx, `UPDATE budgets SET total_cents = 1200
			WHERE user_id = ? AND month = '2026-11'`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.ExecContext(ctx, `UPDATE budget_categories bc
			INNER JOIN budgets b ON b.id = bc.budget_id SET bc.budget_cents = 200
			WHERE b.user_id = ? AND b.month = '2026-11'`, id); err != nil {
			t.Fatal(err)
		}
		// The writer remains open for the entire call. A locking read cannot
		// pass by waiting for cleanup; it would hit this independent deadline.
		readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
		defer readCancel()
		budget, err := service.GetOrCreateBudget(readCtx, id, "2026-11")
		if err != nil {
			t.Fatalf("existing budget waited for an uncommitted writer: %v", err)
		}
		if budget.Generated || budget.TotalCents != 600 || budget.CategorySumCents != 600 {
			t.Fatalf("read observed uncommitted or mixed budget: %+v", budget)
		}
		for _, category := range budget.Categories {
			if category.BudgetCents != 100 {
				t.Fatalf("read observed an uncommitted category: %+v", budget)
			}
		}
		if err := writer.Commit(); err != nil {
			t.Fatal(err)
		}
		budget, err = service.GetOrCreateBudget(ctx, id, "2026-11")
		if err != nil || budget.TotalCents != 1200 || budget.CategorySumCents != 1200 {
			t.Fatalf("subsequent read did not observe committed update: %+v %v", budget, err)
		}
	})

	t.Run("generation is bounded by Singapore month while saved history stays readable", func(t *testing.T) {
		id, token := newUser("finance_generation_window")
		if _, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(1000), Housing: "on_campus",
		}); err != nil {
			t.Fatal(err)
		}
		for _, month := range []string{"2025-11", "2027-11"} {
			budget, err := service.GetOrCreateBudget(ctx, id, month)
			if err != nil || !budget.Generated || budget.TotalCents != 1000 {
				t.Fatalf("inclusive Singapore 12-month boundary %s: %+v %v", month, budget, err)
			}
		}
		for _, month := range []string{"2025-10", "2027-12", "0001-01", "9999-12"} {
			if _, err := service.GetOrCreateBudget(ctx, id, month); !errors.Is(err, ErrNotFound) {
				t.Fatalf("out-of-range missing month %s: %v", month, err)
			}
			assertFinanceError(t, financeRequest(t, router, "GET",
				fmt.Sprintf("/api/users/%d/budgets/%s", id, month), nil, token, false), 404, "not_found")
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ? AND month = ?`,
				id, month).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied month was persisted: month=%s count=%d err=%v", month, count, err)
			}
		}
		if _, err := service.SaveBudget(ctx, id, "2024-01", financeBudgetInput(600, 9000,
			[6]int64{100, 100, 100, 100, 100, 100})); err != nil {
			t.Fatal(err)
		}
		budget, err := service.GetOrCreateBudget(ctx, id, "2024-01")
		if err != nil || budget.Generated || budget.TotalCents != 600 || budget.WarnBP != 9000 {
			t.Fatalf("existing old budget became unreadable: %+v %v", budget, err)
		}
	})

	t.Run("current initialization uses caller time and never onboards a profileless account", func(t *testing.T) {
		id, _ := newUser("finance_explicit_current_time")
		withoutProfile, _ := newUser("finance_missing_current_profile")
		if _, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(1000), Housing: "on_campus",
		}); err != nil {
			t.Fatal(err)
		}
		for _, userID := range []int64{id, withoutProfile} {
			if _, err := service.SaveBudget(ctx, userID, "2026-10", financeBudgetInput(600, 9000,
				[6]int64{100, 100, 100, 100, 100, 100})); err != nil {
				t.Fatal(err)
			}
		}
		explicit := NewService(db, WithClock(func() time.Time {
			t.Fatal("EnsureCurrentBudget consulted the service clock instead of its explicit time")
			return time.Time{}
		}))
		for i := 0; i < 2; i++ {
			if err := explicit.EnsureCurrentBudget(ctx, id, fixed); err != nil {
				t.Fatal(err)
			}
			if err := explicit.EnsureCurrentBudget(ctx, withoutProfile, fixed); err != nil {
				t.Fatal(err)
			}
		}
		budget, err := service.GetOrCreateBudget(ctx, id, "2026-11")
		if err != nil || budget.Generated || budget.Source != "copied_from_2026-10" ||
			budget.TotalCents != 600 || budget.WarnBP != 9000 {
			t.Fatalf("explicit month did not reuse the shared generator: %+v %v", budget, err)
		}
		var count int
		var createdAt time.Time
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(created_at) FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, id).Scan(&count, &createdAt); err != nil ||
			count != 1 || !createdAt.Equal(fixed.UTC()) {
			t.Fatalf("initialization duplicated budget or used a second timestamp: count=%d created=%v err=%v", count, createdAt, err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, withoutProfile).Scan(&count); err != nil || count != 0 {
			t.Fatalf("profileless account received a budget: count=%d err=%v", count, err)
		}
		if _, err := service.GetOrCreateBudget(ctx, withoutProfile, "2026-11"); !errors.Is(err, ErrProfileRequired) {
			t.Fatalf("GET generated a new budget without a profile: %v", err)
		}
	})

	t.Run("concurrent callers on both sides of Singapore rollover create complete months", func(t *testing.T) {
		id, _ := newUser("finance_concurrent_rollover")
		if _, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(1000), Housing: "on_campus",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.SaveBudget(ctx, id, "2026-09", financeBudgetInput(600, 9000,
			[6]int64{100, 100, 100, 100, 100, 100})); err != nil {
			t.Fatal(err)
		}
		before := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
		after := before.Add(time.Second)
		rollover := NewService(db, WithClock(func() time.Time { return after }))
		runFinanceConcurrent(t, 24, func(index int) error {
			switch index % 3 {
			case 0:
				return rollover.EnsureCurrentBudget(ctx, id, before)
			case 1:
				return rollover.EnsureCurrentBudget(ctx, id, after)
			default:
				got, err := rollover.Snapshot(ctx, id, "")
				if err != nil {
					return err
				}
				if got.Month != "2026-11" || got.TotalBudgetCents != 600 ||
					got.RemainingCents != 600 || !reflect.DeepEqual(got.Missing, []string{"transactions"}) {
					return fmt.Errorf("incomplete rollover snapshot: %+v", got)
				}
				for _, stat := range got.Categories {
					if stat.BudgetCents != 100 {
						return fmt.Errorf("mixed rollover category: %+v", stat)
					}
				}
				return nil
			}
		})
		for _, month := range []string{"2026-10", "2026-11"} {
			var budgets, categories int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT b.id), COUNT(bc.category)
				FROM budgets b INNER JOIN budget_categories bc ON bc.budget_id = b.id
				WHERE b.user_id = ? AND b.month = ?`, id, month).Scan(&budgets, &categories); err != nil ||
				budgets != 1 || categories != 6 {
				t.Fatalf("rollover has duplicate or incomplete categories: %s %d %d %v", month, budgets, categories, err)
			}
			budget, err := rollover.GetOrCreateBudget(ctx, id, month)
			if err != nil || budget.Generated || budget.TotalCents != 600 || budget.CategorySumCents != 600 {
				t.Fatalf("rollover month: %+v %v", budget, err)
			}
		}
	})

	t.Run("concurrent read and save never mixes budget versions", func(t *testing.T) {
		id, _ := newUser("finance_concurrent_save")
		_, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(600), Housing: "on_campus",
		})
		if err != nil {
			t.Fatal(err)
		}
		runFinanceConcurrent(t, 24, func(index int) error {
			var budget *Budget
			var err error
			if index%3 == 0 {
				budget, err = service.GetOrCreateBudget(ctx, id, "2026-11")
			} else {
				amount := int64(index + 1)
				budget, err = service.SaveBudget(ctx, id, "2026-11", financeBudgetInput(6*amount, 8000,
					[6]int64{amount, amount, amount, amount, amount, amount}))
			}
			if err != nil {
				return err
			}
			if len(budget.Categories) != 6 || budget.TotalCents != budget.CategorySumCents || budget.UnallocatedCents != 0 {
				return fmt.Errorf("mixed parent and categories: %+v", budget)
			}
			if budget.Source == "saved" {
				for _, category := range budget.Categories {
					if category.BudgetCents*6 != budget.TotalCents {
						return fmt.Errorf("mixed category versions: %+v", budget)
					}
				}
			}
			return nil
		})
	})
}

func financeMySQLDatabase(t *testing.T, envName string) *sql.DB {
	t.Helper()
	db := testdb.Open(t, envName)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatalf("initialize legacy MySQL schema: %v", err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("initialize finance migrations: %v", err)
	}
	return db
}

func financeInt64(value int64) *int64 { return &value }

func financeBudgetInput(total int64, warn int, amounts [6]int64) BudgetInput {
	input := BudgetInput{TotalCents: &total, WarnBP: &warn}
	for i, category := range categoryOrder {
		input.Categories = append(input.Categories, CategoryBudgetInput{Category: category, BudgetCents: financeInt64(amounts[i])})
	}
	return input
}

func financeRequest(t *testing.T, router http.Handler, method, path string, body any, token string, cookie bool) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		if cookie {
			request.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		} else {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeFinanceResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected HTTP %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode finance response: %v", err)
	}
}

func assertFinanceError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error auth.APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("non-structured error: %s", response.Body.String())
	}
	if response.Code != status || body.Error.Code != code || body.Error.Message == "" {
		t.Fatalf("got HTTP %d %s; want %d/%s", response.Code, response.Body.String(), status, code)
	}
	if (code == "validation_failed") != (len(body.Error.Fields) > 0) {
		t.Fatalf("unexpected error fields: %+v", body.Error)
	}
}

func runFinanceConcurrent(t *testing.T, count int, operation func(int) error) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			if err := operation(index); err != nil {
				failures <- fmt.Errorf("operation %d: %w", index, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
