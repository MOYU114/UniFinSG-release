package finance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
	_ "time/tzdata"

	"unichatgo/internal/insight"
)

var singapore = func() *time.Location {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		panic(err) // The time zone database is embedded above.
	}
	return location
}()

type Service struct {
	db  *sql.DB
	now func() time.Time
}

type Option func(*Service)

func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// NewService uses only the MySQL finance schema supplied by migrations.
func NewService(db *sql.DB, options ...Option) *Service {
	service := &Service{db: db, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) GetProfile(ctx context.Context, userID int64) (*Profile, error) {
	profile, err := readProfile(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

func (s *Service) SaveProfile(ctx context.Context, userID int64, input ProfileInput) (*Profile, error) {
	profile, err := normalizeProfile(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().UTC().Truncate(time.Microsecond)
	_, err = tx.ExecContext(ctx, `INSERT INTO user_profiles
		(user_id, monthly_disposable_cents, housing, default_budget_cents, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE monthly_disposable_cents = VALUES(monthly_disposable_cents),
		housing = VALUES(housing), default_budget_cents = VALUES(default_budget_cents),
		updated_at = VALUES(updated_at)`,
		userID, profile.MonthlyDisposableCents, profile.Housing, profile.DefaultBudgetCents, now, now)
	if err != nil {
		return nil, fmt.Errorf("save profile: %w", err)
	}
	saved, err := readProfile(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit profile: %w", err)
	}
	return saved, nil
}

func (s *Service) GetOrCreateBudget(ctx context.Context, userID int64, month string) (*Budget, error) {
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	return s.getOrCreateBudgetAt(ctx, userID, month, s.now(), false)
}

// EnsureCurrentBudget uses the caller's captured time so authentication and
// finance agree on the current Singapore month even across a month boundary.
// Accounts without a profile are left unchanged until onboarding is complete.
func (s *Service) EnsureCurrentBudget(ctx context.Context, userID int64, now time.Time) error {
	_, err := s.getOrCreateBudgetAt(ctx, userID, now.In(singapore).Format("2006-01"), now, true)
	return err
}

func (s *Service) getOrCreateBudgetAt(ctx context.Context, userID int64, month string, now time.Time, skipWithoutProfile bool) (*Budget, error) {
	if userID <= 0 {
		return nil, ErrNotFound
	}
	// Parent and category rows must share one committed version. Reading an
	// existing budget needs no owner lock and can proceed while a writer waits
	// or holds that user's row lock.
	read, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin budget read: %w", err)
	}
	defer read.Rollback()
	budget, err := readBudget(ctx, read, userID, month)
	if err == nil {
		if err := read.Commit(); err != nil {
			return nil, fmt.Errorf("commit budget read: %w", err)
		}
		return budget, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if !budgetGenerationAllowed(month, now) {
		return nil, ErrNotFound
	}
	if skipWithoutProfile {
		if _, err := readProfile(ctx, read, userID); err != nil {
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			var owner int64
			if err := read.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ?`, userID).Scan(&owner); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, ErrNotFound
				}
				return nil, fmt.Errorf("read budget owner: %w", err)
			}
			return nil, nil
		}
	}
	if err := read.Commit(); err != nil {
		return nil, fmt.Errorf("commit missing budget read: %w", err)
	}

	// Only generation enters the serialized writer path. Recheck after taking
	// the owner lock because a concurrent reader may already have generated it.
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	budget, err = readBudget(ctx, tx, userID, month)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit budget read: %w", err)
		}
		return budget, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	profile, err := readProfile(ctx, tx, userID)
	if errors.Is(err, ErrNotFound) {
		if skipWithoutProfile {
			return nil, nil
		}
		return nil, ErrProfileRequired
	}
	if err != nil {
		return nil, err
	}

	var previousMonth string
	err = tx.QueryRowContext(ctx, `SELECT month FROM budgets
		WHERE user_id = ? AND month < ? ORDER BY month DESC LIMIT 1`, userID, month).Scan(&previousMonth)
	if err == nil {
		budget, err = readBudget(ctx, tx, userID, previousMonth)
		if err != nil {
			return nil, err
		}
		budget.Source = "copied_from_" + previousMonth
	} else if errors.Is(err, sql.ErrNoRows) {
		budget = &Budget{
			TotalCents: profile.DefaultBudgetCents, WarnBP: 8000,
			Categories: allocateDefaultBudget(profile.DefaultBudgetCents), Source: "profile_default",
		}
	} else {
		return nil, fmt.Errorf("find previous budget: %w", err)
	}
	budget.Month = month
	if err := s.writeBudgetAt(ctx, tx, userID, budget, now); err != nil {
		return nil, err
	}
	saved, err := readBudget(ctx, tx, userID, month)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generated budget: %w", err)
	}
	saved.Generated = true
	return saved, nil
}

func budgetGenerationAllowed(month string, now time.Time) bool {
	current := now.In(singapore)
	first := time.Date(current.Year(), current.Month(), 1, 0, 0, 0, 0, singapore)
	return month >= first.AddDate(0, -12, 0).Format("2006-01") &&
		month <= first.AddDate(0, 12, 0).Format("2006-01")
}

func (s *Service) SaveBudget(ctx context.Context, userID int64, month string, input BudgetInput) (*Budget, error) {
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := s.saveBudgetInTx(ctx, tx, userID, month, input, "saved")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit budget: %w", err)
	}
	return saved, nil
}

// saveBudgetInTx validates and writes a budget inside the caller's
// transaction. source records where the write came from.
func (s *Service) saveBudgetInTx(ctx context.Context, tx *sql.Tx, userID int64, month string,
	input BudgetInput, source string) (*Budget, error) {
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	budget, err := normalizeBudget(input)
	if err != nil {
		return nil, err
	}
	if source != "" {
		budget.Source = source
	}
	budget.Month = month
	if err := s.writeBudgetAt(ctx, tx, userID, &budget, s.now()); err != nil {
		return nil, err
	}
	return readBudget(ctx, tx, userID, month)
}

// ReadBudget returns the stored budget for one month without creating it.
func (s *Service) ReadBudget(ctx context.Context, userID int64, month string) (*Budget, error) {
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	return readBudget(ctx, s.db, userID, month)
}

// BeginWrite starts a transaction and takes the same per-user lock every
// finance writer uses. The draft decision endpoint owns this transaction so
// the ledger write and the draft result commit together.
func (s *Service) BeginWrite(ctx context.Context, userID int64) (*sql.Tx, error) {
	return s.beginUserWrite(ctx, userID)
}

func (s *Service) beginUserWrite(ctx context.Context, userID int64) (*sql.Tx, error) {
	if userID <= 0 {
		return nil, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin finance transaction: %w", err)
	}
	var id int64
	// Lock an existing row even when the target month's budget is absent.
	// Every profile/budget writer acquires this lock first, so concurrent
	// generation and saving cannot produce mixed parent/category versions.
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ? FOR UPDATE`, userID).Scan(&id)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lock finance owner: %w", err)
	}
	return tx, nil
}

func (s *Service) writeBudget(ctx context.Context, tx *sql.Tx, userID int64, budget *Budget) error {
	return s.writeBudgetAt(ctx, tx, userID, budget, s.now())
}

func (s *Service) writeBudgetAt(ctx context.Context, tx *sql.Tx, userID int64, budget *Budget, now time.Time) error {
	now = now.UTC().Truncate(time.Microsecond)
	_, err := tx.ExecContext(ctx, `INSERT INTO budgets
		(user_id, month, total_cents, warn_bp, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE total_cents = VALUES(total_cents), warn_bp = VALUES(warn_bp),
		source = VALUES(source), updated_at = VALUES(updated_at)`,
		userID, budget.Month, budget.TotalCents, budget.WarnBP, budget.Source, now, now)
	if err != nil {
		return fmt.Errorf("save budget: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE bc FROM budget_categories bc
		INNER JOIN budgets b ON b.id = bc.budget_id
		WHERE b.user_id = ? AND b.month = ?`, userID, budget.Month)
	if err != nil {
		return fmt.Errorf("replace category budgets: %w", err)
	}
	for _, category := range budget.Categories {
		_, err = tx.ExecContext(ctx, `INSERT INTO budget_categories (budget_id, category, budget_cents)
			SELECT id, ?, ? FROM budgets WHERE user_id = ? AND month = ?`,
			category.Category, category.BudgetCents, userID, budget.Month)
		if err != nil {
			return fmt.Errorf("save category budget: %w", err)
		}
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// querier is satisfied by both *sql.DB and *sql.Tx so read helpers work on
// either a pool or an open write transaction.
type querier interface {
	rowQuerier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readProfile(ctx context.Context, query rowQuerier, userID int64) (*Profile, error) {
	var profile Profile
	err := query.QueryRowContext(ctx, `SELECT monthly_disposable_cents, housing,
		default_budget_cents, updated_at FROM user_profiles WHERE user_id = ?`, userID).
		Scan(&profile.MonthlyDisposableCents, &profile.Housing, &profile.DefaultBudgetCents, &profile.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	profile.UpdatedAt = profile.UpdatedAt.In(singapore)
	return &profile, nil
}

func readBudget(ctx context.Context, query querier, userID int64, month string) (*Budget, error) {
	budget := &Budget{Month: month}
	err := query.QueryRowContext(ctx, `SELECT total_cents, warn_bp, source FROM budgets
		WHERE user_id = ? AND month = ?`, userID, month).
		Scan(&budget.TotalCents, &budget.WarnBP, &budget.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read budget: %w", err)
	}
	rows, err := query.QueryContext(ctx, `SELECT bc.category, bc.budget_cents
		FROM budget_categories bc INNER JOIN budgets b ON b.id = bc.budget_id
		WHERE b.user_id = ? AND b.month = ?`, userID, month)
	if err != nil {
		return nil, fmt.Errorf("read category budgets: %w", err)
	}
	defer rows.Close()
	values := make(map[insight.Category]int64, len(categoryOrder))
	for rows.Next() {
		var category insight.Category
		var amount int64
		if err := rows.Scan(&category, &amount); err != nil {
			return nil, fmt.Errorf("scan category budget: %w", err)
		}
		if !knownCategory(category) || amount < 0 || amount > math.MaxInt64-budget.CategorySumCents {
			return nil, ErrInconsistentData
		}
		values[category] = amount
		budget.CategorySumCents += amount
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read category budgets: %w", err)
	}
	if len(values) != len(categoryOrder) || budget.TotalCents < 0 {
		return nil, ErrInconsistentData
	}
	for _, category := range categoryOrder {
		budget.Categories = append(budget.Categories, CategoryBudget{Category: category, BudgetCents: values[category]})
	}
	budget.UnallocatedCents = budget.TotalCents - budget.CategorySumCents
	return budget, nil
}
