package finance_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/api"
	"unichatgo/internal/auth"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/models"
	"unichatgo/internal/service/assistant"
	"unichatgo/internal/storage"
	"unichatgo/internal/worker"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

// Requires an exclusive empty TEST_MYSQL_REGRESSION_DSN. All business tables use
// real MySQL, and the router mounts the real local auth, legacy and finance
// handlers together with asynchronous execution left disabled.
func TestMySQLPersonalRouteRegression(t *testing.T) {
	fixture := q1Fixture(t)

	t.Run("all personal routes enforce the same authenticated owner", func(t *testing.T) {
		alice, bob := fixture.user(t, "q1_matrix_alice"), fixture.user(t, "q1_matrix_bob")
		type ownedResources struct {
			user    *q1User
			txID    int64
			session int64
		}
		owners := make([]ownedResources, 0, 2)
		for _, user := range []*q1User{alice, bob} {
			record, err := fixture.finance.CreateTransaction(fixture.ctx, user.ID, finance.TransactionInput{
				AmountCents: 700, SpentOn: "2026-11-01", Category: insight.CategoryFood,
			})
			if err != nil {
				t.Fatal(err)
			}
			session, err := fixture.legacy.CreateSession(fixture.ctx, user.ID, "Ownership regression")
			if err != nil {
				t.Fatal(err)
			}
			owners = append(owners, ownedResources{user, record.ID, session.ID})
		}
		structured := map[string]bool{
			"GET /api/users/:id/profile": true, "PUT /api/users/:id/profile": true,
			"GET /api/users/:id/budgets/:month": true, "PUT /api/users/:id/budgets/:month": true,
			"GET /api/users/:id/transactions": true, "POST /api/users/:id/transactions": true,
			"PATCH /api/users/:id/transactions/:tid": true, "DELETE /api/users/:id/transactions/:tid": true,
			"GET /api/users/:id/dashboard": true,
			"DELETE /api/users/:id":        true,
		}
		coveredNew, coveredLegacy := 0, 0
		for _, route := range fixture.router.Routes() {
			if route.Path != "/api/users/:id" && !strings.HasPrefix(route.Path, "/api/users/:id/") {
				continue
			}
			isStructured := structured[route.Method+" "+route.Path]
			if isStructured {
				coveredNew++
			} else {
				coveredLegacy++
			}
			t.Run(route.Method+" "+route.Path, func(t *testing.T) {
				for i, owner := range owners {
					foreign := owners[1-i]
					path := strings.NewReplacer(
						":id", fmt.Sprint(foreign.user.ID), ":tid", fmt.Sprint(foreign.txID),
						":session_id", fmt.Sprint(foreign.session), ":month", "2026-11",
						":request_id", "regression-request",
					).Replace(route.Path)
					body := map[string]any{"user_id": owner.user.ID}
					for _, access := range []string{"cookie", "bearer"} {
						response := fixture.request(t, route.Method, path, body, owner.user, access, true)
						q1Error(t, response, 403, "forbidden", isStructured)
					}
					q1Error(t, fixture.request(t, route.Method, path, body, nil, "", false), 401, "unauthenticated", isStructured)
					if route.Method != "GET" {
						ownPath := strings.NewReplacer(
							":id", fmt.Sprint(owner.user.ID), ":tid", fmt.Sprint(owner.txID),
							":session_id", fmt.Sprint(owner.session), ":month", "2026-11",
							":request_id", "regression-request",
						).Replace(route.Path)
						q1Error(t, fixture.request(t, route.Method, ownPath, body, owner.user, "cookie", false),
							403, "csrf_invalid", isStructured)
					}
				}
			})
		}
		if coveredNew != len(structured) || coveredLegacy != 11 {
			t.Fatalf("personal route surface changed; update centralized coverage: new=%d legacy=%d", coveredNew, coveredLegacy)
		}
		for _, owner := range owners {
			var transactions, sessions int
			if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = ?`, owner.user.ID).Scan(&transactions); err != nil {
				t.Fatal(err)
			}
			if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM sessions WHERE user_id = ?`, owner.user.ID).Scan(&sessions); err != nil {
				t.Fatal(err)
			}
			if transactions != 1 || sessions != 1 {
				t.Fatal("a rejected request mutated an owner's records")
			}
		}
	})

	t.Run("forged identities and CRUD preserve monthly sums and real recommendation facts", func(t *testing.T) {
		alice, bob := fixture.user(t, "q1_finance_alice"), fixture.user(t, "q1_finance_bob")
		for _, user := range []*q1User{alice, bob} {
			profile := map[string]any{
				"monthly_disposable_cents": 5000, "default_budget_cents": 5000,
				"housing": "on_campus", "user_id": bob.ID,
			}
			q1JSON(t, fixture.request(t, "PUT", user.path("/profile")+"?user_id="+fmt.Sprint(bob.ID),
				profile, user, "cookie", true), 200, new(finance.Profile))
			for _, month := range []string{"2026-10", "2026-11"} {
				body := q1BudgetBody()
				body["user_id"] = bob.ID
				q1JSON(t, fixture.request(t, "PUT", user.path("/budgets/"+month)+"?user_id="+fmt.Sprint(bob.ID),
					body, user, "cookie", true), 200, new(finance.Budget))
			}
		}
		var profile finance.Profile
		q1JSON(t, fixture.request(t, "GET", alice.path("/profile")+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"user_id": bob.ID}, alice, "cookie", true), 200, &profile)
		if profile.MonthlyDisposableCents != 5000 {
			t.Fatalf("forged profile identity: %+v", profile)
		}
		// Distinct foreign values make accidental B reads or writes observable.
		bobProfile := map[string]any{"monthly_disposable_cents": 9000, "housing": "with_family"}
		q1JSON(t, fixture.request(t, "PUT", bob.path("/profile"), bobProfile, bob, "cookie", true), 200, new(finance.Profile))
		bobBudget := q1BudgetBody()
		bobBudget["warn_bp"] = 9000
		q1JSON(t, fixture.request(t, "PUT", bob.path("/budgets/2026-11"), bobBudget, bob, "cookie", true), 200, new(finance.Budget))
		var budget finance.Budget
		q1JSON(t, fixture.request(t, "GET", alice.path("/budgets/2026-11")+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"user_id": bob.ID}, alice, "cookie", true), 200, &budget)
		if budget.WarnBP != 8000 || budget.Source != "saved" || budget.Generated {
			t.Fatalf("budget write/read trusted supplied identity or generated missing data: %+v", budget)
		}
		var me auth.CurrentUserInfo
		q1JSON(t, fixture.request(t, "GET", "/api/me?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"user_id": bob.ID}, alice, "cookie", true), 200, &me)
		if me.UserID != alice.ID {
			t.Fatal("/me used a request-supplied identity")
		}
		q1JSON(t, fixture.request(t, "GET", alice.path("/profile")+"?user_id="+fmt.Sprint(bob.ID),
			nil, alice, "cookie", true), 200, &profile)
		if profile.MonthlyDisposableCents != 5000 {
			t.Fatal("profile read used forged query identity")
		}
		create := func(owner *q1User, amount int64, date, category string) finance.Transaction {
			t.Helper()
			var record finance.Transaction
			q1JSON(t, fixture.request(t, "POST", owner.path("/transactions")+"?user_id="+fmt.Sprint(bob.ID),
				map[string]any{"amount_cents": amount, "spent_on": date, "category": category, "user_id": bob.ID},
				owner, "cookie", true), 201, &record)
			return record
		}
		foreign := create(bob, 777, "2026-11-01", "housing_tuition")
		first := create(alice, 100, "2026-11-01", "food")
		second := create(alice, 200, "2026-11-02", "transport")
		create(alice, 300, "2026-10-31", "food")
		fixture.assertMonth(t, alice, bob.ID, "2026-11", 300, map[insight.Category]int64{insight.CategoryFood: 100, insight.CategoryTransport: 200})
		fixture.assertMonth(t, alice, bob.ID, "2026-10", 300, map[insight.Category]int64{insight.CategoryFood: 300})
		for _, method := range []string{"PATCH", "DELETE"} {
			q1Error(t, fixture.request(t, method, alice.path(fmt.Sprintf("/transactions/%d", foreign.ID)),
				map[string]any{"amount_cents": 1, "user_id": bob.ID}, alice, "cookie", true), 404, "not_found", true)
		}
		var changed finance.Transaction
		q1JSON(t, fixture.request(t, "PATCH", alice.path(fmt.Sprintf("/transactions/%d", first.ID)),
			map[string]any{"amount_cents": 400, "category": "transport", "user_id": bob.ID},
			alice, "cookie", true), 200, &changed)
		fixture.assertMonth(t, alice, bob.ID, "2026-11", 600, map[insight.Category]int64{insight.CategoryTransport: 600})
		q1JSON(t, fixture.request(t, "PATCH", alice.path(fmt.Sprintf("/transactions/%d", second.ID))+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"spent_on": "2026-10-30", "category": "food", "user_id": bob.ID},
			alice, "cookie", true), 200, &changed)
		fixture.assertMonth(t, alice, bob.ID, "2026-11", 400, map[insight.Category]int64{insight.CategoryTransport: 400})
		fixture.assertMonth(t, alice, bob.ID, "2026-10", 500, map[insight.Category]int64{insight.CategoryFood: 500})
		response := fixture.request(t, "DELETE", alice.path(fmt.Sprintf("/transactions/%d", first.ID))+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"user_id": bob.ID}, alice, "cookie", true)
		if response.Code != 204 {
			t.Fatalf("delete own transaction: %d %s", response.Code, response.Body.String())
		}
		fixture.assertMonth(t, alice, bob.ID, "2026-11", 0, map[insight.Category]int64{})
		fixture.assertMonth(t, alice, bob.ID, "2026-10", 500, map[insight.Category]int64{insight.CategoryFood: 500})
		fixture.assertMonth(t, bob, alice.ID, "2026-11", 777, map[insight.Category]int64{insight.CategoryHousingTuition: 777})
	})

	t.Run("account deletion cascades only the authenticated account and revokes every session", func(t *testing.T) {
		alice, bob := fixture.user(t, "q1_delete_alice"), fixture.user(t, "q1_delete_bob")
		budgetIDs := make(map[int64]int64)
		for _, user := range []*q1User{alice, bob} {
			q1JSON(t, fixture.request(t, "PUT", user.path("/profile"),
				map[string]any{"monthly_disposable_cents": 5000, "housing": "on_campus"},
				user, "cookie", true), 200, new(finance.Profile))
			q1JSON(t, fixture.request(t, "PUT", user.path("/budgets/2026-11"),
				q1BudgetBody(), user, "cookie", true), 200, new(finance.Budget))
			q1JSON(t, fixture.request(t, "POST", user.path("/transactions"),
				map[string]any{"amount_cents": 650, "spent_on": "2026-11-01", "category": "food"},
				user, "cookie", true), 201, new(finance.Transaction))
			var budgetID int64
			if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT id FROM budgets WHERE user_id = ? AND month = ?`,
				user.ID, "2026-11").Scan(&budgetID); err != nil {
				t.Fatal(err)
			}
			budgetIDs[user.ID] = budgetID
			session, err := fixture.legacy.CreateSession(fixture.ctx, user.ID, "Account regression history")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.legacy.AddMessage(fixture.ctx, models.Message{
				UserID: user.ID, SessionID: session.ID, Role: models.RoleUser, Content: "Account regression message",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.legacy.RecordTempFile(fixture.ctx, user.ID, session.ID,
				"sample.txt", filepath.Join(t.TempDir(), "sample.txt"), "text/plain", 16, time.Hour); err != nil {
				t.Fatal(err)
			}
			response := fixture.request(t, "POST", user.path("/token"),
				map[string]any{"provider": "openai", "token": "phase7-account-placeholder-only"}, user, "cookie", true)
			if response.Code != 204 {
				t.Fatalf("prepare legacy key for deletion: %d %s", response.Code, response.Body.String())
			}
		}
		secondToken, err := fixture.auth.IssueToken(fixture.ctx, alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		aliceBefore := fixture.personalCounts(t, alice.ID, budgetIDs[alice.ID])
		bobBefore := fixture.personalCounts(t, bob.ID, budgetIDs[bob.ID])
		if aliceBefore["user_tokens"] != 2 || aliceBefore["budget_categories"] != 6 {
			t.Fatalf("account fixture does not exercise multiple sessions/categories: %+v", aliceBefore)
		}
		for table, count := range aliceBefore {
			if count < 1 {
				t.Fatalf("account fixture omitted personal table %s", table)
			}
		}
		if _, err := fixture.db.ExecContext(fixture.ctx, `INSERT INTO eval_events (participant_code, task, event, received_at)
			VALUES ('P98', 'onboarding', 'start', ?)`, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		publicBefore := fixture.publicCounts(t)
		confirm := map[string]any{"confirm": "DELETE", "user_id": bob.ID, "cognito_username": "ignored-request-identity"}
		q1Error(t, fixture.request(t, "DELETE", alice.path(""), confirm, nil, "", false), 401, "unauthenticated", true)
		q1Error(t, fixture.request(t, "DELETE", bob.path(""), confirm, alice, "cookie", true), 403, "forbidden", true)
		q1Error(t, fixture.request(t, "DELETE", alice.path(""), confirm, alice, "cookie", false), 403, "csrf_invalid", true)
		for _, body := range []any{
			map[string]any{}, map[string]any{"confirm": nil}, map[string]any{"confirm": "delete"},
			map[string]any{"confirm": " DELETE "}, map[string]any{"user_id": bob.ID},
		} {
			q1Error(t, fixture.request(t, "DELETE", alice.path(""), body, alice, "cookie", true), 400, "validation_failed", true)
		}
		q1Error(t, fixture.request(t, "DELETE", alice.path(""), map[string]any{"confirm": 1}, alice, "cookie", true),
			400, "bad_request", true)
		if after := fixture.personalCounts(t, alice.ID, budgetIDs[alice.ID]); !reflect.DeepEqual(after, aliceBefore) {
			t.Fatalf("rejected deletion changed personal data: before=%v after=%v", aliceBefore, after)
		}
		response := fixture.request(t, "DELETE", alice.path("")+"?user_id="+fmt.Sprint(bob.ID), confirm, alice, "cookie", true)
		if response.Code != 204 || response.Body.Len() != 0 {
			t.Fatalf("delete account: %d %s", response.Code, response.Body.String())
		}
		cleared := make(map[string]bool)
		for _, cookie := range response.Result().Cookies() {
			if cookie.Value != "" || cookie.MaxAge >= 0 || cookie.Path != "/" {
				t.Fatalf("account deletion did not clear %s", cookie.Name)
			}
			cleared[cookie.Name] = true
		}
		if !cleared["auth_token"] || !cleared["csrf_token"] {
			t.Fatal("account deletion must clear both session cookies")
		}
		for table, count := range fixture.personalCounts(t, alice.ID, budgetIDs[alice.ID]) {
			if count != 0 {
				t.Fatalf("deleted account retained %d rows in %s", count, table)
			}
		}
		if after := fixture.personalCounts(t, bob.ID, budgetIDs[bob.ID]); !reflect.DeepEqual(after, bobBefore) {
			t.Fatalf("account deletion changed B: before=%v after=%v", bobBefore, after)
		}
		if after := fixture.publicCounts(t); !reflect.DeepEqual(after, publicBefore) {
			t.Fatalf("account deletion changed anonymous/public data: before=%v after=%v", publicBefore, after)
		}
		for _, access := range []string{"cookie", "bearer"} {
			for _, suffix := range []string{"/profile", "/budgets/2026-11", "/transactions", "/dashboard"} {
				q1Error(t, fixture.request(t, "GET", alice.path(suffix), nil, alice, access, true), 401, "unauthenticated", true)
			}
			q1Error(t, fixture.request(t, "GET", "/api/me", nil, alice, access, true), 401, "unauthenticated", true)
			q1Error(t, fixture.request(t, "GET", alice.path("/token"), nil, alice, access, true), 401, "", false)
		}
		secondSession := &q1User{ID: alice.ID, Token: secondToken}
		q1Error(t, fixture.request(t, "GET", secondSession.path("/dashboard"), nil, secondSession, "bearer", false),
			401, "unauthenticated", true)
		q1Error(t, fixture.request(t, "GET", secondSession.path("/token"), nil, secondSession, "bearer", false), 401, "", false)
		var other auth.CurrentUserInfo
		q1JSON(t, fixture.request(t, "GET", "/api/me", nil, bob, "cookie", true), 200, &other)
		if other.UserID != bob.ID {
			t.Fatal("B's identity changed after A deletion")
		}
		fixture.assertMonth(t, bob, alice.ID, "2026-11", 650, map[insight.Category]int64{insight.CategoryFood: 650})
		if response := fixture.request(t, "GET", bob.path("/token"), nil, bob, "cookie", true); response.Code != 200 {
			t.Fatal("B's legacy session stopped working after A deletion")
		}
	})

	t.Run("legacy credentials and histories remain isolated and logout revokes all surfaces", func(t *testing.T) {
		alice, bob := fixture.user(t, "q1_legacy_alice"), fixture.user(t, "q1_legacy_bob")
		sessions := make(map[int64]*models.Session)
		for _, user := range []*q1User{alice, bob} {
			session, err := fixture.legacy.CreateSession(fixture.ctx, user.ID, fmt.Sprintf("Owned by %d", user.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.legacy.AddMessage(fixture.ctx, models.Message{
				UserID: user.ID, SessionID: session.ID, Role: models.RoleUser, Content: fmt.Sprintf("owner-%d", user.ID),
			}); err != nil {
				t.Fatal(err)
			}
			sessions[user.ID] = session
		}
		for _, method := range []string{"GET", "DELETE"} {
			path := alice.path(fmt.Sprintf("/conversation/sessions/%d", sessions[bob.ID].ID))
			if method == "GET" {
				path += "/messages"
			}
			q1Error(t, fixture.request(t, method, path+"?user_id="+fmt.Sprint(bob.ID),
				map[string]any{"user_id": bob.ID}, alice, "cookie", true), 404, "", false)
		}
		var listing struct {
			Sessions []models.Session `json:"session_list"`
		}
		q1JSON(t, fixture.request(t, "POST", alice.path("/conversation/session-list")+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"user_id": bob.ID}, alice, "cookie", true), 200, &listing)
		if len(listing.Sessions) != 1 || listing.Sessions[0].ID != sessions[alice.ID].ID {
			t.Fatal("legacy session listing trusted a supplied identity")
		}
		var history struct {
			Session  models.Session   `json:"session"`
			Messages []models.Message `json:"messages"`
		}
		q1JSON(t, fixture.request(t, "GET", bob.path(fmt.Sprintf("/conversation/sessions/%d/messages", sessions[bob.ID].ID)),
			nil, bob, "cookie", true), 200, &history)
		if history.Session.UserID != bob.ID || len(history.Messages) != 1 || history.Messages[0].Content != fmt.Sprintf("owner-%d", bob.ID) {
			t.Fatal("rejected foreign session deletion affected its owner's history")
		}
		response := fixture.request(t, "POST", alice.path("/token")+"?user_id="+fmt.Sprint(bob.ID),
			map[string]any{"provider": "openai", "token": "phase7-placeholder-only", "user_id": bob.ID}, alice, "cookie", true)
		if response.Code != 204 {
			t.Fatalf("legacy token write: %d %s", response.Code, response.Body.String())
		}
		var aliceKeys, bobKeys int
		if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM apiKeys WHERE user_id = ?`, alice.ID).Scan(&aliceKeys); err != nil {
			t.Fatal(err)
		}
		if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM apiKeys WHERE user_id = ?`, bob.ID).Scan(&bobKeys); err != nil || aliceKeys != 1 || bobKeys != 0 {
			t.Fatalf("legacy token ownership changed: alice=%d bob=%d err=%v", aliceKeys, bobKeys, err)
		}
		var capabilities struct {
			Async bool `json:"async_execution"`
		}
		q1JSON(t, fixture.request(t, "GET", "/api/capabilities", nil, nil, "", false), 200, &capabilities)
		if capabilities.Async {
			t.Fatal("regression fixture accidentally enabled the legacy async framework")
		}
		response = fixture.request(t, "POST", "/api/auth/logout",
			map[string]any{"user_id": bob.ID}, alice, "cookie", true)
		if response.Code != 200 {
			t.Fatalf("logout: %d %s", response.Code, response.Body.String())
		}
		q1Error(t, fixture.request(t, "GET", alice.path("/profile"), nil, alice, "cookie", true), 401, "unauthenticated", true)
		q1Error(t, fixture.request(t, "GET", alice.path("/token"), nil, alice, "cookie", true), 401, "", false)
		if response := fixture.request(t, "GET", bob.path("/token"), nil, bob, "cookie", true); response.Code != 200 {
			t.Fatal("A logout invalidated B's session")
		}
	})
}

type q1User struct {
	ID      int64
	Token   string
	CSRF    string
	Cookies []*http.Cookie
}

func (user *q1User) path(suffix string) string {
	return fmt.Sprintf("/api/users/%d%s", user.ID, suffix)
}

type q1Environment struct {
	ctx     context.Context
	db      *sql.DB
	router  *gin.Engine
	auth    *auth.Service
	finance *finance.Service
	legacy  *assistant.Service
}

func q1Fixture(t *testing.T) *q1Environment {
	t.Helper()
	db := testdb.Open(t, "TEST_MYSQL_REGRESSION_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNICHATGO_APIKEY_KEY", base64.StdEncoding.EncodeToString(key[:]))
	legacy, err := assistant.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(db, nil, time.Hour)
	applicationAuth, err := auth.NewHTTPHandler(authService, auth.ModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	account, err := auth.NewAccountHTTPHandler(authService, auth.ModeLocal, nil)
	if err != nil {
		t.Fatal(err)
	}
	applicationAuth.SetAccountHandler(account)
	router := gin.New()
	api.NewHandler(legacy, authService, worker.DispatcherConfig{MinWorkers: 1, MaxWorkers: 1, QueueSize: 1},
		t.TempDir(), assistant.DefaultTempFileTTL, nil).RegisterRoutes(router, applicationAuth)
	now := func() time.Time { return time.Date(2026, 11, 12, 4, 0, 0, 0, time.UTC) }
	financeService := finance.NewService(db, finance.WithClock(now))
	applicationAuth.RegisterUserRoutes(router.Group("/api"), financeService)
	financeHTTP := finance.NewHTTPHandler(financeService, authService)
	financeHTTP.RegisterRoutes(router.Group("/api"))
	financeHTTP.RegisterTransactionRoutes(router.Group("/api"))
	financeHTTP.RegisterDashboardRoutes(router.Group("/api"))
	return &q1Environment{ctx: ctx, db: db, router: router, auth: authService, finance: financeService, legacy: legacy}
}

func (fixture *q1Environment) personalCounts(t *testing.T, userID, budgetID int64) map[string]int {
	t.Helper()
	result := make(map[string]int)
	for _, table := range []string{"users", "user_profiles", "budgets", "transactions", "sessions", "messages", "temp_files", "apiKeys", "user_tokens"} {
		column := "user_id"
		if table == "users" {
			column = "id"
		}
		var count int
		if err := fixture.db.QueryRowContext(fixture.ctx, "SELECT COUNT(*) FROM `"+table+"` WHERE "+column+" = ?", userID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		result[table] = count
	}
	var categories int
	// Keep the captured budget ID after its parent is deleted so orphaned
	// categories would be visible instead of disappearing behind a JOIN.
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM budget_categories WHERE budget_id = ?`, budgetID).Scan(&categories); err != nil {
		t.Fatal(err)
	}
	result["budget_categories"] = categories
	return result
}

func (fixture *q1Environment) publicCounts(t *testing.T) map[string]int {
	t.Helper()
	result := make(map[string]int)
	for _, table := range []string{"eval_events"} {
		var count int
		if err := fixture.db.QueryRowContext(fixture.ctx, "SELECT COUNT(*) FROM `"+table+"`").Scan(&count); err != nil {
			t.Fatal(err)
		}
		result[table] = count
	}
	return result
}

func (fixture *q1Environment) user(t *testing.T, name string) *q1User {
	t.Helper()
	credentials := map[string]string{"username": name, "password": "Phase7-test-password-only"}
	var registered struct {
		ID int64 `json:"id"`
	}
	q1JSON(t, fixture.request(t, "POST", "/api/users/register", credentials, nil, "", false), 201, &registered)
	response := fixture.request(t, "POST", "/api/users/login", credentials, nil, "", false)
	var login struct {
		Token string `json:"auth_token"`
	}
	q1JSON(t, response, 200, &login)
	user := &q1User{ID: registered.ID, Token: login.Token, Cookies: response.Result().Cookies()}
	for _, cookie := range user.Cookies {
		if cookie.Name == "csrf_token" {
			user.CSRF = cookie.Value
		}
	}
	if user.ID <= 0 || user.Token == "" || user.CSRF == "" {
		t.Fatal("local login did not issue a complete session")
	}
	return user
}

func (fixture *q1Environment) request(t *testing.T, method, path string, body any, user *q1User, access string, csrf bool) *httptest.ResponseRecorder {
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
	if user != nil {
		if access == "bearer" {
			request.Header.Set("Authorization", "Bearer "+user.Token)
		} else {
			for _, cookie := range user.Cookies {
				request.AddCookie(cookie)
			}
			if csrf {
				request.Header.Set("X-CSRF-Token", user.CSRF)
			}
		}
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func (fixture *q1Environment) assertMonth(t *testing.T, user *q1User, forgedID int64, month string, total int64, expected map[insight.Category]int64) {
	t.Helper()
	query := fmt.Sprintf("?month=%s&user_id=%d", month, forgedID)
	var page finance.TransactionPage
	q1JSON(t, fixture.request(t, "GET", user.path("/transactions")+query,
		map[string]any{"user_id": forgedID}, user, "cookie", true), 200, &page)
	fromRecords := make(map[insight.Category]int64)
	var sum int64
	for _, transaction := range page.Items {
		fromRecords[transaction.Category] += transaction.AmountCents
		sum += transaction.AmountCents
		if !strings.HasPrefix(transaction.SpentOn, month+"-") {
			t.Fatalf("another month leaked into listing: %+v", transaction)
		}
	}
	if sum != total || !reflect.DeepEqual(fromRecords, expected) || int64(len(page.Items)) != page.Total {
		t.Fatalf("transaction listing differs from expected month: %+v", page)
	}
	var snapshot insight.Snapshot
	q1JSON(t, fixture.request(t, "GET", user.path("/dashboard")+query,
		map[string]any{"user_id": forgedID}, user, "cookie", true), 200, &snapshot)
	var categorySum int64
	for _, stat := range snapshot.Categories {
		categorySum += stat.SpentCents
		if stat.SpentCents != expected[stat.Category] {
			t.Fatalf("category does not match record listing: %+v", stat)
		}
	}
	if len(snapshot.Categories) != 6 || snapshot.TotalSpentCents != total || categorySum != total ||
		snapshot.TransactionCount != len(page.Items) || snapshot.RemainingCents != 5000-total {
		t.Fatalf("dashboard/list/category mismatch: %+v", snapshot)
	}
}

func q1BudgetBody() map[string]any {
	categories := []map[string]any{}
	for _, category := range []string{"food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"} {
		categories = append(categories, map[string]any{"category": category, "budget_cents": 1000})
	}
	return map[string]any{"total_cents": 5000, "warn_bp": 8000, "categories": categories}
}

func q1JSON(t *testing.T, response *httptest.ResponseRecorder, status int, target any) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("want HTTP %d, got %d %s", status, response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatal(err)
	}
}

func q1Error(t *testing.T, response *httptest.ResponseRecorder, status int, code string, structured bool) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("want HTTP %d, got %d %s", status, response.Code, response.Body.String())
	}
	if structured {
		var result struct {
			Error auth.APIError `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Error.Code != code || result.Error.Message == "" {
			t.Fatalf("structured error changed: %s %v", response.Body.String(), err)
		}
	} else {
		var result struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Error == "" {
			t.Fatalf("legacy error format changed: %s %v", response.Body.String(), err)
		}
	}
}
