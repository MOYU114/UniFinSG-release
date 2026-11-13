package agentv2

import (
	"encoding/json"
	"time"

	"unichatgo/internal/llm"
)

const (
	// contextMessageLimit is the number of stored messages replayed to the model.
	contextMessageLimit = 20
	// contextTokenBudget is the approximate prompt budget for history.
	contextTokenBudget = 12000
	// imagePlaceholder replaces historical images; only the current turn may
	// send real image bytes.
	imagePlaceholder = "[图片]"
)

// buildContext turns stored history plus the current user text into the model
// conversation. It keeps whole messages (never splitting a tool call from its
// result) and drops the oldest ones until the token budget fits.
func buildContext(history []Message, current string, _ time.Time) []llm.Message {
	var parts []llm.Part
	if current != "" {
		parts = []llm.Part{{Type: llm.PartText, Text: current}}
	}
	return buildContextParts(history, parts)
}

// buildContextParts is buildContext for a current message that may carry
// images. Only this message sends image bytes; historical images are replaced
// by the placeholder in messageToLLM.
func buildContextParts(history []Message, current []llm.Part) []llm.Message {
	var message *llm.Message
	if len(current) > 0 {
		message = &llm.Message{Role: llm.RoleUser, Parts: current}
	}
	return buildMessages(history, message)
}

// buildContextFromHistory resumes a paused turn: the decision tool_result is
// already part of the stored history, so no extra user message is appended.
func buildContextFromHistory(history []Message, now time.Time) []llm.Message {
	return buildMessages(history, nil)
}

func buildMessages(history []Message, current *llm.Message) []llm.Message {
	if len(history) > contextMessageLimit {
		history = history[len(history)-contextMessageLimit:]
	}
	type group struct {
		messages []llm.Message
		tokens   int
	}
	groups := make([]group, 0, len(history))
	for _, message := range history {
		converted := messageToLLM(message)
		if len(converted) == 0 {
			continue
		}
		tokens := 0
		for _, item := range converted {
			tokens += estimateTokens(item)
		}
		groups = append(groups, group{messages: converted, tokens: tokens})
	}
	total := 0
	if current != nil {
		total = estimateTokens(*current)
	}
	for _, item := range groups {
		total += item.tokens
	}
	start := 0
	for start < len(groups) && total > contextTokenBudget {
		total -= groups[start].tokens
		start++
	}
	size := len(groups) - start
	if current != nil {
		size++
	}
	out := make([]llm.Message, 0, size)
	for _, item := range groups[start:] {
		out = append(out, item.messages...)
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

// messageToLLM converts one stored message. An assistant message may carry
// both tool calls and their results; adapters require the results to be a
// separate tool turn, so it becomes up to two model messages.
func messageToLLM(message Message) []llm.Message {
	role := message.Role
	if role == "" {
		role = RoleUser
	}
	if len(message.Parts) == 0 {
		if message.Content == "" {
			return nil
		}
		return []llm.Message{{Role: modelRole(role), Parts: []llm.Part{{Type: llm.PartText, Text: message.Content}}}}
	}
	switch role {
	case RoleAssistant:
		head := make([]llm.Part, 0, len(message.Parts))
		results := make([]llm.Part, 0, len(message.Parts))
		callIDs := make([]string, 0, len(message.Parts))
		for _, part := range message.Parts {
			switch part.Type {
			case PartText:
				if part.Text != "" {
					head = append(head, llm.Part{Type: llm.PartText, Text: part.Text})
				}
			case PartToolCall:
				callIDs = append(callIDs, part.ID)
				head = append(head, llm.Part{
					Type: llm.PartToolCall, ToolCallID: part.ID, ToolName: part.Name, ToolArgs: part.Args, ToolExtraContent: part.ExtraContent,
				})
			case PartToolResult:
				results = append(results, toolResultPart(part))
			}
		}
		// A paused draft has no tool_result until the user decides. Adapters
		// require one result per tool call, so the pending state is reported
		// explicitly; the real result is appended to this message later.
		if len(callIDs) > 0 {
			answered := make(map[string]bool, len(results))
			for _, part := range results {
				answered[part.ToolCallID] = true
			}
			for _, id := range callIDs {
				if answered[id] {
					continue
				}
				results = append(results, llm.Part{
					Type: llm.PartToolResult, ToolCallID: id,
					ToolResult: json.RawMessage(`{"status":"pending_approval","message":"This draft is waiting for the user's decision."}`),
				})
			}
		}
		out := make([]llm.Message, 0, 2)
		if len(head) > 0 {
			out = append(out, llm.Message{Role: llm.RoleAssistant, Parts: head})
		}
		if len(results) > 0 {
			out = append(out, llm.Message{Role: llm.RoleTool, Parts: results})
		}
		return out
	case RoleTool:
		results := make([]llm.Part, 0, len(message.Parts))
		for _, part := range message.Parts {
			if part.Type == PartToolResult {
				results = append(results, toolResultPart(part))
			}
		}
		if len(results) == 0 {
			return nil
		}
		return []llm.Message{{Role: llm.RoleTool, Parts: results}}
	default:
		parts := make([]llm.Part, 0, len(message.Parts))
		for _, part := range message.Parts {
			switch part.Type {
			case PartText:
				if part.Text != "" {
					parts = append(parts, llm.Part{Type: llm.PartText, Text: part.Text})
				}
			case PartImage:
				parts = append(parts, llm.Part{Type: llm.PartText, Text: imagePlaceholder})
			}
		}
		if len(parts) == 0 {
			if message.Content == "" {
				return nil
			}
			parts = append(parts, llm.Part{Type: llm.PartText, Text: message.Content})
		}
		return []llm.Message{{Role: llm.RoleUser, Parts: parts}}
	}
}

func toolResultPart(part Part) llm.Part {
	result := part.Result
	if len(result) == 0 {
		result = []byte(`{}`)
	}
	return llm.Part{
		Type: llm.PartToolResult, ToolCallID: part.CallID, ToolName: part.Name,
		ToolResult: result, IsError: part.IsError,
	}
}

func modelRole(role string) string {
	switch role {
	case RoleAssistant:
		return llm.RoleAssistant
	case RoleTool:
		return llm.RoleTool
	default:
		return llm.RoleUser
	}
}

// estimateTokens is a deliberately conservative heuristic: roughly two runes
// per token, which over-counts Latin text and fits CJK text.
func estimateTokens(message llm.Message) int {
	runes := 0
	for _, part := range message.Parts {
		runes += len([]rune(part.Text))
		runes += len([]rune(part.ToolName)) + len(part.ToolArgs)/2 + len(part.ToolResult)/2
	}
	return runes/2 + 4
}
