// Package credits enforces the daily platform-model allowance. BYOK turns are
// never counted.
package credits

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Modes match llm.ModePlatform / llm.ModeBYOK.
const (
	ModePlatform = "platform"
	ModeBYOK     = "byok"
)

// Tiers decide the charge for one turn.
const (
	TierChat   = "chat"
	TierTool   = "tool"
	TierVision = "vision"
)

// Costs is the per-tier charge reported to the frontend.
type Costs struct {
	Chat   int `json:"chat"`
	Tool   int `json:"tool"`
	Vision int `json:"vision"`
}

// Config is the daily allowance and tier costs.
type Config struct {
	DailyGrant int
	Costs      Costs
}

// ConfigFromEnv reads CREDITS_DAILY_GRANT and the three cost variables.
func ConfigFromEnv() Config {
	return Config{
		DailyGrant: intFromEnv("CREDITS_DAILY_GRANT", 100),
		Costs: Costs{
			Chat:   intFromEnv("CREDITS_COST_CHAT", 1),
			Tool:   intFromEnv("CREDITS_COST_TOOL", 2),
			Vision: intFromEnv("CREDITS_COST_VISION", 5),
		},
	}
}

func intFromEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

// Errors.
var (
	ErrExhausted   = errors.New("credits exhausted")
	ErrUnavailable = errors.New("credits store unavailable")
)

// ExhaustedError carries the next reset time for the 402 response.
type ExhaustedError struct {
	ResetsAt time.Time
}

func (e *ExhaustedError) Error() string { return ErrExhausted.Error() }
func (e *ExhaustedError) Unwrap() error { return ErrExhausted }

// Snapshot is exactly the JSON shape in design §6.6. BYOK leaves every field
// other than mode nil so the encoder writes nulls.
type Snapshot struct {
	Mode       string  `json:"mode"`
	DailyGrant *int    `json:"daily_grant"`
	Used       *int    `json:"used"`
	Remaining  *int    `json:"remaining"`
	ResetsAt   *string `json:"resets_at"`
	Costs      *Costs  `json:"costs"`
}

var sgt = time.FixedZone("Asia/Singapore", 8*60*60)

func dayKey(t time.Time) string {
	return t.In(sgt).Format("20060102")
}

func resetsAt(t time.Time) time.Time {
	local := t.In(sgt)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, sgt)
}

// TierFor maps a finished turn to its charge tier.
func TierFor(hasImage, hasTools bool) string {
	switch {
	case hasImage:
		return TierVision
	case hasTools:
		return TierTool
	default:
		return TierChat
	}
}

// Store is the Redis-backed daily counter. Implementations must return the
// value after the increment.
type Store interface {
	Used(ctx context.Context, userID int64, day string) (int, error)
	Add(ctx context.Context, userID int64, day string, delta int) (int, error)
}

// Service applies the precheck/settle policy.
type Service struct {
	store  Store
	config Config
	now    func() time.Time
}

func NewService(store Store, config Config) *Service {
	return &Service{store: store, config: config, now: time.Now}
}

func (s *Service) cost(tier string) int {
	switch tier {
	case TierChat:
		return s.config.Costs.Chat
	case TierTool:
		return s.config.Costs.Tool
	case TierVision:
		return s.config.Costs.Vision
	default:
		return 0
	}
}

// Reserve checks that the remaining allowance covers the highest possible
// tier for this turn. It does not increment the counter.
func (s *Service) Reserve(ctx context.Context, userID int64, mode string, hasImage bool) (*Reservation, error) {
	if mode == ModeBYOK {
		return &Reservation{service: s, userID: userID, mode: ModeBYOK}, nil
	}
	if s.store == nil {
		return nil, ErrUnavailable
	}
	now := s.now()
	day := dayKey(now)
	tier := TierTool
	if hasImage {
		tier = TierVision
	}
	required := s.cost(tier)
	used, err := s.store.Used(ctx, userID, day)
	if err != nil {
		return nil, ErrUnavailable
	}
	if s.config.DailyGrant-used < required {
		return nil, &ExhaustedError{ResetsAt: resetsAt(now)}
	}
	return &Reservation{service: s, userID: userID, day: day, mode: ModePlatform, reserved: required}, nil
}

// Snapshot reports the current allowance.
func (s *Service) Snapshot(ctx context.Context, userID int64, mode string) (Snapshot, error) {
	if mode == ModeBYOK {
		return Snapshot{Mode: ModeBYOK}, nil
	}
	if s.store == nil {
		return Snapshot{}, ErrUnavailable
	}
	now := s.now()
	used, err := s.store.Used(ctx, userID, dayKey(now))
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	grant := s.config.DailyGrant
	remaining := grant - used
	if remaining < 0 {
		remaining = 0
	}
	reset := resetsAt(now).Format(time.RFC3339)
	costs := s.config.Costs
	return Snapshot{
		Mode:       ModePlatform,
		DailyGrant: &grant,
		Used:       &used,
		Remaining:  &remaining,
		ResetsAt:   &reset,
		Costs:      &costs,
	}, nil
}

// Reservation is one turn's precheck. Settle at the end with the actual tier;
// Release if the turn failed before producing any output.
type Reservation struct {
	service  *Service
	userID   int64
	day      string
	mode     string
	reserved int
	charged  int
	settled  bool
}

func (r *Reservation) Mode() string  { return r.mode }
func (r *Reservation) Reserved() int { return r.reserved }
func (r *Reservation) Charged() int  { return r.charged }

// Settle charges the actual tier and returns the charged amount.
func (r *Reservation) Settle(ctx context.Context, tier string) (int, error) {
	if r == nil {
		return 0, nil
	}
	if r.mode == ModeBYOK || r.settled {
		return r.charged, nil
	}
	cost := r.service.cost(tier)
	if cost <= 0 {
		r.settled = true
		return 0, nil
	}
	if r.service.store == nil {
		return 0, ErrUnavailable
	}
	if _, err := r.service.store.Add(ctx, r.userID, r.day, cost); err != nil {
		return 0, ErrUnavailable
	}
	r.settled = true
	r.charged = cost
	return cost, nil
}

// Release undoes a settlement, or is a no-op when nothing was charged.
func (r *Reservation) Release(ctx context.Context) error {
	if r == nil || r.mode == ModeBYOK || !r.settled || r.charged == 0 {
		return nil
	}
	if r.service.store == nil {
		return ErrUnavailable
	}
	if _, err := r.service.store.Add(ctx, r.userID, r.day, -r.charged); err != nil {
		return ErrUnavailable
	}
	r.settled = false
	r.charged = 0
	return nil
}
