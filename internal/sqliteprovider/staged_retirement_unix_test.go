//go:build unix && !aix

package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRetainedStagedGenerationRetiresExactIdentity(t *testing.T) {
	stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "exact")
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
	var stat unix.Stat_t
	if err := unix.Fstat(int(retained.platform.stage.Fd()), &stat); err != nil {
		t.Fatalf("Fstat(retained stage) error = %v", err)
	}
	if stat.Nlink != 0 || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatalf("retained stage after retirement = mode %#o, links %d", stat.Mode, stat.Nlink)
	}
	if err := retained.Retire(context.Background()); err != nil {
		t.Fatalf("second Retire() error = %v", err)
	}
	if err := retained.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := retained.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestRetainedStagedGenerationChecksInstalledIdentity(t *testing.T) {
	root := t.TempDir()
	stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
	installed := filepath.Join(root, "installed.db")
	retained := mustRetainStagedGeneration(t, stage)
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

func TestRetainedStagedGenerationCancellationPreservesStage(t *testing.T) {
	t.Run("capture", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "capture")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		retained, err := retainStagedGeneration(ctx, stage)
		if retained != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("retainStagedGeneration(canceled) = %#v, %v", retained, err)
		}
		assertStagedRetirementContents(t, stage, "capture")
	})

	t.Run("retire", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retire")
		retained := mustRetainStagedGeneration(t, stage)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := retained.Retire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Retire(canceled) error = %v", err)
		}
		assertStagedRetirementContents(t, stage, "retire")
	})
}

func TestRetainedStagedGenerationRejectsPathTransitions(t *testing.T) {
	t.Run("rename", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		moved := stage + ".moved"
		if err := os.Rename(stage, moved); err != nil {
			t.Fatalf("rename stage: %v", err)
		}
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a renamed stage")
		}
		assertStagedRetirementContents(t, moved, "retained")
	})

	t.Run("pathname replacement", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		moved := stage + ".moved"
		if err := os.Rename(stage, moved); err != nil {
			t.Fatalf("rename stage: %v", err)
		}
		writeStagedRetirementFile(t, root, "stage.db", "decoy")
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a pathname replacement")
		}
		assertStagedRetirementContents(t, moved, "retained")
		assertStagedRetirementContents(t, stage, "decoy")
	})

	t.Run("parent replacement", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "private")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatalf("mkdir parent: %v", err)
		}
		stage := writeStagedRetirementFile(t, parent, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		movedParent := parent + ".moved"
		if err := os.Rename(parent, movedParent); err != nil {
			t.Fatalf("rename parent: %v", err)
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatalf("replace parent: %v", err)
		}
		decoy := writeStagedRetirementFile(t, parent, "stage.db", "decoy")
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a replaced parent")
		}
		assertStagedRetirementContents(t, filepath.Join(movedParent, "stage.db"), "retained")
		assertStagedRetirementContents(t, decoy, "decoy")
	})
}

func TestRetainedStagedGenerationRejectsLinkCountDrift(t *testing.T) {
	root := t.TempDir()
	stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
	retained := mustRetainStagedGeneration(t, stage)
	alias := filepath.Join(root, "alias.db")
	if err := os.Link(stage, alias); err != nil {
		t.Fatalf("hardlink stage: %v", err)
	}
	if err := retained.Retire(context.Background()); err == nil {
		t.Fatal("Retire() accepted hard-link drift")
	}
	assertStagedRetirementContents(t, stage, "retained")
	assertStagedRetirementContents(t, alias, "retained")
}

func TestRetainStagedGenerationRejectsUnsafeObjects(t *testing.T) {
	root := t.TempDir()
	target := writeStagedRetirementFile(t, root, "target.db", "target")
	symlink := filepath.Join(root, "symlink.db")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("symlink stage: %v", err)
	}
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	fifo := filepath.Join(root, "fifo.db")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo stage: %v", err)
	}
	hardlink := filepath.Join(root, "hardlink.db")
	if err := os.Link(target, hardlink); err != nil {
		t.Fatalf("hardlink stage: %v", err)
	}
	wide := writeStagedRetirementFile(t, root, "wide.db", "wide")
	if err := os.Chmod(wide, 0o644); err != nil {
		t.Fatalf("chmod stage: %v", err)
	}
	for _, path := range []string{symlink, directory, fifo, hardlink, wide} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			retained, err := retainStagedGeneration(context.Background(), path)
			if retained != nil || err == nil {
				t.Fatalf("retainStagedGeneration(%s) = %#v, %v", path, retained, err)
			}
		})
	}
	assertStagedRetirementContents(t, target, "target")
}

func TestRetainedStagedGenerationRejectsLateQuarantineTransitions(t *testing.T) {
	t.Run("source reappears", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		quarantine := ".manual-quarantine"
		if err := unix.Renameat(
			int(retained.platform.parent.Fd()), retained.platform.leaf,
			int(retained.platform.parent.Fd()), quarantine,
		); err != nil {
			t.Fatalf("quarantine stage: %v", err)
		}
		retained.platform.quarantine = quarantine
		writeStagedRetirementFile(t, root, "stage.db", "decoy")
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a reappeared source name")
		}
		assertStagedRetirementContents(t, filepath.Join(root, quarantine), "retained")
		assertStagedRetirementContents(t, stage, "decoy")
	})

	t.Run("quarantine replacement", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		quarantine := ".manual-quarantine"
		quarantinePath := filepath.Join(root, quarantine)
		if err := os.Rename(stage, quarantinePath); err != nil {
			t.Fatalf("quarantine stage: %v", err)
		}
		retained.platform.quarantine = quarantine
		moved := quarantinePath + ".moved"
		if err := os.Rename(quarantinePath, moved); err != nil {
			t.Fatalf("move quarantine: %v", err)
		}
		writeStagedRetirementFile(t, root, quarantine, "decoy")
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() accepted a replaced quarantine")
		}
		assertStagedRetirementContents(t, moved, "retained")
		assertStagedRetirementContents(t, quarantinePath, "decoy")
	})
}

func TestRetainedStagedGenerationCompletesAlreadyUnlinkedProof(t *testing.T) {
	root := t.TempDir()
	stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
	retained := mustRetainStagedGeneration(t, stage)
	quarantine := ".manual-quarantine"
	if err := os.Rename(stage, filepath.Join(root, quarantine)); err != nil {
		t.Fatalf("quarantine stage: %v", err)
	}
	if err := os.Remove(filepath.Join(root, quarantine)); err != nil {
		t.Fatalf("unlink quarantine: %v", err)
	}
	retained.platform.quarantine = quarantine
	retained.platform.unlinked = true
	if err := retained.Retire(context.Background()); err != nil {
		t.Fatalf("Retire(already unlinked) error = %v", err)
	}
}

func TestRetainedStagedGenerationCloseDoesNotRetire(t *testing.T) {
	stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
	retained := mustRetainStagedGeneration(t, stage)
	if err := retained.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := retained.Retire(context.Background()); err == nil {
		t.Fatal("Retire() accepted a closed capability")
	}
	assertStagedRetirementContents(t, stage, "retained")
	var nilRetained *retainedStagedGeneration
	if err := nilRetained.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
	if err := nilRetained.Retire(context.Background()); err == nil {
		t.Fatal("nil Retire() succeeded")
	}
}

func TestStagedRetirementInputValidation(t *testing.T) {
	stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
	for _, path := range []string{"", "relative.db", stage + string(os.PathSeparator) + ".."} {
		if retained, err := retainStagedGeneration(context.Background(), path); retained != nil || err == nil {
			t.Fatalf("retainStagedGeneration(%q) = %#v, %v", path, retained, err)
		}
	}
	if retained, err := retainStagedGeneration(nil, stage); retained != nil || err == nil {
		t.Fatalf("retainStagedGeneration(nil) = %#v, %v", retained, err)
	}
	if err := (*retainedStagedGeneration)(nil).Retire(nil); err == nil {
		t.Fatal("Retire(nil) succeeded")
	}
	if err := (*retainedStagedGeneration)(nil).Check(context.Background(), stage); err == nil {
		t.Fatal("Check on nil retention succeeded")
	}
	if leaf, err := unusedStagedRetirementLeaf(nil); leaf != "" || err == nil {
		t.Fatalf("unusedStagedRetirementLeaf(nil) = %q, %v", leaf, err)
	}
	if leaf, err := unusedStagedRetirementLeaf(func(string) (bool, error) {
		return false, errors.New("inspection failed")
	}); leaf != "" || err == nil {
		t.Fatalf("unusedStagedRetirementLeaf(error) = %q, %v", leaf, err)
	}
	leaf, err := unusedStagedRetirementLeaf(func(string) (bool, error) { return true, nil })
	if err != nil || !strings.HasPrefix(leaf, ".sqlite-retirement-") {
		t.Fatalf("unusedStagedRetirementLeaf() = %q, %v", leaf, err)
	}
}

func mustRetainStagedGeneration(t *testing.T, path string) *retainedStagedGeneration {
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

func writeStagedRetirementFile(t *testing.T, root, name, contents string) string {
	t.Helper()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("secure staged retirement parent: %v", err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write staged retirement file: %v", err)
	}
	return path
}

func assertStagedRetirementContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(contents) != want {
		t.Fatalf("contents of %s = %q, want %q", path, contents, want)
	}
}
