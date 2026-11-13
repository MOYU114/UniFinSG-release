package eval

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// NewTrustedProxyIPResolver parses the comma-separated TRUSTED_PROXY_CIDRS
// configuration. An empty value returns nil so the HTTP handler retains its
// existing socket-only behavior. A malformed entry must fail application startup.
//
// Only a trusted socket peer can supply X-Forwarded-For. Entries are walked
// right-to-left across all header lines, stopping at the first untrusted hop.
// That hop is the client boundary; anything to its left is client-controlled.
func NewTrustedProxyIPResolver(rawCIDRs string) (func(*http.Request) (string, error), error) {
	if strings.TrimSpace(rawCIDRs) == "" {
		return nil, nil
	}
	entries := strings.Split(rawCIDRs, ",")
	prefixes := make([]netip.Prefix, 0, len(entries))
	for index, entry := range entries {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS entry %d must be a valid CIDR", index+1)
		}
		// Normalize mapped IPv4 consistently with socket and forwarded IPs.
		// A broader mapped prefix also includes native IPv6 space, so it
		// cannot be unambiguously represented by an IPv4 trust boundary.
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS entry %d has an ambiguous IPv4-mapped prefix", index+1)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	isTrusted := func(address netip.Addr) bool {
		for _, prefix := range prefixes {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	return func(request *http.Request) (string, error) {
		if request == nil {
			return "", errors.New("client connection address is unavailable")
		}
		remote, err := netip.ParseAddrPort(request.RemoteAddr)
		if err != nil {
			return "", errors.New("client connection address is invalid")
		}
		peer := remote.Addr().WithZone("").Unmap()
		if !isTrusted(peer) {
			return peer.String(), nil
		}
		values := request.Header.Values("X-Forwarded-For")
		for line := len(values) - 1; line >= 0; line-- {
			value := values[line]
			for {
				comma := strings.LastIndexByte(value, ',')
				part := value[comma+1:]
				address, valid := parseForwardedClientAddress(part)
				if !valid {
					// A malformed trusted suffix establishes no client
					// identity. Use the fixed socket bucket instead of
					// trusting any earlier, attacker-controlled entry.
					return peer.String(), nil
				}
				if !isTrusted(address) {
					return address.String(), nil
				}
				if comma < 0 {
					break
				}
				value = value[:comma]
			}
		}
		// Missing XFF or an entirely trusted chain has no untrusted client
		// boundary. Keep the socket bucket rather than selecting an arbitrary
		// header value. X-Real-IP and Forwarded never override this result.
		return peer.String(), nil
	}, nil
}

func parseForwardedClientAddress(value string) (netip.Addr, bool) {
	// HTTP optional whitespace is SP/HTAB. Do not normalize other characters,
	// accept hostnames, resolve DNS, or interpret RFC Forwarded syntax as XFF.
	value = strings.Trim(value, " \t")
	address, err := netip.ParseAddr(value)
	if err != nil {
		// ALB's client-port preservation uses IPv4:port or [IPv6]:port.
		endpoint, endpointErr := netip.ParseAddrPort(value)
		if endpointErr != nil {
			return netip.Addr{}, false
		}
		address = endpoint.Addr()
	}
	if address.Zone() != "" {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}
