package llmconfig

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"unichatgo/internal/crypto"
	"unichatgo/internal/llm"
)

// ModelFactory builds an adapter. Tests replace it with a fake.
type ModelFactory func(kind, baseURL, model, apiKey string) (llm.ChatModel, error)

// Options configures the service.
type Options struct {
	AllowInsecure bool
	Limiter       Limiter
	Now           func() time.Time
}

// Service owns validation, encryption and connectivity testing.
type Service struct {
	store         Store
	cipher        *crypto.Cipher
	allowInsecure bool
	limiter       Limiter
	now           func() time.Time
	newModel      ModelFactory
}

func NewService(store Store, cipher *crypto.Cipher, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Service{
		store:         store,
		cipher:        cipher,
		allowInsecure: opts.AllowInsecure,
		limiter:       opts.Limiter,
		now:           now,
	}
	s.newModel = func(kind, baseURL, model, apiKey string) (llm.ChatModel, error) {
		return llm.New(llm.Config{
			Kind:          kind,
			BaseURL:       baseURL,
			Model:         model,
			APIKey:        apiKey,
			AllowInsecure: s.allowInsecure,
			HTTPClient:    llm.NewHTTPClient(s.allowInsecure),
		})
	}
	return s
}

// SetModelFactory replaces the adapter factory (tests only).
func (s *Service) SetModelFactory(factory ModelFactory) {
	if factory != nil {
		s.newModel = factory
	}
}

// Get returns the stored configuration, or a "not configured" view.
func (s *Service) Get(ctx context.Context, userID int64) (*View, error) {
	config, err := s.store.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return &View{Enabled: false, Configured: false}, nil
	}
	return viewOf(config), nil
}

// Save validates, tests and stores a configuration. A failed test leaves the
// stored row untouched.
func (s *Service) Save(ctx context.Context, userID int64, input Input) (*View, error) {
	existing, err := s.store.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	kind, baseURL, model, err := s.validateFields(input.Kind, input.BaseURL, input.Model, existing, false)
	if err != nil {
		return nil, err
	}
	key, ciphertext, last4, err := s.resolveKey(input.APIKey, existing)
	if err != nil {
		return nil, err
	}
	result, status := s.probe(ctx, kind, baseURL, model, key)
	if !result.OK {
		return nil, &TestFailedError{Status: status, Message: result.Error}
	}
	// An omitted "enabled" must not silently disable a working configuration.
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}
	testedAt := s.now()
	testedOK := true
	config := Config{
		UserID:           userID,
		Enabled:          enabled,
		Kind:             kind,
		BaseURL:          baseURL,
		Model:            model,
		APIKeyCiphertext: ciphertext,
		APIKeyLast4:      last4,
		LastTestAt:       &testedAt,
		LastTestOK:       &testedOK,
	}
	if err := s.store.Upsert(ctx, config); err != nil {
		return nil, err
	}
	return viewOf(&config), nil
}

// Test runs a minimal request against the supplied or stored configuration.
func (s *Service) Test(ctx context.Context, userID int64, input Input) (TestResult, error) {
	if s.limiter != nil && !s.limiter.Allow(ctx, userID) {
		return TestResult{}, ErrRateLimited
	}
	existing, err := s.store.Get(ctx, userID)
	if err != nil {
		return TestResult{}, err
	}
	kind, baseURL, model, err := s.validateFields(input.Kind, input.BaseURL, input.Model, existing, true)
	if err != nil {
		return TestResult{}, err
	}
	key, _, _, err := s.resolveKey(input.APIKey, existing)
	if err != nil {
		return TestResult{}, err
	}
	result, _ := s.probe(ctx, kind, baseURL, model, key)
	return result, nil
}

// Delete removes the configuration and returns the user to the platform model.
func (s *Service) Delete(ctx context.Context, userID int64) error {
	return s.store.Delete(ctx, userID)
}

func (s *Service) validateFields(kind, baseURL, model string, existing *Config, allowFallback bool) (string, string, string, error) {
	if allowFallback && existing != nil {
		if strings.TrimSpace(kind) == "" {
			kind = existing.Kind
		}
		if strings.TrimSpace(baseURL) == "" {
			baseURL = existing.BaseURL
		}
		if strings.TrimSpace(model) == "" {
			model = existing.Model
		}
	}
	kind = strings.TrimSpace(kind)
	baseURL = strings.TrimSpace(baseURL)
	model = strings.TrimSpace(model)

	fields := map[string]string{}
	if kind != llm.KindOpenAICompatible && kind != llm.KindAnthropic {
		fields["kind"] = "must_be_openai_compatible_or_anthropic"
	}
	if _, err := llm.ValidateBaseURL(baseURL, s.allowInsecure); err != nil {
		fields["base_url"] = "must_be_a_public_https_url"
	}
	if n := utf8.RuneCountInString(model); n < 1 || n > 128 {
		fields["model"] = "must_be_between_1_and_128_characters"
	}
	if len(fields) > 0 {
		return "", "", "", &ValidationError{Fields: fields}
	}
	return kind, baseURL, model, nil
}

func (s *Service) resolveKey(input *string, existing *Config) (key, ciphertext, last4 string, err error) {
	if input != nil && strings.TrimSpace(*input) != "" {
		key = strings.TrimSpace(*input)
		if s.cipher == nil {
			return "", "", "", errors.New("llm key encryption is not configured")
		}
		ciphertext, err = s.cipher.Encrypt(key)
		if err != nil {
			return "", "", "", err
		}
		return key, ciphertext, keyLast4(key), nil
	}
	if existing == nil {
		return "", "", "", &ValidationError{Fields: map[string]string{"api_key": "is_required"}}
	}
	if s.cipher == nil {
		return "", "", "", errors.New("llm key encryption is not configured")
	}
	key, err = s.cipher.Decrypt(existing.APIKeyCiphertext)
	if err != nil {
		return "", "", "", err
	}
	return key, existing.APIKeyCiphertext, existing.APIKeyLast4, nil
}

func keyLast4(key string) string {
	runes := []rune(key)
	if len(runes) <= 4 {
		// Never echo a short key in full; real provider keys are much longer.
		return strings.Repeat("*", len(runes))
	}
	return string(runes[len(runes)-4:])
}

func (s *Service) probe(ctx context.Context, kind, baseURL, model, key string) (TestResult, int) {
	start := s.now()
	chat, err := s.newModel(kind, baseURL, model, key)
	if err != nil {
		return TestResult{OK: false, Error: llm.RedactSecrets(err.Error(), key)}, 0
	}
	timeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = chat.Stream(timeout, llm.Request{
		Messages:  []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "ping"}}}},
		MaxTokens: 1,
	}, nil)
	latency := s.now().Sub(start).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if err != nil {
		status := 0
		var upstream *llm.UpstreamError
		if errors.As(err, &upstream) {
			status = upstream.Status
		}
		return TestResult{OK: false, LatencyMS: latency, Error: llm.SanitizeError(err, key)}, status
	}
	return TestResult{OK: true, LatencyMS: latency}, 0
}

func viewOf(config *Config) *View {
	return &View{
		Enabled:     config.Enabled,
		Configured:  true,
		Kind:        config.Kind,
		BaseURL:     config.BaseURL,
		Model:       config.Model,
		APIKeyLast4: config.APIKeyLast4,
		LastTestAt:  config.LastTestAt,
		LastTestOK:  config.LastTestOK,
	}
}

// compile-time guard: the production store also feeds the resolver.
var _ llm.ConfigStore = (*MySQLStore)(nil)
