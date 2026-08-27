//go:build linux

package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestApplyPatchTransactionFSLinuxCloseoutInvalidDescriptors(t *testing.T) {
	invalid := applyPatchTxnPlatformAnchor{fd: -1}
	if err := closeApplyPatchTxnPlatformAnchor(invalid); err != nil {
		t.Fatalf("negative descriptor close = %v", err)
	}
	if err := closeApplyPatchTxnPlatformAnchor(applyPatchTxnPlatformAnchor{fd: 1 << 30}); err == nil {
		t.Fatal("invalid descriptor close succeeded")
	}
	if _, err := applyPatchTxnPlatformAnchorIdentity(invalid); err == nil {
		t.Fatal("invalid descriptor identity succeeded")
	}
	if file, _, err := applyPatchTxnPlatformCreateRegular(invalid, "file", 0o600); err == nil || file != nil {
		t.Fatalf("invalid descriptor create = %#v, %v", file, err)
	}
	if _, err := applyPatchTxnPlatformInspectAt(invalid, "file"); err == nil {
		t.Fatal("invalid descriptor inspect succeeded")
	}
	if file, _, _, err := applyPatchTxnPlatformOpenRegular(invalid, "file"); err == nil || file != nil {
		t.Fatalf("invalid descriptor read-open = %#v, %v", file, err)
	}
	if file, err := applyPatchTxnPlatformOpenRegularWrite(
		invalid, "file", applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
	); err == nil || file != nil {
		t.Fatalf("invalid descriptor write-open = %#v, %v", file, err)
	}
	if err := applyPatchTxnPlatformLinkNoReplace(invalid, "a", invalid, "b"); err == nil {
		t.Fatal("invalid descriptor link succeeded")
	}
	if err := applyPatchTxnPlatformRenameNoReplace(invalid, "a", invalid, "b"); err == nil {
		t.Fatal("invalid descriptor rename succeeded")
	}
	if err := applyPatchTxnPlatformRemoveExact(
		invalid,
		"a",
		"b",
		applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
		false,
		nil,
	); err == nil {
		t.Fatal("invalid descriptor removal succeeded")
	}
	if _, err := applyPatchTxnPlatformMkdir(invalid, "directory", 0o700); err == nil {
		t.Fatal("invalid descriptor mkdir succeeded")
	}
	if child, _, err := applyPatchTxnPlatformOpenChildDirectory(invalid, "directory"); err == nil {
		t.Fatalf("invalid descriptor child open = %#v, %v", child, err)
	}
	if err := applyPatchTxnPlatformSyncDirectory(invalid); err == nil {
		t.Fatal("invalid descriptor sync succeeded")
	}
	if _, err := applyPatchTxnPlatformReadDirectoryNames(invalid, 1); err == nil {
		t.Fatal("invalid descriptor listing succeeded")
	}
	if err := applyPatchTxnPlatformProbeNoReplace(invalid, "file"); err == nil {
		t.Fatal("invalid descriptor probe succeeded")
	}
	if _, err := applyPatchTxnStateFromFD(-1, ""); err == nil {
		t.Fatal("invalid descriptor state succeeded")
	}
}

func TestApplyPatchTransactionFSLinuxCloseoutKindsAndLimits(t *testing.T) {
	directory := t.TempDir()
	regularPath := filepath.Join(directory, "regular")
	if err := os.WriteFile(regularPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(directory, "symlink")
	if err := os.Symlink("regular", symlinkPath); err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(directory, "fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name string
		path string
		kind string
	}{
		{name: "regular", path: regularPath, kind: "regular"},
		{name: "directory", path: directory, kind: "directory"},
		{name: "symlink", path: symlinkPath, kind: "symlink"},
		{name: "special", path: fifoPath, kind: "special"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			info, statErr := os.Lstat(testCase.path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			identity, identityErr := applyPatchTxnPlatformIdentityFromFileInfo(info, testCase.kind)
			if identityErr != nil || identity.Kind != testCase.kind {
				t.Fatalf("identity kind = %#v, %v", identity, identityErr)
			}
			if _, err := applyPatchTxnPlatformIdentityFromFileInfo(info, "wrong"); err == nil {
				t.Fatal("wrong expected identity kind succeeded")
			}
			fd, openErr := unix.Open(testCase.path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer unix.Close(fd)
			state, stateErr := applyPatchTxnStateFromFD(fd, "")
			if stateErr != nil || state.Identity.Kind != testCase.kind {
				t.Fatalf("descriptor kind = %#v, %v", state, stateErr)
			}
			if _, err := applyPatchTxnStateFromFD(fd, "wrong"); err == nil {
				t.Fatal("wrong descriptor kind succeeded")
			}
		})
	}

	rootAnchor, rootIdentity, rootOpenErr := openApplyPatchTxnPlatformAnchor("/")
	if rootOpenErr != nil || !rootIdentity.valid("directory") {
		t.Fatalf("filesystem root anchor = %#v, %#v, %v", rootAnchor, rootIdentity, rootOpenErr)
	}
	if err := closeApplyPatchTxnPlatformAnchor(rootAnchor); err != nil {
		t.Fatal(err)
	}
	anchor, _, anchorErr := openApplyPatchTxnPlatformAnchor(directory)
	if anchorErr != nil {
		t.Fatal(anchorErr)
	}
	defer closeApplyPatchTxnPlatformAnchor(anchor)
	if _, err := applyPatchTxnPlatformReadDirectoryNames(anchor, 0); err == nil {
		t.Fatal("zero-limit platform listing succeeded with entries")
	}
}

func TestApplyPatchTransactionFSLinuxCloseoutPrimitiveErrorClassification(t *testing.T) {
	if err := wrapApplyPatchTxnLinuxPrimitiveError("noop", nil); err != nil {
		t.Fatalf("nil primitive error = %v", err)
	}
	for _, unsupported := range []error{unix.ENOSYS, unix.EOPNOTSUPP, unix.EINVAL} {
		err := wrapApplyPatchTxnLinuxPrimitiveError("unsupported", unsupported)
		if !errors.Is(err, errApplyPatchTransactionUnsupported) || !errors.Is(err, unsupported) {
			t.Fatalf("unsupported primitive error = %v", err)
		}
	}
	ordinary := wrapApplyPatchTxnLinuxPrimitiveError("ordinary", unix.EPERM)
	if errors.Is(ordinary, errApplyPatchTransactionUnsupported) || !errors.Is(ordinary, unix.EPERM) {
		t.Fatalf("ordinary primitive error = %v", ordinary)
	}
}
