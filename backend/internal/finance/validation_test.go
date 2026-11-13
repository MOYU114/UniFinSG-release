package finance

import (
	"errors"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"unichatgo/internal/insight"
)

func TestValidateMonth(t *testing.T) {
	for _, month := range []string{"0001-01", "2026-02", "2026-12", "9999-12"} {
		t.Run("valid_"+month, func(t *testing.T) {
			if err := validateMonth(month); err != nil {
				t.Fatalf("valid month %q rejected: %v", month, err)
			}
		})
	}
	for _, month := range []string{
		"", "2026", "2026-1", "26-01", "02026-01", "0000-01",
		"2026-00", "2026-13", "2026-01-01", "2026/01",
		" 2026-01", "2026-01 ", "２０２６-01", "+026-01",
	} {
		t.Run("invalid_"+month, func(t *testing.T) {
			if err := validateMonth(month); !errors.Is(err, ErrInvalidMonth) {
				t.Fatalf("invalid month %q: got %v, want ErrInvalidMonth", month, err)
			}
		})
	}
}

func TestNormalizeProfileDefaultsAndExplicitZero(t *testing.T) {
	tests := []struct {
		name     string
		input    ProfileInput
		expected int64
	}{
		{"omitted_default", ProfileInput{MonthlyDisposableCents: validationInt64(120000), Housing: "on_campus"}, 120000},
		{"explicit_zero", ProfileInput{MonthlyDisposableCents: validationInt64(120000), Housing: "off_campus", DefaultBudgetCents: validationInt64(0)}, 0},
		{"independent_default", ProfileInput{MonthlyDisposableCents: validationInt64(100), Housing: "with_family", DefaultBudgetCents: validationInt64(200)}, 200},
		{"no_undocumented_maximum", ProfileInput{MonthlyDisposableCents: validationInt64(math.MaxInt64), Housing: "on_campus", DefaultBudgetCents: validationInt64(math.MaxInt64)}, math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile, err := normalizeProfile(test.input)
			if err != nil {
				t.Fatalf("normalize profile: %v", err)
			}
			if profile.MonthlyDisposableCents != *test.input.MonthlyDisposableCents ||
				profile.Housing != test.input.Housing || profile.DefaultBudgetCents != test.expected {
				t.Fatalf("unexpected normalized profile: %+v", profile)
			}
		})
	}
}

func TestNormalizeProfileRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name  string
		input ProfileInput
		field string
	}{
		{"missing_disposable", ProfileInput{Housing: "on_campus"}, "monthly_disposable_cents"},
		{"zero_disposable", ProfileInput{MonthlyDisposableCents: validationInt64(0), Housing: "on_campus"}, "monthly_disposable_cents"},
		{"negative_disposable", ProfileInput{MonthlyDisposableCents: validationInt64(-1), Housing: "on_campus"}, "monthly_disposable_cents"},
		{"missing_housing", ProfileInput{MonthlyDisposableCents: validationInt64(1)}, "housing"},
		{"unknown_housing", ProfileInput{MonthlyDisposableCents: validationInt64(1), Housing: "other"}, "housing"},
		{"housing_case", ProfileInput{MonthlyDisposableCents: validationInt64(1), Housing: "ON_CAMPUS"}, "housing"},
		{"negative_default", ProfileInput{MonthlyDisposableCents: validationInt64(1), Housing: "on_campus", DefaultBudgetCents: validationInt64(-1)}, "default_budget_cents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeProfile(test.input)
			assertValidationField(t, err, test.field)
		})
	}
}

func TestNormalizeBudgetPreservesUnallocatedAmount(t *testing.T) {
	tests := []struct {
		name        string
		total       int64
		food        int64
		warn        *int
		expectedBP  int
		unallocated int64
	}{
		{"zero_budget", 0, 0, nil, 8000, 0},
		{"unallocated_positive", 100, 40, nil, 8000, 60},
		{"unallocated_negative", 10, 11, validationInt(5000), 5000, -1},
		{"upper_warn_boundary", 10, 10, validationInt(10000), 10000, 0},
		{"maximum_total", math.MaxInt64, 0, nil, 8000, math.MaxInt64},
		{"maximum_category", 0, math.MaxInt64, nil, 8000, -math.MaxInt64},
		{"maximum_balanced", math.MaxInt64, math.MaxInt64, nil, 8000, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validationBudgetInput(test.total)
			input.Categories[0].BudgetCents = validationInt64(test.food)
			input.WarnBP = test.warn
			budget, err := normalizeBudget(input)
			if err != nil {
				t.Fatalf("normalize budget: %v", err)
			}
			if budget.TotalCents != test.total || budget.WarnBP != test.expectedBP ||
				budget.CategorySumCents != test.food || budget.UnallocatedCents != test.unallocated {
				t.Fatalf("unexpected normalized budget: %+v", budget)
			}
		})
	}
}

func TestNormalizeBudgetOrdersCategories(t *testing.T) {
	input := validationBudgetInput(210)
	for i := range input.Categories {
		input.Categories[i].BudgetCents = validationInt64(int64((i + 1) * 10))
	}
	for left, right := 0, len(input.Categories)-1; left < right; left, right = left+1, right-1 {
		input.Categories[left], input.Categories[right] = input.Categories[right], input.Categories[left]
	}
	budget, err := normalizeBudget(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(budget.Categories) != len(validationCategoryOrder) {
		t.Fatalf("expected six categories, got %d", len(budget.Categories))
	}
	for i, category := range budget.Categories {
		if category.Category != validationCategoryOrder[i] || category.BudgetCents != int64((i+1)*10) {
			t.Fatalf("category %d lost its amount or contract order: %+v", i, category)
		}
	}
	if budget.CategorySumCents != 210 || budget.UnallocatedCents != 0 {
		t.Fatalf("incorrect category total: %+v", budget)
	}
}

func TestNormalizeBudgetRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BudgetInput)
		field  string
	}{
		{"missing_total", func(in *BudgetInput) { in.TotalCents = nil }, "total_cents"},
		{"negative_total", func(in *BudgetInput) { in.TotalCents = validationInt64(-1) }, "total_cents"},
		{"low_warn", func(in *BudgetInput) { in.WarnBP = validationInt(4999) }, "warn_bp"},
		{"high_warn", func(in *BudgetInput) { in.WarnBP = validationInt(10001) }, "warn_bp"},
		{"no_categories", func(in *BudgetInput) { in.Categories = nil }, "categories"},
		{"missing_category", func(in *BudgetInput) { in.Categories = in.Categories[:5] }, "categories"},
		{"extra_category", func(in *BudgetInput) { in.Categories = append(in.Categories, in.Categories[0]) }, "categories"},
		{"duplicate_category", func(in *BudgetInput) { in.Categories[1] = in.Categories[0] }, "categories"},
		{"unknown_category", func(in *BudgetInput) { in.Categories[0].Category = insight.Category("unknown") }, "categories"},
		{"missing_amount", func(in *BudgetInput) { in.Categories[0].BudgetCents = nil }, "categories"},
		{"negative_amount", func(in *BudgetInput) { in.Categories[0].BudgetCents = validationInt64(-1) }, "categories"},
		{"sum_overflow", func(in *BudgetInput) {
			in.Categories[0].BudgetCents = validationInt64(math.MaxInt64)
			in.Categories[1].BudgetCents = validationInt64(1)
		}, "categories"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validationBudgetInput(100)
			test.mutate(&input)
			_, err := normalizeBudget(input)
			assertValidationField(t, err, test.field)
		})
	}
}

func TestAllocateDefaultBudgetRemainders(t *testing.T) {
	tests := []struct {
		total    int64
		expected []int64
	}{
		{0, []int64{0, 0, 0, 0, 0, 0}},
		{1, []int64{1, 0, 0, 0, 0, 0}},
		{2, []int64{1, 1, 0, 0, 0, 0}},
		{4, []int64{2, 1, 0, 1, 0, 0}},
		{7, []int64{3, 1, 0, 1, 1, 1}},
		{11, []int64{5, 2, 0, 1, 1, 2}},
		{100000, []int64{40000, 10000, 0, 15000, 15000, 20000}},
		{100001, []int64{40001, 10000, 0, 15000, 15000, 20000}},
	}
	for _, test := range tests {
		categories := allocateDefaultBudget(test.total)
		amounts := make([]int64, 0, len(categories))
		for _, category := range categories {
			amounts = append(amounts, category.BudgetCents)
		}
		if !reflect.DeepEqual(amounts, test.expected) {
			t.Errorf("total %d: amounts %v, want %v", test.total, amounts, test.expected)
		}
		assertDefaultBudgetInvariant(t, test.total, categories)
	}
}

func TestAllocateDefaultBudgetPreservesMaximumInteger(t *testing.T) {
	for _, total := range []int64{math.MaxInt64, math.MaxInt64 - 1, math.MaxInt64 - 99} {
		assertDefaultBudgetInvariant(t, total, allocateDefaultBudget(total))
	}
}

func assertDefaultBudgetInvariant(t *testing.T, total int64, categories []CategoryBudget) {
	t.Helper()
	if len(categories) != len(validationCategoryOrder) {
		t.Fatalf("total %d: expected six categories, got %d", total, len(categories))
	}
	sum := new(big.Int)
	for i, category := range categories {
		if category.Category != validationCategoryOrder[i] {
			t.Fatalf("total %d: category %d out of order: %s", total, i, category.Category)
		}
		if category.BudgetCents < 0 {
			t.Fatalf("total %d: negative allocation %+v", total, category)
		}
		if category.Category == insight.CategoryHousingTuition && category.BudgetCents != 0 {
			t.Fatalf("total %d: zero-weight category received an allocation", total)
		}
		sum.Add(sum, big.NewInt(category.BudgetCents))
	}
	if sum.Cmp(big.NewInt(total)) != 0 {
		t.Fatalf("total %d: allocated total is %s", total, sum)
	}
}

var validationCategoryOrder = []insight.Category{
	insight.CategoryFood,
	insight.CategoryTransport,
	insight.CategoryHousingTuition,
	insight.CategoryDailyShopping,
	insight.CategoryStudyDigital,
	insight.CategoryEntertainmentOther,
}

func validationBudgetInput(total int64) BudgetInput {
	input := BudgetInput{TotalCents: validationInt64(total)}
	for _, category := range validationCategoryOrder {
		input.Categories = append(input.Categories, CategoryBudgetInput{
			Category: category, BudgetCents: validationInt64(0),
		})
	}
	return input
}

func assertValidationField(t *testing.T, err error, field string) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected ValidationError for %s, got %v", field, err)
	}
	for key, value := range validation.Fields {
		if (key == field || strings.HasPrefix(key, field+".") || strings.HasPrefix(key, field+"[")) && value != "" {
			return
		}
	}
	t.Fatalf("expected nonempty validation detail for %s, got %#v", field, validation.Fields)
}

func validationInt64(value int64) *int64 { return &value }
func validationInt(value int) *int       { return &value }
