package agentv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"unichatgo/internal/drafts"
	"unichatgo/internal/llm"
	"unichatgo/internal/tools"
)

// fakeDraftService implements both tools.DraftCreator and the handler's Drafts
// interface. The MySQL-backed service is covered by the drafts package tests.
type fakeDraftService struct {
	mu        sync.Mutex
	next      int
	drafts    map[string]*drafts.Draft
	decisions []drafts.DecideInput
}

func newFakeDraftService() *fakeDraftService {
	return &fakeDraftService{drafts: make(map[string]*drafts.Draft)}
}

func (s *fakeDraftService) Create(_ context.Context, in drafts.CreateInput) (*drafts.Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := "d_test_" + string(rune('a'+s.next))
	draft := &drafts.Draft{
		ID: id, UserID: in.UserID, SessionID: in.SessionID, TurnID: in.TurnID, ToolCallID: in.ToolCallID,
		Kind: in.Kind, Payload: in.Payload, Before: in.Before, Warnings: in.Warnings,
		Status: drafts.StatusPending, ExpiresAt: time.Now().Add(30 * time.Minute), CreatedAt: time.Now(),
	}
	s.drafts[id] = draft
	copy := *draft
	return &copy, nil
}

func (s *fakeDraftService) Get(_ context.Context, userID int64, id string) (*drafts.Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[id]
	if !ok || draft.UserID != userID {
		return nil, drafts.ErrNotFound
	}
	copy := *draft
	return &copy, nil
}

func (s *fakeDraftService) Decide(_ context.Context, in drafts.DecideInput) (*drafts.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, ok := s.drafts[in.DraftID]
	if !ok || draft.UserID != in.UserID {
		return nil, drafts.ErrNotFound
	}
	if draft.Status != drafts.StatusPending {
		return nil, drafts.ErrNotPending
	}
	s.decisions = append(s.decisions, in)
	if in.Action == drafts.ActionReject {
		draft.Status = drafts.StatusRejected
		draft.RejectReason = in.Reason
		payload, _ := json.Marshal(map[string]any{
			"status": drafts.StatusRejected, "reason": in.Reason,
			"instruction": "The user rejected this draft. Do not resubmit the same content.",
		})
		copy := *draft
		return &drafts.Decision{Draft: &copy, ToolResult: payload}, nil
	}
	draft.Status = drafts.StatusApproved
	result := json.RawMessage(`{"transaction_ids":[1]}`)
	draft.Result = result
	payload, _ := json.Marshal(map[string]any{
		"status": drafts.StatusApproved, "applied": json.RawMessage(result), "edited": false,
	})
	copy := *draft
	return &drafts.Decision{Draft: &copy, Applied: result, ToolResult: payload}, nil
}

func (s *fakeDraftService) ExpirePending(context.Context, int64) (int64, error) { return 0, nil }

func (s *fakeDraftService) expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if draft, ok := s.drafts[id]; ok {
		draft.Status = drafts.StatusExpired
	}
}

func (s *fakeDraftService) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.drafts)
}

func (s *fakeDraftService) decisionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.decisions)
}

func newDraftRegistry(t *testing.T, service tools.Finance, creator tools.DraftCreator) *tools.Registry {
	t.Helper()
	registry, err := tools.NewAgentRegistry(service, creator, func() time.Time {
		return time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return registry
}

func proposeCall(id, name string, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

func TestRunnerPausesOnFirstProposeAndRejectsExtras(t *testing.T) {
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{
			proposeCall("call_1", "propose_transactions",
				`{"items":[{"amount_cents":650,"category":"food"}]}`),
			proposeCall("call_2", "propose_transactions",
				`{"items":[{"amount_cents":300,"category":"transport"}]}`),
		}},
		{text: "This should not run."},
	}}
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	var events []string
	result, err := runner.Run(context.Background(), RunInput{
		UserID: 1, SessionID: 2, TurnID: "turn-1", Content: "lunch 6.50 and mrt 3",
		Model: model, Emit: func(event string, _ any) error {
			events = append(events, event)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.AwaitingApproval || result.Draft == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.ModelCalls != 1 {
		t.Fatalf("model calls = %d, want 1", result.ModelCalls)
	}
	if service.count() != 1 {
		t.Fatalf("drafts = %d, want 1", service.count())
	}
	if !contains(events, "draft") {
		t.Fatalf("missing draft event in %v", events)
	}
	var draftParts, errorResults int
	for _, part := range result.Parts {
		if part.Type == PartDraft {
			draftParts++
		}
		if part.Type == PartToolResult && part.IsError {
			errorResults++
		}
	}
	if draftParts != 1 || errorResults != 1 {
		t.Fatalf("parts = %+v", result.Parts)
	}
	if result.Draft.Kind != drafts.KindCreateTransactions {
		t.Fatalf("kind = %s", result.Draft.Kind)
	}
}

func TestSendMessagePausesAndStoresDraftPart(t *testing.T) {
	store := newMemoryStore()
	session, err := store.CreateSession(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{proposeCall("call_1", "propose_transactions",
			`{"items":[{"amount_cents":650,"category":"food","merchant":"Toast Box"}]}`)}},
		{text: "I prepared a draft. " + Disclaimer},
	}}
	coord := newFakeCoord()
	creditService, creditsStore := newTestCredits()
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	router := newDraftTestRouter(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, service, coord, 1)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"record lunch 6.50"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	events := parseSSEEvents(t, response.Body.String())
	draftEvent := firstEvent(t, events, "draft")
	if draftEvent.Data["draft_id"] == "" || draftEvent.Data["kind"] != drafts.KindCreateTransactions {
		t.Fatalf("draft event = %+v", draftEvent.Data)
	}
	done := firstEvent(t, events, "done")
	if done.Data["status"] != TurnApproval {
		t.Fatalf("done = %+v", done.Data)
	}
	if done.Data["credits_charged"].(float64) != 2 {
		t.Fatalf("credits_charged = %v", done.Data["credits_charged"])
	}
	if creditsStore.used[1] != 2 {
		t.Fatalf("used credits = %d", creditsStore.used[1])
	}

	messages, _, err := store.ListMessages(context.Background(), 1, session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	assistant := messages[len(messages)-1]
	var draftPart *Part
	for i := range assistant.Parts {
		if assistant.Parts[i].Type == PartDraft {
			draftPart = &assistant.Parts[i]
		}
	}
	if draftPart == nil || draftPart.DraftID == "" {
		t.Fatalf("assistant parts = %+v", assistant.Parts)
	}

	turn, err := store.GetTurn(context.Background(), 1, assistant.TurnID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != TurnApproval || turn.ModelCalls != 1 {
		t.Fatalf("turn = %+v", turn)
	}
}

func TestDecisionApproveContinuesWithoutChargingCredits(t *testing.T) {
	store := newMemoryStore()
	session, _ := store.CreateSession(context.Background(), 1, "")
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{proposeCall("call_1", "propose_transactions",
			`{"items":[{"amount_cents":650,"category":"food"}]}`)}},
		{text: "Recorded your lunch. " + Disclaimer},
	}}
	coord := newFakeCoord()
	creditService, creditsStore := newTestCredits()
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	router := newDraftTestRouter(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, service, coord, 1)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"record lunch 6.50"}`)))
	draftEvent := firstEvent(t, parseSSEEvents(t, first.Body.String()), "draft")
	draftID := draftEvent.Data["draft_id"].(string)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/drafts/"+draftID+"/decision",
		strings.NewReader(`{"action":"approve"}`)))
	if second.Code != http.StatusOK {
		t.Fatalf("decision status = %d: %s", second.Code, second.Body.String())
	}
	events := parseSSEEvents(t, second.Body.String())
	if !containsEvent(events, "status") || !containsEvent(events, "delta") {
		t.Fatalf("missing status/delta in %v", eventNames(events))
	}
	done := firstEvent(t, events, "done")
	if done.Data["status"] != TurnCompleted {
		t.Fatalf("done = %+v", done.Data)
	}
	if done.Data["credits_charged"].(float64) != 0 {
		t.Fatalf("continuation charged credits: %+v", done.Data)
	}
	if creditsStore.used[1] != 2 {
		t.Fatalf("used credits = %d, want 2", creditsStore.used[1])
	}
	if service.decisionCount() != 1 {
		t.Fatalf("decisions = %d", service.decisionCount())
	}

	messages, _, err := store.ListMessages(context.Background(), 1, session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(messages))
	}
	turn, err := store.GetTurn(context.Background(), 1, messages[1].TurnID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != TurnCompleted || turn.ModelCalls != 2 {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.CreditsCharged != 2 {
		t.Fatalf("continuation changed charged credits: %+v", turn)
	}
	paused := messages[1]
	found := false
	for _, part := range paused.Parts {
		if part.Type == PartToolResult && part.CallID == "call_1" && strings.Contains(string(part.Result), "approved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("decision result was not appended: %+v", paused.Parts)
	}

	// A reload must restore the card with the live status and the result.
	reloaded := httptest.NewRecorder()
	router.ServeHTTP(reloaded, httptest.NewRequest(http.MethodGet,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages", nil))
	if reloaded.Code != http.StatusOK {
		t.Fatalf("reload status = %d", reloaded.Code)
	}
	var payload struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(reloaded.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	var status string
	var result json.RawMessage
	for _, message := range payload.Messages {
		for _, part := range message.Parts {
			if part.Type == PartDraft && part.DraftID == draftID {
				status = part.Status
				result = part.Result
			}
		}
	}
	if status != drafts.StatusApproved || len(result) == 0 {
		t.Fatalf("reloaded draft = %s %s", status, result)
	}
}

func TestDecisionOnExpiredDraftIsConflict(t *testing.T) {
	store := newMemoryStore()
	session, _ := store.CreateSession(context.Background(), 1, "")
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{proposeCall("call_1", "propose_transactions",
			`{"items":[{"amount_cents":650,"category":"food"}]}`)}},
		{text: "done"},
	}}
	coord := newFakeCoord()
	creditService, _ := newTestCredits()
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	router := newDraftTestRouter(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, service, coord, 1)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"record lunch"}`)))
	draftID := firstEvent(t, parseSSEEvents(t, first.Body.String()), "draft").Data["draft_id"].(string)
	service.expire(draftID)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/drafts/"+draftID+"/decision",
		strings.NewReader(`{"action":"approve"}`)))
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "draft_not_pending") {
		t.Fatalf("expired decision status = %d: %s", second.Code, second.Body.String())
	}
}

func TestDecisionRejectFeedsReasonToTheModel(t *testing.T) {
	store := newMemoryStore()
	session, _ := store.CreateSession(context.Background(), 1, "")
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{proposeCall("call_1", "propose_transactions",
			`{"items":[{"amount_cents":650,"category":"food"}]}`)}},
		{text: "No problem, I will not record it. " + Disclaimer},
	}}
	coord := newFakeCoord()
	creditService, _ := newTestCredits()
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	router := newDraftTestRouter(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, service, coord, 1)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"record lunch 6.50"}`)))
	draftID := firstEvent(t, parseSSEEvents(t, first.Body.String()), "draft").Data["draft_id"].(string)

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/drafts/"+draftID+"/decision",
		strings.NewReader(`{"action":"reject","reason":"It was 8.50, not 6.50"}`)))
	if second.Code != http.StatusOK {
		t.Fatalf("decision status = %d: %s", second.Code, second.Body.String())
	}
	if service.count() != 1 {
		t.Fatalf("reject must not create another draft, got %d", service.count())
	}
	requests := model.requests
	if len(requests) < 2 {
		t.Fatalf("model calls = %d", len(requests))
	}
	foundReason := false
	for _, message := range requests[1].Messages {
		for _, part := range message.Parts {
			if part.Type == llm.PartToolResult && strings.Contains(string(part.ToolResult), "It was 8.50") {
				foundReason = true
			}
		}
	}
	if !foundReason {
		t.Fatalf("reject reason did not reach the model: %+v", requests[1].Messages)
	}
}

func TestDecisionRejectsOtherUsersAndDoubleClicks(t *testing.T) {
	store := newMemoryStore()
	session, _ := store.CreateSession(context.Background(), 1, "")
	service := newFakeDraftService()
	model := &scriptedModel{steps: []modelStep{
		{calls: []llm.ToolCall{proposeCall("call_1", "propose_transactions",
			`{"items":[{"amount_cents":650,"category":"food"}]}`)}},
		{text: "done"},
		{text: "done again"},
	}}
	coord := newFakeCoord()
	creditService, _ := newTestCredits()
	runner := NewRunner(newDraftRegistry(t, &fakeFinance{}, service), 6, time.Second)
	router := newDraftTestRouter(store, runner,
		&fakeResolver{model: model, mode: llm.ModePlatform}, creditService, service, coord, 1)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"record lunch"}`)))
	draftID := firstEvent(t, parseSSEEvents(t, first.Body.String()), "draft").Data["draft_id"].(string)

	foreign, err := service.Create(context.Background(), drafts.CreateInput{
		UserID: 2, SessionID: session.ID, TurnID: "turn-x", ToolCallID: "call-x",
		Kind: drafts.KindCreateTransactions, Payload: json.RawMessage(`{"items":[]}`),
	})
	if err != nil {
		t.Fatalf("create foreign draft: %v", err)
	}
	other := httptest.NewRecorder()
	router.ServeHTTP(other, httptest.NewRequest(http.MethodPost,
		"/api/users/1/agent/drafts/"+foreign.ID+"/decision",
		strings.NewReader(`{"action":"approve"}`)))
	if other.Code != http.StatusNotFound {
		t.Fatalf("other user status = %d: %s", other.Code, other.Body.String())
	}

	approve := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/users/1/agent/drafts/"+draftID+"/decision",
			strings.NewReader(`{"action":"approve"}`)))
		return recorder
	}
	second := approve()
	if second.Code != http.StatusOK {
		t.Fatalf("first approve status = %d: %s", second.Code, second.Body.String())
	}
	third := approve()
	if third.Code != http.StatusConflict || !strings.Contains(third.Body.String(), "draft_not_pending") {
		t.Fatalf("second approve status = %d: %s", third.Code, third.Body.String())
	}
	if service.decisionCount() != 1 {
		t.Fatalf("decisions = %d, want 1", service.decisionCount())
	}
}

func containsEvent(events []sseEvent, name string) bool {
	for _, event := range events {
		if event.Name == name {
			return true
		}
	}
	return false
}

func eventNames(events []sseEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}
