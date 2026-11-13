package eval

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
)

func TestValidateEvent(t *testing.T) {
	base := EventInput{ParticipantCode: "P00", Task: "onboarding", Event: "end", Success: evalBool(false)}
	for _, task := range taskOrder {
		input := base
		input.Task = task
		if _, err := validateEvent(input); err != nil {
			t.Fatalf("valid task/P00/false result rejected: %s %v", task, err)
		}
	}
	for _, test := range []struct {
		name  string
		input EventInput
		field string
	}{
		{"participant", EventInput{ParticipantCode: "P100", Task: "onboarding", Event: "start"}, "participant_code"},
		{"lowercase participant", EventInput{ParticipantCode: "p01", Task: "onboarding", Event: "start"}, "participant_code"},
		{"unicode digit", EventInput{ParticipantCode: "P０１", Task: "onboarding", Event: "start"}, "participant_code"},
		{"unknown task", EventInput{ParticipantCode: "P01", Task: "other", Event: "start"}, "task"},
		{"unknown event", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "finish"}, "event"},
		{"missing success", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "end"}, "success"},
		{"33 unicode code characters", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "start", ErrorCode: evalString(strings.Repeat("界", 33))}, "error_code"},
		{"invalid client date", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "start", ClientTS: evalString("2026-02-30T09:00:00Z")}, "client_ts"},
		{"offset outside RFC3339", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "start", ClientTS: evalString("2026-11-01T09:00:00+24:00")}, "client_ts"},
		{"single digit hour", EventInput{ParticipantCode: "P01", Task: "onboarding", Event: "start", ClientTS: evalString("2026-11-01T9:00:00Z")}, "client_ts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var validation *ValidationError
			if _, err := validateEvent(test.input); !errors.As(err, &validation) || validation.Fields[test.field] == "" {
				t.Fatalf("expected %s validation: %v", test.field, err)
			}
		})
	}
	base.ErrorCode = evalString(strings.Repeat("界", 32))
	base.ClientTS = evalString("2026-11-12T14:03:22.123456789123+08:00")
	validated, err := validateEvent(base)
	if err != nil || *validated.ErrorCode != *base.ErrorCode || *validated.ClientTS != "2026-11-12T14:03:22.123456789+08:00" {
		t.Fatalf("valid Unicode/RFC3339 normalization: %+v %v", validated, err)
	}
	for _, code := range []string{"", "a:b;c", "=SUM(1,2)", "\t@SUM(1,2)"} {
		base.ErrorCode = &code
		if _, err := validateEvent(base); err != nil {
			t.Fatalf("error code acquired an extra character restriction: %q %v", code, err)
		}
	}
}

func TestDecodeEventDiscardsPrivateFieldsAndRejectsInvalidTypes(t *testing.T) {
	var input EventInput
	if err := decodeEvent(strings.NewReader(`{"participant_code":"P01","task":"add_expense","event":"end","success":false,"amount_cents":650,"note":"private-note","user_id":99}`), &input); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil || strings.Contains(string(body), "private") || strings.Contains(string(body), "amount") || strings.Contains(string(body), "user_id") {
		t.Fatalf("unknown/private input fields survived decoding: %s %v", body, err)
	}
	for _, raw := range []string{"null", "[]", `{`, `{} {}`, `{"success":"true"}`, `{"error_code":1}`, `{"client_ts":false}`} {
		if err := decodeEvent(strings.NewReader(raw), &input); err == nil {
			t.Fatalf("accepted invalid event object: %s", raw)
		}
	}
}

func TestSummaryLatestAttemptsAndServerTimestamps(t *testing.T) {
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	event := func(id int64, participant, kind string, seconds float64, success *bool, code *string) Event {
		return Event{
			ID: id, ParticipantCode: participant, Task: "onboarding", Event: kind, Success: success,
			ErrorCode: code, ClientTS: evalString("1999-01-01T00:00:00Z"),
			ReceivedAt: base.Add(time.Duration(seconds * float64(time.Second))),
		}
	}
	events := []Event{
		event(1, "P00", "start", 0, nil, nil), event(2, "P00", "end", 2, evalBool(true), nil),
		event(3, "P01", "start", 0, nil, nil), event(4, "P01", "end", 1, evalBool(false), evalString("old")),
		event(5, "P01", "start", 3, nil, nil), event(6, "P01", "end", 7.5, evalBool(false), evalString("=SUM(1,2)")),
		event(7, "P02", "start", 0, nil, nil), event(8, "P02", "end", 1, evalBool(true), nil),
		event(9, "P02", "start", 8, nil, nil),
		event(10, "P03", "end", 9, evalBool(true), nil),
		event(11, "P04", "start", 0, nil, nil), event(12, "P04", "start", 5, nil, nil),
		event(13, "P04", "end", 25, evalBool(true), nil), event(14, "P04", "end", 26, evalBool(false), evalString("duplicate")),
	}
	// Input ordering must not affect pairing; equal timestamps use database id.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	got := summarize(events)
	if len(got) != 5 || got[0].Participants != 4 || got[0].SuccessRate == nil || *got[0].SuccessRate != 0.5 ||
		got[0].MedianSeconds == nil || *got[0].MedianSeconds != 4.5 || got[0].P90Seconds == nil || *got[0].P90Seconds != 20 ||
		!reflect.DeepEqual(got[0].TopErrors, map[string]int{"=SUM(1,2)": 1}) {
		t.Fatalf("latest attempt summary: %+v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Task != taskOrder[i] || got[i].Participants != 0 || got[i].SuccessRate != nil ||
			got[i].MedianSeconds != nil || got[i].P90Seconds != nil || got[i].TopErrors == nil {
			t.Fatalf("empty task must retain null metrics and an error map: %+v", got[i])
		}
	}
}

func TestSummaryDurationPercentiles(t *testing.T) {
	for _, sample := range []struct {
		durations   []float64
		median, p90 float64
	}{
		{[]float64{1.25, 3.75}, 2.5, 3.75},
		{[]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 5.5, 9},
		{[]float64{0}, 0, 0},
	} {
		var events []Event
		for i, duration := range sample.durations {
			start := time.Date(2026, 11, 12, 0, i, 0, 0, time.UTC)
			participant := fmt.Sprintf("P%02d", i)
			events = append(events,
				Event{ID: int64(i * 2), Task: "add_expense_by_chat", ParticipantCode: participant, Event: "start", ReceivedAt: start},
				Event{ID: int64(i*2 + 1), Task: "add_expense_by_chat", ParticipantCode: participant, Event: "end", Success: evalBool(true), ReceivedAt: start.Add(time.Duration(duration * float64(time.Second)))},
			)
		}
		got := summarize(events)[4]
		if got.MedianSeconds == nil || *got.MedianSeconds != sample.median || got.P90Seconds == nil || *got.P90Seconds != sample.p90 {
			t.Fatalf("percentiles for %v: %+v", sample.durations, got)
		}
	}
}

func TestCSVExportsPreventFormulaInjectionAndKeepQuotedFields(t *testing.T) {
	values := []string{"=1+1", "+SUM(A1:A2)", "-2+3", "@SUM(1,2)", "\ttext", "\rtext", "\ntext", "  =1", "\uFEFF=1", "'=1+1", "'literal"}
	rows := [][]string{{"error_code", "title"}}
	for _, value := range values {
		rows = append(rows, []string{value, `ordinary, "quoted" title`})
	}
	encoded, err := encodeCSV(rows)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := csv.NewReader(strings.NewReader(string(encoded))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range values {
		if decoded[i+1][0] != "'"+value || decoded[i+1][1] != `ordinary, "quoted" title` {
			t.Fatalf("unsafe CSV cell: %q", decoded[i+1])
		}
	}
	summary := summaryCSV([]TaskSummary{{Task: "onboarding", Participants: 1, TopErrors: map[string]int{"z:error": 2, "a;error": 1}}})
	if summary[1][5] != `a\;error:1;z\:error:2` || summary[1][2] != "" {
		t.Fatalf("unstable code ordering/null encoding: %v", summary)
	}
	// Both maps previously serialized as "a:1;b:2", losing the distinction
	// between a literal error code and two separate error frequencies.
	separate := summaryCSV([]TaskSummary{{TopErrors: map[string]int{"a": 1, "b": 2}}})[1][5]
	literal := summaryCSV([]TaskSummary{{TopErrors: map[string]int{"a:1;b": 2}}})[1][5]
	backslash := summaryCSV([]TaskSummary{{TopErrors: map[string]int{`a\:1\;b`: 2}}})[1][5]
	if separate != "a:1;b:2" || literal != `a\:1\;b:2` || backslash != `a\\\:1\\\;b:2` {
		t.Fatalf("ambiguous error frequencies: separate=%q literal=%q backslash=%q", separate, literal, backslash)
	}
	for _, view := range []string{"summary", "events"} {
		var cells []string
		for _, code := range []string{"=x", "'=x", "''=x"} {
			rows := summaryCSV([]TaskSummary{{TopErrors: map[string]int{code: 1}}})
			if view == "events" {
				rows = eventsCSV([]Event{{ErrorCode: &code}})
			}
			encoded, err := encodeCSV(rows)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := csv.NewReader(strings.NewReader(string(encoded))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			cell := decoded[1][5]
			want := code
			if view == "summary" {
				want += ":1"
			}
			if strings.TrimPrefix(cell, "'") != want {
				t.Fatalf("%s formula-safe cell cannot be reversed: code=%q cell=%q", view, code, cell)
			}
			cells = append(cells, cell)
		}
		if cells[0] == cells[1] || cells[1] == cells[2] || cells[0] == cells[2] {
			t.Fatalf("%s formula protection collided: %q", view, cells)
		}
	}
}

func TestHTTPAdminAndRedisFailClosed(t *testing.T) {
	router := gin.New()
	NewHTTPHandler(NewService(nil, nil)).RegisterRoutes(router.Group("/api"))
	t.Setenv("ADMIN_TOKEN", "")
	response := evalRequest(router, "GET", "/api/admin/eval/export", "", "test-only-admin", "192.0.2.10:1234")
	assertEvalError(t, response, 503, "unavailable")
	t.Setenv("ADMIN_TOKEN", "test-only-admin")
	for _, token := range []string{"", "wrong-token", "test-only-admi"} {
		assertEvalError(t, evalRequest(router, "GET", "/api/admin/eval/export", "", token, "192.0.2.10:1234"), 403, "forbidden")
	}
	assertEvalError(t, evalRequest(router, "GET", "/api/admin/eval/export?view=invalid", "", "test-only-admin", "192.0.2.10:1234"), 400, "bad_request")
	assertEvalError(t, evalRequest(router, "GET", "/api/admin/eval/export?format=invalid", "", "test-only-admin", "192.0.2.10:1234"), 400, "bad_request")
	assertEvalError(t, evalRequest(router, "GET", "/api/admin/eval/export?format=json&view=events", "", "test-only-admin", "192.0.2.10:1234"), 503, "unavailable")
	assertEvalError(t, evalRequest(router, "POST", "/api/eval/events", `{"participant_code":"P01","task":"onboarding","event":"start"}`, "", "192.0.2.10:1234"), 503, "unavailable")
}

func evalRequest(router http.Handler, method, path, body, admin, remote string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote
	request.Header.Set("Content-Type", "application/json")
	if admin != "" {
		request.Header.Set("X-Admin-Token", admin)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertEvalError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var result struct {
		Error auth.APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != status || result.Error.Code != code {
		t.Fatalf("want %d %s, got %d %s (decode %v)", status, code, response.Code, response.Body.String(), err)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("evaluation response was cacheable")
	}
}

func evalBool(value bool) *bool       { return &value }
func evalString(value string) *string { return &value }
