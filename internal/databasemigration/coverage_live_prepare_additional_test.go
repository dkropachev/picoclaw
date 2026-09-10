package databasemigration

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestAdditionalPrepareGenerationBoundaries(t *testing.T) {
	t.Run("backup verification failure", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		if err := os.Remove(filepath.Join(session.root, backupManifestHash)); err != nil {
			t.Fatal(err)
		}
		main, cleanup, err := session.prepareGeneration(t.Context(), spec)
		if main != nil || cleanup == nil || err == nil ||
			!strings.Contains(err.Error(), "verify generation backup") {
			t.Fatalf("corrupt generation backup = %q, %v", main, err)
		}
	})

	t.Run("store selection", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
		session := additionalSnapshot(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})

		main, cleanup, err := session.prepareGeneration(t.Context(), storecatalog.Spec{ID: "global/models"})
		if main != nil || cleanup == nil || err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("unknown store generation = %q, %v", main, err)
		}

		main, cleanup, err = session.prepareGeneration(t.Context(), storecatalog.Spec{
			ID: "global/auth", LegacyRoots: []string{filepath.Join(home, "legacy.json")},
		})
		if main != nil || cleanup == nil || err == nil {
			t.Fatalf("changed legacy roots = %q, %v", main, err)
		}
	})

	t.Run("copy cancellation", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		ctx := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 3}
		main, cleanup, err := session.prepareGeneration(ctx, spec)
		if main != nil || cleanup == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled generation copy = %q, %v", main, err)
		}
	})

	t.Run("copy differs after verification", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		record := session.manifest.Files[0]
		backupPath := filepath.Join(session.root, filepath.FromSlash(record.Backup))
		info, err := os.Lstat(backupPath)
		if err != nil {
			t.Fatal(err)
		}
		replacement := []byte(strings.Repeat("x", int(record.Size)))
		ctx := &actionAfterMigrationErrChecks{
			Context: context.Background(),
			allowed: 3,
			action: func() {
				if writeErr := os.WriteFile(backupPath, replacement, 0o600); writeErr != nil {
					t.Errorf("replace verified backup: %v", writeErr)
					return
				}
				if timeErr := os.Chtimes(backupPath, info.ModTime(), info.ModTime()); timeErr != nil {
					t.Errorf("restore verified backup timestamp: %v", timeErr)
				}
			},
		}
		main, cleanup, err := session.prepareGeneration(ctx, spec)
		if main != nil || cleanup == nil || err == nil {
			t.Fatalf("changed generation copy = %q, %v", main, err)
		}
	})
}

func TestAdditionalPrepareLegacyInputsBoundaries(t *testing.T) {
	fresh := func(t *testing.T) (storecatalog.Spec, *backupSession) {
		t.Helper()
		home := migrationHome(t)
		legacy := filepath.Join(home, "legacy", "auth.json")
		spec := storecatalog.Spec{
			ID: "global/auth", Path: filepath.Join(home, "auth.db"), LegacyRoots: []string{legacy},
		}
		writeMigrationFile(t, legacy, []byte("legacy payload"))
		return spec, additionalSnapshot(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	}

	t.Run("catalog mismatch", func(t *testing.T) {
		spec, session := fresh(t)
		changed := spec
		changed.LegacyRoots = append(changed.LegacyRoots, filepath.Join(filepath.Dir(spec.Path), "other"))
		if roots, cleanup, err := session.prepareLegacyInputs(t.Context(), changed); roots != nil ||
			cleanup == nil || err == nil {
			t.Fatalf("changed roots = %#v, %v", roots, err)
		}
	})

	t.Run("invalid manifest and root", func(t *testing.T) {
		spec, session := fresh(t)
		session.manifest.CaptureMode = "invalid"
		if roots, cleanup, err := session.prepareLegacyInputs(t.Context(), spec); roots != nil ||
			cleanup == nil || err == nil || !strings.Contains(err.Error(), "validate") {
			t.Fatalf("invalid manifest = %#v, %v", roots, err)
		}

		spec, session = fresh(t)
		spec.LegacyRoots[0] = "relative-legacy"
		if roots, cleanup, err := session.prepareLegacyInputs(t.Context(), spec); roots != nil ||
			cleanup == nil || err == nil {
			t.Fatalf("relative live root = %#v, %v", roots, err)
		}
	})

	t.Run("missing backup bytes", func(t *testing.T) {
		spec, session := fresh(t)
		record := session.manifest.Files[0]
		if err := os.Remove(filepath.Join(session.root, filepath.FromSlash(record.Backup))); err != nil {
			t.Fatal(err)
		}
		if roots, cleanup, err := session.prepareLegacyInputs(t.Context(), spec); roots != nil ||
			cleanup == nil || err == nil {
			t.Fatalf("missing legacy backup = %#v, %v", roots, err)
		}
	})

	t.Run("copied bytes differ from manifest", func(t *testing.T) {
		spec, session := fresh(t)
		session.manifest.Files[0].SHA256 = strings.Repeat("0", sha256.Size*2)
		if roots, cleanup, err := session.prepareLegacyInputs(t.Context(), spec); roots != nil ||
			cleanup == nil || err == nil {
			t.Fatalf("mismatched legacy digest = %#v, %v", roots, err)
		}
	})
}

func additionalGenerationSnapshot(t *testing.T) (string, storecatalog.Spec, *backupSession) {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("original database"))
	return home, spec, additionalSnapshot(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
}

func additionalLiveSnapshot(t *testing.T) (string, storecatalog.Spec, *backupSession) {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{filepath.Join(home, "legacy")},
	}
	writeMigrationFile(t, spec.Path, []byte("original database"))
	writeMigrationFile(t, filepath.Join(spec.LegacyRoots[0], "auth.json"), []byte("legacy payload"))
	return home, spec, additionalSnapshot(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
}

func additionalSnapshot(
	t *testing.T,
	home string,
	all []storecatalog.Spec,
	selected []storecatalog.Spec,
) *backupSession {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "backup-parent")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	session, err := snapshotBackup(
		t.Context(), func() time.Time { return time.Unix(2, 0).UTC() },
		home, all, selected, parent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
