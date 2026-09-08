package databasereadiness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestGenerationExclusionsDeduplicatePhysicalHardlinks(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.db")
	second := filepath.Join(root, "second.db")
	writeReadinessFile(t, first, []byte("generation"))
	if err := os.Link(first, second); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	exclusions, err := generationExclusions([]storecatalog.Spec{
		{ID: "global/first", Path: first},
		{ID: "global/second", Path: second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exclusions.paths) != 8 || len(exclusions.physical) != 1 {
		t.Fatalf("generation exclusions = %d lexical, %d physical", len(exclusions.paths), len(exclusions.physical))
	}
}

func TestLegacyDiscoveryRejectsInvalidAndOverlongPathComponents(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, string([]byte{'b', 'a', 'd', 0xff})),
		filepath.Join(root, strings.Repeat("x", legacyComponentMaxBytes+1)),
	} {
		found, err := legacyInputExists(t.Context(), []string{path}, generationSet{})
		if found || !errors.Is(err, errLegacyIntegrity) {
			t.Errorf("legacyInputExists(%q) = %t, %v", path, found, err)
		}
	}
}
