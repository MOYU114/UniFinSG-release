package finance

import (
	"math"
	"reflect"
	"testing"

	"unichatgo/internal/insight"
)

func TestAffordability(t *testing.T) {
	tests := []struct {
		name                                  string
		total, spent, categoryBudget, amount  int64
		wantRemaining                         int64
		wantUsage                             *int
		wantExceedsTotal, wantExceedsCategory bool
	}{
		{"normal exact integer ratio", 100000, 46500, 15000, 8000, 45500, snapshotTestInt(36333), false, true},
		{"exactly total and category", 10000, 8000, 10000, 2000, 0, snapshotTestInt(10000), false, false},
		{"over total", 10000, 8000, 20000, 2001, -1, snapshotTestInt(5000), true, false},
		{"over category", 10000, 8000, 8000, 1, 1999, snapshotTestInt(10001), false, true},
		{"zero category budget", 10000, 0, 0, 1, 9999, nil, false, true},
		{"negative remaining", 100, 200, 100, 25, -125, snapshotTestInt(22500), true, true},
		{"zero total budget", 0, 0, 100, 1, -1, snapshotTestInt(100), true, false},
		{"largest integers and intermediate sum", math.MaxInt64, math.MaxInt64, math.MaxInt64, 1, -1, snapshotTestInt(10000), true, true},
		{"smallest remaining is representable", 0, math.MaxInt64, 0, 1, math.MinInt64, nil, true, true},
	}
	service := NewService(nil)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := affordabilityTestSnapshot(test.total, test.spent, test.categoryBudget)
			before := *snapshot
			got := service.Affordability(snapshot, test.amount, insight.CategoryFood)
			want := insight.Affordability{
				AmountCents: test.amount, Category: insight.CategoryFood, RemainingAfterCents: test.wantRemaining,
				CategoryUsageAfterBP: test.wantUsage, ExceedsTotal: test.wantExceedsTotal, ExceedsCategory: test.wantExceedsCategory,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v; want %+v", got, want)
			}
			if !reflect.DeepEqual(*snapshot, before) {
				t.Fatal("affordability changed the source snapshot")
			}
		})
	}
}

func TestAffordabilityContractExample(t *testing.T) {
	snapshot := affordabilityTestSnapshot(100000, 46500, 40000)
	snapshot.Categories[4].BudgetCents = 15000
	got := NewService(nil).Affordability(snapshot, 8000, insight.CategoryStudyDigital)
	if got.RemainingAfterCents != 45500 || got.CategoryUsageAfterBP == nil || *got.CategoryUsageAfterBP != 5333 ||
		got.ExceedsTotal || got.ExceedsCategory {
		t.Fatalf("contract example mismatch: %+v", got)
	}
}

func TestAffordabilityInvalidInputsNeverReturnWrappedNumbers(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot *insight.Snapshot
		amount   int64
		category insight.Category
	}{
		{"nil snapshot", nil, 1, insight.CategoryFood},
		{"zero amount", affordabilityTestSnapshot(100, 0, 100), 0, insight.CategoryFood},
		{"negative amount", affordabilityTestSnapshot(100, 0, 100), -1, insight.CategoryFood},
		{"invalid category", affordabilityTestSnapshot(100, 0, 100), 1, "unknown"},
		{"remaining underflow", affordabilityTestSnapshot(0, math.MaxInt64, 0), 2, insight.CategoryFood},
		{"ratio overflow", affordabilityTestSnapshot(math.MaxInt64, math.MaxInt64, 1), 1, insight.CategoryFood},
		{"invalid negative budget", affordabilityTestSnapshot(-1, 0, 0), 1, insight.CategoryFood},
		{"missing categories", &insight.Snapshot{}, 1, insight.CategoryFood},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != invalidAffordability {
					t.Fatalf("want fixed contract failure; got %v", recovered)
				}
			}()
			NewService(nil).Affordability(test.snapshot, test.amount, test.category)
			t.Fatal("returned a value for invalid or unrepresentable input")
		})
	}
}

func affordabilityTestSnapshot(total, spent, categoryBudget int64) *insight.Snapshot {
	snapshot := &insight.Snapshot{
		TotalBudgetCents: total, TotalSpentCents: spent, RemainingCents: total - spent,
	}
	for i, category := range categoryOrder {
		stat := insight.CategoryStat{Category: category}
		if i == 0 {
			stat.SpentCents, stat.BudgetCents = spent, categoryBudget
		}
		snapshot.Categories = append(snapshot.Categories, stat)
	}
	return snapshot
}
