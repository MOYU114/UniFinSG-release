// Package llmconfig stores and serves per-user BYOK model configuration.
package llmconfig

import (
	"errors"
	"time"
)

// Config is one llm_configs row.
type Config struct {
	UserID           int64
	Enabled          bool
	Kind             string
	BaseURL          string
	Model            string
	APIKeyCiphertext string
	APIKeyLast4      string
	LastTestAt       *time.Time
	LastTestOK       *bool
}

// Input is a save or test request. A nil APIKey means "reuse the stored key".
type Input struct {
	Kind    string
	BaseURL string
	Model   string
	APIKey  *string
	// Enabled is nil when the request omitted the field. PUT then keeps the
	// stored value (or enables a brand-new configuration).
	Enabled *bool
}

// View is the only representation returned over HTTP. It never contains the
// API key or its ciphertext.
type View struct {
	Enabled     bool       `json:"enabled"`
	Configured  bool       `json:"configured"`
	Kind        string     `json:"kind,omitempty"`
	BaseURL     string     `json:"base_url,omitempty"`
	Model       string     `json:"model,omitempty"`
	APIKeyLast4 string     `json:"api_key_last4,omitempty"`
	LastTestAt  *time.Time `json:"last_test_at,omitempty"`
	LastTestOK  *bool      `json:"last_test_ok,omitempty"`
}

// TestResult is the response of the connectivity endpoint.
type TestResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ValidationError maps to 400 validation_failed.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "llm configuration validation failed" }

// TestFailedError maps to 422 llm_test_failed. Message is already sanitised.
type TestFailedError struct {
	Status  int
	Message string
}

func (e *TestFailedError) Error() string { return "llm connectivity test failed: " + e.Message }

// ErrRateLimited maps to 429.
var ErrRateLimited = errors.New("too many model connection tests")
