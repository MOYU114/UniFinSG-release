package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type CognitoConfig struct {
	Region       string
	UserPoolID   string
	ClientID     string
	ClientSecret string
	Domain       string
	RedirectURI  string
	FrontendURL  string
}

func CognitoConfigFromEnv() CognitoConfig {
	return CognitoConfig{
		Region: os.Getenv("COGNITO_REGION"), UserPoolID: os.Getenv("COGNITO_USER_POOL_ID"),
		ClientID: os.Getenv("COGNITO_CLIENT_ID"), ClientSecret: os.Getenv("COGNITO_CLIENT_SECRET"),
		Domain: os.Getenv("COGNITO_DOMAIN"), RedirectURI: os.Getenv("COGNITO_REDIRECT_URI"),
		FrontendURL: os.Getenv("FRONTEND_URL"),
	}
}

type CognitoOption func(*CognitoHandler)

func WithCognitoHTTPClient(client *http.Client) CognitoOption {
	return func(handler *CognitoHandler) {
		if client != nil {
			copy := *client
			handler.client = &copy
		}
	}
}

func WithCognitoClock(now func() time.Time) CognitoOption {
	return func(handler *CognitoHandler) {
		if now != nil {
			handler.now = now
		}
	}
}

var cognitoRegionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

func validateCognitoConfig(config CognitoConfig) (CognitoConfig, error) {
	if !cognitoRegionPattern.MatchString(config.Region) ||
		!regexp.MustCompile("^"+regexp.QuoteMeta(config.Region)+`_[A-Za-z0-9]+$`).MatchString(config.UserPoolID) {
		return CognitoConfig{}, errors.New("Cognito region and user pool ID are required and must match")
	}
	if strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(config.ClientSecret) == "" {
		return CognitoConfig{}, errors.New("Cognito client ID and client secret are required")
	}
	if !strings.Contains(config.Domain, "://") {
		config.Domain = "https://" + config.Domain
	}
	domain, err := url.Parse(config.Domain)
	if err != nil || domain.Scheme != "https" || domain.Host == "" || domain.Hostname() == "" ||
		domain.User != nil || (domain.Path != "" && domain.Path != "/") ||
		domain.RawQuery != "" || domain.Fragment != "" {
		return CognitoConfig{}, errors.New("Cognito domain must be an HTTPS origin")
	}
	config.Domain = strings.TrimRight(domain.String(), "/")
	for _, field := range []struct {
		name  string
		value string
	}{
		{"Cognito redirect URI", config.RedirectURI}, {"frontend URL", config.FrontendURL},
	} {
		parsed, err := url.Parse(field.value)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" ||
			parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return CognitoConfig{}, errors.New(field.name + " must be an absolute HTTP(S) URL without query or fragment")
		}
		if parsed.Scheme == "http" && parsed.Hostname() != "localhost" &&
			parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
			return CognitoConfig{}, errors.New(field.name + " must use HTTPS outside localhost")
		}
	}
	config.FrontendURL = strings.TrimRight(config.FrontendURL, "/")
	return config, nil
}

func cognitoFlowCipher(clientSecret string) (cipher.AEAD, error) {
	// The confidential client secret is shared across deployment instances.
	// Domain separation derives a distinct cookie-encryption key; no extra
	// process-local secret or new deployment environment variable is needed.
	key := sha256.Sum256([]byte("unifinsg:cognito-flow:v1:" + clientSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func safeCognitoRedirect(value string) (string, bool) {
	if value == "" {
		return "/dashboard", true
	}
	decoded := value
	for i := 0; i < 10; i++ {
		if !strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "//") ||
			strings.Contains(decoded, `\`) || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
			return "", false
		}
		parsed, err := url.Parse(decoded)
		if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
			return "", false
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		if next == decoded {
			return value, true
		}
		decoded = next
	}
	return "", false
}
