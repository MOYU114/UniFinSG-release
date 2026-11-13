package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCipherRoundTripsRawAndBase64Keys(t *testing.T) {
	raw := strings.Repeat("k", 32)
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
	for name, key := range map[string]string{"raw": raw, "base64": encoded} {
		t.Run(name, func(t *testing.T) {
			cipher, err := NewCipherFromKey(key)
			if err != nil {
				t.Fatalf("cipher: %v", err)
			}
			sealed, err := cipher.Encrypt("secret-value")
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			if strings.Contains(sealed, "secret-value") {
				t.Fatal("ciphertext contains plaintext")
			}
			plain, err := cipher.Decrypt(sealed)
			if err != nil || plain != "secret-value" {
				t.Fatalf("decrypt: %q %v", plain, err)
			}
		})
	}
}

func TestCipherRejectsInvalidKeysAndCiphertext(t *testing.T) {
	if _, err := NewCipherFromKey("short"); err == nil {
		t.Fatal("short key was accepted")
	}
	cipher, err := NewCipherFromKey(strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	if _, err := cipher.Decrypt("not-base64"); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
	sealed, err := cipher.Encrypt("value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	tampered := []byte(sealed)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := cipher.Decrypt(string(tampered)); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("expected ErrInvalidCiphertext for tampered data, got %v", err)
	}
}
