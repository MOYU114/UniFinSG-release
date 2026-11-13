package agentv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/tools"
)

// Extraction warnings. The first two mirror the draft card markers from
// design section 6.3; the rest explain deterministic corrections.
const (
	WarningPossibleDuplicate = "possible_duplicate"
	WarningNeedsReview       = "needs_review"
	WarningAmountDropped     = "amount_dropped"
	WarningFutureDate        = "future_date"
	WarningInvalidDate       = "invalid_date"
	WarningRecordsTruncated  = "records_truncated"
)

// ExtractionRecord is one record of the submit_extraction tool call. Pointers
// distinguish a missing field from a zero value.
type ExtractionRecord struct {
	AmountCents *int64   `json:"amount_cents"`
	SpentOn     *string  `json:"spent_on"`
	Merchant    *string  `json:"merchant"`
	Category    *string  `json:"category"`
	Note        *string  `json:"note"`
	Confidence  *float64 `json:"confidence"`
}

// Extraction is the full submit_extraction payload.
type Extraction struct {
	Records   []ExtractionRecord `json:"records"`
	ImageKind string             `json:"image_kind"`
	Notes     string             `json:"notes"`
}

// ValidatedRecord is one record after the deterministic corrections.
type ValidatedRecord struct {
	AmountCents int64
	SpentOn     string
	Category    insight.Category
	Merchant    *string
	Note        *string
	Confidence  float64
	Warnings    []string
}

// ValidateExtraction applies the rules of design section 6.3: drop impossible
// amounts, default missing dates to today (SGT), reject dates beyond tomorrow,
// fall back to entertainment_other for unknown categories and cap the list at
// 20 records.
func ValidateExtraction(in Extraction, now time.Time) ([]ValidatedRecord, []string) {
	today := finance.SingaporeToday(now)
	tomorrow := finance.SingaporeToday(now.AddDate(0, 0, 1))
	warnings := make([]string, 0, 4)
	records := in.Records
	if len(records) > finance.MaxProposalItems {
		records = records[:finance.MaxProposalItems]
		warnings = append(warnings, WarningRecordsTruncated)
	}
	out := make([]ValidatedRecord, 0, len(records))
	for _, record := range records {
		if record.AmountCents == nil || *record.AmountCents < finance.MinTransactionAmountCents ||
			*record.AmountCents > finance.MaxTransactionAmountCents {
			warnings = append(warnings, WarningAmountDropped)
			continue
		}
		validated := ValidatedRecord{
			AmountCents: *record.AmountCents,
			SpentOn:     today,
			Category:    insight.CategoryEntertainmentOther,
			Confidence:  confidenceOrZero(record.Confidence),
		}
		dateWarning := ""
		if record.SpentOn != nil && strings.TrimSpace(*record.SpentOn) != "" {
			raw := strings.TrimSpace(*record.SpentOn)
			if _, err := time.ParseInLocation("2006-01-02", raw, singaporeLocation()); err != nil {
				dateWarning = WarningInvalidDate
			} else if raw > tomorrow {
				dateWarning = WarningFutureDate
			} else {
				validated.SpentOn = raw
			}
		}
		if dateWarning != "" {
			validated.Warnings = append(validated.Warnings, dateWarning)
		}
		if record.Category != nil && knownCategory(*record.Category) {
			validated.Category = insight.Category(*record.Category)
		} else {
			validated.Warnings = append(validated.Warnings, WarningNeedsReview)
		}
		if record.Merchant != nil {
			if value := truncateRunes(strings.TrimSpace(*record.Merchant), finance.MaxMerchantRunes); value != "" {
				validated.Merchant = &value
			}
		}
		if record.Note != nil {
			if value := truncateRunes(strings.TrimSpace(*record.Note), finance.MaxNoteRunes); value != "" {
				validated.Note = &value
			}
		}
		out = append(out, validated)
	}
	return out, warnings
}

// DraftWarnings flattens the global and per-record warnings for the draft card.
func DraftWarnings(global []string, records []ValidatedRecord) []string {
	seen := make(map[string]struct{}, len(global)+len(records))
	out := make([]string, 0, len(global)+len(records))
	add := func(value string) {
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	for _, warning := range global {
		add(warning)
	}
	for _, record := range records {
		for _, warning := range record.Warnings {
			add(warning)
		}
	}
	return out
}

func confidenceOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	confidence := *value
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func knownCategory(value string) bool {
	for _, category := range tools.Categories {
		if category.Category == value {
			return true
		}
	}
	return false
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

// DuplicateFinder marks records that look like an existing transaction. It is
// advisory: a failure must not fail the turn.
type DuplicateFinder interface {
	MarkDuplicates(ctx context.Context, userID int64, records []ValidatedRecord) error
}

// MySQLDuplicateFinder flags the same user, same amount, a date within one day
// and a similar merchant.
type MySQLDuplicateFinder struct {
	db *sql.DB
}

func NewMySQLDuplicateFinder(db *sql.DB) *MySQLDuplicateFinder {
	return &MySQLDuplicateFinder{db: db}
}

func (f *MySQLDuplicateFinder) MarkDuplicates(ctx context.Context, userID int64, records []ValidatedRecord) error {
	if len(records) == 0 {
		return nil
	}
	amounts := make([]any, 0, len(records))
	seen := make(map[int64]struct{}, len(records))
	minDate, maxDate := "", ""
	for _, record := range records {
		if _, ok := seen[record.AmountCents]; !ok {
			seen[record.AmountCents] = struct{}{}
			amounts = append(amounts, record.AmountCents)
		}
		if minDate == "" || record.SpentOn < minDate {
			minDate = record.SpentOn
		}
		if maxDate == "" || record.SpentOn > maxDate {
			maxDate = record.SpentOn
		}
	}
	start, err := time.ParseInLocation("2006-01-02", minDate, singaporeLocation())
	if err != nil {
		return nil
	}
	end, err := time.ParseInLocation("2006-01-02", maxDate, singaporeLocation())
	if err != nil {
		return nil
	}
	query := `SELECT amount_cents, spent_on, merchant FROM transactions
		WHERE user_id = ? AND amount_cents IN (` + placeholders(len(amounts)) + `)
		AND spent_on BETWEEN ? AND ?`
	args := make([]any, 0, len(amounts)+3)
	args = append(args, userID)
	args = append(args, amounts...)
	args = append(args, start.AddDate(0, 0, -1).Format("2006-01-02"), end.AddDate(0, 0, 1).Format("2006-01-02"))
	rows, err := f.db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("find duplicate transactions: %w", err)
	}
	defer rows.Close()
	type existing struct {
		amount   int64
		spentOn  string
		merchant string
	}
	existingRows := make([]existing, 0, 8)
	for rows.Next() {
		var (
			amount   int64
			spentOn  time.Time
			merchant sql.NullString
		)
		if err := rows.Scan(&amount, &spentOn, &merchant); err != nil {
			return fmt.Errorf("scan duplicate transaction: %w", err)
		}
		existingRows = append(existingRows, existing{
			amount: amount, spentOn: spentOn.Format("2006-01-02"), merchant: merchant.String,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read duplicate transactions: %w", err)
	}
	for i := range records {
		for _, row := range existingRows {
			if !isPossibleDuplicate(records[i], row.amount, row.spentOn, row.merchant) {
				continue
			}
			records[i].Warnings = appendUnique(records[i].Warnings, WarningPossibleDuplicate)
			break
		}
	}
	return nil
}

// isPossibleDuplicate matches the same amount, a date within one day and a
// similar merchant.
func isPossibleDuplicate(record ValidatedRecord, amount int64, spentOn, merchant string) bool {
	if amount != record.AmountCents {
		return false
	}
	recordDate, err := time.ParseInLocation("2006-01-02", record.SpentOn, singaporeLocation())
	if err != nil {
		return false
	}
	rowDate, err := time.ParseInLocation("2006-01-02", spentOn, singaporeLocation())
	if err != nil {
		return false
	}
	days := int(rowDate.Sub(recordDate).Hours() / 24)
	if days < -1 || days > 1 {
		return false
	}
	recordMerchant := ""
	if record.Merchant != nil {
		recordMerchant = *record.Merchant
	}
	return similarMerchant(recordMerchant, merchant)
}

// similarMerchant compares normalised names; a missing name on either side
// leaves amount and date as the signal.
func similarMerchant(left, right string) bool {
	a := normalizeMerchant(left)
	b := normalizeMerchant(right)
	if a == "" || b == "" {
		return true
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func normalizeMerchant(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// extractionSchema is the submit_extraction JSON schema from design 6.3.
const extractionSchema = `{
  "type": "object",
  "properties": {
    "records": {
      "type": "array",
      "maxItems": 20,
      "items": {
        "type": "object",
        "properties": {
          "amount_cents": {"type": "integer", "minimum": 1, "maximum": 100000000},
          "spent_on": {"type": ["string", "null"], "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
          "merchant": {"type": ["string", "null"], "maxLength": 64},
          "category": {
            "type": ["string", "null"],
            "enum": ["food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other", null]
          },
          "note": {"type": ["string", "null"], "maxLength": 200},
          "confidence": {"type": "number", "minimum": 0, "maximum": 1}
        },
        "required": ["amount_cents"]
      }
    },
    "image_kind": {"type": "string", "enum": ["receipt", "bank_app", "payment_screenshot", "other"]},
    "notes": {"type": "string"}
  },
  "required": ["records"]
}`

var extractionSchemaJSON = json.RawMessage(extractionSchema)
