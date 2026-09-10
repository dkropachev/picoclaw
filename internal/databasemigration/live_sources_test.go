//go:build (unix && !aix) || windows

package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestVerifyLiveSourcesRejectsLostGenerationUpdate(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{filepath.Join(home, "auth.json")},
	}
	writeMigrationFile(t, spec.Path, []byte("original generation"))
	session := snapshotLiveSources(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	if err := session.verifyLiveSources(context.Background(), spec); err != nil {
		t.Fatalf("unchanged live sources failed verification: %v", err)
	}
	writeMigrationFile(t, spec.Path, []byte("concurrent update!!"))
	if err := session.verifyLiveSources(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "changed after snapshot") {
		t.Fatalf("lost update verification = %v", err)
	}
}

func TestVerifyLiveSourcesRejectsIdentityReplacementWithIdenticalBytes(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	payload := []byte("identical replacement")
	writeMigrationFile(t, spec.Path, payload)
	session := snapshotLiveSources(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	if err := os.Rename(spec.Path, filepath.Join(t.TempDir(), "original")); err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, spec.Path, payload)
	if err := session.verifyLiveSources(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("identity replacement verification = %v", err)
	}
}

func TestVerifyLiveSourcesRejectsNewAndRemovedLegacyInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "added",
			mutate: func(t *testing.T, root string) {
				writeMigrationFile(t, filepath.Join(root, "new.json"), []byte("new"))
			},
			want: "added",
		},
		{
			name: "changed",
			mutate: func(t *testing.T, root string) {
				writeMigrationFile(t, filepath.Join(root, "old.json"), []byte("NEW"))
			},
			want: "changed after snapshot",
		},
		{
			name: "removed",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "old.json")); err != nil {
					t.Fatal(err)
				}
			},
			want: "removed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			legacyRoot := filepath.Join(home, "legacy")
			spec := storecatalog.Spec{
				ID: "global/auth", Path: filepath.Join(home, "auth.db"),
				LegacyRoots: []string{legacyRoot},
			}
			writeMigrationFile(t, filepath.Join(legacyRoot, "old.json"), []byte("old"))
			session := snapshotLiveSources(
				t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
			)
			test.mutate(t, legacyRoot)
			if err := session.verifyLiveSources(context.Background(), spec); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("legacy %s verification = %v", test.name, err)
			}
		})
	}
}

func TestVerifyLiveSourcesRejectsWALJournalCoexistence(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("database"))
	writeMigrationFile(t, spec.Path+"-wal", []byte("wal"))
	writeMigrationFile(t, spec.Path+"-journal", []byte("journal"))
	session, err := snapshotBackup(
		context.Background(), time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
		filepath.Join(home, "backups"),
	)
	if session != nil || err == nil || !strings.Contains(err.Error(), "WAL and rollback") {
		t.Fatalf("incoherent generation snapshot = %#v, %v", session, err)
	}
}

func TestVerifyLiveSourcesPreservesOverlappingLegacyRootProvenance(t *testing.T) {
	home := migrationHome(t)
	legacyRoot := filepath.Join(home, "legacy")
	nestedRoot := filepath.Join(legacyRoot, "nested")
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{nestedRoot, legacyRoot},
	}
	writeMigrationFile(t, filepath.Join(nestedRoot, "state.json"), []byte("state"))
	session := snapshotLiveSources(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	legacyCount := 0
	for _, record := range session.manifest.Files {
		if record.Role == "legacy" {
			legacyCount++
		}
	}
	if legacyCount != 2 {
		t.Fatalf("overlapping roots recorded %d legacy copies", legacyCount)
	}
	if err := session.verifyLiveSources(context.Background(), spec); err != nil {
		t.Fatalf("overlapping roots failed verification: %v", err)
	}
}

func TestVerifyLiveSourcesUsesManifestCatalogGenerationExclusions(t *testing.T) {
	home := migrationHome(t)
	selected := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{home},
	}
	other := storecatalog.Spec{ID: "global/models", Path: filepath.Join(home, "models.db")}
	writeMigrationFile(t, other.Path, []byte("other catalog generation"))
	session := snapshotLiveSources(
		t,
		home,
		[]storecatalog.Spec{selected, other},
		[]storecatalog.Spec{selected},
	)
	if err := session.verifyLiveSources(context.Background(), selected); err != nil {
		t.Fatalf("catalog exclusion was enumerated as legacy input: %v", err)
	}
}

func TestVerifyLiveSourcesRejectsNewPhysicalAlias(t *testing.T) {
	home := migrationHome(t)
	legacyRoot := filepath.Join(home, "legacy")
	source := filepath.Join(legacyRoot, "source.json")
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{legacyRoot},
	}
	writeMigrationFile(t, source, []byte("source"))
	session := snapshotLiveSources(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	if err := os.Link(source, filepath.Join(legacyRoot, "alias.json")); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := session.verifyLiveSources(context.Background(), spec); err == nil ||
		!strings.Contains(err.Error(), "physical alias") {
		t.Fatalf("physical alias verification = %v", err)
	}
}

func TestVerifyLiveSourcesContextAndManifestBoundaries(t *testing.T) {
	if err := (*backupSession)(nil).verifyLiveSources(context.Background(), storecatalog.Spec{}); err == nil {
		t.Fatal("nil live-source session succeeded")
	}
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	session := snapshotLiveSources(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.verifyLiveSources(canceled, spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled live-source verification = %v", err)
	}
	session.manifest.CatalogGenerations = nil
	if err := session.verifyLiveSources(context.Background(), spec); err == nil {
		t.Fatal("missing catalog exclusions passed live-source verification")
	}
}

func snapshotLiveSources(
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
		context.Background(), func() time.Time { return time.Unix(1, 0).UTC() },
		home, all, selected, parent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
