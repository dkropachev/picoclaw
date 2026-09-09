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

func TestBackupArchiveVerifyFilesArchiveAndControlChanges(t *testing.T) {
	home := migrationHome(t)
	auth := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	models := storecatalog.Spec{ID: "global/models", Path: filepath.Join(home, "models.db")}
	writeMigrationFile(t, auth.Path, []byte("auth"))
	writeMigrationFile(t, models.Path, []byte("models"))
	session := backupArchiveSnapshot(
		t, home, []storecatalog.Spec{auth, models}, []storecatalog.Spec{auth, models},
	)
	for _, record := range session.manifest.Files {
		if record.StoreID == models.ID.String() {
			if err := os.Remove(filepath.Join(session.root, filepath.FromSlash(record.Backup))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := session.verifyStore(nil, auth.ID); err == nil {
		t.Fatal("selected-store verification accepted incomplete exact archive")
	}
	if err := session.verify(t.Context()); err == nil {
		t.Fatal("full verification ignored missing other-store bytes")
	}
	if err := session.verifyStore(t.Context(), ""); err == nil {
		t.Fatal("invalid store identity verified")
	}

	_, _, changed := backupArchiveGenerationSnapshot(t)
	changed.manifest.CreatedAt = changed.manifest.CreatedAt.Add(time.Second)
	if err := changed.verifyStore(t.Context(), "global/auth"); err == nil ||
		!strings.Contains(err.Error(), "manifest changed") {
		t.Fatalf("in-memory manifest change = %v", err)
	}

	_, _, badHash := backupArchiveGenerationSnapshot(t)
	writeMigrationFile(t, filepath.Join(badHash.root, backupManifestHash), []byte(strings.Repeat("0", 64)+"\n"))
	if err := badHash.verifyStore(t.Context(), "global/auth"); err == nil ||
		!strings.Contains(err.Error(), "hash is invalid") {
		t.Fatalf("manifest hash change = %v", err)
	}
}

func TestBackupArchiveVerifyStoreRejectsTamperedSiblingStoreArchive(t *testing.T) {
	home := migrationHome(t)
	auth := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	models := storecatalog.Spec{ID: "global/models", Path: filepath.Join(home, "models.db")}
	writeMigrationFile(t, auth.Path, []byte("auth"))
	writeMigrationFile(t, models.Path, []byte("model"))
	session := backupArchiveSnapshot(
		t, home, []storecatalog.Spec{auth, models}, []storecatalog.Spec{auth, models},
	)
	for _, record := range session.manifest.Files {
		if record.StoreID != models.ID.String() {
			continue
		}
		path := filepath.Join(session.root, filepath.FromSlash(record.Backup))
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	if err := session.verifyStore(t.Context(), auth.ID); err == nil {
		t.Fatal("selected-store verification accepted tampered sibling archive")
	}
}

func TestBackupArchiveVerifyLiveSourcesManifestAndFilesystemBoundaries(t *testing.T) {
	t.Run("catalog exclusion inspection", func(t *testing.T) {
		_, spec, session := backupArchiveLiveSnapshot(t)
		unsafe := t.TempDir()
		session.manifest.CatalogGenerations = append(session.manifest.CatalogGenerations, unsafe)
		if err := session.verifyLiveSources(t.Context(), spec); err == nil {
			t.Fatalf("directory exclusion = %v", err)
		}

		_, spec, session = backupArchiveLiveSnapshot(t)
		parentFile := filepath.Join(t.TempDir(), "file")
		writeMigrationFile(t, parentFile, []byte("file"))
		session.manifest.CatalogGenerations = append(
			session.manifest.CatalogGenerations, filepath.Join(parentFile, "child"),
		)
		if err := session.verifyLiveSources(t.Context(), spec); err == nil {
			t.Fatalf("uninspectable exclusion = %v", err)
		}
	})

	for _, test := range []struct {
		name   string
		want   string
		mutate func(*testing.T, *storecatalog.Spec, *backupSession)
	}{
		{
			name: "duplicate store", want: "store manifest is duplicated",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				session.manifest.Stores = append(session.manifest.Stores, session.manifest.Stores[0])
			},
		},
		{
			name: "duplicate generation", want: "generation role is duplicated",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				session.manifest.Files = append(session.manifest.Files, session.manifest.Files[0])
			},
		},
		{
			name: "duplicate legacy", want: "legacy source is duplicated",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				for _, record := range session.manifest.Files {
					if record.Role == "legacy" {
						session.manifest.Files = append(session.manifest.Files, record)
						return
					}
				}
			},
		},
		{
			name: "invalid role", want: "source role is invalid",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				session.manifest.Files[0].Role = "other"
			},
		},
		{
			name: "inconsistent existence", want: "manifest is inconsistent",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				session.manifest.Stores[0].Exists = false
			},
		},
		{
			name: "sidecar without database", want: "sidecar without",
			mutate: func(_ *testing.T, spec *storecatalog.Spec, session *backupSession) {
				for index := range session.manifest.Files {
					if session.manifest.Files[index].Role == "database" {
						session.manifest.Files[index].Role = "wal"
						session.manifest.Files[index].Source = spec.Path + "-wal"
					}
				}
				session.manifest.Stores[0].Exists = false
			},
		},
		{
			name: "wal and journal", want: "WAL and rollback",
			mutate: func(_ *testing.T, spec *storecatalog.Spec, session *backupSession) {
				wal := session.manifest.Files[0]
				wal.Role, wal.Source = "wal", spec.Path+"-wal"
				journal := session.manifest.Files[0]
				journal.Role, journal.Source = "journal", spec.Path+"-journal"
				session.manifest.Files = append(session.manifest.Files, wal, journal)
			},
		},
		{
			name: "invalid legacy source", want: "legacy source path is invalid",
			mutate: func(_ *testing.T, _ *storecatalog.Spec, session *backupSession) {
				for index := range session.manifest.Files {
					if session.manifest.Files[index].Role == "legacy" {
						session.manifest.Files[index].Source = "relative"
					}
				}
			},
		},
		{
			name: "legacy overlaps generation", want: "overlaps a catalog generation",
			mutate: func(_ *testing.T, spec *storecatalog.Spec, session *backupSession) {
				for index := range session.manifest.Files {
					if session.manifest.Files[index].Role == "legacy" {
						session.manifest.Files[index].Source = spec.Path
					}
				}
			},
		},
		{
			name: "legacy outside roots", want: "outside its catalog roots",
			mutate: func(t *testing.T, _ *storecatalog.Spec, session *backupSession) {
				for index := range session.manifest.Files {
					if session.manifest.Files[index].Role == "legacy" {
						session.manifest.Files[index].Source = filepath.Join(t.TempDir(), "outside.json")
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, spec, session := backupArchiveLiveSnapshot(t)
			test.mutate(t, &spec, session)
			if err := session.verifyLiveSources(t.Context(), spec); err == nil {
				t.Fatalf("verifyLiveSources() = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("generation presence and metadata", func(t *testing.T) {
		_, spec, session := backupArchiveLiveSnapshot(t)
		if err := os.Remove(spec.Path); err != nil {
			t.Fatal(err)
		}
		if err := session.verifyLiveSources(t.Context(), spec); err == nil ||
			!strings.Contains(err.Error(), "presence changed") {
			t.Fatalf("removed generation = %v", err)
		}

		_, spec, session = backupArchiveLiveSnapshot(t)
		if err := os.Chmod(spec.Path, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := session.verifyLiveSources(t.Context(), spec); err == nil ||
			!strings.Contains(err.Error(), "metadata changed") {
			t.Fatalf("generation mode change = %v", err)
		}
	})

	t.Run("legacy root boundaries", func(t *testing.T) {
		_, spec, session := backupArchiveLiveSnapshot(t)
		spec.LegacyRoots = make([]string, backupMaxEntries+1)
		if err := session.verifyLiveSources(t.Context(), spec); err == nil {
			t.Fatalf("legacy root count = %v", err)
		}

		_, spec, session = backupArchiveLiveSnapshot(t)
		spec.LegacyRoots = []string{"relative"}
		if err := session.verifyLiveSources(t.Context(), spec); err == nil {
			t.Fatalf("relative legacy root = %v", err)
		}

		_, spec, session = backupArchiveLiveSnapshot(t)
		blocker := filepath.Join(filepath.Dir(spec.Path), "blocker")
		writeMigrationFile(t, blocker, []byte("blocker"))
		spec.LegacyRoots = []string{filepath.Join(blocker, "child")}
		for index := len(session.manifest.Files) - 1; index >= 0; index-- {
			if session.manifest.Files[index].Role == "legacy" {
				session.manifest.Files = append(session.manifest.Files[:index], session.manifest.Files[index+1:]...)
			}
		}
		if err := session.verifyLiveSources(t.Context(), spec); err == nil {
			t.Fatalf("unreadable legacy root = %v", err)
		}
	})
}

func TestBackupArchiveVerifyLiveSourceRecordBoundaries(t *testing.T) {
	newRecord := func(t *testing.T) (string, BackupFileManifest) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "source")
		writeMigrationFile(t, path, []byte("payload"))
		return path, backupArchiveLiveRecord(t, path)
	}

	for _, test := range []struct {
		name   string
		want   string
		mutate func(*BackupFileManifest)
	}{
		{
			name: "missing identity", want: "identity changed",
			mutate: func(record *BackupFileManifest) { record.SourceIdentity = "" },
		},
		{name: "negative size", want: "metadata changed", mutate: func(record *BackupFileManifest) { record.Size = -1 }},
		{
			name: "invalid source mode", want: "metadata changed",
			mutate: func(record *BackupFileManifest) { record.SourceMode = 1 << 16 },
		},
		{name: "invalid digest", want: "content changed", mutate: func(record *BackupFileManifest) { record.SHA256 = "bad" }},
		{name: "size mismatch", want: "metadata changed", mutate: func(record *BackupFileManifest) { record.Size++ }},
		{
			name: "identity mismatch", want: "identity changed",
			mutate: func(record *BackupFileManifest) { record.SourceIdentity = "different" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, record := newRecord(t)
			test.mutate(&record)
			if _, err := verifyLiveSourceRecord(t.Context(), path, record); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyLiveSourceRecord() = %v, want %q", err, test.want)
			}
		})
	}

	t.Run("canceled hash", func(t *testing.T) {
		path, record := newRecord(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := verifyLiveSourceRecord(ctx, path, record); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled live-source hash = %v", err)
		}
	})

	for _, test := range []struct {
		name           string
		restoreModTime bool
		want           string
	}{
		{name: "metadata changes during verification", want: "changed during verification"},
		{name: "content changes during verification", restoreModTime: true, want: "content changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, record := newRecord(t)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx := &actionAfterMigrationErrChecks{
				Context: context.Background(),
				action: func() {
					if writeErr := os.WriteFile(path, []byte("PAYLOAD"), 0o600); writeErr != nil {
						t.Errorf("change live source: %v", writeErr)
						return
					}
					modTime := info.ModTime().Add(time.Hour)
					if test.restoreModTime {
						modTime = info.ModTime()
					}
					if timeErr := os.Chtimes(path, modTime, modTime); timeErr != nil {
						t.Errorf("set live source timestamp: %v", timeErr)
					}
				},
			}
			if _, err := verifyLiveSourceRecord(ctx, path, record); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyLiveSourceRecord() = %v, want %q", err, test.want)
			}
		})
	}
}
