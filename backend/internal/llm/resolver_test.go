package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"unichatgo/internal/crypto"
)

type fakeConfigStore struct {
	config *UserConfig
	err    error
}

func (f *fakeConfigStore) GetLLMConfig(context.Context, int64) (*UserConfig, error) {
	return f.config, f.err
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	cipher, err := crypto.NewCipherFromKey(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return cipher
}

func TestResolverPrefersEnabledBYOK(t *testing.T) {
	cipher := testCipher(t)
	sealed, err := cipher.Encrypt("user-api-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	resolver := NewResolver(&fakeConfigStore{config: &UserConfig{
		Enabled:          true,
		Kind:             KindOpenAICompatible,
		BaseURL:          "https://api.example.com",
		Model:            "custom-model",
		APIKeyCiphertext: sealed,
	}}, cipher, PlatformConfig{Kind: KindOpenAICompatible, BaseURL: "https://platform.example.com", Model: "platform-model"}, false)

	_, mode, err := resolver.ResolveForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mode != ModeBYOK {
		t.Fatalf("unexpected mode %q", mode)
	}
	if mode, err := resolver.ModeForUser(context.Background(), 1); err != nil || mode != ModeBYOK {
		t.Fatalf("mode lookup: %q %v", mode, err)
	}
}

func TestResolverFallsBackToPlatform(t *testing.T) {
	resolver := NewResolver(&fakeConfigStore{config: &UserConfig{Enabled: false}}, testCipher(t),
		PlatformConfig{Kind: KindAnthropic, BaseURL: "https://platform.example.com", Model: "platform-model"}, false)
	_, mode, err := resolver.ResolveForUser(context.Background(), 1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mode != ModePlatform {
		t.Fatalf("unexpected mode %q", mode)
	}
}

func TestResolverReportsMissingConfiguration(t *testing.T) {
	resolver := NewResolver(&fakeConfigStore{}, nil, PlatformConfig{}, false)
	if _, _, err := resolver.ResolveForUser(context.Background(), 1); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if _, err := resolver.ModeForUser(context.Background(), 1); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}
