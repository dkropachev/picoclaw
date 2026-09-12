//go:build windows

package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsRetainedStagedGenerationRetiresExactIdentity(t *testing.T) {
	stage := writeWindowsStagedRetirementFile(t, t.TempDir(), "stage.db", "exact")
	retained, err := retainStagedGeneration(context.Background(), stage)
	if err != nil {
		t.Fatalf("retainStagedGeneration() error = %v", err)
	}
	if err := retained.Check(context.Background(), stage); err != nil {
		t.Fatalf("Check(original) error = %v", err)
	}
	if err := retained.Retire(context.Background()); err != nil {
		t.Fatalf("Retire() error = %v", err)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired stage still exists: %v", err)
	}
	if err := retained.platform.requireHandleDeletionPending(); err != nil {
		t.Fatalf("retained handle does not prove deletion: %v", err)
	}
	if err := retained.Retire(context.Background()); err != nil {
		t.Fatalf("second Retire() error = %v", err)
	}
	if err := retained.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestWindowsRetainedStagedGenerationChecksInstalledIdentity(t *testing.T) {
	root := t.TempDir()
	stage := writeWindowsStagedRetirementFile(t, root, "stage.db", "retained")
	installed := filepath.Join(root, "installed.db")
	retained := mustRetainWindowsStagedGeneration(t, stage)
	if err := os.Rename(stage, installed); err != nil {
		t.Fatal(err)
	}
	if err := retained.Check(context.Background(), installed); err != nil {
		t.Fatalf("Check(installed) error = %v", err)
	}
	if err := retained.Check(context.Background(), stage); err == nil {
		t.Fatal("Check(original) accepted an absent pre-cutover name")
	}
	if err := os.Rename(installed, stage); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRetainedStageHandleDoesNotBlockSQLiteOpen(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage.db")
	if err := PrepareStore(stage); err != nil {
		t.Fatal(err)
	}
	retained := mustRetainWindowsStagedGeneration(t, stage)
	database, err := OpenStore(stage, time.Second)
	if err != nil {
		t.Fatalf("OpenStore with retained identity handle: %v", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := ConfigureOffline(t.Context(), database, time.Second); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		t.Context(),
		"CREATE TABLE retained_open(value TEXT NOT NULL) STRICT",
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := retained.Check(t.Context(), stage); err != nil {
		t.Fatalf("retained stage changed across SQLite open: %v", err)
	}
	if err := retained.Retire(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsRetainedStagedGenerationCancellationPreservesStage(t *testing.T) {
	stage := writeWindowsStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
	retained := mustRetainWindowsStagedGeneration(t, stage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retained.Retire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Retire(cancelled) error = %v", err)
	}
	assertWindowsStagedRetirementContents(t, stage, "retained")
}

func TestWindowsRetainedStagedGenerationRejectsReplacementAndLinks(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		root := t.TempDir()
		stage := writeWindowsStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainWindowsStagedGeneration(t, stage)
		moved := stage + ".moved"
		if err := os.Rename(stage, moved); err != nil {
			t.Fatalf("rename stage: %v", err)
		}
		writeWindowsStagedRetirementFile(t, root, "stage.db", "decoy")
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a pathname replacement")
		}
		assertWindowsStagedRetirementContents(t, moved, "retained")
		assertWindowsStagedRetirementContents(t, stage, "decoy")
	})

	t.Run("hardlink drift", func(t *testing.T) {
		root := t.TempDir()
		stage := writeWindowsStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainWindowsStagedGeneration(t, stage)
		alias := filepath.Join(root, "alias.db")
		if err := os.Link(stage, alias); err != nil {
			t.Fatalf("hardlink stage: %v", err)
		}
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted hard-link drift")
		}
		assertWindowsStagedRetirementContents(t, stage, "retained")
		assertWindowsStagedRetirementContents(t, alias, "retained")
	})
}

func TestWindowsRetainStagedGenerationRejectsUnsafeObjects(t *testing.T) {
	root := t.TempDir()
	target := writeWindowsStagedRetirementFile(t, root, "target.db", "target")
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	if retained, err := retainStagedGeneration(context.Background(), directory); retained != nil || err == nil {
		t.Fatalf("retainStagedGeneration(directory) = %#v, %v", retained, err)
	}
	symlink := filepath.Join(root, "symlink.db")
	if err := os.Symlink(target, symlink); err != nil {
		t.Logf("symlink unavailable on this Windows host: %v", err)
	} else if retained, err := retainStagedGeneration(context.Background(), symlink); retained != nil || err == nil {
		t.Fatalf("retainStagedGeneration(symlink) = %#v, %v", retained, err)
	}
	hardlink := filepath.Join(root, "hardlink.db")
	if err := os.Link(target, hardlink); err != nil {
		t.Fatalf("hardlink stage: %v", err)
	}
	if retained, err := retainStagedGeneration(context.Background(), target); retained != nil || err == nil {
		t.Fatalf("retainStagedGeneration(hardlinked) = %#v, %v", retained, err)
	}
	assertWindowsStagedRetirementContents(t, target, "target")
}

func TestWindowsRetainedStagedGenerationClosePreservesStage(t *testing.T) {
	stage := writeWindowsStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
	retained := mustRetainWindowsStagedGeneration(t, stage)
	if err := retained.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := retained.Retire(context.Background()); err == nil {
		t.Fatal("Retire() accepted a closed capability")
	}
	assertWindowsStagedRetirementContents(t, stage, "retained")
}

func mustRetainWindowsStagedGeneration(t *testing.T, path string) *retainedStagedGeneration {
	t.Helper()
	retained, err := retainStagedGeneration(context.Background(), path)
	if err != nil {
		t.Fatalf("retainStagedGeneration(%s) error = %v", path, err)
	}
	t.Cleanup(func() {
		if err := retained.Close(); err != nil {
			t.Errorf("Close retained stage: %v", err)
		}
	})
	return retained
}

func writeWindowsStagedRetirementFile(t *testing.T, root, name, contents string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write staged retirement file: %v", err)
	}
	return path
}

func assertWindowsStagedRetirementContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != want {
		t.Fatalf("contents of %s = %q, want %q", path, contents, want)
	}
}
