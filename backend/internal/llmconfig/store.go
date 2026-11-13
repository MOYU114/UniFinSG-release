package llmconfig

import (
	"context"
	"database/sql"
	"errors"

	"unichatgo/internal/llm"
)

// Store persists llm_configs rows.
type Store interface {
	Get(ctx context.Context, userID int64) (*Config, error)
	Upsert(ctx context.Context, config Config) error
	Delete(ctx context.Context, userID int64) error
}

// MySQLStore is the production store. It also implements llm.ConfigStore so
// the resolver can load the same row.
type MySQLStore struct {
	db *sql.DB
}

func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

func (s *MySQLStore) Get(ctx context.Context, userID int64) (*Config, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT enabled, kind, base_url, model, api_key_ciphertext, api_key_last4, last_test_at, last_test_ok
		FROM llm_configs WHERE user_id = ?`, userID)
	var (
		config Config
		lastAt sql.NullTime
		lastOK sql.NullBool
	)
	config.UserID = userID
	err := row.Scan(&config.Enabled, &config.Kind, &config.BaseURL, &config.Model,
		&config.APIKeyCiphertext, &config.APIKeyLast4, &lastAt, &lastOK)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastAt.Valid {
		value := lastAt.Time
		config.LastTestAt = &value
	}
	if lastOK.Valid {
		value := lastOK.Bool
		config.LastTestOK = &value
	}
	return &config, nil
}

func (s *MySQLStore) Upsert(ctx context.Context, config Config) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO llm_configs
			(user_id, enabled, kind, base_url, model, api_key_ciphertext, api_key_last4, last_test_at, last_test_ok)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			enabled = VALUES(enabled),
			kind = VALUES(kind),
			base_url = VALUES(base_url),
			model = VALUES(model),
			api_key_ciphertext = VALUES(api_key_ciphertext),
			api_key_last4 = VALUES(api_key_last4),
			last_test_at = VALUES(last_test_at),
			last_test_ok = VALUES(last_test_ok)`,
		config.UserID, config.Enabled, config.Kind, config.BaseURL, config.Model,
		config.APIKeyCiphertext, config.APIKeyLast4, config.LastTestAt, config.LastTestOK)
	return err
}

func (s *MySQLStore) Delete(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM llm_configs WHERE user_id = ?`, userID)
	return err
}

// GetLLMConfig implements llm.ConfigStore.
func (s *MySQLStore) GetLLMConfig(ctx context.Context, userID int64) (*llm.UserConfig, error) {
	config, err := s.Get(ctx, userID)
	if err != nil || config == nil {
		return nil, err
	}
	return &llm.UserConfig{
		Enabled:          config.Enabled,
		Kind:             config.Kind,
		BaseURL:          config.BaseURL,
		Model:            config.Model,
		APIKeyCiphertext: config.APIKeyCiphertext,
	}, nil
}
