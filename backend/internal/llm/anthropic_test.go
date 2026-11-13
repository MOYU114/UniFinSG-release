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

func writeAnthropicStream(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", event)
	}
}

func TestAnthropicStreamsTextWithNativeHeaders(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("missing x-api-key header: %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != anthropicVersion {
			t.Errorf("unexpected anthropic-version %q", r.Header.Get("anthropic-version"))
		}
		body = decodeBody(t, r)
		writeAnthropicStream(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":9}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		)
	}))
	defer server.Close()

	model := NewAnthropic(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "anthropic-key", Model: "claude-test"})
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
	if deltas.String() != "Hi there" || result.Text != "Hi there" {
		t.Fatalf("unexpected text %q / %q", deltas.String(), result.Text)
	}
	if result.StopReason != "end_turn" || result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 3 {
		t.Fatalf("unexpected result %+v", result)
	}
	if body["system"] != "be brief" {
		t.Fatalf("system prompt missing: %+v", body)
	}
	if body["max_tokens"].(float64) <= 0 {
		t.Fatalf("max_tokens is required: %+v", body)
	}
}

func TestAnthropicToolUseInputFragments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicStream(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"submit_extraction","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"records\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"[]}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`,
		)
	}))
	defer server.Close()

	model := NewAnthropic(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	result, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "extract"}}}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "submit_extraction" || string(call.Args) != `{"records":[]}` {
		t.Fatalf("unexpected tool call %+v", call)
	}
}

func TestAnthropicForcesExtractionToolAndEncodesImages(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = decodeBody(t, r)
		writeAnthropicStream(w, `{"type":"message_stop"}`)
	}))
	defer server.Close()

	model := NewAnthropic(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	_, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{
			{Type: PartText, Text: "receipt"},
			{Type: PartImage, ImageMIME: "image/jpeg", ImageData: []byte{9, 8, 7}},
		}}},
		JSONSchema: json.RawMessage(`{"type":"object","properties":{"records":{"type":"array"}}}`),
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	tools := body["tools"].([]any)
	found := false
	for _, tool := range tools {
		entry := tool.(map[string]any)
		if entry["name"] == "submit_extraction" {
			found = true
			if entry["input_schema"].(map[string]any)["type"] != "object" {
				t.Fatalf("schema was not attached: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatalf("submit_extraction tool was not added: %+v", tools)
	}
	choice := body["tool_choice"].(map[string]any)
	if choice["type"] != "tool" || choice["name"] != "submit_extraction" {
		t.Fatalf("tool_choice was not forced: %+v", choice)
	}
	messages := body["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	image := content[1].(map[string]any)
	source := image["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/jpeg" || source["data"] != "CQgH" {
		t.Fatalf("unexpected image block %+v", image)
	}
}

func TestAnthropicUpstreamErrorIsWrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer server.Close()

	model := NewAnthropic(Config{BaseURL: server.URL, HTTPClient: server.Client(), APIKey: "k", Model: "m"})
	_, err := model.Stream(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "hi"}}}},
	}, nil)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusTooManyRequests {
		t.Fatalf("expected a 429 UpstreamError, got %v", err)
	}
	if !strings.Contains(upstream.Message, "slow down") {
		t.Fatalf("upstream message was lost: %s", upstream.Message)
	}
}

func TestAnthropicBaseURLNormalisation(t *testing.T) {
	tests := map[string]string{
		"https://api.anthropic.com":             "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/":            "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1":          "https://api.anthropic.com/v1/messages",
		"https://api.anthropic.com/v1/messages": "https://api.anthropic.com/v1/messages",
	}
	for input, want := range tests {
		if got := normalizeAnthropicBaseURL(input); got != want {
			t.Errorf("normalizeAnthropicBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}
