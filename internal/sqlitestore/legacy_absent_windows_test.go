//go:build windows

package sqlitestore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestWindowsSealedAbsentLegacyRootRejectsAmbiguousComponents(t *testing.T) {
	for _, component := range []string{
		"", ".", "..", "CON", "nul.txt", "COM1", "LPT9.log", "name.", "name ",
		"stream:ads", "short~1", "question?", "slash\\name",
	} {
		if validSealedAbsentWindowsComponent(component) {
			t.Errorf("ambiguous Windows component %q was accepted", component)
		}
	}
	for _, component := range []string{"legacy", "repository-reviews.db", "name~safe"} {
		if !validSealedAbsentWindowsComponent(component) {
			t.Errorf("ordinary Windows component %q was rejected", component)
		}
	}
}

func TestWindowsSealedAbsentLegacyRootRetainsAncestorName(t *testing.T) {
	base := privateSealedAbsentLegacyTestDirectory(t)
	ancestor := filepath.Join(base, "ancestor")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := fileutil.SecurePrivateDirectory(ancestor); err != nil {
		t.Fatal(err)
	}
	proof, err := captureSealedAbsentLegacyRoot(
		t.Context(), filepath.Join(ancestor, "missing"),
	)
	if err != nil {
		t.Fatal(err)
	}
	moved := ancestor + ".moved"
	if err := os.Rename(ancestor, moved); err == nil {
		if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
			t.Fatal("renamed retained Windows ancestor still revalidated")
		}
		if err := closeSealedAbsentLegacyRoot(proof); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err != nil {
		t.Fatalf("failed rename changed retained Windows proof: %v", err)
	}
	if err := closeSealedAbsentLegacyRoot(proof); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ancestor, moved); err != nil {
		t.Fatalf("released Windows ancestor handle still blocked rename: %v", err)
	}
}
