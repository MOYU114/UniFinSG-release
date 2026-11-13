package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrCognitoTokenInvalid = errors.New("Cognito ID token is invalid")
	ErrCognitoUnavailable  = errors.New("Cognito is unavailable")
)

type CognitoIdentity struct {
	Subject  string
	Email    string
	Username string
}

type cognitoJWTVerifier struct {
	issuer   string
	clientID string
	client   *http.Client
	now      func() time.Time
	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	expires  time.Time
}

func (verifier *cognitoJWTVerifier) verify(ctx context.Context, token, expectedNonce string) (CognitoIdentity, error) {
	if len(token) > 64*1024 || expectedNonce == "" {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	var header struct {
		Algorithm string   `json:"alg"`
		KeyID     string   `json:"kid"`
		Critical  []string `json:"crit"`
	}
	if err := decodeJWTPart(parts[0], &header); err != nil || header.Algorithm != "RS256" ||
		header.KeyID == "" || len(header.KeyID) > 256 || len(header.Critical) > 0 {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	key, err := verifier.key(ctx, header.KeyID)
	if err != nil {
		return CognitoIdentity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	var claims struct {
		Issuer          string          `json:"iss"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		Expires         int64           `json:"exp"`
		IssuedAt        int64           `json:"iat"`
		NotBefore       int64           `json:"nbf"`
		TokenUse        string          `json:"token_use"`
		Subject         string          `json:"sub"`
		Nonce           string          `json:"nonce"`
		Email           string          `json:"email"`
		Username        string          `json:"cognito:username"`
	}
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	now := verifier.now().Unix()
	if claims.Issuer != verifier.issuer || !validCognitoAudience(claims.Audience, claims.AuthorizedParty, verifier.clientID) ||
		claims.Expires <= now || claims.IssuedAt > now+60 || claims.NotBefore > now ||
		claims.TokenUse != "id" || !validCognitoSubject(claims.Subject) ||
		subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 ||
		!validCognitoDisplayString(claims.Email, 320) || !validCognitoDisplayString(claims.Username, 128) {
		return CognitoIdentity{}, ErrCognitoTokenInvalid
	}
	return CognitoIdentity{Subject: claims.Subject, Email: claims.Email, Username: claims.Username}, nil
}

// OIDC subjects are opaque, case-sensitive identifiers of at most 255 ASCII
// characters. Cognito does not promise a particular UUID format. Never trim,
// case-fold, parse, or otherwise normalize a subject before identity lookup.
func validCognitoSubject(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for i := range value {
		if value[i] > 127 {
			return false
		}
	}
	return true
}

func validCognitoDisplayString(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validCognitoAudience(raw json.RawMessage, party, clientID string) bool {
	if party != "" && party != clientID {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == clientID
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil || len(multiple) == 0 {
		return false
	}
	if len(multiple) > 1 && party != clientID {
		return false
	}
	for _, audience := range multiple {
		if audience == clientID {
			return true
		}
	}
	return false
}

func decodeJWTPart(encoded string, target any) error {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func (verifier *cognitoJWTVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if key := verifier.keys[kid]; key != nil && verifier.now().Before(verifier.expires) {
		return key, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, verifier.issuer+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, ErrCognitoUnavailable
	}
	response, err := verifier.client.Do(request)
	if err != nil {
		return nil, ErrCognitoUnavailable
	}
	defer response.Body.Close()
	body, err := readCognitoBody(response.Body, 1<<20)
	if err != nil || response.StatusCode != http.StatusOK {
		return nil, ErrCognitoUnavailable
	}
	var document struct {
		Keys []struct {
			Type       string   `json:"kty"`
			ID         string   `json:"kid"`
			Algorithm  string   `json:"alg"`
			Use        string   `json:"use"`
			Operations []string `json:"key_ops"`
			Modulus    string   `json:"n"`
			Exponent   string   `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, ErrCognitoUnavailable
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, entry := range document.Keys {
		if entry.Type != "RSA" || entry.ID == "" || len(entry.ID) > 256 ||
			(entry.Algorithm != "" && entry.Algorithm != "RS256") || (entry.Use != "" && entry.Use != "sig") {
			continue
		}
		if len(entry.Operations) > 0 {
			verifies := false
			for _, operation := range entry.Operations {
				verifies = verifies || operation == "verify"
			}
			if !verifies {
				continue
			}
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(entry.Modulus)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(entry.Exponent)
		if modulusErr != nil || exponentErr != nil || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		n := new(big.Int).SetBytes(modulus)
		e := new(big.Int).SetBytes(exponent).Int64()
		if n.BitLen() < 2048 || n.BitLen() > 8192 || e < 3 || e > 1<<31-1 || e%2 == 0 {
			continue
		}
		if keys[entry.ID] != nil {
			return nil, ErrCognitoUnavailable
		}
		keys[entry.ID] = &rsa.PublicKey{N: n, E: int(e)}
	}
	if len(keys) == 0 {
		return nil, ErrCognitoUnavailable
	}
	verifier.keys, verifier.expires = keys, verifier.now().Add(time.Hour)
	if key := keys[kid]; key != nil {
		return key, nil
	}
	return nil, ErrCognitoTokenInvalid
}

func readCognitoBody(body io.Reader, maximum int64) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil || int64(len(value)) > maximum {
		return nil, ErrCognitoUnavailable
	}
	return value, nil
}
