package finance

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"unichatgo/internal/insight"
)

func TestNormalizeTransactionSingaporeDateBoundary(t *testing.T) {
	tests := []struct {
		name    string
		now     time.Time
		spentOn string
		valid   bool
	}{
		{"before_local_midnight", time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC), "2026-10-31", true},
		{"tomorrow_before_local_midnight", time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC), "2026-11-01", false},
		{"local_today_utc_tomorrow", time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC), "2026-11-01", true},
		{"tomorrow_after_local_midnight", time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC), "2026-11-02", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := transactionValidInput()
			input.SpentOn = test.spentOn
			transaction, err := normalizeTransaction(input, test.now)
			if !test.valid {
				transactionRequireValidation(t, err)
				return
			}
			if err != nil || transaction.SpentOn != test.spentOn {
				t.Fatalf("got transaction=%+v err=%v", transaction, err)
			}
		})
	}
}

func TestNormalizeTransactionDateCalendar(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, date := range []string{"0001-01-01", "1900-01-01", "2000-02-29", "2024-02-29", "2026-01-01"} {
		t.Run("valid_"+date, func(t *testing.T) {
			input := transactionValidInput()
			input.SpentOn = date
			if _, err := normalizeTransaction(input, now); err != nil {
				t.Fatalf("valid past date rejected: %v", err)
			}
		})
	}
	for _, date := range []string{
		"", "2026-1-01", "2026-01-1", "26-01-01", "2026/01/01",
		"2026-01", "2026-01-01T00:00:00Z", "2026-00-01", "2026-13-01",
		"2026-01-00", "2026-04-31", "2025-02-29", "1900-02-29",
		" 2026-01-01", "2026-01-01 ", "２０２６-01-01",
	} {
		t.Run("invalid_"+date, func(t *testing.T) {
			input := transactionValidInput()
			input.SpentOn = date
			_, err := normalizeTransaction(input, now)
			transactionRequireValidation(t, err)
		})
	}
}

func TestNormalizeTransactionAmountAndCategories(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, amount := range []int64{1, 100000000} {
		for _, category := range validationCategoryOrder {
			t.Run(strconv.FormatInt(amount, 10)+"_"+string(category), func(t *testing.T) {
				input := transactionValidInput()
				input.AmountCents = amount
				input.Category = category
				actual, err := normalizeTransaction(input, now)
				if err != nil || actual.AmountCents != amount || actual.Category != category {
					t.Fatalf("valid amount/category rejected or changed: actual=%+v err=%v", actual, err)
				}
			})
		}
	}
	for _, amount := range []int64{math.MinInt64, -1, 0, 100000001, math.MaxInt64} {
		t.Run("invalid_amount_"+strconv.FormatInt(amount, 10), func(t *testing.T) {
			input := transactionValidInput()
			input.AmountCents = amount
			_, err := normalizeTransaction(input, now)
			transactionRequireValidation(t, err)
		})
	}
	for _, category := range []insight.Category{"", "Food", "unknown", "food "} {
		t.Run("invalid_category_"+string(category), func(t *testing.T) {
			input := transactionValidInput()
			input.Category = category
			_, err := normalizeTransaction(input, now)
			transactionRequireValidation(t, err)
		})
	}
}

func TestNormalizeTransactionUnicodeTextLimits(t *testing.T) {
	tests := []struct {
		name     string
		merchant *string
		note     *string
		valid    bool
	}{
		{"omitted", nil, nil, true},
		{"empty", transactionString(""), transactionString(""), true},
		{"unicode_boundaries", transactionString(strings.Repeat("餐", 64)), transactionString(strings.Repeat("🍜", 200)), true},
		{"merchant_over_boundary", transactionString(strings.Repeat("餐", 65)), nil, false},
		{"note_over_boundary", nil, transactionString(strings.Repeat("🍜", 201)), false},
		{"significant_spaces", transactionString("  Fine Food  "), transactionString("  lunch  "), true},
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := transactionValidInput()
			input.Merchant, input.Note = test.merchant, test.note
			transaction, err := normalizeTransaction(input, now)
			if !test.valid {
				transactionRequireValidation(t, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transaction.Merchant, test.merchant) || !reflect.DeepEqual(transaction.Note, test.note) {
				t.Fatalf("text changed: %+v", transaction)
			}
		})
	}
}

func TestTransactionPatchDistinguishesMissingNullAndValue(t *testing.T) {
	var patch TransactionPatch
	err := decodeObject(strings.NewReader(`{"amount_cents":42,"merchant":null,"note":""}`), &patch)
	if err != nil {
		t.Fatal(err)
	}
	if !patch.AmountCents.Set || patch.AmountCents.Value == nil || *patch.AmountCents.Value != 42 {
		t.Fatalf("numeric value was not retained: %+v", patch.AmountCents)
	}
	if !patch.Merchant.Set || patch.Merchant.Value != nil {
		t.Fatalf("explicit null was not retained: %+v", patch.Merchant)
	}
	if !patch.Note.Set || patch.Note.Value == nil || *patch.Note.Value != "" {
		t.Fatalf("empty text was not retained: %+v", patch.Note)
	}
	if patch.SpentOn.Set || patch.Category.Set {
		t.Fatalf("absent fields marked as present: %+v", patch)
	}
}

func TestApplyTransactionPatchPreservesUnspecifiedFields(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	current := transactionCurrent()
	type patchTest struct {
		name    string
		json    string
		changed bool
		want    Transaction
	}
	tests := []patchTest{
		{"empty_object", `{}`, false, current},
		{"unknown_user_id", `{"user_id":999}`, false, current},
		{"explicit_same_value", `{"amount_cents":650}`, true, current},
	}
	amountChanged := current
	amountChanged.AmountCents = 700
	tests = append(tests, patchTest{"one_core_field", `{"amount_cents":700}`, true, amountChanged})
	dateCategoryChanged := current
	dateCategoryChanged.SpentOn, dateCategoryChanged.Category = "2026-10-30", insight.CategoryTransport
	tests = append(tests, patchTest{"date_and_category", `{"spent_on":"2026-10-30","category":"transport"}`, true, dateCategoryChanged})
	textCleared := current
	textCleared.Merchant, textCleared.Note = nil, nil
	tests = append(tests, patchTest{"clear_optional_text", `{"merchant":null,"note":null}`, true, textCleared})
	textEmpty := current
	textEmpty.Note = transactionString("")
	tests = append(tests, patchTest{"empty_text_is_not_null", `{"note":""}`, true, textEmpty})
	textChanged := current
	textChanged.Merchant, textChanged.Note = transactionString("餐厅"), transactionString("Dinner")
	tests = append(tests, patchTest{"replace_text", `{"merchant":"餐厅","note":"Dinner"}`, true, textChanged})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var patch TransactionPatch
			if err := decodeObject(strings.NewReader(test.json), &patch); err != nil {
				t.Fatal(err)
			}
			actual, changed, err := applyTransactionPatch(current, patch, now)
			if err != nil || changed != test.changed {
				t.Fatalf("changed=%v err=%v, want changed=%v", changed, err, test.changed)
			}
			// Persistence owns updated_at. These tests focus on patch semantics.
			actual.UpdatedAt = test.want.UpdatedAt
			if !reflect.DeepEqual(actual, test.want) {
				t.Fatalf("patch changed unspecified data:\ngot  %+v\nwant %+v", actual, test.want)
			}
		})
	}
}

func TestApplyTransactionPatchValidatesResult(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, raw := range []string{
		`{"amount_cents":null}`,
		`{"spent_on":null}`,
		`{"category":null}`,
		`{"amount_cents":0}`,
		`{"amount_cents":100000001}`,
		`{"spent_on":"2026-11-02"}`,
		`{"spent_on":"2025-02-29"}`,
		`{"category":"unknown"}`,
		`{"merchant":"` + strings.Repeat("餐", 65) + `"}`,
		`{"note":"` + strings.Repeat("🍜", 201) + `"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var patch TransactionPatch
			if err := decodeObject(strings.NewReader(raw), &patch); err != nil {
				t.Fatalf("well-formed patch should decode before validation: %v", err)
			}
			_, _, err := applyTransactionPatch(transactionCurrent(), patch, now)
			transactionRequireValidation(t, err)
		})
	}
}

func TestTransactionDecodeRejectsInvalidFieldTypes(t *testing.T) {
	invalid := []string{
		`{"amount_cents":"650"}`,
		`{"amount_cents":1.5}`,
		`{"amount_cents":9223372036854775808}`,
		`{"spent_on":20261031}`,
		`{"category":true}`,
		`{"merchant":123}`,
		`{"note":[]}`,
		`{"note":{}}`,
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			var input TransactionInput
			if err := decodeObject(strings.NewReader(raw), &input); err == nil {
				t.Fatal("invalid creation field type accepted")
			}
			var patch TransactionPatch
			if err := decodeObject(strings.NewReader(raw), &patch); err == nil {
				t.Fatal("invalid patch field type accepted")
			}
		})
	}
}

func TestNormalizeTransactionListDefaultsUseSingaporeMonth(t *testing.T) {
	tests := []struct {
		name  string
		now   time.Time
		month string
	}{
		{"before_month_boundary", time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC), "2026-10"},
		{"after_month_boundary", time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC), "2026-11"},
		{"after_year_boundary", time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC), "2027-01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := normalizeTransactionList(TransactionListOptions{}, test.now)
			if err != nil {
				t.Fatal(err)
			}
			if actual.Month != test.month || actual.Page != 1 || actual.PageSize != 20 || actual.Category != "" {
				t.Fatalf("unexpected defaults: %+v", actual)
			}
		})
	}
}

func TestNormalizeTransactionListPreservesValidFilters(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, category := range append([]insight.Category{""}, validationCategoryOrder...) {
		input := TransactionListOptions{Month: "2024-02", Category: category, Page: 2, PageSize: 100}
		actual, err := normalizeTransactionList(input, now)
		if err != nil || actual != input {
			t.Fatalf("valid filters changed: got=%+v err=%v want=%+v", actual, err, input)
		}
	}
}

func TestNormalizeTransactionListRejectsInvalidOptions(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	tests := []TransactionListOptions{
		{Page: -1}, {PageSize: -1}, {PageSize: 101},
		{Month: "2026-1"}, {Month: "2026-13"}, {Month: "2026-01-01"},
		{Category: "Food"}, {Category: "unknown"},
	}
	if strconv.IntSize == 64 {
		tests = append(tests, TransactionListOptions{Page: int(^uint(0) >> 1), PageSize: 100})
	}
	for _, input := range tests {
		if actual, err := normalizeTransactionList(input, now); err == nil {
			t.Errorf("invalid pagination/filter accepted: input=%+v actual=%+v", input, actual)
		}
	}
}

func transactionValidInput() TransactionInput {
	return TransactionInput{AmountCents: 650, SpentOn: "2026-10-31", Category: insight.CategoryFood}
}

func transactionCurrent() Transaction {
	created := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	return Transaction{
		ID: 501, AmountCents: 650, SpentOn: "2026-10-31", Category: insight.CategoryFood,
		Merchant: transactionString("Fine Food"), Note: transactionString("Lunch"),
		CreatedAt: created, UpdatedAt: created,
	}
}

func transactionRequireValidation(t *testing.T, err error) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Fields) == 0 {
		t.Fatalf("expected field validation error, got %v", err)
	}
}

func transactionString(value string) *string { return &value }
