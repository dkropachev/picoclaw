//go:build featuretools

package main

import (
	"reflect"
	"testing"
)

func TestFrontendExpectedSpecPathsUsesConfiguredRule(t *testing.T) {
	cfg := frontendOwnershipConfig{
		Rules: []frontendOwnershipRule{
			{
				Spec: "docs/features/chat-channels.md",
				Patterns: []string{
					"web/frontend/src/components/chat/**",
				},
			},
		},
	}
	normalizeFrontendOwnershipConfig(&cfg)

	got := frontendExpectedSpecPaths(cfg, "web/frontend/src/components/chat/assistant-message.tsx")
	want := []string{"docs/features/chat-channels.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frontendExpectedSpecPaths() = %#v, want %#v", got, want)
	}
}

func TestForbiddenFrontendCodeOwnershipPatternUsesConfiguredPatterns(t *testing.T) {
	cfg := frontendOwnershipConfig{
		ForbiddenBroadCodeOwnershipPatterns: []string{
			"web/frontend/**",
			"web/frontend/src/components/**",
		},
	}
	normalizeFrontendOwnershipConfig(&cfg)

	if !forbiddenFrontendCodeOwnershipPattern(cfg, "./web/frontend/**") {
		t.Fatal("expected broad frontend ownership pattern to be forbidden")
	}
	if forbiddenFrontendCodeOwnershipPattern(cfg, "web/frontend/src/components/chat/**") {
		t.Fatal("expected feature-owned frontend component pattern to be allowed")
	}
}

func TestExpectedOwnerSpecChangedRequiresExpectedFrontendSpec(t *testing.T) {
	expected := []string{"docs/features/chat-channels.md"}
	owners := []featureOwnership{
		{SpecRelPath: "docs/features/chat-channels.md"},
		{SpecRelPath: "docs/features/launcher-management.md"},
	}

	if expectedOwnerSpecChanged(expected, owners, map[string]bool{
		"docs/features/launcher-management.md": true,
	}) {
		t.Fatal("wrong-but-owning frontend spec satisfied expected owner check")
	}

	if !expectedOwnerSpecChanged(expected, owners, map[string]bool{
		"docs/features/chat-channels.md": true,
	}) {
		t.Fatal("expected changed frontend owner spec to satisfy expected owner check")
	}
}
