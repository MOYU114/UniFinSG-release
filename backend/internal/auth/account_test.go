package auth

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type accountDeleteFunc func(context.Context, string) error

func (delete accountDeleteFunc) DeleteUser(ctx context.Context, username string) error {
	return delete(ctx, username)
}

func TestAccountLocalDeletionRevokesAllSessionsAndCascades(t *testing.T) {
	called := false
	router, service, db := newAccountTestServer(t, ModeLocal, accountDeleteFunc(func(context.Context, string) error {
		called = true
		return nil
	}))
	token, second, other := accountTestTokens(t, service)
	now := time.Now().UTC()
	for _, statement := range []string{
		`INSERT INTO sessions (id, user_id, title, created_at, updated_at) VALUES (1, 1, 'Test chat', ?, ?)`,
		`INSERT INTO sessions (id, user_id, title, created_at, updated_at) VALUES (2, 2, 'Other chat', ?, ?)`,
	} {
		if _, err := db.Exec(statement, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO messages (user_id, session_id, role, content, created_at)
		VALUES (1, 1, 'user', 'Test message', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO apiKeys (user_id, provider, api_key, created_at)
		VALUES (1, 'test-provider', 'test-only-value', ?)`, now); err != nil {
		t.Fatal(err)
	}
	response := accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, token, true)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || called {
		t.Fatalf("unexpected local deletion: status=%d body=%q provider_called=%v", response.Code, response.Body.String(), called)
	}
	for _, table := range []string{"sessions", "messages", "apiKeys", "user_tokens"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE user_id = ?", 1).Scan(&count); err != nil || count != 0 {
			t.Fatalf("personal rows remain in %s: count=%d err=%v", table, count, err)
		}
	}
	for _, revoked := range []string{token, second} {
		if _, err := service.ValidateToken(context.Background(), revoked); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("deleted user's session remains usable: %v", err)
		}
	}
	if userID, err := service.ValidateToken(context.Background(), other); err != nil || userID != 2 {
		t.Fatalf("other user's session changed: id=%d err=%v", userID, err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected both session cookies to be cleared, got %d", len(cookies))
	}
	for _, cookie := range cookies {
		if cookie.MaxAge >= 0 || cookie.Value != "" || cookie.Path != "/" {
			t.Fatalf("cookie was not cleared: name=%s", cookie.Name)
		}
		if cookie.Name == service.AuthCookieName() && (!cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode) {
			t.Fatal("cleared auth cookie attributes changed")
		}
		if cookie.Name == service.CSRFCookieName() && (cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode) {
			t.Fatal("cleared CSRF cookie attributes changed")
		}
	}
}

func TestAccountDeletionRequiresOwnershipCSRFAndConfirmation(t *testing.T) {
	router, service, db := newAccountTestServer(t, ModeLocal, nil)
	token, _, _ := accountTestTokens(t, service)
	assertAuthError(t, accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, "", true), http.StatusUnauthorized, "unauthenticated")
	assertAuthError(t, accountRequest(router, "/api/users/2", `{"confirm":"DELETE"}`, token, true), http.StatusForbidden, "forbidden")
	assertAuthError(t, accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, token, false), http.StatusForbidden, "csrf_invalid")
	for _, raw := range []string{``, `null`, `[]`, `"DELETE"`, `{`, `{} {}`, `{"confirm":1}`, `{"confirm":[]}`} {
		assertAuthError(t, accountRequest(router, "/api/users/1", raw, token, true), http.StatusBadRequest, "bad_request")
	}
	for _, raw := range []string{`{}`, `{"confirm":null}`, `{"confirm":"delete"}`, `{"confirm":" DELETE "}`,
		`{"confirm":"DELETE","confirm":null}`, `{"user_id":1,"username":"DELETE"}`} {
		assertAuthError(t, accountRequest(router, "/api/users/1", raw, token, true), http.StatusBadRequest, "validation_failed")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, 1).Scan(&count); err != nil || count != 1 {
		t.Fatalf("a rejected request changed the account: count=%d err=%v", count, err)
	}
	if _, err := service.ValidateToken(context.Background(), token); err != nil {
		t.Fatalf("a rejected request revoked the session: %v", err)
	}
}

func TestAccountCognitoDeletionUsesStoredIdentityAfterCommit(t *testing.T) {
	for _, providerFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "provider_success", true: "provider_failure"}[providerFailure], func(t *testing.T) {
			var db *sql.DB
			calls := 0
			router, service, database := newAccountTestServer(t, ModeCognito, accountDeleteFunc(func(ctx context.Context, username string) error {
				calls++
				if username != "verified-provider-username" {
					t.Fatalf("provider identity came from request or another account: %q", username)
				}
				var count int
				if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, 1).Scan(&count); err != nil || count != 0 {
					t.Fatalf("provider deletion happened before local commit: count=%d err=%v", count, err)
				}
				if providerFailure {
					return errors.New("verified-provider-username sensitive-provider-detail")
				}
				return nil
			}))
			db = database
			if _, err := db.Exec(`UPDATE users SET cognito_username = ? WHERE id = ?`, "verified-provider-username", 1); err != nil {
				t.Fatal(err)
			}
			token, _, _ := accountTestTokens(t, service)
			var logs bytes.Buffer
			oldWriter := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(oldWriter)
			response := accountRequest(router, "/api/users/1",
				`{"confirm":"DELETE","user_id":2,"username":"attacker","cognito_username":"attacker","sub":"attacker"}`, token, true)
			if response.Code != http.StatusNoContent || calls != 1 {
				t.Fatalf("deletion status=%d calls=%d body=%q", response.Code, calls, response.Body.String())
			}
			if strings.Contains(logs.String(), "verified-provider") || strings.Contains(logs.String(), "sensitive") {
				t.Fatal("provider failure log exposed identity or provider details")
			}
			if providerFailure && !strings.Contains(logs.String(), "warning:") {
				t.Fatal("provider failure did not produce a warning")
			}
		})
	}
}

func TestAccountMissingCognitoIdentityStillDeletesLocalAccount(t *testing.T) {
	calls := 0
	router, service, db := newAccountTestServer(t, ModeCognito, accountDeleteFunc(func(context.Context, string) error {
		calls++
		return nil
	}))
	token, _, _ := accountTestTokens(t, service)
	response := accountRequest(router, "/api/users/1", `{"confirm":"DELETE","username":"guessed","sub":"guessed"}`, token, true)
	if response.Code != http.StatusNoContent || calls != 0 {
		t.Fatalf("missing provider identity caused incorrect behavior: status=%d calls=%d", response.Code, calls)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, 1).Scan(&count); err != nil || count != 0 {
		t.Fatalf("local account remains: %d %v", count, err)
	}
}

func TestAccountDeletionRollsBackBeforeProviderCallOnDatabaseFailure(t *testing.T) {
	calls := 0
	router, service, db := newAccountTestServer(t, ModeCognito, accountDeleteFunc(func(context.Context, string) error {
		calls++
		return nil
	}))
	if _, err := db.Exec(`UPDATE users SET cognito_username = 'verified-provider-username' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER deny_account_delete BEFORE DELETE ON users
		BEGIN SELECT RAISE(ABORT, 'private database detail'); END`); err != nil {
		t.Fatal(err)
	}
	token, second, _ := accountTestTokens(t, service)
	response := accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, token, true)
	assertAuthError(t, response, http.StatusServiceUnavailable, "unavailable")
	if calls != 0 || strings.Contains(response.Body.String(), "private") || len(response.Result().Cookies()) != 0 {
		t.Fatal("failed local deletion attempted provider deletion, exposed details, or cleared cookies")
	}
	for _, preserved := range []string{token, second} {
		if _, err := service.ValidateToken(context.Background(), preserved); err != nil {
			t.Fatalf("failed deletion changed a session: %v", err)
		}
	}
}

func TestAccountBearerDeletionAndProductionCookies(t *testing.T) {
	previousMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(previousMode)
	router, service, _ := newAccountTestServer(t, ModeLocal, nil)
	token, _, _ := accountTestTokens(t, service)
	req := httptest.NewRequest(http.MethodDelete, "/api/users/1", strings.NewReader(`{"confirm":"DELETE"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("bearer deletion failed: %d %s", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if !cookie.Secure {
			t.Fatal("production cookie was not Secure")
		}
	}
}

func TestAccountHandlerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewAccountHTTPHandler(nil, ModeLocal, nil); err == nil {
		t.Fatal("nil service accepted")
	}
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	service := NewService(db, nil, time.Hour)
	if _, err := NewAccountHTTPHandler(service, "invalid", nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if handler, err := NewAccountHTTPHandler(service, "", nil); err != nil || handler.mode != ModeLocal {
		t.Fatalf("empty mode should default to local: %v", err)
	}
}

func TestAccountDeletionApplicationCleanupOnlyAfterOwnCommit(t *testing.T) {
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, id := range []int64{1, 2} {
		if _, err := db.Exec(`INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, '', ?)`,
			id, fmt.Sprintf("cleanup_user_%d", id), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db, nil, time.Hour)
	handler, err := NewAccountHTTPHandler(service, ModeLocal, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cleaned []int64
	handler.SetAfterLocalDelete(func(userID int64) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cleanup ran before successful local commit: count=%d err=%v", count, err)
		}
		cleaned = append(cleaned, userID)
	})
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	token, _, otherToken := accountTestTokens(t, service)
	assertAuthError(t, accountRequest(router, "/api/users/2", `{"confirm":"DELETE"}`, token, true), 403, "forbidden")
	assertAuthError(t, accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, token, false), 403, "csrf_invalid")
	assertAuthError(t, accountRequest(router, "/api/users/1", `{"confirm":"delete"}`, token, true), 400, "validation_failed")
	assertAuthError(t, accountRequest(router, "/api/users/1",
		`{"confirm":"DELETE","padding":"`+strings.Repeat("x", 8192)+`"}`, token, true), 400, "bad_request")
	if len(cleaned) != 0 {
		t.Fatal("a rejected request stopped application work")
	}
	if _, err := db.Exec(`CREATE TRIGGER deny_cleanup_delete BEFORE DELETE ON users WHEN OLD.id = 2
		BEGIN SELECT RAISE(ABORT, 'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	assertAuthError(t, accountRequest(router, "/api/users/2", `{"confirm":"DELETE"}`, otherToken, true), 503, "unavailable")
	if len(cleaned) != 0 {
		t.Fatal("a rolled-back deletion stopped application work")
	}
	response := accountRequest(router, "/api/users/1", `{"confirm":"DELETE","user_id":2}`, token, true)
	if response.Code != 204 || len(cleaned) != 1 || cleaned[0] != 1 {
		t.Fatalf("cleanup did not receive only the authenticated deleted user: status=%d cleaned=%v", response.Code, cleaned)
	}
}

func TestAccountDeletionClearsAllRedisSessions(t *testing.T) {
	cache, cleanup := newRedisCacheClient(t)
	defer cleanup()
	router, service, _ := newAccountTestServer(t, ModeLocal, nil)
	service.rdb = cache
	token, second, other := accountTestTokens(t, service)
	t.Cleanup(func() {
		_ = cache.Del(context.Background(), redisTokenPrefix+token, redisTokenPrefix+second, redisTokenPrefix+other)
	})
	response := accountRequest(router, "/api/users/1", `{"confirm":"DELETE"}`, token, true)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d", response.Code)
	}
	for _, removed := range []string{token, second} {
		count, err := cache.Raw().Exists(context.Background(), redisTokenPrefix+removed).Result()
		if err != nil || count != 0 {
			t.Fatalf("deleted user's cached token remains: count=%d err=%v", count, err)
		}
	}
	if count, err := cache.Raw().Exists(context.Background(), redisTokenPrefix+other).Result(); err != nil || count != 1 {
		t.Fatalf("another user's token cache changed: count=%d err=%v", count, err)
	}
}

func newAccountTestServer(t *testing.T, mode string, deleter CognitoUserDeleter) (*gin.Engine, *Service, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if mode == ModeCognito {
		// Identity-only extension of the legacy auth fixture; no finance tables.
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN cognito_username TEXT NULL`); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"account_first", "account_other"} {
		if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, '', ?)`, name, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(db, nil, time.Hour)
	handler, err := NewAccountHTTPHandler(service, mode, deleter)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	return router, service, db
}

func accountTestTokens(t *testing.T, service *Service) (string, string, string) {
	t.Helper()
	tokens := make([]string, 3)
	for i, id := range []int64{1, 1, 2} {
		var err error
		tokens[i], err = service.IssueToken(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
	}
	return tokens[0], tokens[1], tokens[2]
}

func accountRequest(router http.Handler, path, body, token string, csrf bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	}
	if csrf {
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "test-csrf"})
		req.Header.Set("X-CSRF-Token", "test-csrf")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}
