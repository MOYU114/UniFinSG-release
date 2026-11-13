package main

import (
	"strings"
	"testing"
	"time"

	"unichatgo/internal/insight"
)

func TestSeedMonthUsesSingaporeDayAndRejectsFuture(t *testing.T) {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, requested, now, month string
		days                        int
		fail                        bool
	}{
		{"Singapore new day", "", "2026-10-28T16:05:00Z", "2026-10", 29, false},
		{"Singapore new month", "", "2026-10-31T16:00:00Z", "2026-11", 1, false},
		{"past leap February", "2024-02", "2026-10-28T00:00:00Z", "2024-02", 29, false},
		{"past normal February", "2025-02", "2026-10-28T00:00:00Z", "2025-02", 28, false},
		{"future month", "2026-11", "2026-10-28T00:00:00Z", "", 0, true},
		{"invalid month", "2026-13", "2026-10-28T00:00:00Z", "", 0, true},
		{"noncanonical month", "2026-1", "2026-10-28T00:00:00Z", "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			start, days, err := seedMonth(test.requested, now.In(location))
			if (err != nil) != test.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if !test.fail && (start.Format("2006-01") != test.month || days != test.days) {
				t.Fatalf("month=%s days=%d", start.Format("2006-01"), days)
			}
		})
	}
}

func TestDemoDataStaysWithinSelectedDays(t *testing.T) {
	start := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{1, 2, 29} {
		seen := make(map[insight.Category]bool)
		fixtures := demoTransactions(start, days)
		for _, input := range fixtures {
			date, err := time.Parse("2006-01-02", input.SpentOn)
			if err != nil || date.Before(start) || date.After(start.AddDate(0, 0, days-1)) {
				t.Fatalf("fixture date outside selected range: %s", input.SpentOn)
			}
			if input.AmountCents <= 0 || input.AmountCents > 100000000 {
				t.Fatal("fixture amount violates transaction contract")
			}
			if input.Note == nil || !strings.Contains(*input.Note, "demo") {
				t.Fatal("fixtures must be explicitly marked as demo data")
			}
			seen[input.Category] = true
		}
		if days == 29 && len(seen) != 6 {
			t.Fatal("a full demo month should exercise all six categories")
		}
	}
}
