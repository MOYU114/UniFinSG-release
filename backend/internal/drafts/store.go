package drafts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MySQLStore is the production draft store. It requires migration 000017.
type MySQLStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db, now: time.Now}
}

const draftColumns = "id, user_id, session_id, turn_id, tool_call_id, kind, payload, " +
	"final_payload, status, reject_reason, result, expires_at, decided_at, created_at"

func (s *MySQLStore) Insert(ctx context.Context, draft Draft) (*Draft, error) {
	if draft.UserID <= 0 || draft.SessionID <= 0 || draft.ID == "" {
		return nil, ErrNotFound
	}
	payload, err := encodePayload(draft.Payload, draft.Before, draft.Warnings)
	if err != nil {
		return nil, fmt.Errorf("encode draft payload: %w", err)
	}
	createdAt := draft.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now().UTC().Truncate(time.Microsecond)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO agent_drafts
		(id, user_id, session_id, turn_id, tool_call_id, kind, payload, status, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draft.ID, draft.UserID, draft.SessionID, draft.TurnID, draft.ToolCallID, draft.Kind,
		string(payload), StatusPending, draft.ExpiresAt.UTC().Truncate(time.Microsecond), createdAt)
	if err != nil {
		return nil, fmt.Errorf("insert draft: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return nil, ErrNotFound
	}
	saved := draft
	saved.Status = StatusPending
	saved.CreatedAt = createdAt
	return &saved, nil
}

func (s *MySQLStore) Get(ctx context.Context, userID int64, id string) (*Draft, error) {
	if userID <= 0 || id == "" {
		return nil, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+draftColumns+` FROM agent_drafts WHERE id = ? AND user_id = ?`,
		id, userID)
	draft, err := scanDraft(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read draft: %w", err)
	}
	return draft, nil
}

func (s *MySQLStore) Consume(ctx context.Context, userID int64, id, status, reason string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_drafts
		SET status = ?, reject_reason = ?, decided_at = NOW(6)
		WHERE id = ? AND user_id = ? AND status = 'pending' AND expires_at > NOW(6)`,
		status, nullableString(reason), id, userID)
	if err != nil {
		return false, fmt.Errorf("consume draft: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read consumed draft count: %w", err)
	}
	return affected == 1, nil
}

func (s *MySQLStore) CompleteTx(ctx context.Context, tx *sql.Tx, userID int64, id string,
	finalPayload, result json.RawMessage) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_drafts
		SET status = 'approved', final_payload = ?, result = ?
		WHERE id = ? AND user_id = ?`,
		optionalJSON(finalPayload), optionalJSON(result), id, userID)
	if err != nil {
		return fmt.Errorf("complete draft: %w", err)
	}
	return nil
}

func (s *MySQLStore) MarkFailed(ctx context.Context, userID int64, id, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_drafts
		SET status = 'failed', reject_reason = ?, decided_at = NOW(6)
		WHERE id = ? AND user_id = ?`,
		nullableString(truncateRunes(reason, 200)), id, userID)
	if err != nil {
		return fmt.Errorf("fail draft: %w", err)
	}
	return nil
}

func (s *MySQLStore) ExpirePending(ctx context.Context, userID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin draft expiry: %w", err)
	}
	defer tx.Rollback()

	query := `SELECT id, turn_id FROM agent_drafts WHERE status = 'pending' AND expires_at <= NOW(6)`
	args := []any{}
	if userID > 0 {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` FOR UPDATE`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("find expired drafts: %w", err)
	}
	ids := make([]string, 0, 8)
	turns := make([]string, 0, 8)
	for rows.Next() {
		var id, turnID string
		if err := rows.Scan(&id, &turnID); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan expired draft: %w", err)
		}
		ids = append(ids, id)
		if turnID != "" {
			turns = append(turns, turnID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("read expired drafts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close expired drafts: %w", err)
	}
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit draft expiry: %w", err)
		}
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_drafts SET status = 'expired', decided_at = NOW(6)
		WHERE id IN (`+placeholders(len(ids))+`)`, stringArgs(ids)...); err != nil {
		return 0, fmt.Errorf("expire drafts: %w", err)
	}
	if len(turns) > 0 {
		// Design section 6.4 step 6: an expired draft completes its turn.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_turns SET status = 'completed', finished_at = NOW(6)
			WHERE status = 'awaiting_approval' AND id IN (`+placeholders(len(turns))+`)`,
			stringArgs(turns)...); err != nil {
			return 0, fmt.Errorf("complete expired turns: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit draft expiry: %w", err)
	}
	return int64(len(ids)), nil
}

func (s *MySQLStore) ListPendingForSession(ctx context.Context, userID, sessionID int64) ([]Draft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+draftColumns+` FROM agent_drafts
		WHERE user_id = ? AND session_id = ? AND status = 'pending' AND expires_at > NOW(6)
		ORDER BY created_at DESC`, userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list pending drafts: %w", err)
	}
	defer rows.Close()
	out := make([]Draft, 0, 4)
	for rows.Next() {
		draft, err := scanDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending draft: %w", err)
		}
		out = append(out, *draft)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending drafts: %w", err)
	}
	return out, nil
}

func scanDraft(row interface{ Scan(...any) error }) (*Draft, error) {
	var (
		draft     Draft
		payload   []byte
		final     sql.NullString
		reject    sql.NullString
		result    sql.NullString
		decidedAt sql.NullTime
		expiresAt time.Time
		createdAt time.Time
	)
	if err := row.Scan(&draft.ID, &draft.UserID, &draft.SessionID, &draft.TurnID, &draft.ToolCallID,
		&draft.Kind, &payload, &final, &draft.Status, &reject, &result, &expiresAt, &decidedAt, &createdAt); err != nil {
		return nil, err
	}
	if err := decodePayload(json.RawMessage(payload), &draft); err != nil {
		return nil, fmt.Errorf("decode draft payload: %w", err)
	}
	draft.FinalPayload = decodeOptionalJSON(final)
	draft.RejectReason = reject.String
	draft.Result = decodeOptionalJSON(result)
	draft.ExpiresAt = expiresAt.UTC()
	draft.CreatedAt = createdAt.UTC()
	if decidedAt.Valid {
		value := decidedAt.Time.UTC()
		draft.DecidedAt = &value
	}
	return &draft, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
