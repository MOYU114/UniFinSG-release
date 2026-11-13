package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStreamerSendsOnlyOpenAIFields(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("LLM_BASE_URL", server.URL)
	t.Setenv("LLM_PROVIDER", "openai")
	streamer := newHTTPStreamer()
	var got strings.Builder
	if err := streamer.Stream(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, func(d string) error {
		got.WriteString(d)
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got.String() != "Hi" {
		t.Fatalf("unexpected answer %q", got.String())
	}
	if _, ok := body["provider"]; ok {
		t.Fatal("request body must not include a non-OpenAI provider field")
	}
}

func TestHTTPStreamerReportsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"unknown_parameter"}}`)
	}))
	defer server.Close()
	t.Setenv("LLM_BASE_URL", server.URL)
	err := newHTTPStreamer().Stream(context.Background(), []ChatMessage{{Role: "user", Content: "hello"}}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "unknown_parameter") {
		t.Fatalf("expected upstream status and body in error, got %v", err)
	}
}
