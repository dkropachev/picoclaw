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

func TestBackupPrepareNoCleanupClosuresAreSafe(t *testing.T) {
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(t.TempDir(), "auth.db")}
	if path, cleanup, err := (*backupSession)(nil).prepareGenerationWithOps(
		nil, spec, defaultBackupPrepareOps(),
	); path != "" || cleanup == nil || err == nil {
		t.Fatalf("nil generation prepare = %q, %v", path, err)
	} else if cleanupErr := cleanup(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if roots, cleanup, err := (*backupSession)(nil).prepareLegacyInputsWithOps(
		nil, spec, defaultBackupPrepareOps(),
	); roots != nil || cleanup == nil || err == nil {
		t.Fatalf("nil legacy prepare = %#v, %v", roots, err)
	} else if cleanupErr := cleanup(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
}

func TestBackupPrepareRejectsEmptyDetachedProvenance(t *testing.T) {
	if store, err := backupManifestStoreForProvenance(
		validManifestValidationFixture(t), backupStoreProvenance{},
	); store.StoreID != "" || err == nil {
		t.Fatalf("empty detached provenance = %#v, %v", store, err)
	}
}

func TestBackupPrepareCleanupRejectsParentReplacement(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "generation", true: "legacy"}[legacy], func(t *testing.T) {
			_, spec, session := additionalLiveSnapshot(t)
			var cleanup func() error
			var err error
			if legacy {
				_, cleanup, err = session.prepareLegacyInputsWithOps(
					t.Context(), spec, defaultBackupPrepareOps(),
				)
			} else {
				_, cleanup, err = session.prepareGenerationWithOps(
					t.Context(), spec, defaultBackupPrepareOps(),
				)
			}
			if err != nil {
				t.Fatal(err)
			}
			originalParent := session.parent + ".original"
			if err := os.Rename(session.parent, originalParent); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(session.parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := cleanup(); err == nil || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("cleanup after parent replacement = %v", err)
			}
		})
	}
}

func TestBackupPrepareRejectsWorkRootReplacementWhileSecuring(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "generation", true: "legacy"}[legacy], func(t *testing.T) {
			_, spec, session := additionalLiveSnapshot(t)
			ops := defaultBackupPrepareOps()
			ops.secureDir = func(path string) error {
				if err := os.Rename(path, path+".original"); err != nil {
					return err
				}
				return os.Mkdir(path, 0o700)
			}
			if legacy {
				roots, cleanup, err := session.prepareLegacyInputsWithOps(
					context.Background(), spec, ops,
				)
				if roots != nil || cleanup == nil || err == nil ||
					!strings.Contains(err.Error(), "changed while securing") {
					t.Fatalf("replaced legacy work root = %#v, %v", roots, err)
				}
				return
			}
			path, cleanup, err := session.prepareGenerationWithOps(
				context.Background(), spec, ops,
			)
			if path != "" || cleanup == nil || err == nil ||
				!strings.Contains(err.Error(), "changed while securing") {
				t.Fatalf("replaced generation work root = %q, %v", path, err)
			}
		})
	}
}

func TestBackupPrepareTempDirectoryOperationFaultsCleanOwnedRoot(t *testing.T) {
	canary := errors.New("prepared temp operation canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupTempDirectoryOps, *int)
	}{
		{
			name: "parent pin",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				ops.pinParent = func(string) (fileidentity.Identity, error) {
					return fileidentity.Identity{}, canary
				}
			},
		},
		{
			name: "mkdir",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				ops.mkdirTemp = func(string, string) (string, error) { return "", canary }
			},
		},
		{
			name: "created identity",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				original := ops.existing
				calls := 0
				ops.existing = func(path string) (
					fileidentity.Identity, fileidentity.ObjectType, bool, error,
				) {
					calls++
					if calls == 1 {
						return fileidentity.Identity{}, 0, false, canary
					}
					return original(path)
				}
			},
		},
		{
			name: "parent validation",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				ops.validateParent = func(string, fileidentity.Identity) error { return canary }
			},
		},
		{
			name: "secure",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				ops.secure = func(string) error { return canary }
			},
		},
		{
			name: "secured identity",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				original := ops.existing
				calls := 0
				ops.existing = func(path string) (
					fileidentity.Identity, fileidentity.ObjectType, bool, error,
				) {
					calls++
					if calls == 2 {
						return fileidentity.Identity{}, 0, false, canary
					}
					return original(path)
				}
			},
		},
		{
			name: "final parent validation",
			mutate: func(ops *backupTempDirectoryOps, _ *int) {
				original := ops.validateParent
				calls := 0
				ops.validateParent = func(path string, identity fileidentity.Identity) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(path, identity)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := migrationHome(t)
			ops := defaultBackupTempDirectoryOps()
			removeCalls := 0
			remove := ops.remove
			ops.remove = func(path string, identity fileidentity.Identity) error {
				removeCalls++
				return remove(path, identity)
			}
			test.mutate(&ops, &removeCalls)
			path, err := createPinnedBackupTempDirectoryWithOps(parent, ".prepare-", ops)
			if path != "" || !errors.Is(err, canary) {
				t.Fatalf("faulted temp directory = %q, %v", path, err)
			}
			wantRemove := 1
			if test.name == "parent pin" || test.name == "mkdir" || test.name == "created identity" {
				wantRemove = 0
			}
			if removeCalls != wantRemove {
				t.Fatalf("faulted temp cleanup calls = %d, want %d", removeCalls, wantRemove)
			}
		})
	}
}
