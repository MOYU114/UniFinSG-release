// Package tools is the single source of truth for the tools the agent can
// call. The in-process agent reads the registry to build the model tool list
// and the MCP adapter will register the same entries later, so names,
// descriptions and JSON schemas must stay identical on both paths.
//
// Every handler receives the caller's identity through the explicit
// UserContext; no handler reads a global or request-scoped identity.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// UserContext is the identity and turn metadata of one tool invocation.
type UserContext struct {
	UserID     int64
	SessionID  int64
	TurnID     string
	ToolCallID string
}

// Handler executes a tool. It must use only the supplied UserContext for
// identity and must not mutate the ledger.
type Handler func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error)

// Tool is one registry entry.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	ReadOnly    bool
	Destructive bool
	Handler     Handler
}

// Spec is the model-facing subset of a Tool.
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ArgError reports invalid tool arguments. The agent returns it to the model
// as an is_error tool result so the model can correct itself.
type ArgError struct {
	Message string
}

func (e *ArgError) Error() string { return e.Message }

func argErrorf(format string, args ...any) error {
	return &ArgError{Message: fmt.Sprintf(format, args...)}
}

// Registry holds tools in registration order.
type Registry struct {
	mu    sync.RWMutex
	order []string
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

var (
	ErrDuplicateTool = errors.New("tools: duplicate tool name")
	ErrInvalidTool   = errors.New("tools: invalid tool definition")
)

// Register adds a tool. Duplicate names and incomplete definitions are
// rejected so a typo cannot silently shadow a tool.
func (r *Registry) Register(tool Tool) error {
	if tool.Name == "" || tool.Description == "" || len(tool.InputSchema) == 0 || tool.Handler == nil {
		return ErrInvalidTool
	}
	if !json.Valid(tool.InputSchema) {
		return fmt.Errorf("%w: schema for %s is not valid JSON", ErrInvalidTool, tool.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTool, tool.Name)
	}
	r.tools[tool.Name] = tool
	r.order = append(r.order, tool.Name)
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns every tool in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Specs returns the model-facing tool definitions in registration order.
func (r *Registry) Specs() []Spec {
	tools := r.List()
	out := make([]Spec, 0, len(tools))
	for _, tool := range tools {
		out = append(out, Spec{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return out
}
