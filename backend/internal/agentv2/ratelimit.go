package agentv2

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

const defaultRPM = 5

// RateLimiter applies the per-user ASSISTANT_RPM window. Both platform and
// BYOK turns are limited; the design keeps the minute limit for both modes.
type RateLimiter interface {
	// Allow reports whether this request may proceed and, when it may not,
	// the number of seconds until the window resets.
	Allow(ctx context.Context, userID int64) (bool, int, error)
}

type localRPMWindow struct {
	count   int
	expires time.Time
}

type rpmLimiter struct {
	client *redis.Client
	limit  int
	mu     sync.Mutex
	local  map[int64]localRPMWindow
}

// NewRateLimiter reads ASSISTANT_RPM (default 5). Redis is used when
// available; otherwise a single-instance approximate window keeps the
// endpoint usable, matching the inherited limiter's fallback.
func NewRateLimiter(client *redis.Client) RateLimiter {
	return &rpmLimiter{
		client: client,
		limit:  rpmFromEnv(),
		local:  make(map[int64]localRPMWindow),
	}
}

func rpmFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("ASSISTANT_RPM"))
	if raw == "" {
		return defaultRPM
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return defaultRPM
	}
	return value
}

func (l *rpmLimiter) Allow(ctx context.Context, userID int64) (bool, int, error) {
	now := time.Now()
	key := fmt.Sprintf("agent:rpm:%d:%d", userID, now.Unix()/60)
	if l.client != nil {
		count, err := l.client.Incr(ctx, key).Result()
		if err == nil {
			if count == 1 {
				_ = l.client.Expire(ctx, key, 2*time.Minute).Err()
			}
			return count <= int64(l.limit), retryAfterSeconds(now), nil
		}
	}
	return l.allowLocal(userID, now), retryAfterSeconds(now), nil
}

func (l *rpmLimiter) allowLocal(userID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, window := range l.local {
		if window.expires.Before(now) {
			delete(l.local, id)
		}
	}
	window := l.local[userID]
	if window.expires.Before(now) {
		window = localRPMWindow{expires: now.Add(2 * time.Minute)}
	}
	window.count++
	l.local[userID] = window
	return window.count <= l.limit
}

func retryAfterSeconds(now time.Time) int {
	seconds := 60 - now.Second()
	if seconds < 1 {
		return 1
	}
	return seconds
}
