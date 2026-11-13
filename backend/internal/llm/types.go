// Package llm is the provider-neutral streaming interface used by the agent.
// It ships two adapters, openai_compatible and anthropic, plus the resolver
// that selects a platform or BYOK model for a user.
package llm

import (
	"context"
	"encoding/json"
	"net/http"
)

// Part types carried inside a Message.
const (
	PartText       = "text"
	PartImage      = "image"
	PartToolCall   = "tool_call"
	PartToolResult = "tool_result"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Adapter kinds.
const (
	KindOpenAICompatible = "openai_compatible"
	KindAnthropic        = "anthropic"
)

// Part is one piece of a message: text, an image, a tool call or a tool result.
type Part struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	ImageMIME        string          `json:"image_mime,omitempty"`
	ImageData        []byte          `json:"image_data,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolName         string          `json:"tool_name,omitempty"`
	ToolArgs         json.RawMessage `json:"tool_args,omitempty"`
	ToolExtraContent json.RawMessage `json:"tool_extra_content,omitempty"`
	ToolResult       json.RawMessage `json:"tool_result,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
}

// Message is one conversation turn. System prompts live in Request.System.
type Message struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	// ExtraContent is opaque provider metadata, including Gemini thought signatures.
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

// ToolSpec describes a tool exposed to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Request is a single streaming model call.
type Request struct {
	System     string          `json:"system,omitempty"`
	Messages   []Message       `json:"messages"`
	Tools      []ToolSpec      `json:"tools,omitempty"`
	ToolChoice string          `json:"tool_choice,omitempty"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
	MaxTokens  int             `json:"max_tokens,omitempty"`
}

// Event is emitted while the model streams.
type Event struct {
	TextDelta string    `json:"text_delta,omitempty"`
	ToolCall  *ToolCall `json:"tool_call,omitempty"`
}

// Usage is the token accounting reported by the provider, when available.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Result is the complete response after the stream ends.
type Result struct {
	Text       string     `json:"text"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	Usage      Usage      `json:"usage"`
}

// ChatModel streams a model response. emit may be nil.
type ChatModel interface {
	Stream(ctx context.Context, req Request, emit func(Event) error) (Result, error)
}

// Config builds a concrete adapter.
type Config struct {
	Kind    string
	BaseURL string
	APIKey  string
	Model   string
	// ReasoningEffort is sent as reasoning_effort by the OpenAI-compatible
	// adapter when set (platform model only, from LLM_REASONING_EFFORT).
	ReasoningEffort string
	AllowInsecure   bool
	HTTPClient      *http.Client
}

// New builds an adapter for cfg.Kind.
func New(cfg Config) (ChatModel, error) {
	switch cfg.Kind {
	case KindOpenAICompatible:
		return NewOpenAICompatible(cfg), nil
	case KindAnthropic:
		return NewAnthropic(cfg), nil
	default:
		return nil, ErrUnknownKind
	}
}
