package agent

import "unichatgo/internal/insight"

// Keep the Agent wire contract tied to the finance snapshot types.  Aliases
// make it impossible for the two packages to silently drift apart.
type Snapshot = insight.Snapshot
type CategoryStat = insight.CategoryStat
type Affordability = insight.Affordability
type Category = insight.Category
type Status = insight.Status
type InsightProvider = insight.Provider

const (
	CategoryFood               = insight.CategoryFood
	CategoryTransport          = insight.CategoryTransport
	CategoryHousingTuition     = insight.CategoryHousingTuition
	CategoryDailyShopping      = insight.CategoryDailyShopping
	CategoryStudyDigital       = insight.CategoryStudyDigital
	CategoryEntertainmentOther = insight.CategoryEntertainmentOther
	StatusOK                   = insight.StatusOK
	StatusWarn                 = insight.StatusWarn
	StatusOver                 = insight.StatusOver
)

const Disclaimer = "This is educational budgeting guidance, not professional financial advice."

type Intent string

const (
	IntentOverview      Intent = "overview"
	IntentOverspend     Intent = "overspend"
	IntentAffordability Intent = "affordability"
	IntentTips          Intent = "tips"
	IntentOther         Intent = "other"
)

type Query struct {
	Intent           Intent
	AmountCents      int64
	Category         Category
	AmountFound      bool
	AmountOutOfRange bool
}

type RuleAdvice struct {
	ID           string  `json:"id"`
	Text         string  `json:"text"`
	NumbersCents []int64 `json:"numbers_cents"`
}

type CategoryFact struct {
	Category    Category `json:"category"`
	BudgetCents int64    `json:"budget_cents"`
	SpentCents  int64    `json:"spent_cents"`
	// RemainingCents is budget minus spent; negative when the category is over budget.
	RemainingCents int64 `json:"remaining_cents"`
	UsageBP        *int  `json:"usage_bp"`
}

type Facts struct {
	Month         string         `json:"month"`
	SnapshotHash  string         `json:"snapshot_hash"`
	Numbers       FactsNumbers   `json:"numbers"`
	Categories    []CategoryFact `json:"categories"`
	UsageBP       *int           `json:"usage_bp,omitempty"`
	Affordability *Affordability `json:"affordability,omitempty"`
	RuleAdvice    []RuleAdvice   `json:"rule_advice"`
	Missing       []string       `json:"missing"`
}

type FactsNumbers struct {
	TotalBudgetCents    int64  `json:"total_budget_cents"`
	TotalSpentCents     int64  `json:"total_spent_cents"`
	RemainingCents      int64  `json:"remaining_cents"`
	ProjectedSpentCents *int64 `json:"projected_spent_cents,omitempty"`
}

type HistoryMessage struct {
	ID        int64       `json:"id"`
	Role      string      `json:"role"`
	Content   string      `json:"content"`
	Verified  *bool       `json:"verified,omitempty"`
	Fallback  *bool       `json:"fallback,omitempty"`
	CreatedAt interface{} `json:"created_at"`
}
