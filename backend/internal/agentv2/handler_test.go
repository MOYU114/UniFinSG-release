package agentv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/llm"
)

type sseEvent struct {
	Name string
	Data map[string]any
}

func parseSSEEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	events := make([]sseEvent, 0, 8)
	for _, chunk := range strings.Split(body, "\n\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var name string
		var data string
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name == "" {
			continue
		}
		payload := map[string]any{}
		if data != "" {
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("invalid SSE data %q: %v", data, err)
			}
		}
		events = append(events, sseEvent{Name: name, Data: payload})
	}
	return events
}

func newTestRouter(store Store, runner *Runner, models ModelResolver, creditService Credits, coord Coordination, userID int64) *gin.Engine {
	return newDraftTestRouter(store, runner, models, creditService, nil, coord, userID)
}

func newDraftTestRouter(store Store, runner *Runner, models ModelResolver, creditService Credits,
	draftService Drafts, coord Coordination, userID int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := newTestHandler(store, runner, models, creditService, draftService, coord, nil,
		func(c *gin.Context) {
			c.Set("auth_user_id", userID)
			c.Next()
		},
		func(c *gin.Context) { c.Next() })
	handler.RegisterRoutes(router.Group("/api"))
	return router
}

func newRunningFixture(t *testing.T, userID int64) (*memoryStore, *scriptedModel, *fakeCoord, *gin.Engine, int64) {
	t.Helper()
	store := newMemoryStore()
	session, err := store.CreateSession(context.Background(), userID, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{{ID: "call_1", Name: "get_budget_snapshot", Args: json.RawMessage(`{}`)}}},
		{text: "You have S$123.45 left this month. " + Disclaimer},
	}}
	coord := newFakeCoord()
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 12345}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: model, mode: llm.ModePlatform}, creditService, coord, userID)
	return store, model, coord, router, session.ID
}

func TestSendMessageStreamsToolEventsAndVerifies(t *testing.T) {
	store, _, _, router, sessionID := newRunningFixture(t, 1)

	body := `{"content":"How much can I still spend this month?","client_request_id":"req-1"}`
	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("content type = %q", contentType)
	}
	events := parseSSEEvents(t, response.Body.String())
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	for _, want := range []string{"ack", "status", "tool_call", "tool_result", "delta", "done"} {
		if !contains(names, want) {
			t.Fatalf("missing %s event in %v", want, names)
		}
	}
	ack := firstEvent(t, events, "ack")
	if ack.Data["mode"] != "platform" || ack.Data["credits_reserved"].(float64) != 2 {
		t.Fatalf("unexpected ack: %+v", ack.Data)
	}
	if ack.Data["turn_id"] == "" || ack.Data["user_message_id"].(float64) <= 0 {
		t.Fatalf("ack is missing identifiers: %+v", ack.Data)
	}
	toolCall := firstEvent(t, events, "tool_call")
	if toolCall.Data["name"] != "get_budget_snapshot" {
		t.Fatalf("unexpected tool call: %+v", toolCall.Data)
	}
	toolResult := firstEvent(t, events, "tool_result")
	if toolResult.Data["ok"] != true || toolResult.Data["summary"] == "" {
		t.Fatalf("unexpected tool result: %+v", toolResult.Data)
	}
	done := firstEvent(t, events, "done")
	if done.Data["verified"] != true {
		t.Fatalf("expected verified=true, got %+v", done.Data)
	}
	if done.Data["credits_charged"].(float64) != 2 {
		t.Fatalf("credits_charged = %v", done.Data["credits_charged"])
	}
	if done.Data["credits_remaining"].(float64) != 98 {
		t.Fatalf("credits_remaining = %v", done.Data["credits_remaining"])
	}
	if done.Data["assistant_message_id"].(float64) <= 0 {
		t.Fatalf("assistant message id missing: %+v", done.Data)
	}

	messages, _, err := store.ListMessages(context.Background(), 1, sessionID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected a user and an assistant message, got %d", len(messages))
	}
	assistant := messages[1]
	if assistant.Role != RoleAssistant || assistant.Verified == nil || !*assistant.Verified {
		t.Fatalf("assistant message not verified: %+v", assistant)
	}
	if !strings.Contains(assistant.Content, "S$123.45") {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
	if len(assistant.Parts) < 3 {
		t.Fatalf("assistant parts = %+v", assistant.Parts)
	}
	session, err := store.GetSession(context.Background(), 1, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.Title != "How much can I still" {
		t.Fatalf("auto title = %q", session.Title)
	}
}

func TestSendMessageEnforcesOwnership(t *testing.T) {
	store := newMemoryStore()
	other, err := store.CreateSession(context.Background(), 2, "private")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 1}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: &scriptedModel{}, mode: llm.ModePlatform}, creditService, newFakeCoord(), 1)

	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/"+itoa(other.ID)+"/messages",
		strings.NewReader(`{"content":"hello"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/users/1/agent/sessions/"+itoa(other.ID)+"/messages", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestSendMessageRejectsConcurrentTurn(t *testing.T) {
	_, _, coord, router, sessionID := newRunningFixture(t, 1)
	coord.hold("agent:session:" + itoa(sessionID) + ":lock")

	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages",
		strings.NewReader(`{"content":"hello"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "turn_in_progress") {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
}

func TestSendMessageIsIdempotentPerClientRequestID(t *testing.T) {
	store, _, _, router, _ := newRunningFixture(t, 1)
	body := `{"content":"hello","client_request_id":"same-id"}`

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages", strings.NewReader(body)))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d: %s", first.Code, first.Body.String())
	}
	afterFirst := store.messageCount()

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages", strings.NewReader(body)))
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), "duplicate_request") {
		t.Fatalf("unexpected body: %s", second.Body.String())
	}
	if store.messageCount() != afterFirst {
		t.Fatalf("retry stored extra messages: %d -> %d", afterFirst, store.messageCount())
	}
}

func TestSendMessageRejectsExhaustedCredits(t *testing.T) {
	store := newMemoryStore()
	if _, err := store.CreateSession(context.Background(), 1, ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	creditService, creditsStore := newTestCredits()
	creditsStore.used[1] = 99
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 1}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: &scriptedModel{}, mode: llm.ModePlatform}, creditService, newFakeCoord(), 1)

	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages",
		strings.NewReader(`{"content":"hello"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "credits_exhausted") {
		t.Fatalf("unexpected body: %s", response.Body.String())
	}
	if store.messageCount() != 0 {
		t.Fatalf("a rejected turn must not store messages")
	}
}

func TestSendMessageReportsMissingModel(t *testing.T) {
	store := newMemoryStore()
	if _, err := store.CreateSession(context.Background(), 1, ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 1}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{err: llm.ErrNotConfigured}, creditService, newFakeCoord(), 1)

	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages",
		strings.NewReader(`{"content":"hello"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "llm_not_configured") {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestSendMessageValidatesContent(t *testing.T) {
	store := newMemoryStore()
	if _, err := store.CreateSession(context.Background(), 1, ""); err != nil {
		t.Fatalf("create session: %v", err)
	}
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 1}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: &scriptedModel{}, mode: llm.ModePlatform}, creditService, newFakeCoord(), 1)

	for _, body := range []string{
		`{"content":""}`,
		`{"content":"   "}`,
		`{"content":"` + strings.Repeat("a", 2001) + `"}`,
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/1/messages", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d for %q", response.Code, body)
		}
	}
}

func TestSessionCRUD(t *testing.T) {
	store := newMemoryStore()
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 1}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: &scriptedModel{}, mode: llm.ModePlatform}, creditService, newFakeCoord(), 1)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions", strings.NewReader(`{"title":"Trip"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	var created Session
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if created.Title != "Trip" {
		t.Fatalf("title = %q", created.Title)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/users/1/agent/sessions", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sessions"`) {
		t.Fatalf("list status = %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/users/1/agent/sessions/"+itoa(created.ID), strings.NewReader(`{"title":"Renamed"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Renamed") {
		t.Fatalf("rename status = %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/users/1/agent/sessions/"+itoa(created.ID), nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/users/1/agent/sessions/"+itoa(created.ID), nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d", response.Code)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstEvent(t *testing.T, events []sseEvent, name string) sseEvent {
	t.Helper()
	for _, event := range events {
		if event.Name == name {
			return event
		}
	}
	t.Fatalf("missing %s event", name)
	return sseEvent{}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }

type denyLimiter struct{ retry int }

func (d denyLimiter) Allow(context.Context, int64) (bool, int, error) {
	return false, d.retry, nil
}

func TestSendMessageRateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newMemoryStore()
	session, err := store.CreateSession(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	creditService, _ := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{}), 6, time.Second)
	router := gin.New()
	handler := newTestHandler(store, runner,
		&fakeResolver{model: &scriptedModel{}, mode: llm.ModePlatform},
		creditService, nil, newFakeCoord(), denyLimiter{retry: 42},
		func(c *gin.Context) { c.Set("auth_user_id", int64(1)); c.Next() },
		func(c *gin.Context) { c.Next() })
	handler.RegisterRoutes(router.Group("/api"))

	body := `{"content":"hello","client_request_id":"rate-1"}`
	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Retry-After"); got != "42" {
		t.Fatalf("Retry-After = %q", got)
	}
	if len(store.messages) != 0 {
		t.Fatal("a rate-limited request must not persist a message")
	}
}
