package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

func TestCurrentUserRequiresValidIdentityAndEmailSource(t *testing.T) {
	service := NewService(nil, nil, time.Hour)
	for _, id := range []int64{0, -1} {
		if _, err := service.CurrentUser(context.Background(), id, ModeLocal, time.Now()); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("invalid identity %d: %v", id, err)
		}
	}
	for _, mode := range []string{ModeCognito, "invalid"} {
		if _, err := service.CurrentUser(context.Background(), 1, mode, time.Now()); !errors.Is(err, ErrCurrentUserUnavailable) {
			t.Fatalf("unsupported identity source %q: %v", mode, err)
		}
	}
}

func TestMySQLCurrentUser(t *testing.T) {
	db := openMeMySQLDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	service := NewService(db, nil, time.Hour)
	newUser := func(username string) int64 {
		t.Helper()
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
			VALUES (?, 'integration-test-only', ?)`, username, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	saveProfile := func(id int64) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO user_profiles
			(user_id, monthly_disposable_cents, housing, default_budget_cents)
			VALUES (?, 100000, 'on_campus', 100000)`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	saveBudget := func(id int64, month, source string) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO budgets (user_id, month, total_cents, source)
			VALUES (?, ?, 100000, ?)`, id, month, source)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	id := newUser("me_owner")
	other := newUser("me_other")
	check := func(at time.Time, month string, onboarded bool) {
		t.Helper()
		user, err := service.CurrentUser(ctx, id, ModeLocal, at)
		if err != nil {
			t.Fatal(err)
		}
		if user.UserID != id || user.Email != "me_owner" || user.AuthMode != ModeLocal ||
			user.CurrentMonth != month || user.Onboarded != onboarded {
			t.Fatalf("unexpected identity/onboarding at %s: %+v", at.Format(time.RFC3339), user)
		}
	}
	check(before, "2026-10", false)
	saveProfile(other)
	saveBudget(other, "2026-10", "saved")
	check(before, "2026-10", false)
	saveProfile(id)
	check(before, "2026-10", false)
	saveBudget(id, "2026-09", "saved")
	check(before, "2026-10", false)
	saveBudget(id, "2026-12", "saved")
	check(before, "2026-10", false)
	saveBudget(id, "2026-10", "saved")
	check(before, "2026-10", true)
	check(after, "2026-11", false)

	var missingMonth int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ? AND month = ?`, id, "2026-11").Scan(&missingMonth); err != nil || missingMonth != 0 {
		t.Fatalf("identity reader unexpectedly generated a budget: count=%d err=%v", missingMonth, err)
	}
	saveBudget(id, "2026-11", "copied_from_2026-10")
	check(after, "2026-11", true)
	if _, err := service.CurrentUser(ctx, id+10000, ModeLocal, after); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("deleted or missing identity: %v", err)
	}
	budgetOnly := newUser("me_budget_only")
	saveBudget(budgetOnly, "2026-11", "saved")
	user, err := service.CurrentUser(ctx, budgetOnly, ModeLocal, after)
	if err != nil || user.Onboarded {
		t.Fatalf("budget without a profile: user=%+v err=%v", user, err)
	}
	defaultMode, err := service.CurrentUser(ctx, id, "", after)
	if err != nil || defaultMode.AuthMode != ModeLocal || !defaultMode.Onboarded {
		t.Fatalf("default local mode: user=%+v err=%v", defaultMode, err)
	}

	// Exercise the actual HTTP authentication middleware with both Cookie and
	// Bearer access. Query parameters and request bodies cannot select a user.
	router := gin.New()
	handler, err := NewHTTPHandler(service, ModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	initializerCalls := 0
	initializer := currentBudgetInitializerFunc(func(ctx context.Context, userID int64, now time.Time) error {
		initializerCalls++
		if userID != id || now.IsZero() || ctx == nil {
			t.Fatalf("initializer received invalid request identity/time: id=%d now=%v", userID, now)
		}
		return nil
	})
	handler.RegisterUserRoutes(router.Group("/api"), initializer)
	assertAuthError(t, authRequest(t, router, "GET", "/api/me", nil, nil, nil), 401, "unauthenticated")
	if initializerCalls != 0 {
		t.Fatal("unauthenticated request initialized a budget")
	}
	token, err := service.IssueToken(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	response := authRequest(t, router, "GET", fmt.Sprintf("/api/me?user_id=%d", other),
		map[string]int64{"user_id": other}, []*http.Cookie{{Name: "auth_token", Value: token}}, nil)
	var result CurrentUserInfo
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	currentMonth := time.Now().In(meSingapore).Format("2006-01")
	if response.Code != 200 || result.UserID != id || result.Email != "me_owner" ||
		result.AuthMode != ModeLocal || result.CurrentMonth != currentMonth {
		t.Fatalf("cookie /me response: status=%d body=%s", response.Code, response.Body.String())
	}
	var fields map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil || len(fields) != 5 {
		t.Fatalf("expected exactly five contract fields: %s err=%v", response.Body.String(), err)
	}
	response = authRequest(t, router, "GET", "/api/me", nil, nil, map[string]string{"Authorization": "Bearer " + token})
	if response.Code != 200 {
		t.Fatalf("Bearer /me: status=%d body=%s", response.Code, response.Body.String())
	}
	cognitoRouter := gin.New()
	cognitoHandler, err := NewHTTPHandler(service, ModeCognito)
	if err != nil {
		t.Fatal(err)
	}
	cognitoHandler.RegisterUserRoutes(cognitoRouter.Group("/api"), initializer)
	response = authRequest(t, cognitoRouter, "GET", "/api/me", nil, nil, map[string]string{"Authorization": "Bearer " + token})
	assertAuthError(t, response, 503, "unavailable")
	if _, err := db.ExecContext(ctx, `UPDATE users SET cognito_sub = ?, email = ? WHERE id = ?`,
		"f65d438e-174e-40a1-9a80-e4098385599b", "verified-token-claim@example.org", id); err != nil {
		t.Fatal(err)
	}
	response = authRequest(t, cognitoRouter, "GET", fmt.Sprintf("/api/me?user_id=%d", other),
		nil, nil, map[string]string{"Authorization": "Bearer " + token})
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
		response.Code != 200 || result.UserID != id || result.Email != "verified-token-claim@example.org" ||
		result.AuthMode != ModeCognito || result.CurrentMonth != currentMonth {
		t.Fatalf("Cognito /me must use its authenticated identity's stored claim: %d %s %v",
			response.Code, response.Body.String(), err)
	}
	if err := service.RevokeToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	assertAuthError(t, authRequest(t, router, "GET", "/api/me", nil,
		[]*http.Cookie{{Name: "auth_token", Value: token}}, nil), 401, "unauthenticated")
	if initializerCalls != 4 {
		t.Fatalf("expected one initializer call for each authenticated /me: %d", initializerCalls)
	}
}

type currentBudgetInitializerFunc func(context.Context, int64, time.Time) error

func (f currentBudgetInitializerFunc) EnsureCurrentBudget(ctx context.Context, id int64, now time.Time) error {
	return f(ctx, id, now)
}

func openMeMySQLDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := testdb.Open(t, "TEST_MYSQL_ME_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatalf("initialize /me legacy schema: %v", err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("initialize /me finance schema: %v", err)
	}
	return db
}
