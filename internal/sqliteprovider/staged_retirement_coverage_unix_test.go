//go:build unix && !aix

package sqliteprovider

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type stagedRetirementCoverageContext struct {
	calls    int
	cancelAt int
	onErr    func(int)
}

func (*stagedRetirementCoverageContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (*stagedRetirementCoverageContext) Done() <-chan struct{} { return nil }

func (ctx *stagedRetirementCoverageContext) Err() error {
	ctx.calls++
	if ctx.onErr != nil {
		ctx.onErr(ctx.calls)
	}
	if ctx.cancelAt > 0 && ctx.calls >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func (*stagedRetirementCoverageContext) Value(any) any { return nil }

func TestStagedRetirementCommonStateCoverage(t *testing.T) {
	t.Run("canceled after retention", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		ctx := &stagedRetirementCoverageContext{cancelAt: 2}
		retained, err := retainStagedGeneration(ctx, stage)
		if retained != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("retainStagedGeneration(second cancellation) = %#v, %v", retained, err)
		}
		assertStagedRetirementContents(t, stage, "retained")
	})

	t.Run("check inputs and states", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		if err := retained.Check(nil, stage); err == nil {
			t.Fatal("Check(nil context) succeeded")
		}
		if err := retained.Check(context.Background(), "relative.db"); err == nil {
			t.Fatal("Check(relative path) succeeded")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := retained.Check(ctx, stage); !errors.Is(err, context.Canceled) {
			t.Fatalf("Check(canceled) error = %v", err)
		}
		other := filepath.Join(t.TempDir(), "other.db")
		if err := retained.Check(context.Background(), other); err == nil {
			t.Fatal("Check(different parent) succeeded")
		}
		withoutPlatform := &retainedStagedGeneration{}
		if err := withoutPlatform.Check(context.Background(), stage); err == nil {
			t.Fatal("Check(without platform) succeeded")
		}
		if err := withoutPlatform.Close(); err != nil {
			t.Fatalf("Close(without platform) error = %v", err)
		}
	})

	t.Run("retired and closed checks", func(t *testing.T) {
		retiredStage := writeStagedRetirementFile(t, t.TempDir(), "retired.db", "retired")
		retired := mustRetainStagedGeneration(t, retiredStage)
		if err := retired.Retire(context.Background()); err != nil {
			t.Fatalf("Retire() error = %v", err)
		}
		if err := retired.Check(context.Background(), retiredStage); err == nil {
			t.Fatal("Check(retired) succeeded")
		}

		closedStage := writeStagedRetirementFile(t, t.TempDir(), "closed.db", "closed")
		closed := mustRetainStagedGeneration(t, closedStage)
		if err := closed.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if err := closed.Check(context.Background(), closedStage); err == nil {
			t.Fatal("Check(closed) succeeded")
		}
	})

	t.Run("unsafe ancestor", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatalf("mkdir real parent: %v", err)
		}
		linkedParent := filepath.Join(root, "linked")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatalf("symlink parent: %v", err)
		}
		if err := validateStagedRetirementPath(filepath.Join(linkedParent, "stage.db")); err == nil {
			t.Fatal("validateStagedRetirementPath accepted a symlink ancestor")
		}
	})

	t.Run("regular-file ancestor", func(t *testing.T) {
		ancestor := filepath.Join(t.TempDir(), "ancestor")
		if err := os.WriteFile(ancestor, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateStagedRetirementPath(filepath.Join(ancestor, "stage.db")); err == nil {
			t.Fatal("validateStagedRetirementPath accepted a regular-file ancestor")
		}
	})

	t.Run("exhausted generated names", func(t *testing.T) {
		calls := 0
		leaf, err := unusedStagedRetirementLeaf(func(string) (bool, error) {
			calls++
			return false, nil
		})
		if leaf != "" || err == nil || calls != stagedRetirementNameAttempts {
			t.Fatalf("unusedStagedRetirementLeaf(exhausted) = %q, %v after %d calls", leaf, err, calls)
		}
	})
}

func TestStagedRetirementUnixCaptureFailureCoverage(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "stage.db")
		retained, err := retainStagedGenerationPlatform(path)
		if retained != nil || err == nil {
			t.Fatalf("retainStagedGenerationPlatform(missing parent) = %#v, %v", retained, err)
		}
	})

	t.Run("symlink parent handle", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatalf("mkdir real parent: %v", err)
		}
		writeStagedRetirementFile(t, realParent, "stage.db", "retained")
		linkedParent := filepath.Join(root, "linked")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatalf("symlink parent: %v", err)
		}
		retained, err := retainStagedGenerationPlatform(filepath.Join(linkedParent, "stage.db"))
		if retained != nil || err == nil {
			t.Fatalf("retainStagedGenerationPlatform(symlink parent) = %#v, %v", retained, err)
		}
	})

	t.Run("unsafe parent mode", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatalf("chmod parent: %v", err)
		}
		retained, err := retainStagedGenerationPlatform(stage)
		if retained != nil || err == nil {
			t.Fatalf("retainStagedGenerationPlatform(unsafe parent) = %#v, %v", retained, err)
		}
	})
}

func TestStagedRetirementUnixInternalStateCoverage(t *testing.T) {
	t.Run("invalid platform", func(t *testing.T) {
		var nilPlatform *stagedRetirementPlatform
		if err := nilPlatform.retire(context.Background()); err == nil {
			t.Fatal("nil platform retirement succeeded")
		}
		if err := (&stagedRetirementPlatform{}).retire(context.Background()); err == nil {
			t.Fatal("empty platform retirement succeeded")
		}
		if err := nilPlatform.close(); err != nil {
			t.Fatalf("nil platform close error = %v", err)
		}
	})

	t.Run("canceled immediately before rename", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		ctx := &stagedRetirementCoverageContext{cancelAt: 2}
		if err := retained.Retire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("Retire(second cancellation) error = %v", err)
		}
		assertStagedRetirementContents(t, stage, "retained")
	})

	t.Run("generated namespace exhausted", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		collision := filepath.Join(
			root,
			".sqlite-retirement-"+string(bytes.Repeat([]byte{'0'}, stagedRetirementRandomBytes*2)),
		)
		if err := os.WriteFile(collision, []byte("collision"), 0o600); err != nil {
			t.Fatalf("write retirement collision: %v", err)
		}
		originalReader := cryptorand.Reader
		cryptorand.Reader = bytes.NewReader(make([]byte, stagedRetirementRandomBytes*stagedRetirementNameAttempts))
		defer func() { cryptorand.Reader = originalReader }()
		if err := retained.Retire(context.Background()); err == nil {
			t.Fatal("Retire() succeeded with an exhausted quarantine namespace")
		}
		assertStagedRetirementContents(t, stage, "retained")
		assertStagedRetirementContents(t, collision, "collision")
	})

	t.Run("rename handle failure", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained, err := retainStagedGeneration(context.Background(), stage)
		if err != nil {
			t.Fatalf("retainStagedGeneration() error = %v", err)
		}
		ctx := &stagedRetirementCoverageContext{onErr: func(call int) {
			if call == 2 {
				_ = retained.platform.parent.Close()
			}
		}}
		if err := retained.Retire(ctx); err == nil {
			t.Fatal("Retire() succeeded with a closed parent before rename")
		}
		assertStagedRetirementContents(t, stage, "retained")
		retained.platform.parent = nil
		if err := retained.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("empty quarantine", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		if err := retained.platform.validateQuarantine(); err == nil {
			t.Fatal("validateQuarantine() accepted an empty name")
		}
	})

	t.Run("missing quarantine", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		quarantine := ".quarantine"
		quarantinePath := filepath.Join(root, quarantine)
		moved := quarantinePath + ".moved"
		if err := os.Rename(stage, quarantinePath); err != nil {
			t.Fatalf("quarantine stage: %v", err)
		}
		retained.platform.quarantine = quarantine
		if err := os.Rename(quarantinePath, moved); err != nil {
			t.Fatalf("move quarantine: %v", err)
		}
		if err := retained.platform.validateQuarantine(); err == nil {
			t.Fatal("validateQuarantine() accepted a missing quarantine")
		}
		if err := os.Rename(moved, stage); err != nil {
			t.Fatalf("restore stage: %v", err)
		}
	})

	t.Run("closed stage handle", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained, err := retainStagedGeneration(context.Background(), stage)
		if err != nil {
			t.Fatalf("retainStagedGeneration() error = %v", err)
		}
		retained.platform.quarantine = ".quarantine"
		if err := retained.platform.stage.Close(); err != nil {
			t.Fatalf("close stage handle: %v", err)
		}
		if err := retained.platform.validateQuarantine(); err == nil {
			t.Fatal("validateQuarantine() accepted a closed stage handle")
		}
		retained.platform.stage = nil
		if err := retained.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("closed parent handle", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained, err := retainStagedGeneration(context.Background(), stage)
		if err != nil {
			t.Fatalf("retainStagedGeneration() error = %v", err)
		}
		retained.platform.quarantine = ".quarantine"
		if err := retained.platform.parent.Close(); err != nil {
			t.Fatalf("close parent handle: %v", err)
		}
		if err := retained.platform.validateQuarantine(); err == nil {
			t.Fatal("validateQuarantine() accepted a closed parent handle")
		}
		if available, err := retained.platform.quarantineAvailable("unused"); available || err == nil {
			t.Fatalf("quarantineAvailable(closed parent) = %t, %v", available, err)
		}
		if err := retained.platform.requireMissing("unused"); err == nil {
			t.Fatal("requireMissing(closed parent) succeeded")
		}
		retained.platform.parent = nil
		if err := retained.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	t.Run("unsafe retained parent", func(t *testing.T) {
		root := t.TempDir()
		stage := writeStagedRetirementFile(t, root, "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatalf("chmod parent: %v", err)
		}
		if err := retained.platform.validateParent(); err == nil {
			t.Fatal("validateParent() accepted an unsafe mode")
		}
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatalf("restore parent mode: %v", err)
		}
	})

	t.Run("unlinked proof rejects linked stage", func(t *testing.T) {
		stage := writeStagedRetirementFile(t, t.TempDir(), "stage.db", "retained")
		retained := mustRetainStagedGeneration(t, stage)
		retained.platform.quarantine = ".missing-quarantine"
		if err := retained.platform.finishUnlinked(); err == nil {
			t.Fatal("finishUnlinked() accepted a linked stage")
		}
		assertStagedRetirementContents(t, stage, "retained")
	})
}
