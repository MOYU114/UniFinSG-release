// Command mock-llm provides a tiny OpenAI-compatible streaming endpoint for
// local Agent integration tests. It deliberately never logs request bodies.
//
// Behaviour:
//   - a forced submit_extraction tool choice returns a fixed extraction sample;
//   - a user message about budgets returns a get_budget_snapshot tool call;
//   - a user message about transactions returns a list_transactions tool call;
//   - once a tool result is present it answers with the exact remaining amount;
//   - otherwise it streams MOCK_LLM_TEXT, so the plain text mode still works.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type completionRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools      []json.RawMessage `json:"tools"`
	ToolChoice json.RawMessage   `json:"tool_choice"`
}

const mockFollowUps = "Where did my money go this month?\nWill I overspend this month?\nRecord coffee: 4.5"

const defaultAnswer = "Based on the supplied facts, your budget is on track."

func main() {
	port := os.Getenv("MOCK_LLM_PORT")
	if port == "" {
		port = "8091"
	}
	http.HandleFunc("/v1/chat/completions", complete)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		panic(err)
	}
}

func complete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req completionRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if errorRate() > 0 && rand.Intn(100) < errorRate() {
		http.Error(w, "mock llm error", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	if delay := durationEnv("MOCK_LLM_TTFT_MS"); delay > 0 {
		time.Sleep(delay)
	}

	// The agent's follow-up call sends no tools and quotes the last exchange.
	if len(req.Tools) == 0 && strings.HasPrefix(lastUserText(req), "User's last message:") {
		// writeText splits on whitespace, so send the lines as one delta.
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": mockFollowUps}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		return
	}
	if name, args, ok := toolCallFor(req); ok {
		writeToolCall(w, flusher, name, args)
		return
	}
	writeText(w, flusher, answerFor(req))
}

// toolCallFor decides whether this request should return a tool call.
func toolCallFor(req completionRequest) (string, string, bool) {
	if forcedExtraction(req.ToolChoice) {
		return "submit_extraction", `{"records":[{"amount_cents":650,"spent_on":null,"merchant":"Kopitiam","category":"food","note":"lunch","confidence":0.9}],"image_kind":"receipt","notes":"mock sample"}`, true
	}
	if hasToolResult(req) {
		return "", "", false
	}
	text := strings.ToLower(lastUserText(req))
	switch {
	case text == "":
		return "", "", false
	case strings.Contains(text, "transaction") || strings.Contains(text, "spending list"):
		return "list_transactions", `{"limit":10}`, true
	case strings.Contains(text, "budget") || strings.Contains(text, "remaining") ||
		strings.Contains(text, "spent") || strings.Contains(text, "left") || strings.Contains(text, "afford"):
		return "get_budget_snapshot", `{}`, true
	default:
		return "", "", false
	}
}

func forcedExtraction(choice json.RawMessage) bool {
	if len(choice) == 0 {
		return false
	}
	var parsed struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(choice, &parsed); err != nil {
		return false
	}
	return parsed.Function.Name == "submit_extraction"
}

// hasToolResult reports whether the current turn, i.e. the messages after the
// last user message, already contains a tool result. Earlier turns in the
// same conversation must not suppress tool calls for a new question.
func hasToolResult(req completionRequest) bool {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		switch req.Messages[i].Role {
		case "user":
			return false
		case "tool":
			return true
		}
	}
	return false
}

func lastUserText(req completionRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		return contentText(req.Messages[i].Content)
	}
	return ""
}

// contentText accepts both a plain string and the OpenAI content-parts array.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	collected := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			collected = append(collected, part.Text)
		}
	}
	return strings.Join(collected, " ")
}

// answerFor produces the final text. When a tool result carries a remaining
// amount it is echoed exactly so number verification passes end to end.
func answerFor(req completionRequest) string {
	if configured := os.Getenv("MOCK_LLM_TEXT"); configured != "" {
		return shapeOutput(configured, outputWords())
	}
	if remaining, ok := lastRemainingCents(req); ok {
		return fmt.Sprintf("Based on your ledger, you have S$%s left this month.", formatCents(remaining))
	}
	return shapeOutput(defaultAnswer, outputWords())
}

func lastRemainingCents(req completionRequest) (int64, bool) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "tool" {
			continue
		}
		text := contentText(req.Messages[i].Content)
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			continue
		}
		if cents, ok := findCents(payload, "remaining_cents"); ok {
			return cents, true
		}
	}
	return 0, false
}

func findCents(value any, key string) (int64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[key]; ok {
			if number, ok := raw.(float64); ok {
				return int64(number), true
			}
		}
		for _, child := range typed {
			if cents, ok := findCents(child, key); ok {
				return cents, true
			}
		}
	case []any:
		for _, child := range typed {
			if cents, ok := findCents(child, key); ok {
				return cents, true
			}
		}
	}
	return 0, false
}

func formatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func writeToolCall(w http.ResponseWriter, flusher http.Flusher, name, args string) {
	callID := "call_mock_" + strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
	chunk, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "id": callID, "type": "function",
					"function": map[string]any{"name": name, "arguments": args},
				}},
			},
			"finish_reason": nil,
		}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
	finish, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", finish)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeText(w http.ResponseWriter, flusher http.Flusher, answer string) {
	for _, token := range strings.Fields(answer) {
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": token + " "}}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
		if tps := tokensPerSecond(); tps > 0 {
			time.Sleep(time.Second / time.Duration(tps))
		} else if delay := durationEnv("MOCK_LLM_DELAY_MS"); delay > 0 {
			time.Sleep(delay)
		}
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func durationEnv(name string) time.Duration {
	n, _ := strconv.Atoi(os.Getenv(name))
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Millisecond
}

func tokensPerSecond() int {
	n, _ := strconv.Atoi(os.Getenv("MOCK_LLM_TOKENS_PER_SEC"))
	if n < 0 {
		return 0
	}
	return n
}

func outputWords() int {
	n, _ := strconv.Atoi(os.Getenv("MOCK_LLM_OUTPUT_WORDS"))
	if n < 0 {
		return 0
	}
	return n
}

func shapeOutput(answer string, words int) string {
	if words <= 0 {
		return answer
	}
	parts := strings.Fields(answer)
	if len(parts) >= words {
		return strings.Join(parts[:words], " ")
	}
	for len(parts) < words {
		parts = append(parts, fmt.Sprintf("word%d", len(parts)+1))
	}
	return strings.Join(parts, " ")
}

func errorRate() int {
	n, _ := strconv.Atoi(os.Getenv("MOCK_LLM_ERROR_RATE"))
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}
