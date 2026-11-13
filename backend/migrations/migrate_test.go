package migrations

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"

	"unichatgo/internal/storage"
	"unichatgo/migrations/testdb"
)

func TestApplyCanceledBeforeOpeningDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Apply(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context without database access, got %v", err)
	}
}

func TestMySQLMigrationLifecycle(t *testing.T) {
	db := testdb.Open(t, "TEST_MYSQL_MIGRATIONS_DSN")
	latest := latestEmbeddedMigration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := storage.Migrate(db, "mysql"); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	sentinel := insertLegacyUser(t, db, "migration_sentinel")
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("first application: %v", err)
	}
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("repeat application: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("migration closed application pool: %v", err)
	}
	assertVersion(t, db, latest, false)

	// Two application instances may start together.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Apply(ctx, db)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent application: %v", err)
		}
	}
	assertVersion(t, db, latest, false)

	userID := insertLegacyUser(t, db, "migration_finance")
	if _, err := db.ExecContext(ctx, `UPDATE users SET cognito_sub = ? WHERE id = ?`, "test-subject", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE users SET cognito_sub = ? WHERE id = ?`, "test-subject", sentinel); err == nil {
		t.Fatal("duplicate Cognito subject was accepted")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_profiles (user_id, monthly_disposable_cents, housing, default_budget_cents) VALUES (?, 120000, 'on_campus', 100000)`, userID); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO budgets (user_id, month, total_cents, warn_bp, source) VALUES (?, '2026-10', 100000, 8000, 'profile_default')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	budgetID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO budgets (user_id, month, total_cents) VALUES (?, '2026-10', 1)`, userID); err == nil {
		t.Fatal("duplicate monthly budget was accepted")
	}
	for _, category := range []string{"food", "transport", "housing_tuition", "daily_shopping", "study_digital", "entertainment_other"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO budget_categories (budget_id, category, budget_cents) VALUES (?, ?, 10000)`, budgetID, category); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO transactions (user_id, amount_cents, spent_on, category, merchant, note) VALUES (?, 650, '2026-10-01', 'food', NULL, NULL)`, userID); err != nil {
		t.Fatal(err)
	}
	for _, amount := range []int64{0, -1, 100000001} {
		if _, err := db.ExecContext(ctx, `INSERT INTO transactions (user_id, amount_cents, spent_on, category) VALUES (?, ?, '2026-10-01', 'food')`, userID, amount); err == nil {
			t.Fatalf("invalid amount %d was accepted", amount)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_profiles (user_id, monthly_disposable_cents, housing, default_budget_cents) VALUES (?, 120000, 'on_campus', -1)`, sentinel); err == nil {
		t.Fatal("negative default budget was accepted")
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM user_profiles WHERE user_id = ?`,
		`SELECT COUNT(*) FROM budgets WHERE user_id = ?`,
		`SELECT COUNT(*) FROM transactions WHERE user_id = ?`,
	} {
		assertCount(t, db, query, userID, 0)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM budget_categories WHERE budget_id = ?`, budgetID, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM users WHERE id = ?`, sentinel, 1)

	if _, err := db.ExecContext(ctx, `UPDATE unifinsg_schema_migrations SET dirty = TRUE WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}
	var dirty migrate.ErrDirty
	if err := Apply(ctx, db); !errors.As(err, &dirty) || dirty.Version != latest {
		t.Fatalf("expected refusal of dirty schema version %d, got %v", latest, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE unifinsg_schema_migrations SET dirty = FALSE WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}

	m, err := newMigrator(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	downErr := m.Down()
	sourceErr, databaseErr := m.Close()
	if err := errors.Join(downErr, sourceErr, databaseErr); err != nil {
		t.Fatalf("rollback test-owned schema: %v", err)
	}
	for _, table := range []string{"user_profiles", "budgets", "budget_categories", "transactions"} {
		assertCount(t, db, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?`, table, 0)
	}
	assertCount(t, db, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'users' AND column_name = ?`, "cognito_sub", 0)
	assertCount(t, db, `SELECT COUNT(*) FROM users WHERE id = ?`, sentinel, 1)
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("apply again after rollback: %v", err)
	}
	assertVersion(t, db, latest, false)
}

func latestEmbeddedMigration(t *testing.T) int {
	t.Helper()
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	latest := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		prefix, _, found := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !found || err != nil || version <= 0 {
			t.Fatalf("invalid embedded migration name: %s", entry.Name())
		}
		if version > latest {
			latest = version
		}
	}
	if latest == 0 {
		t.Fatal("no embedded up migrations found")
	}
	return latest
}

func insertLegacyUser(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	result, err := db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, 'test-only-hash', ?)`, username, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertVersion(t *testing.T, db *sql.DB, expectedVersion int, expectedDirty bool) {
	t.Helper()
	var version int
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM unifinsg_schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != expectedVersion || dirty != expectedDirty {
		t.Fatalf("schema state: got version=%d dirty=%v, want version=%d dirty=%v", version, dirty, expectedVersion, expectedDirty)
	}
}

func assertCount(t *testing.T, db *sql.DB, query string, arg any, expected int) {
	t.Helper()
	var count int
	if err := db.QueryRow(query, arg).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("unexpected row count: got %d, want %d", count, expected)
	}
}
