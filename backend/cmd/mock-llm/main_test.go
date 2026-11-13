package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCompleteHonorsOutputAndErrorControls(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	t.Setenv("MOCK_LLM_OUTPUT_WORDS", "3")
	t.Setenv("MOCK_LLM_TTFT_MS", "0")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	w := httptest.NewRecorder()
	complete(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
	if strings.Count(w.Body.String(), `"content"`) != 3 {
		t.Fatalf("expected 3 streamed tokens: %s", w.Body.String())
	}
	_ = os.Getenv("MOCK_LLM_TOKENS_PER_SEC")
}

func TestCompleteErrorRate100(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "100")
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	w := httptest.NewRecorder()
	complete(w, req)
	if w.Code != 502 {
		t.Fatalf("status=%d, want 502", w.Code)
	}
}

func TestCompleteReturnsBudgetToolCall(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	t.Setenv("MOCK_LLM_OUTPUT_WORDS", "0")
	t.Setenv("MOCK_LLM_TEXT", "")
	body := `{"messages":[{"role":"user","content":"How much budget do I have left?"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"get_budget_snapshot"`) {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"tool_calls"`) {
		t.Fatalf("expected an OpenAI tool_calls delta: %s", w.Body.String())
	}
}

func TestCompleteReturnsTransactionsToolCall(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	body := `{"messages":[{"role":"user","content":"Show my transactions"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	if !strings.Contains(w.Body.String(), `"list_transactions"`) {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestCompleteCallsToolsAgainInALaterTurn(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	t.Setenv("MOCK_LLM_TEXT", "")
	body := `{"messages":[
		{"role":"user","content":"How much budget is left?"},
		{"role":"assistant","content":null},
		{"role":"tool","content":"{\"remaining_cents\":100}"},
		{"role":"assistant","content":"S$1.00"},
		{"role":"user","content":"Show my transactions"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	if !strings.Contains(w.Body.String(), `"list_transactions"`) {
		t.Fatalf("an earlier turn's tool result suppressed the tool call: %s", w.Body.String())
	}
}

func TestCompleteEchoesToolRemainingAmount(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	t.Setenv("MOCK_LLM_OUTPUT_WORDS", "0")
	body := `{"messages":[
		{"role":"user","content":"How much budget do I have left?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_budget_snapshot","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"{\"remaining_cents\":12345}"}
	]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	if !strings.Contains(w.Body.String(), "S$123.45") {
		t.Fatalf("expected the exact tool amount: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"tool_calls"`) {
		t.Fatalf("a tool result must not trigger another tool call: %s", w.Body.String())
	}
}

func TestCompleteReturnsForcedExtractionSample(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	body := `{"messages":[{"role":"user","content":"receipt"}],
		"tool_choice":{"type":"function","function":{"name":"submit_extraction"}}}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	if !strings.Contains(w.Body.String(), `"submit_extraction"`) {
		t.Fatalf("expected submit_extraction: %s", w.Body.String())
	}
}

func TestCompleteReturnsFollowUpLines(t *testing.T) {
	t.Setenv("MOCK_LLM_ERROR_RATE", "0")
	body := `{"messages":[{"role":"user","content":"User's last message:\nbudget left?\n\nAssistant's reply:\nYou have S$12.00 left."}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	complete(w, req)
	out := w.Body.String()
	if w.Code != 200 || strings.Contains(out, "tool_calls") || !strings.Contains(out, `month?\nWill I`) || !strings.Contains(out, "[DONE]") {
		t.Fatalf("unexpected response: %d %s", w.Code, out)
	}
}
