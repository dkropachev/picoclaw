//go:build linux

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
	"golang.org/x/sys/unix"
)

func TestBackupParentCreationInjectedFaultCoverage(t *testing.T) {
	canary := errors.New("parent creation coverage canary")
	tests := []struct {
		name   string
		want   string
		mutate func(*backupParentCreationOps)
	}{
		{name: "initial container", want: "creation container", mutate: func(ops *backupParentCreationOps) {
			ops.container = func(*os.Root) error { return canary }
		}},
		{name: "random", want: canary.Error(), mutate: func(ops *backupParentCreationOps) {
			ops.random = func() (string, error) { return "", canary }
		}},
		{name: "invalid temporary leaf", want: "temporary parent name is invalid", mutate: func(ops *backupParentCreationOps) {
			ops.random = func() (string, error) { return "created", nil }
		}},
		{name: "name exhaustion", want: "temporary parent name is unavailable", mutate: func(ops *backupParentCreationOps) {
			ops.mkdir = func(*os.Root, string, os.FileMode) error { return os.ErrExist }
		}},
		{name: "mkdir", want: canary.Error(), mutate: func(ops *backupParentCreationOps) {
			ops.mkdir = func(*os.Root, string, os.FileMode) error { return canary }
		}},
		{name: "open", want: "open temporary", mutate: func(ops *backupParentCreationOps) {
			ops.open = func(*os.Root, string) (*os.File, error) { return nil, canary }
		}},
		{name: "validate", want: "bind temporary", mutate: func(ops *backupParentCreationOps) {
			ops.validate = func(*os.Root, string, *os.File, fileidentity.Identity, fileidentity.ObjectType) error {
				return canary
			}
		}},
		{name: "initial fence", want: "fence temporary", mutate: func(ops *backupParentCreationOps) {
			ops.validate = func(_ *os.Root, _ string, opened *os.File, _ fileidentity.Identity, _ fileidentity.ObjectType) error {
				return opened.Close()
			}
		}},
		{name: "secured fence", want: "revalidate secured", mutate: func(ops *backupParentCreationOps) {
			secure := ops.secure
			ops.secure = func(opened *os.File, identity fileidentity.Identity) error {
				if err := secure(opened, identity); err != nil {
					return err
				}
				return opened.Close()
			}
		}},
		{name: "synced fence", want: "revalidate synced", mutate: func(ops *backupParentCreationOps) {
			syncChild := ops.syncChild
			ops.syncChild = func(opened *os.File, identity fileidentity.Identity) error {
				if err := syncChild(opened, identity); err != nil {
					return err
				}
				return opened.Close()
			}
		}},
		{name: "exact handle", want: "exact handle changed", mutate: func(ops *backupParentCreationOps) {
			publish := ops.publish
			openedIdentity := ops.opened
			calls := 0
			ops.opened = func(file *os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
				calls++
				if calls == 2 {
					return fileidentity.Identity{}, fileidentity.ObjectTypeDirectory, canary
				}
				return openedIdentity(file)
			}
			ops.publish = func(root *os.Root, source, target string, opened *os.File) (*os.File, error) {
				if _, err := publish(root, source, target, opened); err != nil {
					return nil, err
				}
				return root.Open(target)
			}
		}},
		{name: "missing temporary proof", want: "temporary parent name remains", mutate: func(ops *backupParentCreationOps) {
			ops.missing = func(*os.Root, string) error { return canary }
		}},
		{name: "published binding", want: "bind published", mutate: func(ops *backupParentCreationOps) {
			publish := ops.publish
			ops.publish = func(root *os.Root, source, target string, opened *os.File) (*os.File, error) {
				exact, err := publish(root, source, target, opened)
				if err == nil {
					err = opened.Close()
				}
				return exact, err
			}
		}},
		{name: "published revalidation", want: "revalidate published", mutate: func(ops *backupParentCreationOps) {
			open := ops.open
			syncParent := ops.sync
			var opened *os.File
			ops.open = func(root *os.Root, leaf string) (*os.File, error) {
				var err error
				opened, err = open(root, leaf)
				return opened, err
			}
			ops.sync = func(root *os.Root) error {
				if err := syncParent(root); err != nil {
					return err
				}
				return opened.Close()
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "created")
			ops := defaultBackupParentCreationOps()
			ops.random = func() (string, error) { return ".database-backup-parent-coverage", nil }
			test.mutate(&ops)
			owned, err := exclusivelyCreateMissingBackupParentWithOps(path, true, ops)
			if err == nil || !strings.Contains(err.Error(), test.want) || owned.Valid() {
				t.Fatalf("creation fault = %#v, %v; want %q", owned, err, test.want)
			}
		})
	}

	owned, err := exclusivelyCreateMissingBackupParent(filepath.Join(t.TempDir(), "missing", "created"), true)
	if err == nil || owned.Valid() {
		t.Fatalf("missing creation parent = %#v, %v", owned, err)
	}
}

func TestBackupParentRemainingContextCoverage(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	source := filepath.Join(base, "source")
	for _, path := range []string{parent, source} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec := storecatalog.Spec{
		ID: "global/context", Path: filepath.Join(source, "store.db"),
		LegacyRoots: []string{filepath.Join(base, "legacy")},
	}
	if _, err := validateBackupParentWithContext(
		&cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 7},
		parent, base, []storecatalog.Spec{spec},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("legacy lexical cancellation = %v", err)
	}
	if err := validateBackupParentPhysicalAliasesBoundContext(
		&cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 2},
		parent, fileidentity.Identity{}, []storecatalog.Spec{spec},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("physical-check cancellation = %v", err)
	}
	if err := validateBackupParentPhysicalAliasesBoundContext(
		&cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 2},
		filepath.Join(base, "missing"), fileidentity.Identity{}, []storecatalog.Spec{spec},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("prospective-ancestor cancellation = %v", err)
	}
	if err := validateBackupParentPhysicalAliasesBoundContext(
		t.Context(), filepath.Join(base, "missing"), fileidentity.Identity{},
		[]storecatalog.Spec{{ID: "global/invalid", Path: "relative"}},
	); err == nil || !strings.Contains(err.Error(), "physically overlaps") {
		t.Fatalf("invalid projected source = %v", err)
	}
	if _, _, _, err := nearestExistingBackupDirectoryIdentityContext(nil, base); err != nil {
		t.Fatalf("nil-context nearest ancestor = %v", err)
	}
	if _, _, _, err := nearestExistingBackupDirectoryIdentityContext(
		canceledParentTreeContext(), base,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled nearest ancestor = %v", err)
	}
}

func TestBackupParentCreationCleanupCoverage(t *testing.T) {
	base := t.TempDir()
	expected := parentTreeIdentity(t, base)
	if err := removeCapturedEmptyBackupParent(base, fileidentity.Identity{}); err == nil {
		t.Fatal("cleanup accepted an invalid identity")
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(base, link); err != nil {
		t.Fatal(err)
	}
	if err := removeCapturedEmptyBackupParent(link, expected); err != nil {
		t.Fatalf("cleanup of a substituted symlink = %v", err)
	}

	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeCapturedEmptyBackupParent(filepath.Join(regular, "child"), expected); err == nil {
		t.Fatal("cleanup inspection error was ignored")
	}
}

func TestBackupParentProjectionFaultCoverage(t *testing.T) {
	base := t.TempDir()
	anchor := parentTreeIdentity(t, base)
	lookup := func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		return fileidentity.Identity{}, 0, false, nil
	}
	if _, err := newBackupParentProjectionState(nil, base, filepath.Join(base, "backups"), anchor, lookup); err != nil {
		t.Fatalf("nil-context projection = %v", err)
	}
	if _, err := newBackupParentProjectionState(
		canceledParentTreeContext(), base, filepath.Join(base, "backups"), anchor, lookup,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled projection = %v", err)
	}
	for _, test := range []struct{ name, anchorPath, parent, want string }{
		{name: "invalid input", anchorPath: "relative", parent: filepath.Join(base, "backups"), want: "state is invalid"},
		{name: "unsafe suffix", anchorPath: base, parent: filepath.Join(filepath.Dir(base), "outside"), want: "suffix is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newBackupParentProjectionState(t.Context(), test.anchorPath, test.parent, anchor, lookup)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("projection constructor = %v; want %q", err, test.want)
			}
		})
	}
	if err := validateProspectiveBackupParentOutsideSource(filepath.Join(base, "store.db"), nil); err == nil {
		t.Fatal("nil projection state was accepted")
	}

	state, err := newBackupParentProjectionState(t.Context(), base, filepath.Join(base, "backups"), anchor, lookup)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "store.db")
	if err := validateProspectiveBackupParentOutsideSource(source, state); err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(source, state); err != nil {
		t.Fatalf("duplicate projection source = %v", err)
	}

	canary := errors.New("projection lookup canary")
	state, err = newBackupParentProjectionState(t.Context(), base, filepath.Join(base, "backups"), anchor,
		func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, canary
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(source, state); !errors.Is(err, canary) {
		t.Fatalf("projection lookup failure = %v", err)
	}

	physical := filepath.Join(base, "physical")
	state, err = newBackupParentProjectionState(t.Context(), base, filepath.Join(base, "backups"), anchor,
		func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			if filepath.Clean(path) == physical {
				return anchor, fileidentity.ObjectTypeDirectory, true, nil
			}
			return fileidentity.Identity{}, 0, false, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(filepath.Join(physical, "store.db"), state); err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveBackupParentOutsideSource(
		filepath.Join(physical, "backups", "member"), state,
	); err == nil || !strings.Contains(err.Error(), "aliases a catalog source") {
		t.Fatalf("new source against existing projection = %v", err)
	}

	state, err = newBackupParentProjectionState(t.Context(), base, filepath.Join(base, "backups"), anchor,
		func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return anchor, fileidentity.ObjectTypeDirectory, true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	state.suffix = strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 255)+string(os.PathSeparator), 64), string(os.PathSeparator))
	if err := validateProspectiveBackupParentOutsideSource(source, state); err == nil ||
		!strings.Contains(err.Error(), "projected path is invalid") {
		t.Fatalf("oversized projected path = %v", err)
	}

	state = &backupParentProjectionState{ctx: canceledParentTreeContext()}
	if err := compareProspectiveBackupProjection(base, source, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled projection comparison = %v", err)
	}
	if err := validateBackupParentProjectionStable(nil); err == nil {
		t.Fatal("nil stable projection state was accepted")
	}
	state = &backupParentProjectionState{
		ctx: canceledParentTreeContext(), identity: lookup,
		seen: map[string]backupParentProjectionObservation{base: {}}, seenOrder: []string{base},
	}
	if err := validateBackupParentProjectionStable(state); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stable projection = %v", err)
	}
	state.ctx = t.Context()
	state.identity = func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		return fileidentity.Identity{}, 0, false, canary
	}
	if err := validateBackupParentProjectionStable(state); !errors.Is(err, canary) {
		t.Fatalf("stable projection lookup failure = %v", err)
	}
}

func TestBackupParentCatalogBoundCoverage(t *testing.T) {
	if _, err := validateBackupParentCatalogBounds(
		t.Context(), make([]storecatalog.Spec, backupMaxEntries/4+1),
	); err == nil {
		t.Fatal("oversized generation catalog was accepted")
	}
	if _, err := validateBackupParentCatalogBounds(
		&cancelAfterMigrationErrChecks{Context: t.Context()}, make([]storecatalog.Spec, 257),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("catalog cancellation = %v", err)
	}
	if _, err := validateBackupParentCatalogBounds(t.Context(), []storecatalog.Spec{{
		LegacyRoots: make([]string, backupMaxLegacyRoots+1),
	}}); err == nil {
		t.Fatal("oversized legacy catalog was accepted")
	}

	base := t.TempDir()
	spec := storecatalog.Spec{Path: filepath.Join(base, "store", "store.db"), LegacyRoots: []string{filepath.Join(base, "legacy")}}
	for _, allowed := range []int{2, 6} {
		if _, err := validateBackupParentWithContext(
			&cancelAfterMigrationErrChecks{Context: t.Context(), allowed: allowed},
			filepath.Join(base, "archive"), base, []storecatalog.Spec{spec},
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("parent validation cancellation after %d checks = %v", allowed, err)
		}
	}
}

func TestBackupParentCreatedUnixHandleFaultCoverage(t *testing.T) {
	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	regular := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	regularFile, err := os.Open(regular)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regularFile.Close() })
	if err := secureBackupParentCreatedDirectoryHandle(regularFile, parentTreeIdentity(t, regular)); err == nil {
		t.Fatal("regular file passed created-directory security")
	}
	if err := validateBackupParentCreatedDirectoryHandle(regularFile, parentTreeIdentity(t, regular)); err == nil {
		t.Fatal("regular file passed created-directory validation")
	}
	if err := syncBackupParentCreatedDirectoryHandle(regularFile, parentTreeIdentity(t, regular)); err == nil {
		t.Fatal("regular file passed pre-sync validation")
	}

	fd, err := unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	pathHandle := os.NewFile(uintptr(fd), base)
	if pathHandle == nil {
		_ = unix.Close(fd)
		t.Fatal("O_PATH descriptor conversion failed")
	}
	t.Cleanup(func() { _ = pathHandle.Close() })
	if err := secureBackupParentCreatedDirectoryHandle(pathHandle, identity); err == nil {
		t.Fatal("O_PATH chmod unexpectedly succeeded")
	}
	if err := syncBackupParentCreatedDirectoryHandle(pathHandle, identity); err == nil {
		t.Fatal("O_PATH sync unexpectedly succeeded")
	}
}
