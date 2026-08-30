//go:build featuretools

package main

import (
	"os"
	"path/filepath"
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

func TestDiscoverTestsSkipsIgnoredDocsPlansEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	productTest := filepath.Join(root, "pkg", "feature_test.go")
	plannedEvidence := filepath.Join(root, "docs", "plans", "run", "candidate_test.go")
	for _, path := range []string{productTest, plannedEvidence} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			path,
			[]byte("package fixture\nfunc TestDiscovered(t *testing.T) {}\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	discovered := make(map[string]bool)
	if err := discoverTests(root, func(_, _, source string) {
		discovered[source] = true
	}); err != nil {
		t.Fatal(err)
	}
	if !discovered["pkg/feature_test.go"] ||
		discovered["docs/plans/run/candidate_test.go"] || len(discovered) != 1 {
		t.Fatalf("discovered tests = %#v", discovered)
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
