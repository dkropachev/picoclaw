//go:build unix && !aix

package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnixSealedAbsentLegacyRootRejectsAncestorReplacement(t *testing.T) {
	base := privateSealedAbsentLegacyTestDirectory(t)
	ancestor := filepath.Join(base, "ancestor")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ancestor, "missing")
	proof, err := captureSealedAbsentLegacyRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSealedAbsentLegacyRoot(proof)
	moved := ancestor + ".moved"
	if err := os.Rename(ancestor, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
		t.Fatal("sealed absence accepted a replacement ancestor")
	}
	if _, err := os.Lstat(filepath.Join(moved, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proof mutated retained namespace: %v", err)
	}
}

func TestUnixSealedAbsentLegacyRootRejectsUnsafeOrAliasedAncestor(t *testing.T) {
	base := privateSealedAbsentLegacyTestDirectory(t)
	unsafe := filepath.Join(base, "unsafe")
	if err := os.Mkdir(unsafe, 0o777); err != nil {
		t.Fatal(err)
	}
	if proof, err := captureSealedAbsentLegacyRoot(
		t.Context(), filepath.Join(unsafe, "missing"),
	); proof != nil || err == nil {
		if proof != nil {
			_ = closeSealedAbsentLegacyRoot(proof)
		}
		t.Fatalf("writable nearest ancestor = %#v, %v", proof, err)
	}

	realDirectory := filepath.Join(base, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}
	if proof, err := captureSealedAbsentLegacyRoot(
		t.Context(), filepath.Join(alias, "missing"),
	); proof != nil || err == nil {
		if proof != nil {
			_ = closeSealedAbsentLegacyRoot(proof)
		}
		t.Fatalf("symlinked ancestor = %#v, %v", proof, err)
	}
}

func TestUnixSealedAbsentLegacyRootRejectsSpecialObjectAppearance(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "special", "root")
	proof, err := captureSealedAbsentLegacyRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSealedAbsentLegacyRoot(proof)
	if err := unix.Mkfifo(filepath.Join(ancestor, "special"), 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	if err := revalidateSealedAbsentLegacyRoot(t.Context(), proof); err == nil {
		t.Fatal("sealed absence accepted a later special object")
	}
}

func TestUnixSealedAbsentLegacyRootRejectsPermissionAmbiguity(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory access checks")
	}
	base := privateSealedAbsentLegacyTestDirectory(t)
	blocked := filepath.Join(base, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if proof, err := captureSealedAbsentLegacyRoot(
		t.Context(), filepath.Join(blocked, "missing"),
	); proof != nil || err == nil {
		if proof != nil {
			_ = closeSealedAbsentLegacyRoot(proof)
		}
		t.Fatalf("permission-ambiguous root = %#v, %v", proof, err)
	}
}
