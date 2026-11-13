package finance

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDecodeFinanceObject(t *testing.T) {
	for _, raw := range []string{
		"", "null", "[]", `"text"`, "1", "{", `{} {}`,
		`{"monthly_disposable_cents":1.5}`,
		`{"monthly_disposable_cents":"100"}`,
		`{"monthly_disposable_cents":9223372036854775808}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var input ProfileInput
			if err := decodeObject(strings.NewReader(raw), &input); err == nil {
				t.Fatalf("accepted malformed or incorrectly typed JSON: %s", raw)
			}
		})
	}
	var input ProfileInput
	if err := decodeObject(strings.NewReader(` {"monthly_disposable_cents":100,"housing":"on_campus","default_budget_cents":0,"user_id":999} `), &input); err != nil {
		t.Fatal(err)
	}
	if input.MonthlyDisposableCents == nil || *input.MonthlyDisposableCents != 100 ||
		input.DefaultBudgetCents == nil || *input.DefaultBudgetCents != 0 || input.Housing != "on_campus" {
		t.Fatalf("typed fields were not preserved: %+v", input)
	}
	// user_id is intentionally absent from both input structs: JSON body
	// identity cannot influence the authenticated identity used by handlers.
	var budget BudgetInput
	if err := decodeObject(strings.NewReader(`{"total_cents":0,"categories":[],"user_id":999}`), &budget); err != nil {
		t.Fatal(err)
	}
	if budget.TotalCents == nil || *budget.TotalCents != 0 {
		t.Fatalf("zero total was treated as missing: %+v", budget)
	}
}

func TestFinanceErrorsAreStructuredAndDoNotExposeDependencies(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&ValidationError{Fields: map[string]string{"total_cents": "must_be_nonnegative"}}, 400, "validation_failed"},
		{fmt.Errorf("month: %w", ErrInvalidMonth), 400, "bad_request"},
		{fmt.Errorf("read: %w", ErrNotFound), 404, "not_found"},
		{ErrProfileRequired, 409, "profile_required"},
		{ErrInconsistentData, 500, "internal"},
		{errors.New("private database connection details"), 503, "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			writeError(context, test.err)
			assertFinanceError(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "private database") {
				t.Fatal("dependency details leaked into HTTP response")
			}
		})
	}
}
