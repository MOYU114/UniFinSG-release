package llmconfig

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/crypto"
	"unichatgo/internal/llm"
)

type memoryStore struct {
	configs map[int64]*Config
	upserts int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{configs: map[int64]*Config{}}
}

func (m *memoryStore) Get(_ context.Context, userID int64) (*Config, error) {
	config, ok := m.configs[userID]
	if !ok {
		return nil, nil
	}
	copy := *config
	return &copy, nil
}

func (m *memoryStore) Upsert(_ context.Context, config Config) error {
	copy := config
	m.configs[config.UserID] = &copy
	m.upserts++
	return nil
}

func (m *memoryStore) Delete(_ context.Context, userID int64) error {
	delete(m.configs, userID)
	return nil
}

type fakeModel struct {
	err error
}

func (f *fakeModel) Stream(context.Context, llm.Request, func(llm.Event) error) (llm.Result, error) {
	if f.err != nil {
		return llm.Result{}, f.err
	}
	return llm.Result{Text: "pong"}, nil
}

func newTestService(t *testing.T, store Store, modelErr error) (*Service, *crypto.Cipher) {
	t.Helper()
	cipher, err := crypto.NewCipherFromKey(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	service := NewService(store, cipher, Options{Now: time.Now})
	service.SetModelFactory(func(kind, baseURL, model, apiKey string) (llm.ChatModel, error) {
		return &fakeModel{err: modelErr}, nil
	})
	return service, cipher
}

func enabled(value bool) *bool { return &value }

func TestSaveEncryptsKeyAndNeverReturnsIt(t *testing.T) {
	store := newMemoryStore()
	service, cipher := newTestService(t, store, nil)
	key := "sk-user-secret-1234"
	view, err := service.Save(context.Background(), 1, Input{
		Kind:    llm.KindOpenAICompatible,
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-test",
		APIKey:  &key,
		Enabled: enabled(true),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !view.Enabled || !view.Configured || view.APIKeyLast4 != "1234" {
		t.Fatalf("unexpected view %+v", view)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), "ciphertext") {
		t.Fatalf("view leaked the key: %s", raw)
	}
	stored := store.configs[1]
	if stored == nil || stored.APIKeyCiphertext == key || stored.APIKeyCiphertext == "" {
		t.Fatalf("key was not encrypted at rest: %+v", stored)
	}
	plain, err := cipher.Decrypt(stored.APIKeyCiphertext)
	if err != nil || plain != key {
		t.Fatalf("ciphertext does not round-trip: %q %v", plain, err)
	}
}

func TestSaveFailureDoesNotPersistAndRedactsKey(t *testing.T) {
	store := newMemoryStore()
	key := "sk-user-secret-9999"
	service, _ := newTestService(t, store, &llm.UpstreamError{Status: 401, Message: key + " is invalid"})
	_, err := service.Save(context.Background(), 1, Input{
		Kind:    llm.KindOpenAICompatible,
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-test",
		APIKey:  &key,
		Enabled: enabled(true),
	})
	if err == nil {
		t.Fatal("expected the connectivity test to fail")
	}
	var failed *TestFailedError
	if !errors.As(err, &failed) || failed.Status != 401 {
		t.Fatalf("unexpected error %v", err)
	}
	if strings.Contains(err.Error(), key) || strings.Contains(failed.Message, key) {
		t.Fatalf("api key leaked into the error: %v", err)
	}
	if len(store.configs) != 0 || store.upserts != 0 {
		t.Fatal("a failed test must not save the configuration")
	}
}

func TestSaveOmittedKeyReusesStoredKey(t *testing.T) {
	store := newMemoryStore()
	cipher, err := crypto.NewCipherFromKey(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	var seen []string
	service := NewService(store, cipher, Options{})
	service.SetModelFactory(func(kind, baseURL, model, apiKey string) (llm.ChatModel, error) {
		seen = append(seen, apiKey)
		return &fakeModel{}, nil
	})
	key := "sk-first-key-abcd"
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "one", APIKey: &key, Enabled: enabled(true),
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	first := *store.configs[1]
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "two", Enabled: enabled(true),
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	empty := ""
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "three", APIKey: &empty, Enabled: enabled(true),
	}); err != nil {
		t.Fatalf("third save: %v", err)
	}
	if len(seen) != 3 || seen[1] != key || seen[2] != key {
		t.Fatalf("stored key was not reused: %+v", seen)
	}
	if store.configs[1].APIKeyCiphertext != first.APIKeyCiphertext || store.configs[1].APIKeyLast4 != "abcd" {
		t.Fatal("omitted key rewrote the stored ciphertext")
	}
}

func TestSaveOmittedEnabledKeepsStoredState(t *testing.T) {
	store := newMemoryStore()
	service, _ := newTestService(t, store, nil)
	key := "sk-enabled-state-1234"
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "one",
		APIKey: &key, Enabled: enabled(false),
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	view, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "two",
	})
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if view.Enabled {
		t.Fatal("omitted enabled re-enabled a disabled configuration")
	}
	if store.configs[1].Enabled {
		t.Fatal("store unexpectedly flipped enabled")
	}
}

func TestValidationRejectsBadSettings(t *testing.T) {
	store := newMemoryStore()
	service, _ := newTestService(t, store, nil)
	key := "k"
	cases := []Input{
		{Kind: "gemini", BaseURL: "https://api.example.com", Model: "m", APIKey: &key},
		{Kind: llm.KindOpenAICompatible, BaseURL: "http://api.example.com", Model: "m", APIKey: &key},
		{Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com", Model: "", APIKey: &key},
		{Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com", Model: "m"},
	}
	for _, input := range cases {
		if _, err := service.Save(context.Background(), 1, input); err == nil {
			t.Fatalf("accepted invalid input %+v", input)
		}
	}
	if len(store.configs) != 0 {
		t.Fatal("validation failures must not save")
	}
}

func TestGetReportsNotConfigured(t *testing.T) {
	service, _ := newTestService(t, newMemoryStore(), nil)
	view, err := service.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	raw, _ := json.Marshal(view)
	if string(raw) != `{"enabled":false,"configured":false}` {
		t.Fatalf("unexpected empty view %s", raw)
	}
}

func authStub(userID int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("auth_user_id", userID)
		c.Next()
	}
}

func newRouter(service *Service, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := newTestHandler(service, authStub(userID), func(c *gin.Context) { c.Next() })
	handler.RegisterRoutes(router.Group("/api"))
	return router
}

func TestHTTPEnforcesPathUserOwnership(t *testing.T) {
	service, _ := newTestService(t, newMemoryStore(), nil)
	router := newRouter(service, 1)

	request := httptest.NewRequest(http.MethodGet, "/api/users/2/llm-config", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for another user's config, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/users/1/llm-config", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"configured":false`) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
}

func TestHTTPPutFailureDoesNotSaveAndReturns422(t *testing.T) {
	store := newMemoryStore()
	key := "sk-put-secret-7777"
	service, _ := newTestService(t, store, &llm.UpstreamError{Status: 400, Message: key + " rejected"})
	router := newRouter(service, 1)
	body := `{"kind":"openai_compatible","base_url":"https://api.example.com/v1","model":"m","api_key":"` + key + `","enabled":true}`
	request := httptest.NewRequest(http.MethodPut, "/api/users/1/llm-config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "llm_test_failed") {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), key) {
		t.Fatal("api key leaked in the 422 response")
	}
	if len(store.configs) != 0 {
		t.Fatal("failed PUT saved the configuration")
	}
}

func TestHTTPGetNeverReturnsTheKey(t *testing.T) {
	store := newMemoryStore()
	key := "sk-get-secret-4242"
	service, _ := newTestService(t, store, nil)
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindAnthropic, BaseURL: "https://api.example.com", Model: "claude", APIKey: &key, Enabled: enabled(true),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	router := newRouter(service, 1)
	request := httptest.NewRequest(http.MethodGet, "/api/users/1/llm-config", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", response.Code)
	}
	if strings.Contains(response.Body.String(), key) {
		t.Fatal("api key leaked in GET")
	}
	if !strings.Contains(response.Body.String(), `"api_key_last4":"4242"`) {
		t.Fatalf("last4 missing: %s", response.Body.String())
	}
}

type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, int64) bool { return false }

func TestTestEndpointHonoursLimiter(t *testing.T) {
	store := newMemoryStore()
	service, _ := newTestService(t, store, nil)
	service.limiter = denyLimiter{}
	router := newRouter(service, 1)
	request := httptest.NewRequest(http.MethodPost, "/api/users/1/llm-config/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", response.Code, response.Body.String())
	}
}

func TestTestEndpointRedactsTheKey(t *testing.T) {
	store := newMemoryStore()
	key := "sk-test-secret-3131"
	service, _ := newTestService(t, store, &llm.UpstreamError{Status: 401, Message: key + " rejected"})
	router := newRouter(service, 1)
	body := `{"kind":"openai_compatible","base_url":"https://api.example.com/v1","model":"m","api_key":"` + key + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/users/1/llm-config/test", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"ok":false`) {
		t.Fatalf("unexpected body %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), key) {
		t.Fatal("api key leaked in the test response")
	}
	if !strings.Contains(response.Body.String(), "[redacted]") {
		t.Fatalf("upstream summary was not redacted: %s", response.Body.String())
	}
}

func TestDeleteRemovesTheConfiguration(t *testing.T) {
	store := newMemoryStore()
	key := "sk-delete-secret-5151"
	service, _ := newTestService(t, store, nil)
	if _, err := service.Save(context.Background(), 1, Input{
		Kind: llm.KindOpenAICompatible, BaseURL: "https://api.example.com/v1", Model: "m", APIKey: &key, Enabled: enabled(true),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	router := newRouter(service, 1)
	request := httptest.NewRequest(http.MethodDelete, "/api/users/1/llm-config", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	if len(store.configs) != 0 {
		t.Fatal("configuration was not deleted")
	}
}
