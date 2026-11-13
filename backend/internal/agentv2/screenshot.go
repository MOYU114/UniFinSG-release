package agentv2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/llm"
)

// DraftCreator stores a validated create_transactions proposal. It is the
// same drafts.Service the propose_* tools use.
type DraftCreator interface {
	Create(ctx context.Context, in drafts.CreateInput) (*drafts.Draft, error)
}

const extractionToolName = "submit_extraction"

// ExtractionPrompt is the system prompt of the fixed screenshot workflow.
func ExtractionPrompt(now time.Time) string {
	return fmt.Sprintf(`You read one image from a budgeting chat: a receipt, a bank app screen or a payment confirmation.
Extract every expense you can see and return it by calling %s exactly once.
Rules:
- amount_cents is the total in Singapore cents as an integer (S$6.50 -> 650). Never guess: omit a record when the amount is unreadable.
- spent_on is YYYY-MM-DD, or null when the date is not visible. Today is %s (Asia/Singapore).
- merchant is the shop or payee, or null.
- category is one of food, transport, housing_tuition, daily_shopping, study_digital, entertainment_other, or null when unsure.
- note is a short description, or null.
- confidence is 0..1.
- image_kind is receipt, bank_app, payment_screenshot or other.
- If the image contains no expense, return an empty records array.
Return no prose outside the tool call.`, extractionToolName, finance.SingaporeToday(now))
}

// runScreenshot is the fixed extraction workflow from design section 6.3.
// A non-empty extraction becomes a draft and pauses the turn; an empty one
// falls back to the normal ReAct loop with the image still in context.
func (r *Runner) runScreenshot(ctx context.Context, in RunInput) (RunResult, error) {
	emit := in.Emit
	if emit == nil {
		emit = func(string, any) error { return nil }
	}
	now := r.now()
	if err := emit("status", map[string]string{"phase": "extracting"}); err != nil {
		return RunResult{}, err
	}

	extraction, text, err := r.extract(ctx, in, now)
	if err != nil {
		code := "llm_upstream_error"
		if ctx.Err() != nil {
			code = "timeout"
		}
		return RunResult{ModelCalls: 1, ErrorCode: code}, err
	}

	records, warnings := ValidateExtraction(extraction, now)
	if len(records) == 0 || r.drafts == nil {
		// Fall back to ReAct; the extraction text is not shown because it was
		// produced for the tool call, not for the user.
		steps := r.maxSteps - 1
		if steps < 1 {
			steps = 1
		}
		result, runErr := r.loop(ctx, loopInput{
			base:     in,
			system:   SystemPrompt(now, r.disclaimer),
			messages: buildContextParts(in.History, in.Parts),
			steps:    steps,
		})
		result.ModelCalls++
		return result, runErr
	}

	if r.duplicates != nil {
		// Duplicate detection is advisory; a failure must not fail the turn.
		_ = r.duplicates.MarkDuplicates(ctx, in.UserID, records)
	}
	warnings = DraftWarnings(warnings, records)

	items := make([]draftItem, 0, len(records))
	for _, record := range records {
		items = append(items, draftItem{
			AmountCents: record.AmountCents, SpentOn: record.SpentOn, Category: record.Category,
			Merchant: record.Merchant, Note: record.Note,
		})
	}
	payload, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return RunResult{ModelCalls: 1, ErrorCode: "draft_failed"}, err
	}
	callID := "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	draft, err := r.drafts.Create(ctx, drafts.CreateInput{
		UserID: in.UserID, SessionID: in.SessionID, TurnID: in.TurnID, ToolCallID: callID,
		Kind: drafts.KindCreateTransactions, Payload: payload, Warnings: warnings,
	})
	if err != nil {
		return RunResult{ModelCalls: 1, ErrorCode: "draft_failed"}, err
	}
	info := &DraftInfo{
		ID: draft.ID, Kind: draft.Kind, Payload: draft.Payload, Before: draft.Before,
		Warnings: draft.Warnings, ExpiresAt: draft.ExpiresAt, ToolCallID: callID,
	}
	parts := []Part{
		{Type: PartToolCall, ID: callID, Name: "propose_transactions", Args: payload},
		{Type: PartDraft, DraftID: draft.ID, Kind: draft.Kind},
	}
	if err := emit("tool_call", map[string]any{
		"id": callID, "name": "propose_transactions", "args": json.RawMessage(payload),
	}); err != nil {
		return RunResult{ModelCalls: 1, ErrorCode: "", Output: true, AwaitingApproval: true, Draft: info, Parts: parts}, err
	}
	if err := emit("draft", info); err != nil {
		return RunResult{ModelCalls: 1, Output: true, AwaitingApproval: true, Draft: info, Parts: parts}, err
	}
	if err := emit("tool_result", map[string]any{
		"id": callID, "name": "propose_transactions", "ok": true,
		"summary": "Created a draft that is waiting for your confirmation",
	}); err != nil {
		return RunResult{ModelCalls: 1, Output: true, AwaitingApproval: true, Draft: info, Parts: parts}, err
	}

	sentence := strings.TrimSpace(text)
	if sentence == "" {
		sentence = extractionSentence(len(records))
	}
	sentence = truncateRunes(sentence, 400)
	if err := emit("delta", map[string]string{"text": sentence}); err != nil {
		return RunResult{ModelCalls: 1, Output: true, AwaitingApproval: true, Draft: info, Parts: parts}, err
	}
	parts = append(parts, Part{Type: PartText, Text: sentence})
	return RunResult{
		Answer: sentence, Parts: parts, UsedTools: true, ModelCalls: 1,
		Output: true, AwaitingApproval: true, Draft: info,
	}, nil
}

// extract makes the one forced submit_extraction call.
func (r *Runner) extract(ctx context.Context, in RunInput, now time.Time) (Extraction, string, error) {
	result, err := in.Model.Stream(ctx, llm.Request{
		System:     ExtractionPrompt(now),
		Messages:   []llm.Message{{Role: llm.RoleUser, Parts: in.Parts}},
		ToolChoice: "required",
		JSONSchema: extractionSchemaJSON,
		MaxTokens:  2000,
	}, func(llm.Event) error { return nil })
	if err != nil {
		return Extraction{}, result.Text, err
	}
	for _, call := range result.ToolCalls {
		if call.Name != extractionToolName {
			continue
		}
		var extraction Extraction
		if err := json.Unmarshal(call.Args, &extraction); err != nil {
			return Extraction{}, result.Text, nil
		}
		return extraction, result.Text, nil
	}
	return Extraction{}, result.Text, nil
}

func extractionSentence(count int) string {
	if count == 1 {
		return "I found 1 expense in the image. Please check the draft below."
	}
	return fmt.Sprintf("I found %d expenses in the image. Please check the draft below.", count)
}

// draftItem is the create_transactions payload shape shared with the tools.
type draftItem struct {
	AmountCents int64            `json:"amount_cents"`
	SpentOn     string           `json:"spent_on"`
	Category    insight.Category `json:"category"`
	Merchant    *string          `json:"merchant"`
	Note        *string          `json:"note"`
}
