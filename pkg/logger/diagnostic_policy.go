package logger

import (
	"context"
	"sync"
	"sync/atomic"
)

// DiagnosticPolicy is an immutable, comparable capability captured from
// stored configuration. Its fields are intentionally private: callers may
// carry, compare, and narrow a policy, but cannot mutate or forge its fields
// except through the constructor. The zero value is invalid and safely
// disables application previews.
type DiagnosticPolicy struct {
	applicationPreviewEnabled bool
	valid                     bool
}

// NewDiagnosticPolicy captures preview admission from stored configuration.
// Runtime/CLI/environment log-level overrides must not be passed here. Raw
// application previews require both explicit permission and stored DEBUG.
func NewDiagnosticPolicy(logSensitiveData bool, storedLevel LogLevel) DiagnosticPolicy {
	return DiagnosticPolicy{
		applicationPreviewEnabled: logSensitiveData && storedLevel == DEBUG,
		valid:                     true,
	}
}

// Meet returns the most restrictive policy. Invalid and zero policies are
// disabled, so false always dominates and a nested caller cannot widen an
// origin capability.
func (policy DiagnosticPolicy) Meet(other DiagnosticPolicy) DiagnosticPolicy {
	return DiagnosticPolicy{
		applicationPreviewEnabled: policy.allowsApplicationPreview() &&
			other.allowsApplicationPreview(),
		valid: policy.valid && other.valid,
	}
}

func (policy DiagnosticPolicy) allowsApplicationPreview() bool {
	return policy.valid && policy.applicationPreviewEnabled
}

type diagnosticPolicyContextKey struct{}

const maxDiagnosticPolicyLiveAncestors = 64

type diagnosticPolicyBinding struct {
	effective     DiagnosticPolicy
	active        atomic.Bool
	liveAncestors []*diagnosticPolicyBinding
	liveLinked    bool
}

// BindRootDiagnosticPolicy establishes a revocable policy at a trusted runtime
// root. If a prior binding exists, its captured policy is still met so this
// function cannot widen an existing origin restriction.
func BindRootDiagnosticPolicy(
	parent context.Context,
	current DiagnosticPolicy,
) (context.Context, func()) {
	return bindDiagnosticPolicy(parent, current)
}

// RebindDiagnosticPolicy carries an origin capability into work whose
// cancellation/deadline comes from parent. A missing origin binding fails
// closed; detached work cannot establish preview rights. Effective policies
// from ordinary origin and parent bindings are retained after revocation.
// A broken live-linked NarrowDiagnosticPolicy binding instead remains a false
// cap and cannot be revived through rebind.
func RebindDiagnosticPolicy(
	parent context.Context,
	origin context.Context,
	current DiagnosticPolicy,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	effective := DiagnosticPolicy{}
	originBinding, originOK := diagnosticBindingFromContext(origin)
	if originOK {
		originEffective := diagnosticPolicyForRebind(originBinding)
		effective = originEffective.Meet(current)
		if parentBinding, parentOK := diagnosticBindingFromContext(parent); parentOK {
			effective = effective.Meet(diagnosticPolicyForRebind(parentBinding))
		}
	}
	return bindEffectiveDiagnosticPolicy(parent, effective)
}

func diagnosticPolicyForRebind(binding *diagnosticPolicyBinding) DiagnosticPolicy {
	if binding == nil || binding.liveLinked && !diagnosticBindingLive(binding) {
		return DiagnosticPolicy{}
	}
	return binding.effective
}

// NarrowDiagnosticPolicy derives a revocable synchronous child whose policy
// is capped by the current live parent binding. Unlike RebindDiagnosticPolicy,
// it retains a live link to parent revocation: revoking any linked ancestor
// makes lookup fail closed immediately. A missing parent binding cannot
// establish preview authority. Live ancestry is bounded and overflow fails
// closed.
func NarrowDiagnosticPolicy(
	parent context.Context,
	current DiagnosticPolicy,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}

	effective := DiagnosticPolicy{}
	var liveAncestors []*diagnosticPolicyBinding
	if prior, ok := diagnosticBindingFromContext(parent); ok {
		if diagnosticBindingLive(prior) {
			effective = prior.effective.Meet(current)
		}
		var bounded bool
		liveAncestors, bounded = appendDiagnosticLiveAncestor(nil, prior)
		if !bounded {
			effective = DiagnosticPolicy{}
		}
	}
	return bindEffectiveDiagnosticPolicyWithLineage(
		parent,
		effective,
		liveAncestors,
		true,
	)
}

func bindDiagnosticPolicy(
	parent context.Context,
	current DiagnosticPolicy,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}

	effective := current
	var liveAncestors []*diagnosticPolicyBinding
	liveLinked := false
	if prior, ok := parent.Value(diagnosticPolicyContextKey{}).(*diagnosticPolicyBinding); ok &&
		prior != nil {
		effective = prior.effective.Meet(current)
		if prior.liveLinked {
			liveLinked = true
			if !diagnosticBindingLive(prior) {
				effective = DiagnosticPolicy{}
			}
			var bounded bool
			liveAncestors, bounded = appendDiagnosticLiveAncestor(nil, prior)
			if !bounded {
				effective = DiagnosticPolicy{}
			}
		}
	}
	return bindEffectiveDiagnosticPolicyWithLineage(
		parent,
		effective,
		liveAncestors,
		liveLinked,
	)
}

func bindEffectiveDiagnosticPolicy(
	parent context.Context,
	effective DiagnosticPolicy,
) (context.Context, func()) {
	return bindEffectiveDiagnosticPolicyWithLineage(parent, effective, nil, false)
}

func bindEffectiveDiagnosticPolicyWithLineage(
	parent context.Context,
	effective DiagnosticPolicy,
	liveAncestors []*diagnosticPolicyBinding,
	liveLinked bool,
) (context.Context, func()) {
	binding := &diagnosticPolicyBinding{
		effective:     effective,
		liveAncestors: liveAncestors,
		liveLinked:    liveLinked,
	}
	binding.active.Store(true)
	child := context.WithValue(parent, diagnosticPolicyContextKey{}, binding)
	var once sync.Once
	return child, func() {
		once.Do(func() {
			binding.active.Store(false)
		})
	}
}

func appendDiagnosticLiveAncestor(
	destination []*diagnosticPolicyBinding,
	parent *diagnosticPolicyBinding,
) ([]*diagnosticPolicyBinding, bool) {
	if parent == nil || len(parent.liveAncestors)+1 > maxDiagnosticPolicyLiveAncestors {
		return nil, false
	}
	destination = append(destination, parent.liveAncestors...)
	destination = append(destination, parent)
	return destination, true
}

func diagnosticBindingLive(binding *diagnosticPolicyBinding) bool {
	if binding == nil || !binding.active.Load() {
		return false
	}
	for _, ancestor := range binding.liveAncestors {
		if ancestor == nil || !ancestor.active.Load() {
			return false
		}
	}
	return true
}

func diagnosticBindingFromContext(ctx context.Context) (*diagnosticPolicyBinding, bool) {
	if ctx == nil {
		return nil, false
	}
	binding, ok := ctx.Value(diagnosticPolicyContextKey{}).(*diagnosticPolicyBinding)
	return binding, ok && binding != nil
}

// DiagnosticPolicyFromContext returns the latest active binding. Missing,
// malformed, or explicitly revoked bindings return the disabled zero policy.
// Context cancellation alone does not revoke a generation policy lease.
func DiagnosticPolicyFromContext(ctx context.Context) DiagnosticPolicy {
	binding, ok := diagnosticBindingFromContext(ctx)
	if !ok || !diagnosticBindingLive(binding) {
		return DiagnosticPolicy{}
	}
	return binding.effective
}
