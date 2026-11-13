package auth_test

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/api"
	"unichatgo/internal/auth"
	"unichatgo/internal/config"
	"unichatgo/internal/service/assistant"
	"unichatgo/internal/storage"
	"unichatgo/internal/worker"
)

func TestApplicationAuthPreservesLegacyRoutes(t *testing.T) {
	router, db := integratedAuthRouter(t, auth.ModeLocal)
	credentials := map[string]string{"username": "integrated_user", "password": "Passw0rd!"}
	registered := integratedAuthRequest(t, router, http.MethodPost, "/api/users/register", credentials, nil, "")
	if registered.Code != http.StatusCreated {
		t.Fatalf("registration returned %d: %s", registered.Code, registered.Body.String())
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &user); err != nil || user.ID <= 0 {
		t.Fatalf("registration ID: %v", err)
	}
	var passwordHash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, user.ID).Scan(&passwordHash); err != nil ||
		!strings.HasPrefix(passwordHash, "$2") {
		t.Fatalf("application routes did not use bcrypt registration: %v", err)
	}

	login := integratedAuthRequest(t, router, http.MethodPost, "/api/users/login", credentials, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("new bcrypt account cannot log in: %d", login.Code)
	}
	cookies := login.Result().Cookies()
	var csrf string
	for _, cookie := range cookies {
		if cookie.Name == "csrf_token" {
			csrf = cookie.Value
		}
	}
	tokenPath := "/api/users/" + strconv.FormatInt(user.ID, 10) + "/token"
	read := integratedAuthRequest(t, router, http.MethodGet, tokenPath, nil, cookies, "")
	if read.Code != http.StatusOK {
		t.Fatalf("new login cannot access legacy token listing: %d %s", read.Code, read.Body.String())
	}

	tokenBody := map[string]string{"provider": "openai", "token": "integration-test-placeholder"}
	blocked := integratedAuthRequest(t, router, http.MethodPost, tokenPath, tokenBody, cookies, "")
	assertLegacyAuthError(t, blocked, http.StatusForbidden)
	written := integratedAuthRequest(t, router, http.MethodPost, tokenPath, tokenBody, cookies, csrf)
	if written.Code != http.StatusNoContent {
		t.Fatalf("new CSRF cookie cannot authorize legacy token write: %d %s", written.Code, written.Body.String())
	}
	read = integratedAuthRequest(t, router, http.MethodGet, tokenPath, nil, cookies, "")
	var tokens struct {
		Tokens []struct {
			Provider string `json:"provider"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &tokens); err != nil || read.Code != http.StatusOK ||
		len(tokens.Tokens) != 1 || tokens.Tokens[0].Provider != "openai" {
		t.Fatalf("legacy provider data did not round-trip: %d %s %v", read.Code, read.Body.String(), err)
	}

	logout := integratedAuthRequest(t, router, http.MethodPost, "/api/auth/logout", nil, cookies, csrf)
	if logout.Code != http.StatusOK {
		t.Fatalf("new logout failed: %d %s", logout.Code, logout.Body.String())
	}
	revoked := integratedAuthRequest(t, router, http.MethodGet, tokenPath, nil, cookies, "")
	assertLegacyAuthError(t, revoked, http.StatusUnauthorized)
}

func TestApplicationCognitoModeClosesLegacyPasswordRoutes(t *testing.T) {
	router, db := integratedAuthRouter(t, auth.ModeCognito)
	for _, path := range []string{"/api/users/register", "/api/users/login"} {
		response := integratedAuthRequest(t, router, http.MethodPost, path,
			map[string]string{"username": "blocked_user", "password": "Passw0rd!"}, nil, "")
		var result struct {
			Error auth.APIError `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
			response.Code != http.StatusNotFound || result.Error.Code != "not_found" {
			t.Fatalf("legacy password route remains open at %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled registration created users: count=%d err=%v", count, err)
	}
}

func integratedAuthRouter(t *testing.T, mode string) (*gin.Engine, *sql.DB) {
	t.Helper()
	var testKey [32]byte
	if _, err := rand.Read(testKey[:]); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UNICHATGO_APIKEY_KEY", base64.StdEncoding.EncodeToString(testKey[:]))
	db, err := storage.Open("sqlite3", &config.Config{
		Databases: map[string]config.DatabaseConfig{"sqlite3": {DSN: ":memory:"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Migrate(db, "sqlite3"); err != nil {
		t.Fatal(err)
	}
	legacyService, err := assistant.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(db, nil, time.Hour)
	applicationAuth, err := auth.NewHTTPHandler(authService, mode)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewHandler(legacyService, authService,
		worker.DispatcherConfig{MinWorkers: 1, MaxWorkers: 1, QueueSize: 1},
		t.TempDir(), assistant.DefaultTempFileTTL, nil)
	router := gin.New()
	handler.RegisterRoutes(router, applicationAuth)
	return router, db
}

func integratedAuthRequest(t *testing.T, router http.Handler, method, path string, body any, cookies []*http.Cookie, csrf string) *httptest.ResponseRecorder {
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
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertLegacyAuthError(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != status || body.Error == "" {
		t.Fatalf("legacy error format changed: status=%d body=%s", response.Code, response.Body.String())
	}
}
