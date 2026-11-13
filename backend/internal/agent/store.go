package agent

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

type storedMessage struct {
	HistoryMessage
	verified bool
	fallback bool
}

type store struct {
	db       *sql.DB
	mu       sync.RWMutex
	messages map[int64][]storedMessage
	audits   []auditRecord
	nextID   atomic.Int64
}

type auditRecord struct {
	UserID       int64
	SnapshotHash string
	Intent       Intent
	Verified     bool
	Fallback     bool
	CreatedAt    time.Time
}

func newStore(db *sql.DB) *store {
	s := &store{db: db, messages: make(map[int64][]storedMessage)}
	s.nextID.Store(0)
	return s
}

func (s *store) saveTurn(ctx context.Context, userID int64, question, answer string, verified, fallback bool) (int64, error) {
	now := time.Now().UTC()
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		id := s.nextID.Add(2) - 1
		q := storedMessage{HistoryMessage: HistoryMessage{ID: id, Role: "user", Content: question, CreatedAt: now}}
		a := storedMessage{HistoryMessage: HistoryMessage{ID: id + 1, Role: "assistant", Content: answer, CreatedAt: now}, verified: verified, fallback: fallback}
		v, f := verified, fallback
		a.Verified, a.Fallback = &v, &f
		s.messages[userID] = append(s.messages[userID], q, a)
		return id + 1, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO assistant_messages (user_id, role, content, verified, fallback, created_at) VALUES (?, 'user', ?, NULL, NULL, ?)`, userID, question, now); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO assistant_messages (user_id, role, content, verified, fallback, created_at) VALUES (?, 'assistant', ?, ?, ?, ?)`, userID, answer, verified, fallback, now)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *store) history(ctx context.Context, userID int64, limit int) ([]HistoryMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	if s.db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		items := s.messages[userID]
		start := len(items) - limit
		if start < 0 {
			start = 0
		}
		out := make([]HistoryMessage, 0, len(items)-start)
		for _, item := range items[start:] {
			out = append(out, item.HistoryMessage)
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, content, verified, fallback, created_at FROM assistant_messages WHERE user_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reverse []HistoryMessage
	for rows.Next() {
		var m HistoryMessage
		var verified, fallback sql.NullBool
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &verified, &fallback, &m.CreatedAt); err != nil {
			return nil, err
		}
		if verified.Valid {
			v := verified.Bool
			m.Verified = &v
		}
		if fallback.Valid {
			f := fallback.Bool
			m.Fallback = &f
		}
		reverse = append(reverse, m)
	}
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	return reverse, rows.Err()
}

func (s *store) audit(ctx context.Context, record auditRecord) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.audits = append(s.audits, record)
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO assistant_audits (user_id, snapshot_hash, intent, verified, fallback, created_at) VALUES (?, ?, ?, ?, ?, ?)`, record.UserID, record.SnapshotHash, record.Intent, record.Verified, record.Fallback, record.CreatedAt)
	return err
}

func logStoreError(operation string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("agent: unable to %s: %v", operation, err)
	}
}
