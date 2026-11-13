package assistant

import (
	"unichatgo/internal/crypto"
)

const apiTokenKeyEnv = crypto.KeyEnv

// tokenCipher is the shared AES-GCM envelope. The alias keeps the assistant
// package's field type and behaviour unchanged after the extraction.
type tokenCipher = crypto.Cipher

// errInvalidCiphertext is retained for the legacy plaintext-token fallback in
// decryptToken.
var errInvalidCiphertext = crypto.ErrInvalidCiphertext

// SealExecution freezes credentials without putting plaintext in the queue.
func (s *Service) SealExecution(plain string) (string, error) {
	return s.cipher.Encrypt(plain)
}

// OpenExecution never applies the legacy plaintext-token compatibility fallback.
func (s *Service) OpenExecution(sealed string) (string, error) {
	return s.cipher.Decrypt(sealed)
}

// ExecutionKeyFingerprint binds all cluster roles to the same decryption key.
func ExecutionKeyFingerprint() (string, error) {
	return crypto.KeyFingerprintFromEnv()
}

func newTokenCipherFromEnv() (*tokenCipher, error) {
	return crypto.NewCipherFromEnv()
}
