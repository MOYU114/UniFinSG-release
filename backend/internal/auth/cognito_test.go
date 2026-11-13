package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type cognitoTestTransport func(*http.Request) (*http.Response, error)

func (transport cognitoTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

type cognitoTestHarness struct {
	handler       *CognitoHandler
	router        *gin.Engine
	service       *Service
	key           *rsa.PrivateKey
	keyID         string
	claims        map[string]any
	tokenOverride func(http.ResponseWriter, *http.Request)
	jwksOverride  func(http.ResponseWriter, *http.Request)
	tokenCalls    atomic.Int64
	jwksCalls     atomic.Int64
	expectedFlow  cognitoFlow
}

func newCognitoTestHarness(t *testing.T) *cognitoTestHarness {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := randomCognitoValue()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &cognitoTestHarness{key: key, keyID: "key-1"}
	now := time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)
	config := CognitoConfig{
		Region: "ap-southeast-1", UserPoolID: "ap-southeast-1_fixture",
		ClientID: "fixture-client", ClientSecret: secret,
		Domain:      "test.auth.ap-southeast-1.amazoncognito.com",
		RedirectURI: "http://localhost:8080/api/auth/callback", FrontendURL: "http://localhost:3000",
	}
	fixture.claims = map[string]any{
		"iss": "https://cognito-idp.ap-southeast-1.amazonaws.com/ap-southeast-1_fixture",
		"aud": config.ClientID, "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"token_use": "id", "sub": "a52d11e8-3c97-4b2e-9f71-60a21acee102",
		"nonce": "nonce-for-verifier-tests", "email": "student@u.nus.edu",
		"email_verified": false, "cognito:username": "provider-username",
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ap-southeast-1_fixture/.well-known/jwks.json":
			fixture.jwksCalls.Add(1)
			if fixture.jwksOverride != nil {
				fixture.jwksOverride(w, request)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"keys": []any{cognitoTestJWK(&fixture.key.PublicKey, fixture.keyID)},
			})
		case "/oauth2/token":
			fixture.tokenCalls.Add(1)
			if fixture.tokenOverride != nil {
				fixture.tokenOverride(w, request)
				return
			}
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			clientID, clientSecret, basic := request.BasicAuth()
			if !basic || clientID != config.ClientID || clientSecret != config.ClientSecret ||
				request.Method != http.MethodPost || request.Form.Get("grant_type") != "authorization_code" ||
				request.Form.Get("client_id") != config.ClientID ||
				request.Form.Get("redirect_uri") != config.RedirectURI ||
				request.Form.Get("code") != "one-use-code" ||
				request.Form.Get("code_verifier") != fixture.expectedFlow.Verifier {
				t.Error("code exchange did not use the confidential client and original PKCE verifier")
			}
			token := cognitoTestToken(t, fixture.key, fixture.keyID, "RS256", fixture.claims)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id_token": token, "access_token": "provider-access-token", "refresh_token": "provider-refresh-token",
			})
		default:
			t.Errorf("unexpected provider path: %s", request.URL.Path)
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(provider.Close)
	providerURL, _ := url.Parse(provider.URL)
	client := &http.Client{Transport: cognitoTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" ||
			(request.URL.Host != "test.auth.ap-southeast-1.amazoncognito.com" &&
				request.URL.Host != "cognito-idp.ap-southeast-1.amazonaws.com") {
			t.Errorf("request escaped fixed provider endpoints: %s", request.URL)
		}
		copy := request.Clone(request.Context())
		copy.URL.Scheme, copy.URL.Host = providerURL.Scheme, providerURL.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	insertUser(t, db, 1)
	fixture.service = NewService(db, nil, time.Hour)
	fixture.handler, err = NewCognitoHandler(fixture.service, config,
		WithCognitoHTTPClient(client), WithCognitoClock(func() time.Time { return now }), nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler.resolveIdentity = func(context.Context, CognitoIdentity) (int64, error) { return 1, nil }
	fixture.router = gin.New()
	local, _ := NewHTTPHandler(fixture.service, ModeCognito)
	local.RegisterRoutes(fixture.router.Group("/api"))
	fixture.handler.RegisterRoutes(fixture.router.Group("/api"))
	return fixture
}

func cognitoTestJWK(key *rsa.PublicKey, kid string) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func cognitoTestToken(t *testing.T, key *rsa.PrivateKey, kid, algorithm string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": algorithm, "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (fixture *cognitoTestHarness) login(t *testing.T, redirect string) (*http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/auth/login?"+url.Values{"redirect": {redirect}}.Encode(), nil))
	if recorder.Code != http.StatusFound || recorder.Body.Len() != 0 {
		t.Fatalf("login status/body: %d %s", recorder.Code, recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cognitoFlowCookie {
		t.Fatal("login did not set a single flow cookie")
	}
	flow, err := fixture.handler.decodeFlow(cookies[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	fixture.expectedFlow = flow
	fixture.claims["nonce"] = flow.Nonce
	return cookies[0], recorder
}

func (fixture *cognitoTestHarness) callback(cookie *http.Cookie, state string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/auth/callback?"+
		url.Values{"code": {"one-use-code"}, "state": {state}}.Encode(), nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

func TestCognitoAuthorizationAndCallback(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	defer gin.SetMode(oldMode)
	fixture := newCognitoTestHarness(t)
	cookie, login := fixture.login(t, "/budget?month=2026-11")
	flow := fixture.expectedFlow
	location, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	challenge := sha256.Sum256([]byte(flow.Verifier))
	if location.Scheme != "https" || location.Host != "test.auth.ap-southeast-1.amazoncognito.com" ||
		location.Path != "/oauth2/authorize" || query.Get("response_type") != "code" ||
		query.Get("state") != flow.State || query.Get("nonce") != flow.Nonce ||
		query.Get("code_challenge_method") != "S256" ||
		query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) ||
		query.Get("scope") != "openid email" || query.Get("client_secret") != "" {
		t.Fatal("authorization request parameters do not match the protected flow")
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode ||
		cookie.Path != "/api/auth" || cookie.MaxAge != 600 {
		t.Fatal("flow cookie attributes are incorrect")
	}
	if login.Header().Get("Cache-Control") != "no-store" || login.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("authorization redirect can leak or cache flow state")
	}
	for _, hidden := range []string{flow.State, flow.Verifier, flow.Nonce, "/budget"} {
		if strings.Contains(cookie.Value, hidden) {
			t.Fatal("flow cookie exposed unencrypted authorization values")
		}
	}
	var resolved CognitoIdentity
	fixture.handler.resolveIdentity = func(_ context.Context, identity CognitoIdentity) (int64, error) {
		resolved = identity
		return 1, nil
	}
	result := fixture.callback(cookie, flow.State)
	if result.Code != http.StatusFound || result.Body.Len() != 0 ||
		result.Header().Get("Location") != "http://localhost:3000/budget?month=2026-11" {
		t.Fatalf("callback result: %d %s", result.Code, result.Header().Get("Location"))
	}
	if result.Header().Get("Cache-Control") != "no-store" || result.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("callback can leak authorization query parameters to the frontend")
	}
	if resolved.Subject != fixture.claims["sub"] || resolved.Email != fixture.claims["email"] ||
		resolved.Username != fixture.claims["cognito:username"] {
		t.Fatal("verified identity was not passed to provisioning")
	}
	sessionCookies := map[string]*http.Cookie{}
	for _, set := range result.Result().Cookies() {
		sessionCookies[set.Name] = set
	}
	if sessionCookies[cognitoFlowCookie] == nil || sessionCookies[cognitoFlowCookie].MaxAge >= 0 {
		t.Fatal("callback did not clear the flow cookie")
	}
	session, csrf := sessionCookies["auth_token"], sessionCookies["csrf_token"]
	if session == nil || csrf == nil || !session.HttpOnly || csrf.HttpOnly ||
		!session.Secure || !csrf.Secure || session.Value == "" || csrf.Value == "" {
		t.Fatal("callback did not issue existing session/CSRF cookies")
	}
	if id, err := fixture.service.ValidateToken(context.Background(), session.Value); err != nil || id != 1 {
		t.Fatalf("callback session is unusable: id=%d err=%v", id, err)
	}
	for _, leaked := range []string{"provider-access-token", "provider-refresh-token", fixture.handler.config.ClientSecret} {
		if strings.Contains(result.Header().Get("Location")+result.Body.String(), leaked) {
			t.Fatal("callback exposed provider credentials")
		}
	}
	for _, path := range []string{"/api/users/register", "/api/users/login"} {
		recorder := httptest.NewRecorder()
		fixture.router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		assertAuthError(t, recorder, http.StatusNotFound, "not_found")
	}
}

func TestCognitoCallbackStateProtection(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	for _, test := range []string{"missing_cookie", "wrong_state", "missing_state", "tampered_cookie", "expired", "future", "wrong_secret"} {
		t.Run(test, func(t *testing.T) {
			cookie, _ := fixture.login(t, "")
			state := fixture.expectedFlow.State
			switch test {
			case "missing_cookie":
				cookie = nil
			case "wrong_state":
				state = "attacker-state"
			case "missing_state":
				state = ""
			case "tampered_cookie":
				cookie.Value = "X" + cookie.Value
			case "expired", "future":
				flow := fixture.expectedFlow
				if test == "expired" {
					flow.Expires = fixture.handler.now().Unix()
				} else {
					flow.Expires = fixture.handler.now().Add(11 * time.Minute).Unix()
				}
				cookie.Value, _ = fixture.handler.encodeFlow(flow)
			case "wrong_secret":
				original := fixture.handler.flowCipher
				fixture.handler.flowCipher, _ = cognitoFlowCipher("different-test-secret")
				cookie.Value, _ = fixture.handler.encodeFlow(fixture.expectedFlow)
				fixture.handler.flowCipher = original
			}
			result := fixture.callback(cookie, state)
			if result.Code != http.StatusFound ||
				result.Header().Get("Location") != "http://localhost:3000/signin?error=state_mismatch" {
				t.Fatalf("state failure was not rejected: %d %s", result.Code, result.Header().Get("Location"))
			}
			if fixture.tokenCalls.Load() != 0 {
				t.Fatal("invalid state reached code exchange")
			}
			for _, set := range result.Result().Cookies() {
				if set.Name == "auth_token" || set.Name == "csrf_token" {
					t.Fatal("invalid state issued a session")
				}
			}
		})
	}
}

func TestCognitoTokenVerification(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	original := fixture.claims
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"wrong_issuer", func(claims map[string]any) { claims["iss"] = "https://attacker.example" }},
		{"wrong_audience", func(claims map[string]any) { claims["aud"] = "another-client" }},
		{"expired", func(claims map[string]any) { claims["exp"] = fixture.handler.now().Unix() }},
		{"missing_expiry", func(claims map[string]any) { delete(claims, "exp") }},
		{"future_issue", func(claims map[string]any) { claims["iat"] = fixture.handler.now().Add(time.Minute * 2).Unix() }},
		{"future_validity", func(claims map[string]any) { claims["nbf"] = fixture.handler.now().Add(time.Second).Unix() }},
		{"access_token", func(claims map[string]any) { claims["token_use"] = "access" }},
		{"missing_subject", func(claims map[string]any) { delete(claims, "sub") }},
		{"oversized_subject", func(claims map[string]any) { claims["sub"] = strings.Repeat("s", 256) }},
		{"non_ascii_subject", func(claims map[string]any) { claims["sub"] = "非ASCII" }},
		{"wrong_nonce", func(claims map[string]any) { claims["nonce"] = "wrong-nonce" }},
		{"missing_nonce", func(claims map[string]any) { delete(claims, "nonce") }},
		{"email_type", func(claims map[string]any) { claims["email"] = true }},
		{"email_length", func(claims map[string]any) { claims["email"] = strings.Repeat("学", 321) }},
		{"email_control", func(claims map[string]any) { claims["email"] = "student\n@u.nus.edu" }},
		{"username_type", func(claims map[string]any) { claims["cognito:username"] = []string{"one", "two"} }},
		{"username_length", func(claims map[string]any) { claims["cognito:username"] = strings.Repeat("用", 129) }},
		{"multiple_aud_without_azp", func(claims map[string]any) { claims["aud"] = []string{"fixture-client", "another"} }},
		{"wrong_azp", func(claims map[string]any) { claims["azp"] = "another" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := make(map[string]any, len(original))
			for key, value := range original {
				claims[key] = value
			}
			test.change(claims)
			token := cognitoTestToken(t, fixture.key, fixture.keyID, "RS256", claims)
			if _, err := fixture.handler.verifier.verify(context.Background(), token, "nonce-for-verifier-tests"); !errors.Is(err, ErrCognitoTokenInvalid) {
				t.Fatalf("claim violation accepted: %v", err)
			}
		})
	}
	valid := cognitoTestToken(t, fixture.key, fixture.keyID, "RS256", original)
	if identity, err := fixture.handler.verifier.verify(context.Background(), valid, "nonce-for-verifier-tests"); err != nil || identity.Email != "student@u.nus.edu" {
		t.Fatalf("valid signed token, including email_verified=false, rejected: %v", err)
	}
	if fixture.jwksCalls.Load() != 1 {
		t.Fatalf("known key not cached: calls=%d", fixture.jwksCalls.Load())
	}
	for _, token := range []string{
		"not.a.token", "", strings.Repeat("x", 65537),
		cognitoTestToken(t, fixture.key, fixture.keyID, "none", original),
		cognitoTestToken(t, fixture.key, fixture.keyID, "HS256", original),
		valid[:len(valid)-20] + strings.Repeat("A", 20),
	} {
		if _, err := fixture.handler.verifier.verify(context.Background(), token, "nonce-for-verifier-tests"); !errors.Is(err, ErrCognitoTokenInvalid) {
			t.Fatalf("invalid token representation accepted: %v", err)
		}
	}
}

func TestCognitoJWKSRotationAndFailures(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	first := cognitoTestToken(t, fixture.key, fixture.keyID, "RS256", fixture.claims)
	if _, err := fixture.handler.verifier.verify(context.Background(), first, "nonce-for-verifier-tests"); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture.key, fixture.keyID = key, "rotated-key"
	rotated := cognitoTestToken(t, key, "rotated-key", "RS256", fixture.claims)
	if _, err := fixture.handler.verifier.verify(context.Background(), rotated, "nonce-for-verifier-tests"); err != nil {
		t.Fatalf("new kid did not refresh cached JWKS: %v", err)
	}
	if fixture.jwksCalls.Load() != 2 {
		t.Fatalf("expected two JWKS fetches, got %d", fixture.jwksCalls.Load())
	}
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{"unavailable", `{}`, 503}, {"malformed", `{`, 200},
		{"no_signing_keys", `{"keys":[]}`, 200},
		{"too_large", strings.Repeat("x", 1<<20+1), 200},
		{"wrong_key_type", `{"keys":[{"kid":"rotated-key","kty":"oct"}]}`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.handler.verifier.expires = time.Time{}
			fixture.jwksOverride = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			}
			if _, err := fixture.handler.verifier.verify(context.Background(), rotated, "nonce-for-verifier-tests"); !errors.Is(err, ErrCognitoUnavailable) {
				t.Fatalf("JWKS failure classification: %v", err)
			}
		})
	}
}

func TestCognitoSubjectIsOpaqueAndPreserved(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	for _, subject := range []string{
		"a", "provider|subject/not-a-uuid", "subject-with-trailing-space ",
		" ", "opaque\x00subject", strings.Repeat("s", 255),
	} {
		fixture.claims["sub"] = subject
		token := cognitoTestToken(t, fixture.key, fixture.keyID, "RS256", fixture.claims)
		identity, err := fixture.handler.verifier.verify(context.Background(), token, "nonce-for-verifier-tests")
		if err != nil || identity.Subject != subject {
			t.Fatalf("valid opaque subject was rejected or normalized: %q err=%v", subject, err)
		}
		username := cognitoLocalUsername(subject)
		if len(username) != 72 {
			t.Fatalf("internal username length=%d", len(username))
		}
		var validation *CredentialsValidationError
		if _, err := fixture.service.RegisterLocalUser(context.Background(), username, "valid-test-password"); !errors.As(err, &validation) {
			t.Fatalf("Cognito placeholder could be registered through local auth: %v", err)
		}
	}
}

func TestCognitoCodeExchangeFailures(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	for _, test := range []struct {
		name, body, failure string
		status              int
	}{
		{"denied", `{"error":"invalid_grant"}`, "token_invalid", 400},
		{"missing_token", `{}`, "token_invalid", 200},
		{"malformed_json", `{`, "token_invalid", 200},
		{"wrong_token_type", `{"id_token":true}`, "token_invalid", 200},
		{"bad_jwt", `{"id_token":"not-a-jwt"}`, "token_invalid", 200},
		{"redirect", ``, "token_invalid", 302},
		{"upstream_down", `private provider detail`, "cognito_unavailable", 503},
		{"throttled", ``, "cognito_unavailable", 429},
		{"response_too_large", strings.Repeat("x", 1<<20+1), "cognito_unavailable", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.tokenOverride = func(w http.ResponseWriter, _ *http.Request) {
				if test.status == 302 {
					w.Header().Set("Location", "https://attacker.example/stolen")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}
			cookie, _ := fixture.login(t, "")
			response := fixture.callback(cookie, fixture.expectedFlow.State)
			if response.Code != http.StatusFound ||
				response.Header().Get("Location") != "http://localhost:3000/signin?error="+test.failure {
				t.Fatalf("unexpected failure response: %d %s", response.Code, response.Header().Get("Location"))
			}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == "auth_token" || cookie.Name == "csrf_token" {
					t.Fatal("failed exchange issued a session")
				}
			}
		})
	}
}

func TestCognitoRedirectValidation(t *testing.T) {
	for _, path := range []string{"/", "/dashboard", "/budget?month=2026-11", "/settings#privacy", "/transactions/餐饮"} {
		if result, valid := safeCognitoRedirect(path); !valid || result != path {
			t.Errorf("valid local path rejected: %q", path)
		}
	}
	for _, path := range []string{
		"//attacker.example", "https://attacker.example", "dashboard", `\/attacker.example`,
		`/\attacker.example`, "/%5cattacker.example", "/%2fattacker.example",
		"/%252Fattacker.example", "/%255cattacker.example", "/\r\nLocation: https://attacker.example",
		"/%0aLocation:bad", "/%zz", "/%250d%250aLocation:bad",
	} {
		if _, valid := safeCognitoRedirect(path); valid {
			t.Errorf("unsafe redirect accepted: %q", path)
		}
	}
	if result, valid := safeCognitoRedirect(""); !valid || result != "/dashboard" {
		t.Fatal("default redirect changed")
	}
	fixture := newCognitoTestHarness(t)
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/auth/login?"+url.Values{"redirect": {"//attacker.example"}}.Encode(), nil))
	assertAuthError(t, response, http.StatusBadRequest, "bad_request")
	if len(response.Result().Cookies()) != 0 {
		t.Fatal("invalid redirect began a login flow")
	}
}

func TestCognitoConfigurationAndIndependentFlowCipher(t *testing.T) {
	fixture := newCognitoTestHarness(t)
	config := fixture.handler.config
	for _, mutate := range []func(*CognitoConfig){
		func(c *CognitoConfig) { c.ClientSecret = "" },
		func(c *CognitoConfig) { c.Region = "../attacker" },
		func(c *CognitoConfig) { c.UserPoolID = "us-east-1_other" },
		func(c *CognitoConfig) { c.Domain = "http://provider.example" },
		func(c *CognitoConfig) { c.Domain = "https://provider.example/extra" },
		func(c *CognitoConfig) { c.RedirectURI = "http://public.example/callback" },
		func(c *CognitoConfig) { c.FrontendURL = "https://user:password@example.com" },
		func(c *CognitoConfig) { c.FrontendURL = "https://example.com?redirect=elsewhere" },
	} {
		bad := config
		mutate(&bad)
		if _, err := NewCognitoHandler(fixture.service, bad); err == nil {
			t.Fatal("invalid Cognito configuration accepted")
		}
	}
	if _, err := NewCognitoHandler(nil, config); err == nil {
		t.Fatal("nil auth service accepted")
	}
	other, err := NewCognitoHandler(fixture.service, config, WithCognitoClock(fixture.handler.now))
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := fixture.login(t, "/dashboard")
	flow, err := other.decodeFlow(cookie.Value)
	if err != nil || flow.State != fixture.expectedFlow.State {
		t.Fatalf("a second instance could not open a shared-client flow cookie: %v", err)
	}
	if other.client.Timeout != 10*time.Second {
		t.Fatal("provider client timeout was not bounded")
	}
}
