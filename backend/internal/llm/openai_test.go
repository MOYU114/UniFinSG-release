package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func writeOpenAIStream(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestOpenAIStreamsTextWithoutProviderField(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization header %q", got)
		}
		body = decodeBody(t, r)
		writeOpenAIStream(w,
			`{"choices":[{"delta":{"content":"Hel"},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"content":"lo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`,
		)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "test-key", Model: "gpt-test"})
	var deltas strings.Builder
	result, err := model.Stream(context.Background(), Request{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
	}, func(event Event) error {
		deltas.WriteString(event.TextDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if deltas.String() != "Hello" || result.Text != "Hello" {
		t.Fatalf("unexpected text %q / %q", deltas.String(), result.Text)
	}
	if result.StopReason != "stop" || result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 2 {
		t.Fatalf("unexpected result %+v", result)
	}
	if _, ok := body["provider"]; ok {
		t.Fatal("request must not contain a provider field")
	}
	if body["model"] != "gpt-test" || body["stream"] != true {
		t.Fatalf("unexpected request body %+v", body)
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 || messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("unexpected messages %+v", messages)
	}
}

func TestOpenAIToolCallArgumentFragmentsAccumulate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOpenAIStream(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_transactions","arguments":"{\"from\":"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"2026-11-01\"}"}}]},"finish_reason":"tool_calls"}]}`,
		)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	var emitted []ToolCall
	result, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "list"}}}},
		Tools:    []ToolSpec{{Name: "list_transactions", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(event Event) error {
		if event.ToolCall != nil {
			emitted = append(emitted, *event.ToolCall)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "list_transactions" {
		t.Fatalf("unexpected call %+v", call)
	}
	if string(call.Args) != `{"from":"2026-11-01"}` {
		t.Fatalf("arguments were not accumulated: %s", call.Args)
	}
	if len(emitted) != 1 || emitted[0].Name != "list_transactions" {
		t.Fatalf("tool call event not emitted: %+v", emitted)
	}
}

func TestOpenAIEncodesImagesAndToolResults(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	_, err := model.Stream(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Parts: []Part{
				{Type: PartText, Text: "what is this?"},
				{Type: PartImage, ImageMIME: "image/png", ImageData: []byte{1, 2, 3}},
			}},
			{Role: RoleAssistant, Parts: []Part{
				{Type: PartText, Text: "checking"},
				{Type: PartToolCall, ToolCallID: "call_1", ToolName: "list_transactions", ToolArgs: json.RawMessage(`{"limit":5}`)},
			}},
			{Role: RoleTool, Parts: []Part{
				{Type: PartToolResult, ToolCallID: "call_1", ToolResult: json.RawMessage(`{"items":[]}`)},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected three messages, got %d", len(messages))
	}
	user := messages[0].(map[string]any)
	content := user["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,AQID" {
		t.Fatalf("image was not encoded as a data URL: %+v", content)
	}
	assistant := messages[1].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	if calls[0].(map[string]any)["function"].(map[string]any)["arguments"] != `{"limit":5}` {
		t.Fatalf("tool call arguments were not preserved: %+v", calls)
	}
	tool := messages[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != `{"items":[]}` {
		t.Fatalf("unexpected tool message %+v", tool)
	}
}

func TestOpenAIJSONSchemaForcesExtractionTool(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"{}"}}]}`)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	_, err := model.Stream(context.Background(), Request{
		Messages:   []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "extract"}}}},
		JSONSchema: json.RawMessage(`{"type":"object"}`),
		MaxTokens:  64,
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if _, ok := body["response_format"]; ok {
		t.Fatalf("json_schema response_format must not be used: %+v", body)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "submit_extraction" {
		t.Fatalf("unexpected tool_choice %+v", choice)
	}
	tools := body["tools"].([]any)
	found := false
	for _, raw := range tools {
		fn := raw.(map[string]any)["function"].(map[string]any)
		if fn["name"] == "submit_extraction" && fn["parameters"].(map[string]any)["type"] == "object" {
			found = true
		}
	}
	if !found {
		t.Fatalf("submit_extraction tool was not registered: %+v", tools)
	}
	if body["max_tokens"].(float64) != 64 {
		t.Fatalf("max_tokens missing: %+v", body)
	}
}

func TestOpenAITruncatedStreamFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	if _, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
	}, nil); err == nil {
		t.Fatal("a stream without [DONE] or finish_reason must fail")
	}
}

func TestOpenAIUpstreamErrorIsRedactedAndTruncated(t *testing.T) {
	key := "sk-live-abcdef123456"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, key+" "+strings.Repeat("detail ", 200))
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: key, Model: "m"})
	_, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
	}, nil)
	if err == nil {
		t.Fatal("expected an upstream error")
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("expected UpstreamError, got %T", err)
	}
	if upstream.Status != http.StatusUnauthorized {
		t.Fatalf("unexpected status %d", upstream.Status)
	}
	if strings.Contains(upstream.Message, key) || strings.Contains(err.Error(), key) {
		t.Fatal("api key leaked into the error")
	}
	if len([]rune(upstream.Message)) > maxUpstreamMessage {
		t.Fatalf("message was not truncated: %d runes", len([]rune(upstream.Message)))
	}
	if !strings.Contains(upstream.Message, "[redacted]") {
		t.Fatalf("key-shaped text was not redacted: %s", upstream.Message)
	}
}

func TestOpenAIBaseURLNormalisation(t *testing.T) {
	tests := map[string]string{
		"https://api.example.com":                                  "https://api.example.com/v1/chat/completions",
		"https://api.example.com/":                                 "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1":                               "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1/":                              "https://api.example.com/v1/chat/completions",
		"https://api.example.com/v1/chat/completions":              "https://api.example.com/v1/chat/completions",
		"https://generativelanguage.googleapis.com/v1beta/openai/": "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		"https://api.example.com/custom/api":                       "https://api.example.com/custom/api/chat/completions",
	}
	for input, want := range tests {
		if got := normalizeOpenAIBaseURL(input); got != want {
			t.Errorf("normalizeOpenAIBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenAIToolChoice(t *testing.T) {
	if openAIToolChoice("") != "auto" || openAIToolChoice("none") != "none" || openAIToolChoice("required") != "required" {
		t.Fatal("string tool choices were not preserved")
	}
	choice := openAIToolChoice("propose_transactions").(map[string]any)
	if choice["function"].(map[string]any)["name"] != "propose_transactions" {
		t.Fatalf("named tool choice was not encoded: %+v", choice)
	}
}

func TestOpenAIToolSignatureSurvivesStreamingAndReplay(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/chat/completions" {
			t.Errorf("API prefix changed: %s", r.URL.Path)
		}
		body := decodeBody(t, r)
		requests++
		if requests == 1 {
			writeOpenAIStream(w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"report_status","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"extra_content":{"google":{"thought_signature":"opaque-test-signature"}}}]},"finish_reason":"tool_calls"}]}`)
			return
		}
		messages := body["messages"].([]any)
		call := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
		extra, ok := call["extra_content"].(map[string]any)
		if !ok || extra["google"].(map[string]any)["thought_signature"] != "opaque-test-signature" {
			t.Error("tool signature was lost during replay")
		}
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"API_OK"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	model := NewOpenAICompatible(Config{BaseURL: server.URL + "/v1beta/openai/", HTTPClient: server.Client(), Model: "gemini-test"})
	first, err := model.Stream(context.Background(), Request{}, nil)
	if err != nil || len(first.ToolCalls) != 1 {
		t.Fatalf("first call: %v", err)
	}
	call := first.ToolCalls[0]
	_, err = model.Stream(context.Background(), Request{Messages: []Message{
		{Role: RoleAssistant, Parts: []Part{{Type: PartToolCall, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Args, ToolExtraContent: call.ExtraContent}}},
		{Role: RoleTool, Parts: []Part{{Type: PartToolResult, ToolCallID: call.ID, ToolResult: json.RawMessage(`{}`)}}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIRetriesWithoutReasoningWhenToolsAreRefused(t *testing.T) {
	var efforts []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		efforts = append(efforts, body["reasoning_effort"])
		if body["reasoning_effort"] != "none" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6-luna in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."}}`)
			return
		}
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), Model: "gpt-6-luna"})
	result, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
		Tools:    []ToolSpec{{Name: "list_categories", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(Event) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if result.Text != "ok" || len(efforts) != 2 || efforts[0] != nil || efforts[1] != "none" {
		t.Fatalf("text=%q efforts=%v", result.Text, efforts)
	}
}

func TestOpenAISendsConfiguredReasoningEffort(t *testing.T) {
	var got any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = decodeBody(t, r)["reasoning_effort"]
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), Model: "m", ReasoningEffort: "low"})
	if _, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
	}, func(Event) error { return nil }); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got != "low" {
		t.Fatalf("reasoning_effort = %v", got)
	}
}

func TestOpenAIRetriesWithMaxCompletionTokens(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeBody(t, r)
		if _, ok := body["max_tokens"]; ok {
			keys = append(keys, "max_tokens")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens","code":"unsupported_parameter"}}`)
			return
		}
		if body["max_completion_tokens"] != float64(2000) {
			t.Errorf("max_completion_tokens = %v", body["max_completion_tokens"])
		}
		keys = append(keys, "max_completion_tokens")
		writeOpenAIStream(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	model := NewOpenAICompatible(Config{BaseURL: server.URL, HTTPClient: server.Client(), Model: "gpt-6-luna"})
	request := Request{
		Messages:  []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
		MaxTokens: 2000,
	}
	for i := 0; i < 2; i++ {
		if _, err := model.Stream(context.Background(), request, func(Event) error { return nil }); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	// The adjustment is remembered: the second call goes straight to the new key.
	if strings.Join(keys, ",") != "max_tokens,max_completion_tokens,max_completion_tokens" {
		t.Fatalf("requests = %v", keys)
	}
}

func TestGeminiReplayAddsPlaceholderSignatureToUnsignedCalls(t *testing.T) {
	calls := func(extra json.RawMessage) Request {
		return Request{Messages: []Message{{Role: RoleAssistant, Parts: []Part{
			{Type: PartToolCall, ToolCallID: "call_a", ToolName: "propose_transactions", ToolArgs: json.RawMessage(`{}`), ToolExtraContent: extra},
			{Type: PartToolCall, ToolCallID: "call_b", ToolName: "list_categories", ToolArgs: json.RawMessage(`{}`)},
		}}}}
	}
	toolCalls := func(msgs []map[string]any) []map[string]any {
		return msgs[0]["tool_calls"].([]map[string]any)
	}

	// Synthesized (unsigned) calls get the placeholder on the first call only.
	got := toolCalls(openAIMessages(calls(nil), true))
	if string(got[0]["extra_content"].(json.RawMessage)) != string(geminiSkipSignature) {
		t.Fatalf("first unsigned call missing placeholder: %+v", got[0])
	}
	if _, ok := got[1]["extra_content"]; ok {
		t.Fatal("placeholder must only go on the first call")
	}

	// A real signature is never replaced.
	real := json.RawMessage(`{"google":{"thought_signature":"real"}}`)
	got = toolCalls(openAIMessages(calls(real), true))
	if string(got[0]["extra_content"].(json.RawMessage)) != string(real) {
		t.Fatal("real signature was overwritten")
	}

	// Non-Gemini providers are left untouched.
	got = toolCalls(openAIMessages(calls(nil), false))
	if _, ok := got[0]["extra_content"]; ok {
		t.Fatal("placeholder sent to a non-Gemini provider")
	}
}

func TestIsGeminiTarget(t *testing.T) {
	if !isGeminiTarget("https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", "x") {
		t.Fatal("Google endpoint not detected")
	}
	if !isGeminiTarget("https://proxy.example/v1/chat/completions", "gemini-3-flash-preview") {
		t.Fatal("Gemini model not detected")
	}
	if isGeminiTarget("https://api.openai.com/v1/chat/completions", "gpt-6-luna") {
		t.Fatal("OpenAI misdetected as Gemini")
	}
}
