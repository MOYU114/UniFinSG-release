package eval

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"unichatgo/internal/redis"
)

type Service struct {
	db    *sql.DB
	cache *redis.Client
	now   func() time.Time
}

type Option func(*Service)

func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

func NewService(db *sql.DB, cache *redis.Client, options ...Option) *Service {
	service := &Service{db: db, cache: cache, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Record(ctx context.Context, input EventInput) error {
	input, err := validateEvent(input)
	if err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO eval_events
		(participant_code, task, event, success, error_code, client_ts, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, input.ParticipantCode, input.Task, input.Event,
		input.Success, input.ErrorCode, input.ClientTS, s.now().UTC().Truncate(time.Microsecond))
	if err != nil {
		return fmt.Errorf("record evaluation event: %w", err)
	}
	return nil
}

func (s *Service) Events(ctx context.Context) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, participant_code, task, event,
		success, error_code, client_ts, received_at FROM eval_events ORDER BY received_at, id`)
	if err != nil {
		return nil, fmt.Errorf("read evaluation events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		var success sql.NullBool
		var errorCode, clientTS sql.NullString
		if err := rows.Scan(&event.ID, &event.ParticipantCode, &event.Task, &event.Event,
			&success, &errorCode, &clientTS, &event.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan evaluation event: %w", err)
		}
		if success.Valid {
			value := success.Bool
			event.Success = &value
		}
		if errorCode.Valid {
			value := errorCode.String
			event.ErrorCode = &value
		}
		if clientTS.Valid {
			value := clientTS.String
			event.ClientTS = &value
		}
		event.ReceivedAt = event.ReceivedAt.UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation events: %w", err)
	}
	return events, nil
}

func (s *Service) Summary(ctx context.Context) ([]TaskSummary, error) {
	events, err := s.Events(ctx)
	if err != nil {
		return nil, err
	}
	return summarize(events), nil
}
