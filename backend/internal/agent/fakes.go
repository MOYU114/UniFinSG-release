package agent

import (
	"context"

	"unichatgo/internal/insight"
)

// DefaultFakeInsightProvider is intentionally deterministic and is only for
// unit tests and local evaluation commands. Production startup always injects
// finance.Service and never selects this provider.
type DefaultFakeInsightProvider struct {
	SnapshotValue *insight.Snapshot
	SnapshotError error
}

func (f DefaultFakeInsightProvider) Snapshot(context.Context, int64, string) (*insight.Snapshot, error) {
	if f.SnapshotError != nil {
		return nil, f.SnapshotError
	}
	if f.SnapshotValue != nil {
		return f.SnapshotValue, nil
	}
	return &insight.Snapshot{Month: "2026-11", Categories: []insight.CategoryStat{
		{Category: insight.CategoryFood}, {Category: insight.CategoryTransport},
		{Category: insight.CategoryHousingTuition}, {Category: insight.CategoryDailyShopping},
		{Category: insight.CategoryStudyDigital}, {Category: insight.CategoryEntertainmentOther},
	}, Missing: []string{"profile", "budget", "transactions"}}, nil
}

func (f DefaultFakeInsightProvider) Affordability(s *insight.Snapshot, amount int64, category insight.Category) insight.Affordability {
	if s == nil || amount <= 0 || category == "" {
		panic("fake provider: invalid affordability input")
	}
	return insight.Affordability{AmountCents: amount, Category: category, RemainingAfterCents: s.RemainingCents - amount, ExceedsTotal: s.RemainingCents-amount < 0}
}
