package agentv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"unichatgo/internal/llm"
)

// ctxCheckingStore behaves like a real database driver: writes fail once the
// caller's context is cancelled.
type ctxCheckingStore struct{ *memoryStore }

func (s ctxCheckingStore) InsertMessage(ctx context.Context, message Message) (*Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.memoryStore.InsertMessage(ctx, message)
}

func (s ctxCheckingStore) FinishTurn(ctx context.Context, turn Turn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memoryStore.FinishTurn(ctx, turn)
}

// disconnectingModel simulates the browser going away while the answer is
// being generated.
type disconnectingModel struct{ cancel context.CancelFunc }

func (m *disconnectingModel) Stream(_ context.Context, _ llm.Request, emit func(llm.Event) error) (llm.Result, error) {
	text := "Noted. " + Disclaimer
	_ = emit(llm.Event{TextDelta: text})
	m.cancel()
	return llm.Result{Text: text}, nil
}

func TestSendMessagePersistsAnswerAfterClientDisconnect(t *testing.T) {
	memory := newMemoryStore()
	store := ctxCheckingStore{memory}
	session, err := memory.CreateSession(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creditService, creditStore := newTestCredits()
	runner := NewRunner(newTestRegistry(t, &fakeFinance{remaining: 100}), 6, time.Second)
	router := newTestRouter(store, runner, &fakeResolver{model: &disconnectingModel{cancel: cancel}, mode: llm.ModePlatform},
		creditService, newFakeCoord(), 1)

	request := httptest.NewRequest(http.MethodPost, "/api/users/1/agent/sessions/"+itoa(session.ID)+"/messages",
		strings.NewReader(`{"content":"hello"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(httptest.NewRecorder(), request)

	messages, _, err := memory.ListMessages(context.Background(), 1, session.ID, 0, 10)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[1].Role != RoleAssistant {
		t.Fatalf("assistant answer was not persisted after disconnect: %+v", messages)
	}
	if got := creditStore.used[1]; got != 1 {
		t.Fatalf("credits used = %d, want 1 (the answer was produced)", got)
	}
}
