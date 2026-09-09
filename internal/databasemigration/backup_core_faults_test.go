package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestBackupCoreSnapshotParentAndCleanupFaults(t *testing.T) {
	canary := errors.New("core snapshot canary")
	fixedNow := func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	t.Run("created parent rolls back after canceled revalidation", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		session, err := snapshotBackupWithOps(
			&backupCoreParentAppearedContext{Context: t.Context(), parent: parent, cancel: true},
			fixedNow, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
			defaultBackupSnapshotOps(),
		)
		requireBackupSnapshotError(t, session, err, context.Canceled.Error(), context.Canceled)
		if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("created parent survived rejected revalidation: %v", statErr)
		}
	})
	t.Run("created parent rolls back after privacy drift", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		ctx := &backupCoreParentAppearedContext{Context: t.Context(), parent: parent}
		ctx.onAppear = func() {
			if err := os.Chmod(parent, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		session, err := snapshotBackupWithOps(
			ctx, fixedNow, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
			defaultBackupSnapshotOps(),
		)
		requireBackupSnapshotError(t, session, err, "after containment scan", nil)
		if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("drifted created parent survived rollback: %v", statErr)
		}
	})
	t.Run("parent changes before stage creation", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		ops := defaultBackupSnapshotOps()
		originalLstat := ops.lstat
		changed := false
		ops.lstat = func(path string) (os.FileInfo, error) {
			info, err := originalLstat(path)
			if !changed && strings.HasSuffix(path, backupPartialSuffix) {
				changed = true
				parentTreeReplaceDirectory(t, parent)
			}
			return info, err
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "identity changed", nil)
	})
	t.Run("parent changes during stage creation", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		ops := defaultBackupSnapshotOps()
		originalMkdir := ops.mkdir
		ops.mkdir = func(path string, mode os.FileMode) error {
			if err := originalMkdir(path, mode); err != nil {
				return err
			}
			parentTreeReplaceDirectory(t, parent)
			return nil
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "stage creation", nil)
	})
	t.Run("cleanup does not cross a replaced parent", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		ops := defaultBackupSnapshotOps()
		removeCalls, syncCalls := 0, 0
		ops.secureDir = func(string) error {
			parentTreeReplaceDirectory(t, parent)
			return canary
		}
		ops.removeAll = func(string, fileidentity.Identity) error {
			removeCalls++
			return nil
		}
		ops.syncDir = func(string) error {
			syncCalls++
			return nil
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "", canary)
		if removeCalls != 0 || syncCalls != 0 {
			t.Fatalf("cleanup across replaced parent called remove=%d sync=%d", removeCalls, syncCalls)
		}
	})
	t.Run("secured stage identity changes", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		other := filepath.Join(home, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		otherIdentity := backupCoreIdentity(t, other, fileidentity.ObjectTypeDirectory)
		ops := defaultBackupSnapshotOps()
		originalIdentity := ops.identity
		stageCalls, removeCalls := 0, 0
		ops.identity = func(path string) (fileidentity.Identity, bool, error) {
			if strings.HasSuffix(path, backupPartialSuffix) {
				stageCalls++
				if stageCalls == 2 {
					return otherIdentity, true, nil
				}
			}
			return originalIdentity(path)
		}
		originalRemove := ops.removeAll
		ops.removeAll = func(path string, identity fileidentity.Identity) error {
			removeCalls++
			return originalRemove(path, identity)
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "changed while securing", nil)
		if removeCalls != 1 {
			t.Fatalf("secured-stage drift called remove=%d times", removeCalls)
		}
	})
	t.Run("cleanup refuses a substituted stage", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		other := filepath.Join(home, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		otherIdentity := backupCoreIdentity(t, other, fileidentity.ObjectTypeDirectory)
		ops := defaultBackupSnapshotOps()
		originalIdentity := ops.identity
		stageCalls, removeCalls, syncCalls := 0, 0, 0
		ops.identity = func(path string) (fileidentity.Identity, bool, error) {
			if strings.HasSuffix(path, backupPartialSuffix) {
				stageCalls++
				if stageCalls > 1 {
					return otherIdentity, true, nil
				}
			}
			return originalIdentity(path)
		}
		ops.secureDir = func(string) error { return canary }
		ops.removeAll = func(string, fileidentity.Identity) error {
			removeCalls++
			return nil
		}
		ops.syncDir = func(string) error {
			syncCalls++
			return nil
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "stage identity changed", canary)
		if removeCalls != 0 || syncCalls != 0 {
			t.Fatalf("substituted-stage cleanup called remove=%d sync=%d", removeCalls, syncCalls)
		}
	})
}

type backupCoreParentAppearedContext struct {
	context.Context
	parent   string
	onAppear func()
	cancel   bool
}

func (ctx *backupCoreParentAppearedContext) Err() error {
	if _, err := os.Lstat(ctx.parent); err == nil {
		if ctx.onAppear != nil {
			action := ctx.onAppear
			ctx.onAppear = nil
			action()
		}
		if ctx.cancel {
			return context.Canceled
		}
	}
	return ctx.Context.Err()
}

func TestBackupCoreSnapshotFinalInventoryFaults(t *testing.T) {
	fixedNow := func() time.Time { return time.Unix(1_810_000_000, 0).UTC() }
	for _, test := range []struct {
		name, want string
		edit       func(*testing.T, BackupFileManifest) BackupFileManifest
	}{
		{
			name: "manifest metadata budget",
			want: "metadata budget",
			edit: func(_ *testing.T, record BackupFileManifest) BackupFileManifest {
				record.SHA256 = strings.Repeat("f", int(backupMaxManifestSize))
				return record
			},
		},
		{
			name: "manifest semantic validation",
			want: "inventory",
			edit: func(_ *testing.T, record BackupFileManifest) BackupFileManifest {
				record.Role = "unsupported"
				return record
			},
		},
		{
			name: "final source verification",
			want: "final database backup source verification",
			edit: func(t *testing.T, record BackupFileManifest) BackupFileManifest {
				writeMigrationFile(t, record.Source, []byte("changed generation"))
				return record
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
			writeMigrationFile(t, spec.Path, []byte("core generation"))
			ops := defaultBackupSnapshotOps()
			originalCopy := ops.copyFile
			ops.copyFile = func(
				ctx context.Context,
				root, storeID, role, source, destination string,
				budget *backupBudget,
			) (BackupFileManifest, error) {
				record, err := originalCopy(ctx, root, storeID, role, source, destination, budget)
				if err != nil {
					return BackupFileManifest{}, err
				}
				return test.edit(t, record), nil
			}
			session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
			requireBackupSnapshotError(t, session, err, test.want, nil)
		})
	}
	t.Run("late catalog generation", func(t *testing.T) {
		home := migrationHome(t)
		legacy := filepath.Join(home, "missing-legacy")
		spec := storecatalog.Spec{
			ID: "global/core", Path: filepath.Join(home, "core.db"), LegacyRoots: []string{legacy},
		}
		writeMigrationFile(t, spec.Path, []byte("core generation"))
		ops := defaultBackupSnapshotOps()
		ops.walkLegacy = func(
			_ context.Context,
			_, _ string,
			_ map[string]struct{},
			_ map[fileidentity.Identity]struct{},
			_ *backupBudget,
			_ func(string) error,
		) error {
			return os.Mkdir(spec.Path+"-wal", 0o700)
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "exclusion is unsafe", nil)
	})

	t.Run("parent changes during publication", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/core", Path: filepath.Join(home, "core.db")}
		ops := defaultBackupSnapshotOps()
		originalRename := ops.rename
		ops.rename = func(oldPath, newPath string) error {
			if err := originalRename(oldPath, newPath); err != nil {
				return err
			}
			parentTreeReplaceDirectory(t, parent)
			return nil
		}
		session, err := backupCoreSnapshot(t, fixedNow, home, spec, ops)
		requireBackupSnapshotError(t, session, err, "during publication", nil)
	})
}

func TestBackupCoreVerificationIdentityInventories(t *testing.T) {
	canary := errors.New("core verification canary")
	for _, test := range []struct {
		name, want string
		edit       func(*testing.T, *backupSession, *backupVerifyOps, fileidentity.Identity, fileidentity.Identity)
	}{
		{"parent pin changes", "backup parent", func(t *testing.T, session *backupSession, _ *backupVerifyOps, _, _ fileidentity.Identity) {
			parentTreeReplaceDirectory(t, session.parent)
		}},
		{"unpublished root", "not published", func(_ *testing.T, session *backupSession, _ *backupVerifyOps, _, _ fileidentity.Identity) {
			session.root += backupPartialSuffix
			session.finalRoot = filepath.Join(session.parent, "different")
		}},
		{"second inventory error", "core verification canary", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, _, _ fileidentity.Identity) {
			ops.exactTree = backupSecondExactTree(ops.exactTree, canary, nil)
		}},
		{"inventory cardinality drift", "identities changed", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, _, _ fileidentity.Identity) {
			ops.exactTree = backupSecondExactTree(ops.exactTree, nil, func(inventory map[fileidentity.Identity]string) {
				for identity := range inventory {
					delete(inventory, identity)
					break
				}
			})
		}},
		{"inventory path drift", "identities changed", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, _, _ fileidentity.Identity) {
			ops.exactTree = backupSecondExactTree(ops.exactTree, nil, func(inventory map[fileidentity.Identity]string) {
				for identity, path := range inventory {
					inventory[identity] = path + "-replacement"
					break
				}
			})
		}},
		{"final root identity drift", "during verification", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, first, second fileidentity.Identity) {
			calls := 0
			ops.identity = func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				calls++
				if calls == 2 {
					return second, fileidentity.ObjectTypeDirectory, true, nil
				}
				return first, fileidentity.ObjectTypeDirectory, true, nil
			}
		}},
		{"final root validation unavailable", "final root validation is unavailable", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, _, _ fileidentity.Identity) {
			ops.validateRoot = nil
		}},
		{"final root validation failure", "core verification canary", func(_ *testing.T, _ *backupSession, ops *backupVerifyOps, _, _ fileidentity.Identity) {
			ops.validateRoot = func(string, fileidentity.Identity) error { return canary }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, ops, first, second := backupCoreVerifyFixture(t)
			test.edit(t, session, &ops, first, second)
			requireBackupError(t, session.verifyFilesWithOps(t.Context(), ops), test.want)
		})
	}
}

func TestBackupCoreVerificationRevalidatesPayloadsBeforeControls(t *testing.T) {
	session, ops, _, _ := backupCoreVerifyFixture(t)
	revalidated := false
	ops.revalidate = func(
		context.Context, string, map[string]fileidentity.Identity, map[string]os.FileInfo,
	) error {
		revalidated = true
		return nil
	}
	read := ops.read
	reads := 0
	ops.read = func(path string, limit int64) ([]byte, fileidentity.Identity, error) {
		reads++
		if reads > 2 && !revalidated {
			return nil, fileidentity.Identity{}, errors.New("controls read before payload revalidation")
		}
		return read(path, limit)
	}
	if err := session.verifyFilesWithOps(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
}

func TestBackupCoreExactTreeHandleAndTypeFaults(t *testing.T) {
	t.Run("regular handle cannot resolve inventoried path", func(t *testing.T) {
		pathRoot := migrationHome(t)
		handleRoot := migrationHome(t)
		member := filepath.Join(pathRoot, "member")
		writeMigrationFile(t, member, []byte("member"))
		before, err := os.Lstat(member)
		if err != nil {
			t.Fatal(err)
		}
		root := parentTreeRoot(t, handleRoot)
		err = verifyExactBackupRegular(
			pathRoot, root, "member", before, make(map[fileidentity.Identity]string),
		)
		if err == nil || !strings.Contains(err.Error(), "open database backup inventory file") {
			t.Fatalf("unresolvable regular handle = %v", err)
		}
	})
}

func TestBackupCoreLiveStateAndRecordFaults(t *testing.T) {
	_, spec, session := backupArchiveLiveSnapshot(t)
	ops := defaultBackupLiveVerifyOps()
	state, err := session.newBackupLiveVerificationState(t.Context(), ops)
	if err != nil {
		t.Fatal(err)
	}

	unsafeInfo := backupCoreFileInfo{name: "legacy", mode: os.ModeNamedPipe}
	for _, test := range []struct {
		name, want string
		edit       func(*backupLiveVerificationState, *backupLiveVerifyOps)
	}{
		{"incomplete state", "state is invalid", func(state *backupLiveVerificationState, _ *backupLiveVerifyOps) {
			state.records = nil
		}},
		{"missing indexed store", "store manifest is missing", func(state *backupLiveVerificationState, _ *backupLiveVerifyOps) {
			delete(state.stores, spec.ID.String())
		}},
		{"unsafe initial legacy type", "unsafe type", func(_ *backupLiveVerificationState, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.LegacyRoots[0], 0, unsafeInfo, nil)
		}},
		{"unsafe final legacy type", "changed to an unsafe type", func(_ *backupLiveVerificationState, ops *backupLiveVerifyOps) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.LegacyRoots[0], 2, unsafeInfo, nil)
		}},
		{"legacy enumeration is bounded", "file count limit", func(_ *backupLiveVerificationState, ops *backupLiveVerifyOps) {
			ops.walkLegacy = func(_ context.Context, _, _ string, _ map[string]struct{}, _ *backupBudget, visit func(string) error) error {
				for index := 0; index <= backupMaxFiles; index++ {
					if err := visit(filepath.Join(spec.LegacyRoots[0], backupCoreIndexName(index))); err != nil {
						return err
					}
				}
				return nil
			}
		}},
		{"legacy record source remains exact", "path changed", func(state *backupLiveVerificationState, _ *backupLiveVerifyOps) {
			records := state.records[spec.ID.String()]
			for key, record := range records.legacy {
				record.Source = filepath.Join(filepath.Dir(record.Source), "different")
				records.legacy[key] = record
				break
			}
			state.records[spec.ID.String()] = records
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid, faultOps := backupCoreCloneLiveState(state), ops
			test.edit(invalid, &faultOps)
			requireBackupError(t, session.verifyLiveSourcesWithState(t.Context(), spec, faultOps, invalid), test.want)
		})
	}

	t.Run("equal sort values use deterministic tie breaker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "same")
		got := sortedBackupPaths(map[string]string{"first": path, "second": path})
		if len(got) != 2 || got[0] != path || got[1] != path {
			t.Fatalf("sorted equal paths = %#v", got)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("hard-linked live source", func(t *testing.T) {
			path := filepath.Join(migrationHome(t), "source")
			writeMigrationFile(t, path, []byte("payload"))
			record := backupArchiveLiveRecord(t, path)
			if err := os.Link(path, filepath.Join(t.TempDir(), "alias")); err != nil {
				t.Fatal(err)
			}
			if _, err := verifyLiveSourceRecord(t.Context(), path, record); err == nil ||
				!strings.Contains(err.Error(), "hard-link") {
				t.Fatalf("hard-linked live source = %v", err)
			}
		})
	}
}

func backupCoreVerifyFixture(
	t *testing.T,
) (*backupSession, backupVerifyOps, fileidentity.Identity, fileidentity.Identity) {
	t.Helper()
	outer := migrationHome(t)
	parent := filepath.Join(outer, "parent")
	root := filepath.Join(parent, "archive")
	other := filepath.Join(outer, "other")
	for _, path := range []string{parent, root, other} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parentIdentity, err := pinPrivateBackupDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	rootIdentity := backupCoreIdentity(t, root, fileidentity.ObjectTypeDirectory)
	otherIdentity := backupCoreIdentity(t, other, fileidentity.ObjectTypeDirectory)
	manifestControl := filepath.Join(outer, "manifest-control")
	markerControl := filepath.Join(outer, "marker-control")
	writeMigrationFile(t, manifestControl, nil)
	writeMigrationFile(t, markerControl, nil)
	manifestIdentity := backupCoreIdentity(t, manifestControl, fileidentity.ObjectTypeRegular)
	markerIdentity := backupCoreIdentity(t, markerControl, fileidentity.ObjectTypeRegular)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("canonical manifest\n")
	digest := sha256.Sum256(payload)
	hashPayload := append([]byte(hex.EncodeToString(digest[:])), '\n')
	session := &backupSession{
		root: root, identity: rootIdentity, parent: parent, parentIdentity: parentIdentity,
		manifest: BackupManifest{},
	}
	ops := backupVerifyOps{
		validateManifest: func(BackupManifest) error { return nil },
		lstat:            func(string) (os.FileInfo, error) { return rootInfo, nil },
		validateDirectory: func(string, os.FileInfo) error {
			return nil
		},
		marshal: func(BackupManifest) ([]byte, error) { return payload, nil },
		read: func(path string, _ int64) ([]byte, fileidentity.Identity, error) {
			if filepath.Base(path) == backupManifestHash {
				return hashPayload, markerIdentity, nil
			}
			return payload, manifestIdentity, nil
		},
		hash: func(
			context.Context, string, os.FileInfo, fileidentity.Identity, int64,
		) (string, int64, error) {
			return "", 0, nil
		},
		identity: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return rootIdentity, fileidentity.ObjectTypeDirectory, true, nil
		},
		exactTree: func(
			context.Context, string, BackupManifest,
		) (map[fileidentity.Identity]string, error) {
			return map[fileidentity.Identity]string{
				rootIdentity:     ".",
				manifestIdentity: backupManifestName,
				markerIdentity:   backupManifestHash,
			}, nil
		},
		revalidate: func(
			context.Context, string, map[string]fileidentity.Identity, map[string]os.FileInfo,
		) error {
			return nil
		},
		validateRoot: func(string, fileidentity.Identity) error { return nil },
	}
	return session, ops, rootIdentity, otherIdentity
}

func backupCoreCloneLiveState(state *backupLiveVerificationState) *backupLiveVerificationState {
	clone := &backupLiveVerificationState{
		excludedKeys:   make(map[string]struct{}, len(state.excludedKeys)),
		identityOwners: make(map[fileidentity.Identity]string, len(state.identityOwners)),
		stores:         make(map[string]BackupStoreManifest, len(state.stores)),
		records:        make(map[string]backupLiveStoreRecords, len(state.records)),
	}
	for key := range state.excludedKeys {
		clone.excludedKeys[key] = struct{}{}
	}
	for identity, owner := range state.identityOwners {
		clone.identityOwners[identity] = owner
	}
	for key, store := range state.stores {
		clone.stores[key] = store
	}
	for key, records := range state.records {
		generation := make(map[string]BackupFileManifest, len(records.generation))
		legacy := make(map[string]BackupFileManifest, len(records.legacy))
		for role, record := range records.generation {
			generation[role] = record
		}
		for path, record := range records.legacy {
			legacy[path] = record
		}
		clone.records[key] = backupLiveStoreRecords{generation: generation, legacy: legacy}
	}
	return clone
}

func backupCoreSnapshot(
	t *testing.T, now func() time.Time, home string, spec storecatalog.Spec, ops backupSnapshotOps,
) (*backupSession, error) {
	t.Helper()
	return snapshotBackupWithOps(
		t.Context(), now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "", ops,
	)
}

func backupSecondExactTree(
	original func(context.Context, string, BackupManifest) (map[fileidentity.Identity]string, error),
	fault error,
	edit func(map[fileidentity.Identity]string),
) func(context.Context, string, BackupManifest) (map[fileidentity.Identity]string, error) {
	calls := 0
	return func(ctx context.Context, root string, manifest BackupManifest) (map[fileidentity.Identity]string, error) {
		calls++
		if calls == 2 && fault != nil {
			return nil, fault
		}
		inventory, err := original(ctx, root, manifest)
		if calls == 2 && edit != nil {
			edit(inventory)
		}
		return inventory, err
	}
}

func backupCoreIdentity(
	t *testing.T,
	path string,
	want fileidentity.ObjectType,
) fileidentity.Identity {
	t.Helper()
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != want {
		t.Fatalf("identity for %q = %#v, %v, %t, %v", path, identity, objectType, exists, err)
	}
	return identity
}

func backupCoreIndexName(index int) string {
	return "entry-" + strconv.Itoa(index)
}

func backupArchiveGenerationSnapshot(
	t *testing.T,
) (string, storecatalog.Spec, *backupSession) {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("original database"))
	return home, spec, backupArchiveSnapshot(
		t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
	)
}

func backupArchiveLiveSnapshot(t *testing.T) (string, storecatalog.Spec, *backupSession) {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{filepath.Join(home, "legacy")},
	}
	writeMigrationFile(t, spec.Path, []byte("original database"))
	writeMigrationFile(t, filepath.Join(spec.LegacyRoots[0], "auth.json"), []byte("legacy payload"))
	return home, spec, backupArchiveSnapshot(
		t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
	)
}

func backupArchiveSnapshot(
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
		t.Context(), func() time.Time { return time.Unix(2, 0).UTC() },
		home, all, selected, parent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func backupArchiveLiveRecord(t *testing.T, path string) BackupFileManifest {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, exists, err := fileidentity.Existing(path)
	if err != nil || !exists {
		t.Fatalf("live source identity = %#v, %t, %v", identity, exists, err)
	}
	digest := sha256.Sum256(payload)
	return BackupFileManifest{
		Source: path, SourceIdentity: identity.String(), SHA256: hex.EncodeToString(digest[:]),
		Size: int64(len(payload)), SourceMode: uint32(info.Mode().Perm()),
	}
}

type backupCoreFileInfo struct {
	name string
	mode os.FileMode
}

func (i backupCoreFileInfo) Name() string      { return i.name }
func (backupCoreFileInfo) Size() int64         { return 0 }
func (i backupCoreFileInfo) Mode() os.FileMode { return i.mode }
func (backupCoreFileInfo) ModTime() time.Time  { return time.Time{} }
func (i backupCoreFileInfo) IsDir() bool       { return i.mode.IsDir() }
func (backupCoreFileInfo) Sys() any            { return nil }
