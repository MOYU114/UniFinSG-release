package llmconfig

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Limiter guards the connectivity test endpoint. It fails open to the local
// window when Redis is unavailable because this is not a security control.
type Limiter interface {
	Allow(ctx context.Context, userID int64) bool
}

type localWindow struct {
	count   int
	expires time.Time
}

type minuteLimiter struct {
	client *redis.Client
	limit  int
	mu     sync.Mutex
	local  map[int64]localWindow
}

// NewLimiter uses ASSISTANT_RPM (default 5) per user per minute.
func NewLimiter(client *redis.Client) Limiter {
	return &minuteLimiter{client: client, limit: limitFromEnv(), local: map[int64]localWindow{}}
}

func limitFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("ASSISTANT_RPM"))
	if raw == "" {
		return 5
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 5
	}
	return value
}

func (l *minuteLimiter) Allow(ctx context.Context, userID int64) bool {
	if l.client != nil {
		key := fmt.Sprintf("llm:test:%d:%d", userID, time.Now().Unix()/60)
		count, err := l.client.Incr(ctx, key).Result()
		if err == nil {
			if count == 1 {
				_ = l.client.Expire(ctx, key, 2*time.Minute).Err()
			}
			return count <= int64(l.limit)
		}
	}
	return l.allowLocal(userID)
}

func (l *minuteLimiter) allowLocal(userID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for id, window := range l.local {
		if window.expires.Before(now) {
			delete(l.local, id)
		}
	}
	window := l.local[userID]
	if window.expires.Before(now) {
		window = localWindow{expires: now.Add(2 * time.Minute)}
	}
	window.count++
	l.local[userID] = window
	return window.count <= l.limit
}
