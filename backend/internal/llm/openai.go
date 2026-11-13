package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
)

type openAIModel struct {
	endpoint string
	model    string
	key      string
	client   *http.Client

	// gemini marks Google's OpenAI-compatible endpoint (or a Gemini model),
	// which requires a thought signature on replayed function calls.
	gemini bool

	mu   sync.Mutex
	opts bodyOptions
}

// NewOpenAICompatible builds the OpenAI-compatible streaming adapter.
func NewOpenAICompatible(cfg Config) ChatModel {
	client := cfg.HTTPClient
	if client == nil {
		client = NewHTTPClient(cfg.AllowInsecure)
	}
	endpoint := normalizeOpenAIBaseURL(cfg.BaseURL)
	return &openAIModel{
		endpoint: endpoint,
		gemini:   isGeminiTarget(endpoint, cfg.Model),
		model:    strings.TrimSpace(cfg.Model),
		key:      strings.TrimSpace(cfg.APIKey),
		client:   client,
		opts:     bodyOptions{effort: strings.TrimSpace(cfg.ReasoningEffort)},
	}
}

func isGeminiTarget(endpoint, model string) bool {
	if parsed, err := url.Parse(endpoint); err == nil && strings.HasSuffix(strings.ToLower(parsed.Hostname()), "generativelanguage.googleapis.com") {
		return true
	}
	return strings.Contains(strings.ToLower(model), "gemini")
}

// geminiSkipSignature is Google's documented placeholder for function calls
// that the model did not produce in this session (server-synthesized calls
// such as the screenshot fast path's propose_transactions, or history stored
// before signatures were persisted). Gemini 3 rejects such calls with
// "Function call is missing a thought_signature" unless it is present.
var geminiSkipSignature = json.RawMessage(`{"google":{"thought_signature":"skip_thought_signature_validator"}}`)

func normalizeOpenAIBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	// A root URL uses OpenAI's /v1 default. Explicit API prefixes (including
	// Gemini's /v1beta/openai) are already complete and must be preserved.
	parsed, err := url.Parse(base)
	if err == nil && (parsed.Path == "" || parsed.Path == "/") {
		base += "/v1"
	}
	return base + "/chat/completions"
}

// bodyOptions are request-shape adjustments learned from upstream 400s.
type bodyOptions struct {
	effort           string
	completionTokens bool // send max_completion_tokens instead of max_tokens
}

func (m *openAIModel) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	m.mu.Lock()
	opts := m.opts
	m.mu.Unlock()
	// Newer OpenAI models reject some classic chat/completions parameters
	// (e.g. gpt-6-luna: tools with the default reasoning effort, and
	// max_tokens). These 400s arrive before any output, so adjusting the
	// request and retrying is safe; the adjustment is kept for later calls.
	for attempt := 0; ; attempt++ {
		result, err := m.stream(ctx, m.body(req, opts), emit)
		next, retry := adjustForUpstream(err, opts)
		if !retry || attempt >= 2 {
			return result, err
		}
		opts = next
		m.mu.Lock()
		m.opts = opts
		m.mu.Unlock()
	}
}

func adjustForUpstream(err error, opts bodyOptions) (bodyOptions, bool) {
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusBadRequest {
		return opts, false
	}
	switch {
	case opts.effort == "" && strings.Contains(upstream.Message, "reasoning_effort"):
		opts.effort = "none"
		return opts, true
	case !opts.completionTokens && strings.Contains(upstream.Message, "max_completion_tokens"):
		opts.completionTokens = true
		return opts, true
	}
	return opts, false
}

func (m *openAIModel) stream(ctx context.Context, requestBody map[string]any, emit func(Event) error) (Result, error) {
	payload, err := json.Marshal(requestBody)
	if err != nil {
		return Result{}, fmt.Errorf("llm: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("llm: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if m.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+m.key)
	}
	resp, err := m.client.Do(httpReq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	body := newLimitedBody(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(body, 64<<10))
		return Result{}, upstreamError(resp.StatusCode, string(snippet), m.key)
	}

	state := &openAIState{calls: map[int]*openAIToolCall{}}
	scanErr := scanSSE(body, func(_ string, data string) error {
		if data == "[DONE]" {
			state.done = true
			return errStreamDone
		}
		return state.handle(data, emit)
	})
	if scanErr != nil && !errors.Is(scanErr, errStreamDone) {
		result, _ := state.result()
		return result, scanErr
	}
	result, err := state.result()
	if err != nil {
		return Result{}, err
	}
	// A truncated stream must not be billed as a completed turn. Accept either
	// the [DONE] sentinel or a finish_reason; some compatible servers send only
	// one of the two.
	if !state.done && state.finish == "" {
		return Result{}, upstreamError(0, "the model stream ended before completion", "")
	}
	for i := range result.ToolCalls {
		if err := emitEvent(emit, Event{ToolCall: &result.ToolCalls[i]}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *openAIModel) body(req Request, opts bodyOptions) map[string]any {
	body := map[string]any{
		"model":    m.model,
		"messages": openAIMessages(req, m.gemini),
		"stream":   true,
	}
	if opts.effort != "" {
		body["reasoning_effort"] = opts.effort
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  rawOrEmptyObject(tool.InputSchema),
			},
		})
	}
	if len(req.JSONSchema) > 0 {
		// Both adapters use the same structured-output path: force a
		// submit_extraction tool call. This avoids relying on json_schema
		// support, which many OpenAI-compatible servers lack (§5.2).
		tools = upsertOpenAIExtractionTool(tools, req.JSONSchema)
		body["tools"] = tools
		body["tool_choice"] = map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "submit_extraction"},
		}
	} else if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = openAIToolChoice(req.ToolChoice)
	}
	if req.MaxTokens > 0 {
		if opts.completionTokens {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	// Never send a "provider" field: several OpenAI-compatible servers reject
	// unknown top-level keys (PR #2 compatibility fix).
	return body
}

func upsertOpenAIExtractionTool(tools []map[string]any, schema json.RawMessage) []map[string]any {
	for _, tool := range tools {
		fn, ok := tool["function"].(map[string]any)
		if ok && fn["name"] == "submit_extraction" {
			fn["parameters"] = rawOrEmptyObject(schema)
			return tools
		}
	}
	return append(tools, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "submit_extraction",
			"description": "Return the structured extraction result.",
			"parameters":  rawOrEmptyObject(schema),
		},
	})
}

func openAIToolChoice(choice string) any {
	switch choice {
	case "", "auto":
		return "auto"
	case "none":
		return "none"
	case "required":
		return "required"
	default:
		return map[string]any{"type": "function", "function": map[string]any{"name": choice}}
	}
}

func openAIMessages(req Request, gemini bool) []map[string]any {
	out := make([]map[string]any, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}
	for _, msg := range req.Messages {
		if msg.Role == RoleTool {
			for _, part := range msg.Parts {
				if part.Type != PartToolResult {
					continue
				}
				out = append(out, map[string]any{
					"role":         "tool",
					"tool_call_id": part.ToolCallID,
					"content":      string(part.ToolResult),
				})
			}
			continue
		}
		role := msg.Role
		if role == "" {
			role = RoleUser
		}
		var texts []string
		var images []map[string]any
		var calls []map[string]any
		for _, part := range msg.Parts {
			switch part.Type {
			case PartText:
				texts = append(texts, part.Text)
			case PartImage:
				images = append(images, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": imageDataURL(part.ImageMIME, part.ImageData)},
				})
			case PartToolCall:
				call := map[string]any{
					"id":   part.ToolCallID,
					"type": "function",
					"function": map[string]any{
						"name":      part.ToolName,
						"arguments": string(rawOrEmptyObject(part.ToolArgs)),
					},
				}
				if len(part.ToolExtraContent) > 0 {
					call["extra_content"] = part.ToolExtraContent
				}
				calls = append(calls, call)
			}
		}
		entry := map[string]any{"role": role}
		text := strings.Join(texts, "\n")
		if len(images) > 0 {
			content := make([]map[string]any, 0, len(images)+1)
			if text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
			content = append(content, images...)
			entry["content"] = content
		} else if len(calls) > 0 && text == "" {
			// OpenAI expects null content for a pure tool-call assistant turn.
			entry["content"] = nil
		} else {
			entry["content"] = text
		}
		if len(calls) > 0 {
			if gemini {
				ensureGeminiSignature(calls)
			}
			entry["tool_calls"] = calls
		}
		out = append(out, entry)
	}
	return out
}

// ensureGeminiSignature gives the first call of an assistant step a
// placeholder signature when none of its calls carries one. Gemini only
// requires the signature on the first call of a parallel batch.
func ensureGeminiSignature(calls []map[string]any) {
	for _, call := range calls {
		if _, ok := call["extra_content"]; ok {
			return
		}
	}
	calls[0]["extra_content"] = geminiSkipSignature
}

type openAIToolCall struct {
	id           string
	name         string
	arguments    strings.Builder
	extraContent json.RawMessage
}

type openAIState struct {
	text   strings.Builder
	calls  map[int]*openAIToolCall
	order  []int
	finish string
	usage  Usage
	done   bool
}

func (s *openAIState) handle(data string, emit func(Event) error) error {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index        int             `json:"index"`
					ID           string          `json:"id"`
					ExtraContent json.RawMessage `json:"extra_content"`
					Function     struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			s.text.WriteString(choice.Delta.Content)
			if err := emitEvent(emit, Event{TextDelta: choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, call := range choice.Delta.ToolCalls {
			entry, ok := s.calls[call.Index]
			if !ok {
				entry = &openAIToolCall{}
				s.calls[call.Index] = entry
				s.order = append(s.order, call.Index)
			}
			if call.ID != "" {
				entry.id = call.ID
			}
			if call.Function.Name != "" && entry.name == "" {
				entry.name = call.Function.Name
			}
			entry.arguments.WriteString(call.Function.Arguments)
			if len(call.ExtraContent) > 0 && string(call.ExtraContent) != "null" {
				entry.extraContent = append(json.RawMessage(nil), call.ExtraContent...)
			}
		}
		if choice.FinishReason != "" {
			s.finish = choice.FinishReason
		}
	}
	if chunk.Usage != nil {
		s.usage = Usage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
	}
	return nil
}

func (s *openAIState) result() (Result, error) {
	result := Result{Text: s.text.String(), StopReason: s.finish, Usage: s.usage}
	sort.Ints(s.order)
	for _, index := range s.order {
		call := s.calls[index]
		args := strings.TrimSpace(call.arguments.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return Result{}, upstreamError(0, "the model returned malformed tool arguments", "")
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: call.id, Name: call.name, Args: json.RawMessage(args), ExtraContent: call.extraContent})
	}
	return result, nil
}

func emitEvent(emit func(Event) error, event Event) error {
	if emit == nil {
		return nil
	}
	return emit(event)
}
