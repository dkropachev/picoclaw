package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestSnapshotBackupDurabilityOperationFailures(t *testing.T) {
	canary := errors.New("snapshot operation canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupSnapshotOps)
		want   string
	}{
		{name: "create root", want: "create database migration backup", mutate: func(ops *backupSnapshotOps) {
			ops.mkdir = func(string, os.FileMode) error { return canary }
		}},
		{name: "secure root", want: "secure database migration backup", mutate: func(ops *backupSnapshotOps) {
			ops.secureDir = func(string) error { return canary }
		}},
		{name: "sync parent", want: "sync database backup parent", mutate: func(ops *backupSnapshotOps) {
			ops.syncDir = func(string) error { return canary }
		}},
		{name: "complete manifest", want: "commit database backup inventory", mutate: func(ops *backupSnapshotOps) {
			ops.commit = func(*backupSession) error { return canary }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
			ops := defaultBackupSnapshotOps()
			test.mutate(&ops)
			session, err := snapshotBackupWithOps(
				t.Context(), time.Now, home,
				[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "", ops,
			)
			if err == nil || !errors.Is(err, canary) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("snapshotBackupWithOps() = %#v, %v", session, err)
			}
		})
	}
}

func TestSnapshotBackupKnownGenerationTransitionFailures(t *testing.T) {
	canary := errors.New("known generation canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSnapshotOps, string)
		want   string
	}{
		{name: "inspect", want: "known generation canary", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.lstat = backupPathLstatFault(ops.lstat, source, 0, nil, canary)
		}},
		{name: "identity error", want: "known generation canary", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.identity = backupIdentityFault(ops.identity, source, canary)
		}},
		{name: "identity disappears", want: "changed during identity lookup", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.identity = backupIdentityFault(ops.identity, source, nil)
		}},
		{
			name: "identity replacement",
			mutate: func(t *testing.T, ops *backupSnapshotOps, source string) {
				other := filepath.Join(filepath.Dir(source), "other.db")
				writeMigrationFile(t, other, []byte("other generation"))
				otherInfo, err := os.Lstat(other)
				if err != nil {
					t.Fatal(err)
				}
				ops.lstat = backupPathLstatFault(ops.lstat, source, 2, otherInfo, nil)
			},
			want: "changed during identity lookup",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			known := storecatalog.Spec{ID: "global/known", Path: filepath.Join(home, "known.db")}
			selected := storecatalog.Spec{ID: "global/selected", Path: filepath.Join(home, "selected.db")}
			writeMigrationFile(t, known.Path, []byte("known"))
			ops := defaultBackupSnapshotOps()
			test.mutate(t, &ops, known.Path)
			session, err := snapshotBackupWithOps(
				t.Context(), time.Now, home,
				[]storecatalog.Spec{known, selected}, []storecatalog.Spec{selected}, "", ops,
			)
			if session != nil || err == nil {
				t.Fatalf("snapshotBackupWithOps() = %#v, %v", session, err)
			}
		})
	}
}

func TestSnapshotBackupSelectedGenerationOperationFailures(t *testing.T) {
	canary := errors.New("selected generation canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSnapshotOps, string)
		want   string
	}{
		{name: "identity error", want: "identity is unavailable", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.identity = backupIdentityFault(ops.identity, source, canary)
		}},
		{
			name: "copy",
			mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
				original := ops.copyFile
				ops.copyFile = func(
					ctx context.Context, root, storeID, role, path, destination string,
					budget *backupBudget,
				) (BackupFileManifest, error) {
					if path == source {
						return BackupFileManifest{}, canary
					}
					return original(ctx, root, storeID, role, path, destination, budget)
				}
			},
			want: "snapshot store",
		},
		{
			name: "identity changes",
			mutate: func(t *testing.T, ops *backupSnapshotOps, source string) {
				other := filepath.Join(filepath.Dir(source), "other.db")
				writeMigrationFile(t, other, []byte("other"))
				ops.identity = archiveCloseoutIdentityResult(
					ops.identity, source, 4, parentTreeIdentity(t, other), true, nil,
				)
			},
			want: "identity changed during snapshot",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			known := storecatalog.Spec{ID: "global/known", Path: filepath.Join(home, "known.db")}
			selected := storecatalog.Spec{ID: "global/selected", Path: filepath.Join(home, "selected.db")}
			writeMigrationFile(t, selected.Path, []byte("selected"))
			ops := defaultBackupSnapshotOps()
			test.mutate(t, &ops, selected.Path)
			session, err := snapshotBackupWithOps(
				t.Context(), time.Now, home,
				[]storecatalog.Spec{known, selected}, []storecatalog.Spec{selected}, "", ops,
			)
			if session != nil || err == nil {
				t.Fatalf("snapshotBackupWithOps() = %#v, %v", session, err)
			}
		})
	}
}

func TestSnapshotBackupLegacyOperationFailures(t *testing.T) {
	canary := errors.New("legacy operation canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSnapshotOps, string)
		want   string
	}{
		{
			name: "walk",
			mutate: func(_ *testing.T, ops *backupSnapshotOps, _ string) {
				ops.walkLegacy = func(
					context.Context, string, string, map[string]struct{},
					map[fileidentity.Identity]struct{}, *backupBudget, func(string) error,
				) error {
					return canary
				}
			},
			want: "legacy inputs",
		},
		{name: "identity error", want: "legacy operation canary", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.walkLegacy = visitLegacySource(source)
			ops.identity = backupIdentityFault(ops.identity, source, canary)
		}},
		{name: "identity disappears", want: "identity is unavailable", mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
			ops.walkLegacy = visitLegacySource(source)
			ops.identity = backupIdentityFault(ops.identity, source, nil)
		}},
		{
			name: "copy",
			mutate: func(_ *testing.T, ops *backupSnapshotOps, source string) {
				ops.walkLegacy = visitLegacySource(source)
				ops.copyFile = func(
					context.Context, string, string, string, string, string, *backupBudget,
				) (BackupFileManifest, error) {
					return BackupFileManifest{}, canary
				}
			},
			want: "legacy operation canary",
		},
		{
			name: "identity changes",
			mutate: func(t *testing.T, ops *backupSnapshotOps, source string) {
				ops.walkLegacy = visitLegacySource(source)
				other := filepath.Join(filepath.Dir(source), "other.json")
				writeMigrationFile(t, other, []byte("other"))
				ops.identity = archiveCloseoutIdentityResult(
					ops.identity, source, 2, parentTreeIdentity(t, other), true, nil,
				)
			},
			want: "identity changed during snapshot",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			known := storecatalog.Spec{ID: "global/known", Path: filepath.Join(home, "known.db")}
			legacy := filepath.Join(home, "legacy.json")
			selected := storecatalog.Spec{
				ID: "global/selected", Path: filepath.Join(home, "selected.db"),
				LegacyRoots: []string{legacy},
			}
			writeMigrationFile(t, legacy, []byte("legacy"))
			ops := defaultBackupSnapshotOps()
			test.mutate(t, &ops, legacy)
			session, err := snapshotBackupWithOps(
				t.Context(), time.Now, home,
				[]storecatalog.Spec{known, selected}, []storecatalog.Spec{selected}, "", ops,
			)
			if session != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("snapshotBackupWithOps() = %#v, %v", session, err)
			}
		})
	}
}

func visitLegacySource(source string) func(
	context.Context, string, string, map[string]struct{}, map[fileidentity.Identity]struct{},
	*backupBudget, func(string) error,
) error {
	return func(
		_ context.Context,
		_, _ string,
		_ map[string]struct{},
		_ map[fileidentity.Identity]struct{},
		_ *backupBudget,
		visit func(string) error,
	) error {
		return visit(source)
	}
}
