package agentv2

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/llm"
)

const maxFollowUps = 3

// Follow-up questions shown under a finished reply. They are phrased so the
// agent can answer each with its read-only tools.
const (
	followRemaining = "How much can I still spend this month?"
	followWhere     = "Where did my money go this month?"
	followOverspend = "Will I overspend this month?"
	followTips      = "Any tips to save money this week?"
	followRecent    = "Show my spending this week"
	followTop       = "Which category am I spending the most on?"
	followRecord    = "Record lunch: 6.5"
)

// FollowUps suggests up to three next questions for a finished turn. They are
// chosen from the tools the turn used, so they cost no extra model call. A
// turn that failed or is waiting on a draft decision gets none: the user has
// something else to do first.
func FollowUps(content string, parts []Part, status string) []string {
	if status != TurnCompleted {
		return []string{}
	}
	used := map[string]bool{}
	for _, part := range parts {
		if (part.Type == PartToolCall || part.Type == PartToolResult) && part.Name != "" {
			used[part.Name] = true
		}
	}
	var candidates []string
	switch {
	case used["propose_transactions"], used["propose_update_transaction"], used["propose_delete_transaction"]:
		candidates = []string{followRemaining, followRecent, followOverspend}
	case used["propose_update_budget"]:
		candidates = []string{followRemaining, followOverspend, followTips}
	case used["list_transactions"]:
		candidates = []string{followTop, followRemaining, followTips}
	case used["get_budget_snapshot"]:
		candidates = []string{followWhere, followOverspend, followTips}
	default:
		candidates = []string{followRemaining, followWhere, followRecord}
	}
	asked := strings.ToLower(strings.TrimSpace(content))
	out := make([]string, 0, maxFollowUps)
	for _, candidate := range candidates {
		if strings.ToLower(candidate) == asked || len(out) == maxFollowUps {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

const (
	followUpTimeout      = 8 * time.Second
	followUpMaxTokens    = 800
	followUpMaxRunes     = 120
	followUpContextRunes = 1500
)

const followUpSystem = `You suggest what a student might ask a personal budgeting assistant next.
The assistant can read the user's monthly budget, spending by category and recent transactions, and can record, change or delete expenses after the user confirms.
Given the user's last message and the assistant's reply, write exactly 3 short follow-up questions or requests the user could send next.
Rules:
- Write them as the user, in the same language as the user's message.
- Each must be something the assistant can answer from the ledger or do with its tools. No investment or loan advice.
- Do not repeat the user's last message. Keep each under 12 words.
- Output only the 3 lines, one per line, with no numbering, bullets, quotes or extra text.`

// SuggestFollowUps asks the turn's own model for three follow-up questions
// that fit this reply. It falls back to the rule-based FollowUps when the turn
// did not complete, the call fails or the output is unusable, so a finished
// reply always gets suggestions.
func SuggestFollowUps(ctx context.Context, model llm.ChatModel, question, answer string, parts []Part, status string) []string {
	fallback := FollowUps(question, parts, status)
	// A client that already left will never see the suggestions.
	if status != TurnCompleted || model == nil || strings.TrimSpace(answer) == "" || ctx.Err() != nil {
		return fallback
	}
	ctx, cancel := context.WithTimeout(ctx, followUpTimeout)
	defer cancel()
	asked := strings.TrimSpace(question)
	if asked == "" {
		asked = "(The user confirmed a ledger change the assistant proposed.)"
	}
	prompt := "User's last message:\n" + truncateRunes(asked, followUpContextRunes) +
		"\n\nAssistant's reply:\n" + truncateRunes(strings.TrimSpace(answer), followUpContextRunes)
	result, err := model.Stream(ctx, llm.Request{
		System:    followUpSystem,
		Messages:  []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: prompt}}}},
		MaxTokens: followUpMaxTokens,
	}, func(llm.Event) error { return nil })
	if err != nil {
		return fallback
	}
	if parsed := parseFollowUps(result.Text, question); len(parsed) > 0 {
		return parsed
	}
	return fallback
}

// parseFollowUps keeps up to three clean, distinct lines from the model output.
func parseFollowUps(text, question string) []string {
	asked := strings.ToLower(strings.TrimSpace(question))
	seen := map[string]bool{}
	out := make([]string, 0, maxFollowUps)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-*•·0123456789.)、 \t")
		line = strings.Trim(strings.TrimSpace(line), "\"'“”‘’`")
		line = strings.TrimSpace(line)
		key := strings.ToLower(line)
		n := utf8.RuneCountInString(line)
		if n < 2 || n > followUpMaxRunes || seen[key] || key == asked {
			continue
		}
		seen[key] = true
		out = append(out, line)
		if len(out) == maxFollowUps {
			break
		}
	}
	return out
}
