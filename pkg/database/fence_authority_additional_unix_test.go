//go:build unix && !aix

//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package database

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUnixFenceAndGuardRejectLockPermissionDrift(t *testing.T) {
	home := t.TempDir()
	fence, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(home, StateDirectoryName, storageLockFileName)
	if err := os.Chmod(lockPath, 0o644); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if fence.Authorizes(home) {
		t.Fatal("fence authorized a publicly accessible lock file")
	}
	if release, guardErr := fence.Guard(home); release != nil || CodeOf(guardErr) != CodeIntegrity {
		t.Fatalf("Guard() release=%t, err=%v", release != nil, guardErr)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}

	migrationHome := t.TempDir()
	migration, err := AcquireMigrationFence(migrationHome)
	if err != nil {
		t.Fatal(err)
	}
	migrationLock := filepath.Join(migrationHome, StateDirectoryName, storageLockFileName)
	if err := os.Chmod(migrationLock, 0o644); err != nil {
		_ = migration.Close()
		t.Fatal(err)
	}
	if release, guardErr := migration.GuardMigration(migrationHome); release != nil ||
		CodeOf(guardErr) != CodeUnauthorized {
		t.Fatalf("GuardMigration() release=%t, err=%v", release != nil, guardErr)
	}
	if ctx, contextErr := migration.MigrationContext(
		nil, filepath.Join(migrationHome, "stage.db"),
	); ctx != nil || CodeOf(contextErr) != CodeConflict {
		t.Fatalf("MigrationContext() = %v, %v", ctx, contextErr)
	}
	if err := migration.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnixMigrationContextExpiresOnPhysicalLockReplacement(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "stage.db")
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	ctx, err := fence.MigrationContext(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(home, StateDirectoryName, storageLockFileName)
	if err := os.Rename(lockPath, lockPath+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !MigrationContextPresent(ctx) || MigrationContextActive(ctx) ||
		MigrationContextAuthorizes(ctx, target) {
		t.Fatal("physical lock replacement retained live migration authority")
	}
}

func TestUnixFenceRejectsReplacementHomeIdentity(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if err := os.Rename(home, home+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if fence.Authorizes(home) {
		t.Fatal("fence authorized a replacement home directory")
	}
	if release, guardErr := fence.Guard(home); release != nil || CodeOf(guardErr) != CodeIntegrity {
		t.Fatalf("replacement-home Guard() release=%t, err=%v", release != nil, guardErr)
	}
}

func TestUnixCanonicalMigrationTargetFailsFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Remove(removed); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			t.Skip("platform does not unlink the current working directory")
		}
		t.Fatal(err)
	}
	if target, targetErr := canonicalMigrationTarget("relative.db"); target != "" || CodeOf(targetErr) != CodeInvalid {
		t.Fatalf("canonicalMigrationTarget() = %q, %v", target, targetErr)
	}
}
