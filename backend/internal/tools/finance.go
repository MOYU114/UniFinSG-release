package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
)

// Finance is the read surface the tools need. *finance.Service implements it.
type Finance interface {
	// SnapshotReadOnly must not create or modify any row.
	SnapshotReadOnly(ctx context.Context, userID int64, month string) (*insight.Snapshot, error)
	Affordability(s *insight.Snapshot, amountCents int64, category insight.Category) insight.Affordability
	ListTransactions(ctx context.Context, userID int64, options finance.TransactionListOptions) (*finance.TransactionPage, error)
	GetTransaction(ctx context.Context, userID, transactionID int64) (*finance.Transaction, error)
	// ReadBudget must not create the month's budget.
	ReadBudget(ctx context.Context, userID int64, month string) (*finance.Budget, error)
}

// CategoryInfo is one entry of the six-category contract.
type CategoryInfo struct {
	Category string `json:"category"`
	LabelEN  string `json:"label_en"`
	LabelZH  string `json:"label_zh"`
}

// Categories is the fixed six-category list, in contract order.
var Categories = []CategoryInfo{
	{Category: string(insight.CategoryFood), LabelEN: "Food and drinks", LabelZH: "餐饮"},
	{Category: string(insight.CategoryTransport), LabelEN: "Transport", LabelZH: "交通"},
	{Category: string(insight.CategoryHousingTuition), LabelEN: "Housing and tuition", LabelZH: "住房与学费"},
	{Category: string(insight.CategoryDailyShopping), LabelEN: "Daily shopping", LabelZH: "日常购物"},
	{Category: string(insight.CategoryStudyDigital), LabelEN: "Study and digital", LabelZH: "学习与数码"},
	{Category: string(insight.CategoryEntertainmentOther), LabelEN: "Entertainment and other", LabelZH: "娱乐与其他"},
}

var categoryNames = func() []string {
	names := make([]string, 0, len(Categories))
	for _, category := range Categories {
		names = append(names, category.Category)
	}
	return names
}()

func knownCategory(value string) bool {
	for _, name := range categoryNames {
		if name == value {
			return true
		}
	}
	return false
}

// decodeArgs rejects unknown fields so a misspelled argument surfaces as a
// correctable tool error instead of being silently ignored.
func decodeArgs(raw json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		trimmed = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return argErrorf("invalid arguments: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return argErrorf("invalid arguments: exactly one JSON object is required")
	}
	return nil
}

func monthOrDefault(month string, now time.Time) string {
	if month == "" {
		return now.In(singapore()).Format("2006-01")
	}
	return month
}

var sgt = func() *time.Location {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		return time.FixedZone("Asia/Singapore", 8*60*60)
	}
	return location
}()

func singapore() *time.Location { return sgt }

// NewReadOnlyRegistry registers the three read-only tools of phase P2.
func NewReadOnlyRegistry(service Finance, now func() time.Time) (*Registry, error) {
	if service == nil {
		return nil, errors.New("tools: finance service is required")
	}
	if now == nil {
		now = time.Now
	}
	registry := NewRegistry()
	for _, tool := range []Tool{
		listCategoriesTool(),
		budgetSnapshotTool(service, now),
		listTransactionsTool(service, now),
	} {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewAgentRegistry registers all seven tools of design section 7.2: the three
// read-only tools plus the four propose_* draft tools.
func NewAgentRegistry(service Finance, creator DraftCreator, now func() time.Time) (*Registry, error) {
	registry, err := NewReadOnlyRegistry(service, now)
	if err != nil {
		return nil, err
	}
	if creator == nil {
		return nil, errors.New("tools: draft creator is required")
	}
	if now == nil {
		now = time.Now
	}
	for _, tool := range []Tool{
		proposeTransactionsTool(creator, now),
		proposeUpdateTransactionTool(service, creator, now),
		proposeDeleteTransactionTool(service, creator),
		proposeUpdateBudgetTool(service, creator),
	} {
		if err := registry.Register(tool); err != nil {
			return nil, err
		}
	}
	return registry, nil
}
