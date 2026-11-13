package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

const budgetSnapshotDescription = `Read the user's budget snapshot for one month: total budget, spent, remaining, ` +
	`per-category budget and spending, projected spending and the missing-data flags. ` +
	`Optionally check whether a planned purchase still fits the budget. ` +
	`Always call this instead of guessing or recalling amounts.`

// CategorySnapshot is one category row of the tool result.
type CategorySnapshot struct {
	Category       string `json:"category"`
	BudgetCents    int64  `json:"budget_cents"`
	SpentCents     int64  `json:"spent_cents"`
	RemainingCents int64  `json:"remaining_cents"`
	UsageBP        *int   `json:"usage_bp"`
	Status         string `json:"status"`
}

// BudgetSnapshotResult is the tool result for get_budget_snapshot.
type BudgetSnapshotResult struct {
	Month               string                 `json:"month"`
	AsOf                string                 `json:"as_of"`
	TotalBudgetCents    int64                  `json:"total_budget_cents"`
	TotalSpentCents     int64                  `json:"total_spent_cents"`
	RemainingCents      int64                  `json:"remaining_cents"`
	UsageBP             *int                   `json:"usage_bp"`
	ProjectedSpentCents *int64                 `json:"projected_spent_cents"`
	Categories          []CategorySnapshot     `json:"categories"`
	Affordability       *insight.Affordability `json:"affordability,omitempty"`
	Missing             []string               `json:"missing"`
}

type budgetSnapshotInput struct {
	Month         string `json:"month"`
	Affordability *struct {
		AmountCents int64  `json:"amount_cents"`
		Category    string `json:"category"`
	} `json:"affordability"`
}

func budgetSnapshotTool(service Finance, now func() time.Time) Tool {
	return Tool{
		Name:        "get_budget_snapshot",
		Description: budgetSnapshotDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "month": {
      "type": "string",
      "description": "Month in YYYY-MM. Defaults to the current Singapore month.",
      "pattern": "^[0-9]{4}-(0[1-9]|1[0-2])$"
    },
    "affordability": {
      "type": "object",
      "description": "Optional purchase check against this month's budget.",
      "properties": {
        "amount_cents": {"type": "integer", "minimum": 1, "maximum": 100000000},
        "category": {
          "type": "string",
          "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"]
        }
      },
      "required": ["amount_cents", "category"],
      "additionalProperties": false
    }
  },
  "additionalProperties": false
}`),
		ReadOnly: true,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input budgetSnapshotInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			if input.Affordability != nil {
				if input.Affordability.AmountCents < 1 || input.Affordability.AmountCents > 100000000 {
					return nil, argErrorf("affordability.amount_cents must be between 1 and 100000000")
				}
			}
			snapshot, err := service.SnapshotReadOnly(ctx, uc.UserID, monthOrDefault(input.Month, now()))
			if err != nil {
				return nil, toolFinanceError(err)
			}
			result := BudgetSnapshotResult{
				Month:               snapshot.Month,
				AsOf:                snapshot.AsOf.In(singapore()).Format(time.RFC3339),
				TotalBudgetCents:    snapshot.TotalBudgetCents,
				TotalSpentCents:     snapshot.TotalSpentCents,
				RemainingCents:      snapshot.RemainingCents,
				UsageBP:             snapshot.UsageBP,
				ProjectedSpentCents: snapshot.ProjectedSpentCents,
				Missing:             append([]string{}, snapshot.Missing...),
				Categories:          make([]CategorySnapshot, 0, len(snapshot.Categories)),
			}
			for _, stat := range snapshot.Categories {
				result.Categories = append(result.Categories, CategorySnapshot{
					Category:       string(stat.Category),
					BudgetCents:    stat.BudgetCents,
					SpentCents:     stat.SpentCents,
					RemainingCents: stat.BudgetCents - stat.SpentCents,
					UsageBP:        stat.UsageBP,
					Status:         string(stat.Status),
				})
			}
			if input.Affordability != nil {
				category := insight.Category(input.Affordability.Category)
				if !knownCategory(string(category)) {
					// Design section 7.2: an unsupported category falls back to
					// the catch-all bucket instead of failing the tool call.
					category = insight.CategoryEntertainmentOther
				}
				value, ok := safeAffordability(service, snapshot, input.Affordability.AmountCents, category)
				if !ok {
					return nil, argErrorf("affordability could not be computed for this amount")
				}
				result.Affordability = &value
			}
			return result, nil
		},
	}
}

func safeAffordability(service Finance, snapshot *insight.Snapshot, amount int64, category insight.Category) (result insight.Affordability, ok bool) {
	defer func() {
		if recover() != nil {
			result = insight.Affordability{}
			ok = false
		}
	}()
	return service.Affordability(snapshot, amount, category), true
}

func toolFinanceError(err error) error {
	switch {
	case errors.Is(err, finance.ErrInvalidMonth):
		return argErrorf("month must use YYYY-MM")
	case errors.Is(err, finance.ErrNotFound):
		return argErrorf("no financial data exists for this account yet")
	default:
		return fmt.Errorf("read finance data: %w", err)
	}
}
