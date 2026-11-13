package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"unichatgo/internal/redis"
)

const (
	defaultPerMinute = 5
	defaultPerDay    = 30
)

type rateLimiter struct {
	cache     *redis.Client
	mu        sync.Mutex
	local     map[string]windowCount
	lastWarn  time.Time
	perMinute int
	perDay    int
}
type windowCount struct {
	count   int
	expires time.Time
}

// QuotaUsage is the caller's remaining assistant allowance. Used counts are
// clamped to the limit because rejected requests still increment the window.
type QuotaUsage struct {
	DailyLimit      int    `json:"daily_limit"`
	DailyUsed       int    `json:"daily_used"`
	DailyRemaining  int    `json:"daily_remaining"`
	MinuteLimit     int    `json:"minute_limit"`
	MinuteRemaining int    `json:"minute_remaining"`
	ResetsAt        string `json:"resets_at"`
}

func newRateLimiter(cache *redis.Client) *rateLimiter {
	return &rateLimiter{
		cache:     cache,
		local:     make(map[string]windowCount),
		perMinute: limitFromEnv("ASSISTANT_RPM", defaultPerMinute),
		perDay:    limitFromEnv("ASSISTANT_DAILY_LIMIT", defaultPerDay),
	}
}

func limitFromEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		log.Printf("agent: ignoring invalid %s=%q; using %d", name, raw, fallback)
		return fallback
	}
	return v
}

func singaporeNow() time.Time {
	return time.Now().In(time.FixedZone("Asia/Singapore", 8*60*60))
}

func (r *rateLimiter) allow(ctx context.Context, userID int64) (bool, string, error) {
	now := singaporeNow()
	minute, day := fmt.Sprintf("agent:rate:%d:m:%d", userID, now.Unix()/60), fmt.Sprintf("agent:rate:%d:d:%s", userID, now.Format("2006-01-02"))
	if r.cache != nil && r.cache.Raw() != nil {
		for _, item := range []struct {
			key string
			max int
			ttl time.Duration
		}{{minute, r.perMinute, 2 * time.Minute}, {day, r.perDay, 25 * time.Hour}} {
			key, max, ttl := item.key, item.max, item.ttl
			value, err := r.cache.Raw().Incr(ctx, key).Result()
			if err != nil {
				r.warnFallback()
				return r.allowLocal(userID)
			}
			if value == 1 {
				_ = r.cache.Raw().Expire(ctx, key, ttl).Err()
			}
			if value > int64(max) {
				return false, map[bool]string{true: "rate_limited", false: "quota_exceeded"}[key == minute], nil
			}
		}
		return true, "", nil
	}
	r.warnFallback()
	return r.allowLocal(userID)
}

func (r *rateLimiter) allowLocal(userID int64) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	minuteKey := fmt.Sprintf("%d:m", userID)
	dayKey := fmt.Sprintf("%d:d", userID)
	for _, item := range []struct {
		key string
		max int
		ttl time.Duration
	}{{minuteKey, r.perMinute, 2 * time.Minute}, {dayKey, r.perDay, 25 * time.Hour}} {
		key, max, ttl := item.key, item.max, item.ttl
		v := r.local[key]
		if v.expires.Before(now) {
			v = windowCount{expires: now.Add(ttl)}
		}
		v.count++
		r.local[key] = v
		if v.count > max {
			if key == minuteKey {
				return false, "rate_limited", nil
			}
			return false, "quota_exceeded", nil
		}
	}
	return true, "", nil
}

// release undoes a reservation when snapshot acquisition fails. This keeps
// transient finance outages from consuming the assistant quota.
func (r *rateLimiter) release(ctx context.Context, userID int64) {
	now := time.Now().In(time.FixedZone("Asia/Singapore", 8*60*60))
	minute := fmt.Sprintf("agent:rate:%d:m:%d", userID, now.Unix()/60)
	day := fmt.Sprintf("agent:rate:%d:d:%s", userID, now.Format("2006-01-02"))
	if r.cache != nil && r.cache.Raw() != nil {
		for _, key := range []string{minute, day} {
			value, err := r.cache.Raw().Decr(ctx, key).Result()
			if err == nil && value <= 0 {
				_ = r.cache.Raw().Del(ctx, key).Err()
			}
		}
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range []string{fmt.Sprintf("%d:m", userID), fmt.Sprintf("%d:d", userID)} {
		if value, ok := r.local[key]; ok && value.count > 0 {
			value.count--
			r.local[key] = value
		}
	}
}

func (r *rateLimiter) retryAfter(userID int64, reason string) int {
	now := time.Now().In(time.FixedZone("Asia/Singapore", 8*60*60))
	if reason == "quota_exceeded" {
		next := now.AddDate(0, 0, 1)
		midnight := time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, now.Location())
		return maxInt(1, int(time.Until(midnight).Seconds()))
	}
	return maxInt(1, 60-now.Second())
}

// usage reads the current windows without reserving a request, so the page
// can show the remaining allowance without spending a question.
func (r *rateLimiter) usage(ctx context.Context, userID int64) QuotaUsage {
	now := singaporeNow()
	next := now.AddDate(0, 0, 1)
	out := QuotaUsage{
		DailyLimit:  r.perDay,
		MinuteLimit: r.perMinute,
		ResetsAt:    time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, now.Location()).Format(time.RFC3339),
	}
	minuteUsed, dayUsed, ok := 0, 0, false
	if r.cache != nil && r.cache.Raw() != nil {
		minute, day := fmt.Sprintf("agent:rate:%d:m:%d", userID, now.Unix()/60), fmt.Sprintf("agent:rate:%d:d:%s", userID, now.Format("2006-01-02"))
		values, err := r.cache.Raw().MGet(ctx, minute, day).Result()
		if err == nil && len(values) == 2 {
			minuteUsed, dayUsed, ok = redisCount(values[0]), redisCount(values[1]), true
		} else {
			r.warnFallback()
		}
	}
	if !ok {
		r.mu.Lock()
		local := time.Now()
		for key, dst := range map[string]*int{fmt.Sprintf("%d:m", userID): &minuteUsed, fmt.Sprintf("%d:d", userID): &dayUsed} {
			if v, found := r.local[key]; found && v.expires.After(local) {
				*dst = v.count
			}
		}
		r.mu.Unlock()
	}
	out.DailyUsed = min(dayUsed, r.perDay)
	out.DailyRemaining = r.perDay - out.DailyUsed
	out.MinuteRemaining = r.perMinute - min(minuteUsed, r.perMinute)
	return out
}

func redisCount(v interface{}) int {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (r *rateLimiter) warnFallback() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastWarn) > time.Minute {
		log.Printf("agent: Redis unavailable; using single-instance approximate rate limiting")
		r.lastWarn = time.Now()
	}
}
