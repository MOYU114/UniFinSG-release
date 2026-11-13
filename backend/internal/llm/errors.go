package llm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrUnknownKind is returned when a configuration names an unsupported adapter.
var ErrUnknownKind = errors.New("unsupported llm adapter kind")

const maxUpstreamMessage = 300

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bsk-ant-[a-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._\-]{8,}`),
	regexp.MustCompile(`(?i)x-api-key["'\s:=]+[a-z0-9._\-]{8,}`),
}

// UpstreamError wraps a non-success provider response. Message is already
// truncated and stripped of anything that resembles an API key.
type UpstreamError struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func (e *UpstreamError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("llm upstream %d: %s", e.Status, e.Message)
	}
	return "llm upstream error: " + e.Message
}

// SanitizeError returns a short, key-free description safe for HTTP responses
// and logs. apiKey may be empty when the key is not known at this layer.
func SanitizeError(err error, apiKey string) string {
	if err == nil {
		return ""
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return RedactSecrets(upstream.Message, apiKey)
	}
	return RedactSecrets(err.Error(), apiKey)
}

// RedactSecrets removes the exact key (when supplied) and common key-shaped
// tokens, then truncates the result to 300 characters.
func RedactSecrets(text, apiKey string) string {
	text = strings.TrimSpace(text)
	if apiKey != "" {
		text = strings.ReplaceAll(text, apiKey, "[redacted]")
	}
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "[redacted]")
	}
	if utf8.RuneCountInString(text) > maxUpstreamMessage {
		runes := []rune(text)
		text = string(runes[:maxUpstreamMessage])
	}
	return text
}

func upstreamError(status int, body, apiKey string) *UpstreamError {
	return &UpstreamError{Status: status, Message: RedactSecrets(body, apiKey)}
}
