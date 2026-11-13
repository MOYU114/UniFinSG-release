// Package finance stores and serves the authenticated user's financial data.
package finance

import (
	"errors"
	"math"
	"regexp"
	"time"

	"unichatgo/internal/insight"
)

var (
	ErrNotFound         = errors.New("finance resource not found")
	ErrProfileRequired  = errors.New("a profile is required to generate a budget")
	ErrInvalidMonth     = errors.New("month must use YYYY-MM")
	ErrInconsistentData = errors.New("stored finance data is inconsistent")
	monthPattern        = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	categoryOrder       = [...]insight.Category{
		insight.CategoryFood, insight.CategoryTransport,
		insight.CategoryHousingTuition, insight.CategoryDailyShopping,
		insight.CategoryStudyDigital, insight.CategoryEntertainmentOther,
	}
)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "finance fields are invalid" }

type ProfileInput struct {
	MonthlyDisposableCents *int64 `json:"monthly_disposable_cents"`
	Housing                string `json:"housing"`
	DefaultBudgetCents     *int64 `json:"default_budget_cents"`
}

type Profile struct {
	MonthlyDisposableCents int64     `json:"monthly_disposable_cents"`
	Housing                string    `json:"housing"`
	DefaultBudgetCents     int64     `json:"default_budget_cents"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type CategoryBudgetInput struct {
	Category    insight.Category `json:"category"`
	BudgetCents *int64           `json:"budget_cents"`
}

type CategoryBudget struct {
	Category    insight.Category `json:"category"`
	BudgetCents int64            `json:"budget_cents"`
}

type BudgetInput struct {
	TotalCents *int64                `json:"total_cents"`
	WarnBP     *int                  `json:"warn_bp"`
	Categories []CategoryBudgetInput `json:"categories"`
}

type Budget struct {
	Month            string           `json:"month"`
	TotalCents       int64            `json:"total_cents"`
	WarnBP           int              `json:"warn_bp"`
	Categories       []CategoryBudget `json:"categories"`
	CategorySumCents int64            `json:"category_sum_cents"`
	UnallocatedCents int64            `json:"unallocated_cents"`
	Generated        bool             `json:"generated"`
	Source           string           `json:"source"`
}

func validateMonth(month string) error {
	if !monthPattern.MatchString(month) || month[:4] == "0000" {
		return ErrInvalidMonth
	}
	return nil
}

func normalizeProfile(input ProfileInput) (Profile, error) {
	fields := make(map[string]string)
	profile := Profile{Housing: input.Housing}
	if input.MonthlyDisposableCents == nil || *input.MonthlyDisposableCents <= 0 {
		fields["monthly_disposable_cents"] = "must_be_positive"
	} else {
		profile.MonthlyDisposableCents = *input.MonthlyDisposableCents
	}
	switch input.Housing {
	case "on_campus", "off_campus", "with_family":
	default:
		fields["housing"] = "must_be_a_supported_housing_type"
	}
	profile.DefaultBudgetCents = profile.MonthlyDisposableCents
	if input.DefaultBudgetCents != nil {
		profile.DefaultBudgetCents = *input.DefaultBudgetCents
		if profile.DefaultBudgetCents < 0 {
			fields["default_budget_cents"] = "must_be_nonnegative"
		}
	}
	if len(fields) > 0 {
		return Profile{}, &ValidationError{Fields: fields}
	}
	return profile, nil
}

func normalizeBudget(input BudgetInput) (Budget, error) {
	fields := make(map[string]string)
	budget := Budget{WarnBP: 8000, Source: "saved"}
	if input.TotalCents == nil || *input.TotalCents < 0 {
		fields["total_cents"] = "must_be_nonnegative"
	} else {
		budget.TotalCents = *input.TotalCents
	}
	if input.WarnBP != nil {
		budget.WarnBP = *input.WarnBP
		if budget.WarnBP < 5000 || budget.WarnBP > 10000 {
			fields["warn_bp"] = "must_be_between_5000_and_10000"
		}
	}
	values := make(map[insight.Category]int64, len(categoryOrder))
	for _, category := range input.Categories {
		if !knownCategory(category.Category) {
			fields["categories"] = "must_contain_each_supported_category_once"
			continue
		}
		if _, exists := values[category.Category]; exists {
			fields["categories"] = "must_contain_each_supported_category_once"
		}
		if category.BudgetCents == nil || *category.BudgetCents < 0 {
			fields["categories"] = "category_budgets_must_be_nonnegative"
			values[category.Category] = 0
		} else {
			values[category.Category] = *category.BudgetCents
		}
	}
	if len(input.Categories) != len(categoryOrder) || len(values) != len(categoryOrder) {
		fields["categories"] = "must_contain_each_supported_category_once"
	}
	for _, category := range categoryOrder {
		amount := values[category]
		budget.Categories = append(budget.Categories, CategoryBudget{Category: category, BudgetCents: amount})
		if amount > math.MaxInt64-budget.CategorySumCents {
			fields["categories"] = "category_sum_exceeds_integer_range"
		} else {
			budget.CategorySumCents += amount
		}
	}
	if len(fields) > 0 {
		return Budget{}, &ValidationError{Fields: fields}
	}
	// Both operands are nonnegative int64 values, so this may be negative
	// without overflowing. Unallocated funds are deliberately not clamped.
	budget.UnallocatedCents = budget.TotalCents - budget.CategorySumCents
	return budget, nil
}

func knownCategory(category insight.Category) bool {
	for _, candidate := range categoryOrder {
		if category == candidate {
			return true
		}
	}
	return false
}

// ValidateBudgetInput applies the shared budget rules (total, warn_bp and the
// complete six-category allocation).
func ValidateBudgetInput(input BudgetInput) error {
	_, err := normalizeBudget(input)
	return err
}

// ValidateBudgetMonth checks the YYYY-MM format.
func ValidateBudgetMonth(month string) error {
	return validateMonth(month)
}

// MergeBudgetInput overlays a partial budget patch on the current record and
// returns the complete input. The agent tool and the decision endpoint both
// use it so an edited payload cannot bypass the category contract.
func MergeBudgetInput(current *Budget, patch BudgetInput) (BudgetInput, error) {
	fields := make(map[string]string)
	total := int64(0)
	hasTotal := false
	warn := 8000
	values := make(map[insight.Category]int64, len(categoryOrder))
	if current != nil {
		total = current.TotalCents
		hasTotal = true
		warn = current.WarnBP
		for _, category := range current.Categories {
			values[category.Category] = category.BudgetCents
		}
	}
	if patch.TotalCents != nil {
		total = *patch.TotalCents
		hasTotal = true
	}
	if patch.WarnBP != nil {
		warn = *patch.WarnBP
	}
	seen := make(map[insight.Category]bool, len(patch.Categories))
	for _, category := range patch.Categories {
		if !knownCategory(category.Category) {
			fields["categories"] = "must_contain_supported_categories"
			continue
		}
		if seen[category.Category] {
			fields["categories"] = "must_contain_each_supported_category_once"
			continue
		}
		seen[category.Category] = true
		if category.BudgetCents == nil || *category.BudgetCents < 0 {
			fields["categories"] = "category_budgets_must_be_nonnegative"
			continue
		}
		values[category.Category] = *category.BudgetCents
	}
	if !hasTotal {
		fields["total_cents"] = "must_be_nonnegative"
	}
	if len(fields) > 0 {
		return BudgetInput{}, &ValidationError{Fields: fields}
	}
	merged := BudgetInput{TotalCents: &total, WarnBP: &warn}
	for _, category := range categoryOrder {
		amount := values[category]
		merged.Categories = append(merged.Categories, CategoryBudgetInput{
			Category: category, BudgetCents: &amount,
		})
	}
	if err := ValidateBudgetInput(merged); err != nil {
		return BudgetInput{}, err
	}
	return merged, nil
}

func allocateDefaultBudget(total int64) []CategoryBudget {
	weights := [...]int64{40, 10, 0, 15, 15, 20}
	categories := make([]CategoryBudget, len(categoryOrder))
	var allocated int64
	for i, category := range categoryOrder {
		// Divide before multiplying to support the full nonnegative int64
		// range, including budgets whose multiplication by 100 would overflow.
		amount := (total/100)*weights[i] + (total%100)*weights[i]/100
		categories[i] = CategoryBudget{Category: category, BudgetCents: amount}
		allocated += amount
	}
	for i, remainder := 0, total-allocated; remainder > 0; i++ {
		if weights[i] == 0 {
			continue
		}
		categories[i].BudgetCents++
		remainder--
	}
	return categories
}
