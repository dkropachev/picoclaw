package mcp

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalToolNamePreservesSimpleAndCaseFoldedNames(t *testing.T) {
	tests := []struct {
		name   string
		server string
		tool   string
		want   string
	}{
		{
			name:   "simple",
			server: "github",
			tool:   "create_issue",
			want:   "mcp_github_create_issue",
		},
		{
			name:   "case folded",
			server: "GitHub",
			tool:   "Create_Issue",
			want:   "mcp_github_create_issue",
		},
		{
			name:   "hyphens preserved",
			server: "remote-api",
			tool:   "fetch-data",
			want:   "mcp_remote-api_fetch-data",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanonicalToolName(test.server, test.tool); got != test.want {
				t.Fatalf("CanonicalToolName(%q, %q) = %q, want %q", test.server, test.tool, got, test.want)
			}
		})
	}
}

func TestCanonicalToolNameLossyNamesAreStableAndDistinct(t *testing.T) {
	first := CanonicalToolName("GitHub Server", "issues.list")
	again := CanonicalToolName("GitHub Server", "issues.list")
	second := CanonicalToolName("GitHub@Server", "issues.list")

	if first != again {
		t.Fatalf("canonical name changed between calls: %q != %q", first, again)
	}
	if want := "mcp_github_server_issues_list_54111047"; first != want {
		t.Fatalf("lossy canonical name = %q, want historical value %q", first, want)
	}
	if first == second {
		t.Fatalf("lossy identities collided: %q", first)
	}
	if !strings.HasPrefix(first, "mcp_github_server_issues_list_") {
		t.Fatalf("lossy canonical name = %q, want sanitized prefix and hash", first)
	}
	if len(first) > maxToolNameLength {
		t.Fatalf("lossy canonical name length = %d, want <= %d", len(first), maxToolNameLength)
	}
}

func TestCanonicalToolNameLongNamesAreBoundedAndDistinct(t *testing.T) {
	server := strings.Repeat("server", 20)
	first := CanonicalToolName(server, strings.Repeat("tool", 30)+"a")
	second := CanonicalToolName(server, strings.Repeat("tool", 30)+"b")

	if len(first) != maxToolNameLength {
		t.Fatalf("long canonical name length = %d, want %d (%q)", len(first), maxToolNameLength, first)
	}
	if first == second {
		t.Fatalf("long identities collided: %q", first)
	}
}

func TestDetectCanonicalToolNameCollision(t *testing.T) {
	err := DetectCanonicalToolNameCollision([]ToolIdentity{
		{Server: "github", Tool: "search"},
		{Server: "GitHub", Tool: "Search"},
	})
	if !errors.Is(err, ErrCanonicalToolNameCollision) {
		t.Fatalf("DetectCanonicalToolNameCollision() error = %v, want collision", err)
	}
	var collision *CanonicalToolNameCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("error type = %T, want *CanonicalToolNameCollisionError", err)
	}
	if collision.Name != "mcp_github_search" {
		t.Fatalf("collision name = %q, want mcp_github_search", collision.Name)
	}
	if collision.First != (ToolIdentity{Server: "GitHub", Tool: "Search"}) ||
		collision.Second != (ToolIdentity{Server: "github", Tool: "search"}) {
		t.Fatalf("collision identities = %#v / %#v, want deterministic order", collision.First, collision.Second)
	}
}

func TestDetectCanonicalToolNameCollisionAcrossServerToolBoundary(t *testing.T) {
	err := DetectCanonicalToolNameCollision([]ToolIdentity{
		{Server: "a_b", Tool: "c"},
		{Server: "a", Tool: "b_c"},
	})
	if !errors.Is(err, ErrCanonicalToolNameCollision) {
		t.Fatalf("DetectCanonicalToolNameCollision() error = %v, want boundary collision", err)
	}
}

func TestDetectCanonicalToolNameCollisionAllowsExactDuplicates(t *testing.T) {
	identity := ToolIdentity{Server: "github", Tool: "search"}
	if err := DetectCanonicalToolNameCollision([]ToolIdentity{identity, identity}); err != nil {
		t.Fatalf("exact duplicate identity reported as ambiguous: %v", err)
	}
}
