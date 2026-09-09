//nolint:govet // Fault-path assertions intentionally use narrow error scopes.
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

func TestBackupParentProspectiveProjectionUsesAncestorIdentity(t *testing.T) {
	base := t.TempDir()
	anchorIdentity := parentTreeIdentity(t, base)
	alias := filepath.Join(base, "alias-view")
	physical := filepath.Join(base, "physical-view")
	parent := filepath.Join(alias, "backups")
	lookupCalls := make(map[string]int)
	lookup := func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		path = filepath.Clean(path)
		lookupCalls[path]++
		if path == physical {
			return anchorIdentity, fileidentity.ObjectTypeDirectory, true, nil
		}
		return fileidentity.Identity{}, 0, false, nil
	}

	state, err := newBackupParentProjectionState(
		t.Context(), alias, parent, anchorIdentity, lookup,
	)
	if err != nil {
		t.Fatal(err)
	}
	aliasedGeneration := filepath.Join(physical, "backups", "store.db")
	if err := validateProspectiveBackupParentOutsideSource(aliasedGeneration, state); err == nil ||
		!strings.Contains(err.Error(), "aliases a catalog source") {
		t.Fatalf("projected generation overlap = %v", err)
	}

	state, err = newBackupParentProjectionState(
		t.Context(), alias, parent, anchorIdentity, lookup,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, generation := range generationPaths(filepath.Join(physical, "store.db")) {
		if err := validateProspectiveBackupParentOutsideSource(generation, state); err != nil {
			t.Fatalf("normal projected sibling %q = %v", generation, err)
		}
	}
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(physical, "backups2", "store.db"), state,
	); err != nil {
		t.Fatalf("component-prefix sibling = %v", err)
	}
	if lookupCalls[physical] != 2 {
		t.Fatalf("physical ancestor lookups = %d, want one per projection state", lookupCalls[physical])
	}
}

func TestBackupParentProspectiveProjectionChecksEarlierSources(t *testing.T) {
	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	alias := filepath.Join(base, "alias")
	physical := filepath.Join(base, "physical")
	parent := filepath.Join(alias, "backups")
	lateAnchor := filepath.Join(physical, "generation")
	state, err := newBackupParentProjectionState(
		t.Context(), alias, parent, identity,
		func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			if filepath.Clean(path) == lateAnchor {
				return identity, fileidentity.ObjectTypeDirectory, true, nil
			}
			return fileidentity.Identity{}, 0, false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(lateAnchor, state); err != nil {
		t.Fatalf("source before projected mapping = %v", err)
	}
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(lateAnchor, "store.db"), state,
	); err == nil || !strings.Contains(err.Error(), "aliases a catalog source") {
		t.Fatalf("late projection against earlier source = %v", err)
	}
}

func TestBackupParentProspectiveProjectionIsBoundedAndCancelable(t *testing.T) {
	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	parent := filepath.Join(base, "backups")
	state, err := newBackupParentProjectionState(
		t.Context(), base, parent, identity,
		func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state.steps = backupMaxEntries
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(base, "store.db"), state,
	); err == nil || !strings.Contains(err.Error(), "projection limit") {
		t.Fatalf("prospective projection bound = %v", err)
	}
	state, err = newBackupParentProjectionState(
		t.Context(), base, parent, identity,
		func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	state.sourceChecks = backupMaxParentProjectionSources
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(base, "store.db"), state,
	); err == nil || !strings.Contains(err.Error(), "source limit") {
		t.Fatalf("prospective projection source bound = %v", err)
	}
	state.sourceChecks = 0
	state.comparisons = backupMaxParentProjectionSources
	if err := compareProspectiveBackupProjection(
		filepath.Join(base, "backups"), filepath.Join(base, "store.db"), state,
	); err == nil || !strings.Contains(err.Error(), "comparison limit") {
		t.Fatalf("prospective projection comparison bound = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	state, err = newBackupParentProjectionState(
		ctx, base, parent, identity,
		func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			cancel()
			return fileidentity.Identity{}, 0, false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(base, "store.db"), state,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("prospective projection cancellation = %v", err)
	}
}

func TestBackupParentProspectiveProjectionRejectsObservationDrift(t *testing.T) {
	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	parent := filepath.Join(base, "backups")
	changed := false
	firstAncestor := filepath.Dir(filepath.Join(base, "nested", "store.db"))
	state, err := newBackupParentProjectionState(
		t.Context(), base, parent, identity,
		func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			if changed && filepath.Clean(path) == firstAncestor {
				return identity, fileidentity.ObjectTypeDirectory, true, nil
			}
			return fileidentity.Identity{}, 0, false, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(base, "nested", "store.db"), state,
	); err != nil {
		t.Fatal(err)
	}
	changed = true
	if err := validateBackupParentProjectionStable(state); err == nil ||
		!strings.Contains(err.Error(), "changed during validation") {
		t.Fatalf("prospective projection observation drift = %v", err)
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
	if err != nil || !owned.Valid() {
		t.Fatalf("exclusive parent create = %#v, %v", owned, err)
	}
	if info, err := os.Lstat(created); err != nil || !info.IsDir() {
		t.Fatalf("created parent = %#v, %v", info, err)
	}
	if current := parentTreeIdentity(t, created); current != owned {
		t.Fatalf("created parent identity = %#v, want captured %#v", current, owned)
	}

	concurrent := filepath.Join(base, "concurrent")
	if err := os.Mkdir(concurrent, 0o700); err != nil {
		t.Fatal(err)
	}
	owned, err = exclusivelyCreateMissingBackupParent(concurrent, true)
	if err != nil || owned.Valid() {
		t.Fatalf("lost exclusive-create race = %#v, %v", owned, err)
	}
	if info, err := os.Lstat(concurrent); err != nil || !info.IsDir() {
		t.Fatalf("concurrent parent changed = %#v, %v", info, err)
	}
	owned, err = exclusivelyCreateMissingBackupParent(concurrent, false)
	if err != nil || owned.Valid() {
		t.Fatalf("known existing parent ownership = %#v, %v", owned, err)
	}
}

func TestExclusiveBackupParentCreationOrdersDurability(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "created")
	ops := defaultBackupParentCreationOps()
	ops.random = func() (string, error) { return ".database-backup-parent-test", nil }
	secure := ops.secure
	syncChild := ops.syncChild
	publish := ops.publish
	syncParent := ops.sync
	stage := 0
	ops.secure = func(file *os.File, identity fileidentity.Identity) error {
		if err := secure(file, identity); err != nil {
			return err
		}
		stage = 1
		return nil
	}
	ops.syncChild = func(file *os.File, identity fileidentity.Identity) error {
		if stage != 1 {
			return errors.New("temporary parent synced before privacy validation")
		}
		if err := syncChild(file, identity); err != nil {
			return err
		}
		stage = 2
		return nil
	}
	ops.publish = func(root *os.Root, source, target string, opened *os.File) (*os.File, error) {
		if stage != 2 {
			return nil, errors.New("temporary parent published before directory sync")
		}
		exact, err := publish(root, source, target, opened)
		if err == nil {
			stage = 3
		}
		return exact, err
	}
	ops.sync = func(root *os.Root) error {
		if stage != 3 {
			return errors.New("parent synced before no-replace publication")
		}
		stage = 4
		return syncParent(root)
	}
	identity, err := exclusivelyCreateMissingBackupParentWithOps(path, true, ops)
	if err != nil || !identity.Valid() || stage != 4 {
		t.Fatalf("durable parent publication = %#v, stage %d, %v", identity, stage, err)
	}
}

func TestExclusiveBackupParentCreationCleansCapturedFailures(t *testing.T) {
	canary := errors.New("parent creation canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupParentCreationOps)
	}{
		{name: "container after capture", mutate: func(ops *backupParentCreationOps) {
			container, calls := ops.container, 0
			ops.container = func(root *os.Root) error {
				calls++
				if calls == 2 {
					return canary
				}
				return container(root)
			}
		}},
		{name: "child sync", mutate: func(ops *backupParentCreationOps) {
			ops.syncChild = func(*os.File, fileidentity.Identity) error { return canary }
		}},
		{name: "publication after rename", mutate: func(ops *backupParentCreationOps) {
			publish := ops.publish
			ops.publish = func(root *os.Root, source, target string, opened *os.File) (*os.File, error) {
				exact, err := publish(root, source, target, opened)
				if err != nil {
					return exact, err
				}
				return exact, canary
			}
		}},
		{name: "parent sync", mutate: func(ops *backupParentCreationOps) {
			ops.sync = func(*os.Root) error { return canary }
		}},
		{name: "container after publication", mutate: func(ops *backupParentCreationOps) {
			container, calls := ops.container, 0
			ops.container = func(root *os.Root) error {
				calls++
				if calls == 3 {
					return canary
				}
				return container(root)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "created")
			temporary := filepath.Join(base, ".database-backup-parent-test")
			ops := defaultBackupParentCreationOps()
			ops.random = func() (string, error) { return filepath.Base(temporary), nil }
			test.mutate(&ops)
			identity, err := exclusivelyCreateMissingBackupParentWithOps(path, true, ops)
			if !errors.Is(err, canary) || identity.Valid() {
				t.Fatalf("failed parent publication = %#v, %v", identity, err)
			}
			for _, candidate := range []string{temporary, path} {
				if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("captured failure left %q: %v", candidate, err)
				}
			}
		})
	}
}

func TestExclusiveBackupParentCreationLeavesUnownedNames(t *testing.T) {
	t.Run("lost publication race", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "created")
		temporary := filepath.Join(base, ".database-backup-parent-test")
		ops := defaultBackupParentCreationOps()
		ops.random = func() (string, error) { return filepath.Base(temporary), nil }
		publish := ops.publish
		ops.publish = func(root *os.Root, source, target string, opened *os.File) (*os.File, error) {
			if err := root.Mkdir(target, 0o700); err != nil {
				return nil, err
			}
			if err := root.WriteFile(filepath.Join(target, "foreign"), []byte("retain"), 0o600); err != nil {
				return nil, err
			}
			return publish(root, source, target, opened)
		}
		identity, err := exclusivelyCreateMissingBackupParentWithOps(path, true, ops)
		if err != nil || identity.Valid() {
			t.Fatalf("lost parent publication race = %#v, %v", identity, err)
		}
		if payload, err := os.ReadFile(filepath.Join(path, "foreign")); err != nil || string(payload) != "retain" {
			t.Fatalf("unowned winner changed = %q, %v", payload, err)
		}
		if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("captured temporary parent remains: %v", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*backupParentCreationOps, error)
	}{
		{"identity capture failure", func(ops *backupParentCreationOps, canary error) {
			ops.opened = func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
				return fileidentity.Identity{}, 0, canary
			}
		}},
		{"security proof failure", func(ops *backupParentCreationOps, canary error) {
			ops.secure = func(*os.File, fileidentity.Identity) error { return canary }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "created")
			temporary := filepath.Join(base, ".database-backup-parent-test")
			canary := errors.New("capture canary")
			ops := defaultBackupParentCreationOps()
			ops.random = func() (string, error) { return filepath.Base(temporary), nil }
			test.mutate(&ops, canary)
			identity, err := exclusivelyCreateMissingBackupParentWithOps(path, true, ops)
			if !errors.Is(err, canary) || identity.Valid() {
				t.Fatalf("uncaptured parent creation = %#v, %v", identity, err)
			}
			if info, err := os.Lstat(temporary); err != nil || !info.IsDir() {
				t.Fatalf("uncaptured temporary parent was removed = %#v, %v", info, err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncaptured final parent appeared: %v", err)
			}
		})
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
