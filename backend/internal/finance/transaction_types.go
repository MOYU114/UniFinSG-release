package finance

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	"unichatgo/internal/insight"
)

var transactionDatePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// Transaction provenance values stored in transactions.source.
const (
	SourceManual = "manual"
	SourceAgent  = "agent"
)

// Shared limits. The HTTP handlers, the agent tools and the draft decision
// endpoint all validate through the helpers below so the model path and the
// confirmation path cannot drift.
const (
	MinTransactionAmountCents int64 = 1
	MaxTransactionAmountCents int64 = 100000000
	MaxMerchantRunes                = 64
	MaxNoteRunes                    = 200
	MaxProposalItems                = 20
)

type TransactionInput struct {
	AmountCents int64            `json:"amount_cents"`
	SpentOn     string           `json:"spent_on"`
	Category    insight.Category `json:"category"`
	Merchant    *string          `json:"merchant"`
	Note        *string          `json:"note"`
}

type Transaction struct {
	ID          int64            `json:"id"`
	AmountCents int64            `json:"amount_cents"`
	SpentOn     string           `json:"spent_on"`
	Category    insight.Category `json:"category"`
	Merchant    *string          `json:"merchant"`
	Note        *string          `json:"note"`
	Source      string           `json:"source"`
	DraftID     *string          `json:"draft_id,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// TransactionPatchField distinguishes an absent field from an explicit null.
// Value is nil for null; Set is false only when the field was not supplied.
type TransactionPatchField[T any] struct {
	Set   bool
	Value *T
}

func (field *TransactionPatchField[T]) UnmarshalJSON(raw []byte) error {
	field.Set = true
	field.Value = nil
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	field.Value = &value
	return nil
}

type TransactionPatch struct {
	AmountCents TransactionPatchField[int64]            `json:"amount_cents"`
	SpentOn     TransactionPatchField[string]           `json:"spent_on"`
	Category    TransactionPatchField[insight.Category] `json:"category"`
	Merchant    TransactionPatchField[string]           `json:"merchant"`
	Note        TransactionPatchField[string]           `json:"note"`
}

type TransactionListOptions struct {
	Month    string
	Category insight.Category
	Page     int
	PageSize int
}

type TransactionPage struct {
	Items    []Transaction `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Total    int64         `json:"total"`
}

func normalizeTransaction(input TransactionInput, now time.Time) (Transaction, error) {
	fields := make(map[string]string)
	if input.AmountCents < MinTransactionAmountCents || input.AmountCents > MaxTransactionAmountCents {
		fields["amount_cents"] = "must_be_an_integer_between_1_and_100000000"
	}
	if !transactionDatePattern.MatchString(input.SpentOn) || input.SpentOn[:4] == "0000" {
		fields["spent_on"] = "must_be_a_valid_date"
	} else if _, err := time.ParseInLocation("2006-01-02", input.SpentOn, singapore); err != nil {
		fields["spent_on"] = "must_be_a_valid_date"
	} else if input.SpentOn > now.In(singapore).Format("2006-01-02") {
		fields["spent_on"] = "must_not_be_in_the_future"
	}
	if !knownCategory(input.Category) {
		fields["category"] = "must_be_a_supported_category"
	}
	if input.Merchant != nil && utf8.RuneCountInString(*input.Merchant) > MaxMerchantRunes {
		fields["merchant"] = "must_be_at_most_64_characters"
	}
	if input.Note != nil && utf8.RuneCountInString(*input.Note) > MaxNoteRunes {
		fields["note"] = "must_be_at_most_200_characters"
	}
	if len(fields) > 0 {
		return Transaction{}, &ValidationError{Fields: fields}
	}
	return Transaction{
		AmountCents: input.AmountCents, SpentOn: input.SpentOn, Category: input.Category,
		Merchant: input.Merchant, Note: input.Note, Source: SourceManual,
	}, nil
}

// ValidateTransactionInput applies the shared transaction rules. Tools and the
// draft decision endpoint both call it before anything is written.
func ValidateTransactionInput(input TransactionInput, now time.Time) error {
	_, err := normalizeTransaction(input, now)
	return err
}

// NormalizeTransactionInput validates and returns the canonical record,
// including the manual provenance used by ordinary user writes.
func NormalizeTransactionInput(input TransactionInput, now time.Time) (Transaction, error) {
	return normalizeTransaction(input, now)
}

// ParseTransactionPatch decodes a partial update and rejects unknown fields so
// a misspelled key cannot silently drop a change.
func ParseTransactionPatch(raw []byte) (TransactionPatch, error) {
	var patch TransactionPatch
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return patch, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return TransactionPatch{}, &ValidationError{Fields: map[string]string{"changes": "must_be_a_supported_patch"}}
	}
	return patch, nil
}

// ApplyTransactionPatch validates a patch against the current record and
// returns the resulting record. changed is false for an empty patch.
func ApplyTransactionPatch(current Transaction, patch TransactionPatch, now time.Time) (Transaction, bool, error) {
	return applyTransactionPatch(current, patch, now)
}

func applyTransactionPatch(current Transaction, patch TransactionPatch, now time.Time) (Transaction, bool, error) {
	changed := patch.AmountCents.Set || patch.SpentOn.Set || patch.Category.Set || patch.Merchant.Set || patch.Note.Set
	if !changed {
		return current, false, nil
	}
	fields := make(map[string]string)
	input := TransactionInput{
		AmountCents: current.AmountCents, SpentOn: current.SpentOn, Category: current.Category,
		Merchant: current.Merchant, Note: current.Note,
	}
	if patch.AmountCents.Set {
		if patch.AmountCents.Value == nil {
			fields["amount_cents"] = "must_not_be_null"
		} else {
			input.AmountCents = *patch.AmountCents.Value
		}
	}
	if patch.SpentOn.Set {
		if patch.SpentOn.Value == nil {
			fields["spent_on"] = "must_not_be_null"
		} else {
			input.SpentOn = *patch.SpentOn.Value
		}
	}
	if patch.Category.Set {
		if patch.Category.Value == nil {
			fields["category"] = "must_not_be_null"
		} else {
			input.Category = *patch.Category.Value
		}
	}
	if patch.Merchant.Set {
		input.Merchant = patch.Merchant.Value
	}
	if patch.Note.Set {
		input.Note = patch.Note.Value
	}
	if len(fields) > 0 {
		return Transaction{}, false, &ValidationError{Fields: fields}
	}
	updated, err := normalizeTransaction(input, now)
	if err != nil {
		return Transaction{}, false, err
	}
	updated.ID, updated.CreatedAt, updated.UpdatedAt = current.ID, current.CreatedAt, current.UpdatedAt
	// Provenance is owned by the write path, not by the patch.
	updated.Source, updated.DraftID = current.Source, current.DraftID
	return updated, true, nil
}

func normalizeTransactionList(options TransactionListOptions, now time.Time) (TransactionListOptions, error) {
	if options.Month == "" {
		options.Month = now.In(singapore).Format("2006-01")
	}
	if err := validateMonth(options.Month); err != nil {
		return TransactionListOptions{}, err
	}
	if options.Page == 0 {
		options.Page = 1
	}
	if options.PageSize == 0 {
		options.PageSize = 20
	}
	fields := make(map[string]string)
	if options.Page < 1 {
		fields["page"] = "must_be_positive"
	}
	if options.PageSize < 1 || options.PageSize > 100 {
		fields["page_size"] = "must_be_between_1_and_100"
	}
	if options.Category != "" && !knownCategory(options.Category) {
		fields["category"] = "must_be_a_supported_category"
	}
	if options.Page > 0 && options.PageSize > 0 &&
		int64(options.Page-1) > math.MaxInt64/int64(options.PageSize) {
		fields["page"] = "pagination_offset_exceeds_integer_range"
	}
	if len(fields) > 0 {
		return TransactionListOptions{}, &ValidationError{Fields: fields}
	}
	return options, nil
}
