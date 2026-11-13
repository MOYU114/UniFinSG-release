package finance

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"unichatgo/internal/auth"
	"unichatgo/internal/insight"
)

// This suite owns an initially empty database supplied independently from the
// other MySQL suites so it can run without sharing their migration lifecycle.
func TestMySQLSnapshotIntegration(t *testing.T) {
	db := financeMySQLDatabase(t, "TEST_MYSQL_SNAPSHOT_DSN")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, singapore)
	service := NewService(db, WithClock(func() time.Time { return now }))
	authService := auth.NewService(db, nil, time.Hour)
	router := gin.New()
	NewHTTPHandler(service, authService).RegisterDashboardRoutes(router.Group("/api"))
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
	saveProfile := func(id int64) {
		t.Helper()
		if _, err := service.SaveProfile(ctx, id, ProfileInput{
			MonthlyDisposableCents: financeInt64(1000), Housing: "on_campus",
		}); err != nil {
			t.Fatal(err)
		}
	}
	record := func(id int64, amount int64, date string, category insight.Category) *Transaction {
		t.Helper()
		value, err := service.CreateTransaction(ctx, id, TransactionInput{
			AmountCents: amount, SpentOn: date, Category: category,
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	snapshot := func(id int64, month string) *insight.Snapshot {
		t.Helper()
		value, err := service.Snapshot(ctx, id, month)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}

	t.Run("dashboard reads exact current user month and reflects CRUD", func(t *testing.T) {
		alice, token := newUser("snapshot_alice")
		bob, _ := newUser("snapshot_bob")
		saveProfile(alice)
		if _, err := service.SaveBudget(ctx, alice, "2026-11", financeBudgetInput(1000, 9000, [6]int64{500, 500})); err != nil {
			t.Fatal(err)
		}
		first := record(alice, 100, "2026-11-01", insight.CategoryFood)
		second := record(alice, 200, "2026-11-02", insight.CategoryFood)
		record(alice, 300, "2026-11-03", insight.CategoryTransport)
		record(alice, 5000, "2026-10-31", insight.CategoryFood)
		record(bob, 8000, "2026-11-01", insight.CategoryFood)
		got := snapshot(alice, "")
		if got.Month != "2026-11" || got.TotalBudgetCents != 1000 || got.TotalSpentCents != 600 ||
			got.TransactionCount != 3 || got.UsageBP == nil || *got.UsageBP != 6000 || got.RemainingCents != 400 ||
			got.ProjectedSpentCents == nil || *got.ProjectedSpentCents != 1500 || got.TopCategory == nil ||
			*got.TopCategory != insight.CategoryFood || len(got.Missing) != 0 {
			t.Fatalf("snapshot mixed users/months or calculated incorrectly: %+v", got)
		}
		path := fmt.Sprintf("/api/users/%d/dashboard", alice)
		assertFinanceError(t, financeRequest(t, router, "GET", path, nil, "", false), 401, "unauthenticated")
		assertFinanceError(t, financeRequest(t, router, "GET", fmt.Sprintf("/api/users/%d/dashboard", bob), nil, token, false), 403, "forbidden")
		assertFinanceError(t, financeRequest(t, router, "GET", path+"?month=2026-1", nil, token, false), 400, "bad_request")
		var dashboard insight.Snapshot
		decodeFinanceResponse(t, financeRequest(t, router, "GET", path+"?user_id="+fmt.Sprint(bob), nil, token, true), &dashboard)
		if dashboard.Hash != got.Hash || dashboard.TotalSpentCents != got.TotalSpentCents {
			t.Fatalf("dashboard differed from provider: %+v %+v", dashboard, got)
		}
		oldHash := got.Hash
		if _, err := service.UpdateTransaction(ctx, alice, first.ID, TransactionPatch{
			AmountCents: TransactionPatchField[int64]{Set: true, Value: financeInt64(400)},
		}); err != nil {
			t.Fatal(err)
		}
		got = snapshot(alice, "2026-11")
		if got.TotalSpentCents != 900 || got.Categories[0].SpentCents != 600 ||
			got.Categories[0].Status != insight.StatusOver || got.Hash == oldHash {
			t.Fatalf("updated amount was not reflected: %+v", got)
		}
		past := "2026-10-30"
		if _, err := service.UpdateTransaction(ctx, alice, second.ID, TransactionPatch{
			SpentOn: TransactionPatchField[string]{Set: true, Value: &past},
		}); err != nil {
			t.Fatal(err)
		}
		got = snapshot(alice, "2026-11")
		if got.TotalSpentCents != 700 || got.TransactionCount != 2 || got.ProjectedSpentCents != nil {
			t.Fatalf("moved transaction stayed in month: %+v", got)
		}
		if err := service.DeleteTransaction(ctx, alice, first.ID); err != nil {
			t.Fatal(err)
		}
		got = snapshot(alice, "2026-11")
		if got.TotalSpentCents != 300 || got.TransactionCount != 1 || got.TopCategory == nil || *got.TopCategory != insight.CategoryTransport {
			t.Fatalf("deleted transaction remained in snapshot: %+v", got)
		}
		pastMonth := snapshot(alice, "2026-10")
		if pastMonth.TotalSpentCents != 5200 || pastMonth.DaysElapsed != 31 || pastMonth.ProjectedSpentCents == nil ||
			*pastMonth.ProjectedSpentCents != 5200 || !reflect.DeepEqual(pastMonth.Missing, []string{"budget"}) {
			t.Fatalf("past month snapshot: %+v", pastMonth)
		}
		future := snapshot(alice, "2026-12")
		if future.TotalSpentCents != 0 || future.DaysElapsed != 0 || future.ProjectedSpentCents != nil {
			t.Fatalf("future month snapshot: %+v", future)
		}
	})

	t.Run("only current snapshot with a profile initializes a budget", func(t *testing.T) {
		id, _ := newUser("snapshot_empty")
		got := snapshot(id, "2026-11")
		if !reflect.DeepEqual(got.Missing, []string{"profile", "budget", "transactions"}) ||
			got.UsageBP != nil || got.TopCategory != nil || len(got.Categories) != 6 {
			t.Fatalf("empty snapshot: %+v", got)
		}
		saveProfile(id)
		for _, month := range []string{"2026-10", "2026-12"} {
			got = snapshot(id, month)
			if !reflect.DeepEqual(got.Missing, []string{"budget", "transactions"}) {
				t.Fatalf("historical/future budget was generated: %+v", got)
			}
		}
		var budgets int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets WHERE user_id = ?`, id).Scan(&budgets); err != nil || budgets != 0 {
			t.Fatalf("historical/future snapshot generated a budget: %d %v", budgets, err)
		}
		got = snapshot(id, "2026-11")
		if !reflect.DeepEqual(got.Missing, []string{"transactions"}) || got.TotalBudgetCents != 1000 ||
			got.RemainingCents != 1000 || got.UsageBP == nil || *got.UsageBP != 0 {
			t.Fatalf("current snapshot did not initialize the profile default: %+v", got)
		}
		var source string
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(source) FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, id).Scan(&budgets, &source); err != nil ||
			budgets != 1 || source != "profile_default" {
			t.Fatalf("current default was not persisted once: count=%d source=%s err=%v", budgets, source, err)
		}
		if again := snapshot(id, "2026-11"); !reflect.DeepEqual(got, again) {
			t.Fatalf("initialization result differed from saved reread: first=%+v second=%+v", got, again)
		}
	})

	t.Run("rollover copies the previous budget and hashes the generated exact numbers", func(t *testing.T) {
		id, _ := newUser("snapshot_generated_copy")
		saveProfile(id)
		if _, err := service.SaveBudget(ctx, id, "2026-10", financeBudgetInput(1200, 9000, [6]int64{600, 600})); err != nil {
			t.Fatal(err)
		}
		spending := record(id, 540, "2026-11-01", insight.CategoryFood)
		got := snapshot(id, "")
		if got.TotalBudgetCents != 1200 || got.TotalSpentCents != 540 || got.RemainingCents != 660 ||
			got.UsageBP == nil || *got.UsageBP != 4500 || got.Categories[0].BudgetCents != 600 ||
			got.Categories[0].UsageBP == nil || *got.Categories[0].UsageBP != 9000 ||
			got.Categories[0].Status != insight.StatusWarn || len(got.Missing) != 0 {
			t.Fatalf("copied budget lost exact numbers or threshold: %+v", got)
		}
		var source string
		if err := db.QueryRowContext(ctx, `SELECT source FROM budgets WHERE user_id = ? AND month = '2026-11'`,
			id).Scan(&source); err != nil || source != "copied_from_2026-10" {
			t.Fatalf("copy source changed: %s %v", source, err)
		}
		if again := snapshot(id, "2026-11"); !reflect.DeepEqual(got, again) {
			t.Fatalf("hash or numbers changed without time/data changes: first=%+v second=%+v", got, again)
		}
		if _, err := service.UpdateTransaction(ctx, id, spending.ID, TransactionPatch{
			AmountCents: TransactionPatchField[int64]{Set: true, Value: financeInt64(541)},
		}); err != nil {
			t.Fatal(err)
		}
		changed := snapshot(id, "2026-11")
		if changed.Hash == got.Hash || changed.TotalSpentCents != 541 || changed.RemainingCents != 659 {
			t.Fatalf("changed data was not reflected in hash/numbers: %+v", changed)
		}
		later := NewService(db, WithClock(func() time.Time { return now.Add(time.Second) }))
		timed, err := later.Snapshot(ctx, id, "2026-11")
		if err != nil {
			t.Fatal(err)
		}
		if timed.Hash == changed.Hash || timed.TotalBudgetCents != changed.TotalBudgetCents ||
			timed.TotalSpentCents != changed.TotalSpentCents || timed.RemainingCents != changed.RemainingCents ||
			!reflect.DeepEqual(timed.Categories, changed.Categories) {
			t.Fatalf("as_of must change hash while preserving unchanged financial facts: %+v %+v", changed, timed)
		}
	})

	t.Run("snapshot without profile cannot copy a previous budget", func(t *testing.T) {
		id, _ := newUser("snapshot_prior_no_profile")
		if _, err := service.SaveBudget(ctx, id, "2026-10", financeBudgetInput(600, 8000, [6]int64{600})); err != nil {
			t.Fatal(err)
		}
		got := snapshot(id, "")
		if !reflect.DeepEqual(got.Missing, []string{"profile", "budget", "transactions"}) || got.TotalBudgetCents != 0 {
			t.Fatalf("snapshot implicitly onboarded a profileless user: %+v", got)
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, id).Scan(&count); err != nil || count != 0 {
			t.Fatalf("profileless snapshot wrote current budget: %d %v", count, err)
		}
	})

	t.Run("budget initialization and reread use one time captured across rollover", func(t *testing.T) {
		id, _ := newUser("snapshot_single_clock_rollover")
		saveProfile(id)
		before := time.Date(2026, 10, 31, 15, 59, 59, 0, time.UTC)
		calls := 0
		clock := NewService(db, WithClock(func() time.Time {
			calls++
			if calls == 1 {
				return before
			}
			return before.Add(time.Second)
		}))
		got, err := clock.Snapshot(ctx, id, "")
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || got.Month != "2026-10" || !got.AsOf.Equal(before) || got.DaysElapsed != 31 ||
			got.TotalBudgetCents != 1000 || !reflect.DeepEqual(got.Missing, []string{"transactions"}) {
			t.Fatalf("initialization crossed the captured month: calls=%d snapshot=%+v", calls, got)
		}
		var createdAt time.Time
		if err := db.QueryRowContext(ctx, `SELECT created_at FROM budgets
			WHERE user_id = ? AND month = '2026-10'`, id).Scan(&createdAt); err != nil || !createdAt.Equal(before) {
			t.Fatalf("budget timestamp did not use captured time: %v %v", createdAt, err)
		}
		var future int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM budgets
			WHERE user_id = ? AND month = '2026-11'`, id).Scan(&future); err != nil || future != 0 {
			t.Fatalf("initialization wrote the next month: %d %v", future, err)
		}
	})

	t.Run("one spending day does not project even with many transactions", func(t *testing.T) {
		id, _ := newUser("snapshot_one_day")
		for i := 0; i < 4; i++ {
			record(id, 25, "2026-11-01", insight.CategoryFood)
		}
		got := snapshot(id, "2026-11")
		if got.TransactionCount != 4 || got.ProjectedSpentCents != nil || got.TotalSpentCents != 100 {
			t.Fatalf("record count was treated as distinct days: %+v", got)
		}
	})

	t.Run("Singapore default month at UTC month boundary", func(t *testing.T) {
		id, _ := newUser("snapshot_month_boundary")
		clock := NewService(db, WithClock(func() time.Time { return time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC) }))
		got, err := clock.Snapshot(ctx, id, "")
		if err != nil || got.Month != "2026-11" || got.DaysElapsed != 1 || got.DaysInMonth != 30 {
			t.Fatalf("wrong Singapore default month: %+v %v", got, err)
		}
	})

	t.Run("concurrent budget and spending versions remain consistent", func(t *testing.T) {
		id, _ := newUser("snapshot_consistency")
		saveProfile(id)
		if _, err := service.SaveBudget(ctx, id, "2026-11", financeBudgetInput(100, 8000, [6]int64{100})); err != nil {
			t.Fatal(err)
		}
		record(id, 100, "2026-11-01", insight.CategoryFood)
		runFinanceConcurrent(t, 2, func(worker int) error {
			for i := 0; i < 40; i++ {
				if worker == 0 {
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						return err
					}
					amount := int64(100 + i%2*100)
					queries := []string{
						`UPDATE budgets SET total_cents = ? WHERE user_id = ? AND month = '2026-11'`,
						`UPDATE budget_categories bc INNER JOIN budgets b ON b.id = bc.budget_id
							SET bc.budget_cents = ? WHERE b.user_id = ? AND b.month = '2026-11' AND bc.category = 'food'`,
						`UPDATE transactions SET amount_cents = ? WHERE user_id = ? AND spent_on = '2026-11-01'`,
					}
					for _, query := range queries {
						if _, err := tx.ExecContext(ctx, query, amount, id); err != nil {
							_ = tx.Rollback()
							return err
						}
					}
					if err := tx.Commit(); err != nil {
						return err
					}
				} else {
					got, err := service.Snapshot(ctx, id, "2026-11")
					if err != nil {
						return err
					}
					if got.TotalBudgetCents != got.TotalSpentCents || got.TotalBudgetCents != got.Categories[0].BudgetCents {
						return fmt.Errorf("snapshot mixed committed versions")
					}
				}
			}
			return nil
		})
	})
}
