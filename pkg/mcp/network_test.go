package mcp

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestMCPRemoteHTTPClientRejectsCrossOriginRedirect(t *testing.T) {
	endpoint, err := url.Parse("https://mcp.example.test/api")
	if err != nil {
		t.Fatal(err)
	}
	client := newMCPRemoteHTTPClient(endpoint, &http.Client{})
	previous, _ := http.NewRequest(http.MethodPost, endpoint.String(), nil)

	sameOrigin, _ := http.NewRequest(http.MethodPost, "https://mcp.example.test/next", nil)
	if err := client.CheckRedirect(sameOrigin, []*http.Request{previous}); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
	crossOrigin, _ := http.NewRequest(http.MethodPost, "https://127.0.0.1/internal", nil)
	if err := client.CheckRedirect(crossOrigin, []*http.Request{previous}); err == nil ||
		!strings.Contains(err.Error(), "changed origins") {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
}

func TestPrivateOrSpecialMCPAddresses(t *testing.T) {
	for _, rawIP := range []string{
		"0.0.0.1",
		"10.0.0.1",
		"100.64.0.1",
		"127.0.0.1",
		"169.254.169.254",
		"192.168.1.1",
		"198.18.0.1",
		"240.0.0.1",
		"::1",
		"fc00::1",
		"fe80::1",
		"64:ff9b::7f00:1",
	} {
		if !IsPrivateOrSpecialIP(net.ParseIP(rawIP)) {
			t.Errorf("IsPrivateOrSpecialIP(%q) = false", rawIP)
		}
	}
	if IsPrivateOrSpecialIP(net.ParseIP("8.8.8.8")) {
		t.Error("public address was classified as private or special")
	}
}

func TestExplicitLocalMCPHostnames(t *testing.T) {
	for _, hostname := range []string{
		"localhost",
		"mcp.localhost",
		"mcp.local",
		"mcp.internal",
		"mcp.internal.",
		"mcp.home.arpa",
		"mcp-streamable-server",
		"10.0.0.5",
	} {
		if !IsExplicitLocalHostname(hostname) {
			t.Errorf("IsExplicitLocalHostname(%q) = false", hostname)
		}
	}
	for _, hostname := range []string{
		"mcp.example.com",
		"identity.example.test",
		"identity。example.com",
		"8.8.8.8",
	} {
		if IsExplicitLocalHostname(hostname) {
			t.Errorf("IsExplicitLocalHostname(%q) = true", hostname)
		}
	}
}
