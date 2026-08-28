package logger

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDiagnosticPolicyTruthTableAndMeet(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	disabledPermission := NewDiagnosticPolicy(false, DEBUG)
	disabledLevel := NewDiagnosticPolicy(true, INFO)
	zero := DiagnosticPolicy{}

	if !enabled.allowsApplicationPreview() {
		t.Fatal("stored permission plus DEBUG did not enable preview")
	}
	for name, policy := range map[string]DiagnosticPolicy{
		"permission false": disabledPermission,
		"stored info":      disabledLevel,
		"zero":             zero,
		"stored warn":      NewDiagnosticPolicy(true, WARN),
		"stored error":     NewDiagnosticPolicy(true, ERROR),
		"stored trace":     NewDiagnosticPolicy(true, LogLevel(-2)),
		"stored no level":  NewDiagnosticPolicy(true, LogLevel(6)),
		"stored disabled":  NewDiagnosticPolicy(true, LogLevel(7)),
		"invalid level":    NewDiagnosticPolicy(true, LogLevel(127)),
	} {
		if policy.allowsApplicationPreview() {
			t.Fatalf("%s enabled preview", name)
		}
	}

	if !enabled.Meet(enabled).allowsApplicationPreview() {
		t.Fatal("true meet true must remain enabled")
	}
	for name, policy := range map[string]DiagnosticPolicy{
		"true/false": enabled.Meet(disabledPermission),
		"false/true": disabledLevel.Meet(enabled),
		"true/zero":  enabled.Meet(zero),
		"zero/true":  zero.Meet(enabled),
	} {
		if policy.allowsApplicationPreview() {
			t.Fatalf("%s widened preview", name)
		}
	}

	// Policies remain directly comparable for generation/reload signatures.
	if enabled == disabledPermission || enabled != NewDiagnosticPolicy(true, DEBUG) {
		t.Fatal("diagnostic policy comparison is not stable")
	}
}

func TestDiagnosticPolicyContextRevocationAndRebind(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	disabled := NewDiagnosticPolicy(false, DEBUG)

	if DiagnosticPolicyFromContext(nil).allowsApplicationPreview() ||
		DiagnosticPolicyFromContext(context.Background()).allowsApplicationPreview() {
		t.Fatal("missing context policy enabled preview")
	}

	ctxA, revokeA := BindRootDiagnosticPolicy(context.Background(), enabled)
	if !DiagnosticPolicyFromContext(ctxA).allowsApplicationPreview() {
		t.Fatal("active enabled binding was not returned")
	}
	revokeA()
	revokeA()
	if DiagnosticPolicyFromContext(ctxA).allowsApplicationPreview() {
		t.Fatal("revoked binding remained enabled")
	}

	// Prior effective policy is retained as the origin cap after revocation.
	ctxB, revokeB := RebindDiagnosticPolicy(ctxA, ctxA, enabled)
	defer revokeB()
	if !DiagnosticPolicyFromContext(ctxB).allowsApplicationPreview() {
		t.Fatal("revoked true origin unexpectedly narrowed true rebound")
	}

	falseOrigin, revokeFalseOrigin := BindRootDiagnosticPolicy(context.Background(), disabled)
	revokeFalseOrigin()
	falseThenTrue, revokeFalseThenTrue := RebindDiagnosticPolicy(
		context.Background(), falseOrigin, enabled,
	)
	defer revokeFalseThenTrue()
	if DiagnosticPolicyFromContext(falseThenTrue).allowsApplicationPreview() {
		t.Fatal("A(false) -> B(true) widened stale origin")
	}

	trueOrigin, revokeTrueOrigin := BindRootDiagnosticPolicy(context.Background(), enabled)
	revokeTrueOrigin()
	trueThenFalse, revokeTrueThenFalse := RebindDiagnosticPolicy(
		context.Background(), trueOrigin, disabled,
	)
	defer revokeTrueThenFalse()
	if DiagnosticPolicyFromContext(trueThenFalse).allowsApplicationPreview() {
		t.Fatal("A(true) -> B(false) ignored false current policy")
	}

	// Revoking latest binding never reveals an older enabled binding.
	liveA, revokeLiveA := BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeLiveA()
	liveB, revokeLiveB := RebindDiagnosticPolicy(liveA, liveA, enabled)
	revokeLiveB()
	if DiagnosticPolicyFromContext(liveB).allowsApplicationPreview() {
		t.Fatal("revoked latest binding fell back to earlier binding")
	}

	trueFalse, revokeTrueFalse := RebindDiagnosticPolicy(liveA, liveA, disabled)
	defer revokeTrueFalse()
	trueFalseTrue, revokeTrueFalseTrue := RebindDiagnosticPolicy(
		context.Background(), trueFalse, enabled,
	)
	defer revokeTrueFalseTrue()
	if DiagnosticPolicyFromContext(trueFalseTrue).allowsApplicationPreview() {
		t.Fatal("A(true) -> B(false) -> C(true) widened nested false cap")
	}

	detached, revokeDetached := RebindDiagnosticPolicy(
		context.Background(), context.Background(), enabled,
	)
	defer revokeDetached()
	if DiagnosticPolicyFromContext(detached).allowsApplicationPreview() {
		t.Fatal("rebind without origin established preview rights")
	}
}

func TestDiagnosticPolicyConcurrentRevokeIsIdempotent(t *testing.T) {
	ctx, revoke := BindRootDiagnosticPolicy(
		context.Background(),
		NewDiagnosticPolicy(true, DEBUG),
	)

	var group sync.WaitGroup
	for range 64 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				_ = DiagnosticPolicyFromContext(ctx)
				revoke()
			}
		}()
	}
	group.Wait()
	if DiagnosticPolicyFromContext(ctx).allowsApplicationPreview() {
		t.Fatal("concurrently revoked binding remained active")
	}
}

func TestDiagnosticPolicyCancellationAndFreshParentRebind(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	originParent, cancelOrigin := context.WithCancel(context.Background())
	origin, revokeOrigin := BindRootDiagnosticPolicy(originParent, enabled)
	defer revokeOrigin()
	cancelOrigin()
	if origin.Err() == nil || !DiagnosticPolicyFromContext(origin).allowsApplicationPreview() {
		t.Fatal("context cancellation incorrectly revoked diagnostic lease")
	}

	withoutCancel := context.WithoutCancel(origin)
	if withoutCancel.Err() != nil ||
		!DiagnosticPolicyFromContext(withoutCancel).allowsApplicationPreview() {
		t.Fatal("WithoutCancel did not retain active origin policy")
	}

	freshParent, cancelFresh := context.WithCancel(context.Background())
	rebound, revokeRebound := RebindDiagnosticPolicy(freshParent, origin, enabled)
	defer revokeRebound()
	if rebound.Err() != nil || !DiagnosticPolicyFromContext(rebound).allowsApplicationPreview() {
		t.Fatal("canceled origin did not rebind onto fresh parent")
	}
	cancelFresh()
	if rebound.Err() == nil || !DiagnosticPolicyFromContext(rebound).allowsApplicationPreview() {
		t.Fatal("fresh parent cancellation incorrectly revoked rebound policy")
	}
	revokeRebound()
	if DiagnosticPolicyFromContext(rebound).allowsApplicationPreview() {
		t.Fatal("explicit rebound revoke did not disable policy")
	}
}

func TestDiagnosticPolicyRebindMeetsCurrentParentCap(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	disabled := NewDiagnosticPolicy(false, DEBUG)
	origin, revokeOrigin := BindRootDiagnosticPolicy(nil, enabled)
	defer revokeOrigin()
	currentParent, revokeCurrentParent := BindRootDiagnosticPolicy(
		context.Background(), disabled,
	)
	defer revokeCurrentParent()
	rebound, revokeRebound := RebindDiagnosticPolicy(currentParent, origin, enabled)
	defer revokeRebound()
	if DiagnosticPolicyFromContext(rebound).allowsApplicationPreview() {
		t.Fatal("current parent false cap was widened")
	}

	nestedRoot, revokeNestedRoot := BindRootDiagnosticPolicy(origin, disabled)
	defer revokeNestedRoot()
	if DiagnosticPolicyFromContext(nestedRoot).allowsApplicationPreview() {
		t.Fatal("root bind widened an existing false policy")
	}
	nilParentRebind, revokeNilParentRebind := RebindDiagnosticPolicy(
		nil,
		origin,
		enabled,
	)
	defer revokeNilParentRebind()
	if !DiagnosticPolicyFromContext(nilParentRebind).allowsApplicationPreview() {
		t.Fatal("nil current parent lost valid origin/current meet")
	}
}

func TestNarrowDiagnosticPolicyRetainsLiveRevocationAndBounds(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	disabled := NewDiagnosticPolicy(false, DEBUG)

	root, revokeRoot := BindRootDiagnosticPolicy(context.Background(), enabled)
	narrowed, revokeNarrowed := NarrowDiagnosticPolicy(root, enabled)
	if !DiagnosticPolicyFromContext(narrowed).allowsApplicationPreview() {
		t.Fatal("live enabled parent/current meet was disabled")
	}
	nestedRoot, revokeNestedRoot := BindRootDiagnosticPolicy(narrowed, enabled)
	if !DiagnosticPolicyFromContext(nestedRoot).allowsApplicationPreview() {
		t.Fatal("root bind on live-linked child lost enabled policy")
	}
	revokeRoot()
	if DiagnosticPolicyFromContext(narrowed).allowsApplicationPreview() ||
		DiagnosticPolicyFromContext(nestedRoot).allowsApplicationPreview() {
		t.Fatal("parent revoke did not disable live-linked descendants")
	}
	revokeNestedRoot()
	revokeNarrowed()

	childRoot, revokeChildRoot := BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeChildRoot()
	child, revokeChild := NarrowDiagnosticPolicy(childRoot, enabled)
	childDescendant, revokeChildDescendant := BindRootDiagnosticPolicy(child, enabled)
	defer revokeChildDescendant()
	revokeChild()
	if DiagnosticPolicyFromContext(child).allowsApplicationPreview() ||
		DiagnosticPolicyFromContext(childDescendant).allowsApplicationPreview() {
		t.Fatal("narrow child revoke did not disable itself and linked descendant")
	}
	brokenRebind, revokeBrokenRebind := RebindDiagnosticPolicy(
		context.Background(),
		child,
		enabled,
	)
	defer revokeBrokenRebind()
	if DiagnosticPolicyFromContext(brokenRebind).allowsApplicationPreview() {
		t.Fatal("rebind revived an inactive live-linked origin")
	}
	brokenParentRebind, revokeBrokenParentRebind := RebindDiagnosticPolicy(
		child,
		childRoot,
		enabled,
	)
	defer revokeBrokenParentRebind()
	if DiagnosticPolicyFromContext(brokenParentRebind).allowsApplicationPreview() {
		t.Fatal("rebind ignored an inactive live-linked current parent")
	}
	narrowedAfterRevoke, revokeNarrowedAfterRevoke := NarrowDiagnosticPolicy(child, enabled)
	defer revokeNarrowedAfterRevoke()
	if DiagnosticPolicyFromContext(narrowedAfterRevoke).allowsApplicationPreview() {
		t.Fatal("narrow captured authority from an inactive live-linked parent")
	}
	rootAfterRevoke, revokeRootAfterRevoke := BindRootDiagnosticPolicy(child, enabled)
	defer revokeRootAfterRevoke()
	if DiagnosticPolicyFromContext(rootAfterRevoke).allowsApplicationPreview() {
		t.Fatal("root bind revived an inactive live-linked parent")
	}

	falseRoot, revokeFalseRoot := BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeFalseRoot()
	falseChild, revokeFalseChild := NarrowDiagnosticPolicy(falseRoot, disabled)
	defer revokeFalseChild()
	if DiagnosticPolicyFromContext(falseChild).allowsApplicationPreview() {
		t.Fatal("narrow false cap was widened")
	}

	detached, revokeDetached := NarrowDiagnosticPolicy(context.Background(), enabled)
	defer revokeDetached()
	if DiagnosticPolicyFromContext(detached).allowsApplicationPreview() {
		t.Fatal("narrow without parent provenance established preview rights")
	}
	nilParent, revokeNilParent := NarrowDiagnosticPolicy(nil, enabled)
	defer revokeNilParent()
	if DiagnosticPolicyFromContext(nilParent).allowsApplicationPreview() {
		t.Fatal("narrow nil parent established preview rights")
	}

	boundedRoot, revokeBoundedRoot := BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	defer revokeBoundedRoot()
	current := boundedRoot
	var atBound context.Context
	revokes := make([]func(), 0, maxDiagnosticPolicyLiveAncestors+1)
	for index := range maxDiagnosticPolicyLiveAncestors + 1 {
		var revoke func()
		//nolint:fatcontext // The test deliberately constructs the bounded live lineage.
		current, revoke = NarrowDiagnosticPolicy(current, enabled)
		revokes = append(revokes, revoke)
		if index < maxDiagnosticPolicyLiveAncestors &&
			!DiagnosticPolicyFromContext(current).allowsApplicationPreview() {
			t.Fatalf("live ancestry disabled before bound at index %d", index)
		}
		if index == maxDiagnosticPolicyLiveAncestors-1 {
			atBound = current
		}
	}
	defer func() {
		for _, revoke := range revokes {
			revoke()
		}
	}()
	if DiagnosticPolicyFromContext(current).allowsApplicationPreview() {
		t.Fatal("live ancestry overflow did not fail closed")
	}
	rootPastBound, revokeRootPastBound := BindRootDiagnosticPolicy(atBound, enabled)
	defer revokeRootPastBound()
	if DiagnosticPolicyFromContext(rootPastBound).allowsApplicationPreview() {
		t.Fatal("nested root bind widened ancestry overflow")
	}
}

func TestNarrowDiagnosticPolicyConcurrentAncestorRevoke(t *testing.T) {
	enabled := NewDiagnosticPolicy(true, DEBUG)
	zero := DiagnosticPolicy{}
	root, revokeRoot := BindRootDiagnosticPolicy(context.Background(), enabled)
	narrowed, revokeNarrowed := NarrowDiagnosticPolicy(root, enabled)
	defer revokeNarrowed()

	const workers = 32
	start := make(chan struct{})
	sampled := make(chan struct{}, workers)
	var wait sync.WaitGroup
	var invalid atomic.Bool
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for iteration := 0; iteration < 1000; iteration++ {
				got := DiagnosticPolicyFromContext(narrowed)
				if got != enabled && got != zero {
					invalid.Store(true)
				}
				if iteration == 0 {
					sampled <- struct{}{}
				}
			}
		}()
	}
	close(start)
	for range workers {
		<-sampled
	}
	revokeRoot()
	wait.Wait()
	if invalid.Load() || DiagnosticPolicyFromContext(narrowed) != zero {
		t.Fatal("concurrent live ancestor revoke produced an invalid or active policy")
	}
}
