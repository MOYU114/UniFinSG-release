package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const anthropicVersion = "2023-06-01"

type anthropicModel struct {
	endpoint string
	model    string
	key      string
	client   *http.Client
}

// NewAnthropic builds the native Anthropic Messages streaming adapter.
func NewAnthropic(cfg Config) ChatModel {
	client := cfg.HTTPClient
	if client == nil {
		client = NewHTTPClient(cfg.AllowInsecure)
	}
	return &anthropicModel{
		endpoint: normalizeAnthropicBaseURL(cfg.BaseURL),
		model:    strings.TrimSpace(cfg.Model),
		key:      strings.TrimSpace(cfg.APIKey),
		client:   client,
	}
}

func normalizeAnthropicBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/v1/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

func (m *anthropicModel) Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error) {
	payload, err := json.Marshal(m.body(req))
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
		httpReq.Header.Set("x-api-key", m.key)
	}
	httpReq.Header.Set("anthropic-version", anthropicVersion)
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

	state := &anthropicState{blocks: map[int]*anthropicBlock{}}
	scanErr := scanSSE(body, func(_ string, data string) error {
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
	if !state.done && state.stop == "" {
		return Result{}, upstreamError(0, "the model stream ended before completion", "")
	}
	for i := range result.ToolCalls {
		if err := emitEvent(emit, Event{ToolCall: &result.ToolCalls[i]}); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (m *anthropicModel) body(req Request) map[string]any {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	body := map[string]any{
		"model":      m.model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   anthropicMessages(req.Messages),
	}
	if strings.TrimSpace(req.System) != "" {
		body["system"] = req.System
	}
	tools := anthropicTools(req.Tools)
	if len(req.JSONSchema) > 0 {
		tools = upsertExtractionTool(tools, req.JSONSchema)
		body["tools"] = tools
		body["tool_choice"] = map[string]any{"type": "tool", "name": "submit_extraction"}
		return body
	}
	if req.ToolChoice == "none" {
		return body
	}
	if len(tools) > 0 {
		body["tools"] = tools
		if choice := anthropicToolChoice(req.ToolChoice); choice != nil {
			body["tool_choice"] = choice
		}
	}
	return body
}

func anthropicToolChoice(choice string) map[string]any {
	switch choice {
	case "", "auto":
		return map[string]any{"type": "auto"}
	case "required":
		return map[string]any{"type": "any"}
	default:
		return map[string]any{"type": "tool", "name": choice}
	}
}

func anthropicTools(specs []ToolSpec) []map[string]any {
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		tools = append(tools, map[string]any{
			"name":         spec.Name,
			"description":  spec.Description,
			"input_schema": rawOrEmptyObject(spec.InputSchema),
		})
	}
	return tools
}

func upsertExtractionTool(tools []map[string]any, schema json.RawMessage) []map[string]any {
	for _, tool := range tools {
		if tool["name"] == "submit_extraction" {
			tool["input_schema"] = schema
			return tools
		}
	}
	return append(tools, map[string]any{
		"name":         "submit_extraction",
		"description":  "Return the structured extraction result.",
		"input_schema": schema,
	})
}

func anthropicMessages(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		role := msg.Role
		if role != RoleAssistant {
			role = RoleUser
		}
		blocks := make([]map[string]any, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			switch part.Type {
			case PartText:
				blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
			case PartImage:
				blocks = append(blocks, map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": part.ImageMIME,
						"data":       base64String(part.ImageData),
					},
				})
			case PartToolCall:
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    part.ToolCallID,
					"name":  part.ToolName,
					"input": rawOrEmptyObject(part.ToolArgs),
				})
			case PartToolResult:
				block := map[string]any{
					"type":        "tool_result",
					"tool_use_id": part.ToolCallID,
					"content":     string(part.ToolResult),
				}
				if part.IsError {
					block["is_error"] = true
				}
				blocks = append(blocks, block)
			}
		}
		if len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": ""})
		}
		// Anthropic requires alternating roles; merge consecutive turns of the
		// same role into one message.
		if len(out) > 0 && out[len(out)-1]["role"] == role {
			previous := out[len(out)-1]["content"].([]map[string]any)
			out[len(out)-1]["content"] = append(previous, blocks...)
			continue
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	return out
}

type anthropicBlock struct {
	kind  string
	id    string
	name  string
	text  strings.Builder
	input strings.Builder
}

type anthropicState struct {
	blocks map[int]*anthropicBlock
	order  []int
	stop   string
	usage  Usage
	done   bool
}

func (s *anthropicState) block(index int, kind string) *anthropicBlock {
	block, ok := s.blocks[index]
	if !ok {
		block = &anthropicBlock{kind: kind}
		s.blocks[index] = block
		s.order = append(s.order, index)
	}
	return block
}

func (s *anthropicState) handle(data string, emit func(Event) error) error {
	var event struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Text  string          `json:"text"`
			Input json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Message struct {
			Usage struct {
				InputTokens int `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}
	switch event.Type {
	case "message_start":
		s.usage.InputTokens = event.Message.Usage.InputTokens
	case "content_block_start":
		kind := event.ContentBlock.Type
		switch kind {
		case "text":
			kind = PartText
		case "tool_use":
			kind = PartToolCall
		}
		block := s.block(event.Index, kind)
		if event.ContentBlock.ID != "" {
			block.id = event.ContentBlock.ID
		}
		if event.ContentBlock.Name != "" {
			block.name = event.ContentBlock.Name
		}
		if event.ContentBlock.Text != "" {
			block.text.WriteString(event.ContentBlock.Text)
			if err := emitEvent(emit, Event{TextDelta: event.ContentBlock.Text}); err != nil {
				return err
			}
		}
		if input := strings.TrimSpace(string(event.ContentBlock.Input)); input != "" && input != "{}" {
			block.input.WriteString(input)
		}
	case "content_block_delta":
		block := s.block(event.Index, "")
		switch event.Delta.Type {
		case "text_delta":
			block.kind = PartText
			block.text.WriteString(event.Delta.Text)
			if event.Delta.Text != "" {
				if err := emitEvent(emit, Event{TextDelta: event.Delta.Text}); err != nil {
					return err
				}
			}
		case "input_json_delta":
			block.kind = PartToolCall
			block.input.WriteString(event.Delta.PartialJSON)
		}
	case "message_delta":
		if event.Delta.StopReason != "" {
			s.stop = event.Delta.StopReason
		}
		if event.Usage.OutputTokens > 0 {
			s.usage.OutputTokens = event.Usage.OutputTokens
		}
	case "message_stop":
		s.done = true
		return errStreamDone
	case "error":
		return upstreamError(http.StatusBadGateway, event.Error.Message, "")
	}
	return nil
}

func (s *anthropicState) result() (Result, error) {
	result := Result{StopReason: s.stop, Usage: s.usage}
	var text strings.Builder
	for _, index := range s.order {
		block := s.blocks[index]
		switch block.kind {
		case PartToolCall:
			args := strings.TrimSpace(block.input.String())
			if args == "" {
				args = "{}"
			}
			if !json.Valid([]byte(args)) {
				return Result{}, upstreamError(0, "the model returned malformed tool arguments", "")
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:   block.id,
				Name: block.name,
				Args: json.RawMessage(args),
			})
		default:
			text.WriteString(block.text.String())
		}
	}
	result.Text = text.String()
	return result, nil
}
