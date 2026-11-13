package eval

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"time"
)

const rateLimitScript = `
local count = redis.call("INCR", KEYS[1])
local ttl = redis.call("PTTL", KEYS[1])
if count == 1 or ttl < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {count, ttl}
`

// AllowIP uses one atomic fixed-window counter per normalized IP. Redis retains
// only its hash for 60 seconds; no address is persisted with evaluation events.
func (s *Service) AllowIP(ctx context.Context, address netip.Addr) (bool, int, error) {
	if s == nil || s.cache == nil || s.cache.Raw() == nil || !address.IsValid() {
		return false, 0, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	values, err := s.cache.Raw().Eval(ctx, rateLimitScript, []string{rateLimitKey(address)}, 60000).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("evaluation rate limit: %w", err)
	}
	if len(values) != 2 {
		return false, 0, ErrUnavailable
	}
	count, countOK := values[0].(int64)
	ttl, ttlOK := values[1].(int64)
	if !countOK || !ttlOK || count < 1 || ttl < 0 || ttl > 60000 {
		return false, 0, ErrUnavailable
	}
	retry := int((ttl + 999) / 1000)
	if retry < 1 {
		retry = 1
	}
	return count <= 60, retry, nil
}

func rateLimitKey(address netip.Addr) string {
	digest := sha256.Sum256([]byte(address.Unmap().String()))
	return fmt.Sprintf("unifinsg:eval:ip:%x", digest)
}
