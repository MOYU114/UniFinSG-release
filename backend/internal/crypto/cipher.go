// Package crypto provides the AES-256-GCM envelope used for secrets that must
// be stored at rest, such as provider API keys and execution credentials.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// KeyEnv is the environment variable holding the 32-byte encryption key. The
// value is either 32 raw characters or standard base64 for 32 bytes.
const KeyEnv = "UNICHATGO_APIKEY_KEY"

// ErrInvalidCiphertext is returned when a stored value cannot be opened with
// the configured key. It never contains the ciphertext or the key.
var ErrInvalidCiphertext = errors.New("invalid token ciphertext")

// Cipher seals and opens short secrets with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// NewCipherFromKey accepts either 32 raw characters or standard base64 that
// decodes to 32 bytes.
func NewCipherFromKey(raw string) (*Cipher, error) {
	key, err := DecodeKey(raw)
	if err != nil {
		return nil, err
	}
	return NewCipher(key)
}

// NewCipherFromEnv builds the Cipher from KeyEnv.
func NewCipherFromEnv() (*Cipher, error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, fmt.Errorf("%s not set", KeyEnv)
	}
	c, err := NewCipherFromKey(raw)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", KeyEnv, err)
	}
	return c, nil
}

// DecodeKey normalises a configured key into 32 raw bytes.
func DecodeKey(raw string) ([]byte, error) {
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid key length %d, want 32", len(key))
	}
	return key, nil
}

// KeyFingerprint identifies the configured key without revealing it.
func KeyFingerprint(raw string) (string, error) {
	key, err := DecodeKey(raw)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(key)
	return hex.EncodeToString(hash[:]), nil
}

// KeyFingerprintFromEnv reads KeyEnv and returns its fingerprint.
func KeyFingerprintFromEnv() (string, error) {
	return KeyFingerprint(strings.TrimSpace(os.Getenv(KeyEnv)))
}

// Encrypt returns base64(nonce || ciphertext).
func (c *Cipher) Encrypt(plain string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	cipherText := c.aead.Seal(nil, nonce, []byte(plain), nil)
	buf := append(nonce, cipherText...)
	return base64.StdEncoding.EncodeToString(buf), nil
}

// Decrypt opens a value produced by Encrypt.
func (c *Cipher) Decrypt(input string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	ns := c.aead.NonceSize()
	if len(data) < ns {
		return "", ErrInvalidCiphertext
	}
	nonce := data[:ns]
	cipherText := data[ns:]
	plain, err := c.aead.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return "", ErrInvalidCiphertext
	}
	return string(plain), nil
}
