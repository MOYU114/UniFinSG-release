//go:build live

package llm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in only: uses a real provider and may consume Google API quota.
func TestGeminiLive(t *testing.T) {
	key := os.Getenv("GOOGLE_API_KEY")
	if key == "" {
		t.Skip("GOOGLE_API_KEY is required for live verification")
	}
	model := NewOpenAICompatible(Config{
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/",
		Model:   "gemini-3-flash-preview", APIKey: key,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t.Run("streamed_text", func(t *testing.T) {
		var deltas strings.Builder
		result, err := model.Stream(ctx, Request{
			Messages:  []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "Reply exactly API_OK, without explanation."}}}},
			MaxTokens: 1024,
		}, func(event Event) error { deltas.WriteString(event.TextDelta); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.Text) != "API_OK" || result.Text != deltas.String() {
			t.Fatalf("unexpected streamed reply: %q", result.Text)
		}
	})
	t.Run("tool_call_and_continuation", func(t *testing.T) {
		messages := []Message{{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "Call report_status with status API_OK. After receiving its result, reply exactly API_OK."}}}}
		tool := ToolSpec{Name: "report_status", Description: "Report connectivity status.", InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}`)}
		result, err := model.Stream(ctx, Request{Messages: messages, Tools: []ToolSpec{tool}, ToolChoice: "report_status", MaxTokens: 1024}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != tool.Name || result.ToolCalls[0].ID == "" {
			t.Fatal("provider did not return one identifiable report_status call")
		}
		call := result.ToolCalls[0]
		var args struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil || args.Status != "API_OK" {
			t.Fatal("tool arguments did not match the request")
		}
		messages = append(messages,
			Message{Role: RoleAssistant, Parts: []Part{{Type: PartToolCall, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Args, ToolExtraContent: call.ExtraContent}}},
			Message{Role: RoleTool, Parts: []Part{{Type: PartToolResult, ToolCallID: call.ID, ToolName: call.Name, ToolResult: json.RawMessage(`{"status":"API_OK"}`)}}},
		)
		continued, err := model.Stream(ctx, Request{Messages: messages, Tools: []ToolSpec{tool}, ToolChoice: "none", MaxTokens: 1024}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(continued.Text) != "API_OK" {
			t.Fatalf("unexpected continuation: %q", continued.Text)
		}
	})
	t.Run("synthesized_unsigned_call_replay", func(t *testing.T) {
		// Mirrors the screenshot fast path: a server-built tool call with no
		// thought signature in history must not be rejected.
		tool := ToolSpec{Name: "propose_transactions", Description: "Propose ledger entries.", InputSchema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object"}}}}`)}
		messages := []Message{
			{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "Record my receipt."}}},
			{Role: RoleAssistant, Parts: []Part{{Type: PartToolCall, ToolCallID: "call_synth", ToolName: tool.Name, ToolArgs: json.RawMessage(`{"items":[]}`)}}},
			{Role: RoleTool, Parts: []Part{{Type: PartToolResult, ToolCallID: "call_synth", ToolName: tool.Name, ToolResult: json.RawMessage(`{"status":"pending_approval"}`)}}},
			{Role: RoleUser, Parts: []Part{{Type: PartText, Text: "Reply exactly API_OK."}}},
		}
		if _, err := model.Stream(ctx, Request{Messages: messages, Tools: []ToolSpec{tool}, MaxTokens: 1024}, nil); err != nil {
			t.Fatal(err)
		}
	})
}
