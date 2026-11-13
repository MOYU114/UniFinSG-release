package agentv2

import (
	"context"
	"errors"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Coordination provides the Redis primitives the turn lifecycle needs:
// the per-session lock and the client_request_id idempotency key.
type Coordination interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key string) error
}

// RedisCoordination is the production implementation.
type RedisCoordination struct {
	client *redis.Client
}

func NewRedisCoordination(client *redis.Client) *RedisCoordination {
	return &RedisCoordination{client: client}
}

var ErrCoordinationUnavailable = errors.New("agent coordination store unavailable")

func (c *RedisCoordination) Acquire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if c == nil || c.client == nil {
		return false, ErrCoordinationUnavailable
	}
	acquired, err := c.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, ErrCoordinationUnavailable
	}
	return acquired, nil
}

func (c *RedisCoordination) Release(ctx context.Context, key string) error {
	if c == nil || c.client == nil {
		return ErrCoordinationUnavailable
	}
	if err := c.client.Del(ctx, key).Err(); err != nil {
		return ErrCoordinationUnavailable
	}
	return nil
}
