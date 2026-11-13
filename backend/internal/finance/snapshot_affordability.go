package finance

import (
	"math"
	"math/big"

	"unichatgo/internal/insight"
)

const invalidAffordability = "finance: invalid affordability input or unrepresentable result"

// Affordability requires a valid Snapshot, a supported category and a positive
// amount in cents. Callers must validate external input before invoking it.
// The Provider contract has no error return: invalid input or a result outside
// the contract's integer range panics with a fixed message instead of silently
// wrapping, truncating, clamping or reporting a misleading null usage.
func (s *Service) Affordability(snapshot *insight.Snapshot, amountCents int64, category insight.Category) insight.Affordability {
	if snapshot == nil || amountCents <= 0 || !knownCategory(category) ||
		snapshot.TotalBudgetCents < 0 || snapshot.TotalSpentCents < 0 ||
		snapshot.RemainingCents != snapshot.TotalBudgetCents-snapshot.TotalSpentCents {
		panic(invalidAffordability)
	}
	var target insight.CategoryStat
	var totalSpent int64
	seen := make(map[insight.Category]bool, len(categoryOrder))
	for _, stat := range snapshot.Categories {
		if !knownCategory(stat.Category) || seen[stat.Category] || stat.SpentCents < 0 || stat.BudgetCents < 0 ||
			stat.SpentCents > math.MaxInt64-totalSpent {
			panic(invalidAffordability)
		}
		seen[stat.Category] = true
		totalSpent += stat.SpentCents
		if stat.Category == category {
			target = stat
		}
	}
	if len(seen) != len(categoryOrder) || totalSpent != snapshot.TotalSpentCents {
		panic(invalidAffordability)
	}
	var remaining, categoryAfter big.Int
	remaining.Sub(big.NewInt(snapshot.RemainingCents), big.NewInt(amountCents))
	if !remaining.IsInt64() {
		panic(invalidAffordability)
	}
	categoryAfter.Add(big.NewInt(target.SpentCents), big.NewInt(amountCents))
	result := insight.Affordability{
		AmountCents: amountCents, Category: category, RemainingAfterCents: remaining.Int64(),
		ExceedsTotal:    remaining.Sign() < 0,
		ExceedsCategory: categoryAfter.Cmp(big.NewInt(target.BudgetCents)) > 0,
	}
	if target.BudgetCents > 0 {
		var usage big.Int
		usage.Mul(&categoryAfter, big.NewInt(10000))
		usage.Quo(&usage, big.NewInt(target.BudgetCents))
		if !usage.IsInt64() || usage.Int64() > int64(math.MaxInt) {
			panic(invalidAffordability)
		}
		bp := int(usage.Int64())
		result.CategoryUsageAfterBP = &bp
	}
	return result
}
