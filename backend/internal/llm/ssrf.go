package llm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxResponseBytes bounds a single provider response body.
	MaxResponseBytes = 8 << 20
	// RequestTimeout is the hard ceiling for one model call.
	RequestTimeout = 35 * time.Second
)

var errRedirectBlocked = errors.New("llm: redirects are not allowed")

// AllowInsecureFromEnv reports LLM_ALLOW_INSECURE_BYOK. It is the only place
// that turns the development switch on.
func AllowInsecureFromEnv() bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("LLM_ALLOW_INSECURE_BYOK")))
	return err == nil && value
}

// NewHTTPClient builds the SSRF-safe client used for provider calls. Loopback
// is only reachable when allowInsecure is true (local development). Private,
// link-local (including 169.254.169.254), unspecified and multicast addresses
// are always rejected, at connect time, after DNS resolution.
func NewHTTPClient(allowInsecure bool) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			return checkDialAddress(address, allowInsecure)
		},
	}
	transport := &http.Transport{
		// A proxy would make the destination address the proxy, bypassing the
		// connect-time IP check.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errRedirectBlocked
		},
	}
}

// ValidateBaseURL enforces the scheme and host rules before a BYOK URL is
// stored. The connect-time check remains the authoritative SSRF defence.
func ValidateBaseURL(raw string, allowInsecure bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("base URL is invalid")
	}
	if u.User != nil {
		return nil, errors.New("base URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("base URL must not contain a query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !allowInsecure {
			return nil, errors.New("base URL must use https")
		}
	default:
		return nil, errors.New("base URL must use https")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("base URL host is required")
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip, allowInsecure) {
		return nil, errors.New("base URL host is not allowed")
	}
	return u, nil
}

func checkDialAddress(address string, allowInsecure bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("llm: invalid dial address")
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || isBlockedIP(ip, allowInsecure) {
		return errors.New("llm: destination address is not allowed")
	}
	return nil
}

func isBlockedIP(ip net.IP, allowInsecure bool) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() {
		return !allowInsecure
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		// Cloud metadata endpoints live in 169.254.0.0/16, already covered by
		// IsLinkLocalUnicast, but keep the explicit guard for clarity.
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
	}
	return false
}

// NewPublicFetchClient builds an SSRF-safe client for fetching public web
// pages (the agent's web_search tool). Every connection, including those made
// while following a redirect, goes through the same connect-time IP check as
// provider calls, so private, link-local and metadata addresses stay
// unreachable. Loopback is always blocked here: there is no development switch.
func NewPublicFetchClient(timeout time.Duration, maxRedirects int) *http.Client {
	client := NewHTTPClient(false)
	client.Timeout = timeout
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errRedirectBlocked
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errRedirectBlocked
		}
		return nil
	}
	return client
}
