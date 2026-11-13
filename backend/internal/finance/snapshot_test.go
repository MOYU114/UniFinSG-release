package finance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"unichatgo/internal/insight"
)

func TestSnapshotCalculation(t *testing.T) {
	tests := []struct {
		name, month, now string
		spent            int64
		count, days      int
		budget           *Budget
		profile          bool
		elapsed, length  int
		usage            *int
		status           insight.Status
		projection       *int64
		missing          []string
	}{
		{"zero spending", "2026-11", "2026-11-12T09:00:00+08:00", 0, 0, 0, snapshotTestBudget(10000, 8000), true, 12, 30, snapshotTestInt(0), insight.StatusOK, nil, []string{"transactions"}},
		{"exact 80 percent", "2026-11", "2026-11-12T09:00:00+08:00", 8000, 3, 3, snapshotTestBudget(10000, 8000), true, 12, 30, snapshotTestInt(8000), insight.StatusWarn, financeInt64(20000), []string{}},
		{"exact 100 percent", "2026-11", "2026-11-12T09:00:00+08:00", 10000, 3, 3, snapshotTestBudget(10000, 8000), true, 12, 30, snapshotTestInt(10000), insight.StatusWarn, financeInt64(25000), []string{}},
		{"over", "2026-11", "2026-11-12T09:00:00+08:00", 10001, 3, 3, snapshotTestBudget(10000, 10000), true, 12, 30, snapshotTestInt(10001), insight.StatusOver, financeInt64(25002), []string{}},
		{"custom warn below", "2026-11", "2026-11-12T09:00:00+08:00", 8999, 3, 3, snapshotTestBudget(10000, 9000), true, 12, 30, snapshotTestInt(8999), insight.StatusOK, financeInt64(22497), []string{}},
		{"custom warn at threshold", "2026-11", "2026-11-12T09:00:00+08:00", 6000, 3, 3, snapshotTestBudget(10000, 6000), true, 12, 30, snapshotTestInt(6000), insight.StatusWarn, financeInt64(15000), []string{}},
		{"no budget", "2026-11", "2026-11-12T09:00:00+08:00", 100, 1, 1, nil, true, 12, 30, nil, insight.StatusOver, nil, []string{"budget"}},
		{"all missing", "2026-11", "2026-11-12T09:00:00+08:00", 0, 0, 0, nil, false, 12, 30, nil, insight.StatusOK, nil, []string{"profile", "budget", "transactions"}},
		{"zero budget zero spending", "2026-11", "2026-11-12T09:00:00+08:00", 0, 0, 0, snapshotTestBudget(0, 8000), true, 12, 30, nil, insight.StatusOK, nil, []string{"transactions"}},
		{"zero budget positive spending", "2026-11", "2026-11-12T09:00:00+08:00", 100, 1, 1, snapshotTestBudget(0, 8000), true, 12, 30, nil, insight.StatusOver, nil, []string{}},
		{"one spending date many records", "2026-11", "2026-11-12T09:00:00+08:00", 100, 5, 1, snapshotTestBudget(10000, 8000), true, 12, 30, snapshotTestInt(100), insight.StatusOK, nil, []string{}},
		{"two spending dates", "2026-11", "2026-11-12T09:00:00+08:00", 100, 5, 2, snapshotTestBudget(10000, 8000), true, 12, 30, snapshotTestInt(100), insight.StatusOK, nil, []string{}},
		{"first Singapore day", "2026-11", "2026-10-31T16:00:00Z", 100, 1, 1, snapshotTestBudget(10000, 8000), true, 1, 30, snapshotTestInt(100), insight.StatusOK, nil, []string{}},
		{"last month day", "2026-11", "2026-11-30T23:59:59+08:00", 100, 3, 3, snapshotTestBudget(10000, 8000), true, 30, 30, snapshotTestInt(100), insight.StatusOK, financeInt64(100), []string{}},
		{"February", "2026-02", "2026-02-14T09:00:00+08:00", 100, 3, 3, snapshotTestBudget(10000, 8000), true, 14, 28, snapshotTestInt(100), insight.StatusOK, financeInt64(200), []string{}},
		{"leap February", "2028-02", "2028-02-29T09:00:00+08:00", 100, 3, 3, snapshotTestBudget(10000, 8000), true, 29, 29, snapshotTestInt(100), insight.StatusOK, financeInt64(100), []string{}},
		{"past with one date", "2026-09", "2026-11-12T09:00:00+08:00", 100, 1, 1, snapshotTestBudget(10000, 8000), true, 30, 30, snapshotTestInt(100), insight.StatusOK, financeInt64(100), []string{}},
		{"past without spending", "2026-09", "2026-11-12T09:00:00+08:00", 0, 0, 0, snapshotTestBudget(10000, 8000), true, 30, 30, snapshotTestInt(0), insight.StatusOK, financeInt64(0), []string{"transactions"}},
		{"future", "2026-12", "2026-11-12T09:00:00+08:00", 0, 0, 0, snapshotTestBudget(10000, 8000), true, 0, 31, snapshotTestInt(0), insight.StatusOK, nil, []string{"transactions"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			data := map[insight.Category]snapshotSpending{}
			if test.count > 0 {
				data[insight.CategoryFood] = snapshotSpending{test.spent, test.count}
			}
			got, err := calculateSnapshot(test.month, now, test.profile, test.budget, data, test.days)
			if err != nil {
				t.Fatal(err)
			}
			if got.DaysElapsed != test.elapsed || got.DaysInMonth != test.length ||
				got.TotalSpentCents != test.spent || got.TransactionCount != test.count ||
				!reflect.DeepEqual(got.UsageBP, test.usage) || !reflect.DeepEqual(got.ProjectedSpentCents, test.projection) ||
				!reflect.DeepEqual(got.Missing, test.missing) {
				t.Fatalf("snapshot mismatch: %+v", got)
			}
			if len(got.Categories) != 6 || got.Categories[0].Status != test.status ||
				!reflect.DeepEqual(got.Categories[0].UsageBP, test.usage) {
				t.Fatalf("categories mismatch: %+v", got.Categories)
			}
			for i, category := range got.Categories {
				if category.Category != categoryOrder[i] {
					t.Fatal("unstable category order")
				}
			}
			if test.spent == 0 && got.TopCategory != nil || test.spent > 0 && (got.TopCategory == nil || *got.TopCategory != insight.CategoryFood) {
				t.Fatalf("unexpected top category: %v", got.TopCategory)
			}
			if got.RemainingCents != got.TotalBudgetCents-got.TotalSpentCents || got.AsOf.Location() != singapore ||
				!strings.HasPrefix(got.Hash, "sha256:") || len(got.Hash) != 71 {
				t.Fatalf("invalid calculated values: %+v", got)
			}
		})
	}
}

func TestSnapshotHashAndTopCategoryAreDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 12, 9, 0, 0, 123, singapore)
	data := map[insight.Category]snapshotSpending{
		insight.CategoryTransport: {100, 1},
		insight.CategoryFood:      {100, 1},
	}
	first, err := calculateSnapshot("2026-11", now, true, snapshotTestBudget(1000, 8000), data, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.TopCategory == nil || *first.TopCategory != insight.CategoryFood {
		t.Fatal("top category ties must use contract category order")
	}
	second, err := calculateSnapshot("2026-11", now.UTC(), true, snapshotTestBudget(1000, 8000),
		map[insight.Category]snapshotSpending{insight.CategoryFood: {100, 1}, insight.CategoryTransport: {100, 1}}, 2)
	if err != nil || second.Hash != first.Hash {
		t.Fatalf("same snapshot has unstable hash: %v", err)
	}
	first.Hash = "excluded"
	if hash, err := hashSnapshot(first); err != nil || hash != second.Hash {
		t.Fatalf("hash included itself: %s %v", hash, err)
	}
	first.AsOf = first.AsOf.Add(time.Nanosecond)
	if hash, err := hashSnapshot(first); err != nil || hash == second.Hash {
		t.Fatal("hash did not include as_of")
	}
	encoded, err := json.Marshal(second)
	if err != nil || !strings.Contains(string(encoded), `"missing":[]`) {
		t.Fatal("missing must serialize as an array")
	}
}

func TestSnapshotIntegerArithmetic(t *testing.T) {
	for _, test := range []struct {
		name                 string
		value, scale, divide int64
		want                 int64
		fail                 bool
	}{
		{"multiply beyond int64 but result fits", math.MaxInt64, 10000, math.MaxInt64, 10000, false},
		{"precision above float integer range", 9007199254740993, 10000, 9007199254740995, 9999, false},
		{"round down cents", 10001, 30, 12, 25002, false},
		{"overflow output", math.MaxInt64, 10000, 1, 0, true},
		{"negative value", -1, 1, 1, 0, true},
		{"zero denominator", 1, 1, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := snapshotScaledInt64(test.value, test.scale, test.divide)
			if test.fail {
				if !errors.Is(err, ErrInconsistentData) {
					t.Fatalf("expected range error; got %d %v", got, err)
				}
			} else if err != nil || got != test.want {
				t.Fatalf("got %d %v; want %d", got, err, test.want)
			}
		})
	}
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, singapore)
	for _, test := range []struct {
		name     string
		budget   *Budget
		spending map[insight.Category]snapshotSpending
	}{
		{"total overflow", nil, map[insight.Category]snapshotSpending{insight.CategoryFood: {math.MaxInt64, 3}, insight.CategoryTransport: {1, 1}}},
		{"projection overflow", nil, map[insight.Category]snapshotSpending{insight.CategoryFood: {math.MaxInt64, 3}}},
		{"usage overflow", snapshotTestBudget(1, 8000), map[insight.Category]snapshotSpending{insight.CategoryFood: {math.MaxInt64, 3}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := calculateSnapshot("2026-11", now, true, test.budget, test.spending, 3); !errors.Is(err, ErrInconsistentData) {
				t.Fatalf("expected calculation range error: %v", err)
			}
		})
	}
}

func TestSnapshotRejectsInvalidMonthBeforeDatabaseAccess(t *testing.T) {
	service := NewService(nil)
	for _, month := range []string{"2026-1", "2026-00", "2026-13", "0000-01", "2026-11-01", " 2026-11", "2026-11 "} {
		if _, err := service.Snapshot(context.Background(), 1, month); !errors.Is(err, ErrInvalidMonth) {
			t.Fatalf("accepted invalid month %q: %v", month, err)
		}
	}
}

func snapshotTestBudget(total int64, warn int) *Budget {
	budget := &Budget{TotalCents: total, WarnBP: warn}
	for i, category := range categoryOrder {
		amount := int64(0)
		if i == 0 {
			amount = total
		}
		budget.Categories = append(budget.Categories, CategoryBudget{category, amount})
	}
	return budget
}

func snapshotTestInt(value int) *int { return &value }
