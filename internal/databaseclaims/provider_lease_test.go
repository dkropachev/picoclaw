package databaseclaims

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationGuardProviderLeaseBindsExactTargetAndReplacementHooks(t *testing.T) {
	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	stores, err := guard.Stores()
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	target := providerLeaseTestTarget(t, stores)
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	providerLease, drain, err := guard.NewProviderLease(parent, "global/auth")
	if err != nil || providerLease == nil || drain == nil {
		_ = guard.Release()
		t.Fatalf("provider lease = %#v, drain=%t, %v", providerLease, drain != nil, err)
	}

	beforeClaims := len(claimsLease.identities)
	err = databaseproviderlease.Consume(
		t.Context(), providerLease,
		func(ctx context.Context, access databaseproviderlease.Access) error {
			id, path, targetErr := access.Target()
			if targetErr != nil || id != "global/auth" || path != target {
				return errors.Join(errors.New("provider lease target changed"), targetErr)
			}
			if checkErr := access.Check(ctx); checkErr != nil {
				return checkErr
			}
			if reconcileErr := access.Reconcile(ctx); reconcileErr != nil {
				return reconcileErr
			}
			if discardErr := access.DiscardReplacement(ctx); discardErr != nil {
				return discardErr
			}

			stage := writeReplacementStage(t, claimsLease.home, "provider-unused-stage.db")
			if pinErr := access.PinReplacement(ctx, stage); pinErr != nil {
				return pinErr
			}
			if checkErr := access.CheckReplacement(ctx, stage); checkErr != nil {
				return checkErr
			}
			if claimsLease.replacementPins[id] == nil ||
				claimsLease.replacementPins[id].path != stage {
				return errors.New("provider pin was not published")
			}
			if discardErr := access.DiscardReplacement(ctx); discardErr != nil {
				return discardErr
			}
			if claimsLease.replacementPins[id] != nil {
				return errors.New("provider discard retained its unused pin")
			}
			if discardErr := access.DiscardReplacement(ctx); discardErr != nil {
				return discardErr
			}

			replacement := writeReplacementStage(t, claimsLease.home, "provider-installed-stage.db")
			if pinErr := access.PinReplacement(ctx, replacement); pinErr != nil {
				return pinErr
			}
			if checkErr := access.CheckReplacement(ctx, replacement); checkErr != nil {
				return checkErr
			}
			if claimsLease.replacementPins[id] == nil ||
				claimsLease.replacementPins[id].path != replacement {
				return errors.New("provider replacement path was not retained")
			}
			if renameErr := os.Rename(target, target+".old"); renameErr != nil {
				return renameErr
			}
			if renameErr := os.Rename(replacement, target); renameErr != nil {
				return renameErr
			}
			if reconcileErr := access.ReconcileReplacement(ctx); reconcileErr != nil {
				return reconcileErr
			}
			return access.Check(ctx)
		},
	)
	if err != nil {
		_ = drain(t.Context(), err)
		_ = guard.Release()
		t.Fatal(err)
	}
	if len(claimsLease.identities) <= beforeClaims {
		_ = drain(t.Context(), nil)
		_ = guard.Release()
		t.Fatalf(
			"provider pin did not retain a monotonic physical claim: %d <= %d",
			len(claimsLease.identities), beforeClaims,
		)
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := drain(waitCtx, nil); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if err := drain(waitCtx, errors.New("later drain cause")); err != nil {
		_ = guard.Release()
		t.Fatalf("repeat provider drain = %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claimsLease.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationGuardProviderLeaseDrainCancelsBeforeGuardRelease(t *testing.T) {
	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	providerLease, drain, err := guard.NewProviderLease(parent, "global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	entered := make(chan struct{})
	consumeResult := make(chan error, 1)
	go func() {
		consumeResult <- databaseproviderlease.Consume(
			context.Background(), providerLease,
			func(ctx context.Context, _ databaseproviderlease.Access) error {
				close(entered)
				<-ctx.Done()
				return context.Cause(ctx)
			},
		)
	}()
	<-entered
	cause := errors.New("migration provider scope ended")
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := drain(waitCtx, cause); err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if err := <-consumeResult; !errors.Is(err, cause) {
		_ = guard.Release()
		t.Fatalf("provider consumption after drain = %v", err)
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if err := databaseproviderlease.Wait(waitCtx, providerLease); err != nil {
		t.Fatalf("provider lease was not drained before guard release: %v", err)
	}
}

func TestMigrationGuardReleaseRevokesAndDrainsProviderChild(t *testing.T) {
	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	providerLease, _, err := guard.NewProviderLease(parent, "global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	checked := make(chan struct{})
	canceled := make(chan struct{})
	allowReturn := make(chan struct{})
	consumeResult := make(chan error, 1)
	go func() {
		consumeResult <- databaseproviderlease.Consume(
			context.Background(), providerLease,
			func(ctx context.Context, access databaseproviderlease.Access) error {
				if checkErr := access.Check(ctx); checkErr != nil {
					return checkErr
				}
				close(checked)
				<-ctx.Done()
				close(canceled)
				<-allowReturn
				return context.Cause(ctx)
			},
		)
	}()
	<-checked
	releaseResult := make(chan error, 1)
	go func() { releaseResult <- guard.Release() }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("guard release did not cancel its provider child")
	}
	lease, drain, newErr := guard.NewProviderLease(parent, "global/auth")
	if lease != nil || drain != nil || database.CodeOf(newErr) != database.CodeUnavailable {
		close(allowReturn)
		t.Fatalf("provider child admitted while guard released = %#v, %t, %v", lease, drain != nil, newErr)
	}
	select {
	case releaseErr := <-releaseResult:
		close(allowReturn)
		t.Fatalf("guard release returned before provider drain: %v", releaseErr)
	default:
	}
	repeatReleaseResult := make(chan error, 1)
	go func() { repeatReleaseResult <- guard.Release() }()
	select {
	case releaseErr := <-repeatReleaseResult:
		close(allowReturn)
		t.Fatalf("repeat guard release returned before provider drain: %v", releaseErr)
	default:
	}
	close(allowReturn)
	if err := <-consumeResult; !errors.Is(err, errProviderGuardReleased) {
		t.Fatalf("auto-drained provider result = %v", err)
	}
	if err := <-releaseResult; err != nil {
		t.Fatal(err)
	}
	if err := <-repeatReleaseResult; err != nil {
		t.Fatalf("concurrent guard release = %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := databaseproviderlease.Wait(waitCtx, providerLease); err != nil {
		t.Fatalf("released guard left provider child undrained: %v", err)
	}
}

func TestMigrationGuardAllowsOnlyOneResolvedProviderChild(t *testing.T) {
	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	first, drainFirst, err := guard.NewProviderLease(parent, "global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	second, drainSecond, secondErr := guard.NewProviderLease(parent, "global/auth")
	if second != nil || drainSecond != nil ||
		database.CodeOf(secondErr) != database.CodeConflict {
		_ = drainFirst(t.Context(), secondErr)
		_ = guard.Release()
		t.Fatalf(
			"concurrent provider child = %#v, %t, %v",
			second, drainSecond != nil, secondErr,
		)
	}
	err = drainFirst(t.Context(), nil)
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	second, drainSecond, err = guard.NewProviderLease(parent, "global/auth")
	if err != nil || second == nil || drainSecond == nil {
		_ = guard.Release()
		t.Fatalf(
			"resolved provider child was not replaceable = %#v, %t, %v",
			second, drainSecond != nil, err,
		)
	}
	err = drainSecond(t.Context(), nil)
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	err = guard.Release()
	if err != nil {
		t.Fatal(err)
	}
	err = databaseproviderlease.Wait(t.Context(), first)
	if err != nil {
		t.Fatalf("first provider child was not drained: %v", err)
	}
}

func TestMigrationGuardRejectsNewChildUntilPriorPinIsDiscarded(t *testing.T) {
	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	first, drainFirst, err := guard.NewProviderLease(parent, "global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	stage := writeReplacementStage(t, claimsLease.home, "provider-unresolved-stage.db")
	err = databaseproviderlease.Consume(
		t.Context(), first,
		func(ctx context.Context, access databaseproviderlease.Access) error {
			return access.PinReplacement(ctx, stage)
		},
	)
	if err != nil {
		_ = drainFirst(t.Context(), err)
		_ = guard.Release()
		t.Fatal(err)
	}
	err = drainFirst(t.Context(), nil)
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	next, drainNext, nextErr := guard.NewProviderLease(parent, "global/auth")
	if next != nil || drainNext != nil ||
		database.CodeOf(nextErr) != database.CodeConflict {
		_ = guard.Release()
		t.Fatalf(
			"unresolved pin inherited by next child = %#v, %t, %v",
			next, drainNext != nil, nextErr,
		)
	}
	err = guard.DiscardReplacement("global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	if claimsLease.replacementPins["global/auth"] != nil {
		_ = guard.Release()
		t.Fatal("explicit provider pin discard retained a pin")
	}
	next, drainNext, err = guard.NewProviderLease(parent, "global/auth")
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	err = drainNext(t.Context(), nil)
	if err != nil {
		_ = guard.Release()
		t.Fatal(err)
	}
	err = guard.Release()
	if err != nil {
		t.Fatal(err)
	}
	err = databaseproviderlease.Wait(t.Context(), next)
	if err != nil {
		t.Fatalf("next provider child was not drained: %v", err)
	}
}

func TestMigrationGuardProviderLeaseRejectsInvalidAuthorityAndInputs(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if lease, drain, err := (*MigrationRefreshingGuard)(nil).NewProviderLease(
		parent, "global/auth",
	); lease != nil || drain != nil || database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil guard provider lease = %#v, %t, %v", lease, drain != nil, err)
	}

	claimsLease := acquireMigrationLeaseWithMain(t)
	guard, err := claimsLease.GuardStoresMigrating()
	if err != nil {
		t.Fatal(err)
	}
	var lease *databaseproviderlease.Lease
	var drain func(context.Context, error) error
	lease, drain, err = guard.NewProviderLease(parent, "bad id")
	if lease != nil || drain != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("invalid ID provider lease = %#v, %t, %v", lease, drain != nil, err)
	}
	lease, drain, err = guard.NewProviderLease(parent, "global/missing")
	if lease != nil ||
		drain != nil || database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("unclaimed provider lease = %#v, %t, %v", lease, drain != nil, err)
	}
	lease, drain, err = guard.NewProviderLease(context.Background(), "global/auth")
	if lease != nil ||
		drain != nil || database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("unbounded provider lease = %#v, %t, %v", lease, drain != nil, err)
	}
	canceled, cancelCause := context.WithCancelCause(parent)
	parentCause := errors.New("provider parent ended")
	cancelCause(parentCause)
	lease, drain, err = guard.NewProviderLease(canceled, "global/auth")
	if lease != nil ||
		drain != nil || !errors.Is(err, parentCause) {
		_ = guard.Release()
		t.Fatalf("canceled provider lease = %#v, %t, %v", lease, drain != nil, err)
	}
	err = guard.DiscardReplacement("global/missing")
	if database.CodeOf(err) != database.CodeInvalid {
		_ = guard.Release()
		t.Fatalf("unclaimed discard = %v", err)
	}
	if err = guard.Release(); err != nil {
		t.Fatal(err)
	}
	lease, drain, err = guard.NewProviderLease(parent, "global/auth")
	if lease != nil || drain != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("released guard provider lease = %#v, %t, %v", lease, drain != nil, err)
	}
	err = (*MigrationRefreshingGuard)(nil).DiscardReplacement("global/auth")
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil guard discard = %v", err)
	}
	err = guard.DiscardReplacement("global/auth")
	if database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("released guard discard = %v", err)
	}
}

func TestProviderLeaseGuardHookPreservesCancellation(t *testing.T) {
	if err := (*providerLeaseChild)(nil).drain(t.Context(), nil); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil provider child drain = %v", err)
	}
	if err := providerLeaseGuardHook(nil, func() error { return nil }); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil provider hook context = %v", err)
	}
	if err := providerLeaseGuardHook(t.Context(), nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil provider hook = %v", err)
	}
	preCanceled, cancelPre := context.WithCancelCause(t.Context())
	preCause := errors.New("provider hook canceled before admission")
	cancelPre(preCause)
	called := false
	if err := providerLeaseGuardHook(preCanceled, func() error {
		called = true
		return nil
	}); !errors.Is(err, preCause) || called {
		t.Fatalf("pre-canceled provider hook = %v, called=%t", err, called)
	}
	postCanceled, cancelPost := context.WithCancelCause(t.Context())
	postCause := errors.New("provider hook canceled after admission")
	hookCause := errors.New("provider hook failed")
	err := providerLeaseGuardHook(postCanceled, func() error {
		cancelPost(postCause)
		return hookCause
	})
	if !errors.Is(err, hookCause) || !errors.Is(err, postCause) {
		t.Fatalf("post-canceled provider hook = %v", err)
	}
}

func TestMigrationGuardProviderLeaseRejectsCorruptTargetAndPinLedgers(t *testing.T) {
	t.Run("target index", func(t *testing.T) {
		claimsLease := acquireMigrationLeaseWithMain(t)
		guard, err := claimsLease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		index := claimsLease.byID["global/auth"]
		guard.state.stores[index].ID = "launcher/auth"
		parent, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		lease, drain, err := guard.NewProviderLease(parent, "global/auth")
		if lease != nil || drain != nil || database.CodeOf(err) != database.CodeIntegrity {
			_ = guard.Release()
			t.Fatalf("corrupt target index = %#v, %t, %v", lease, drain != nil, err)
		}
		if releaseErr := guard.Release(); database.CodeOf(releaseErr) != database.CodeIntegrity {
			t.Fatalf("corrupt target guard release = %v", releaseErr)
		}
	})

	t.Run("expected main ledger", func(t *testing.T) {
		claimsLease := acquireMigrationLeaseWithMain(t)
		guard, err := claimsLease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		delete(guard.state.expectedMain, "global/auth")
		if err := guard.DiscardReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			_ = guard.Release()
			t.Fatalf("corrupt expected-main discard = %v", err)
		}
		if releaseErr := guard.Release(); database.CodeOf(releaseErr) != database.CodeIntegrity {
			t.Fatalf("corrupt expected-main guard release = %v", releaseErr)
		}
	})

	t.Run("replacement pin ledger", func(t *testing.T) {
		claimsLease := acquireMigrationLeaseWithMain(t)
		guard, err := claimsLease.GuardStoresMigrating()
		if err != nil {
			t.Fatal(err)
		}
		guard.state.pinned["global/auth"] = struct{}{}
		if err := guard.DiscardReplacement("global/auth"); database.CodeOf(err) != database.CodeIntegrity {
			_ = guard.Release()
			t.Fatalf("corrupt replacement-pin discard = %v", err)
		}
		if releaseErr := guard.Release(); database.CodeOf(releaseErr) != database.CodeIntegrity {
			t.Fatalf("corrupt replacement-pin guard release = %v", releaseErr)
		}
	})
}

func providerLeaseTestTarget(t *testing.T, stores []storecatalog.Spec) string {
	t.Helper()
	for _, store := range stores {
		if store.ID == "global/auth" {
			if !filepath.IsAbs(store.Path) {
				t.Fatalf("provider target is not absolute: %q", store.Path)
			}
			return store.Path
		}
	}
	t.Fatal("global/auth store is absent")
	return ""
}
