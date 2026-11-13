package eval

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTrustedProxyConfiguration(t *testing.T) {
	for _, raw := range []string{"", " \t\n "} {
		resolver, err := NewTrustedProxyIPResolver(raw)
		if err != nil || resolver != nil {
			t.Fatalf("empty config should preserve socket-only handler behavior: hasResolver=%t err=%v", resolver != nil, err)
		}
	}
	for _, raw := range []string{
		"127.0.0.1", "proxy.example/24", "10.0.0.0/33", "2001:db8::/129",
		"10.0.0.0/8,", ",10.0.0.0/8", "10.0.0.0/8,,::1/128",
		"10.0.0.0/8 invalid", "[2001:db8::]/64", "fe80::%en0/64",
		"::ffff:192.0.2.1/95",
	} {
		if resolver, err := NewTrustedProxyIPResolver(raw); err == nil || resolver != nil {
			t.Errorf("malformed trust configuration accepted: %q", raw)
		}
	}
	for _, raw := range []string{
		" 10.0.0.2/8 , 2001:db8::1/32 ", "127.0.0.1/32",
		"::ffff:192.0.2.7/120", "::ffff:0.0.0.0/96", "0.0.0.0/0,::/0",
	} {
		if resolver, err := NewTrustedProxyIPResolver(raw); err != nil || resolver == nil {
			t.Errorf("valid trust configuration rejected: %q err=%v", raw, err)
		}
	}
}

func TestTrustedProxyClientBoundary(t *testing.T) {
	resolver, err := NewTrustedProxyIPResolver("10.0.0.0/8,2001:db8:ffff::/48,fe80::/10")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remote string
		values []string
		want   string
	}{
		{"untrusted peer ignores spoofed headers", "198.51.100.7:1234", []string{"192.0.2.44"}, "198.51.100.7"},
		{"untrusted peer ignores malformed headers", "198.51.100.7:1234", []string{"unknown", ","}, "198.51.100.7"},
		{"trusted socket without XFF", "10.0.0.7:1234", nil, "10.0.0.7"},
		{"single client", "10.0.0.7:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		{"trusted proxy chain", "10.0.0.7:1234", []string{"198.51.100.7, 10.0.0.8, 10.0.0.9"}, "198.51.100.7"},
		{"stop before forged left client", "10.0.0.7:1234", []string{"192.0.2.44,198.51.100.7,10.0.0.9"}, "198.51.100.7"},
		{"malformed left prefix ignored after boundary", "10.0.0.7:1234", []string{"unknown,,198.51.100.7,10.0.0.9"}, "198.51.100.7"},
		{"all trusted fallback", "10.0.0.7:1234", []string{"10.0.0.8,10.0.0.9"}, "10.0.0.7"},
		{"multiple header lines follow wire order", "10.0.0.7:1234", []string{"192.0.2.44,198.51.100.7", "10.0.0.8,10.0.0.9"}, "198.51.100.7"},
		{"untrusted last header ends chain", "10.0.0.7:1234", []string{"192.0.2.44", "198.51.100.7"}, "198.51.100.7"},
		{"last header cannot be skipped when malformed", "10.0.0.7:1234", []string{"198.51.100.7", "unknown"}, "10.0.0.7"},
		{"invalid trusted suffix falls back", "10.0.0.7:1234", []string{"198.51.100.7,unknown,10.0.0.8"}, "10.0.0.7"},
		{"empty final header falls back", "10.0.0.7:1234", []string{"198.51.100.7", ""}, "10.0.0.7"},
		{"trailing comma falls back", "10.0.0.7:1234", []string{"198.51.100.7,"}, "10.0.0.7"},
		{"IPv4 client port", "10.0.0.7:1234", []string{"198.51.100.7:49152,10.0.0.8:123"}, "198.51.100.7"},
		{"IPv6 client port", "10.0.0.7:1234", []string{"[2001:db8:1234::1]:49152"}, "2001:db8:1234::1"},
		{"IPv6 client", "[2001:db8:ffff::7]:1234", []string{"2001:db8:1234::1,2001:db8:ffff::8"}, "2001:db8:1234::1"},
		{"untrusted IPv6 peer", "[2001:db8:1234::7]:1234", []string{"192.0.2.44"}, "2001:db8:1234::7"},
		{"mapped IPv4 socket", "[::ffff:10.0.0.7]:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		{"mapped IPv4 client", "10.0.0.7:1234", []string{"::ffff:198.51.100.7,::ffff:10.0.0.8"}, "198.51.100.7"},
		{"mapped IPv4 client port", "10.0.0.7:1234", []string{"[::ffff:198.51.100.7]:1234"}, "198.51.100.7"},
		{"HTTP whitespace", "10.0.0.7:1234", []string{" \t198.51.100.7\t, \t10.0.0.8 "}, "198.51.100.7"},
		{"remote zone normalized", "[fe80::7%en0]:1234", []string{"198.51.100.7"}, "198.51.100.7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/eval/events", nil)
			request.RemoteAddr = test.remote
			for _, value := range test.values {
				request.Header.Add("X-Forwarded-For", value)
			}
			request.Header.Set("X-Real-IP", "192.0.2.200")
			request.Header.Set("Forwarded", "for=192.0.2.201")
			got, err := resolver(request)
			if err != nil || got != test.want {
				t.Fatalf("client IP=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
}

func TestTrustedProxyMalformedSuffixCannotSelectEarlierClient(t *testing.T) {
	resolver, err := NewTrustedProxyIPResolver("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		"", " ", "unknown", "example.org", `"198.51.100.7"`, "for=198.51.100.7",
		"[2001:db8::1]", "fe80::1%en0", "[fe80::1%en0]:123",
		"198.51.100.7:70000", "198.51.100.7:http", "010.0.0.1", "2130706433",
		"198.51.100.7\n", "\u00a0198.51.100.7", "198.51.100.7\u00a0",
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/eval/events", nil)
		request.RemoteAddr = "10.0.0.7:1234"
		request.Header.Set("X-Forwarded-For", "192.0.2.44,"+invalid+",10.0.0.8")
		got, err := resolver(request)
		if err != nil || got != "10.0.0.7" {
			t.Fatalf("malformed suffix %q selected earlier header IP: got=%q err=%v", invalid, got, err)
		}
	}
}

func TestTrustedProxyMappedCIDRAndInvalidRemote(t *testing.T) {
	resolver, err := NewTrustedProxyIPResolver("::ffff:10.0.0.7/104")
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{"10.0.0.1:80", "[::ffff:10.0.0.1]:80"} {
		request := httptest.NewRequest(http.MethodPost, "/api/eval/events", nil)
		request.RemoteAddr = remote
		request.Header.Set("X-Forwarded-For", "198.51.100.7")
		got, err := resolver(request)
		if err != nil || got != "198.51.100.7" {
			t.Fatalf("mapped trust prefix differed between equivalent remote IPs: got=%q err=%v", got, err)
		}
	}
	for _, remote := range []string{"", "10.0.0.7", "example.org:1234", "10.0.0.7:http", "[::1]:99999"} {
		request := httptest.NewRequest(http.MethodPost, "/api/eval/events", nil)
		request.RemoteAddr = remote
		request.Header.Set("X-Forwarded-For", "198.51.100.7")
		if got, err := resolver(request); err == nil || got != "" {
			t.Fatalf("invalid connection address trusted a header: remote=%q got=%q err=%v", remote, got, err)
		}
	}
	if got, err := resolver(nil); err == nil || got != "" {
		t.Fatalf("nil request should fail safely: got=%q err=%v", got, err)
	}
}

func TestTrustedProxyResolverIsImmutableAcrossRequests(t *testing.T) {
	resolver, err := NewTrustedProxyIPResolver("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request := httptest.NewRequest(http.MethodPost, "/api/eval/events", strings.NewReader(`{}`))
			request.RemoteAddr = "10.0.0.7:1234"
			request.Header.Set("X-Forwarded-For", "198.51.100.7")
			got, err := resolver(request)
			if err != nil || got != "198.51.100.7" {
				t.Errorf("concurrent client resolution: got=%q err=%v", got, err)
			}
		}()
	}
	workers.Wait()
}
