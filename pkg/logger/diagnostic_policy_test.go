package logger

import (
	"context"
	"sync"
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
