package mcp

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

var privateOrSpecialMCPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// IsPrivateOrSpecialIP reports whether an address is local, non-routable, or
// reserved for a purpose that should not be reached through a public MCP host.
func IsPrivateOrSpecialIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if address.IsPrivate() ||
		address.IsLoopback() ||
		address.IsUnspecified() ||
		address.IsMulticast() ||
		address.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range privateOrSpecialMCPPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// IsExplicitLocalHostname reports whether a hostname is intentionally local
// rather than a public DNS name that merely resolved into a private network.
// This permits common LAN, mDNS, Docker, and home-network names while keeping
// public OAuth endpoints subject to the private-address boundary.
func IsExplicitLocalHostname(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	if hostname == "" {
		return false
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return IsPrivateOrSpecialIP(ip)
	}
	for _, character := range hostname {
		if character > 0x7f {
			return false
		}
	}
	if !strings.Contains(hostname, ".") {
		return true
	}
	if hostname == "localhost" {
		return true
	}
	for _, suffix := range []string{
		".localhost",
		".local",
		".localdomain",
		".internal",
		".home.arpa",
	} {
		if strings.HasSuffix(hostname, suffix) {
			return true
		}
	}
	return false
}

// newMCPRemoteHTTPClient gives normal MCP traffic the same-origin redirect
// boundary expected by the credential transport. The configured origin itself
// remains compatible with private DNS and user-supplied proxy transports.
func newMCPRemoteHTTPClient(endpoint *url.URL, supplied *http.Client) *http.Client {
	client := &http.Client{}
	if supplied != nil {
		*client = *supplied
	}

	previousRedirectPolicy := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many MCP server redirects")
		}
		if len(via) > 0 && !sameMCPRemoteOrigin(via[len(via)-1].URL, request.URL) {
			return fmt.Errorf("MCP server redirect changed origins")
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(request, via)
		}
		return nil
	}
	return client
}

func sameMCPRemoteOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || left.Host == "" || right.Host == "" {
		return false
	}
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectiveMCPPort(left) == effectiveMCPPort(right)
}

func effectiveMCPPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}
