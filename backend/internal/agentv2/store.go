package agentv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// MySQLStore is the production store. It relies on migrations 000015/000016
// for messages.parts, messages.turn_id, messages.verified and sessions.kind.
type MySQLStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db, now: time.Now}
}

const sessionColumns = "id, user_id, title, kind, created_at, updated_at"

func (s *MySQLStore) CreateSession(ctx context.Context, userID int64, title string) (*Session, error) {
	if userID <= 0 {
		return nil, ErrNotFound
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = DefaultSessionTitle
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	result, err := s.db.ExecContext(ctx, `INSERT INTO sessions (user_id, title, kind, created_at, updated_at)
		VALUES (?, ?, 'agent', ?, ?)`, userID, title, now, now)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read session id: %w", err)
	}
	return &Session{ID: id, UserID: userID, Title: title, Kind: "agent", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *MySQLStore) ListSessions(ctx context.Context, userID int64, cursor string, limit int) ([]Session, string, error) {
	if userID <= 0 {
		return nil, "", ErrNotFound
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE user_id = ?`
	args := []any{userID}
	if cursor != "" {
		updatedAt, id, err := parseCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, updatedAt, updatedAt, id)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	sessions := make([]Session, 0, limit)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, "", err
		}
		sessions = append(sessions, *session)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read sessions: %w", err)
	}
	next := ""
	if len(sessions) > limit {
		sessions = sessions[:limit]
		last := sessions[len(sessions)-1]
		next = formatCursor(last.UpdatedAt, last.ID)
	}
	return sessions, next, nil
}

func (s *MySQLStore) GetSession(ctx context.Context, userID, sessionID int64) (*Session, error) {
	session, err := scanSession(s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return session, nil
}

func (s *MySQLStore) RenameSession(ctx context.Context, userID, sessionID int64, title string) (*Session, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("session title must not be empty")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`, title, s.now().UTC().Truncate(time.Microsecond), sessionID, userID)
	if err != nil {
		return nil, fmt.Errorf("rename session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read renamed session count: %w", err)
	}
	if affected == 0 {
		if _, err := s.GetSession(ctx, userID, sessionID); err != nil {
			return nil, err
		}
	}
	return s.GetSession(ctx, userID, sessionID)
}

func (s *MySQLStore) DeleteSession(ctx context.Context, userID, sessionID int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read deleted session count: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) TouchSession(ctx context.Context, userID, sessionID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ? AND user_id = ?`,
		at.UTC().Truncate(time.Microsecond), sessionID, userID)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// AutoTitle renames a session only while it still carries the default title,
// so a user rename is never overwritten by the first message.
func (s *MySQLStore) AutoTitle(ctx context.Context, userID, sessionID int64, title string, at time.Time) error {
	if title == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET title = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND title = ?`,
		title, at.UTC().Truncate(time.Microsecond), sessionID, userID, DefaultSessionTitle)
	if err != nil {
		return fmt.Errorf("auto title session: %w", err)
	}
	return nil
}

const messageColumns = "id, user_id, session_id, role, content, parts, turn_id, verified, created_at"

func (s *MySQLStore) InsertMessage(ctx context.Context, message Message) (*Message, error) {
	if message.UserID <= 0 || message.SessionID <= 0 {
		return nil, ErrNotFound
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	if message.CreatedAt.IsZero() {
		message.CreatedAt = now
	}
	var parts any
	if len(message.Parts) > 0 {
		raw, err := json.Marshal(message.Parts)
		if err != nil {
			return nil, fmt.Errorf("encode message parts: %w", err)
		}
		parts = string(raw)
	}
	var turnID any
	if message.TurnID != "" {
		turnID = message.TurnID
	}
	var verified any
	if message.Verified != nil {
		verified = *message.Verified
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO messages
		(user_id, session_id, role, content, parts, turn_id, verified, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		message.UserID, message.SessionID, message.Role, message.Content, parts, turnID, verified, message.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read message id: %w", err)
	}
	saved := message
	saved.ID = id
	return &saved, nil
}

func (s *MySQLStore) ListMessages(ctx context.Context, userID, sessionID int64, before int64, limit int) ([]Message, bool, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	query := `SELECT ` + messageColumns + ` FROM messages WHERE user_id = ? AND session_id = ?`
	args := []any{userID, sessionID}
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	descending := make([]Message, 0, limit)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, false, err
		}
		descending = append(descending, *message)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("read messages: %w", err)
	}
	hasMore := len(descending) > limit
	if hasMore {
		descending = descending[:limit]
	}
	messages := make([]Message, len(descending))
	for i, message := range descending {
		messages[len(descending)-1-i] = message
	}
	return messages, hasMore, nil
}

// AppendToolResult adds the decision result to the assistant message of the
// paused turn. Keeping it in that message lets both adapters send every tool
// result immediately after the tool calls. Re-appending the same call_id is a
// no-op so a retried request cannot duplicate the result.
func (s *MySQLStore) AppendToolResult(ctx context.Context, userID, sessionID int64,
	turnID, callID string, result Part) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append tool result: %w", err)
	}
	defer tx.Rollback()
	var (
		messageID int64
		raw       sql.NullString
	)
	err = tx.QueryRowContext(ctx, `SELECT id, parts FROM messages
		WHERE user_id = ? AND session_id = ? AND turn_id = ? AND role = ?
		ORDER BY id DESC LIMIT 1 FOR UPDATE`,
		userID, sessionID, turnID, RoleAssistant).Scan(&messageID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read paused message: %w", err)
	}
	parts := []Part{}
	if raw.Valid && strings.TrimSpace(raw.String) != "" {
		if err := json.Unmarshal([]byte(raw.String), &parts); err != nil {
			return fmt.Errorf("decode paused message parts: %w", err)
		}
	}
	for _, part := range parts {
		if part.Type == PartToolResult && part.CallID == callID {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit append tool result: %w", err)
			}
			return nil
		}
	}
	parts = append(parts, result)
	encoded, err := json.Marshal(parts)
	if err != nil {
		return fmt.Errorf("encode paused message parts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET parts = ? WHERE id = ? AND user_id = ?`,
		string(encoded), messageID, userID); err != nil {
		return fmt.Errorf("append tool result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit append tool result: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetTurn(ctx context.Context, userID int64, turnID string) (*Turn, error) {
	var (
		turn      Turn
		errorCode sql.NullString
		finished  sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, session_id, mode, kind, credits_charged,
		status, model_calls, error_code, created_at, finished_at
		FROM agent_turns WHERE id = ? AND user_id = ?`, turnID, userID).
		Scan(&turn.ID, &turn.UserID, &turn.SessionID, &turn.Mode, &turn.Kind, &turn.CreditsCharged,
			&turn.Status, &turn.ModelCalls, &errorCode, &turn.CreatedAt, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read turn: %w", err)
	}
	turn.ErrorCode = errorCode.String
	turn.CreatedAt = turn.CreatedAt.UTC()
	if finished.Valid {
		value := finished.Time.UTC()
		turn.FinishedAt = &value
	}
	return &turn, nil
}

// ContinueTurn advances a paused turn without changing credits_charged, which
// was settled when the turn first paused.
func (s *MySQLStore) ContinueTurn(ctx context.Context, userID int64, turnID, status string,
	addedModelCalls int, errorCode string) error {
	var code any
	if errorCode != "" {
		code = errorCode
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_turns
		SET status = ?, model_calls = model_calls + ?, error_code = ?, finished_at = ?
		WHERE id = ? AND user_id = ?`,
		status, addedModelCalls, code, s.now().UTC().Truncate(time.Microsecond), turnID, userID)
	if err != nil {
		return fmt.Errorf("continue turn: %w", err)
	}
	return nil
}

func (s *MySQLStore) CreateTurn(ctx context.Context, turn Turn) error {
	var errorCode any
	if turn.ErrorCode != "" {
		errorCode = turn.ErrorCode
	}
	createdAt := turn.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.now().UTC().Truncate(time.Microsecond)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_turns
		(id, user_id, session_id, mode, kind, credits_charged, status, model_calls, error_code, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		turn.ID, turn.UserID, turn.SessionID, turn.Mode, turn.Kind, turn.CreditsCharged,
		turn.Status, turn.ModelCalls, errorCode, createdAt)
	if err != nil {
		return fmt.Errorf("create turn: %w", err)
	}
	return nil
}

func (s *MySQLStore) FinishTurn(ctx context.Context, turn Turn) error {
	var errorCode any
	if turn.ErrorCode != "" {
		errorCode = turn.ErrorCode
	}
	finished := turn.FinishedAt
	if finished == nil {
		value := s.now().UTC().Truncate(time.Microsecond)
		finished = &value
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agent_turns
		SET status = ?, kind = ?, credits_charged = ?, model_calls = ?, error_code = ?, finished_at = ?
		WHERE id = ? AND user_id = ?`,
		turn.Status, turn.Kind, turn.CreditsCharged, turn.ModelCalls, errorCode, *finished, turn.ID, turn.UserID)
	if err != nil {
		return fmt.Errorf("finish turn: %w", err)
	}
	return nil
}

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var session Session
	if err := row.Scan(&session.ID, &session.UserID, &session.Title, &session.Kind,
		&session.CreatedAt, &session.UpdatedAt); err != nil {
		return nil, err
	}
	session.CreatedAt = session.CreatedAt.UTC()
	session.UpdatedAt = session.UpdatedAt.UTC()
	return &session, nil
}

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var (
		message  Message
		parts    sql.NullString
		turnID   sql.NullString
		verified sql.NullBool
	)
	if err := row.Scan(&message.ID, &message.UserID, &message.SessionID, &message.Role,
		&message.Content, &parts, &turnID, &verified, &message.CreatedAt); err != nil {
		return nil, err
	}
	message.Parts = []Part{}
	if parts.Valid && strings.TrimSpace(parts.String) != "" {
		if err := json.Unmarshal([]byte(parts.String), &message.Parts); err != nil {
			return nil, fmt.Errorf("decode message parts: %w", err)
		}
		if message.Parts == nil {
			message.Parts = []Part{}
		}
	}
	if turnID.Valid {
		message.TurnID = turnID.String
	}
	if verified.Valid {
		value := verified.Bool
		message.Verified = &value
	}
	message.CreatedAt = message.CreatedAt.UTC()
	return &message, nil
}

func formatCursor(updatedAt time.Time, id int64) string {
	return strconv.FormatInt(updatedAt.UnixMicro(), 10) + ":" + strconv.FormatInt(id, 10)
}

func parseCursor(cursor string) (time.Time, int64, error) {
	parts := strings.SplitN(cursor, ":", 2)
	if len(parts) != 2 {
		return time.Time{}, 0, ErrInvalidCursor
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, ErrInvalidCursor
	}
	return time.UnixMicro(micros).UTC(), id, nil
}

// TitleFromContent truncates the first user message to 20 runes.
func TitleFromContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	runes := []rune(content)
	if len(runes) > 20 {
		runes = runes[:20]
	}
	return string(runes)
}
