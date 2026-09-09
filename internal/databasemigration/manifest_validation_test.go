package databasemigration

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestBackupManifestValidationAcceptsCanonicalInventories(t *testing.T) {
	if err := validateBackupManifest(validManifestValidationFixture(t)); err != nil {
		t.Fatal(err)
	}
	for _, roles := range [][]string{
		{"database"},
		{"database", "wal"},
		{"database", "wal", "shm"},
		{"database", "journal"},
	} {
		manifest := validGenerationManifestFixture(t, roles...)
		if err := validateBackupManifest(manifest); err != nil {
			t.Fatalf("valid roles %q: %v", roles, err)
		}
	}
}

func TestBackupManifestValidationRejectsMalformedMetadata(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	tests := []struct {
		name   string
		mutate func(*BackupManifest)
	}{
		{name: "version", mutate: func(m *BackupManifest) { m.Version++ }},
		{name: "capture mode", mutate: func(m *BackupManifest) { m.CaptureMode = "online" }},
		{name: "zero timestamp", mutate: func(m *BackupManifest) { m.CreatedAt = time.Time{} }},
		{name: "non UTC timestamp", mutate: func(m *BackupManifest) {
			m.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("other", 3600))
		}},
		{name: "nil stores", mutate: func(m *BackupManifest) { m.Stores = nil }},
		{name: "nil files", mutate: func(m *BackupManifest) { m.Files = nil }},
		{name: "nil catalog", mutate: func(m *BackupManifest) { m.CatalogGenerations = nil }},
		{name: "store ID", mutate: func(m *BackupManifest) { m.Stores[0].StoreID = "../auth" }},
		{name: "store path", mutate: func(m *BackupManifest) { m.Stores[0].Path = "relative.db" }},
		{name: "nil roots", mutate: func(m *BackupManifest) { m.Stores[0].LegacyRoots = nil }},
		{name: "nil root kinds", mutate: func(m *BackupManifest) { m.Stores[0].LegacyRootKinds = nil }},
		{name: "root kind count", mutate: func(m *BackupManifest) {
			m.Stores[0].LegacyRootKinds = append(m.Stores[0].LegacyRootKinds, "file")
		}},
		{name: "root kind", mutate: func(m *BackupManifest) { m.Stores[0].LegacyRootKinds[0] = "link" }},
		{name: "root path", mutate: func(m *BackupManifest) { m.Stores[0].LegacyRoots[0] = "relative" }},
		{name: "duplicate root", mutate: func(m *BackupManifest) {
			m.Stores[0].LegacyRoots = append(m.Stores[0].LegacyRoots, m.Stores[0].LegacyRoots[0])
			m.Stores[0].LegacyRootKinds = append(m.Stores[0].LegacyRootKinds, "file")
		}},
		{name: "duplicate store", mutate: func(m *BackupManifest) { m.Stores = append(m.Stores, m.Stores[0]) }},
		{name: "store order", mutate: func(m *BackupManifest) {
			other := m.Stores[0]
			other.StoreID = "global/agents"
			other.Path = filepath.Join(filepath.Dir(other.Path), "agents.db")
			m.Stores = append(m.Stores, other)
		}},
		{name: "overlapping store generations", mutate: func(m *BackupManifest) {
			other := m.Stores[0]
			other.StoreID = "global/models"
			m.Stores = append(m.Stores, other)
		}},
		{name: "file store ID", mutate: func(m *BackupManifest) { m.Files[0].StoreID = "GLOBAL/auth" }},
		{name: "unknown store", mutate: func(m *BackupManifest) { m.Files[0].StoreID = "global/other" }},
		{name: "role", mutate: func(m *BackupManifest) { m.Files[0].Role = "aux" }},
		{name: "legacy root negative", mutate: func(m *BackupManifest) { m.Files[0].LegacyRoot = -1 }},
		{name: "legacy root high", mutate: func(m *BackupManifest) { m.Files[0].LegacyRoot = 1 }},
		{
			name:   "source UTF-8",
			mutate: func(m *BackupManifest) { m.Files[0].Source = filepath.Join(t.TempDir(), invalidUTF8) },
		},
		{name: "source length", mutate: func(m *BackupManifest) {
			m.Files[0].Source = filepath.Join(t.TempDir(), strings.Repeat("s", backupMaxPathBytes))
		}},
		{name: "backup UTF-8", mutate: func(m *BackupManifest) { m.Files[0].Backup = invalidUTF8 }},
		{
			name:   "backup length",
			mutate: func(m *BackupManifest) { m.Files[0].Backup = strings.Repeat("b", backupMaxPathBytes+1) },
		},
		{name: "source identity empty", mutate: func(m *BackupManifest) { m.Files[0].SourceIdentity = "" }},
		{name: "source identity UTF-8", mutate: func(m *BackupManifest) { m.Files[0].SourceIdentity = invalidUTF8 }},
		{name: "source identity conflicts for one path", mutate: func(m *BackupManifest) {
			duplicate := m.Files[0]
			duplicate.SourceIdentity += "-other"
			m.Files = append(m.Files, duplicate)
		}},
		{name: "hash short", mutate: func(m *BackupManifest) { m.Files[0].SHA256 = "00" }},
		{name: "hash uppercase", mutate: func(m *BackupManifest) { m.Files[0].SHA256 = strings.Repeat("A", 64) }},
		{name: "hash alphabet", mutate: func(m *BackupManifest) { m.Files[0].SHA256 = strings.Repeat("g", 64) }},
		{name: "negative size", mutate: func(m *BackupManifest) { m.Files[0].Size = -1 }},
		{name: "large size", mutate: func(m *BackupManifest) { m.Files[0].Size = backupMaxFileBytes + 1 }},
		{name: "backup mode", mutate: func(m *BackupManifest) { m.Files[0].Mode = 0o640 }},
		{name: "source mode", mutate: func(m *BackupManifest) { m.Files[0].SourceMode = 1 << 16 }},
		{
			name:   "legacy missing with file",
			mutate: func(m *BackupManifest) { m.Stores[0].LegacyRootKinds[0] = "missing" },
		},
		{
			name:   "legacy directory root record",
			mutate: func(m *BackupManifest) { m.Stores[0].LegacyRootKinds[0] = "directory" },
		},
		{name: "legacy provenance", mutate: func(m *BackupManifest) { m.Files[0].Backup += ".other" }},
		{name: "source alias", mutate: func(m *BackupManifest) {
			duplicate := m.Files[0]
			duplicate.LegacyRoot = 1
			duplicate.Source = filepath.Join(filepath.Dir(duplicate.Source), "other.json")
			duplicate.Backup = filepath.ToSlash(filepath.Join(
				"stores", backupStoreDirectory(duplicate.StoreID), "legacy", "root-000001", "root",
			))
			m.Stores[0].LegacyRoots = append(m.Stores[0].LegacyRoots, duplicate.Source)
			m.Stores[0].LegacyRootKinds = append(m.Stores[0].LegacyRootKinds, "file")
			m.Files = append(m.Files, duplicate)
		}},
		{name: "duplicate file", mutate: func(m *BackupManifest) { m.Files = append(m.Files, m.Files[0]) }},
		{name: "catalog path", mutate: func(m *BackupManifest) { m.CatalogGenerations[0] = "relative" }},
		{
			name:   "catalog duplicate",
			mutate: func(m *BackupManifest) { m.CatalogGenerations = append(m.CatalogGenerations, m.CatalogGenerations[0]) },
		},
		{name: "catalog order", mutate: func(m *BackupManifest) {
			m.CatalogGenerations[0], m.CatalogGenerations[1] = m.CatalogGenerations[1], m.CatalogGenerations[0]
		}},
		{name: "catalog omission", mutate: func(m *BackupManifest) { m.CatalogGenerations = m.CatalogGenerations[1:] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifestValidationFixture(t)
			test.mutate(&manifest)
			if err := validateBackupManifest(manifest); err == nil {
				t.Fatal("invalid manifest passed validation")
			}
			if payload, err := marshalBackupManifest(manifest); payload != nil || err == nil {
				t.Fatalf("invalid manifest marshaled: %q, %v", payload, err)
			}
		})
	}
}

func TestBackupManifestValidationRejectsOverlappingSourceNamespaces(t *testing.T) {
	valid := validManifestValidationFixture(t)
	root := filepath.Dir(valid.Stores[0].Path)
	appendEmptyManifestStore(
		&valid, "global/models", filepath.Join(root, "models.db"), nil, nil,
	)
	if err := validateBackupManifest(valid); err != nil {
		t.Fatalf("disjoint source namespaces: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*BackupManifest)
	}{
		{name: "global legacy root duplicate", mutate: func(m *BackupManifest) {
			m.Stores[1].LegacyRoots = []string{m.Stores[0].LegacyRoots[0]}
			m.Stores[1].LegacyRootKinds = []string{"missing"}
		}},
		{name: "legacy root equals unselected catalog generation", mutate: func(m *BackupManifest) {
			m.CatalogGenerations = append(m.CatalogGenerations, m.Stores[0].LegacyRoots[0])
			sortManifestCatalog(m)
		}},
		{name: "unselected generation namespace containment", mutate: func(m *BackupManifest) {
			m.CatalogGenerations = append(
				m.CatalogGenerations, filepath.Join(m.Stores[0].Path, "nested.db"),
			)
			sortManifestCatalog(m)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid
			manifest.Stores = append([]BackupStoreManifest(nil), valid.Stores...)
			manifest.CatalogGenerations = append([]string(nil), valid.CatalogGenerations...)
			test.mutate(&manifest)
			if err := validateBackupManifest(manifest); err == nil ||
				!strings.Contains(err.Error(), "namespaces overlap") {
				t.Fatalf("overlapping source namespaces error = %v", err)
			}
		})
	}
}

func TestBackupManifestValidationAllowsCanonicalLegacyContainment(t *testing.T) {
	t.Run("checkpoint generation inside legacy directory", func(t *testing.T) {
		manifest := validGenerationManifestFixture(t, "database")
		checkpointRoot := filepath.Join(filepath.Dir(manifest.Stores[0].Path), "active")
		storePath := filepath.Join(checkpointRoot, "checkpoints.db")
		manifest.Stores[0].Path = storePath
		manifest.Stores[0].LegacyRoots = []string{checkpointRoot}
		manifest.Stores[0].LegacyRootKinds = []string{"directory"}
		manifest.Files[0].Source = storePath
		manifest.CatalogGenerations = generationPaths(storePath)
		sortManifestCatalog(&manifest)
		if err := validateBackupManifest(manifest); err != nil {
			t.Fatalf("canonical checkpoint containment: %v", err)
		}
	})

	t.Run("ancestor legacy directories", func(t *testing.T) {
		manifest := validGenerationManifestFixture(t)
		root := filepath.Dir(manifest.Stores[0].Path)
		manifest.Stores[0].LegacyRoots = []string{filepath.Join(root, "legacy")}
		manifest.Stores[0].LegacyRootKinds = []string{"directory"}
		appendEmptyManifestStore(
			&manifest,
			"global/models",
			filepath.Join(root, "models.db"),
			[]string{filepath.Join(root, "legacy", "nested")},
			[]string{"directory"},
		)
		if err := validateBackupManifest(manifest); err != nil {
			t.Fatalf("canonical legacy containment: %v", err)
		}
	})
}

func TestBackupManifestValidationRejectsGenerationIncoherence(t *testing.T) {
	for _, roles := range [][]string{
		{"database", "shm"},
		{"database", "wal", "journal"},
		{"wal"},
	} {
		manifest := validGenerationManifestFixture(t, roles...)
		if err := validateBackupManifest(manifest); err == nil {
			t.Fatalf("incoherent roles %q passed", roles)
		}
	}
	manifest := validGenerationManifestFixture(t, "database")
	manifest.Stores[0].Exists = false
	if err := validateBackupManifest(manifest); err == nil {
		t.Fatal("inconsistent store existence passed")
	}
}

func TestBackupManifestValidationAppliesCountAndStatusBounds(t *testing.T) {
	manifest := validManifestValidationFixture(t)
	manifest.Stores = make([]BackupStoreManifest, backupMaxEntries+1)
	if err := validateBackupManifest(manifest); err == nil {
		t.Fatal("store count bound passed")
	}
	manifest = validManifestValidationFixture(t)
	manifest.Files = make([]BackupFileManifest, backupMaxFiles+1)
	if err := validateBackupManifest(manifest); err == nil {
		t.Fatal("file count bound passed")
	}
	manifest = validManifestValidationFixture(t)
	manifest.CatalogGenerations = make([]string, backupMaxEntries+1)
	if err := validateBackupManifest(manifest); err == nil {
		t.Fatal("catalog count bound passed")
	}
	manifest = validManifestValidationFixture(t)
	manifest.Stores = make([]BackupStoreManifest, 0, 5)
	manifest.Files = make([]BackupFileManifest, 0, 5)
	manifest.CatalogGenerations = make([]string, 0, 20)
	root := t.TempDir()
	for index := 0; index < 5; index++ {
		id := database.StoreID(fmt.Sprintf("global/store-%d", index))
		path := filepath.Join(root, fmt.Sprintf("store-%d.db", index))
		manifest.Stores = append(manifest.Stores, BackupStoreManifest{
			StoreID: id.String(), Path: path, Exists: true,
			LegacyRoots: []string{}, LegacyRootKinds: []string{},
		})
		manifest.Files = append(manifest.Files, BackupFileManifest{
			StoreID: id.String(), Role: "database", Source: path,
			SourceIdentity: fmt.Sprintf("fixture-%d", index),
			Backup: filepath.ToSlash(filepath.Join(
				"stores", backupStoreDirectory(id.String()), "generation", "database",
			)),
			SHA256: strings.Repeat("a", 64), Size: backupMaxFileBytes,
			Mode: 0o600, SourceMode: 0o600,
		})
		manifest.CatalogGenerations = append(manifest.CatalogGenerations, generationPaths(path)...)
	}
	sortBackupManifestFiles(manifest.Files)
	sort.Slice(manifest.CatalogGenerations, func(i, j int) bool {
		return backupPathKey(manifest.CatalogGenerations[i]) <
			backupPathKey(manifest.CatalogGenerations[j])
	})
	if err := validateBackupManifest(manifest); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("aggregate manifest size bound = %v", err)
	}
}

func validManifestValidationFixture(t *testing.T) BackupManifest {
	t.Helper()
	root := t.TempDir()
	storePath := filepath.Join(root, "auth.db")
	legacyRoot := filepath.Join(root, "source.json")
	backup := filepath.ToSlash(filepath.Join(
		"stores", backupStoreDirectory("global/auth"), "legacy", "root-000000", "root",
	))
	catalog := generationPaths(storePath)
	sort.Slice(catalog, func(i, j int) bool {
		left, right := backupPathKey(catalog[i]), backupPathKey(catalog[j])
		if left != right {
			return left < right
		}
		return catalog[i] < catalog[j]
	})
	return BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		CaptureMode: "offline_quiescent_raw",
		Stores: []BackupStoreManifest{{
			StoreID: "global/auth", Path: storePath,
			LegacyRoots: []string{legacyRoot}, LegacyRootKinds: []string{"file"},
		}},
		Files: []BackupFileManifest{{
			StoreID: "global/auth", Role: "legacy", Source: legacyRoot,
			SourceIdentity: "fixture-source-identity", Backup: backup,
			SHA256: strings.Repeat("a", 64), Size: 1, Mode: 0o600, SourceMode: 0o640,
		}},
		CatalogGenerations: catalog,
	}
}

func validGenerationManifestFixture(t *testing.T, roles ...string) BackupManifest {
	t.Helper()
	manifest := validManifestValidationFixture(t)
	manifest.Stores[0].LegacyRoots = []string{}
	manifest.Stores[0].LegacyRootKinds = []string{}
	manifest.Files = make([]BackupFileManifest, 0, len(roles))
	paths := generationPaths(manifest.Stores[0].Path)
	for _, role := range roles {
		roleIndex := backupRoleOrder(role)
		manifest.Files = append(manifest.Files, BackupFileManifest{
			StoreID: "global/auth", Role: role, Source: paths[roleIndex],
			SourceIdentity: "fixture-" + role,
			Backup: filepath.ToSlash(filepath.Join(
				"stores", backupStoreDirectory("global/auth"), "generation", role,
			)),
			SHA256: strings.Repeat("a", 64), Size: 1, Mode: 0o600, SourceMode: 0o600,
		})
	}
	sortBackupManifestFiles(manifest.Files)
	manifest.Stores[0].Exists = len(roles) > 0 && roles[0] == "database"
	return manifest
}

func appendEmptyManifestStore(
	manifest *BackupManifest,
	storeID string,
	path string,
	legacyRoots []string,
	legacyRootKinds []string,
) {
	if legacyRoots == nil {
		legacyRoots = []string{}
	}
	if legacyRootKinds == nil {
		legacyRootKinds = []string{}
	}
	manifest.Stores = append(manifest.Stores, BackupStoreManifest{
		StoreID: storeID, Path: path, LegacyRoots: legacyRoots, LegacyRootKinds: legacyRootKinds,
	})
	manifest.CatalogGenerations = append(manifest.CatalogGenerations, generationPaths(path)...)
	sortManifestCatalog(manifest)
}

func sortManifestCatalog(manifest *BackupManifest) {
	sort.Slice(manifest.CatalogGenerations, func(i, j int) bool {
		left, right := backupPathKey(manifest.CatalogGenerations[i]),
			backupPathKey(manifest.CatalogGenerations[j])
		if left != right {
			return left < right
		}
		return manifest.CatalogGenerations[i] < manifest.CatalogGenerations[j]
	})
}
