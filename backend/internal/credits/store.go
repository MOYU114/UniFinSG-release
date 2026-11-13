package credits

import (
	"context"
	"errors"
	"fmt"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "credits:used:"
	keyTTL    = 48 * time.Hour
)

// RedisStore stores one counter per user and Singapore calendar day.
type RedisStore struct {
	client *redis.Client
}

func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func key(userID int64, day string) string {
	return fmt.Sprintf("%s%d:%s", keyPrefix, userID, day)
}

func (s *RedisStore) Used(ctx context.Context, userID int64, day string) (int, error) {
	if s == nil || s.client == nil {
		return 0, ErrUnavailable
	}
	value, err := s.client.Get(ctx, key(userID, day)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, nil
	}
	return value, nil
}

func (s *RedisStore) Add(ctx context.Context, userID int64, day string, delta int) (int, error) {
	if s == nil || s.client == nil {
		return 0, ErrUnavailable
	}
	value, err := s.client.IncrBy(ctx, key(userID, day), int64(delta)).Result()
	if err != nil {
		return 0, err
	}
	if value > 0 {
		// Refresh the TTL; a day key only needs to survive one rollover. A TTL
		// failure must not turn a successful charge into an error, otherwise
		// the caller could retry and charge twice.
		_ = s.client.Expire(ctx, key(userID, day), keyTTL).Err()
	}
	if value < 0 {
		return 0, nil
	}
	return int(value), nil
}
