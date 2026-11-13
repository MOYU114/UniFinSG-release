package finance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	"unichatgo/internal/insight"
)

var _ insight.Provider = (*Service)(nil)

type snapshotSpending struct {
	amount int64
	count  int
}

// Snapshot initializes only the current month's missing budget for an account
// with a profile. A read-only repeatable-read transaction then keeps the profile,
// budget and spending on the same committed database view.
func (s *Service) Snapshot(ctx context.Context, userID int64, month string) (*insight.Snapshot, error) {
	asOf := s.now().In(singapore)
	if month == "" {
		month = asOf.Format("2006-01")
	}
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrNotFound
	}
	return s.snapshotAt(ctx, userID, month, asOf, true)
}

// SnapshotReadOnly never creates a missing current-month budget. Read-only
// tools (and MCP readOnlyHint handlers) must use this instead of Snapshot so a
// lookup cannot mutate the ledger.
func (s *Service) SnapshotReadOnly(ctx context.Context, userID int64, month string) (*insight.Snapshot, error) {
	asOf := s.now().In(singapore)
	if month == "" {
		month = asOf.Format("2006-01")
	}
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrNotFound
	}
	return s.snapshotAt(ctx, userID, month, asOf, false)
}

func (s *Service) snapshotAt(ctx context.Context, userID int64, month string, asOf time.Time, initializeCurrent bool) (*insight.Snapshot, error) {
	first, _ := time.ParseInLocation("2006-01", month, singapore)
	last := first.AddDate(0, 1, -1)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin snapshot: %w", err)
	}
	defer tx.Rollback()
	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ?`, userID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read snapshot owner: %w", err)
	}
	var hasProfile bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM user_profiles WHERE user_id = ?)`, userID).Scan(&hasProfile); err != nil {
		return nil, fmt.Errorf("read snapshot profile: %w", err)
	}
	budget, err := readBudget(ctx, tx, userID, month)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, ErrNotFound) {
		budget = nil
		if initializeCurrent && hasProfile && month == asOf.Format("2006-01") {
			// Do not write inside or retain the old snapshot transaction. The
			// shared generator rechecks profile/budget after locking the user;
			// the fresh read below observes one complete committed result.
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit snapshot before budget initialization: %w", err)
			}
			if _, err := s.getOrCreateBudgetAt(ctx, userID, month, asOf, true); err != nil {
				return nil, err
			}
			return s.snapshotAt(ctx, userID, month, asOf, false)
		}
	}
	spending, err := readSnapshotSpending(ctx, tx, userID, first, last)
	if err != nil {
		return nil, err
	}
	var spendingDays int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT spent_on) FROM transactions
		WHERE user_id = ? AND spent_on >= ? AND spent_on <= ?`,
		userID, first.Format("2006-01-02"), last.Format("2006-01-02")).Scan(&spendingDays); err != nil {
		return nil, fmt.Errorf("read snapshot spending days: %w", err)
	}
	result, err := calculateSnapshot(month, asOf, hasProfile, budget, spending, spendingDays)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit snapshot read: %w", err)
	}
	return result, nil
}

func readSnapshotSpending(ctx context.Context, tx *sql.Tx, userID int64, first, last time.Time) (map[insight.Category]snapshotSpending, error) {
	rows, err := tx.QueryContext(ctx, `SELECT category, SUM(amount_cents), COUNT(*)
		FROM transactions WHERE user_id = ? AND spent_on >= ? AND spent_on <= ?
		GROUP BY category`, userID, first.Format("2006-01-02"), last.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("read snapshot spending: %w", err)
	}
	defer rows.Close()
	result := make(map[insight.Category]snapshotSpending, len(categoryOrder))
	for rows.Next() {
		var category insight.Category
		var amount, count string
		if err := rows.Scan(&category, &amount, &count); err != nil {
			return nil, fmt.Errorf("scan snapshot spending: %w", err)
		}
		// MySQL SUM(BIGINT) is DECIMAL and may exceed int64 even when each
		// transaction is valid. Parse explicitly rather than truncate it.
		cents, amountErr := strconv.ParseInt(amount, 10, 64)
		transactions, countErr := strconv.ParseInt(count, 10, strconv.IntSize)
		if !knownCategory(category) || amountErr != nil || countErr != nil || cents <= 0 || transactions <= 0 {
			return nil, fmt.Errorf("invalid spending aggregate: %w", ErrInconsistentData)
		}
		result[category] = snapshotSpending{amount: cents, count: int(transactions)}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read snapshot spending rows: %w", err)
	}
	return result, nil
}

func calculateSnapshot(month string, asOf time.Time, hasProfile bool, budget *Budget,
	spending map[insight.Category]snapshotSpending, spendingDays int) (*insight.Snapshot, error) {
	if err := validateMonth(month); err != nil {
		return nil, err
	}
	asOf = asOf.In(singapore)
	first, _ := time.ParseInLocation("2006-01", month, singapore)
	days := first.AddDate(0, 1, -1).Day()
	result := &insight.Snapshot{
		Month: month, AsOf: asOf, DaysInMonth: days,
		Categories: make([]insight.CategoryStat, 0, len(categoryOrder)),
		Missing:    make([]string, 0, 3),
	}
	if !hasProfile {
		result.Missing = append(result.Missing, "profile")
	}
	categoryBudgets := make(map[insight.Category]int64, len(categoryOrder))
	warnBP := 8000
	if budget == nil {
		result.Missing = append(result.Missing, "budget")
	} else {
		if budget.TotalCents < 0 || budget.WarnBP < 5000 || budget.WarnBP > 10000 {
			return nil, ErrInconsistentData
		}
		result.TotalBudgetCents, warnBP = budget.TotalCents, budget.WarnBP
		for _, item := range budget.Categories {
			if !knownCategory(item.Category) || item.BudgetCents < 0 {
				return nil, ErrInconsistentData
			}
			if _, duplicate := categoryBudgets[item.Category]; duplicate {
				return nil, ErrInconsistentData
			}
			categoryBudgets[item.Category] = item.BudgetCents
		}
		if len(categoryBudgets) != len(categoryOrder) {
			return nil, ErrInconsistentData
		}
	}
	for category := range spending {
		if !knownCategory(category) {
			return nil, ErrInconsistentData
		}
	}
	var topSpent int64
	for _, category := range categoryOrder {
		value := spending[category]
		if value.amount < 0 || value.count < 0 || value.amount > math.MaxInt64-result.TotalSpentCents ||
			value.count > math.MaxInt-result.TransactionCount || (value.amount == 0) != (value.count == 0) {
			return nil, ErrInconsistentData
		}
		result.TotalSpentCents += value.amount
		result.TransactionCount += value.count
		stat := insight.CategoryStat{
			Category: category, BudgetCents: categoryBudgets[category], SpentCents: value.amount, Status: insight.StatusOK,
		}
		var err error
		stat.UsageBP, err = snapshotUsageBP(stat.SpentCents, stat.BudgetCents)
		if err != nil {
			return nil, err
		}
		if stat.SpentCents > stat.BudgetCents {
			stat.Status = insight.StatusOver
		} else if stat.UsageBP != nil && *stat.UsageBP >= warnBP {
			stat.Status = insight.StatusWarn
		}
		result.Categories = append(result.Categories, stat)
		if value.amount > topSpent {
			top := category
			result.TopCategory = &top
			topSpent = value.amount
		}
	}
	if spendingDays < 0 || spendingDays > days || spendingDays > result.TransactionCount ||
		(spendingDays == 0) != (result.TransactionCount == 0) {
		return nil, ErrInconsistentData
	}
	if result.TransactionCount == 0 {
		result.Missing = append(result.Missing, "transactions")
	}
	// Both totals are nonnegative int64 values, so subtraction is exact.
	result.RemainingCents = result.TotalBudgetCents - result.TotalSpentCents
	var err error
	result.UsageBP, err = snapshotUsageBP(result.TotalSpentCents, result.TotalBudgetCents)
	if err != nil {
		return nil, err
	}
	switch current := asOf.Format("2006-01"); {
	case month < current:
		result.DaysElapsed = days
		projected := result.TotalSpentCents
		result.ProjectedSpentCents = &projected
	case month == current:
		result.DaysElapsed = asOf.Day()
		if spendingDays > result.DaysElapsed {
			return nil, ErrInconsistentData
		}
		if spendingDays >= 3 {
			projected, err := snapshotScaledInt64(result.TotalSpentCents, int64(days), int64(result.DaysElapsed))
			if err != nil {
				return nil, err
			}
			result.ProjectedSpentCents = &projected
		}
	}
	result.Hash, err = hashSnapshot(result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func snapshotUsageBP(spent, budget int64) (*int, error) {
	if budget == 0 {
		return nil, nil
	}
	value, err := snapshotScaledInt64(spent, 10000, budget)
	if err != nil || value > int64(math.MaxInt) {
		return nil, ErrInconsistentData
	}
	ratio := int(value)
	return &ratio, nil
}

func snapshotScaledInt64(value, multiplier, divisor int64) (int64, error) {
	if value < 0 || multiplier < 0 || divisor <= 0 {
		return 0, ErrInconsistentData
	}
	var result big.Int
	result.Mul(big.NewInt(value), big.NewInt(multiplier))
	result.Quo(&result, big.NewInt(divisor))
	if !result.IsInt64() {
		return 0, fmt.Errorf("snapshot integer range exceeded: %w", ErrInconsistentData)
	}
	return result.Int64(), nil
}

func hashSnapshot(snapshot *insight.Snapshot) (string, error) {
	type snapshotFields insight.Snapshot
	// An explicit field shadows the embedded Hash and is omitted entirely.
	payload := struct {
		*snapshotFields
		Hash *string `json:"hash,omitempty"`
	}{snapshotFields: (*snapshotFields)(snapshot)}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode snapshot: %w", ErrInconsistentData)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data)), nil
}
