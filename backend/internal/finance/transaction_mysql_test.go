package finance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/insight"
)

// TEST_MYSQL_TRANSACTIONS_DSN selects this suite's exclusive empty database.
// financeMySQLDatabase applies the real schema and cleans only test-owned tables.
func TestMySQLTransactionIntegration(t *testing.T) {
	db := financeMySQLDatabase(t, "TEST_MYSQL_TRANSACTIONS_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Date(2026, 10, 31, 16, 0, 0, 123456000, time.UTC)
	service := NewService(db, WithClock(func() time.Time { return now }))
	authService := auth.NewService(db, nil, time.Hour)
	router := gin.New()
	NewHTTPHandler(service, authService).RegisterTransactionRoutes(router.Group("/api"))
	newUser := func(name string) (int64, string) {
		t.Helper()
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
			VALUES (?, 'integration-test-only', ?)`, name, now.UTC())
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		token, err := authService.IssueToken(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return id, token
	}
	readRecord := func(response *httptest.ResponseRecorder, status int) Transaction {
		t.Helper()
		var record Transaction
		if response.Code != status {
			t.Fatalf("record response HTTP %d, want %d: %s", response.Code, status, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		return record
	}

	t.Run("CRUD HTTP contract patch semantics and ownership", func(t *testing.T) {
		alice, token := newUser("transactions_alice")
		bob, bobToken := newUser("transactions_bob")
		path := fmt.Sprintf("/api/users/%d/transactions", alice)
		bobPath := fmt.Sprintf("/api/users/%d/transactions", bob)
		input := map[string]any{
			"amount_cents": int64(650), "spent_on": "2026-11-01", "category": "food",
			"merchant": strings.Repeat("店", 64), "note": strings.Repeat("餐", 200), "user_id": bob,
		}
		assertFinanceError(t, financeRequest(t, router, "POST", path, input, "", false), 401, "unauthenticated")
		assertFinanceError(t, financeRequest(t, router, "POST", path, input, token, true), 403, "csrf_invalid")
		record := readRecord(financeRequest(t, router, "POST", path, input, token, false), 201)
		if record.ID <= 0 || record.AmountCents != 650 || record.SpentOn != "2026-11-01" ||
			record.Merchant == nil || record.Note == nil || *record.Merchant != input["merchant"] || *record.Note != input["note"] ||
			record.CreatedAt.Format(time.RFC3339Nano) != "2026-11-01T00:00:00.123456+08:00" {
			t.Fatalf("created record: %+v", record)
		}
		var owner int64
		if err := db.QueryRowContext(ctx, `SELECT user_id FROM transactions WHERE user_id = ? AND id = ?`, alice, record.ID).Scan(&owner); err != nil || owner != alice {
			t.Fatalf("request-body identity affected ownership: owner=%d err=%v", owner, err)
		}
		var empty TransactionPage
		decodeFinanceResponse(t, financeRequest(t, router, "GET", bobPath, nil, bobToken, false), &empty)
		if empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 || empty.Page != 1 || empty.PageSize != 20 {
			t.Fatalf("empty/default listing: %+v", empty)
		}

		bobRecord := readRecord(financeRequest(t, router, "POST", bobPath, input, bobToken, false), 201)
		for _, test := range []struct {
			method string
			path   string
			body   any
		}{
			{"GET", bobPath, nil}, {"POST", bobPath, input},
			{"PATCH", fmt.Sprintf("%s/%d", bobPath, bobRecord.ID), map[string]any{"note": nil}},
			{"DELETE", fmt.Sprintf("%s/%d", bobPath, bobRecord.ID), nil},
		} {
			assertFinanceError(t, financeRequest(t, router, test.method, test.path, test.body, token, false), 403, "forbidden")
		}
		for _, method := range []string{"PATCH", "DELETE"} {
			assertFinanceError(t, financeRequest(t, router, method, fmt.Sprintf("%s/%d", path, bobRecord.ID),
				map[string]any{"note": "forged"}, token, false), 404, "not_found")
		}

		itemPath := fmt.Sprintf("%s/%d", path, record.ID)
		now = now.Add(time.Minute)
		unchanged := readRecord(financeRequest(t, router, "PATCH", itemPath, map[string]any{}, token, false), 200)
		if !reflect.DeepEqual(record, unchanged) {
			t.Fatalf("empty patch changed a record: before=%+v after=%+v", record, unchanged)
		}
		for _, field := range []string{"amount_cents", "spent_on", "category"} {
			assertFinanceError(t, financeRequest(t, router, "PATCH", itemPath, map[string]any{field: nil}, token, false), 400, "validation_failed")
		}
		cleared := readRecord(financeRequest(t, router, "PATCH", itemPath,
			map[string]any{"note": nil, "user_id": bob}, token, false), 200)
		if cleared.Note != nil || cleared.Merchant == nil || *cleared.Merchant != *record.Merchant ||
			cleared.AmountCents != record.AmountCents || !cleared.CreatedAt.Equal(record.CreatedAt) || !cleared.UpdatedAt.After(record.UpdatedAt) {
			t.Fatalf("partial null patch: %+v", cleared)
		}
		moved := readRecord(financeRequest(t, router, "PATCH", itemPath,
			map[string]any{"amount_cents": 100000000, "spent_on": "1999-02-28", "category": "transport", "merchant": ""}, token, false), 200)
		if moved.AmountCents != 100000000 || moved.SpentOn != "1999-02-28" || moved.Category != insight.CategoryTransport ||
			moved.Merchant == nil || *moved.Merchant != "" || moved.Note != nil {
			t.Fatalf("patch moved historical record: %+v", moved)
		}
		var page TransactionPage
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path, nil, token, false), &page)
		if page.Total != 0 {
			t.Fatalf("moved record remained in default Singapore month: %+v", page)
		}
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?month=1999-02&category=transport", nil, token, false), &page)
		if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != record.ID {
			t.Fatalf("historical/category listing: %+v", page)
		}
		for _, patch := range []map[string]any{
			{"amount_cents": 0}, {"amount_cents": 100000001}, {"spent_on": "2026-11-02"},
			{"spent_on": "2026-02-29"}, {"category": "unknown"}, {"merchant": strings.Repeat("x", 65)}, {"note": strings.Repeat("x", 201)},
		} {
			assertFinanceError(t, financeRequest(t, router, "PATCH", itemPath, patch, token, false), 400, "validation_failed")
		}
		again := readRecord(financeRequest(t, router, "PATCH", itemPath, map[string]any{}, token, false), 200)
		if !reflect.DeepEqual(moved, again) {
			t.Fatalf("invalid patches partially persisted: before=%+v after=%+v", moved, again)
		}
		for _, raw := range []string{`null`, `[]`, `{} {}`, `{"amount_cents":1.5}`, `{"note":123}`, `{"spent_on":123}`} {
			request := httptest.NewRequest("PATCH", itemPath, strings.NewReader(raw))
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertFinanceError(t, response, 400, "bad_request")
		}

		// Valid Cookie mutation requires the matching double-submit CSRF token.
		request := httptest.NewRequest("DELETE", itemPath, nil)
		request.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		request.AddCookie(&http.Cookie{Name: "csrf_token", Value: "transactions-csrf"})
		request.Header.Set("X-CSRF-Token", "transactions-csrf")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 204 || response.Body.Len() != 0 {
			t.Fatalf("delete response: %d %q", response.Code, response.Body.String())
		}
		assertFinanceError(t, financeRequest(t, router, "DELETE", itemPath, nil, token, false), 404, "not_found")
		assertFinanceError(t, financeRequest(t, router, "PATCH", itemPath, map[string]any{}, token, false), 404, "not_found")
		decodeFinanceResponse(t, financeRequest(t, router, "GET", bobPath, nil, bobToken, false), &page)
		if page.Total != 1 || page.Items[0].ID != bobRecord.ID {
			t.Fatalf("another user's record was altered: %+v", page)
		}
	})

	t.Run("pagination ordering date range and invalid queries", func(t *testing.T) {
		id, token := newUser("transactions_pagination")
		path := fmt.Sprintf("/api/users/%d/transactions", id)
		create := func(date string, category insight.Category) int64 {
			t.Helper()
			record, err := service.CreateTransaction(ctx, id, TransactionInput{AmountCents: 1, SpentOn: date, Category: category})
			if err != nil {
				t.Fatal(err)
			}
			if record.Merchant != nil || record.Note != nil {
				t.Fatalf("omitted text was not null: %+v", record)
			}
			return record.ID
		}
		earliest := create("2026-10-01", insight.CategoryFood)
		sameTime1 := create("2026-10-31", insight.CategoryFood)
		sameTime2 := create("2026-10-31", insight.CategoryTransport)
		now = now.Add(time.Second)
		later := create("2026-10-31", insight.CategoryFood)
		create("2026-09-30", insight.CategoryFood)
		create("2026-11-01", insight.CategoryFood)
		// Arbitrarily old valid dates should not acquire an undocumented
		// recent-history restriction in the database implementation.
		ancient := create("0001-01-01", insight.CategoryStudyDigital)
		oldPage, err := service.ListTransactions(ctx, id, TransactionListOptions{Month: "0001-01"})
		if err != nil || oldPage.Total != 1 || oldPage.Items[0].ID != ancient {
			t.Fatalf("oldest supported calendar year: page=%+v err=%v", oldPage, err)
		}
		var page TransactionPage
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?month=2026-10&page_size=2", nil, token, false), &page)
		if page.Total != 4 || page.Page != 1 || page.PageSize != 2 || len(page.Items) != 2 ||
			page.Items[0].ID != later || page.Items[1].ID != sameTime2 {
			t.Fatalf("first page order: %+v", page)
		}
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?month=2026-10&page_size=2&page=2", nil, token, false), &page)
		if page.Total != 4 || len(page.Items) != 2 || page.Items[0].ID != sameTime1 || page.Items[1].ID != earliest {
			t.Fatalf("second page order: %+v", page)
		}
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?month=2026-10&category=food&page_size=100", nil, token, false), &page)
		if page.Total != 3 || len(page.Items) != 3 {
			t.Fatalf("category total/filter: %+v", page)
		}
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?month=2026-10&page=10", nil, token, false), &page)
		if page.Total != 4 || page.Items == nil || len(page.Items) != 0 {
			t.Fatalf("empty later page lost total/array: %+v", page)
		}
		for _, query := range []string{
			"month=", "month=2026-1", "month=2026-13", "page=0", "page=-1", "page=1.5",
			"page=", "page=999999999999999999999999999999999", "page=1&page=2", "page_size=0",
		} {
			assertFinanceError(t, financeRequest(t, router, "GET", path+"?"+query, nil, token, false), 400, "bad_request")
		}
		for _, query := range []string{"page_size=101", "category=", "category=unknown", "page=9223372036854775807&page_size=100"} {
			assertFinanceError(t, financeRequest(t, router, "GET", path+"?"+query, nil, token, false), 400, "validation_failed")
		}
		for _, invalidID := range []string{"0", "-1", "abc", "9999999999999999999999999999"} {
			assertFinanceError(t, financeRequest(t, router, "DELETE", path+"/"+invalidID, nil, token, false), 400, "bad_request")
		}
	})

	t.Run("count and items use a single concurrent snapshot", func(t *testing.T) {
		id, _ := newUser("transactions_concurrent")
		fixed := now
		concurrent := NewService(db, WithClock(func() time.Time { return fixed }))
		for i := 0; i < 20; i++ {
			if _, err := concurrent.CreateTransaction(ctx, id, TransactionInput{AmountCents: 1, SpentOn: "2026-11-01", Category: insight.CategoryFood}); err != nil {
				t.Fatal(err)
			}
		}
		start := make(chan struct{})
		failures := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 40; i++ {
				record, err := concurrent.CreateTransaction(ctx, id, TransactionInput{AmountCents: 1, SpentOn: "2026-11-01", Category: insight.CategoryTransport})
				if err != nil {
					failures <- err
					return
				}
				if err := concurrent.DeleteTransaction(ctx, id, record.ID); err != nil {
					failures <- err
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 40; i++ {
				page, err := concurrent.ListTransactions(ctx, id, TransactionListOptions{Month: "2026-11", PageSize: 100})
				if err != nil {
					failures <- err
					return
				}
				if page.Total != int64(len(page.Items)) || page.Total < 20 || page.Total > 21 {
					failures <- fmt.Errorf("count/items snapshots differ: total=%d items=%d", page.Total, len(page.Items))
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		close(failures)
		for err := range failures {
			t.Error(err)
		}
	})
}
