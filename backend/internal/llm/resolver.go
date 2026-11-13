package llm

import (
	"context"
	"errors"
	"os"
	"strings"

	"unichatgo/internal/crypto"
)

// ErrNotConfigured is returned when neither a BYOK configuration nor the
// platform model is available. HTTP maps it to 503 llm_not_configured.
var ErrNotConfigured = errors.New("llm_not_configured")

// ErrCipherUnavailable means a BYOK row exists but the server cannot decrypt it.
var ErrCipherUnavailable = errors.New("llm key encryption is not configured")

// Modes reported by the resolver and the credits endpoint.
const (
	ModePlatform = "platform"
	ModeBYOK     = "byok"
)

// UserConfig is the subset of llm_configs the resolver needs.
type UserConfig struct {
	Enabled          bool
	Kind             string
	BaseURL          string
	Model            string
	APIKeyCiphertext string
}

// ConfigStore loads a user's BYOK row. A missing row returns (nil, nil).
type ConfigStore interface {
	GetLLMConfig(ctx context.Context, userID int64) (*UserConfig, error)
}

// PlatformConfig is the operator-provided fallback model.
type PlatformConfig struct {
	Kind    string
	BaseURL string
	Model   string
	APIKey  string
	// ReasoningEffort is optional; see Config.ReasoningEffort.
	ReasoningEffort string
}

// PlatformFromEnv reads LLM_KIND / LLM_BASE_URL / LLM_MODEL / LLM_API_KEY.
func PlatformFromEnv() PlatformConfig {
	kind := strings.TrimSpace(os.Getenv("LLM_KIND"))
	if kind == "" {
		kind = KindOpenAICompatible
	}
	return PlatformConfig{
		Kind:    kind,
		BaseURL: strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
		Model:   strings.TrimSpace(os.Getenv("LLM_MODEL")),
		APIKey:  strings.TrimSpace(os.Getenv("LLM_API_KEY")),

		ReasoningEffort: strings.TrimSpace(os.Getenv("LLM_REASONING_EFFORT")),
	}
}

func (p PlatformConfig) configured() bool {
	return p.BaseURL != "" && p.Model != ""
}

// Resolver selects the model for a user: an enabled BYOK row first, otherwise
// the platform configuration.
type Resolver struct {
	store         ConfigStore
	cipher        *crypto.Cipher
	platform      PlatformConfig
	allowInsecure bool
}

func NewResolver(store ConfigStore, cipher *crypto.Cipher, platform PlatformConfig, allowInsecure bool) *Resolver {
	return &Resolver{store: store, cipher: cipher, platform: platform, allowInsecure: allowInsecure}
}

// ResolveForUser returns a ready ChatModel and its mode.
func (r *Resolver) ResolveForUser(ctx context.Context, userID int64) (ChatModel, string, error) {
	userConfig, err := r.userConfig(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	if userConfig != nil && userConfig.Enabled {
		if r.cipher == nil {
			return nil, "", ErrCipherUnavailable
		}
		key, err := r.cipher.Decrypt(userConfig.APIKeyCiphertext)
		if err != nil {
			return nil, "", err
		}
		if _, err := ValidateBaseURL(userConfig.BaseURL, r.allowInsecure); err != nil {
			return nil, "", err
		}
		model, err := New(Config{
			Kind:          userConfig.Kind,
			BaseURL:       userConfig.BaseURL,
			APIKey:        key,
			Model:         userConfig.Model,
			AllowInsecure: r.allowInsecure,
			HTTPClient:    NewHTTPClient(r.allowInsecure),
		})
		if err != nil {
			return nil, "", err
		}
		return model, ModeBYOK, nil
	}
	if !r.platform.configured() {
		return nil, "", ErrNotConfigured
	}
	model, err := New(Config{
		Kind:    r.platform.Kind,
		BaseURL: r.platform.BaseURL,
		APIKey:  r.platform.APIKey,
		Model:   r.platform.Model,

		ReasoningEffort: r.platform.ReasoningEffort,
		// The platform endpoint is operator-controlled and may be a local
		// mock/ollama over http, so it keeps the permissive client.
		AllowInsecure: true,
		HTTPClient:    NewHTTPClient(true),
	})
	if err != nil {
		return nil, "", err
	}
	return model, ModePlatform, nil
}

// ModeForUser reports the mode without building a model.
func (r *Resolver) ModeForUser(ctx context.Context, userID int64) (string, error) {
	userConfig, err := r.userConfig(ctx, userID)
	if err != nil {
		return "", err
	}
	if userConfig != nil && userConfig.Enabled {
		return ModeBYOK, nil
	}
	if !r.platform.configured() {
		return "", ErrNotConfigured
	}
	return ModePlatform, nil
}

func (r *Resolver) userConfig(ctx context.Context, userID int64) (*UserConfig, error) {
	if r.store == nil {
		return nil, nil
	}
	return r.store.GetLLMConfig(ctx, userID)
}
