//go:build unix

package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

type stagedCloseoutAuthority struct {
	checks       int
	failCheck    int
	checkErr     error
	discardErr   error
	pinned       string
	discardCalls int
}

func (authority *stagedCloseoutAuthority) Check(context.Context) error {
	authority.checks++
	if authority.checks == authority.failCheck {
		return authority.checkErr
	}
	return nil
}

func (*stagedCloseoutAuthority) Reconcile(context.Context) error { return nil }

func (authority *stagedCloseoutAuthority) PinReplacement(_ context.Context, path string) error {
	authority.pinned = path
	return nil
}

func (*stagedCloseoutAuthority) CheckReplacement(context.Context, string) error { return nil }

func (authority *stagedCloseoutAuthority) DiscardReplacement(context.Context) error {
	authority.discardCalls++
	return authority.discardErr
}

func (*stagedCloseoutAuthority) ReconcileReplacement(context.Context) error { return nil }

func stagedCloseoutSource(path string) ImmutableGenerationSource {
	return immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, path)
	})
}

func stagedCloseoutOps() stagedMigrationOps {
	return stagedMigrationOps{
		replace:  replaceStagedGeneration,
		activate: activateInstalledGeneration,
	}
}

func TestStagedAuthorizedRejectsEarlyBoundaries(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.db")
	target := filepath.Join(root, "target.db")
	validSource := stagedCloseoutSource(sourcePath)
	validOps := stagedMigrationOps{
		replace:  func(string, string) (bool, error) { return false, nil },
		activate: func(context.Context, string, time.Duration, int) error { return nil },
	}

	if _, err := migrateStagedOfflineAuthorized(
		t.Context(), validSource, target, time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{}, stagedMigrationOps{},
	); err == nil {
		t.Fatal("authorized migration accepted unavailable operations")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := migrateStagedOfflineAuthorized(
		canceled, validSource, target, time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{}, validOps,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("authorized canceled error = %v", err)
	}

	if _, err := migrateStagedOfflineAuthorized(
		t.Context(), validSource, ":memory:", time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{}, validOps,
	); err == nil {
		t.Fatal("authorized migration accepted memory target")
	}

	checkErr := errors.New("initial authority check failed")
	if _, err := migrateStagedOfflineAuthorized(
		t.Context(), validSource, target, time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{failCheck: 1, checkErr: checkErr}, validOps,
	); !errors.Is(err, checkErr) {
		t.Fatalf("initial authority error = %v", err)
	}

	blockedParent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateStagedOfflineAuthorized(
		t.Context(), validSource, filepath.Join(blockedParent, "target.db"), time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{}, validOps,
	); err == nil {
		t.Fatal("authorized migration prepared a directory below a file")
	}

	longTarget := filepath.Join(root, strings.Repeat("x", 230))
	if _, err := migrateStagedOfflineAuthorized(
		t.Context(), validSource, longTarget, time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{}, validOps,
	); err == nil {
		t.Fatal("authorized migration allocated an overlong staged component")
	}
}

func TestStagedAuthorizedJoinsPreCutoverWorkingCleanupError(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	sourceErr := errors.New("source scope failed")
	cleanupErr := errors.New("working cleanup failed")
	source := immutableGenerationSourceForTest(func(
		context.Context,
		func(context.Context, string) error,
	) error {
		return sourceErr
	})

	_, err := migrateStagedOfflineAuthorized(
		t.Context(), source, target, time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		&stagedCloseoutAuthority{},
		stagedMigrationOps{
			replace:  func(string, string) (bool, error) { return false, nil },
			activate: func(context.Context, string, time.Duration, int) error { return nil },
			discard:  func(string, time.Duration) error { return cleanupErr },
		},
	)
	if !errors.Is(err, sourceErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("pre-cutover source/cleanup error = %v", err)
	}
}

func TestMaintainOfflineAuthorizedChecksEveryPhase(t *testing.T) {
	canary := errors.New("phase authority failed")
	ops := maintenanceOps{
		inspect:    func(context.Context, string, time.Duration) (int, error) { return 1, nil },
		boundary:   func(context.Context, string, time.Duration) error { return nil },
		checkpoint: func(context.Context, string, time.Duration) error { return nil },
		reopen:     func(context.Context, string, time.Duration) (int, error) { return 1, nil },
	}
	if _, err := maintainOfflineAuthorized(
		t.Context(), "unused.db", time.Second, &stagedCloseoutAuthority{}, maintenanceOps{},
	); err == nil {
		t.Fatal("authorized maintenance accepted unavailable operations")
	}
	for failAt := 1; failAt <= 4; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			authority := &stagedCloseoutAuthority{failCheck: failAt, checkErr: canary}
			if _, err := maintainOfflineAuthorized(
				t.Context(), "unused.db", time.Second, authority, ops,
			); !errors.Is(err, canary) {
				t.Fatalf("phase %d authority error = %v", failAt, err)
			}
		})
	}
}

func TestStagedAuthorizedChecksAroundNormalizedSourceAndPin(t *testing.T) {
	for _, test := range []struct {
		name       string
		failCheck  int
		discardErr error
	}{
		{name: "before source inspection", failCheck: 2},
		{name: "after source normalization", failCheck: 6},
		{name: "before replacement pin", failCheck: 7},
		{name: "after replacement pin with failed discard", failCheck: 9, discardErr: errors.New("discard failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			sourcePath := filepath.Join(root, "source.db")
			target := filepath.Join(root, "target.db")
			createProviderOfflineFixture(t, sourcePath)
			createProviderOfflineFixture(t, target)
			checkErr := errors.New("authority phase failed")
			authority := &stagedCloseoutAuthority{
				failCheck: test.failCheck, checkErr: checkErr, discardErr: test.discardErr,
			}

			_, err := migrateStagedOfflineAuthorized(
				t.Context(), stagedCloseoutSource(sourcePath), target, 5*time.Second, 1,
				installProviderOfflineFixture, acceptStagedValidation,
				authority, stagedCloseoutOps(),
			)
			if !errors.Is(err, checkErr) {
				t.Fatalf("authority phase error = %v", err)
			}
			if test.discardErr != nil {
				if !errors.Is(err, test.discardErr) ||
					!errors.Is(err, errStagedReplacementRemainsPinned) ||
					authority.pinned == "" {
					t.Fatalf("failed post-pin discard = %v, pinned %q", err, authority.pinned)
				}
				t.Cleanup(func() { _ = os.Remove(authority.pinned) })
			}
		})
	}
}

func TestStagedAuthorizedRejectsIncompleteReplacementWithoutError(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.db")
	target := filepath.Join(root, "target.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	authority := &stagedCloseoutAuthority{}

	_, err := migrateStagedOfflineAuthorized(
		t.Context(), stagedCloseoutSource(sourcePath), target, 5*time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		authority,
		stagedMigrationOps{
			replace:  func(string, string) (bool, error) { return false, nil },
			activate: func(context.Context, string, time.Duration, int) error { return nil },
		},
	)
	if err == nil || !strings.Contains(err.Error(), "did not complete") ||
		authority.discardCalls != 1 {
		t.Fatalf("incomplete replacement error = %v, discards = %d", err, authority.discardCalls)
	}
}

func TestOfflineProviderConsumerPropagatesUnavailableAccessTarget(t *testing.T) {
	source := immutableGenerationSourceForTest(func(
		context.Context,
		func(context.Context, string) error,
	) error {
		t.Fatal("invalid access invoked immutable source")
		return nil
	})
	consumer := func(
		ctx context.Context,
		_ *databaseproviderlease.Lease,
		use func(context.Context, databaseproviderlease.Access) error,
	) error {
		return use(ctx, databaseproviderlease.Access{})
	}
	result, err := migrateStagedOfflineFromWithConsumer(
		t.Context(), nil, source, time.Second, 1,
		func(context.Context, string) error {
			t.Fatal("invalid access invoked migration")
			return nil
		},
		func(context.Context, string) error {
			t.Fatal("invalid access invoked validation")
			return nil
		},
		consumer,
	)
	if err == nil || result.installed || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("unavailable access target result=%#v error=%v", result, err)
	}
}

func TestStagedNamespaceInspectionErrors(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(blocked, "left.db")
	right := filepath.Join(blocked, "right.db")
	overlap, err := generationNamespacesPhysicallyOverlap(
		left, filepath.Join(root, "target.db"),
	)
	if err == nil || overlap {
		t.Fatalf("left namespace inspection = %t, %v", overlap, err)
	}
	regular := filepath.Join(root, "regular.db")
	if err := os.WriteFile(regular, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if overlap, err := generationNamespacesPhysicallyOverlap(regular, right); err == nil || overlap {
		t.Fatalf("right namespace inspection = %t, %v", overlap, err)
	}

	wrapped := immutableSourceOutsideLiveTarget(stagedCloseoutSource(left), filepath.Join(root, "target.db"))
	if err := wrapped.use(t.Context(), func(context.Context, string) error {
		t.Fatal("physical inspection failure invoked source consumer")
		return nil
	}); err == nil {
		t.Fatal("physical source inspection failure was accepted")
	}
}

func TestStagedMigrationFilesystemRaceBoundaries(t *testing.T) {
	root := t.TempDir()
	identityPath := filepath.Join(root, "identity.db")
	if err := os.WriteFile(identityPath, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("filesystem race canary")
	baseFilesystem := func() stagedMigrationFilesystemOps {
		return stagedMigrationFilesystemOps{
			exists: func(string) (bool, error) { return false, nil },
			lstat:  func(string) (os.FileInfo, error) { return identity, nil },
			unused: func(string) (string, error) {
				return filepath.Join(root, "stage.db"), nil
			},
			backup: func(context.Context, string, string, time.Duration) error { return nil },
			validate: func(context.Context, string, time.Duration, int) error {
				return nil
			},
			same:       func(string, os.FileInfo) (bool, error) { return true, nil },
			noSidecars: func(string) error { return nil },
		}
	}
	tests := []struct {
		name   string
		mutate func(*stagedMigrationFilesystemOps)
	}{
		{
			name: "target existence inspection",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				filesystem.exists = func(string) (bool, error) { return false, canary }
			},
		},
		{
			name: "target identity capture",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				filesystem.exists = func(string) (bool, error) { return true, nil }
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "stage allocation",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				filesystem.unused = func(string) (string, error) { return "", canary }
			},
		},
		{
			name: "source existence inspection",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				calls := 0
				filesystem.exists = func(string) (bool, error) {
					calls++
					if calls == 2 {
						return false, canary
					}
					return false, nil
				}
			},
		},
		{
			name: "validated stage identity capture",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				filesystem.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "revalidated stage identity capture",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				calls := 0
				filesystem.lstat = func(string) (os.FileInfo, error) {
					calls++
					if calls == 2 {
						return nil, canary
					}
					return identity, nil
				}
			},
		},
		{
			name: "final stage identity comparison",
			mutate: func(filesystem *stagedMigrationFilesystemOps) {
				calls := 0
				filesystem.same = func(string, os.FileInfo) (bool, error) {
					calls++
					if calls == 2 {
						return false, canary
					}
					return true, nil
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filesystem := baseFilesystem()
			test.mutate(&filesystem)
			err := migrateStagedOfflineWithFilesystem(
				t.Context(), filepath.Join(root, "source.db"), filepath.Join(root, "target.db"),
				time.Second, 1,
				func(context.Context, string) error { return nil },
				acceptStagedValidation,
				stagedMigrationOps{
					replace:  func(string, string) (bool, error) { return true, nil },
					activate: func(context.Context, string, time.Duration, int) error { return nil },
				},
				filesystem,
			)
			if !errors.Is(err, canary) {
				t.Fatalf("filesystem race error = %v", err)
			}
		})
	}
}

func TestMaintenanceAndCutoverCloseoutBranches(t *testing.T) {
	validOps := maintenanceOps{
		inspect:    func(context.Context, string, time.Duration) (int, error) { return 1, nil },
		boundary:   func(context.Context, string, time.Duration) error { return nil },
		checkpoint: func(context.Context, string, time.Duration) error { return nil },
		reopen:     func(context.Context, string, time.Duration) (int, error) { return 1, nil },
	}
	if _, err := maintainOffline(t.Context(), ":memory:", time.Second, validOps); err == nil {
		t.Fatal("offline maintenance accepted memory database")
	}

	canary := errors.New("directory sync failed")
	complete, err := replaceStagedGenerationWithOps(
		"stage.db", filepath.Join("parent", "target.db"),
		func(string, string) error { return nil },
		func(string) error { return canary },
	)
	if !complete || !errors.Is(err, canary) {
		t.Fatalf("post-rename directory sync result = %t, %v", complete, err)
	}

	preCutover := &stagedPreCutoverError{cause: canary}
	if preCutover.Error() == "" || !errors.Is(preCutover, canary) {
		t.Fatalf("pre-cutover wrapper = %v", preCutover)
	}
}
