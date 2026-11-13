// Package insight defines the shared budgeting contract for finance and Agent.
// The contract is maintained in docs/api/contract.md, section 5 (GO-INSIGHT-01).
package insight

import (
	"context"
	"time"
)

// Category identifies a spending category from contract section 2.5.
type Category string

// Categories are listed in the order used by Snapshot.Categories.
const (
	CategoryFood               Category = "food"
	CategoryTransport          Category = "transport"
	CategoryHousingTuition     Category = "housing_tuition"
	CategoryDailyShopping      Category = "daily_shopping"
	CategoryStudyDigital       Category = "study_digital"
	CategoryEntertainmentOther Category = "entertainment_other"
)

// Status describes a category's budget usage.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusOver Status = "over"
)

// CategoryStat contains the budget and spending totals for one category.
type CategoryStat struct {
	Category    Category `json:"category"`
	BudgetCents int64    `json:"budget_cents"`
	SpentCents  int64    `json:"spent_cents"`
	UsageBP     *int     `json:"usage_bp"` // Nil when the budget is zero.
	Status      Status   `json:"status"`
}

// Snapshot is the shared source of computed figures for dashboards and Agent.
// Amounts are SGD cents; ratios are integer basis points.
type Snapshot struct {
	Month               string         `json:"month"` // YYYY-MM.
	AsOf                time.Time      `json:"as_of"` // Asia/Singapore.
	DaysElapsed         int            `json:"days_elapsed"`
	DaysInMonth         int            `json:"days_in_month"`
	TotalBudgetCents    int64          `json:"total_budget_cents"`
	TotalSpentCents     int64          `json:"total_spent_cents"` // Sum of category SpentCents.
	RemainingCents      int64          `json:"remaining_cents"`   // May be negative.
	UsageBP             *int           `json:"usage_bp"`
	ProjectedSpentCents *int64         `json:"projected_spent_cents"` // Nil when data is insufficient.
	Categories          []CategoryStat `json:"categories"`            // Exactly six, in contract section 2.5 order.
	TopCategory         *Category      `json:"top_category"`          // Nil when there is no spending.
	TransactionCount    int            `json:"transaction_count"`
	Missing             []string       `json:"missing"` // "profile", "budget", or "transactions".
	Hash                string         `json:"hash"`    // SHA-256 of the above content, for auditing.
}

// Affordability describes the effect of a proposed purchase on a snapshot.
type Affordability struct {
	AmountCents          int64    `json:"amount_cents"`
	Category             Category `json:"category"`
	RemainingAfterCents  int64    `json:"remaining_after_cents"`
	CategoryUsageAfterBP *int     `json:"category_usage_after_bp"`
	ExceedsTotal         bool     `json:"exceeds_total"`
	ExceedsCategory      bool     `json:"exceeds_category"`
}

// Provider is implemented by finance; Agent reads its results without
// recalculating amounts or ratios.
type Provider interface {
	Snapshot(ctx context.Context, userID int64, month string) (*Snapshot, error)
	Affordability(s *Snapshot, amountCents int64, category Category) Affordability
}
