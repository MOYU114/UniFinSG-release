package agentv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"unichatgo/internal/llm"
	"unichatgo/internal/tools"
)

// Defaults from design section 11.
const (
	DefaultMaxSteps    = 6
	DefaultTurnTimeout = 90 * time.Second
)

// Emitter writes one SSE event.
type Emitter func(event string, payload any) error

// RunInput is everything one turn needs. Identity travels in UserID/SessionID
// only; the runner never reads a request context for it.
type RunInput struct {
	UserID    int64
	SessionID int64
	TurnID    string
	History   []Message
	Content   string
	// Parts is the current user message. When it carries images the runner
	// takes the screenshot workflow; Content stays as the plain-text preview.
	Parts []llm.Part
	Model llm.ChatModel
	Emit  Emitter
}

// ResumeInput continues a paused turn after a draft decision. History already
// contains the decision tool_result; Steps is the remaining model-call budget
// (negative means the full budget).
type ResumeInput struct {
	UserID    int64
	SessionID int64
	TurnID    string
	History   []Message
	Steps     int
	Model     llm.ChatModel
	Emit      Emitter
}

// RunResult is the outcome of one turn.
type RunResult struct {
	Answer     string
	Parts      []Part
	UsedTools  bool
	ModelCalls int
	ErrorCode  string
	// Output reports whether the turn produced any user-visible output, which
	// decides whether credits are settled or released.
	Output bool
	// AwaitingApproval reports that a propose_* tool created a draft and the
	// turn stopped before executing anything else.
	AwaitingApproval bool
	Draft            *DraftInfo
}

// Runner executes the ReAct loop.
type Runner struct {
	registry   *tools.Registry
	maxSteps   int
	timeout    time.Duration
	now        func() time.Time
	disclaimer string
	drafts     DraftCreator
	duplicates DuplicateFinder
}

func NewRunner(registry *tools.Registry, maxSteps int, timeout time.Duration) *Runner {
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	return &Runner{
		registry: registry, maxSteps: maxSteps, timeout: timeout,
		now: time.Now, disclaimer: Disclaimer,
	}
}

// WithDrafts enables the screenshot workflow's create_transactions draft.
func (r *Runner) WithDrafts(creator DraftCreator) *Runner {
	r.drafts = creator
	return r
}

// WithDuplicates enables the possible_duplicate marker of the screenshot
// workflow.
func (r *Runner) WithDuplicates(finder DuplicateFinder) *Runner {
	r.duplicates = finder
	return r
}

// WithClock pins the prompt date for tests.
func (r *Runner) WithClock(now func() time.Time) *Runner {
	if now != nil {
		r.now = now
	}
	return r
}

// Run streams one turn. A returned error means the model or transport failed;
// RunResult still carries any text produced before the failure.
func (r *Runner) Run(ctx context.Context, in RunInput) (RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := r.now()
	if hasImageParts(in.Parts) {
		return r.runScreenshot(ctx, in)
	}
	return r.loop(ctx, loopInput{
		base:     in,
		system:   SystemPrompt(now, r.disclaimer),
		messages: buildContextParts(in.History, currentParts(in)),
		steps:    r.maxSteps,
	})
}

// currentParts is the current user message. Callers that only provide text
// keep the plain string path.
func currentParts(in RunInput) []llm.Part {
	if len(in.Parts) > 0 {
		return in.Parts
	}
	if in.Content == "" {
		return nil
	}
	return []llm.Part{{Type: llm.PartText, Text: in.Content}}
}

func hasImageParts(parts []llm.Part) bool {
	for _, part := range parts {
		if part.Type == llm.PartImage {
			return true
		}
	}
	return false
}

// Resume continues the same ReAct loop after a draft decision. Continuation is
// free, so the caller must not reserve or settle credits for it.
func (r *Runner) Resume(ctx context.Context, in ResumeInput) (RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	steps := in.Steps
	if steps < 0 {
		steps = r.maxSteps
	}
	now := r.now()
	return r.loop(ctx, loopInput{
		base: RunInput{
			UserID: in.UserID, SessionID: in.SessionID, TurnID: in.TurnID,
			Model: in.Model, Emit: in.Emit,
		},
		system:   SystemPrompt(now, r.disclaimer),
		messages: buildContextFromHistory(in.History, now),
		steps:    steps,
	})
}

type loopInput struct {
	base     RunInput
	system   string
	messages []llm.Message
	steps    int
}

func (r *Runner) loop(ctx context.Context, in loopInput) (RunResult, error) {
	emit := in.base.Emit
	if emit == nil {
		emit = func(string, any) error { return nil }
	}

	specs := toToolSpecs(r.registry.Specs())

	var answer strings.Builder
	parts := make([]Part, 0, 8)
	usedTools := false
	modelCalls := 0
	errorCode := ""
	var runErr error
	var draft *DraftInfo

	for {
		if modelCalls >= in.steps {
			errorCode = "max_steps"
			break
		}
		if err := ctx.Err(); err != nil {
			errorCode = "timeout"
			runErr = err
			break
		}
		if err := emit("status", map[string]string{"phase": "thinking"}); err != nil {
			return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
		}
		streamed := false
		result, err := in.base.Model.Stream(ctx, llm.Request{
			System: in.system, Messages: in.messages, Tools: specs, ToolChoice: "auto",
		}, func(event llm.Event) error {
			if event.TextDelta == "" {
				return nil
			}
			streamed = true
			answer.WriteString(event.TextDelta)
			return emit("delta", map[string]string{"text": event.TextDelta})
		})
		modelCalls++
		if err != nil {
			if !streamed && result.Text != "" {
				answer.WriteString(result.Text)
			}
			runErr = err
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				errorCode = "timeout"
			} else {
				errorCode = "llm_upstream_error"
			}
			break
		}
		if !streamed && result.Text != "" {
			answer.WriteString(result.Text)
			if err := emit("delta", map[string]string{"text": result.Text}); err != nil {
				return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
			}
		}
		if len(result.ToolCalls) == 0 {
			break
		}
		usedTools = true

		head := make([]llm.Part, 0, len(result.ToolCalls)+1)
		if result.Text != "" {
			head = append(head, llm.Part{Type: llm.PartText, Text: result.Text})
		}
		for _, call := range result.ToolCalls {
			head = append(head, llm.Part{
				Type: llm.PartToolCall, ToolCallID: call.ID, ToolName: call.Name, ToolArgs: call.Args, ToolExtraContent: call.ExtraContent,
			})
			parts = append(parts, Part{Type: PartToolCall, ID: call.ID, Name: call.Name, Args: call.Args, ExtraContent: call.ExtraContent})
		}
		in.messages = append(in.messages, llm.Message{Role: llm.RoleAssistant, Parts: head})

		if err := emit("status", map[string]string{"phase": "calling_tool"}); err != nil {
			return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
		}
		toolParts := make([]llm.Part, 0, len(result.ToolCalls))
		for _, call := range result.ToolCalls {
			if err := emit("tool_call", map[string]any{
				"id": call.ID, "name": call.Name, "args": rawOrEmptyObject(call.Args),
			}); err != nil {
				return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
			}
			if draft != nil {
				// Design section 6.2: once one draft is waiting, the other
				// propose_* calls in the same response must not execute.
				payload := errorPayload("a draft is already waiting for the user's confirmation; " +
					"submit at most one draft per response")
				parts = append(parts, Part{
					Type: PartToolResult, CallID: call.ID, Name: call.Name, Result: payload, IsError: true,
				})
				toolParts = append(toolParts, llm.Part{
					Type: llm.PartToolResult, ToolCallID: call.ID, ToolName: call.Name,
					ToolResult: payload, IsError: true,
				})
				if err := emit("tool_result", map[string]any{
					"id": call.ID, "name": call.Name, "ok": false,
					"summary": "Skipped: a draft is already waiting for confirmation",
				}); err != nil {
					return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
				}
				continue
			}
			payload, isError, summary := r.execute(ctx, in.base, call)
			if !isError {
				if info := parsePendingDraft(payload, call.ID); info != nil {
					draft = info
					// The decision endpoint appends this call's tool_result
					// after the user decides, so the paused message keeps only
					// the call and the draft part.
					parts = append(parts, Part{Type: PartDraft, DraftID: info.ID, Kind: info.Kind})
					if err := emit("draft", info); err != nil {
						return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
					}
					if err := emit("tool_result", map[string]any{
						"id": call.ID, "name": call.Name, "ok": true,
						"summary": "Created a draft that is waiting for your confirmation",
					}); err != nil {
						return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
					}
					continue
				}
			}
			parts = append(parts, Part{
				Type: PartToolResult, CallID: call.ID, Name: call.Name, Result: payload, IsError: isError,
			})
			toolParts = append(toolParts, llm.Part{
				Type: llm.PartToolResult, ToolCallID: call.ID, ToolName: call.Name,
				ToolResult: payload, IsError: isError,
			})
			if err := emit("tool_result", map[string]any{
				"id": call.ID, "name": call.Name, "ok": !isError, "summary": summary,
			}); err != nil {
				return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), err
			}
		}
		in.messages = append(in.messages, llm.Message{Role: llm.RoleTool, Parts: toolParts})
		if draft != nil {
			break
		}
	}

	if answer.Len() > 0 {
		parts = append(parts, Part{Type: PartText, Text: answer.String()})
	}
	return r.finish(answer, parts, usedTools, modelCalls, errorCode, draft), runErr
}

func (r *Runner) finish(answer strings.Builder, parts []Part, usedTools bool, modelCalls int,
	errorCode string, draft *DraftInfo) RunResult {
	return RunResult{
		Answer:           answer.String(),
		Parts:            parts,
		UsedTools:        usedTools,
		ModelCalls:       modelCalls,
		ErrorCode:        errorCode,
		Output:           answer.Len() > 0 || usedTools,
		AwaitingApproval: draft != nil,
		Draft:            draft,
	}
}

// execute runs one tool call and always returns a JSON payload the model can
// read, including for unknown tools and invalid arguments, so it can correct
// itself on the next step.
func (r *Runner) execute(ctx context.Context, in RunInput, call llm.ToolCall) (json.RawMessage, bool, string) {
	tool, ok := r.registry.Get(call.Name)
	if !ok {
		return errorPayload(fmt.Sprintf("unknown tool %q; available tools: %s", call.Name, r.toolNames())),
			true, fmt.Sprintf("Unknown tool %s", call.Name)
	}
	uc := tools.UserContext{
		UserID: in.UserID, SessionID: in.SessionID, TurnID: in.TurnID, ToolCallID: call.ID,
	}
	value, err := safeExecute(ctx, tool.Handler, uc, call.Args)
	if err != nil {
		return errorPayload(err.Error()), true, fmt.Sprintf("%s could not run: %s", call.Name, err.Error())
	}
	payload, marshalErr := marshalToolResult(value)
	if marshalErr != nil {
		return errorPayload("the tool result could not be encoded"), true,
			fmt.Sprintf("%s returned an unreadable result", call.Name)
	}
	return payload, false, summarizeToolResult(call.Name, payload)
}

func safeExecute(ctx context.Context, handler tools.Handler, uc tools.UserContext, args json.RawMessage) (value any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool panicked")
		}
	}()
	return handler(ctx, uc, args)
}

func marshalToolResult(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("invalid JSON result")
		}
		return raw, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func errorPayload(message string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]string{"error": message})
	return encoded
}

// parsePendingDraft detects the pending_approval result of a propose_* tool.
func parsePendingDraft(payload json.RawMessage, callID string) *DraftInfo {
	var candidate struct {
		DraftID   string          `json:"draft_id"`
		Status    string          `json:"status"`
		Kind      string          `json:"kind"`
		Payload   json.RawMessage `json:"payload"`
		Before    json.RawMessage `json:"before"`
		Warnings  []string        `json:"warnings"`
		ExpiresAt time.Time       `json:"expires_at"`
	}
	if err := json.Unmarshal(payload, &candidate); err != nil {
		return nil
	}
	if candidate.Status != tools.PendingApproval || candidate.DraftID == "" {
		return nil
	}
	if candidate.Warnings == nil {
		candidate.Warnings = []string{}
	}
	return &DraftInfo{
		ID: candidate.DraftID, Kind: candidate.Kind, Payload: candidate.Payload,
		Before: candidate.Before, Warnings: candidate.Warnings,
		ExpiresAt: candidate.ExpiresAt, ToolCallID: callID,
	}
}

func (r *Runner) toolNames() string {
	names := make([]string, 0, len(r.registry.List()))
	for _, tool := range r.registry.List() {
		names = append(names, tool.Name)
	}
	return strings.Join(names, ", ")
}

func toToolSpecs(specs []tools.Spec) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(specs))
	for _, spec := range specs {
		out = append(out, llm.ToolSpec{Name: spec.Name, Description: spec.Description, InputSchema: spec.InputSchema})
	}
	return out
}

func rawOrEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
