//go:build (unix && !aix) || windows

package fileidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingTracksHardlinksAndDirectories(t *testing.T) {
	root := t.TempDir()
	directoryIdentity, exists, err := Existing(root)
	if err != nil || !exists || !directoryIdentity.Valid() {
		t.Fatalf("Existing(directory) = %#v, %t, %v", directoryIdentity, exists, err)
	}

	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	firstIdentity, exists, err := Existing(first)
	if err != nil || !exists || !firstIdentity.Valid() {
		t.Fatalf("Existing(first) = %#v, %t, %v", firstIdentity, exists, err)
	}
	secondIdentity, exists, err := Existing(second)
	if err != nil || !exists || secondIdentity != firstIdentity {
		t.Fatalf("Existing(second) = %#v, %t, %v; want %#v", secondIdentity, exists, err, firstIdentity)
	}
	if directoryIdentity == firstIdentity || directoryIdentity.String() == "" {
		t.Fatal("distinct objects shared an identity")
	}
}

func TestExistingRejectsInvalidUnsafeAndMissingPaths(t *testing.T) {
	for _, path := range []string{"", "bad\x00path", string([]byte{'b', 'a', 'd', 0xff})} {
		if identity, exists, err := Existing(path); identity.Valid() || exists || !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Existing(%q) = %#v, %t, %v", path, identity, exists, err)
		}
	}

	root := t.TempDir()
	if identity, exists, err := Existing(filepath.Join(root, "missing")); identity.Valid() || exists || err != nil {
		t.Fatalf("Existing(missing) = %#v, %t, %v", identity, exists, err)
	}
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err == nil {
		if identity, exists, err := Existing(link); identity.Valid() || exists || !errors.Is(err, ErrUnsafeType) {
			t.Fatalf("Existing(symlink) = %#v, %t, %v", identity, exists, err)
		}
	}
}
