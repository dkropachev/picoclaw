package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestBackupParentPhysicalAliasesRejectsContainmentBothDirections(t *testing.T) {
	t.Run("parent physically contains source", func(t *testing.T) {
		base := t.TempDir()
		parent := filepath.Join(base, "archive")
		sourceDirectory := filepath.Join(parent, "nested")
		if err := os.MkdirAll(sourceDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		spec := storecatalog.Spec{
			ID: "global/test", Path: filepath.Join(sourceDirectory, "store.db"),
		}
		if err := validateBackupParentPhysicalAliases(parent, []storecatalog.Spec{spec}); err == nil ||
			!strings.Contains(err.Error(), "physical ancestor") {
			t.Fatalf("parent/source ancestor containment = %v", err)
		}
	})

	t.Run("legacy source physically contains parent", func(t *testing.T) {
		base := t.TempDir()
		legacy := filepath.Join(base, "legacy")
		parent := filepath.Join(legacy, "nested", "archive")
		storeDirectory := filepath.Join(base, "store")
		for _, directory := range []string{parent, storeDirectory} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		spec := storecatalog.Spec{
			ID: "global/test", Path: filepath.Join(storeDirectory, "store.db"),
			LegacyRoots: []string{legacy},
		}
		if err := validateBackupParentPhysicalAliases(parent, []storecatalog.Spec{spec}); err == nil ||
			!strings.Contains(err.Error(), "physically contains") {
			t.Fatalf("legacy/parent descendant containment = %v", err)
		}
	})

	t.Run("missing parent rejected without source mutation", func(t *testing.T) {
		base := t.TempDir()
		legacy := filepath.Join(base, "legacy")
		parent := filepath.Join(legacy, "not-created")
		storeDirectory := filepath.Join(base, "store")
		for _, directory := range []string{legacy, storeDirectory} {
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		spec := storecatalog.Spec{
			ID: "global/test", Path: filepath.Join(storeDirectory, "store.db"),
			LegacyRoots: []string{legacy},
		}
		if err := validateBackupParentPhysicalAliases(parent, []storecatalog.Spec{spec}); err == nil ||
			!strings.Contains(err.Error(), "physically contains") {
			t.Fatalf("prospective legacy containment = %v", err)
		}
		if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected prospective parent was created: %v", err)
		}
	})

	t.Run("separate trees", func(t *testing.T) {
		base := t.TempDir()
		parent := filepath.Join(base, "archive")
		legacy := filepath.Join(base, "legacy")
		storeDirectory := filepath.Join(base, "store")
		for _, directory := range []string{parent, legacy, storeDirectory} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		spec := storecatalog.Spec{
			ID: "global/test", Path: filepath.Join(storeDirectory, "store.db"),
			LegacyRoots: []string{legacy},
		}
		if err := validateBackupParentPhysicalAliases(parent, []storecatalog.Spec{spec}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBackupParentContainmentHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if parent, err := validateBackupParentWithContext(
		ctx, "", t.TempDir(), nil,
	); parent != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parent validation = %q, %v", parent, err)
	}
	if err := validateBackupParentPhysicalAliasesBoundContext(
		ctx, filepath.Join(t.TempDir(), "missing"), fileidentity.Identity{}, nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled containment validation = %v", err)
	}
}

func TestBackupParentProspectiveDefaultAllowsNormalHomeSiblings(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "legacy")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := storecatalog.Spec{
		ID: "global/test", Path: filepath.Join(home, "store.db"),
		LegacyRoots: []string{legacy},
	}
	parent, err := validateBackupParent("", home, []storecatalog.Spec{spec})
	if err != nil || parent != filepath.Join(home, "backups") {
		t.Fatalf("normal prospective default parent = %q, %v", parent, err)
	}
	if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prospective validation created default parent: %v", err)
	}
}

func TestNearestBackupParentAncestorDetectsNewIntermediate(t *testing.T) {
	base := t.TempDir()
	intermediate := filepath.Join(base, "intermediate")
	parent := filepath.Join(intermediate, "backups")
	beforePath, _, beforeIdentity, err := nearestExistingBackupDirectoryIdentity(parent)
	if err != nil || filepath.Clean(beforePath) != filepath.Clean(base) || !beforeIdentity.Valid() {
		t.Fatalf("initial nearest ancestor = %q, %#v, %v", beforePath, beforeIdentity, err)
	}
	if err := os.Mkdir(intermediate, 0o700); err != nil {
		t.Fatal(err)
	}
	afterPath, _, afterIdentity, err := nearestExistingBackupDirectoryIdentity(parent)
	if err != nil || filepath.Clean(afterPath) != filepath.Clean(intermediate) ||
		!afterIdentity.Valid() || afterIdentity == beforeIdentity {
		t.Fatalf("new nearest ancestor = %q, %#v, %v", afterPath, afterIdentity, err)
	}
}

func TestExclusiveBackupParentCreationOwnsOnlySuccessfulCreate(t *testing.T) {
	base := t.TempDir()
	created := filepath.Join(base, "created")
	owned, err := exclusivelyCreateMissingBackupParent(created, true)
	if err != nil || !owned {
		t.Fatalf("exclusive parent create = %t, %v", owned, err)
	}
	if info, err := os.Lstat(created); err != nil || !info.IsDir() {
		t.Fatalf("created parent = %#v, %v", info, err)
	}

	concurrent := filepath.Join(base, "concurrent")
	if err := os.Mkdir(concurrent, 0o700); err != nil {
		t.Fatal(err)
	}
	owned, err = exclusivelyCreateMissingBackupParent(concurrent, true)
	if err != nil || owned {
		t.Fatalf("lost exclusive-create race = %t, %v", owned, err)
	}
	if info, err := os.Lstat(concurrent); err != nil || !info.IsDir() {
		t.Fatalf("concurrent parent changed = %#v, %v", info, err)
	}
	owned, err = exclusivelyCreateMissingBackupParent(concurrent, false)
	if err != nil || owned {
		t.Fatalf("known existing parent ownership = %t, %v", owned, err)
	}
}

func TestConfiguredBackupParentClassificationPreservesCase(t *testing.T) {
	home := filepath.Join(string(os.PathSeparator), "CaseSensitiveHome")
	defaultParent := filepath.Join(home, "backups")
	if configuredBackupParentIsCustom(defaultParent, defaultParent, home) {
		t.Fatal("exact configured default classified as custom")
	}
	caseDifferent := filepath.Join(home, "Backups")
	if !configuredBackupParentIsCustom(caseDifferent, caseDifferent, home) {
		t.Fatal("case-different configured path classified as default")
	}
}

func TestBackupParentContainmentBounds(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "archive")
	legacy := filepath.Join(base, "legacy")
	for _, directory := range []string{parent, legacy} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parentIdentity, exists, err := fileidentity.Existing(parent)
	if err != nil || !exists {
		t.Fatalf("parent identity = %#v, %t, %v", parentIdentity, exists, err)
	}
	state := &backupParentAncestorState{
		steps: backupMaxEntries, seen: make(map[string]struct{}),
	}
	if err := validateBackupParentOutsideSourceAncestors(legacy, parentIdentity, state); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatalf("ancestor bound = %v", err)
	}

	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	otherIdentity, exists, err := fileidentity.Existing(other)
	if err != nil || !exists {
		t.Fatalf("other identity = %#v, %t, %v", otherIdentity, exists, err)
	}
	if err := validateBackupParentPhysicalAliasesBound(parent, otherIdentity, nil); err == nil ||
		!strings.Contains(err.Error(), "binding changed") {
		t.Fatalf("mismatched pinned parent identity = %v", err)
	}
	if err := validateBackupParentPhysicalAliasesBound(parent, parentIdentity, nil); err != nil {
		t.Fatalf("matching pinned parent identity = %v", err)
	}
	scan := &backupParentLegacyScan{
		forbidden: otherIdentity,
	}
	if err := scanBackupLegacyDirectoriesForParent(legacy, 0, scan); err != nil {
		t.Fatal(err)
	}
	entries := scan.entries
	if err := scanBackupLegacyDirectoriesForParent(legacy, 0, scan); err != nil {
		t.Fatal(err)
	}
	if scan.entries <= entries {
		t.Fatalf("repeated directory view was not independently scanned: %d <= %d", scan.entries, entries)
	}
	if err := scanBackupLegacyDirectoriesForParent(legacy, backupMaxDepth+1, scan); err == nil ||
		!strings.Contains(err.Error(), "limit") {
		t.Fatalf("legacy depth bound = %v", err)
	}
}
