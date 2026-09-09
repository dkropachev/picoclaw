//go:build (unix && !aix) || windows

//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
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

func TestWindowsFileIdentityUsesTheComplete128BitValue(t *testing.T) {
	first := windowsFileIdentity{
		volumeSerialNumber: 0x0102030405060708,
		fileID:             [16]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
	}
	same := first
	stable, err := stableWindowsFileIdentity(first, same)
	if err != nil || !stable.Valid() || stable != first.opaqueIdentity() {
		t.Fatal("equal Windows FILE_ID_INFO values produced different identities")
	}

	differentHighBits := first
	differentHighBits.fileID[0] ^= 0xff
	if first == differentHighBits || first.opaqueIdentity() == differentHighBits.opaqueIdentity() {
		t.Fatal("distinct high 64 bits produced the same Windows identity")
	}
	if identity, err := stableWindowsFileIdentity(first, differentHighBits); identity.Valid() ||
		!errors.Is(err, ErrUnsafeType) {
		t.Fatalf("changed Windows file identity = %#v, %v", identity, err)
	}
	differentVolume := first
	differentVolume.volumeSerialNumber++
	if first == differentVolume || first.opaqueIdentity() == differentVolume.opaqueIdentity() {
		t.Fatal("distinct 64-bit volume serials produced the same Windows identity")
	}
	if got := first.opaqueIdentity().String(); len(got) != len("windows:")+16+1+32 {
		t.Fatalf("Windows identity width = %d for %q", len(got), got)
	}
	zeroFileID := windowsFileIdentity{volumeSerialNumber: first.volumeSerialNumber}
	if identity, err := stableWindowsFileIdentity(zeroFileID, zeroFileID); identity.Valid() ||
		!errors.Is(err, ErrUnsupported) {
		t.Fatalf("zero Windows file ID = %#v, %v", identity, err)
	}
}

func TestIdentityBoundTypesAndOpenedHandles(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathIdentity, objectType, exists, err := ExistingWithType(regular)
	if err != nil || !exists || !pathIdentity.Valid() || objectType != ObjectTypeRegular {
		t.Fatalf("ExistingWithType(regular) = %#v, %v, %t, %v", pathIdentity, objectType, exists, err)
	}
	file, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	openedIdentity, openedType, err := Opened(file)
	if err != nil || openedIdentity != pathIdentity || openedType != ObjectTypeRegular {
		t.Fatalf("Opened(regular) = %#v, %v, %v", openedIdentity, openedType, err)
	}
	if err := os.Rename(regular, regular+".moved"); err != nil {
		t.Fatal(err)
	}
	retainedIdentity, retainedType, err := Opened(file)
	if err != nil || retainedIdentity != pathIdentity || retainedType != ObjectTypeRegular {
		t.Fatalf("Opened(retained) = %#v, %v, %v", retainedIdentity, retainedType, err)
	}

	directoryIdentity, directoryType, exists, err := ExistingWithType(root)
	if err != nil || !exists || !directoryIdentity.Valid() || directoryType != ObjectTypeDirectory {
		t.Fatalf("ExistingWithType(directory) = %#v, %v, %t, %v", directoryIdentity, directoryType, exists, err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	directoryOpenedIdentity, directoryOpenedType, err := Opened(directory)
	if closeErr := directory.Close(); err != nil || closeErr != nil ||
		directoryOpenedIdentity != directoryIdentity || directoryOpenedType != ObjectTypeDirectory {
		t.Fatalf(
			"Opened(directory) = %#v, %v, %v; close=%v",
			directoryOpenedIdentity, directoryOpenedType, err, closeErr,
		)
	}
	closed, err := os.Open(regular + ".moved")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if identity, objectType, err := Opened(closed); identity.Valid() || objectType != 0 || err == nil {
		t.Fatalf("Opened(closed) = %#v, %v, %v", identity, objectType, err)
	}
	missing := filepath.Join(root, "missing")
	if identity, objectType, exists, err := ExistingWithType(missing); identity.Valid() ||
		objectType != 0 || exists || err != nil {
		t.Fatalf("ExistingWithType(missing) = %#v, %v, %t, %v", identity, objectType, exists, err)
	}
	if identity, objectType, err := Opened(nil); identity.Valid() || objectType != 0 ||
		!errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Opened(nil) = %#v, %v, %v", identity, objectType, err)
	}
}
