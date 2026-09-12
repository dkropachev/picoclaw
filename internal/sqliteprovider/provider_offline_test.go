package sqliteprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

type offlineProviderHookRecorder struct {
	mu       sync.Mutex
	events   []string
	failures map[string]error
	pinned   string
}

func (recorder *offlineProviderHookRecorder) record(event string) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return recorder.failures[event]
}

func (recorder *offlineProviderHookRecorder) snapshot() ([]string, string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...), recorder.pinned
}

func newOfflineProviderTestLease(
	t *testing.T,
	target string,
	recorder *offlineProviderHookRecorder,
) *databaseproviderlease.Lease {
	t.Helper()
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	lease, err := databaseproviderlease.New(
		parent,
		dblayer.StoreID("global/auth"),
		target,
		databaseproviderlease.Hooks{
			Check: func(context.Context) error {
				return recorder.record("check")
			},
			Reconcile: func(context.Context) error {
				return recorder.record("reconcile")
			},
			PinReplacement: func(_ context.Context, path string) error {
				recorder.mu.Lock()
				recorder.pinned = path
				recorder.mu.Unlock()
				return recorder.record("pin")
			},
			CheckReplacement: func(context.Context, string) error {
				return recorder.record("check-replacement")
			},
			DiscardReplacement: func(context.Context) error {
				return recorder.record("discard")
			},
			ReconcileReplacement: func(context.Context) error {
				return recorder.record("reconcile-replacement")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func TestMigrateStagedOfflineFromUsesSealedSourceAndDerivedTarget(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	var sourceCalls, useCalls int
	var retained func(context.Context, string) error
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		sourceCalls++
		retained = use
		useCalls++
		return use(ctx, sourcePath)
	})

	result, err := MigrateStagedOfflineFrom(
		t.Context(),
		lease,
		source,
		5*time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeVersion != 1 || result.AfterVersion != 1 {
		t.Fatalf("maintenance result = %#v", result)
	}
	if sourceCalls != 1 || useCalls != 1 {
		t.Fatalf("immutable source calls = outer:%d use:%d", sourceCalls, useCalls)
	}
	if retained == nil || retained(t.Context(), sourcePath) == nil {
		t.Fatal("immutable source callback remained usable after its synchronous scope")
	}
	events, pinned := recorder.snapshot()
	assertProviderOfflineCutoverOrder(t, events)
	if filepath.Dir(pinned) != filepath.Dir(target) ||
		!strings.HasPrefix(filepath.Base(pinned), "."+filepath.Base(target)+".migration-stage-") {
		t.Fatalf("pinned stage %q is not derived from target %q", pinned, target)
	}
	if !providerOfflineTableExists(t, target, "installed") {
		t.Fatal("derived lease target did not receive staged generation")
	}
	if providerOfflineTableExists(t, sourcePath, "installed") {
		t.Fatal("sealed source was opened for migration or replaced")
	}
	if _, secondErr := MigrateStagedOfflineFrom(
		t.Context(), lease, source, time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	); dblayer.CodeOf(secondErr) != dblayer.CodeConflict {
		t.Fatalf("second lease consumption = %v", secondErr)
	}
}

func TestMigrateStagedOfflineFromLiveVerificationSeesOnlyPinnedReplacement(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(),
		lease,
		source,
		5*time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				_ context.Context, stage string, _ ValidatedReplacementCheck,
			) error {
				entries, readErr := os.ReadDir(home)
				if readErr != nil {
					return readErr
				}
				stages := make([]string, 0, 2)
				for _, entry := range entries {
					if strings.Contains(entry.Name(), ".migration-stage-") {
						stages = append(stages, filepath.Join(home, entry.Name()))
					}
				}
				if len(stages) != 1 || stages[0] != stage {
					return fmt.Errorf("live verification stages = %v, want [%s]", stages, stage)
				}
				return recorder.record("live-verification")
			})
		},
	)
	if err != nil || !result.installed {
		t.Fatalf("verified migration result=%#v error=%v", result, err)
	}
	events, _ := recorder.snapshot()
	pin := indexProviderOfflineEvent(events, "pin")
	pinCheck := indexProviderOfflineEvent(events, "check-replacement")
	live := indexProviderOfflineEvent(events, "live-verification")
	reconciled := indexProviderOfflineEvent(events, "reconcile-replacement")
	if pin < 0 || pinCheck <= pin || live <= pinCheck || reconciled <= live {
		t.Fatalf("verified replacement event order = %v", events)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".migration-stage-") {
			t.Fatalf("provider stage remained after successful cutover: %s", entry.Name())
		}
	}
}

func TestMigrateStagedOfflineFromLiveVerificationSupportsMissingTarget(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "missing-backup.db")
	target := filepath.Join(home, "live.db")
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	verified := false
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				_ context.Context, stage string, _ ValidatedReplacementCheck,
			) error {
				verified = true
				if stage == sourcePath {
					return errors.New("missing immutable source became the replacement stage")
				}
				return nil
			})
		},
	)
	if err != nil || !verified || !result.installed || result.BeforeVersion != 0 ||
		result.AfterVersion != 1 {
		t.Fatalf("missing-target result=%#v verified=%t error=%v", result, verified, err)
	}
	if !providerOfflineTableExists(t, target, "installed") {
		t.Fatal("missing target did not receive its verified replacement")
	}
}

func TestMigrateStagedOfflineFromLiveVerificationFailureDiscardsPin(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	canary := errors.New("live source drift")
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				context.Context, string, ValidatedReplacementCheck,
			) error {
				return canary
			})
		},
	)
	if !errors.Is(err, canary) || result.installed ||
		dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("failed live verification result=%#v error=%v", result, err)
	}
	events, pinned := recorder.snapshot()
	if indexProviderOfflineEvent(events, "discard") <=
		indexProviderOfflineEvent(events, "check-replacement") ||
		indexProviderOfflineEvent(events, "reconcile-replacement") >= 0 {
		t.Fatalf("failed live verification events = %v", events)
	}
	if _, statErr := os.Lstat(pinned); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed live-verification stage remains at %q: %v", pinned, statErr)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed live verification changed target: equal=%t error=%v", bytes.Equal(before, after), err)
	}
}

func TestMigrateStagedOfflineFromRejectsPostValidationPrePinContentMutation(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	mutated := false
	lease, err := databaseproviderlease.New(
		parent,
		"global/auth",
		target,
		databaseproviderlease.Hooks{
			Check:     func(context.Context) error { return recorder.record("check") },
			Reconcile: func(context.Context) error { return recorder.record("reconcile") },
			PinReplacement: func(_ context.Context, path string) error {
				recorder.mu.Lock()
				recorder.pinned = path
				recorder.mu.Unlock()
				return recorder.record("pin")
			},
			CheckReplacement: func(_ context.Context, path string) error {
				if !mutated {
					info, statErr := os.Lstat(path)
					payload, readErr := os.ReadFile(path)
					if statErr != nil || readErr != nil || len(payload) == 0 {
						return errors.Join(statErr, readErr, errors.New("empty replacement fixture"))
					}
					payload[len(payload)-1] ^= 0xff
					if writeErr := os.WriteFile(path, payload, info.Mode().Perm()); writeErr != nil {
						return writeErr
					}
					if timeErr := os.Chtimes(path, time.Now(), info.ModTime()); timeErr != nil {
						return timeErr
					}
					mutated = true
				}
				return recorder.record("check-replacement")
			},
			DiscardReplacement: func(context.Context) error {
				return recorder.record("discard")
			},
			ReconcileReplacement: func(context.Context) error {
				return recorder.record("reconcile-replacement")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	liveCalled := false
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(context.Context, ValidatedReplacement) error {
			liveCalled = true
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "contents changed after validation") ||
		result.installed || liveCalled || !mutated {
		t.Fatalf(
			"post-validation mutation result=%#v mutated=%t live=%t error=%v",
			result, mutated, liveCalled, err,
		)
	}
}

func TestMigrateStagedOfflineFromRejectsRenamedNormalizedSource(t *testing.T) {
	home := t.TempDir()
	escapeRoot := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	escaped := filepath.Join(escapeRoot, "escaped-working.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	var inner string
	renamed := false
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	lease, err := databaseproviderlease.New(
		parent,
		"global/auth",
		target,
		databaseproviderlease.Hooks{
			Check: func(context.Context) error {
				if inner != "" && !renamed {
					stages, globErr := filepath.Glob(filepath.Join(home, ".*.migration-stage-*.db"))
					if globErr != nil {
						return globErr
					}
					for _, stage := range stages {
						if filepath.Clean(stage) == filepath.Clean(inner) {
							continue
						}
						if renameErr := os.Rename(stage, escaped); renameErr != nil {
							return renameErr
						}
						renamed = true
						break
					}
				}
				return recorder.record("check")
			},
			Reconcile: func(context.Context) error { return recorder.record("reconcile") },
			PinReplacement: func(_ context.Context, path string) error {
				recorder.mu.Lock()
				recorder.pinned = path
				recorder.mu.Unlock()
				return recorder.record("pin")
			},
			CheckReplacement: func(context.Context, string) error {
				return recorder.record("check-replacement")
			},
			DiscardReplacement: func(context.Context) error {
				return recorder.record("discard")
			},
			ReconcileReplacement: func(context.Context) error {
				return recorder.record("reconcile-replacement")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		func(ctx context.Context, stage string) error {
			inner = stage
			return installProviderOfflineFixture(ctx, stage)
		},
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				context.Context, string, ValidatedReplacementCheck,
			) error {
				return nil
			})
		},
	)
	if err == nil || !renamed || result.installed ||
		!strings.Contains(err.Error(), "retained SQLite stage path") {
		t.Fatalf("renamed working result=%#v renamed=%t error=%v", result, renamed, err)
	}
	if _, statErr := os.Lstat(escaped); statErr != nil {
		t.Fatalf("escaped working identity was not retained: %v", statErr)
	}
	if events, _ := recorder.snapshot(); indexProviderOfflineEvent(events, "pin") >= 0 {
		t.Fatalf("renamed working source reached replacement pin: %v", events)
	}
}

func TestStagedProviderDoesNotPathCleanDecoyAfterRetainedCopyFailure(t *testing.T) {
	home := t.TempDir()
	escapeRoot := t.TempDir()
	target := filepath.Join(home, "live.db")
	escaped := filepath.Join(escapeRoot, "escaped-partial-copy.db")
	canary := errors.New("retained copy failure")
	var decoy string
	authority := offlineProviderTestAuthority{recorder: &offlineProviderHookRecorder{}}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, filepath.Join(home, "unused-source.db"))
	})
	result, err := migrateStagedOfflineAuthorizedWithLiveVerification(
		t.Context(), source, target, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(context.Context, ValidatedReplacement) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) { return true, nil },
			activate: func(context.Context, string, time.Duration, int) error {
				return nil
			},
			copySource: func(
				ctx context.Context,
				_ ImmutableGenerationSource,
				stage string,
			) (bool, *retainedStagedGeneration, error) {
				if err := installProviderOfflineFixture(ctx, stage); err != nil {
					return false, nil, err
				}
				if err := os.Rename(stage, escaped); err != nil {
					return false, nil, err
				}
				decoy = stage
				if err := installProviderOfflineFixture(ctx, decoy); err != nil {
					return false, nil, err
				}
				return false, nil, canary
			},
		},
	)
	if !errors.Is(err, canary) || result.installed {
		t.Fatalf("retained copy failure result=%#v error=%v", result, err)
	}
	for name, path := range map[string]string{"escaped": escaped, "decoy": decoy} {
		if _, statErr := os.Lstat(path); statErr != nil {
			t.Fatalf("%s partial-copy object was removed: %v", name, statErr)
		}
	}
}

func TestMigrateStagedOfflineFromDoesNotDeleteDecoyAfterDiscardedStageRename(t *testing.T) {
	home := t.TempDir()
	escapeRoot := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	escaped := filepath.Join(escapeRoot, "escaped-final.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	renamed := false
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	lease, err := databaseproviderlease.New(
		parent,
		"global/auth",
		target,
		databaseproviderlease.Hooks{
			Check:     func(context.Context) error { return recorder.record("check") },
			Reconcile: func(context.Context) error { return recorder.record("reconcile") },
			PinReplacement: func(_ context.Context, path string) error {
				recorder.mu.Lock()
				recorder.pinned = path
				recorder.mu.Unlock()
				return recorder.record("pin")
			},
			CheckReplacement: func(context.Context, string) error {
				return recorder.record("check-replacement")
			},
			DiscardReplacement: func(context.Context) error {
				recorder.mu.Lock()
				stage := recorder.pinned
				recorder.mu.Unlock()
				if stage != "" && !renamed {
					if renameErr := os.Rename(stage, escaped); renameErr != nil {
						return renameErr
					}
					if writeErr := os.WriteFile(stage, []byte("decoy"), 0o600); writeErr != nil {
						return writeErr
					}
					renamed = true
				}
				return recorder.record("discard")
			},
			ReconcileReplacement: func(context.Context) error {
				return recorder.record("reconcile-replacement")
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	canary := errors.New("live verification canary")
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				context.Context, string, ValidatedReplacementCheck,
			) error {
				return canary
			})
		},
	)
	if !errors.Is(err, canary) || !renamed || result.installed {
		t.Fatalf("renamed final result=%#v renamed=%t error=%v", result, renamed, err)
	}
	if _, statErr := os.Lstat(escaped); statErr != nil {
		t.Fatalf("escaped final identity was not retained: %v", statErr)
	}
	_, stage := recorder.snapshot()
	if payload, readErr := os.ReadFile(stage); readErr != nil || string(payload) != "decoy" {
		t.Fatalf("replacement decoy changed = %q, %v", payload, readErr)
	}
}

func TestMigrateStagedOfflineFromRetainsStageRenamedByMigrationCallback(t *testing.T) {
	home := t.TempDir()
	escapeRoot := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	escaped := filepath.Join(escapeRoot, "escaped-callback-stage.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	canary := errors.New("migration callback canary")
	var decoy string
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		func(_ context.Context, stage string) error {
			if renameErr := os.Rename(stage, escaped); renameErr != nil {
				return renameErr
			}
			decoy = stage
			if writeErr := os.WriteFile(decoy, []byte("decoy"), 0o600); writeErr != nil {
				return writeErr
			}
			return canary
		},
		func(context.Context, string) error { return nil },
		func(context.Context, ValidatedReplacement) error { return nil },
	)
	if !errors.Is(err, canary) || result.installed {
		t.Fatalf("callback-renamed stage result=%#v error=%v", result, err)
	}
	if _, statErr := os.Lstat(escaped); statErr != nil {
		t.Fatalf("callback-renamed stage identity was not retained: %v", statErr)
	}
	if payload, readErr := os.ReadFile(decoy); readErr != nil || string(payload) != "decoy" {
		t.Fatalf("callback stage decoy changed = %q, %v", payload, readErr)
	}
}

func TestMigrateStagedOfflineFromRejectsValidDecoyReplacingRetainedStage(t *testing.T) {
	home := t.TempDir()
	escapeRoot := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	escaped := filepath.Join(escapeRoot, "escaped-provider-stage.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	var decoy string
	liveCalled := false
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		func(ctx context.Context, stage string) error {
			if renameErr := os.Rename(stage, escaped); renameErr != nil {
				return renameErr
			}
			decoy = stage
			return installProviderOfflineFixture(ctx, decoy)
		},
		func(context.Context, string) error { return nil },
		func(context.Context, ValidatedReplacement) error {
			liveCalled = true
			return nil
		},
	)
	if err == nil || result.installed || liveCalled ||
		!strings.Contains(err.Error(), "retained SQLite stage after migration") {
		t.Fatalf("valid stage decoy result=%#v live=%t error=%v", result, liveCalled, err)
	}
	if _, statErr := os.Lstat(escaped); statErr != nil {
		t.Fatalf("escaped retained provider stage is unavailable: %v", statErr)
	}
	if _, statErr := os.Lstat(decoy); statErr != nil {
		t.Fatalf("valid replacement decoy was deleted: %v", statErr)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("valid stage decoy changed live target: equal=%t error=%v", bytes.Equal(before, after), err)
	}
}

func TestMigrateStagedOfflineFromRechecksTargetAfterLiveVerification(t *testing.T) {
	for _, test := range []struct {
		name     string
		existing bool
		mutate   func(*testing.T, string)
	}{
		{
			name:     "existing target replaced",
			existing: true,
			mutate: func(t *testing.T, target string) {
				t.Helper()
				if err := os.Rename(target, target+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing target appeared",
			mutate: func(t *testing.T, target string) {
				t.Helper()
				if err := os.WriteFile(target, []byte("appeared"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			sourcePath := filepath.Join(home, "verified-backup.db")
			target := filepath.Join(home, "live.db")
			if test.existing {
				createProviderOfflineFixture(t, sourcePath)
				createProviderOfflineFixture(t, target)
			}
			recorder := &offlineProviderHookRecorder{}
			lease := newOfflineProviderTestLease(t, target, recorder)
			source := immutableGenerationSourceForTest(func(
				ctx context.Context,
				use func(context.Context, string) error,
			) error {
				return use(ctx, sourcePath)
			})
			result, err := MigrateStagedOfflineFromWithLiveVerification(
				t.Context(), lease, source, 5*time.Second, 1,
				installProviderOfflineFixture,
				func(context.Context, string) error { return nil },
				func(ctx context.Context, replacement ValidatedReplacement) error {
					return replacement.Use(ctx, "global/auth", target, func(
						context.Context, string, ValidatedReplacementCheck,
					) error {
						test.mutate(t, target)
						return nil
					})
				},
			)
			if err == nil || result.installed || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown ||
				!strings.Contains(err.Error(), "target") {
				t.Fatalf("target drift result=%#v error=%v", result, err)
			}
			events, pinned := recorder.snapshot()
			if indexProviderOfflineEvent(events, "discard") < 0 ||
				indexProviderOfflineEvent(events, "reconcile-replacement") >= 0 {
				t.Fatalf("target drift events = %v", events)
			}
			if _, statErr := os.Lstat(pinned); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("target-drift stage remains at %q: %v", pinned, statErr)
			}
		})
	}
}

func TestMigrateStagedOfflineFromRejectsInvalidSourceScopesBeforeCutover(t *testing.T) {
	tests := []struct {
		name   string
		source func(string, string) ImmutableGenerationSource
	}{
		{
			name: "source callback omitted use",
			source: func(string, string) ImmutableGenerationSource {
				return immutableGenerationSourceForTest(func(
					context.Context, func(context.Context, string) error,
				) error {
					return nil
				})
			},
		},
		{
			name: "source callback used twice",
			source: func(path, _ string) ImmutableGenerationSource {
				return immutableGenerationSourceForTest(func(
					ctx context.Context, use func(context.Context, string) error,
				) error {
					first := use(ctx, path)
					return errors.Join(first, use(ctx, path))
				})
			},
		},
		{
			name: "source aliases leased target",
			source: func(_, target string) ImmutableGenerationSource {
				return immutableGenerationSourceForTest(func(
					ctx context.Context, use func(context.Context, string) error,
				) error {
					return use(ctx, target)
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			sourcePath := filepath.Join(home, "verified-backup.db")
			target := filepath.Join(home, "live.db")
			createProviderOfflineFixture(t, sourcePath)
			createProviderOfflineFixture(t, target)
			before, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			recorder := &offlineProviderHookRecorder{}
			lease := newOfflineProviderTestLease(t, target, recorder)
			_, migrationErr := MigrateStagedOfflineFrom(
				t.Context(),
				lease,
				test.source(sourcePath, target),
				time.Second,
				1,
				installProviderOfflineFixture,
				func(context.Context, string) error { return nil },
			)
			if migrationErr == nil || dblayer.CodeOf(migrationErr) == dblayer.CodeOutcomeUnknown {
				t.Fatalf("invalid immutable source error = %v", migrationErr)
			}
			after, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("invalid immutable source changed live target")
			}
		})
	}
}

func TestMigrateStagedOfflineFromSanitizesSourceOutcomeBeforeCutover(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	unknown := dblayer.NewError(dblayer.CodeOutcomeUnknown, "source outcome canary")
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return errors.Join(use(ctx, sourcePath), unknown)
	})
	lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})

	_, err = MigrateStagedOfflineFrom(
		t.Context(), lease, source, time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	)
	if err == nil || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown ||
		!errors.Is(err, unknown) {
		t.Fatalf("pre-cutover source outcome error = %v", err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("pre-cutover source outcome changed live target")
	}
}

func TestMigrateStagedOfflineFromRejectsRevokedAndCanceledCallers(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	sourceCalls := 0
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		sourceCalls++
		return use(ctx, sourcePath)
	})

	t.Run("revoked lease", func(t *testing.T) {
		recorder := &offlineProviderHookRecorder{}
		lease := newOfflineProviderTestLease(t, target, recorder)
		canary := errors.New("revoked provider lease")
		if err := databaseproviderlease.Revoke(lease, canary); err != nil {
			t.Fatal(err)
		}
		if _, err := MigrateStagedOfflineFrom(
			t.Context(), lease, source, time.Second, 1,
			installProviderOfflineFixture, func(context.Context, string) error { return nil },
		); !errors.Is(err, canary) {
			t.Fatalf("revoked lease error = %v", err)
		}
		if events, _ := recorder.snapshot(); len(events) != 0 {
			t.Fatalf("revoked lease invoked hooks: %v", events)
		}
	})

	t.Run("canceled caller does not consume", func(t *testing.T) {
		recorder := &offlineProviderHookRecorder{}
		lease := newOfflineProviderTestLease(t, target, recorder)
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := MigrateStagedOfflineFrom(
			canceled, lease, source, time.Second, 1,
			installProviderOfflineFixture, func(context.Context, string) error { return nil },
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled caller error = %v", err)
		}
		if _, err := MigrateStagedOfflineFrom(
			t.Context(), lease, source, 5*time.Second, 1,
			installProviderOfflineFixture, func(context.Context, string) error { return nil },
		); err != nil {
			t.Fatalf("lease was consumed by rejected caller: %v", err)
		}
	})

	if sourceCalls != 1 {
		t.Fatalf("source callback calls = %d, want 1", sourceCalls)
	}
}

func TestMigrateStagedOfflineFromInvalidInputDoesNotConsumeLease(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	if _, err := MigrateStagedOfflineFrom(
		t.Context(), lease, source, time.Second, 0,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("invalid expected version error = %v", err)
	}
	if events, _ := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("invalid input invoked lease hooks: %v", events)
	}
	if _, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil }, nil,
	); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("nil live verification error = %v", err)
	}
	if events, _ := recorder.snapshot(); len(events) != 0 {
		t.Fatalf("nil live verification invoked lease hooks: %v", events)
	}
	if strconv.IntSize > 32 {
		tooLargeValue := maxSQLiteSchemaVersion
		tooLargeValue++
		tooLarge := int(tooLargeValue)
		if _, err := MigrateStagedOfflineFrom(
			t.Context(), lease, source, time.Second, tooLarge,
			installProviderOfflineFixture, func(context.Context, string) error { return nil },
		); dblayer.CodeOf(err) != dblayer.CodeInvalid {
			t.Fatalf("oversized expected version error = %v", err)
		}
	}
	if _, err := MigrateStagedOfflineFrom(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	); err != nil {
		t.Fatalf("invalid call consumed lease: %v", err)
	}

	if _, err := NewImmutableGenerationSource("bad id", func(
		context.Context, func(context.Context, string) error,
	) error {
		return nil
	}); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("invalid source constructor error = %v", err)
	}
	if _, err := NewImmutableGenerationSource("global/auth", nil); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("nil source constructor error = %v", err)
	}
}

func TestMigrateStagedOfflineFromRejectsMismatchedSourceStoreID(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	called := false
	source, err := NewImmutableGenerationSource("launcher/auth", func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		called = true
		return use(ctx, sourcePath)
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})
	if _, err := MigrateStagedOfflineFrom(
		t.Context(), lease, source, time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	); dblayer.CodeOf(err) != dblayer.CodeIntegrity || called {
		t.Fatalf("mismatched source called=%t error=%v", called, err)
	}
}

func TestMigrateStagedOfflineFromMapsReplacementReconcileFailureToUnknown(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	canary := errors.New("replacement reconciliation failed")
	recorder := &offlineProviderHookRecorder{
		failures: map[string]error{"reconcile-replacement": canary},
	}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	_, err := MigrateStagedOfflineFrom(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
	)
	if dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown || !errors.Is(err, canary) {
		t.Fatalf("replacement reconciliation error = %v", err)
	}
	events, _ := recorder.snapshot()
	if indexProviderOfflineEvent(events, "pin") < 0 ||
		indexProviderOfflineEvent(events, "reconcile-replacement") < 0 {
		t.Fatalf("post-replacement hook trace = %v", events)
	}
}

func TestMigrateStagedOfflineFromPreservesPostCutoverAuthorityCauses(t *testing.T) {
	for _, phase := range []string{"sidecar reconciliation", "final check"} {
		t.Run(phase, func(t *testing.T) {
			home := t.TempDir()
			sourcePath := filepath.Join(home, "verified-backup.db")
			target := filepath.Join(home, "live.db")
			createProviderOfflineFixture(t, sourcePath)
			createProviderOfflineFixture(t, target)
			canary := errors.New(phase + " failed")
			recorder := &offlineProviderHookRecorder{}
			var authority offlineProviderAuthority = offlineProviderTestAuthority{recorder: recorder}
			if phase == "sidecar reconciliation" {
				recorder.failures = map[string]error{"reconcile": canary}
			} else {
				authority = offlineProviderFinalCheckAuthority{
					offlineProviderTestAuthority: offlineProviderTestAuthority{recorder: recorder},
					failure:                      canary,
				}
			}
			source := immutableGenerationSourceForTest(func(
				ctx context.Context,
				use func(context.Context, string) error,
			) error {
				return use(ctx, sourcePath)
			})

			result, err := migrateStagedOfflineAuthorized(
				t.Context(), source, target, 5*time.Second, 1,
				installProviderOfflineFixture,
				func(context.Context, string) error { return nil },
				authority,
				stagedMigrationOps{
					replace: replaceStagedGeneration, activate: activateInstalledGeneration,
				},
			)
			if !result.installed || dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown ||
				!errors.Is(err, canary) {
				t.Fatalf("post-cutover %s result=%#v error=%v", phase, result, err)
			}
		})
	}
}

func TestMigrateStagedOfflineFromRetiresWorkingGenerationBeforeCutover(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	canary := errors.New("working cleanup failed")
	authority := offlineProviderTestAuthority{recorder: &offlineProviderHookRecorder{}}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	result, err := migrateStagedOfflineAuthorized(
		t.Context(), source, target, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: replaceStagedGeneration, activate: activateInstalledGeneration,
			discard: func(string, time.Duration) error { return canary },
		},
	)
	if result.installed || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown ||
		!errors.Is(err, canary) {
		t.Fatalf("pre-cutover cleanup result=%#v error=%v", result, err)
	}
}

func TestMigrateStagedOfflineFromMapsPostCallbackLeaseErrorToUnknown(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	lateErr := errors.New("lease ended after provider callback")
	consumer := func(
		ctx context.Context,
		lease *databaseproviderlease.Lease,
		use func(context.Context, databaseproviderlease.Access) error,
	) error {
		if err := databaseproviderlease.Consume(ctx, lease, use); err != nil {
			return err
		}
		return lateErr
	}

	result, err := migrateStagedOfflineFromWithConsumer(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		consumer,
	)
	if dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown || !errors.Is(err, lateErr) ||
		!result.installed || result.AfterVersion != 1 {
		t.Fatalf("post-callback result=%#v error=%v", result, err)
	}
	if !providerOfflineTableExists(t, target, "installed") {
		t.Fatal("post-callback error did not follow completed cutover")
	}
	if events, _ := recorder.snapshot(); indexProviderOfflineEvent(
		events, "reconcile-replacement",
	) < 0 {
		t.Fatalf("post-callback hook trace = %v", events)
	}

	if _, err := migrateStagedOfflineFromWithConsumer(
		t.Context(), nil, ImmutableGenerationSource{}, time.Second, 1, nil, nil, nil,
	); dblayer.CodeOf(err) != dblayer.CodeInvalid {
		t.Fatalf("nil lease consumer error = %v", err)
	}
}

func TestMigrateStagedOfflineFromSanitizesPostCallbackErrorBeforeCutover(t *testing.T) {
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	sourceErr := dblayer.NewError(dblayer.CodeOutcomeUnknown, "source outcome canary")
	lateErr := dblayer.NewError(dblayer.CodeOutcomeUnknown, "lease cleanup outcome canary")
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return errors.Join(use(ctx, sourcePath), sourceErr)
	})
	lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})
	consumer := func(
		ctx context.Context,
		lease *databaseproviderlease.Lease,
		use func(context.Context, databaseproviderlease.Access) error,
	) error {
		return errors.Join(databaseproviderlease.Consume(ctx, lease, use), lateErr)
	}

	result, err := migrateStagedOfflineFromWithConsumer(
		t.Context(), lease, source, time.Second, 1,
		installProviderOfflineFixture, func(context.Context, string) error { return nil },
		consumer,
	)
	if err == nil || result.installed || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown ||
		!errors.Is(err, sourceErr) || !errors.Is(err, lateErr) {
		t.Fatalf("post-callback pre-cutover result=%#v error=%v", result, err)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("post-callback pre-cutover error changed live target")
	}
}

func TestMigrateStagedOfflineFromDiscardsPinAfterKnownReplacementFailure(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	recorder := &offlineProviderHookRecorder{}
	authority := offlineProviderTestAuthority{recorder: recorder}
	canary := errors.New("known replacement failure")
	sourceCalls := 0
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		sourceCalls++
		return use(ctx, sourcePath)
	})
	_, err := migrateStagedOfflineAuthorized(
		ctx,
		source,
		target,
		time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) { return false, canary },
			activate: func(context.Context, string, time.Duration, int) error {
				return nil
			},
		},
	)
	if !errors.Is(err, canary) || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("known replacement failure = %v", err)
	}
	events, pinned := recorder.snapshot()
	pin := indexProviderOfflineEvent(events, "pin")
	discard := indexProviderOfflineEvent(events, "discard")
	if pin < 0 || discard <= pin || indexProviderOfflineEvent(
		events, "reconcile-replacement",
	) >= 0 {
		t.Fatalf("known replacement hook trace = %v", events)
	}
	if sourceCalls != 1 {
		t.Fatalf("immutable source callback calls = %d", sourceCalls)
	}
	if pinned == "" {
		t.Fatal("known replacement failure did not expose a pinned stage")
	}
	if _, statErr := os.Lstat(pinned); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("discarded replacement stage remains at %q: %v", pinned, statErr)
	}
}

func TestMigrateStagedOfflineFromRetainsStageWhenPinCannotBeDiscarded(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	replaceErr := errors.New("known replacement failure")
	discardErr := errors.New("replacement pin could not be retired")
	recorder := &offlineProviderHookRecorder{
		failures: map[string]error{"discard": discardErr},
	}
	authority := offlineProviderTestAuthority{recorder: recorder}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	_, err := migrateStagedOfflineAuthorized(
		ctx,
		source,
		target,
		time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) { return false, replaceErr },
			activate: func(context.Context, string, time.Duration, int) error {
				return nil
			},
		},
	)
	if !errors.Is(err, replaceErr) || !errors.Is(err, discardErr) ||
		dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("failed pin discard error = %v", err)
	}
	events, pinned := recorder.snapshot()
	if indexProviderOfflineEvent(events, "pin") < 0 ||
		indexProviderOfflineEvent(events, "discard") <= indexProviderOfflineEvent(events, "pin") ||
		indexProviderOfflineEvent(events, "reconcile-replacement") >= 0 {
		t.Fatalf("failed pin discard hook trace = %v", events)
	}
	if info, statErr := os.Lstat(pinned); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("still-pinned diagnostic stage was not retained at %q: %v, %v", pinned, info, statErr)
	}
}

func TestMigrateStagedOfflineFromDiscardsPossiblyPublishedPinError(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	pinErr := errors.New("replacement pin final validation failed")
	recorder := &offlineProviderHookRecorder{failures: map[string]error{"pin": pinErr}}
	authority := offlineProviderTestAuthority{recorder: recorder}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	replaceCalled := false

	_, err := migrateStagedOfflineAuthorized(
		ctx,
		source,
		target,
		time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) {
				replaceCalled = true
				return false, nil
			},
			activate: func(context.Context, string, time.Duration, int) error { return nil },
		},
	)
	if !errors.Is(err, pinErr) || errors.Is(err, errStagedReplacementRemainsPinned) ||
		dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("possibly published pin error = %v", err)
	}
	if replaceCalled {
		t.Fatal("replacement ran after pin error")
	}
	events, pinned := recorder.snapshot()
	pin := indexProviderOfflineEvent(events, "pin")
	discard := indexProviderOfflineEvent(events, "discard")
	if pin < 0 || discard <= pin || indexProviderOfflineEvent(
		events, "reconcile-replacement",
	) >= 0 {
		t.Fatalf("possibly published pin hook trace = %v", events)
	}
	if _, statErr := os.Lstat(pinned); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("discarded pin-error stage remains at %q: %v", pinned, statErr)
	}
}

func TestMigrateStagedOfflineFromRetainsPinSentinelWhenPinErrorLooksPostCutover(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	pinErr := dblayer.NewError(dblayer.CodeOutcomeUnknown, "ambiguous pin publication")
	discardErr := errors.New("ambiguous pin could not be retired")
	recorder := &offlineProviderHookRecorder{failures: map[string]error{
		"pin": pinErr, "discard": discardErr,
	}}
	authority := offlineProviderTestAuthority{recorder: recorder}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	_, err := migrateStagedOfflineAuthorized(
		ctx,
		source,
		target,
		time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) {
				t.Fatal("replacement ran after pin error")
				return false, nil
			},
			activate: func(context.Context, string, time.Duration, int) error { return nil },
		},
	)
	if !errors.Is(err, errStagedReplacementRemainsPinned) ||
		!errors.Is(err, discardErr) || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("ambiguous pin error = %v", err)
	}
	_, pinned := recorder.snapshot()
	if info, statErr := os.Lstat(pinned); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("ambiguously pinned stage was not retained at %q: %v, %v", pinned, info, statErr)
	}
}

func TestMigrateStagedOfflineFromPreservesPinnedPreCutoverCode(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	pinErr := dblayer.NewError(dblayer.CodeUnavailable, "pin unavailable")
	discardErr := errors.New("published pin could not be retired")
	recorder := &offlineProviderHookRecorder{failures: map[string]error{
		"pin": pinErr, "discard": discardErr,
	}}
	authority := offlineProviderTestAuthority{recorder: recorder}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})

	_, err := migrateStagedOfflineAuthorized(
		ctx, source, target, time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) {
				t.Fatal("replacement ran after unavailable pin error")
				return false, nil
			},
			activate: func(context.Context, string, time.Duration, int) error { return nil },
		},
	)
	if dblayer.CodeOf(err) != dblayer.CodeUnavailable ||
		!errors.Is(err, errStagedReplacementRemainsPinned) || !errors.Is(err, discardErr) {
		t.Fatalf("pinned pre-cutover code error = %v", err)
	}
	_, pinned := recorder.snapshot()
	if info, statErr := os.Lstat(pinned); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("unretired unavailable stage was not retained at %q: %v, %v", pinned, info, statErr)
	}
}

func TestMigrateStagedOfflineFromRechecksAuthorityAfterPinBeforeReplace(t *testing.T) {
	ctx := t.Context()
	home := t.TempDir()
	sourcePath := filepath.Join(home, "verified-backup.db")
	target := filepath.Join(home, "live.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	checkErr := errors.New("authority changed after replacement pin")
	recorder := &offlineProviderHookRecorder{}
	authority := offlineProviderPostPinCheckAuthority{
		offlineProviderTestAuthority: offlineProviderTestAuthority{recorder: recorder},
		failure:                      checkErr,
	}
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	replaceCalled := false

	_, err = migrateStagedOfflineAuthorized(
		ctx,
		source,
		target,
		time.Second,
		1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		authority,
		stagedMigrationOps{
			replace: func(string, string) (bool, error) {
				replaceCalled = true
				return false, nil
			},
			activate: func(context.Context, string, time.Duration, int) error {
				return nil
			},
		},
	)
	if !errors.Is(err, checkErr) || dblayer.CodeOf(err) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("post-pin authority error = %v", err)
	}
	if replaceCalled {
		t.Fatal("replacement ran after post-pin authority check failed")
	}
	events, pinned := recorder.snapshot()
	pin := indexProviderOfflineEvent(events, "pin")
	discard := indexProviderOfflineEvent(events, "discard")
	postPinCheck := -1
	for index := pin + 1; index < len(events); index++ {
		if events[index] == "check" {
			postPinCheck = index
			break
		}
	}
	if pin < 0 || postPinCheck <= pin || discard <= postPinCheck ||
		indexProviderOfflineEvent(events, "reconcile-replacement") >= 0 {
		t.Fatalf("post-pin authority hook trace = %v", events)
	}
	if _, statErr := os.Lstat(pinned); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("discarded post-pin stage remains at %q: %v", pinned, statErr)
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed post-pin check changed live target")
	}
}

type offlineProviderTestAuthority struct {
	recorder *offlineProviderHookRecorder
}

type offlineProviderPostPinCheckAuthority struct {
	offlineProviderTestAuthority
	failure error
}

type offlineProviderFinalCheckAuthority struct {
	offlineProviderTestAuthority
	failure error
}

func (authority offlineProviderFinalCheckAuthority) Check(ctx context.Context) error {
	authority.recorder.mu.Lock()
	events := append([]string(nil), authority.recorder.events...)
	authority.recorder.mu.Unlock()
	if err := authority.offlineProviderTestAuthority.Check(ctx); err != nil {
		return err
	}
	if indexProviderOfflineEvent(events, "reconcile") >= 0 {
		return authority.failure
	}
	return nil
}

func (authority offlineProviderPostPinCheckAuthority) Check(ctx context.Context) error {
	authority.recorder.mu.Lock()
	pinned := authority.recorder.pinned != ""
	authority.recorder.mu.Unlock()
	if err := authority.offlineProviderTestAuthority.Check(ctx); err != nil {
		return err
	}
	if pinned {
		return authority.failure
	}
	return nil
}

func (authority offlineProviderTestAuthority) Check(context.Context) error {
	return authority.recorder.record("check")
}

func (authority offlineProviderTestAuthority) Reconcile(context.Context) error {
	return authority.recorder.record("reconcile")
}

func (authority offlineProviderTestAuthority) PinReplacement(
	_ context.Context,
	path string,
) error {
	authority.recorder.mu.Lock()
	authority.recorder.pinned = path
	authority.recorder.mu.Unlock()
	return authority.recorder.record("pin")
}

func (authority offlineProviderTestAuthority) CheckReplacement(
	context.Context,
	string,
) error {
	return authority.recorder.record("check-replacement")
}

func (authority offlineProviderTestAuthority) DiscardReplacement(context.Context) error {
	return authority.recorder.record("discard")
}

func (authority offlineProviderTestAuthority) ReconcileReplacement(context.Context) error {
	return authority.recorder.record("reconcile-replacement")
}

func assertProviderOfflineCutoverOrder(t *testing.T, events []string) {
	t.Helper()
	pin := indexProviderOfflineEvent(events, "pin")
	replacement := indexProviderOfflineEvent(events, "reconcile-replacement")
	reconcile := indexProviderOfflineEvent(events, "reconcile")
	if pin < 0 || replacement <= pin || reconcile <= replacement {
		t.Fatalf("provider cutover hook order = %v", events)
	}
	for index := pin + 1; index < replacement; index++ {
		if events[index] == "reconcile" {
			t.Fatalf("ordinary reconciliation preceded replacement promotion: %v", events)
		}
	}
}

func indexProviderOfflineEvent(events []string, wanted string) int {
	for index, event := range events {
		if event == wanted {
			return index
		}
	}
	return -1
}

func createProviderOfflineFixture(t *testing.T, path string) {
	t.Helper()
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := Configure(t.Context(), database, time.Second, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE original_marker(value TEXT NOT NULL) STRICT;
		INSERT INTO original_marker(value) VALUES ('ready');
	`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, 1); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func installProviderOfflineFixture(ctx context.Context, stage string) error {
	database, err := openOfflineStage(ctx, stage, time.Second)
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `
		CREATE TABLE installed(value TEXT NOT NULL) STRICT;
		INSERT INTO installed(value) VALUES ('ready');
	`); err != nil {
		return err
	}
	return SetSchemaVersion(ctx, database, 1)
}

func providerOfflineTableExists(t *testing.T, path, table string) bool {
	t.Helper()
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRowContext(
		t.Context(),
		"SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?",
		table,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}
