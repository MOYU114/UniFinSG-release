package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/drafts"
	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

// DraftCreator stores a validated proposal. *drafts.Service implements it.
type DraftCreator interface {
	Create(ctx context.Context, in drafts.CreateInput) (*drafts.Draft, error)
}

// PendingApproval is the status every propose_* tool returns. The runner sees
// it, emits the draft card and pauses the turn.
const PendingApproval = "pending_approval"

// proposalResult is the tool result for a propose_* call. It never contains
// ledger writes, only the draft the user has to confirm.
type proposalResult struct {
	DraftID   string          `json:"draft_id"`
	Status    string          `json:"status"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	Before    json.RawMessage `json:"before,omitempty"`
	Warnings  []string        `json:"warnings"`
	ExpiresAt time.Time       `json:"expires_at"`
}

const proposeTransactionsDescription = `Propose new transactions for the user to confirm. ` +
	`This does not write the ledger: it creates a draft card that the user must approve. ` +
	`Use it when the user asks to record one or more expenses. Amounts are SGD cents. ` +
	`Call it at most once per response and never claim the expense is recorded until the user confirms.`

const proposeUpdateTransactionDescription = `Propose changes to one existing transaction for the user to confirm. ` +
	`This does not write the ledger: it creates a draft card with a before/after comparison. ` +
	`Call list_transactions first if you do not know the transaction id.`

const proposeDeleteTransactionDescription = `Propose deleting one existing transaction for the user to confirm. ` +
	`This is destructive and does not write the ledger by itself: it creates a draft card that the user must approve. ` +
	`Call list_transactions first if you do not know the transaction id.`

const proposeUpdateBudgetDescription = `Propose a budget change for one month for the user to confirm. ` +
	`This does not write the ledger: it creates a draft card showing the before/after category budgets. ` +
	`Only include the fields that change; omitted fields keep their current value.`

type proposeItem struct {
	AmountCents int64            `json:"amount_cents"`
	SpentOn     string           `json:"spent_on"`
	Category    insight.Category `json:"category"`
	Merchant    *string          `json:"merchant"`
	Note        *string          `json:"note"`
}

type proposeTransactionsInput struct {
	Items []proposeItem `json:"items"`
}

type proposeUpdateInput struct {
	TransactionID int64           `json:"transaction_id"`
	Changes       json.RawMessage `json:"changes"`
}

type proposeDeleteInput struct {
	TransactionID int64  `json:"transaction_id"`
	Reason        string `json:"reason"`
}

type proposeBudgetInput struct {
	Month string `json:"month"`
	finance.BudgetInput
}

type proposeBudgetPayload struct {
	Month string `json:"month"`
	finance.BudgetInput
}

func proposeTransactionsTool(creator DraftCreator, now func() time.Time) Tool {
	return Tool{
		Name:        "propose_transactions",
		Description: proposeTransactionsDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "description": "One entry per expense, at most 20.",
      "minItems": 1,
      "maxItems": 20,
      "items": {
        "type": "object",
        "properties": {
          "amount_cents": {"type": "integer", "minimum": 1, "maximum": 100000000},
          "spent_on": {
            "type": "string",
            "description": "Date YYYY-MM-DD. Defaults to today in Singapore.",
            "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"
          },
          "category": {
            "type": "string",
            "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"]
          },
          "merchant": {"type": "string", "maxLength": 64},
          "note": {"type": "string", "maxLength": 200}
        },
        "required": ["amount_cents", "category"],
        "additionalProperties": false
      }
    }
  },
  "required": ["items"],
  "additionalProperties": false
}`),
		ReadOnly: false,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input proposeTransactionsInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			if len(input.Items) < 1 || len(input.Items) > finance.MaxProposalItems {
				return nil, argErrorf("items must contain between 1 and 20 transactions")
			}
			items := make([]proposeItem, 0, len(input.Items))
			for _, item := range input.Items {
				if item.SpentOn == "" {
					item.SpentOn = finance.SingaporeToday(now())
				}
				if err := finance.ValidateTransactionInput(finance.TransactionInput{
					AmountCents: item.AmountCents, SpentOn: item.SpentOn, Category: item.Category,
					Merchant: item.Merchant, Note: item.Note,
				}, now()); err != nil {
					return nil, toolValidationError(err)
				}
				items = append(items, item)
			}
			payload, err := json.Marshal(map[string]any{"items": items})
			if err != nil {
				return nil, fmt.Errorf("encode proposal: %w", err)
			}
			return createDraft(ctx, creator, uc, drafts.KindCreateTransactions, payload, nil, nil)
		},
	}
}

func proposeUpdateTransactionTool(service Finance, creator DraftCreator, now func() time.Time) Tool {
	return Tool{
		Name:        "propose_update_transaction",
		Description: proposeUpdateTransactionDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "transaction_id": {"type": "integer", "minimum": 1},
    "changes": {
      "type": "object",
      "description": "Only the fields that change. Null clears merchant or note.",
      "properties": {
        "amount_cents": {"type": "integer", "minimum": 1, "maximum": 100000000},
        "spent_on": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
        "category": {
          "type": "string",
          "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"]
        },
        "merchant": {"type": ["string", "null"], "maxLength": 64},
        "note": {"type": ["string", "null"], "maxLength": 200}
      },
      "additionalProperties": false
    }
  },
  "required": ["transaction_id", "changes"],
  "additionalProperties": false
}`),
		ReadOnly: false,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input proposeUpdateInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			if input.TransactionID <= 0 {
				return nil, argErrorf("transaction_id must be a positive integer")
			}
			current, err := service.GetTransaction(ctx, uc.UserID, input.TransactionID)
			if err != nil {
				if errors.Is(err, finance.ErrNotFound) {
					return nil, argErrorf("transaction %d was not found", input.TransactionID)
				}
				return nil, toolFinanceError(err)
			}
			patch, err := finance.ParseTransactionPatch(input.Changes)
			if err != nil {
				return nil, toolValidationError(err)
			}
			_, changed, err := finance.ApplyTransactionPatch(*current, patch, now())
			if err != nil {
				return nil, toolValidationError(err)
			}
			if !changed {
				return nil, argErrorf("changes must change at least one field")
			}
			payload, err := json.Marshal(map[string]any{
				"transaction_id": input.TransactionID,
				"changes":        json.RawMessage(input.Changes),
			})
			if err != nil {
				return nil, fmt.Errorf("encode proposal: %w", err)
			}
			before, err := json.Marshal(current)
			if err != nil {
				return nil, fmt.Errorf("encode snapshot: %w", err)
			}
			return createDraft(ctx, creator, uc, drafts.KindUpdateTransaction, payload, before, nil)
		},
	}
}

func proposeDeleteTransactionTool(service Finance, creator DraftCreator) Tool {
	return Tool{
		Name:        "propose_delete_transaction",
		Description: proposeDeleteTransactionDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "transaction_id": {"type": "integer", "minimum": 1},
    "reason": {"type": "string", "maxLength": 200}
  },
  "required": ["transaction_id"],
  "additionalProperties": false
}`),
		ReadOnly:    false,
		Destructive: true,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input proposeDeleteInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			if input.TransactionID <= 0 {
				return nil, argErrorf("transaction_id must be a positive integer")
			}
			if utf8.RuneCountInString(input.Reason) > finance.MaxNoteRunes {
				return nil, argErrorf("reason must be at most 200 characters")
			}
			current, err := service.GetTransaction(ctx, uc.UserID, input.TransactionID)
			if err != nil {
				if errors.Is(err, finance.ErrNotFound) {
					return nil, argErrorf("transaction %d was not found", input.TransactionID)
				}
				return nil, toolFinanceError(err)
			}
			payload, err := json.Marshal(map[string]any{
				"transaction_id": input.TransactionID,
				"reason":         input.Reason,
			})
			if err != nil {
				return nil, fmt.Errorf("encode proposal: %w", err)
			}
			before, err := json.Marshal(current)
			if err != nil {
				return nil, fmt.Errorf("encode snapshot: %w", err)
			}
			return createDraft(ctx, creator, uc, drafts.KindDeleteTransaction, payload, before, nil)
		},
	}
}

func proposeUpdateBudgetTool(service Finance, creator DraftCreator) Tool {
	return Tool{
		Name:        "propose_update_budget",
		Description: proposeUpdateBudgetDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "month": {"type": "string", "pattern": "^[0-9]{4}-(0[1-9]|1[0-2])$"},
    "total_cents": {"type": "integer", "minimum": 0},
    "warn_bp": {"type": "integer", "minimum": 5000, "maximum": 10000},
    "categories": {
      "type": "array",
      "description": "Category budgets to change; omitted categories keep their value.",
      "items": {
        "type": "object",
        "properties": {
          "category": {
            "type": "string",
            "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"]
          },
          "budget_cents": {"type": "integer", "minimum": 0}
        },
        "required": ["category", "budget_cents"],
        "additionalProperties": false
      }
    }
  },
  "required": ["month"],
  "additionalProperties": false
}`),
		ReadOnly: false,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input proposeBudgetInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			if err := finance.ValidateBudgetMonth(input.Month); err != nil {
				return nil, argErrorf("month must use YYYY-MM")
			}
			current, err := service.ReadBudget(ctx, uc.UserID, input.Month)
			if err != nil {
				if errors.Is(err, finance.ErrNotFound) {
					return nil, argErrorf("no budget exists for %s yet", input.Month)
				}
				return nil, toolFinanceError(err)
			}
			merged, err := finance.MergeBudgetInput(current, input.BudgetInput)
			if err != nil {
				return nil, toolValidationError(err)
			}
			payload, err := json.Marshal(proposeBudgetPayload{Month: input.Month, BudgetInput: merged})
			if err != nil {
				return nil, fmt.Errorf("encode proposal: %w", err)
			}
			before, err := json.Marshal(current)
			if err != nil {
				return nil, fmt.Errorf("encode snapshot: %w", err)
			}
			return createDraft(ctx, creator, uc, drafts.KindUpdateBudget, payload, before, nil)
		},
	}
}

func createDraft(ctx context.Context, creator DraftCreator, uc UserContext, kind string,
	payload, before json.RawMessage, warnings []string) (any, error) {
	if creator == nil {
		return nil, errors.New("draft creation is unavailable")
	}
	draft, err := creator.Create(ctx, drafts.CreateInput{
		UserID: uc.UserID, SessionID: uc.SessionID, TurnID: uc.TurnID, ToolCallID: uc.ToolCallID,
		Kind: kind, Payload: payload, Before: before, Warnings: warnings,
	})
	if err != nil {
		return nil, fmt.Errorf("create draft: %w", err)
	}
	return proposalResult{
		DraftID: draft.ID, Status: PendingApproval, Kind: draft.Kind,
		Payload: draft.Payload, Before: draft.Before, Warnings: draft.Warnings, ExpiresAt: draft.ExpiresAt,
	}, nil
}

// toolValidationError turns finance field errors into one correctable message
// for the model, with stable field ordering.
func toolValidationError(err error) error {
	var validation *finance.ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	fields := make([]string, 0, len(validation.Fields))
	for field := range validation.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, field+" "+validation.Fields[field])
	}
	return argErrorf("invalid arguments: %s", strings.Join(parts, "; "))
}
