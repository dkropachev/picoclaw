//go:build (unix && !aix) || windows

package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

type liveStageExclusionFixture struct {
	home    string
	root    string
	stage   string
	spec    storecatalog.Spec
	session *backupSession
}

func newLiveStageExclusionFixture(
	t *testing.T,
	stageBeforeSnapshot bool,
) *liveStageExclusionFixture {
	t.Helper()
	home := migrationHome(t)
	root := filepath.Join(home, "workspace", "repository_reviews")
	spec := storecatalog.Spec{
		ID:          "global/repository-reviews",
		Path:        filepath.Join(root, "repository-reviews.db"),
		LegacyRoots: []string{root},
	}
	writeMigrationFile(t, filepath.Join(root, "profile.json"), []byte("legacy profile"))
	stage := filepath.Join(root, ".repository-reviews.db.migration-stage-exact")
	if stageBeforeSnapshot {
		writeMigrationFile(t, stage, []byte("staged database"))
	}
	session := snapshotLiveSources(
		t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
	)
	if !stageBeforeSnapshot {
		writeMigrationFile(t, stage, []byte("staged database"))
	}
	return &liveStageExclusionFixture{
		home: home, root: root, stage: stage, spec: spec, session: session,
	}
}

func liveStageCheck(t *testing.T, stage string) sqliteprovider.ValidatedReplacementCheck {
	t.Helper()
	expected, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	identity, exists, err := fileidentity.Existing(stage)
	if err != nil || !exists {
		t.Fatalf("capture test stage identity: %v", err)
	}
	return func(ctx context.Context, observed os.FileInfo) (fileidentity.Identity, error) {
		if err := ctx.Err(); err != nil {
			return fileidentity.Identity{}, err
		}
		current, err := os.Lstat(stage)
		if err != nil || observed == nil || current == nil ||
			!os.SameFile(expected, observed) || !os.SameFile(expected, current) {
			return fileidentity.Identity{}, errors.Join(
				errors.New("test replacement stage identity changed"), err,
			)
		}
		return identity, nil
	}
}

func liveTargetParentCheck(
	t *testing.T,
	parent string,
) sqliteprovider.ValidatedTargetParentCheck {
	t.Helper()
	expected, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(parent)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		t.Fatalf("capture test target-parent identity: %v", err)
	}
	return func(ctx context.Context, observed os.FileInfo) (fileidentity.Identity, error) {
		if err := ctx.Err(); err != nil {
			return fileidentity.Identity{}, err
		}
		current, err := os.Lstat(parent)
		if err != nil || observed == nil || current == nil ||
			!os.SameFile(expected, observed) || !os.SameFile(expected, current) ||
			!current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
			return fileidentity.Identity{}, errors.Join(
				errors.New("test target-parent identity changed"),
				err,
			)
		}
		entries, err := os.ReadDir(parent)
		if err != nil || len(entries) != 1 {
			return fileidentity.Identity{}, errors.Join(
				errors.New("test target parent is not sole-entry"),
				err,
			)
		}
		return identity, nil
	}
}

func newMissingLiveTargetParentFixture(t *testing.T) *liveStageExclusionFixture {
	t.Helper()
	home := migrationHome(t)
	root := filepath.Join(home, "workspace", "repository_reviews")
	spec := storecatalog.Spec{
		ID:          "global/repository-reviews",
		Path:        filepath.Join(root, "repository-reviews.db"),
		LegacyRoots: []string{root},
	}
	session := snapshotLiveSources(
		t,
		home,
		[]storecatalog.Spec{spec},
		[]storecatalog.Spec{spec},
	)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, ".repository-reviews.db.migration-stage-exact")
	writeMigrationFile(t, stage, []byte("staged database"))
	return &liveStageExclusionFixture{
		home: home, root: root, stage: stage, spec: spec, session: session,
	}
}

func TestReplacementLiveVerificationAcceptsOnlyProvenCreatedTargetParent(t *testing.T) {
	fixture := newMissingLiveTargetParentFixture(t)
	if err := fixture.session.verifyLiveSourcesForReplacementStageWithOps(
		t.Context(),
		fixture.spec,
		fixture.stage,
		liveStageCheck(t, fixture.stage),
		defaultBackupLiveVerifyOps(),
	); err == nil || !strings.Contains(err.Error(), "layout changed") {
		t.Fatalf("missing-root verification without parent proof = %v", err)
	}
	if err := fixture.session.verifyLiveSourcesForReplacementStageAndParentWithOps(
		t.Context(),
		fixture.spec,
		fixture.stage,
		liveStageCheck(t, fixture.stage),
		liveTargetParentCheck(t, fixture.root),
		defaultBackupLiveVerifyOps(),
	); err != nil {
		t.Fatalf("proven provider-created target parent = %v", err)
	}
}

func TestReplacementPreexistingEmptyTargetParentUsesOrdinaryVerification(t *testing.T) {
	home := migrationHome(t)
	root := filepath.Join(home, "workspace", "repository_reviews")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := storecatalog.Spec{
		ID:          "global/repository-reviews",
		Path:        filepath.Join(root, "repository-reviews.db"),
		LegacyRoots: []string{root},
	}
	session := snapshotLiveSources(
		t, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
	)
	stage := filepath.Join(root, ".repository-reviews.db.migration-stage-exact")
	writeMigrationFile(t, stage, []byte("staged database"))
	if err := session.verifyLiveSourcesForReplacementStageWithOps(
		t.Context(), spec, stage, liveStageCheck(t, stage), defaultBackupLiveVerifyOps(),
	); err != nil {
		t.Fatalf("ordinary preexisting empty target parent = %v", err)
	}
}

func TestReplacementCreatedTargetParentRejectsEveryExtraEntryClass(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, string)
	}{
		{name: "file", create: func(t *testing.T, path string) {
			writeMigrationFile(t, path, []byte("extra"))
		}},
		{name: "empty directory", create: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "normally skipped legacy-json", create: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "normally skipped backups", create: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "normally skipped state", create: func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "near stage", create: func(t *testing.T, path string) {
			writeMigrationFile(t, path, []byte("near"))
		}},
		{name: "sidecar", create: func(t *testing.T, path string) {
			writeMigrationFile(t, path, []byte("sidecar"))
		}},
		{name: "symlink", create: func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing"), path); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMissingLiveTargetParentFixture(t)
			name := map[string]string{
				"file":                         "extra.json",
				"empty directory":              "empty",
				"normally skipped legacy-json": "legacy-json",
				"normally skipped backups":     "backups",
				"normally skipped state":       ".picoclaw",
				"near stage":                   filepath.Base(fixture.stage) + "-near",
				"sidecar":                      filepath.Base(fixture.stage) + "-wal",
				"symlink":                      "alias",
			}[test.name]
			test.create(t, filepath.Join(fixture.root, name))
			ops := defaultBackupLiveVerifyOps()
			// This stub checks only parent identity so the engine's strict walker,
			// not the test capability, must reject the extra entry.
			identity, _, _, err := fileidentity.ExistingWithType(fixture.root)
			if err != nil {
				t.Fatal(err)
			}
			parentCheck := func(context.Context, os.FileInfo) (fileidentity.Identity, error) {
				return identity, nil
			}
			if err := fixture.session.verifyLiveSourcesForReplacementStageAndParentWithOps(
				t.Context(), fixture.spec, fixture.stage,
				liveStageCheck(t, fixture.stage), parentCheck, ops,
			); err == nil || !strings.Contains(err.Error(), "unexpected entry") {
				t.Fatalf("created parent extra %s = %v", test.name, err)
			}
		})
	}
}

func TestReplacementLiveVerificationExcludesOnlyExactStage(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	if err := fixture.session.verifyLiveSources(t.Context(), fixture.spec); err == nil ||
		!strings.Contains(err.Error(), "added after snapshot") {
		t.Fatalf("ordinary verification with staged target = %v", err)
	}
	if err := fixture.session.verifyLiveSourcesForReplacementStageWithOps(
		t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
		defaultBackupLiveVerifyOps(),
	); err != nil {
		t.Fatalf("exact staged-target exclusion = %v", err)
	}
}

func TestReplacementLiveVerificationDoesNotExcludeStageSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(strings.TrimPrefix(suffix, "-"), func(t *testing.T) {
			fixture := newLiveStageExclusionFixture(t, false)
			writeMigrationFile(t, fixture.stage+suffix, []byte("unapproved sidecar"))
			if err := fixture.session.verifyLiveSourcesForStageWithOps(
				t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
				defaultBackupLiveVerifyOps(),
			); err == nil || !strings.Contains(err.Error(), "added after snapshot") {
				t.Fatalf("staged-target%s exclusion = %v", suffix, err)
			}
		})
	}
}

func TestReplacementLiveVerificationDoesNotWidenStageNameExclusion(t *testing.T) {
	for _, name := range []string{
		".repository-reviews.db.migration-stage-exact-near",
		"second-unrelated-stage.db",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newLiveStageExclusionFixture(t, false)
			writeMigrationFile(t, filepath.Join(fixture.root, name), []byte("other input"))
			if err := fixture.session.verifyLiveSourcesForStageWithOps(
				t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
				defaultBackupLiveVerifyOps(),
			); err == nil || !strings.Contains(err.Error(), "added after snapshot") {
				t.Fatalf("nearby staged target exclusion = %v", err)
			}
		})
	}
}

func TestReplacementLiveVerificationRejectsStagePhysicalAlias(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	alias := filepath.Join(fixture.root, "stage-hardlink-alias.db")
	if err := os.Link(fixture.stage, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := fixture.session.verifyLiveSourcesForStageWithOps(
		t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
		defaultBackupLiveVerifyOps(),
	); err == nil || !strings.Contains(err.Error(), "physical alias") {
		t.Fatalf("staged-target physical alias = %v", err)
	}
}

func TestReplacementLiveVerificationRejectsCatalogIdentityAliasBeforeWalk(t *testing.T) {
	home := migrationHome(t)
	root := filepath.Join(home, "workspace", "repository_reviews")
	spec := storecatalog.Spec{
		ID:          "global/repository-reviews",
		Path:        filepath.Join(root, "repository-reviews.db"),
		LegacyRoots: []string{root},
	}
	other := storecatalog.Spec{
		ID:   "global/other",
		Path: filepath.Join(home, "other.db"),
	}
	writeMigrationFile(t, filepath.Join(root, "profile.json"), []byte("legacy profile"))
	writeMigrationFile(t, other.Path, []byte("catalog generation"))
	session := snapshotLiveSources(
		t, home, []storecatalog.Spec{spec, other}, []storecatalog.Spec{spec},
	)
	stage := filepath.Join(root, ".repository-reviews.db.migration-stage-exact")
	if err := os.Link(other.Path, stage); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	if err := session.verifyLiveSourcesForStageWithOps(
		t.Context(), spec, stage, liveStageCheck(t, stage), defaultBackupLiveVerifyOps(),
	); err == nil || !strings.Contains(err.Error(), "physical alias") {
		t.Fatalf("catalog-aliased exact stage = %v", err)
	}
}

func TestReplacementLiveVerificationRejectsStageSwapAtExactSkip(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	check := liveStageCheck(t, fixture.stage)
	ops := defaultBackupLiveVerifyOps()
	originalWalk := ops.walkLegacyExact
	ops.walkLegacyExact = func(
		ctx context.Context,
		root string,
		backupRoot string,
		excluded map[string]struct{},
		exact *legacyExactExclusion,
		budget *backupBudget,
		visit func(string) error,
	) error {
		original := fixture.stage + ".held"
		if err := os.Rename(fixture.stage, original); err != nil {
			return err
		}
		if err := os.WriteFile(fixture.stage, []byte("staged database"), 0o600); err != nil {
			_ = os.Rename(original, fixture.stage)
			return err
		}
		walkErr := originalWalk(ctx, root, backupRoot, excluded, exact, budget, visit)
		removeErr := os.Remove(fixture.stage)
		restoreErr := os.Rename(original, fixture.stage)
		return errors.Join(walkErr, removeErr, restoreErr)
	}
	if err := fixture.session.verifyLiveSourcesForStageWithOps(
		t.Context(), fixture.spec, fixture.stage, check, ops,
	); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("swapped exact staged-target exclusion = %v", err)
	}
}

func TestReplacementLiveVerificationTreatsCaseVariantAsOrdinaryInput(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	variant := filepath.Join(
		fixture.root,
		strings.ToUpper(filepath.Base(fixture.stage)),
	)
	if variant == fixture.stage {
		t.Fatal("case-variant fixture did not change the exact path")
	}
	if _, err := os.Lstat(variant); err == nil {
		t.Skip("filesystem does not expose case-distinct pathnames")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	writeMigrationFile(t, variant, []byte("case-distinct input"))
	if err := fixture.session.verifyLiveSourcesForStageWithOps(
		t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
		defaultBackupLiveVerifyOps(),
	); err == nil || !strings.Contains(err.Error(), "added after snapshot") {
		t.Fatalf("case-variant staged-target exclusion = %v", err)
	}
}

func TestLegacyExactExclusionValidatesRootFileOnEveryEncounter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exact-stage.db")
	writeMigrationFile(t, root, []byte("stage"))
	expected, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	exact := &legacyExactExclusion{
		path: root,
		validate: func(_ context.Context, path string, observed os.FileInfo) error {
			calls++
			if path != root || observed == nil || !os.SameFile(expected, observed) {
				return errors.New("exact root observation changed")
			}
			return nil
		},
	}
	visits := 0
	for range 2 {
		if err := walkLegacyInputsWithExactExclusion(
			t.Context(), root, filepath.Join(t.TempDir(), "backup"), nil, exact,
			newBackupBudget(), func(string) error { visits++; return nil },
		); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 || exact.seen != 2 || visits != 0 {
		t.Fatalf("exact root calls=%d seen=%d visits=%d", calls, exact.seen, visits)
	}
}

func TestLegacyExactExclusionFailureBranches(t *testing.T) {
	canary := errors.New("exact exclusion canary")

	t.Run("root budget", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "stage.db")
		writeMigrationFile(t, root, []byte("stage"))
		err := walkLegacyInputsWithExactExclusion(
			t.Context(), root, filepath.Join(t.TempDir(), "backup"), nil,
			&legacyExactExclusion{path: root, validate: func(
				context.Context, string, os.FileInfo,
			) error {
				return nil
			}},
			nil,
			func(string) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "budget") {
			t.Fatalf("exact root nil budget = %v", err)
		}
	})

	for _, test := range []struct {
		name     string
		validate func(context.Context, string, os.FileInfo) error
		want     string
	}{
		{name: "root validator missing", want: "validation is unavailable"},
		{
			name: "root validator error",
			validate: func(context.Context, string, os.FileInfo) error {
				return canary
			},
			want: canary.Error(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "stage.db")
			writeMigrationFile(t, root, []byte("stage"))
			err := walkLegacyInputsWithExactExclusion(
				t.Context(), root, filepath.Join(t.TempDir(), "backup"), nil,
				&legacyExactExclusion{path: root, validate: test.validate},
				newBackupBudget(), func(string) error { return nil },
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s = %v", test.name, err)
			}
		})
	}

	for _, test := range []struct {
		name     string
		budget   func() *backupBudget
		validate func(context.Context, string, os.FileInfo) error
		want     string
	}{
		{
			name: "child budget",
			budget: func() *backupBudget {
				budget := newBackupBudget()
				budget.maxEntries = 1
				return budget
			},
			validate: func(context.Context, string, os.FileInfo) error { return nil },
			want:     "entry limit",
		},
		{
			name:   "child validator missing",
			budget: newBackupBudget,
			want:   "validation is unavailable",
		},
		{
			name:   "child validator error",
			budget: newBackupBudget,
			validate: func(context.Context, string, os.FileInfo) error {
				return canary
			},
			want: canary.Error(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, "stage.db")
			writeMigrationFile(t, stage, []byte("stage"))
			err := walkLegacyInputsWithExactExclusion(
				t.Context(), root, filepath.Join(t.TempDir(), "backup"), nil,
				&legacyExactExclusion{path: stage, validate: test.validate},
				test.budget(), func(string) error { return nil },
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s = %v", test.name, err)
			}
		})
	}
}

func TestReplacementLiveVerificationRejectsInvalidExactWalker(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	baseCheck := liveStageCheck(t, fixture.stage)

	t.Run("path mismatch", func(t *testing.T) {
		ops := defaultBackupLiveVerifyOps()
		ops.walkLegacyExact = func(
			ctx context.Context,
			_ string,
			_ string,
			_ map[string]struct{},
			exact *legacyExactExclusion,
			_ *backupBudget,
			_ func(string) error,
		) error {
			info, err := os.Lstat(fixture.stage)
			if err != nil {
				return err
			}
			return exact.validate(ctx, fixture.stage+".other", info)
		}
		err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, baseCheck, ops,
		)
		if err == nil || !strings.Contains(err.Error(), "path changed") {
			t.Fatalf("mismatched exact walker path = %v", err)
		}
	})

	t.Run("identity mismatch", func(t *testing.T) {
		other := filepath.Join(fixture.root, "other-identity.db")
		writeMigrationFile(t, other, []byte("other"))
		otherIdentity, exists, err := fileidentity.Existing(other)
		if err != nil || !exists {
			t.Fatal(err)
		}
		calls := 0
		check := func(ctx context.Context, observed os.FileInfo) (fileidentity.Identity, error) {
			calls++
			if calls == 1 {
				return baseCheck(ctx, observed)
			}
			return otherIdentity, nil
		}
		err = fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, check, defaultBackupLiveVerifyOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "identity changed") {
			t.Fatalf("mismatched exact walker identity = %v", err)
		}
		if err := os.Remove(other); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stage omitted", func(t *testing.T) {
		ops := defaultBackupLiveVerifyOps()
		ops.walkLegacyExact = func(
			ctx context.Context,
			root string,
			backupRoot string,
			excluded map[string]struct{},
			_ *legacyExactExclusion,
			budget *backupBudget,
			visit func(string) error,
		) error {
			omitted := make(map[string]struct{}, len(excluded)+1)
			for key := range excluded {
				omitted[key] = struct{}{}
			}
			omitted[backupPathKey(fixture.stage)] = struct{}{}
			return walkLegacyInputs(ctx, root, backupRoot, omitted, budget, visit)
		}
		err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, baseCheck, ops,
		)
		if err == nil || !strings.Contains(err.Error(), "was not observed") {
			t.Fatalf("omitted exact stage = %v", err)
		}
	})

	t.Run("state construction", func(t *testing.T) {
		broken := *fixture.session
		broken.manifest.CatalogGenerations = nil
		err := broken.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, baseCheck,
			defaultBackupLiveVerifyOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "catalog generation exclusions") {
			t.Fatalf("invalid exact-stage state = %v", err)
		}
	})

	t.Run("backup verification", func(t *testing.T) {
		broken := *fixture.session
		broken.root = filepath.Join(t.TempDir(), "missing-backup")
		err := broken.verifyLiveSourcesForReplacementStageWithOps(
			t.Context(), fixture.spec, fixture.stage, baseCheck,
			defaultBackupLiveVerifyOps(),
		)
		if err == nil || !strings.Contains(err.Error(), "verify database backup") {
			t.Fatalf("invalid replacement backup = %v", err)
		}
	})
}

func TestReplacementLiveVerificationRejectsRecordedStageCollision(t *testing.T) {
	t.Run("catalog generation", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, false)
		fixture.session.manifest.CatalogGenerations = append(
			fixture.session.manifest.CatalogGenerations,
			fixture.stage,
		)
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
			defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "collides with a catalog generation") {
			t.Fatalf("cataloged staged-target exclusion = %v", err)
		}
	})

	t.Run("legacy input", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, true)
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, liveStageCheck(t, fixture.stage),
			defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "collides with a legacy input") {
			t.Fatalf("manifested legacy staged-target exclusion = %v", err)
		}
	})
}

func TestReplacementLiveVerificationRejectsUnsafeStageInputs(t *testing.T) {
	t.Run("relative", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, false)
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, "relative-stage.db", liveStageCheck(t, fixture.stage),
			defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "stage is invalid") {
			t.Fatalf("relative staged-target exclusion = %v", err)
		}
	})

	t.Run("non-regular", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, false)
		directory := filepath.Join(fixture.root, "stage-directory")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, directory, liveStageCheck(t, fixture.stage),
			defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "stage is unsafe") {
			t.Fatalf("directory staged-target exclusion = %v", err)
		}
	})

	t.Run("identity unavailable", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, false)
		canary := errors.New("stage identity canary")
		check := func(context.Context, os.FileInfo) (fileidentity.Identity, error) {
			return fileidentity.Identity{}, canary
		}
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, check, defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "identity is unavailable") ||
			!errors.Is(err, canary) {
			t.Fatalf("unavailable staged-target identity = %v", err)
		}
	})

	t.Run("invalid identity", func(t *testing.T) {
		fixture := newLiveStageExclusionFixture(t, false)
		check := func(context.Context, os.FileInfo) (fileidentity.Identity, error) {
			return fileidentity.Identity{}, nil
		}
		if err := fixture.session.verifyLiveSourcesForStageWithOps(
			t.Context(), fixture.spec, fixture.stage, check, defaultBackupLiveVerifyOps(),
		); err == nil || !strings.Contains(err.Error(), "identity is unavailable") {
			t.Fatalf("invalid staged-target identity = %v", err)
		}
	})
}

func TestReplacementCapabilityRunsExactStageLiveVerification(t *testing.T) {
	fixture := newLiveStageExclusionFixture(t, false)
	if err := os.Remove(fixture.stage); err != nil {
		t.Fatal(err)
	}
	source, cleanup, err := fixture.session.prepareImmutableGenerationSource(
		t.Context(), fixture.spec,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			t.Errorf("clean prepared replacement source: %v", cleanupErr)
		}
	}()

	var pinned string
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	lease, err := databaseproviderlease.New(
		parent,
		fixture.spec.ID,
		fixture.spec.Path,
		databaseproviderlease.Hooks{
			Check:     func(context.Context) error { return nil },
			Reconcile: func(context.Context) error { return nil },
			PinReplacement: func(_ context.Context, path string) error {
				pinned = filepath.Clean(path)
				return nil
			},
			CheckReplacement: func(_ context.Context, path string) error {
				if pinned == "" || filepath.Clean(path) != pinned {
					return errors.New("replacement pin changed")
				}
				return nil
			},
			DiscardReplacement: func(context.Context) error {
				pinned = ""
				return nil
			},
			ReconcileReplacement: func(context.Context) error {
				pinned = ""
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	callbackCalls := 0
	var ordinaryErr error
	result, err := sqliteprovider.MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		func(ctx context.Context, stage string) (returnErr error) {
			database, openErr := sqliteprovider.OpenStore(stage, time.Second)
			if openErr != nil {
				return openErr
			}
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
			if configureErr := sqliteprovider.ConfigureOffline(
				ctx, database, time.Second,
			); configureErr != nil {
				return configureErr
			}
			if _, execErr := database.ExecContext(
				ctx,
				"CREATE TABLE reviewed_repository(id TEXT PRIMARY KEY) STRICT",
			); execErr != nil {
				return execErr
			}
			return sqliteprovider.SetSchemaVersion(ctx, database, 1)
		},
		func(ctx context.Context, stage string) error {
			inspection, inspectErr := sqliteprovider.Inspect(ctx, stage, time.Second)
			if inspectErr != nil {
				return inspectErr
			}
			ready, contractErr := inspection.HasSchemaObjects(
				ctx, "table", "reviewed_repository",
			)
			releaseErr := inspection.Release()
			if contractErr != nil || releaseErr != nil {
				return errors.Join(contractErr, releaseErr)
			}
			if !ready {
				return errors.New("replacement schema is incomplete")
			}
			return nil
		},
		func(
			ctx context.Context,
			replacement sqliteprovider.ValidatedReplacement,
		) error {
			callbackCalls++
			ordinaryErr = fixture.session.verifyLiveSources(ctx, fixture.spec)
			return fixture.session.verifyLiveSourcesForReplacement(
				ctx, fixture.spec, replacement,
			)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if callbackCalls != 1 {
		t.Fatalf("replacement live-verification callbacks = %d, want 1", callbackCalls)
	}
	if ordinaryErr == nil || !strings.Contains(ordinaryErr.Error(), "added after snapshot") {
		t.Fatalf("ordinary callback verification = %v", ordinaryErr)
	}
	if result.BeforeVersion != 0 || result.AfterVersion != 1 {
		t.Fatalf("replacement maintenance result = %#v", result)
	}
	if _, statErr := os.Lstat(fixture.spec.Path); statErr != nil {
		t.Fatalf("installed replacement target: %v", statErr)
	}
}
