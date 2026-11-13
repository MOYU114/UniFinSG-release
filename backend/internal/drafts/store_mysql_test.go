package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"unichatgo/internal/finance"
	"unichatgo/internal/insight"
	"unichatgo/internal/storage"
	"unichatgo/migrations"
	"unichatgo/migrations/testdb"
)

// TestMySQLDraftLifecycle exercises the real agent_drafts schema and the
// agent transaction provenance added by migrations 000017/000018. It is
// skipped unless TEST_MYSQL_DRAFTS_DSN points at an empty exclusive
// unifinsg_test_* database.
func TestMySQLDraftLifecycle(t *testing.T) {
	db := testdb.Open(t, "TEST_MYSQL_DRAFTS_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if err := migrations.Apply(ctx, db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	newUser := func(name string) int64 {
		t.Helper()
		result, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at)
			VALUES (?, 'integration-test-only', ?)`, name, now)
		if err != nil {
			t.Fatalf("insert user: %v", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatalf("read user id: %v", err)
		}
		return id
	}
	userID := newUser("drafts_owner")
	otherID := newUser("drafts_other")
	sessionResult, err := db.ExecContext(ctx, `INSERT INTO sessions (user_id, title, kind, created_at, updated_at)
		VALUES (?, 'drafts', 'agent', ?, ?)`, userID, now, now)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	sessionID, _ := sessionResult.LastInsertId()

	fixed := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	financeService := finance.NewService(db, finance.WithClock(func() time.Time { return fixed }))
	service := NewService(NewMySQLStore(db), financeService, 30*time.Minute)

	createPayload := json.RawMessage(`{"items":[{"amount_cents":650,"spent_on":"2026-11-05",` +
		`"category":"food","merchant":"Toast Box","note":null}]}`)
	create := func(t *testing.T) *Draft {
		t.Helper()
		draft, err := service.Create(ctx, CreateInput{
			UserID: userID, SessionID: sessionID, TurnID: "turn-1", ToolCallID: "call-1",
			Kind: KindCreateTransactions, Payload: createPayload,
		})
		if err != nil {
			t.Fatalf("create draft: %v", err)
		}
		return draft
	}

	t.Run("approve writes an agent transaction and result", func(t *testing.T) {
		draft := create(t)
		decision, err := service.Decide(ctx, DecideInput{UserID: userID, DraftID: draft.ID, Action: ActionApprove})
		if err != nil {
			t.Fatalf("approve: %v", err)
		}
		var (
			source  string
			draftID sql.NullString
		)
		if err := db.QueryRowContext(ctx, `SELECT source, draft_id FROM transactions
			WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&source, &draftID); err != nil {
			t.Fatalf("read transaction: %v", err)
		}
		if source != finance.SourceAgent || !draftID.Valid || draftID.String != draft.ID {
			t.Fatalf("provenance = %s %v", source, draftID)
		}
		var status string
		var result []byte
		if err := db.QueryRowContext(ctx, `SELECT status, result FROM agent_drafts WHERE id = ?`,
			draft.ID).Scan(&status, &result); err != nil {
			t.Fatalf("read draft: %v", err)
		}
		if status != StatusApproved || len(result) == 0 {
			t.Fatalf("draft = %s %s", status, result)
		}
		var applied map[string]any
		if err := json.Unmarshal(decision.Applied, &applied); err != nil {
			t.Fatalf("decode applied: %v", err)
		}
		if ids, ok := applied["transaction_ids"].([]any); !ok || len(ids) != 1 {
			t.Fatalf("applied = %s", decision.Applied)
		}
	})

	t.Run("edited approve uses the edited payload", func(t *testing.T) {
		draft := create(t)
		edited := json.RawMessage(`{"items":[{"amount_cents":700,"spent_on":"2026-11-05",` +
			`"category":"transport","merchant":"MRT","note":null}]}`)
		if _, err := service.Decide(ctx, DecideInput{
			UserID: userID, DraftID: draft.ID, Action: ActionApprove, EditedPayload: edited,
		}); err != nil {
			t.Fatalf("approve: %v", err)
		}
		var amount int64
		var category string
		if err := db.QueryRowContext(ctx, `SELECT amount_cents, category FROM transactions
			WHERE user_id = ? ORDER BY id DESC LIMIT 1`, userID).Scan(&amount, &category); err != nil {
			t.Fatalf("read transaction: %v", err)
		}
		if amount != 700 || category != string(insight.CategoryTransport) {
			t.Fatalf("edited values not applied: %d %s", amount, category)
		}
	})

	t.Run("reject never writes the ledger", func(t *testing.T) {
		var before int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = ?`, userID).Scan(&before); err != nil {
			t.Fatalf("count: %v", err)
		}
		draft := create(t)
		decision, err := service.Decide(ctx, DecideInput{
			UserID: userID, DraftID: draft.ID, Action: ActionReject, Reason: "Wrong amount",
		})
		if err != nil {
			t.Fatalf("reject: %v", err)
		}
		var after int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = ?`, userID).Scan(&after); err != nil {
			t.Fatalf("count: %v", err)
		}
		if after != before {
			t.Fatalf("reject wrote %d transactions", after-before)
		}
		if !containsJSON(decision.ToolResult, "Wrong amount") {
			t.Fatalf("tool result = %s", decision.ToolResult)
		}
	})

	t.Run("concurrent decisions write once", func(t *testing.T) {
		draft := create(t)
		var before int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = ?`, userID).Scan(&before); err != nil {
			t.Fatalf("count: %v", err)
		}
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := service.Decide(context.Background(), DecideInput{
					UserID: userID, DraftID: draft.ID, Action: ActionApprove,
				})
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		var ok, notPending int
		for err := range results {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrNotPending):
				notPending++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		var after int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE user_id = ?`, userID).Scan(&after); err != nil {
			t.Fatalf("count: %v", err)
		}
		if ok != 1 || notPending != 1 || after-before != 1 {
			t.Fatalf("ok=%d notPending=%d wrote=%d", ok, notPending, after-before)
		}
	})

	t.Run("expired and foreign drafts are refused", func(t *testing.T) {
		draft := create(t)
		if _, err := db.ExecContext(ctx, `UPDATE agent_drafts
			SET expires_at = DATE_SUB(NOW(6), INTERVAL 1 MINUTE) WHERE id = ?`, draft.ID); err != nil {
			t.Fatalf("expire: %v", err)
		}
		if _, err := service.Decide(ctx, DecideInput{UserID: userID, DraftID: draft.ID, Action: ActionApprove}); !errors.Is(err, ErrNotPending) {
			t.Fatalf("expired decision: %v", err)
		}
		foreign := create(t)
		if _, err := service.Decide(ctx, DecideInput{UserID: otherID, DraftID: foreign.ID, Action: ActionApprove}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign decision: %v", err)
		}
	})

	t.Run("update and delete execute in one transaction", func(t *testing.T) {
		record, err := financeService.CreateTransaction(ctx, userID, finance.TransactionInput{
			AmountCents: 500, SpentOn: "2026-11-04", Category: insight.CategoryFood,
		})
		if err != nil {
			t.Fatalf("seed transaction: %v", err)
		}
		before, _ := json.Marshal(record)
		update, err := service.Create(ctx, CreateInput{
			UserID: userID, SessionID: sessionID, TurnID: "turn-2", ToolCallID: "call-2",
			Kind: KindUpdateTransaction, Before: before,
			Payload: json.RawMessage(`{"transaction_id":` + itoa(record.ID) + `,"changes":{"amount_cents":800}}`),
		})
		if err != nil {
			t.Fatalf("create update draft: %v", err)
		}
		if _, err := service.Decide(ctx, DecideInput{UserID: userID, DraftID: update.ID, Action: ActionApprove}); err != nil {
			t.Fatalf("approve update: %v", err)
		}
		var amount int64
		var source string
		if err := db.QueryRowContext(ctx, `SELECT amount_cents, source FROM transactions WHERE id = ?`,
			record.ID).Scan(&amount, &source); err != nil {
			t.Fatalf("read updated: %v", err)
		}
		if amount != 800 || source != finance.SourceAgent {
			t.Fatalf("update = %d %s", amount, source)
		}

		deleteDraft, err := service.Create(ctx, CreateInput{
			UserID: userID, SessionID: sessionID, TurnID: "turn-3", ToolCallID: "call-3",
			Kind: KindDeleteTransaction, Before: before,
			Payload: json.RawMessage(`{"transaction_id":` + itoa(record.ID) + `}`),
		})
		if err != nil {
			t.Fatalf("create delete draft: %v", err)
		}
		if _, err := service.Decide(ctx, DecideInput{UserID: userID, DraftID: deleteDraft.ID, Action: ActionApprove}); err != nil {
			t.Fatalf("approve delete: %v", err)
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE id = ?`, record.ID).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		if count != 0 {
			t.Fatal("delete was not applied")
		}
	})

	t.Run("budget update executes and records agent provenance", func(t *testing.T) {
		total := int64(60000)
		warn := 8000
		categories := make([]finance.CategoryBudgetInput, 0, 6)
		for _, category := range []insight.Category{
			insight.CategoryFood, insight.CategoryTransport, insight.CategoryHousingTuition,
			insight.CategoryDailyShopping, insight.CategoryStudyDigital, insight.CategoryEntertainmentOther,
		} {
			amount := int64(10000)
			categories = append(categories, finance.CategoryBudgetInput{Category: category, BudgetCents: &amount})
		}
		if _, err := financeService.SaveBudget(ctx, userID, "2026-11", finance.BudgetInput{
			TotalCents: &total, WarnBP: &warn, Categories: categories,
		}); err != nil {
			t.Fatalf("seed budget: %v", err)
		}
		current, err := financeService.ReadBudget(ctx, userID, "2026-11")
		if err != nil {
			t.Fatalf("read budget: %v", err)
		}
		before, _ := json.Marshal(current)
		newTotal := int64(65000)
		newFood := int64(25000)
		payload, _ := json.Marshal(map[string]any{
			"month": "2026-11", "total_cents": newTotal,
			"categories": []finance.CategoryBudgetInput{{Category: insight.CategoryFood, BudgetCents: &newFood}},
		})
		draft, err := service.Create(ctx, CreateInput{
			UserID: userID, SessionID: sessionID, TurnID: "turn-4", ToolCallID: "call-4",
			Kind: KindUpdateBudget, Before: before, Payload: payload,
		})
		if err != nil {
			t.Fatalf("create budget draft: %v", err)
		}
		if _, err := service.Decide(ctx, DecideInput{UserID: userID, DraftID: draft.ID, Action: ActionApprove}); err != nil {
			t.Fatalf("approve budget: %v", err)
		}
		var storedTotal int64
		var source string
		if err := db.QueryRowContext(ctx, `SELECT total_cents, source FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, userID).Scan(&storedTotal, &source); err != nil {
			t.Fatalf("read budget: %v", err)
		}
		if storedTotal != 65000 || source != finance.SourceAgent {
			t.Fatalf("budget = %d %s", storedTotal, source)
		}
	})
}

func containsJSON(raw json.RawMessage, needle string) bool {
	return strings.Contains(string(raw), needle)
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
