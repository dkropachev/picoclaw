package databasemigration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupManifestValidationAcceptsLifecycleStatesAndFileRoles(t *testing.T) {
	for _, outcome := range []string{
		"snapshotting", "snapshot_complete", "migration_in_progress",
		"dry_run", "complete", "failed", "outcome_unknown",
	} {
		t.Run(outcome, func(t *testing.T) {
			manifest := validManifestValidationFixture(t)
			manifest.Outcome = outcome
			if err := validateBackupManifest(manifest); err != nil {
				t.Fatalf("valid outcome %q: %v", outcome, err)
			}
		})
	}

	for _, role := range []string{"database", "wal", "shm", "journal", "legacy"} {
		t.Run(role, func(t *testing.T) {
			manifest := validManifestValidationFixture(t)
			if role != "legacy" {
				manifest.Files[0].Role = "database"
				manifest.Stores[0].Exists = true
				if role != "database" {
					sidecar := manifest.Files[0]
					sidecar.Role = role
					sidecar.Source = filepath.Join(t.TempDir(), role)
					sidecar.SourceIdentity = "fixture-" + role
					sidecar.Backup = "stores/global.auth/generation/" + role
					manifest.Files = append(manifest.Files, sidecar)
				}
			}
			if err := validateBackupManifest(manifest); err != nil {
				t.Fatalf("valid role %q: %v", role, err)
			}
		})
	}
}

func TestBackupManifestValidationRejectsMalformedMetadata(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	for _, test := range []struct {
		name   string
		mutate func(*BackupManifest)
		want   string
	}{
		{name: "version", mutate: func(m *BackupManifest) { m.Version++ }, want: "version"},
		{name: "zero timestamp", mutate: func(m *BackupManifest) { m.CreatedAt = time.Time{} }, want: "timestamp"},
		{
			name: "non UTC timestamp",
			mutate: func(m *BackupManifest) {
				m.CreatedAt = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.FixedZone("other", 3600))
			},
			want: "timestamp",
		},
		{name: "outcome", mutate: func(m *BackupManifest) { m.Outcome = "unknown" }, want: "outcome"},
		{name: "error UTF-8", mutate: func(m *BackupManifest) { m.Error = invalidUTF8 }, want: "error"},
		{
			name:   "error length",
			mutate: func(m *BackupManifest) { m.Error = strings.Repeat("e", backupMaxErrorBytes+1) },
			want:   "error",
		},
		{name: "store ID", mutate: func(m *BackupManifest) { m.Stores[0].StoreID = "../auth" }, want: "store ID"},
		{
			name:   "duplicate store",
			mutate: func(m *BackupManifest) { m.Stores = append(m.Stores, m.Stores[0]) },
			want:   "store is duplicated",
		},
		{name: "no stores", mutate: func(m *BackupManifest) { m.Stores = nil }, want: "no stores"},
		{
			name:   "no catalog generations",
			mutate: func(m *BackupManifest) { m.CatalogGenerations = nil },
			want:   "no catalog generations",
		},
		{name: "file store ID", mutate: func(m *BackupManifest) { m.Files[0].StoreID = "GLOBAL/auth" }, want: "file store ID"},
		{
			name:   "file store membership",
			mutate: func(m *BackupManifest) { m.Files[0].StoreID = "global/other" },
			want:   "unknown store",
		},
		{name: "role", mutate: func(m *BackupManifest) { m.Files[0].Role = "aux" }, want: "role"},
		{
			name:   "source UTF-8",
			mutate: func(m *BackupManifest) { m.Files[0].Source = filepath.Join(t.TempDir(), invalidUTF8) },
			want:   "path",
		},
		{
			name: "source length",
			mutate: func(m *BackupManifest) {
				m.Files[0].Source = filepath.Join(t.TempDir(), strings.Repeat("s", backupMaxPathBytes))
			},
			want: "path",
		},
		{name: "backup UTF-8", mutate: func(m *BackupManifest) { m.Files[0].Backup = invalidUTF8 }, want: "path"},
		{
			name:   "backup length",
			mutate: func(m *BackupManifest) { m.Files[0].Backup = strings.Repeat("b", backupMaxPathBytes+1) },
			want:   "path",
		},
		{
			name:   "missing source identity",
			mutate: func(m *BackupManifest) { m.Files[0].SourceIdentity = "" },
			want:   "source identity",
		},
		{
			name:   "source identity UTF-8",
			mutate: func(m *BackupManifest) { m.Files[0].SourceIdentity = invalidUTF8 },
			want:   "source identity",
		},
		{name: "hash length", mutate: func(m *BackupManifest) { m.Files[0].SHA256 = "00" }, want: "hash"},
		{
			name:   "hash uppercase",
			mutate: func(m *BackupManifest) { m.Files[0].SHA256 = strings.Repeat("A", 64) },
			want:   "hash",
		},
		{
			name:   "hash alphabet",
			mutate: func(m *BackupManifest) { m.Files[0].SHA256 = strings.Repeat("g", 64) },
			want:   "hash",
		},
		{name: "negative size", mutate: func(m *BackupManifest) { m.Files[0].Size = -1 }, want: "size"},
		{
			name:   "file size bound",
			mutate: func(m *BackupManifest) { m.Files[0].Size = backupMaxFileBytes + 1 },
			want:   "size",
		},
		{name: "backup mode", mutate: func(m *BackupManifest) { m.Files[0].Mode = 0o640 }, want: "private"},
		{
			name:   "source mode",
			mutate: func(m *BackupManifest) { m.Files[0].SourceMode = 1 << 16 },
			want:   "source mode",
		},
		{
			name: "duplicate backup path",
			mutate: func(m *BackupManifest) {
				duplicate := m.Files[0]
				duplicate.Role = "database"
				duplicate.Source = filepath.Join(t.TempDir(), "other.db")
				duplicate.SourceIdentity = "other-identity"
				m.Files = append(m.Files, duplicate)
			},
			want: "backup path is duplicated",
		},
		{
			name: "duplicate record",
			mutate: func(m *BackupManifest) {
				duplicate := m.Files[0]
				duplicate.Backup = "stores/global.auth/legacy/other"
				m.Files = append(m.Files, duplicate)
			},
			want: "record is duplicated",
		},
		{
			name: "duplicate generation role",
			mutate: func(m *BackupManifest) {
				m.Files[0].Role = "database"
				duplicate := m.Files[0]
				duplicate.Source = filepath.Join(t.TempDir(), "other.db")
				duplicate.SourceIdentity = "other-identity"
				duplicate.Backup = "stores/global.auth/generation/other"
				m.Files = append(m.Files, duplicate)
			},
			want: "generation role is duplicated",
		},
		{
			name: "store exists without database",
			mutate: func(m *BackupManifest) {
				m.Stores[0].Exists = true
			},
			want: "existence is inconsistent",
		},
		{
			name: "database exists flag false",
			mutate: func(m *BackupManifest) {
				m.Files[0].Role = "database"
			},
			want: "existence is inconsistent",
		},
		{
			name: "sidecar without main",
			mutate: func(m *BackupManifest) {
				m.Files[0].Role = "wal"
			},
			want: "sidecar without its database",
		},
		{
			name: "WAL and journal",
			mutate: func(m *BackupManifest) {
				m.Files[0].Role = "database"
				m.Stores[0].Exists = true
				for _, role := range []string{"wal", "journal"} {
					record := m.Files[0]
					record.Role = role
					record.Source = filepath.Join(t.TempDir(), role)
					record.SourceIdentity = "fixture-" + role
					record.Backup = "stores/global.auth/generation/" + role
					m.Files = append(m.Files, record)
				}
			},
			want: "mixes WAL",
		},
		{
			name:   "catalog generation path",
			mutate: func(m *BackupManifest) { m.CatalogGenerations[0] = "relative.db" },
			want:   "catalog generation",
		},
		{
			name: "duplicate catalog generation",
			mutate: func(m *BackupManifest) {
				m.CatalogGenerations = append(m.CatalogGenerations, m.CatalogGenerations[0])
			},
			want: "catalog generation is duplicated",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifestValidationFixture(t)
			test.mutate(&manifest)
			err := validateBackupManifest(manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want containing %q", err, test.want)
			}
			if payload, marshalErr := marshalBackupManifest(manifest); payload != nil || marshalErr == nil {
				t.Fatalf("invalid manifest marshaled: %q, %v", payload, marshalErr)
			}
		})
	}
}

func TestBackupManifestValidationAppliesCountAndTotalSizeBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*BackupManifest)
		want   string
	}{
		{
			name: "stores",
			mutate: func(m *BackupManifest) {
				m.Stores = make([]BackupStoreManifest, backupMaxEntries+1)
			},
			want: "store count",
		},
		{
			name: "files",
			mutate: func(m *BackupManifest) {
				m.Files = make([]BackupFileManifest, backupMaxFiles+1)
			},
			want: "file count",
		},
		{
			name: "catalog generations",
			mutate: func(m *BackupManifest) {
				m.CatalogGenerations = make([]string, backupMaxEntries+1)
			},
			want: "catalog generation count",
		},
		{
			name: "total size",
			mutate: func(m *BackupManifest) {
				m.Files = nil
				for index := 0; index < 5; index++ {
					record := validManifestValidationFixture(t).Files[0]
					record.Source = filepath.Join(t.TempDir(), "source")
					record.SourceIdentity = "identity-" + string(rune('a'+index))
					record.Backup = "stores/global.auth/legacy/record-" + string(rune('a'+index))
					record.Size = backupMaxFileBytes
					m.Files = append(m.Files, record)
				}
			},
			want: "size",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifestValidationFixture(t)
			test.mutate(&manifest)
			if err := validateBackupManifest(manifest); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("bound validation error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBackupManifestValidationRunsBeforeVerificationFilesystemAccess(t *testing.T) {
	manifest := validManifestValidationFixture(t)
	manifest.Version++
	session := &backupSession{root: filepath.Join(t.TempDir(), "missing"), manifest: manifest}
	if err := session.verify(t.Context()); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("verification did not reject manifest first: %v", err)
	}
}

func validManifestValidationFixture(t *testing.T) BackupManifest {
	t.Helper()
	return BackupManifest{
		Version:   backupManifestVersion,
		CreatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		Outcome:   "snapshot_complete",
		Stores:    []BackupStoreManifest{{StoreID: "global/auth", LegacyRoots: 1}},
		Files: []BackupFileManifest{{
			StoreID:        "global/auth",
			Role:           "legacy",
			Source:         filepath.Join(t.TempDir(), "source.json"),
			SourceIdentity: "fixture-source-identity",
			Backup:         "stores/global.auth/legacy/source.json",
			SHA256:         strings.Repeat("a", 64),
			Size:           1,
			Mode:           0o600,
			SourceMode:     0o640,
		}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
	}
}
