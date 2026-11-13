package eval

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

func TestMySQLEvaluationIntegration(t *testing.T) {
	cache := evalRedis(t)
	db := testdb.Open(t, "TEST_MYSQL_EVAL_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	service := NewService(db, cache, WithClock(func() time.Time { return now }))
	router := gin.New()
	NewHTTPHandler(service).RegisterRoutes(router.Group("/api"))
	admin := "evaluation-integration-test-only"
	t.Setenv("ADMIN_TOKEN", admin)
	remote := net.JoinHostPort(evalTestIP(t, cache).String(), "1234")
	post := func(body map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := evalRequest(router, "POST", "/api/eval/events", string(raw), "", remote)
		if response.Code != 202 || response.Body.String() != `{"accepted":true}` || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("event was not accepted: %d %s", response.Code, response.Body.String())
		}
	}
	exportJSON := func(view string, target any) {
		t.Helper()
		response := evalRequest(router, "GET", "/api/admin/eval/export?format=json&view="+view, "", admin, remote)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), "[") ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("invalid JSON export: %d %s", response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("five tasks anonymous acceptance and server measured summaries", func(t *testing.T) {
		for i, task := range taskOrder {
			now = time.Date(2026, 11, 12, 9, i, 0, 0, time.UTC)
			post(map[string]any{
				"participant_code": "P00", "task": task, "event": "start",
				"client_ts": "2999-01-01T00:00:00Z", "amount_cents": 123456, "note": "private-note-must-disappear",
			})
			now = now.Add(20*time.Second + time.Duration(i)*250*time.Millisecond)
			body := map[string]any{
				"participant_code": "P00", "task": task, "event": "end", "success": i != 1,
				"client_ts": "1999-01-01T00:00:00Z", "amount": 123.45, "user_id": 123,
			}
			if i == 1 {
				body["error_code"] = "=SUM(1,2)"
			}
			post(body)
		}
		// Unmatched and duplicate ends remain in raw export but cannot count
		// as participants, success or a second completion in the summary.
		post(map[string]any{"participant_code": "P99", "task": "onboarding", "event": "end", "success": true})
		post(map[string]any{"participant_code": "P00", "task": "onboarding", "event": "end", "success": false, "error_code": "duplicate"})
		var summary []TaskSummary
		exportJSON("summary", &summary)
		if len(summary) != 5 {
			t.Fatalf("want five task summaries: %+v", summary)
		}
		for i, item := range summary {
			rate := float64(1)
			if i == 1 {
				rate = 0
			}
			if item.Task != taskOrder[i] || item.Participants != 1 || item.SuccessRate == nil || *item.SuccessRate != rate ||
				item.MedianSeconds == nil || *item.MedianSeconds != 20+float64(i)*0.25 ||
				item.P90Seconds == nil || *item.P90Seconds != *item.MedianSeconds {
				t.Fatalf("unexpected summary: %+v", item)
			}
		}
		if !reflect.DeepEqual(summary[1].TopErrors, map[string]int{"=SUM(1,2)": 1}) || len(summary[0].TopErrors) != 0 {
			t.Fatalf("incorrect latest-attempt errors: %+v", summary)
		}
		response := evalRequest(router, "GET", "/api/admin/eval/export", "", admin, remote)
		rows, err := csv.NewReader(response.Body).ReadAll()
		if err != nil || response.Code != 200 || len(rows) != 6 || rows[0][0] != "task" ||
			rows[2][5] != "'=SUM(1,2):1" || rows[2][3] != "20.25" {
			t.Fatalf("summary CSV: %+v %v status=%d", rows, err, response.Code)
		}
	})

	t.Run("raw events contain only whitelisted fields", func(t *testing.T) {
		var events []Event
		exportJSON("events", &events)
		if len(events) != 12 || events[0].ParticipantCode != "P00" || events[0].ClientTS == nil ||
			*events[0].ClientTS != "2999-01-01T00:00:00Z" || events[0].ReceivedAt.Year() != 2026 {
			t.Fatalf("event timestamps/row count changed: %+v", events)
		}
		response := evalRequest(router, "GET", "/api/admin/eval/export?format=json&view=events", "", admin, remote)
		for _, private := range []string{"amount", "note", "user_id", "private-note-must-disappear"} {
			if strings.Contains(response.Body.String(), private) {
				t.Fatalf("private field leaked in export: %s", private)
			}
		}
		var fields []map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil || len(fields[0]) != 8 {
			t.Fatalf("event schema drift: %v %v", fields, err)
		}
		var columnCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = 'eval_events'`).Scan(&columnCount); err != nil || columnCount != 8 {
			t.Fatalf("unexpected storage columns: %d %v", columnCount, err)
		}
		csvResponse := evalRequest(router, "GET", "/api/admin/eval/export?view=events", "", admin, remote)
		rows, err := csv.NewReader(csvResponse.Body).ReadAll()
		if err != nil || len(rows) != 13 || rows[0][7] != "received_at" {
			t.Fatalf("events CSV rows: %d %v", len(rows), err)
		}
		found := false
		for _, row := range rows[1:] {
			if row[5] == "'=SUM(1,2)" {
				found = true
			}
		}
		if !found {
			t.Fatal("events CSV omitted or failed to escape the error code")
		}
	})

	t.Run("invalid events do not create rows", func(t *testing.T) {
		for _, body := range []string{
			`{"participant_code":"P1","task":"onboarding","event":"start"}`,
			`{"participant_code":"P01","task":"unknown","event":"start"}`,
			`{"participant_code":"P01","task":"onboarding","event":"end"}`,
			`{"participant_code":"P01","task":"onboarding","event":"start","client_ts":"yesterday"}`,
		} {
			assertEvalError(t, evalRequest(router, "POST", "/api/eval/events", body, "", remote), 400, "validation_failed")
		}
		for _, body := range []string{`null`, `{} {}`, `{"success":1}`} {
			assertEvalError(t, evalRequest(router, "POST", "/api/eval/events", body, "", remote), 400, "bad_request")
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM eval_events`).Scan(&count); err != nil || count != 12 {
			t.Fatalf("invalid event was persisted: %d %v", count, err)
		}
	})
}
