package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

const listTransactionsDescription = `List the user's recorded transactions, newest first. Defaults to the current ` +
	`Singapore month. Use this when the user asks what they bought, which merchant they paid or how many ` +
	`transactions a category has. Amounts are in SGD cents.`

type transactionSummary struct {
	ID          int64   `json:"id"`
	SpentOn     string  `json:"spent_on"`
	AmountCents int64   `json:"amount_cents"`
	Category    string  `json:"category"`
	Merchant    *string `json:"merchant"`
	Note        *string `json:"note"`
	Source      string  `json:"source"`
	DraftID     *string `json:"draft_id,omitempty"`
}

type listTransactionsResult struct {
	Month        string               `json:"month"`
	From         string               `json:"from,omitempty"`
	To           string               `json:"to,omitempty"`
	Transactions []transactionSummary `json:"transactions"`
	Count        int                  `json:"count"`
	TotalInMonth int64                `json:"total_in_month"`
}

type listTransactionsInput struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Category string `json:"category"`
	Query    string `json:"query"`
	Limit    *int   `json:"limit"`
}

func listTransactionsTool(service Finance, now func() time.Time) Tool {
	return Tool{
		Name:        "list_transactions",
		Description: listTransactionsDescription,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from": {
      "type": "string",
      "description": "Inclusive start date, YYYY-MM-DD. Must fall in the same month as to.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"
    },
    "to": {
      "type": "string",
      "description": "Inclusive end date, YYYY-MM-DD. Must fall in the same month as from.",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"
    },
    "category": {
      "type": "string",
      "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"]
    },
    "query": {
      "type": "string",
      "description": "Case-insensitive text matched against merchant and note.",
      "maxLength": 100
    },
    "limit": {
      "type": "integer",
      "description": "Maximum number of transactions to return. Defaults to 20.",
      "minimum": 1,
      "maximum": 50,
      "default": 20
    }
  },
  "additionalProperties": false
}`),
		ReadOnly: true,
		Handler: func(ctx context.Context, uc UserContext, args json.RawMessage) (any, error) {
			var input listTransactionsInput
			if err := decodeArgs(args, &input); err != nil {
				return nil, err
			}
			limit := 20
			if input.Limit != nil {
				limit = *input.Limit
			}
			if limit < 1 || limit > 50 {
				return nil, argErrorf("limit must be between 1 and 50")
			}
			if utf8.RuneCountInString(input.Query) > 100 {
				return nil, argErrorf("query must be at most 100 characters")
			}
			if input.Category != "" && !knownCategory(input.Category) {
				return nil, argErrorf("category must be one of the six supported categories")
			}
			from, to, month, err := resolveDateRange(input.From, input.To, now())
			if err != nil {
				return nil, err
			}
			options := finance.TransactionListOptions{Month: month, Page: 1, PageSize: 100}
			if input.Category != "" {
				options.Category = insight.Category(input.Category)
			}
			page, err := service.ListTransactions(ctx, uc.UserID, options)
			if err != nil {
				return nil, toolFinanceError(err)
			}
			result := listTransactionsResult{
				Month:        month,
				From:         from,
				To:           to,
				Transactions: make([]transactionSummary, 0, limit),
				TotalInMonth: page.Total,
			}
			query := strings.ToLower(strings.TrimSpace(input.Query))
			for _, item := range page.Items {
				if from != "" && item.SpentOn < from {
					continue
				}
				if to != "" && item.SpentOn > to {
					continue
				}
				if query != "" && !matchesQuery(item, query) {
					continue
				}
				result.Transactions = append(result.Transactions, transactionSummary{
					ID: item.ID, SpentOn: item.SpentOn, AmountCents: item.AmountCents,
					Category: string(item.Category), Merchant: item.Merchant, Note: item.Note,
					Source: item.Source, DraftID: item.DraftID,
				})
				if len(result.Transactions) == limit {
					break
				}
			}
			result.Count = len(result.Transactions)
			return result, nil
		},
	}
}

// resolveDateRange validates the optional date filters. finance lists one
// month at a time, so a range spanning two months is rejected explicitly
// instead of silently returning the wrong half.
func resolveDateRange(from, to string, now time.Time) (string, string, string, error) {
	if from == "" && to == "" {
		return "", "", now.In(singapore()).Format("2006-01"), nil
	}
	for _, value := range []string{from, to} {
		if value == "" {
			continue
		}
		if _, err := time.ParseInLocation("2006-01-02", value, singapore()); err != nil {
			return "", "", "", argErrorf("dates must use YYYY-MM-DD")
		}
	}
	if from != "" && to != "" {
		if from > to {
			return "", "", "", argErrorf("from must not be after to")
		}
		if from[:7] != to[:7] {
			return "", "", "", argErrorf("from and to must fall in the same month")
		}
	}
	month := from
	if month == "" {
		month = to
	}
	return from, to, month[:7], nil
}

func matchesQuery(item finance.Transaction, query string) bool {
	if item.Merchant != nil && strings.Contains(strings.ToLower(*item.Merchant), query) {
		return true
	}
	return item.Note != nil && strings.Contains(strings.ToLower(*item.Note), query)
}
