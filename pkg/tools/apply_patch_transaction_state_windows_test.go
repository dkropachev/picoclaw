//go:build windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionWindowsLockContentionAndIsolation(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.lock")
	secondPath := filepath.Join(root, "second.lock")
	first, err := acquireApplyPatchTransactionFileLock(context.Background(), firstPath)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Close()
	waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
		_, lockErr := acquireApplyPatchTransactionFileLock(ctx, firstPath)
		return lockErr
	})
	second, err := acquireApplyPatchTransactionFileLock(context.Background(), secondPath)
	if err != nil {
		t.Fatalf("unrelated lock: %v", err)
	}
	if err = second.Close(); err != nil {
		t.Fatalf("close unrelated lock: %v", err)
	}
	waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
		_, lockErr := acquireApplyPatchTransactionFileLock(ctx, firstPath)
		return lockErr
	})
	if err = first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	reopened, err := acquireApplyPatchTransactionFileLock(context.Background(), firstPath)
	if err != nil {
		t.Fatalf("reopen released lock: %v", err)
	}
	if _, err = reopened.fileInfo(); err != nil {
		t.Fatalf("reopened lock info: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.fileInfo(); err == nil {
		t.Fatal("closed lock retained a live handle")
	}
}

func TestApplyPatchTransactionWindowsStateRejectsReparseRoot(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(parent, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := prepareApplyPatchTransactionStateRoot(workspace, linked, nil); err == nil {
		t.Fatal("reparse state root was accepted")
	}
}
