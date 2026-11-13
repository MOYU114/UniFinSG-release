package agentv2

import (
	"testing"
	"time"

	"unichatgo/internal/insight"
)

func extractionTime() time.Time {
	return time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
}

func ptr[T any](value T) *T { return &value }

func TestValidateExtractionDropsInvalidAmounts(t *testing.T) {
	records, warnings := ValidateExtraction(Extraction{Records: []ExtractionRecord{
		{AmountCents: ptr[int64](0)},
		{AmountCents: ptr[int64](-100)},
		{AmountCents: ptr[int64](100000001)},
		{AmountCents: nil},
		{AmountCents: ptr[int64](650), SpentOn: ptr("2026-11-05")},
	}}, extractionTime())
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1: %+v", len(records), records)
	}
	if records[0].AmountCents != 650 {
		t.Fatalf("amount = %d", records[0].AmountCents)
	}
	if len(warnings) != 4 {
		t.Fatalf("warnings = %v, want four amount_dropped", warnings)
	}
	for _, warning := range warnings {
		if warning != WarningAmountDropped {
			t.Fatalf("unexpected warning %q", warning)
		}
	}
}

func TestValidateExtractionCorrectsDates(t *testing.T) {
	records, _ := ValidateExtraction(Extraction{Records: []ExtractionRecord{
		{AmountCents: ptr[int64](100)},
		{AmountCents: ptr[int64](200), SpentOn: ptr("2026-11-20")},
		{AmountCents: ptr[int64](300), SpentOn: ptr("not-a-date")},
		{AmountCents: ptr[int64](400), SpentOn: ptr("2026-11-07")},
	}}, extractionTime())
	if records[0].SpentOn != "2026-11-06" {
		t.Fatalf("missing date = %q, want today", records[0].SpentOn)
	}
	if records[1].SpentOn != "2026-11-06" || !hasWarning(records[1].Warnings, WarningFutureDate) {
		t.Fatalf("future date was not corrected: %+v", records[1])
	}
	if records[2].SpentOn != "2026-11-06" || !hasWarning(records[2].Warnings, WarningInvalidDate) {
		t.Fatalf("invalid date was not corrected: %+v", records[2])
	}
	if records[3].SpentOn != "2026-11-07" {
		t.Fatalf("tomorrow must be accepted, got %q", records[3].SpentOn)
	}
}

func TestValidateExtractionFallsBackToEntertainment(t *testing.T) {
	records, _ := ValidateExtraction(Extraction{Records: []ExtractionRecord{
		{AmountCents: ptr[int64](100), Category: ptr("groceries")},
		{AmountCents: ptr[int64](200), Category: nil},
		{AmountCents: ptr[int64](300), Category: ptr("food")},
	}}, extractionTime())
	for _, index := range []int{0, 1} {
		if records[index].Category != insight.CategoryEntertainmentOther {
			t.Fatalf("record %d category = %q", index, records[index].Category)
		}
		if !hasWarning(records[index].Warnings, WarningNeedsReview) {
			t.Fatalf("record %d is missing needs_review", index)
		}
	}
	if records[2].Category != insight.CategoryFood || len(records[2].Warnings) != 0 {
		t.Fatalf("valid category was changed: %+v", records[2])
	}
}

func TestValidateExtractionCapsAtTwentyRecords(t *testing.T) {
	records := make([]ExtractionRecord, 0, 21)
	for i := 0; i < 21; i++ {
		records = append(records, ExtractionRecord{AmountCents: ptr(int64(100 + i))})
	}
	validated, warnings := ValidateExtraction(Extraction{Records: records}, extractionTime())
	if len(validated) != 20 {
		t.Fatalf("records = %d, want 20", len(validated))
	}
	if !hasWarning(warnings, WarningRecordsTruncated) {
		t.Fatalf("missing records_truncated: %v", warnings)
	}
}

func TestDraftWarningsDeduplicate(t *testing.T) {
	records := []ValidatedRecord{
		{Warnings: []string{WarningNeedsReview, WarningPossibleDuplicate}},
		{Warnings: []string{WarningNeedsReview}},
	}
	warnings := DraftWarnings([]string{WarningRecordsTruncated}, records)
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestPossibleDuplicateRules(t *testing.T) {
	record := ValidatedRecord{
		AmountCents: 650, SpentOn: "2026-11-05", Merchant: ptr("Kopitiam"),
	}
	cases := []struct {
		name     string
		amount   int64
		spentOn  string
		merchant string
		want     bool
	}{
		{"same amount and day", 650, "2026-11-05", "kopitiam", true},
		{"one day earlier", 650, "2026-11-04", "KOPITIAM PTE LTD", true},
		{"one day later", 650, "2026-11-06", "Kopitiam", true},
		{"two days earlier", 650, "2026-11-03", "Kopitiam", false},
		{"different amount", 651, "2026-11-05", "Kopitiam", false},
		{"different merchant", 650, "2026-11-05", "FairPrice", false},
		{"missing merchant", 650, "2026-11-05", "", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isPossibleDuplicate(record, test.amount, test.spentOn, test.merchant); got != test.want {
				t.Fatalf("isPossibleDuplicate = %v, want %v", got, test.want)
			}
		})
	}
}

func hasWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}
