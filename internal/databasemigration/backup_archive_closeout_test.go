package databasemigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

type archiveCloseoutIdentityFunc = func(string) (fileidentity.Identity, bool, error)

func archiveCloseoutIdentityResult(
	original archiveCloseoutIdentityFunc, target string, nth int,
	identity fileidentity.Identity, exists bool, fault error,
) archiveCloseoutIdentityFunc {
	calls := 0
	return func(path string) (fileidentity.Identity, bool, error) {
		if path == target {
			calls++
			if nth == 0 || calls == nth {
				return identity, exists, fault
			}
		}
		return original(path)
	}
}

type archiveCloseoutLiveFixture struct {
	home    string
	spec    storecatalog.Spec
	session *backupSession
	ops     backupLiveVerifyOps
}

func newArchiveCloseoutLiveFixture(t *testing.T) *archiveCloseoutLiveFixture {
	t.Helper()
	home, spec, session := backupArchiveLiveSnapshot(t)
	return &archiveCloseoutLiveFixture{
		home: home, spec: spec, session: session, ops: defaultBackupLiveVerifyOps(),
	}
}

func (fixture *archiveCloseoutLiveFixture) requireError(t *testing.T, want string) {
	t.Helper()
	requireBackupError(t, fixture.session.verifyLiveSourcesWithOps(
		t.Context(), fixture.spec, fixture.ops,
	), want)
}

type archiveCloseoutLegacyFixture struct {
	home, source string
	spec         storecatalog.Spec
	ops          backupSnapshotOps
}

func newArchiveCloseoutLegacyFixture(t *testing.T) *archiveCloseoutLegacyFixture {
	t.Helper()
	home := migrationHome(t)
	root := filepath.Join(home, "legacy")
	source := filepath.Join(root, "state.json")
	writeMigrationFile(t, source, []byte("legacy"))
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"), LegacyRoots: []string{root},
	}
	writeMigrationFile(t, spec.Path, []byte("database"))
	ops := defaultBackupSnapshotOps()
	ops.walkLegacy = archiveCloseoutWalkOne(root, source)
	return &archiveCloseoutLegacyFixture{home: home, source: source, spec: spec, ops: ops}
}

func archiveCloseoutWalkOne(wantRoot, source string) func(
	context.Context, string, string, map[string]struct{},
	map[fileidentity.Identity]struct{}, *backupBudget, func(string) error,
) error {
	return func(
		_ context.Context, root, _ string, _ map[string]struct{},
		_ map[fileidentity.Identity]struct{}, _ *backupBudget, visit func(string) error,
	) error {
		if wantRoot != "" && root != wantRoot {
			return errors.New("unexpected legacy root")
		}
		return visit(source)
	}
}

func archiveCloseoutSnapshotWithOps(
	t *testing.T, home string, all, selected []storecatalog.Spec, ops backupSnapshotOps,
) (*backupSession, error) {
	t.Helper()
	return snapshotBackupWithOps(
		t.Context(), time.Now, home, all, selected, archiveCloseoutBackupParent(t), ops,
	)
}

func TestArchiveVerifyLiveSourceRecordInputAndMetadataBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if _, err := verifyLiveSourceRecord(
		nil, filepath.Join(t.TempDir(), "missing"), BackupFileManifest{},
	); err == nil {
		t.Fatal("missing live source verified")
	}
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := BackupFileManifest{Size: 99, SourceMode: 0o600}
	_, err := verifyLiveSourceRecord(t.Context(), path, record)
	requireBackupError(t, err, "metadata")
	identity := parentTreeIdentity(t, path)
	info := mustMigrationInfo(t, path)
	record = BackupFileManifest{
		Size: info.Size(), SourceMode: uint32(info.Mode().Perm()),
		SourceIdentity: identity.String(), SHA256: strings.Repeat("0", 64),
	}
	_, err = verifyLiveSourceRecord(t.Context(), path, record)
	requireBackupError(t, err, "content")

	unreadable := filepath.Join(t.TempDir(), "unreadable")
	writeMigrationFile(t, unreadable, []byte("payload"))
	record = backupArchiveLiveRecord(t, unreadable)
	if chmodErr := os.Chmod(unreadable, 0); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	probe, openErr := os.Open(unreadable)
	if openErr == nil {
		_ = probe.Close()
		t.Skip("process can read mode-zero files")
	}
	info = mustMigrationInfo(t, unreadable)
	record.SourceMode = uint32(info.Mode().Perm())
	_, err = verifyLiveSourceRecord(t.Context(), unreadable, record)
	requireBackupError(t, err, "changed while opening")
}

func TestArchiveLiveSourceVerificationAdditionalFaultBoundaries(t *testing.T) {
	canary := errors.New("live-source closeout canary")
	if err := (*backupSession)(nil).verifyLiveSourcesWithOps(
		t.Context(), storecatalog.Spec{}, defaultBackupLiveVerifyOps(),
	); err == nil {
		t.Fatal("nil live-source session verified")
	}
	_, spec, session := backupArchiveLiveSnapshot(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := session.verifyLiveSourcesWithOps(
		canceled, spec, defaultBackupLiveVerifyOps(),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled live-source verification = %v", err)
	}
	state, err := session.newBackupLiveVerificationState(t.Context(), defaultBackupLiveVerifyOps())
	if err != nil {
		t.Fatal(err)
	}
	if err := session.verifyLiveSourcesWithState(
		canceled, spec, defaultBackupLiveVerifyOps(), state,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled indexed live-source verification = %v", err)
	}

	t.Run("catalog scan cancellation", func(t *testing.T) {
		root := t.TempDir()
		candidate := &backupSession{root: root, manifest: BackupManifest{
			CatalogGenerations: []string{filepath.Join(root, "a"), filepath.Join(root, "b")},
		}}
		ctx, cancel := context.WithCancel(context.Background())
		ops, calls := defaultBackupLiveVerifyOps(), 0
		ops.lstat = func(string) (os.FileInfo, error) {
			calls++
			cancel()
			return nil, os.ErrNotExist
		}
		state, err := candidate.newBackupLiveVerificationState(ctx, ops)
		if state != nil || !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("canceled catalog scan = %#v, %v; calls=%d", state, err, calls)
		}
	})

	t.Run("legacy identity cancellation", func(t *testing.T) {
		_, spec, session := backupArchiveLiveSnapshot(t)
		first := filepath.Join(spec.LegacyRoots[0], "auth.json")
		second := filepath.Join(spec.LegacyRoots[0], "zz.json")
		writeMigrationFile(t, second, []byte("second"))
		ctx, cancel := context.WithCancel(context.Background())
		ops, secondCalls := defaultBackupLiveVerifyOps(), 0
		identity := ops.identity
		ops.identity = func(path string) (fileidentity.Identity, bool, error) {
			value, exists, err := identity(path)
			if path == first {
				cancel()
			} else if path == second {
				secondCalls++
			}
			return value, exists, err
		}
		if err := session.verifyLiveSourcesWithOps(
			ctx, spec, ops,
		); !errors.Is(err, context.Canceled) || secondCalls != 0 {
			t.Fatalf("canceled legacy identity scan = %v; next calls=%d", err, secondCalls)
		}
	})

	t.Run("legacy root cancellation", func(t *testing.T) {
		home := migrationHome(t)
		roots := []string{filepath.Join(home, "a"), filepath.Join(home, "b")}
		spec := storecatalog.Spec{
			ID: "global/cancel", Path: filepath.Join(home, "store.db"), LegacyRoots: roots,
		}
		session := backupArchiveSnapshot(t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec})
		ctx, cancel := context.WithCancel(context.Background())
		ops, secondCalls := defaultBackupLiveVerifyOps(), 0
		lstat := ops.lstat
		ops.lstat = func(path string) (os.FileInfo, error) {
			if path == roots[0] {
				cancel()
			} else if path == roots[1] {
				secondCalls++
			}
			return lstat(path)
		}
		if err := session.verifyLiveSourcesWithOps(
			ctx, spec, ops,
		); !errors.Is(err, context.Canceled) || secondCalls != 0 {
			t.Fatalf("canceled legacy-root scan = %v; next calls=%d", err, secondCalls)
		}
	})

	for _, test := range []struct {
		name, want string
		mutate     func(*testing.T, *archiveCloseoutLiveFixture)
	}{
		{"invalid catalog exclusions", "exclusions", func(_ *testing.T, f *archiveCloseoutLiveFixture) {
			f.session.manifest.CatalogGenerations = nil
		}},
		{"unsafe catalog member", "exclusion is unsafe", func(t *testing.T, f *archiveCloseoutLiveFixture) {
			f.ops.lstat = backupPathLstatFault(f.ops.lstat, f.spec.Path, 0, mustMigrationInfo(t, t.TempDir()), nil)
		}},
		{"catalog physical alias", "physical alias", func(t *testing.T, f *archiveCloseoutLiveFixture) {
			other := filepath.Join(f.home, "other.db")
			writeMigrationFile(t, other, []byte("other"))
			f.session.manifest.CatalogGenerations = append(f.session.manifest.CatalogGenerations, other)
			f.ops.identity = archiveCloseoutIdentityResult(
				f.ops.identity, other, 0, parentTreeIdentity(t, f.spec.Path), true, nil,
			)
		}},
		{"generation metadata", "metadata changed", func(t *testing.T, f *archiveCloseoutLiveFixture) {
			f.ops.lstat = backupPathLstatFault(f.ops.lstat, f.spec.Path, 2, mustMigrationInfo(t, t.TempDir()), nil)
		}},
		{"legacy root kind", "layout changed", func(_ *testing.T, f *archiveCloseoutLiveFixture) {
			f.session.manifest.Stores[0].LegacyRootKinds[0] = "file"
		}},
		{"legacy root identity", "root identity", func(_ *testing.T, f *archiveCloseoutLiveFixture) {
			f.ops.identity = backupIdentityFault(f.ops.identity, f.spec.LegacyRoots[0], canary)
		}},
		{"legacy root aliases generation", "physical alias", func(t *testing.T, f *archiveCloseoutLiveFixture) {
			f.ops.identity = archiveCloseoutIdentityResult(
				f.ops.identity, f.spec.LegacyRoots[0], 0,
				parentTreeIdentity(t, f.spec.Path), true, nil,
			)
		}},
		{"legacy root becomes symlink", "became a symlink", func(t *testing.T, f *archiveCloseoutLiveFixture) {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(t.TempDir(), link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			f.ops.lstat = backupPathLstatFault(
				f.ops.lstat, f.spec.LegacyRoots[0], 2, mustMigrationInfo(t, link), nil,
			)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newArchiveCloseoutLiveFixture(t)
			test.mutate(t, fixture)
			fixture.requireError(t, test.want)
		})
	}
}

func TestArchiveSnapshotSelectedGenerationFaultBoundaries(t *testing.T) {
	canary := errors.New("selected generation closeout canary")
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupSnapshotOps, storecatalog.Spec)
	}{
		{name: "inspection", mutate: func(_ *testing.T, ops *backupSnapshotOps, spec storecatalog.Spec) {
			ops.lstat = backupPathLstatFault(ops.lstat, spec.Path, 3, nil, canary)
		}},
		{name: "metadata", mutate: func(t *testing.T, ops *backupSnapshotOps, spec storecatalog.Spec) {
			ops.lstat = backupPathLstatFault(
				ops.lstat, spec.Path, 3, mustMigrationInfo(t, t.TempDir()), nil,
			)
		}},
		{name: "identity", mutate: func(_ *testing.T, ops *backupSnapshotOps, spec storecatalog.Spec) {
			ops.identity = archiveCloseoutIdentityResult(
				ops.identity, spec.Path, 3, fileidentity.Identity{}, false, canary,
			)
		}},
		{name: "identity changed", mutate: func(t *testing.T, ops *backupSnapshotOps, spec storecatalog.Spec) {
			other := filepath.Join(t.TempDir(), "other.db")
			writeMigrationFile(t, other, []byte("other"))
			ops.identity = archiveCloseoutIdentityResult(
				ops.identity, spec.Path, 4, parentTreeIdentity(t, other), true, nil,
			)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
			writeMigrationFile(t, spec.Path, []byte("database"))
			ops := defaultBackupSnapshotOps()
			test.mutate(t, &ops, spec)
			session, err := archiveCloseoutSnapshotWithOps(
				t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, ops,
			)
			requireBackupSnapshotError(t, session, err, "", nil)
		})
	}
}

func TestArchiveSnapshotLegacyIdentityAndCopyFaultBoundaries(t *testing.T) {
	canary := errors.New("legacy snapshot closeout canary")
	for _, test := range []struct {
		name, want string
		mutate     func(*testing.T, *archiveCloseoutLegacyFixture)
	}{
		{name: "input identity error", mutate: func(_ *testing.T, f *archiveCloseoutLegacyFixture) {
			f.ops.identity = backupIdentityFault(f.ops.identity, f.source, canary)
		}},
		{name: "input disappeared", mutate: func(_ *testing.T, f *archiveCloseoutLegacyFixture) {
			f.ops.identity = backupIdentityFault(f.ops.identity, f.source, nil)
		}},
		{name: "input aliases generation", mutate: func(t *testing.T, f *archiveCloseoutLegacyFixture) {
			f.ops.identity = archiveCloseoutIdentityResult(
				f.ops.identity, f.source, 0, parentTreeIdentity(t, f.spec.Path), true, nil,
			)
		}},
		{name: "destination outside root", mutate: func(t *testing.T, f *archiveCloseoutLegacyFixture) {
			outside := filepath.Join(t.TempDir(), "outside.json")
			writeMigrationFile(t, outside, []byte("outside"))
			f.ops.walkLegacy = archiveCloseoutWalkOne("", outside)
		}},
		{name: "prepared depth budget", want: "prepared legacy entry budget", mutate: func(t *testing.T, f *archiveCloseoutLegacyFixture) {
			deep := f.spec.LegacyRoots[0]
			for range backupMaxDepth + 1 {
				deep = filepath.Join(deep, "x")
			}
			f.ops.walkLegacy = archiveCloseoutWalkOne(f.spec.LegacyRoots[0], deep)
			f.ops.identity = archiveCloseoutIdentityResult(
				f.ops.identity, deep, 0, parentTreeIdentity(t, f.source), true, nil,
			)
		}},
		{name: "manifest budget", mutate: func(_ *testing.T, f *archiveCloseoutLegacyFixture) {
			original := f.ops.copyFile
			f.ops.copyFile = func(
				ctx context.Context,
				root, storeID, role, source, destination string,
				budget *backupBudget,
			) (BackupFileManifest, error) {
				if role == "legacy" {
					record, err := original(ctx, root, storeID, role, source, destination, budget)
					record.SHA256 = strings.Repeat("f", int(backupMaxManifestSize))
					return record, err
				}
				return original(ctx, root, storeID, role, source, destination, budget)
			}
		}},
		{name: "identity changed", mutate: func(t *testing.T, f *archiveCloseoutLegacyFixture) {
			other := filepath.Join(t.TempDir(), "other")
			writeMigrationFile(t, other, []byte("other"))
			f.ops.identity = archiveCloseoutIdentityResult(
				f.ops.identity, f.source, 2, parentTreeIdentity(t, other), true, nil,
			)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newArchiveCloseoutLegacyFixture(t)
			test.mutate(t, fixture)
			session, err := archiveCloseoutSnapshotWithOps(
				t, fixture.home, []storecatalog.Spec{fixture.spec},
				[]storecatalog.Spec{fixture.spec}, fixture.ops,
			)
			requireBackupSnapshotError(t, session, err, test.want, nil)
		})
	}

	t.Run("root aliases generation", func(t *testing.T) {
		home := migrationHome(t)
		main := filepath.Join(home, "auth.db")
		legacy := filepath.Join(home, "legacy.json")
		writeMigrationFile(t, main, []byte("database"))
		if err := os.Link(main, legacy); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		spec := storecatalog.Spec{ID: "global/auth", Path: main, LegacyRoots: []string{legacy}}
		ops := defaultBackupSnapshotOps()
		ops.copyFile = archiveCloseoutSyntheticCopy
		session, err := archiveCloseoutSnapshotWithOps(
			t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, ops,
		)
		requireBackupSnapshotError(t, session, err, "aliases", nil)
	})

	t.Run("roots physically alias", func(t *testing.T) {
		home := migrationHome(t)
		first := filepath.Join(home, "first.json")
		second := filepath.Join(home, "second.json")
		writeMigrationFile(t, first, []byte("legacy"))
		if err := os.Link(first, second); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		spec := storecatalog.Spec{
			ID: "global/auth", Path: filepath.Join(home, "auth.db"),
			LegacyRoots: []string{first, second},
		}
		ops := defaultBackupSnapshotOps()
		ops.copyFile = archiveCloseoutSyntheticCopy
		session, err := archiveCloseoutSnapshotWithOps(
			t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, ops,
		)
		requireBackupSnapshotError(t, session, err, "physical alias", nil)
	})
}

func archiveCloseoutSyntheticCopy(
	_ context.Context,
	_ string,
	storeID,
	role,
	source,
	destination string,
	_ *backupBudget,
) (BackupFileManifest, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return BackupFileManifest{}, err
	}
	return BackupFileManifest{
		StoreID: storeID, Role: role, Source: source, Backup: filepath.ToSlash(destination),
		SHA256: strings.Repeat("a", 64), Size: info.Size(), Mode: 0o600,
		SourceMode: uint32(info.Mode().Perm()),
	}, nil
}

func archiveCloseoutBackupParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "backups")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestArchiveExactBackupTreeDirectFaultBoundaries(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	regular := filepath.Join(rootPath, "file")
	writeMigrationFile(t, regular, []byte("file"))
	regularInfo := mustMigrationInfo(t, regular)
	if verifyErr := verifyExactBackupDirectory(
		t.Context(), rootPath, root, "file",
		map[string]backupTreeKind{"file": backupTreeDirectory},
		map[string]struct{}{}, map[fileidentity.Identity]string{},
	); verifyErr == nil {
		t.Fatal("regular file verified as exact-tree directory")
	}
	identity := parentTreeIdentity(t, rootPath)
	if verifyErr := verifyExactBackupDirectory(
		t.Context(), rootPath, root, ".",
		map[string]backupTreeKind{".": backupTreeDirectory},
		map[string]struct{}{}, map[fileidentity.Identity]string{identity: "other"},
	); verifyErr == nil || !strings.Contains(verifyErr.Error(), "physical object alias") {
		t.Fatalf("duplicate directory identity = %v", verifyErr)
	}
	missingInfo := regularInfo
	if verifyErr := verifyExactBackupRegular(
		rootPath, root, "missing", missingInfo, map[fileidentity.Identity]string{},
	); verifyErr == nil {
		t.Fatal("missing exact-tree regular file verified")
	}
	other := filepath.Join(rootPath, "other")
	writeMigrationFile(t, other, []byte("other"))
	otherInfo := mustMigrationInfo(t, other)
	requireBackupError(t, verifyExactBackupRegular(
		rootPath, root, "file", otherInfo, map[fileidentity.Identity]string{},
	), "changed while opening")
}

func TestArchiveBackupCatalogRejectsSymlinkedSourceAncestors(t *testing.T) {
	home := migrationHome(t)
	realDirectory := filepath.Join(home, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for _, spec := range []storecatalog.Spec{
		{ID: "global/auth", Path: filepath.Join(alias, "auth.db")},
		{
			ID: "global/auth", Path: filepath.Join(home, "auth.db"),
			LegacyRoots: []string{filepath.Join(alias, "legacy")},
		},
	} {
		if selected, err := validateBackupCatalogInputs(
			[]storecatalog.Spec{spec}, []storecatalog.Spec{spec},
		); selected != nil || err == nil || !strings.Contains(err.Error(), "ancestors") {
			t.Fatalf("symlinked source ancestors = %#v, %v", selected, err)
		}
	}
}

func TestArchiveLegacyWalkRejectsPhysicalBackupNamespace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy")
	backupAlias := filepath.Join(root, "ordinary-looking-directory")
	if err := os.MkdirAll(backupAlias, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := parentTreeIdentity(t, backupAlias)
	visited := false
	err := walkLegacyInputsWithPhysicalExclusions(
		t.Context(), root, filepath.Join(t.TempDir(), "archive"), nil,
		map[fileidentity.Identity]struct{}{identity: {}}, newBackupBudget(),
		func(string) error { visited = true; return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "physically contains") || visited {
		t.Fatalf("physical backup alias walk = %v; visited=%t", err, visited)
	}
}

func TestArchiveManifestJSONCloseoutBoundaries(t *testing.T) {
	budget := &backupManifestJSONBudget{tokens: 16}
	total := 1
	if err := backupManifestJSONStringArray(
		json.NewDecoder(strings.NewReader(`["root"]`)), budget, &total, 1,
	); err == nil || !strings.Contains(err.Error(), "legacy-root limit") {
		t.Fatalf("cumulative legacy-root bound = %v", err)
	}
	for _, test := range []struct {
		name, token, want string
	}{
		{"non-string field", `0`, "field is invalid"},
		{"overlong field", `"` + strings.Repeat("x", 33) + `"`, "field is too long"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := backupManifestJSONField(
				json.NewDecoder(strings.NewReader(test.token)),
				&backupManifestJSONBudget{tokens: 2}, map[string]struct{}{},
			)
			requireBackupError(t, err, test.want)
		})
	}
}

func TestArchiveCommitRevalidatesPinnedDirectories(t *testing.T) {
	for _, test := range []struct {
		name, want string
		target     func(*backupSession) string
	}{
		{"parent", "parent before commit", func(session *backupSession) string { return session.parent }},
		{"stage", "stage before commit", func(session *backupSession) string { return session.root }},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newUncommittedBackupArchive(t)
			parentTreeReplaceDirectory(t, test.target(session))
			requireBackupError(t, commitBackupSnapshotWithOps(session, defaultBackupCommitOps()), test.want)
		})
	}
}

func TestArchiveVerificationFailsClosedOnMissingIdentities(t *testing.T) {
	if err := (*backupSession)(nil).verifyFilesWithOps(t.Context(), backupVerifyOps{}); err == nil {
		t.Fatal("nil backup session verified")
	}
	for _, test := range []struct {
		name, want string
		mutate     func(*backupSession, *backupVerifyOps)
	}{
		{"parent provenance", "parent provenance", func(session *backupSession, _ *backupVerifyOps) {
			session.parent = filepath.Join(session.parent, "other")
		}},
		{"root identity", "root identity changed", func(_ *backupSession, ops *backupVerifyOps) {
			ops.identity = func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, 0, false, nil
			}
		}},
		{"ambiguous inventory", "inventory path identity", func(_ *backupSession, ops *backupVerifyOps) {
			ops.exactTree = func(
				context.Context, string, BackupManifest,
			) (map[fileidentity.Identity]string, error) {
				return map[fileidentity.Identity]string{{}: "ambiguous"}, nil
			}
		}},
		{"control identity", "control file identity", func(_ *backupSession, ops *backupVerifyOps) {
			original := ops.exactTree
			ops.exactTree = func(
				ctx context.Context, root string, manifest BackupManifest,
			) (map[fileidentity.Identity]string, error) {
				inventory, err := original(ctx, root, manifest)
				for identity, path := range inventory {
					if path == backupManifestHash {
						delete(inventory, identity)
					}
				}
				return inventory, err
			}
		}},
		{"payload identity", "inventory path identity", func(session *backupSession, _ *backupVerifyOps) {
			session.manifest.Files = []BackupFileManifest{{Backup: "missing-payload"}}
		}},
		{"revalidation unavailable", "revalidation is unavailable", func(_ *backupSession, ops *backupVerifyOps) {
			ops.revalidate = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, ops, _, _ := backupCoreVerifyFixture(t)
			test.mutate(session, &ops)
			requireBackupError(t, session.verifyFilesWithOps(t.Context(), ops), test.want)
		})
	}
	if _, _, err := defaultBackupVerifyOps().read(
		filepath.Join(t.TempDir(), "missing"), backupMaxManifestSize,
	); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing control read = %v", err)
	}
}

func TestArchiveFinalRootAndPayloadRevalidationBoundaries(t *testing.T) {
	root := t.TempDir()
	identity := parentTreeIdentity(t, root)
	other := t.TempDir()
	otherIdentity := parentTreeIdentity(t, other)
	for _, test := range []struct {
		name, path string
		identity   fileidentity.Identity
	}{
		{"invalid", "relative", identity},
		{"missing", filepath.Join(root, "missing-root"), identity},
		{"substituted", root, otherIdentity},
	} {
		t.Run("root "+test.name, func(t *testing.T) {
			if err := validateFinalBackupRoot(test.path, test.identity); err == nil {
				t.Fatal("invalid final root passed revalidation")
			}
		})
	}
	info := mustMigrationInfo(t, root)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := revalidateHashedBackupFiles(
		canceled, root, nil, map[string]os.FileInfo{"payload": info},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled payload revalidation = %v", err)
	}
	if err := revalidateHashedBackupFiles(
		t.Context(), root, nil, map[string]os.FileInfo{"missing": info},
	); err == nil {
		t.Fatal("missing hashed payload passed revalidation")
	}
	if err := revalidateHashedBackupFiles(
		t.Context(), filepath.Join(root, "missing-root"), nil, nil,
	); err == nil {
		t.Fatal("missing archive root passed payload revalidation")
	}
}

func TestArchiveCatalogCloseoutBoundaries(t *testing.T) {
	home := migrationHome(t)
	main := filepath.Join(home, "store.db")
	firstRoot := filepath.Join(home, "first")
	secondRoot := filepath.Join(home, "second")
	base := storecatalog.Spec{ID: "global/base", Path: main, LegacyRoots: []string{firstRoot}}
	other := storecatalog.Spec{ID: "global/other", Path: main, LegacyRoots: []string{secondRoot}}
	for _, test := range []struct {
		name      string
		all, pick []storecatalog.Spec
	}{
		{"relative legacy root", []storecatalog.Spec{{ID: "global/base", Path: main, LegacyRoots: []string{"relative"}}}, []storecatalog.Spec{base}},
		{"duplicate legacy root", []storecatalog.Spec{{ID: "global/base", Path: main, LegacyRoots: []string{firstRoot, firstRoot}}}, []storecatalog.Spec{base}},
		{"generation owned twice", []storecatalog.Spec{base, other}, []storecatalog.Spec{base}},
		{"selected twice", []storecatalog.Spec{base}, []storecatalog.Spec{base, base}},
		{"legacy provenance", []storecatalog.Spec{base}, []storecatalog.Spec{{ID: base.ID, Path: main, LegacyRoots: []string{secondRoot}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if selected, err := validateBackupCatalogInputs(test.all, test.pick); selected != nil || err == nil {
				t.Fatalf("invalid catalog = %#v, %v", selected, err)
			}
		})
	}
}

func TestArchiveLoadRejectsJSONThatCannotDecodeIntoManifest(t *testing.T) {
	parent := migrationHome(t)
	root := filepath.Join(parent, "archive")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, filepath.Join(root, backupManifestName), []byte(`{"version":"wrong type"}`))
	session, err := loadBackupSession(t.Context(), root)
	requireBackupSnapshotError(t, session, err, "decode", nil)
}

type archiveCloseoutSnapshotFixture struct {
	ctx           context.Context
	now           func() time.Time
	home, parent  string
	all, selected []storecatalog.Spec
	ops           backupSnapshotOps
}

func newArchiveCloseoutSnapshotFixture(t *testing.T) *archiveCloseoutSnapshotFixture {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/closeout", Path: filepath.Join(home, "store.db")}
	return &archiveCloseoutSnapshotFixture{
		ctx: context.Background(), now: func() time.Time { return time.Unix(2_000_000_000, 0).UTC() },
		home: home, parent: archiveCloseoutBackupParent(t),
		all: []storecatalog.Spec{spec}, selected: []storecatalog.Spec{spec},
		ops: defaultBackupSnapshotOps(),
	}
}

func (fixture *archiveCloseoutSnapshotFixture) paths() (string, string, string) {
	name := "database-migrate-" + fixture.now().UTC().Format("20060102T150405.000000000Z")
	finalRoot := filepath.Join(fixture.parent, name)
	return finalRoot, finalRoot + backupPartialSuffix, finalRoot + backupStatusSuffix
}

func TestArchiveSnapshotCloseoutFaults(t *testing.T) {
	canary := errors.New("snapshot closeout canary")
	for _, test := range []struct {
		name, want string
		mutate     func(*testing.T, *archiveCloseoutSnapshotFixture)
	}{
		{"nil clock", "clock is unavailable", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			fixture.now = nil
		}},
		{"invalid timestamp", "timestamp is invalid", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			fixture.now = func() time.Time { return time.Time{} }
		}},
		{"custom parent inspection", "custom database backup directory", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, fixture.parent, 1, nil, canary)
		}},
		{"target collision", "already exists", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			finalRoot, _, _ := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, finalRoot, 1, nil, nil)
		}},
		{"target inspection", "inspect database migration backup target", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			finalRoot, _, _ := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, finalRoot, 1, nil, canary)
		}},
		{"stage identity", "stage identity is unavailable", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			_, stageRoot, _ := fixture.paths()
			fixture.ops.identity = backupIdentityFault(fixture.ops.identity, stageRoot, canary)
		}},
		{"canceled selected loop", "context canceled", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			ctx, cancel := context.WithCancel(context.Background())
			fixture.ctx = ctx
			syncDir := fixture.ops.syncDir
			fixture.ops.syncDir = func(path string) error {
				err := syncDir(path)
				cancel()
				return err
			}
		}},
		{"catalog physical alias", "physical alias", func(t *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			first := filepath.Join(fixture.home, "first.db")
			second := filepath.Join(fixture.home, "second.db")
			writeMigrationFile(t, first, []byte("database"))
			if err := os.Link(first, second); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			fixture.all = []storecatalog.Spec{{ID: "global/first", Path: first}, {ID: "global/second", Path: second}}
			fixture.selected = fixture.all[1:]
		}},
		{"orphan sidecar", "sidecar without its database", func(t *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			writeMigrationFile(t, fixture.selected[0].Path+"-wal", []byte("wal"))
		}},
		{"legacy root inspection", "inspect store", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			root := filepath.Join(fixture.home, "legacy")
			fixture.all[0].LegacyRoots = []string{root}
			fixture.selected[0] = fixture.all[0]
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, root, 1, nil, canary)
		}},
		{"legacy root symlink", "legacy root is a symlink", func(t *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			root := filepath.Join(fixture.home, "legacy-link")
			if err := os.Symlink(t.TempDir(), root); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			fixture.all[0].LegacyRoots = []string{root}
			fixture.selected[0] = fixture.all[0]
		}},
		{"legacy root unsafe type", "unsafe type", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			root := filepath.Join(fixture.home, "legacy-special")
			fixture.all[0].LegacyRoots = []string{root}
			fixture.selected[0] = fixture.all[0]
			fixture.ops.lstat = backupPathLstatFault(
				fixture.ops.lstat, root, 1, backupCoreFileInfo{name: "legacy", mode: os.ModeNamedPipe}, nil,
			)
		}},
		{"legacy file physical alias", "legacy inputs contain a physical alias", func(t *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			root := filepath.Join(fixture.home, "legacy")
			first := filepath.Join(root, "first.json")
			second := filepath.Join(root, "second.json")
			writeMigrationFile(t, first, []byte("legacy"))
			writeMigrationFile(t, second, []byte("other"))
			fixture.ops.identity = archiveCloseoutIdentityResult(
				fixture.ops.identity, second, 0, parentTreeIdentity(t, first), true, nil,
			)
			fixture.all[0].LegacyRoots = []string{root}
			fixture.selected[0] = fixture.all[0]
		}},
		{"staged archive corruption", "verify staged database backup", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			commit := fixture.ops.commit
			fixture.ops.commit = func(session *backupSession) error {
				if err := commit(session); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(session.root, backupManifestHash), []byte("bad\n"), 0o600)
			}
		}},
		{"prepublish status collision", "appeared before publication", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			_, _, status := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, status, 2, nil, nil)
		}},
		{"prepublish status inspection", "inspect database migration backup status", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			_, _, status := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, status, 2, nil, canary)
		}},
		{"publish rename", "publish database migration backup", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			fixture.ops.rename = func(string, string) error { return canary }
		}},
		{"postpublish status collision", "appeared during publication", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			_, _, status := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, status, 3, nil, nil)
		}},
		{"postpublish status inspection", "reinspect database migration backup status", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			_, _, status := fixture.paths()
			fixture.ops.lstat = backupPathLstatFault(fixture.ops.lstat, status, 3, nil, canary)
		}},
		{"published parent sync", "sync published database backup parent", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			syncDir := fixture.ops.syncDir
			calls := 0
			fixture.ops.syncDir = func(path string) error {
				calls++
				if err := syncDir(path); err != nil {
					return err
				}
				if calls == 2 {
					return canary
				}
				return nil
			}
		}},
		{"published verification", "verify published database backup", func(_ *testing.T, fixture *archiveCloseoutSnapshotFixture) {
			finalRoot, _, _ := fixture.paths()
			syncDir := fixture.ops.syncDir
			calls := 0
			fixture.ops.syncDir = func(path string) error {
				calls++
				if err := syncDir(path); err != nil {
					return err
				}
				if calls == 2 {
					return os.WriteFile(filepath.Join(finalRoot, backupManifestHash), []byte("bad\n"), 0o600)
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newArchiveCloseoutSnapshotFixture(t)
			test.mutate(t, fixture)
			session, err := snapshotBackupWithOps(
				fixture.ctx, fixture.now, fixture.home, fixture.all, fixture.selected,
				fixture.parent, fixture.ops,
			)
			requireBackupSnapshotError(t, session, err, test.want, nil)
		})
	}
}

func TestArchiveSnapshotRoundTripsRegularLegacyRootWithNilContext(t *testing.T) {
	home := migrationHome(t)
	legacy := filepath.Join(home, "legacy.json")
	writeMigrationFile(t, legacy, []byte("legacy"))
	spec := storecatalog.Spec{
		ID: "global/closeout", Path: filepath.Join(home, "store.db"), LegacyRoots: []string{legacy},
	}
	session, err := snapshotBackupWithOps(
		nil, time.Now, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
		archiveCloseoutBackupParent(t), defaultBackupSnapshotOps(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := session.manifest.Stores[0].LegacyRootKinds; len(got) != 1 || got[0] != "file" {
		t.Fatalf("regular legacy-root kinds = %#v", got)
	}
}
