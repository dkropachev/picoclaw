package config

import "testing"

func TestNormalizeMCPTransportType(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		want      string
	}{
		{name: "streamable hyphen alias", transport: "streamable-http", want: "http"},
		{name: "streamable underscore alias", transport: "streamable_http", want: "http"},
		{name: "streamable compact alias", transport: "streamablehttp", want: "http"},
		{name: "trim lower", transport: " SSE ", want: "sse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMCPTransportType(tt.transport); got != tt.want {
				t.Fatalf("NormalizeMCPTransportType(%q) = %q, want %q", tt.transport, got, tt.want)
			}
		})
	}
}

func TestEffectiveMCPTransportType(t *testing.T) {
	tests := []struct {
		name   string
		server MCPServerConfig
		want   string
	}{
		{name: "explicit alias", server: MCPServerConfig{Type: "streamable-http"}, want: "http"},
		{name: "url default", server: MCPServerConfig{URL: "https://mcp.example.com"}, want: "sse"},
		{name: "command default", server: MCPServerConfig{Command: "mcp-server"}, want: "stdio"},
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveMCPTransportType(tt.server); got != tt.want {
				t.Fatalf("EffectiveMCPTransportType() = %q, want %q", got, tt.want)
			}
		})
	}
}
