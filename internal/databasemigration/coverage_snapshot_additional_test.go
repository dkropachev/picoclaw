package databasemigration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestSnapshotBackupAdditionalInputAndBoundaryFailures(t *testing.T) {
	fixedNow := func() time.Time { return time.Unix(1_900_000_000, 0).UTC() }
	mustFail := func(
		t *testing.T, home string, all, selected []storecatalog.Spec, parent, want string,
	) {
		t.Helper()
		session, err := snapshotBackup(t.Context(), fixedNow, home, all, selected, parent)
		requireBackupSnapshotError(t, session, err, want, nil)
	}

	t.Run("public custom parent", func(t *testing.T) {
		home := migrationHome(t)
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		mustFail(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, parent, "custom")
	})

	t.Run("unsafe default parent", func(t *testing.T) {
		home := migrationHome(t)
		writeMigrationFile(t, filepath.Join(home, "backups"), nil)
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		mustFail(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "", "")
	})

	t.Run("duplicate catalog entries", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		mustFail(t, home, []storecatalog.Spec{spec, spec}, []storecatalog.Spec{spec}, "", "")
	})

	t.Run("no selected stores", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		mustFail(t, home, []storecatalog.Spec{spec}, nil, "", "")
	})

	for _, test := range []struct {
		name string
		path func(string) string
		make func(*testing.T, string)
	}{
		{
			name: "selected lstat",
			path: func(home string) string { return filepath.Join(home, "bad\x00store.db") },
		},
		{
			name: "selected unsafe type",
			path: func(home string) string { return filepath.Join(home, "selected.db") },
			make: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			known := storecatalog.Spec{ID: "global/known", Path: filepath.Join(home, "known.db")}
			selected := storecatalog.Spec{ID: "global/selected", Path: test.path(home)}
			if test.make != nil {
				test.make(t, selected.Path)
			}
			mustFail(t, home, []storecatalog.Spec{known, selected}, []storecatalog.Spec{selected}, "", "")
		})
	}
}

func TestSnapshotBackupPropagatesCanceledSelectedStore(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session, err := snapshotBackup(
		ctx, time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if session != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshotBackup() = %#v, %v", session, err)
	}

	for _, target := range []string{spec.Path, spec.Path + "-journal"} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			checkedNext, ops := false, defaultBackupSnapshotOps()
			lstat := ops.lstat
			ops.lstat = func(path string) (os.FileInfo, error) {
				if path == target {
					cancel()
				} else if target == spec.Path && path == spec.Path+"-wal" {
					checkedNext = true
				}
				return lstat(path)
			}
			session, err := snapshotBackupWithOps(
				ctx, time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "", ops,
			)
			if session != nil || !errors.Is(err, context.Canceled) || checkedNext {
				t.Fatalf("canceled generation scan = %#v, %v; checked next=%t", session, err, checkedNext)
			}
		})
	}
}

func TestBackupVerificationOperationFaults(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("verify operation canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupVerifyOps)
		want   string
	}{
		{"manifest validation", func(_ *testing.T, ops *backupVerifyOps) {
			ops.validateManifest = func(BackupManifest) error { return canary }
		}, "verify operation canary"},
		{"root inspection", func(_ *testing.T, ops *backupVerifyOps) {
			ops.lstat = backupLstatFault(ops.lstat, 1, nil, canary)
		}, "inspect database backup root"},
		{"root validation", func(_ *testing.T, ops *backupVerifyOps) {
			ops.validateDirectory = func(string, os.FileInfo) error { return canary }
		}, "validate database backup root"},
		{"manifest marshal", func(_ *testing.T, ops *backupVerifyOps) {
			ops.marshal = func(BackupManifest) ([]byte, error) { return nil, canary }
		}, "verify operation canary"},
		{"manifest read", func(_ *testing.T, ops *backupVerifyOps) {
			ops.read = backupReadFault(ops.read, 1, canary)
		}, "read database backup manifest"},
		{"hash read", func(_ *testing.T, ops *backupVerifyOps) {
			ops.read = backupReadFault(ops.read, 2, canary)
		}, "read database backup manifest hash"},
		{"file hash", func(_ *testing.T, ops *backupVerifyOps) {
			ops.hash = func(context.Context, string, os.FileInfo, fileidentity.Identity, int64) (string, int64, error) {
				return "", 0, canary
			}
		}, "verify operation canary"},
		{"file inspection", func(_ *testing.T, ops *backupVerifyOps) {
			ops.lstat = backupLstatFault(ops.lstat, 2, nil, canary)
		}, "inspect database backup file"},
		{
			"file metadata", func(t *testing.T, ops *backupVerifyOps) {
				other := filepath.Join(t.TempDir(), "other")
				writeMigrationFile(t, other, []byte("different-size"))
				otherInfo, err := os.Lstat(other)
				if err != nil {
					t.Fatal(err)
				}
				ops.lstat = backupLstatFault(ops.lstat, 2, otherInfo, nil)
			}, "metadata is invalid",
		},
		{"file digest mismatch", func(_ *testing.T, ops *backupVerifyOps) {
			ops.hash = func(_ context.Context, _ string, _ os.FileInfo, _ fileidentity.Identity, size int64) (string, int64, error) {
				return strings.Repeat("0", sha256.Size*2), size, nil
			}
		}, "file hash is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultBackupVerifyOps()
			test.mutate(t, &ops)
			requireBackupError(t, session.verifyFilesWithOps(t.Context(), ops), test.want)
		})
	}
}

func TestLiveSourceVerificationOperationFaults(t *testing.T) {
	_, spec, session := backupArchiveLiveSnapshot(t)
	canary := errors.New("live verification canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupLiveVerifyOps)
		want   string
	}{
		{"catalog inspection", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.Path, 0, nil, canary)
		}, "inspect catalog generation exclusion"},
		{"catalog identity", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.identity = backupIdentityFault(ops.identity, spec.Path, canary)
		}, "catalog generation identity"},
		{"generation record verification", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.verifyRecord = func(context.Context, string, BackupFileManifest) (fileidentity.Identity, error) {
				return fileidentity.Identity{}, canary
			}
		}, "verify live database"},
		{"legacy traversal", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.walkLegacy = func(context.Context, string, string, map[string]struct{}, *backupBudget, func(string) error) error {
				return canary
			}
		}, "enumerate live legacy inputs"},
		{"legacy identity", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.identity = backupIdentityFault(ops.identity, filepath.Join(spec.LegacyRoots[0], "auth.json"), canary)
		}, "live legacy input identity"},
		{"legacy record verification", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.verifyRecord = backupRecordFault(ops.verifyRecord, "legacy", canary)
		}, "verify live legacy input"},
		{"legacy root inspection", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.LegacyRoots[0], 0, nil, canary)
		}, ""},
		{"legacy root symlink", func(t *testing.T, ops *backupLiveVerifyOps) {
			root := spec.LegacyRoots[0]
			link := filepath.Join(t.TempDir(), "root-link")
			if err := os.Symlink(root, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			linkInfo, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			ops.lstat = backupPathLstatFault(ops.lstat, root, 0, linkInfo, nil)
		}, ""},
		{"legacy root changed", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.LegacyRoots[0], 2, nil, os.ErrNotExist)
		}, ""},
		{"legacy root reinspection", func(_ *testing.T, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.LegacyRoots[0], 2, nil, canary)
		}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultBackupLiveVerifyOps()
			test.mutate(t, &ops)
			requireBackupError(t, session.verifyLiveSourcesWithOps(t.Context(), spec, ops), test.want)
		})
	}

	t.Run("legacy enumeration bound", func(t *testing.T) {
		ops := defaultBackupLiveVerifyOps()
		ops.walkLegacy = func(
			_ context.Context,
			root, _ string,
			_ map[string]struct{},
			_ *backupBudget,
			visit func(string) error,
		) error {
			for index := 0; index <= backupMaxFiles; index++ {
				if err := visit(filepath.Join(root, fmt.Sprintf("%05d", index))); err != nil {
					return err
				}
			}
			return nil
		}
		requireBackupError(t, session.verifyLiveSourcesWithOps(t.Context(), spec, ops), "file count")
	})
}

func TestLiveSourceDefensiveStateBranches(t *testing.T) {
	for _, test := range []struct {
		name, want string
		wantError  bool
		run        func(*testing.T, string, storecatalog.Spec, *backupSession) error
	}{
		{"nil context", "", false, func(_ *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			return session.verifyLiveSourcesWithOps(nil, spec, defaultBackupLiveVerifyOps())
		}},
		{"missing selected exclusion", "", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			session.manifest.CatalogGenerations = session.manifest.CatalogGenerations[1:]
			return session.verifyLiveSources(t.Context(), spec)
		}},
		{"missing store", "", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			session.manifest.Stores[0].StoreID = "global/other"
			return session.verifyLiveSources(t.Context(), spec)
		}},
		{"unrelated file record", "", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			session.manifest.Files = append(session.manifest.Files, BackupFileManifest{StoreID: "global/other", Role: "other"})
			return session.verifyLiveSources(t.Context(), spec)
		}},
		{"catalog physical alias", "", true, func(t *testing.T, home string, spec storecatalog.Spec, session *backupSession) error {
			alias := filepath.Join(home, "alias.db")
			if err := os.Link(spec.Path, alias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			session.manifest.CatalogGenerations = append(session.manifest.CatalogGenerations, alias)
			return session.verifyLiveSources(t.Context(), spec)
		}},
		{"generation inspection changes", "inspect live database", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			ops := defaultBackupLiveVerifyOps()
			ops.lstat = backupPathLstatFault(ops.lstat, spec.Path, 2, nil, errors.New("generation inspection canary"))
			return session.verifyLiveSourcesWithOps(t.Context(), spec, ops)
		}},
		{"generation verification returns invalid identity", "physical identity is invalid", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			ops := defaultBackupLiveVerifyOps()
			ops.verifyRecord = func(context.Context, string, BackupFileManifest) (fileidentity.Identity, error) {
				return fileidentity.Identity{}, nil
			}
			return session.verifyLiveSourcesWithOps(t.Context(), spec, ops)
		}},
		{"duplicate legacy enumeration", "", false, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			legacy := filepath.Join(spec.LegacyRoots[0], "auth.json")
			ops := defaultBackupLiveVerifyOps()
			ops.walkLegacy = func(_ context.Context, _, _ string, _ map[string]struct{}, _ *backupBudget, visit func(string) error) error {
				if err := visit(legacy); err != nil {
					return err
				}
				return visit(legacy)
			}
			return session.verifyLiveSourcesWithOps(t.Context(), spec, ops)
		}},
		{"aggregate live legacy file limit", "file count limit", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			ops := defaultBackupLiveVerifyOps()
			ops.walkLegacy = func(_ context.Context, _, _ string, _ map[string]struct{}, _ *backupBudget, visit func(string) error) error {
				for index := 0; index <= backupMaxFiles; index++ {
					if err := visit(filepath.Join(spec.LegacyRoots[0], fmt.Sprintf("%05d", index))); err != nil {
						return err
					}
				}
				return nil
			}
			return session.verifyLiveSourcesWithOps(t.Context(), spec, ops)
		}},
		{"legacy verification returns invalid identity", "physical identity is invalid", true, func(t *testing.T, _ string, spec storecatalog.Spec, session *backupSession) error {
			ops := defaultBackupLiveVerifyOps()
			ops.verifyRecord = backupRecordFault(ops.verifyRecord, "legacy", nil)
			return session.verifyLiveSourcesWithOps(t.Context(), spec, ops)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, spec, session := backupArchiveLiveSnapshot(t)
			err := test.run(t, home, spec, session)
			if test.wantError {
				requireBackupError(t, err, test.want)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLiveSourceIndexHelpersDefensiveBranches(t *testing.T) {
	root := t.TempDir()
	session := &backupSession{manifest: BackupManifest{
		CatalogGenerations: []string{"relative"},
	}}
	if paths, err := session.catalogGenerationExclusions(); paths != nil || err == nil {
		t.Fatalf("invalid exclusions = %v, %v", paths, err)
	}
	absolute := filepath.Join(root, "store.db")
	session.manifest.CatalogGenerations = []string{absolute, absolute}
	if paths, err := session.catalogGenerationExclusions(); paths != nil || err == nil {
		t.Fatalf("duplicate exclusions = %v, %v", paths, err)
	}

	if err := rememberLiveIdentity(nil, fileidentity.Identity{}, absolute); err == nil {
		t.Fatal("invalid physical identity was remembered")
	}

	legacy := filepath.Join(root, "legacy")
	values := deduplicateLiveLegacyRoots([]string{
		legacy,
		legacy,
		filepath.Join(legacy, "child"),
		filepath.Join(legacy, "backups", "kept"),
		filepath.Join(root, "other"),
	})
	if len(values) != 3 {
		t.Fatalf("deduplicated legacy roots = %q", values)
	}
}

func requireBackupError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || (want != "" && !strings.Contains(err.Error(), want)) {
		t.Fatalf("backup error = %v, want containing %q", err, want)
	}
}

func requireBackupSnapshotError(
	t *testing.T, session *backupSession, err error, want string, cause error,
) {
	t.Helper()
	if session != nil || err == nil || (want != "" && !strings.Contains(err.Error(), want)) ||
		(cause != nil && !errors.Is(err, cause)) {
		t.Fatalf("snapshot = %#v, %v; want containing %q and cause %v", session, err, want, cause)
	}
}

func backupLstatFault(
	original func(string) (os.FileInfo, error), nth int, info os.FileInfo, fault error,
) func(string) (os.FileInfo, error) {
	calls := 0
	return func(path string) (os.FileInfo, error) {
		calls++
		if calls == nth {
			return info, fault
		}
		return original(path)
	}
}

func backupPathLstatFault(
	original func(string) (os.FileInfo, error), target string, nth int, info os.FileInfo, fault error,
) func(string) (os.FileInfo, error) {
	calls := 0
	return func(path string) (os.FileInfo, error) {
		if path == target {
			calls++
			if nth == 0 || calls == nth {
				return info, fault
			}
		}
		return original(path)
	}
}

func backupReadFault(
	original func(string, int64) ([]byte, fileidentity.Identity, error), nth int, fault error,
) func(string, int64) ([]byte, fileidentity.Identity, error) {
	calls := 0
	return func(path string, limit int64) ([]byte, fileidentity.Identity, error) {
		calls++
		if calls == nth {
			return nil, fileidentity.Identity{}, fault
		}
		return original(path, limit)
	}
}

func backupIdentityFault(
	original func(string) (fileidentity.Identity, bool, error), target string, fault error,
) func(string) (fileidentity.Identity, bool, error) {
	return func(path string) (fileidentity.Identity, bool, error) {
		if path == target {
			return fileidentity.Identity{}, false, fault
		}
		return original(path)
	}
}

func backupRecordFault(
	original func(context.Context, string, BackupFileManifest) (fileidentity.Identity, error),
	role string,
	fault error,
) func(context.Context, string, BackupFileManifest) (fileidentity.Identity, error) {
	return func(ctx context.Context, path string, record BackupFileManifest) (fileidentity.Identity, error) {
		if record.Role == role {
			return fileidentity.Identity{}, fault
		}
		return original(ctx, path, record)
	}
}
