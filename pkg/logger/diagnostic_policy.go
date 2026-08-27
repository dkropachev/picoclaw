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

type diagnosticPolicyBinding struct {
	effective DiagnosticPolicy
	active    atomic.Bool
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
// from origin and parent are met even after either lease was revoked.
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
		effective = originBinding.effective.Meet(current)
		if parentBinding, parentOK := diagnosticBindingFromContext(parent); parentOK {
			effective = effective.Meet(parentBinding.effective)
		}
	}
	return bindEffectiveDiagnosticPolicy(parent, effective)
}

func bindDiagnosticPolicy(
	parent context.Context,
	current DiagnosticPolicy,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}

	effective := current
	if prior, ok := parent.Value(diagnosticPolicyContextKey{}).(*diagnosticPolicyBinding); ok &&
		prior != nil {
		effective = prior.effective.Meet(current)
	}
	return bindEffectiveDiagnosticPolicy(parent, effective)
}

func bindEffectiveDiagnosticPolicy(
	parent context.Context,
	effective DiagnosticPolicy,
) (context.Context, func()) {
	binding := &diagnosticPolicyBinding{effective: effective}
	binding.active.Store(true)
	child := context.WithValue(parent, diagnosticPolicyContextKey{}, binding)
	var once sync.Once
	return child, func() {
		once.Do(func() {
			binding.active.Store(false)
		})
	}
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
	if !ok || !binding.active.Load() {
		return DiagnosticPolicy{}
	}
	return binding.effective
}
