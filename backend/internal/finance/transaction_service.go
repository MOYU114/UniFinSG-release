package finance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const transactionColumns = "id, amount_cents, spent_on, category, merchant, note, source, draft_id, created_at, updated_at"

// provenance records where a write came from. Agent writes set source='agent'
// and the draft that the user confirmed; ordinary user writes keep 'manual'.
type provenance struct {
	Source  string
	DraftID *string
}

func (s *Service) CreateTransaction(ctx context.Context, userID int64, input TransactionInput) (*Transaction, error) {
	record, err := normalizeTransaction(input, s.now())
	if err != nil {
		return nil, err
	}
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := s.insertTransaction(ctx, tx, userID, record, nil)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction creation: %w", err)
	}
	return saved, nil
}

func (s *Service) insertTransaction(ctx context.Context, tx *sql.Tx, userID int64,
	record Transaction, from *provenance) (*Transaction, error) {
	source := SourceManual
	var draftID any
	if from != nil {
		source = from.Source
		if from.DraftID != nil {
			draftID = *from.DraftID
		}
	}
	stamp := s.now().UTC().Truncate(time.Microsecond)
	result, err := tx.ExecContext(ctx, `INSERT INTO transactions
		(user_id, amount_cents, spent_on, category, merchant, note, source, draft_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, record.AmountCents, record.SpentOn, record.Category, record.Merchant, record.Note,
		source, draftID, stamp, stamp)
	if err != nil {
		return nil, fmt.Errorf("create transaction: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read transaction id: %w", err)
	}
	return readTransaction(ctx, tx, userID, id)
}

func (s *Service) ListTransactions(ctx context.Context, userID int64, options TransactionListOptions) (*TransactionPage, error) {
	options, err := normalizeTransactionList(options, s.now())
	if err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrNotFound
	}
	start, _ := time.ParseInLocation("2006-01", options.Month, singapore)
	last := start.AddDate(0, 1, -1)
	where := "user_id = ? AND spent_on >= ? AND spent_on <= ?"
	args := []any{userID, start.Format("2006-01-02"), last.Format("2006-01-02")}
	if options.Category != "" {
		where += " AND category = ?"
		args = append(args, options.Category)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin transaction listing: %w", err)
	}
	defer tx.Rollback()
	page := &TransactionPage{Items: []Transaction{}, Page: options.Page, PageSize: options.PageSize}
	// COUNT establishes the repeatable-read snapshot also used by the items
	// query, so concurrent inserts/deletes cannot disagree with this total.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE `+where, args...).Scan(&page.Total); err != nil {
		return nil, fmt.Errorf("count transactions: %w", err)
	}
	offset := int64(options.Page-1) * int64(options.PageSize)
	args = append(args, options.PageSize, offset)
	rows, err := tx.QueryContext(ctx, `SELECT `+transactionColumns+` FROM transactions WHERE `+where+
		` ORDER BY spent_on DESC, created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	for rows.Next() {
		record, err := scanTransaction(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan listed transaction: %w", err)
		}
		page.Items = append(page.Items, *record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read listed transactions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close transaction listing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction listing: %w", err)
	}
	return page, nil
}

func (s *Service) UpdateTransaction(ctx context.Context, userID, transactionID int64, patch TransactionPatch) (*Transaction, error) {
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	saved, err := s.updateTransactionInTx(ctx, tx, userID, transactionID, patch, nil)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction update: %w", err)
	}
	return saved, nil
}

func (s *Service) updateTransactionInTx(ctx context.Context, tx *sql.Tx, userID, transactionID int64,
	patch TransactionPatch, from *provenance) (*Transaction, error) {
	current, err := readTransaction(ctx, tx, userID, transactionID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	updated, changed, err := applyTransactionPatch(*current, patch, now)
	if err != nil {
		return nil, err
	}
	if changed {
		stamp := now.UTC().Truncate(time.Microsecond)
		if from == nil {
			_, err = tx.ExecContext(ctx, `UPDATE transactions
				SET amount_cents = ?, spent_on = ?, category = ?, merchant = ?, note = ?, updated_at = ?
				WHERE user_id = ? AND id = ?`,
				updated.AmountCents, updated.SpentOn, updated.Category, updated.Merchant, updated.Note,
				stamp, userID, transactionID)
		} else {
			var draftID any
			if from.DraftID != nil {
				draftID = *from.DraftID
			}
			_, err = tx.ExecContext(ctx, `UPDATE transactions
				SET amount_cents = ?, spent_on = ?, category = ?, merchant = ?, note = ?,
					source = ?, draft_id = ?, updated_at = ?
				WHERE user_id = ? AND id = ?`,
				updated.AmountCents, updated.SpentOn, updated.Category, updated.Merchant, updated.Note,
				from.Source, draftID, stamp, userID, transactionID)
		}
		if err != nil {
			return nil, fmt.Errorf("update transaction: %w", err)
		}
	}
	return readTransaction(ctx, tx, userID, transactionID)
}

func (s *Service) DeleteTransaction(ctx context.Context, userID, transactionID int64) error {
	tx, err := s.beginUserWrite(ctx, userID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deleteTransactionInTx(ctx, tx, userID, transactionID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction deletion: %w", err)
	}
	return nil
}

func (s *Service) deleteTransactionInTx(ctx context.Context, tx *sql.Tx, userID, transactionID int64) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM transactions WHERE user_id = ? AND id = ?`, userID, transactionID)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted transaction count: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// GetTransaction returns one record scoped to its owner.
func (s *Service) GetTransaction(ctx context.Context, userID, transactionID int64) (*Transaction, error) {
	return readTransaction(ctx, s.db, userID, transactionID)
}

func readTransaction(ctx context.Context, query rowQuerier, userID, transactionID int64) (*Transaction, error) {
	record, err := scanTransaction(query.QueryRowContext(ctx,
		`SELECT `+transactionColumns+` FROM transactions WHERE user_id = ? AND id = ?`, userID, transactionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read transaction: %w", err)
	}
	return record, nil
}

type transactionScanner interface {
	Scan(...any) error
}

func scanTransaction(row transactionScanner) (*Transaction, error) {
	var record Transaction
	var spentOn time.Time
	var draftID sql.NullString
	if err := row.Scan(&record.ID, &record.AmountCents, &spentOn, &record.Category,
		&record.Merchant, &record.Note, &record.Source, &draftID, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return nil, err
	}
	if draftID.Valid {
		value := draftID.String
		record.DraftID = &value
	}
	// DATE is a calendar value, not an instant: preserve its date components
	// rather than converting midnight through a different time zone.
	record.SpentOn = spentOn.Format("2006-01-02")
	record.CreatedAt = record.CreatedAt.In(singapore)
	record.UpdatedAt = record.UpdatedAt.In(singapore)
	return &record, nil
}
