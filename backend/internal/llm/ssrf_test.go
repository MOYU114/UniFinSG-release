package llm

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlockedIPClassification(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "::ffff:127.0.0.1",
		"10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "fe80::1", "fc00::1", "fd12:3456::1",
		"0.0.0.0", "::", "224.0.0.1", "ff02::1",
	}
	for _, raw := range blocked {
		if !isBlockedIP(net.ParseIP(raw), false) {
			t.Errorf("%s should be blocked", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if isBlockedIP(net.ParseIP(raw), false) {
			t.Errorf("%s should be allowed", raw)
		}
	}
	// The development switch only relaxes loopback, never private or metadata.
	if isBlockedIP(net.ParseIP("127.0.0.1"), true) {
		t.Error("loopback should be allowed with the development switch")
	}
	for _, raw := range []string{"10.1.2.3", "192.168.1.1", "169.254.169.254"} {
		if !isBlockedIP(net.ParseIP(raw), true) {
			t.Errorf("%s must stay blocked with the development switch", raw)
		}
	}
}

func TestCheckDialAddress(t *testing.T) {
	if err := checkDialAddress("127.0.0.1:443", false); err == nil {
		t.Fatal("loopback dial address was accepted")
	}
	if err := checkDialAddress("127.0.0.1:443", true); err != nil {
		t.Fatalf("loopback should be allowed in development: %v", err)
	}
	for _, address := range []string{"10.0.0.1:80", "[::1]:80", "[fc00::1]:80", "169.254.169.254:80", "not-an-address"} {
		if err := checkDialAddress(address, false); err == nil {
			t.Errorf("%s was accepted", address)
		}
	}
	if err := checkDialAddress("8.8.8.8:443", false); err != nil {
		t.Fatalf("public address rejected: %v", err)
	}
}

func TestValidateBaseURL(t *testing.T) {
	if _, err := ValidateBaseURL("https://api.example.com/v1", false); err != nil {
		t.Fatalf("https URL rejected: %v", err)
	}
	if _, err := ValidateBaseURL("http://api.example.com", false); err == nil {
		t.Fatal("http URL accepted without the development switch")
	}
	if _, err := ValidateBaseURL("http://localhost:11434", true); err != nil {
		t.Fatalf("local development URL rejected: %v", err)
	}
	for _, raw := range []string{
		"", "ftp://api.example.com", "https://user:pass@api.example.com",
		"https://api.example.com?x=1", "https://api.example.com#frag",
		"https://10.0.0.5", "https://169.254.169.254", "https://[::1]",
	} {
		if _, err := ValidateBaseURL(raw, false); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	if _, err := ValidateBaseURL("https://[::1]", true); err != nil {
		t.Fatalf("loopback literal rejected with the development switch: %v", err)
	}
}

func TestAllowInsecureFromEnv(t *testing.T) {
	t.Setenv("LLM_ALLOW_INSECURE_BYOK", "true")
	if !AllowInsecureFromEnv() {
		t.Fatal("true should enable the development switch")
	}
	t.Setenv("LLM_ALLOW_INSECURE_BYOK", "false")
	if AllowInsecureFromEnv() {
		t.Fatal("false should keep the switch off")
	}
	t.Setenv("LLM_ALLOW_INSECURE_BYOK", "")
	if AllowInsecureFromEnv() {
		t.Fatal("an empty value should keep the switch off")
	}
}

func TestHTTPClientGatesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	if _, err := NewHTTPClient(false).Get(server.URL); err == nil {
		t.Fatal("loopback request succeeded without the development switch")
	}
	resp, err := NewHTTPClient(true).Get(server.URL)
	if err != nil {
		t.Fatalf("loopback request failed with the development switch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}
}

func TestHTTPClientRejectsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "should not be reached")
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	_, err := NewHTTPClient(true).Get(redirect.URL)
	if err == nil {
		t.Fatal("redirect was followed")
	}
	if !errors.Is(err, errRedirectBlocked) {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestLimitedBodyStopsAtEightMegabytes(t *testing.T) {
	body := newLimitedBody(io.NopCloser(strings.NewReader(strings.Repeat("a", MaxResponseBytes+1))))
	if _, err := io.ReadAll(body); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("expected the size limit error, got %v", err)
	}
	exact := newLimitedBody(io.NopCloser(strings.NewReader(strings.Repeat("a", MaxResponseBytes))))
	if _, err := io.ReadAll(exact); err != nil {
		t.Fatalf("an exactly 8MB body should be allowed: %v", err)
	}
}
