package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newAuthHTTPTestServer(t *testing.T, mode string) (*gin.Engine, *Service, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	handler, err := NewHTTPHandler(svc, mode)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	handler.RegisterRoutes(router.Group("/api"))
	router.GET("/api/private", svc.APIMiddleware(), func(c *gin.Context) {
		id, _ := UserIDFromContext(c)
		c.JSON(http.StatusOK, gin.H{"user_id": id})
	})
	router.PUT("/api/users/:id/check", svc.APIMiddleware(), RequirePathUser(), svc.APICSRFMiddleware(), func(c *gin.Context) {
		id, _ := UserIDFromContext(c)
		c.JSON(http.StatusOK, gin.H{"user_id": id})
	})
	return router, svc, db
}

func authRequest(t *testing.T, router http.Handler, method, path string, body any, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func assertAuthError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) APIError {
	t.Helper()
	var result struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid structured error: %s", response.Body.String())
	}
	if response.Code != status || result.Error.Code != code || result.Error.Message == "" {
		t.Fatalf("got status=%d error=%+v; want %d/%s", response.Code, result.Error, status, code)
	}
	if code != "validation_failed" && result.Error.Fields != nil {
		t.Fatalf("unexpected fields for %s", code)
	}
	return result.Error
}

func loginForAuthHTTPTest(t *testing.T, router http.Handler, username string) (*httptest.ResponseRecorder, int64) {
	t.Helper()
	credentials := map[string]string{"username": username, "password": "Passw0rd!"}
	registered := authRequest(t, router, http.MethodPost, "/api/users/register", credentials, nil, nil)
	if registered.Code != http.StatusCreated {
		t.Fatalf("registration failed: status=%d body=%s", registered.Code, registered.Body.String())
	}
	loggedIn := authRequest(t, router, http.MethodPost, "/api/users/login", credentials, nil, nil)
	if loggedIn.Code != http.StatusOK {
		t.Fatalf("login failed: status=%d body=%s", loggedIn.Code, loggedIn.Body.String())
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(loggedIn.Body.Bytes(), &user); err != nil || user.ID == 0 {
		t.Fatalf("login user ID: %v", err)
	}
	return loggedIn, user.ID
}

func TestAuthHTTPRegistrationAndLogin(t *testing.T) {
	router, _, db := newAuthHTTPTestServer(t, ModeLocal)
	login, id := loginForAuthHTTPTest(t, router, "tester")
	var body map[string]any
	if err := json.Unmarshal(login.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	token, tokenOK := body["auth_token"].(string)
	if !tokenOK || token == "" || body["username"] != "tester" || body["created_at"] == nil || body["password_hash"] != nil {
		t.Fatal("incorrect login response fields")
	}
	if _, err := time.Parse(time.RFC3339, body["created_at"].(string)); err != nil {
		t.Fatalf("created_at must be RFC3339: %v", err)
	}
	private := authRequest(t, router, http.MethodGet, "/api/private", nil, login.Result().Cookies(), nil)
	if private.Code != http.StatusOK {
		t.Fatalf("session cookie did not authenticate: %d", private.Code)
	}
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil || !strings.HasPrefix(hash, "$2") {
		t.Fatalf("registration did not persist bcrypt: %v", err)
	}
	duplicate := authRequest(t, router, http.MethodPost, "/api/users/register",
		map[string]string{"username": "tester", "password": "Passw0rd!"}, nil, nil)
	assertAuthError(t, duplicate, http.StatusConflict, "conflict")
	for _, credentials := range []map[string]string{
		{"username": "tester", "password": "wrong"},
		{"username": "unknown", "password": "Passw0rd!"},
		{"username": "", "password": ""},
	} {
		response := authRequest(t, router, http.MethodPost, "/api/users/login", credentials, nil, nil)
		assertAuthError(t, response, http.StatusUnauthorized, "unauthenticated")
	}
}

func TestAuthHTTPValidationAndMalformedJSON(t *testing.T) {
	router, _, _ := newAuthHTTPTestServer(t, ModeLocal)
	response := authRequest(t, router, http.MethodPost, "/api/users/register",
		map[string]string{"username": "x", "password": "short"}, nil, nil)
	result := assertAuthError(t, response, http.StatusBadRequest, "validation_failed")
	if result.Fields["username"] == "" || result.Fields["password"] == "" {
		t.Fatal("missing validation fields")
	}
	for _, body := range []string{`{`, `{"username": 123}`, `{"username":"tester","password":"Passw0rd!"} {}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/users/register", strings.NewReader(body))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertAuthError(t, response, http.StatusBadRequest, "bad_request")
	}
}

func TestAuthModeRoutes(t *testing.T) {
	for _, mode := range []string{ModeLocal, ModeCognito} {
		t.Run(mode, func(t *testing.T) {
			router, _, db := newAuthHTTPTestServer(t, mode)
			for _, path := range []string{"/api/users/register", "/api/users/login"} {
				response := authRequest(t, router, http.MethodPost, path, map[string]string{}, nil, nil)
				if mode == ModeCognito {
					assertAuthError(t, response, http.StatusNotFound, "not_found")
				} else if response.Code == http.StatusNotFound {
					t.Fatal("local route is closed")
				}
			}
			if mode == ModeLocal {
				for _, path := range []string{"/api/auth/login", "/api/auth/callback"} {
					response := authRequest(t, router, http.MethodGet, path, nil, nil, nil)
					assertAuthError(t, response, http.StatusNotFound, "not_found")
				}
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("disabled/invalid requests created a user: %v", err)
			}
		})
	}
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	svc := NewService(db, nil, time.Hour)
	if _, err := NewHTTPHandler(svc, "unknown"); err == nil {
		t.Fatal("accepted unknown auth mode")
	}
	if handler, err := NewHTTPHandler(svc, ""); err != nil || handler.mode != ModeLocal {
		t.Fatalf("empty auth mode default: %v", err)
	}
}

func TestAuthHTTPCSRFAndLocalLogout(t *testing.T) {
	router, _, _ := newAuthHTTPTestServer(t, ModeLocal)
	login, id := loginForAuthHTTPTest(t, router, "tester")
	cookies := login.Result().Cookies()
	var csrf, bearer string
	for _, cookie := range cookies {
		if cookie.Name == "csrf_token" {
			csrf = cookie.Value
		}
		if cookie.Name == "auth_token" {
			bearer = cookie.Value
		}
	}
	for _, headers := range []map[string]string{
		nil,
		{"X-CSRF-Token": "wrong"},
	} {
		response := authRequest(t, router, http.MethodPost, "/api/auth/logout", nil, cookies, headers)
		assertAuthError(t, response, http.StatusForbidden, "csrf_invalid")
	}
	// An empty Bearer header must not fall back to cookies or bypass CSRF.
	emptyBearer := authRequest(t, router, http.MethodPost, "/api/auth/logout", nil, cookies, map[string]string{"Authorization": "Bearer "})
	assertAuthError(t, emptyBearer, http.StatusUnauthorized, "unauthenticated")
	ok := authRequest(t, router, http.MethodPut, "/api/users/"+strconv.FormatInt(id, 10)+"/check",
		map[string]int64{"user_id": id + 1}, nil, map[string]string{"Authorization": "Bearer " + bearer})
	if ok.Code != http.StatusOK {
		t.Fatalf("valid bearer should not require CSRF: %d", ok.Code)
	}
	response := authRequest(t, router, http.MethodPost, "/api/auth/logout", nil, cookies, map[string]string{"X-CSRF-Token": csrf})
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"logout_url":null}` {
		t.Fatalf("local logout response: %d %s", response.Code, response.Body.String())
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.MaxAge != -1 || cookie.Value != "" {
			t.Fatal("logout did not clear session cookies")
		}
	}
	afterLogout := authRequest(t, router, http.MethodGet, "/api/private", nil, cookies, nil)
	assertAuthError(t, afterLogout, http.StatusUnauthorized, "unauthenticated")
}

func TestAuthHTTPUserIsolationAndDependencyErrors(t *testing.T) {
	router, _, db := newAuthHTTPTestServer(t, ModeLocal)
	login, id := loginForAuthHTTPTest(t, router, "tester")
	cookies := login.Result().Cookies()
	for _, tc := range []struct {
		pathID string
		status int
		code   string
	}{
		{strconv.FormatInt(id+1, 10), http.StatusForbidden, "forbidden"},
		{"invalid", http.StatusBadRequest, "bad_request"},
		{"0", http.StatusBadRequest, "bad_request"},
	} {
		response := authRequest(t, router, http.MethodPut, "/api/users/"+tc.pathID+"/check", nil, cookies, nil)
		assertAuthError(t, response, tc.status, tc.code)
	}
	unauthenticated := authRequest(t, router, http.MethodGet, "/api/private", nil, nil, nil)
	assertAuthError(t, unauthenticated, http.StatusUnauthorized, "unauthenticated")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unavailable := authRequest(t, router, http.MethodGet, "/api/private", nil, cookies, nil)
	assertAuthError(t, unavailable, http.StatusServiceUnavailable, "unavailable")
	if strings.Contains(unavailable.Body.String(), "database") {
		t.Fatal("dependency error leaked database details")
	}
}

func TestAuthHTTPProductionCookies(t *testing.T) {
	previous := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	t.Cleanup(func() { gin.SetMode(previous) })
	router, _, _ := newAuthHTTPTestServer(t, ModeLocal)
	login, _ := loginForAuthHTTPTest(t, router, "tester")
	cookies := login.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("expected auth and CSRF cookies, got %d", len(cookies))
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.Path != "/" || cookie.MaxAge <= 0 {
			t.Fatalf("invalid production cookie attributes for %s", cookie.Name)
		}
		switch cookie.Name {
		case "auth_token":
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("invalid auth cookie attributes")
			}
		case "csrf_token":
			if cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("invalid CSRF cookie attributes")
			}
		}
	}
}

func TestCognitoLogoutURLAndRevocation(t *testing.T) {
	t.Setenv("COGNITO_DOMAIN", "https://example.auth.ap-southeast-1.amazoncognito.com")
	t.Setenv("COGNITO_CLIENT_ID", "example-client")
	t.Setenv("FRONTEND_URL", "http://localhost:5173")
	router, svc, _ := newAuthHTTPTestServer(t, ModeCognito)
	user, err := svc.RegisterLocalUser(context.Background(), "test_user", "Passw0rd!")
	if err != nil {
		t.Fatal(err)
	}
	token, err := svc.IssueToken(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := authRequest(t, router, http.MethodPost, "/api/auth/logout", nil, nil, map[string]string{"Authorization": "Bearer " + token})
	if response.Code != http.StatusOK {
		t.Fatalf("Cognito logout failed: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		URL string `json:"logout_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(body.URL)
	if err != nil || target.Scheme != "https" || target.Path != "/logout" ||
		target.Query().Get("client_id") != "example-client" ||
		target.Query().Get("logout_uri") != "http://localhost:5173/signin" {
		t.Fatalf("invalid logout URL: %s %v", body.URL, err)
	}
	if _, err := svc.ValidateToken(context.Background(), token); err == nil {
		t.Fatal("Cognito logout did not revoke local session")
	}
}

func TestCognitoLogoutRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("COGNITO_CLIENT_ID", "example-client")
	t.Setenv("FRONTEND_URL", "https://example.org")
	for _, domain := range []string{"", "http://example.org", "https://example.org/path", "https://user@example.org", "https://example.org?x=y"} {
		t.Setenv("COGNITO_DOMAIN", domain)
		if _, err := cognitoLogoutURL(); err == nil {
			t.Fatalf("accepted invalid Cognito domain %q", domain)
		}
	}
	t.Setenv("COGNITO_DOMAIN", "example.auth.ap-southeast-1.amazoncognito.com")
	if target, err := cognitoLogoutURL(); err != nil || !strings.HasPrefix(target, "https://example.auth.") {
		t.Fatalf("hostname-only Cognito domain should use HTTPS: %q %v", target, err)
	}
}
