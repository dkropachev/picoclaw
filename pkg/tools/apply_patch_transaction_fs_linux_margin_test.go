//go:build linux

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionFSLinuxMarginIdentityAndOpenGuards(t *testing.T) {
	if _, err := applyPatchTxnPlatformIdentityFromFileInfo(
		applyPatchCoverageFileInfo{name: "missing-sys", mode: 0o600},
		"regular",
	); err == nil {
		t.Fatal("file info without syscall identity was accepted")
	}
	if anchor, _, err := openApplyPatchTxnPlatformAnchor("relative"); err == nil {
		_ = closeApplyPatchTxnPlatformAnchor(anchor)
		t.Fatal("relative platform anchor was accepted")
	}

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, _, err := openApplyPatchTxnPlatformAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer closeApplyPatchTxnPlatformAnchor(anchor)
	state, err := applyPatchTxnPlatformInspectAt(anchor, "file")
	if err != nil {
		t.Fatal(err)
	}
	wrong := state.Identity
	wrong.File++
	if file, err := applyPatchTxnPlatformOpenRegularWrite(anchor, "file", wrong); err == nil {
		_ = file.Close()
		t.Fatal("write-open accepted a changed identity")
	}
}

func TestApplyPatchTransactionFSLinuxMarginRemovalGuards(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		directory := t.TempDir()
		anchor, _, err := openApplyPatchTxnPlatformAnchor(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer closeApplyPatchTxnPlatformAnchor(anchor)
		if err := applyPatchTxnPlatformRemoveExact(
			anchor,
			"missing",
			"removal",
			applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
			false,
			nil,
		); err != nil {
			t.Fatalf("missing source removal = %v", err)
		}
	})

	t.Run("source reappeared", func(t *testing.T) {
		directory := t.TempDir()
		sourcePath := filepath.Join(directory, "source")
		if err := os.WriteFile(sourcePath, []byte("owned\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		anchor, _, err := openApplyPatchTxnPlatformAnchor(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer closeApplyPatchTxnPlatformAnchor(anchor)
		state, err := applyPatchTxnPlatformInspectAt(anchor, "source")
		if err != nil {
			t.Fatal(err)
		}
		if err := applyPatchTxnPlatformRemoveExact(
			anchor,
			"source",
			"removal",
			state.Identity,
			false,
			func() error { return os.WriteFile(sourcePath, []byte("alien\n"), 0o600) },
		); err == nil {
			t.Fatal("reappeared removal source was accepted")
		}
	})

	t.Run("rename permission", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "source"), []byte("owned\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		anchor, _, err := openApplyPatchTxnPlatformAnchor(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer closeApplyPatchTxnPlatformAnchor(anchor)
		state, err := applyPatchTxnPlatformInspectAt(anchor, "source")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(directory, 0o700)
		if err := applyPatchTxnPlatformRemoveExact(
			anchor, "source", "removal", state.Identity, false, nil,
		); err == nil {
			t.Skip("filesystem credentials permit rename without directory write mode")
		}
	})
}

func TestApplyPatchTransactionFSLinuxMarginSyncError(t *testing.T) {
	anchor, _, err := openApplyPatchTxnPlatformAnchor("/proc")
	if err != nil {
		t.Skipf("procfs anchor is unavailable: %v", err)
	}
	defer closeApplyPatchTxnPlatformAnchor(anchor)
	if err := applyPatchTxnPlatformSyncDirectory(anchor); err == nil {
		t.Skip("procfs unexpectedly supports directory fsync")
	}
}
