// Package attention prepares trusted user-attention gate policies and runs
// their private workflows without depending on a particular product domain.
package attention

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	preparedPolicyV1       = 1
	maxPolicyRevisionBytes = 256

	// MaxPreparedPolicyBytes preserves the gate compiler's complete input
	// allowance while independently bounding the provenance envelope around it.
	MaxPreparedPolicyBytes = workflows.MaxWorkflowGateInputsBytes + (1 << 20)
)

var (
	ErrInvalidPolicy       = errors.New("invalid attention policy")
	ErrInvalidPolicySource = errors.New("invalid attention policy source")
	ErrPolicyChanged       = errors.New("attention policy changed")
)

// PolicySelector identifies policy in a trusted source. It carries no policy
// material itself.
type PolicySelector struct {
	Repository    string
	DecisionPoint string
}

// PolicySnapshot is one source-owned global layer and optional repository
// override. Revision identifies the exact source generation.
type PolicySnapshot struct {
	Revision   string
	Global     []workflows.GateSpec
	Repository *workflows.RepositoryGatePolicy
}

// PolicyUse runs synchronously while the source's immutable snapshot lease is
// held. A source must invoke it exactly once and propagate its result.
type PolicyUse func(context.Context, PolicySnapshot) error

// PolicySource is the trusted policy/configuration boundary.
type PolicySource interface {
	WithAttentionPolicy(
		ctx context.Context,
		selector PolicySelector,
		use PolicyUse,
	) error
}

// PolicySourceFunc adapts a function to PolicySource.
type PolicySourceFunc func(context.Context, PolicySelector, PolicyUse) error

func (source PolicySourceFunc) WithAttentionPolicy(
	ctx context.Context,
	selector PolicySelector,
	use PolicyUse,
) error {
	if source == nil {
		return ErrInvalidPolicySource
	}
	return source(ctx, selector, use)
}

// PreparedPolicy is an opaque, canonical policy snapshot suitable for durable
// pinning. Its accessors always detach mutable values.
type PreparedPolicy struct {
	canonical        []byte
	sourceRevision   string
	decisionRevision string
	resolution       *workflows.GatePolicyResolution
}

type preparedPolicyEnvelope struct {
	Version          int                             `json:"version"`
	SourceRevision   string                          `json:"source_revision"`
	DecisionRevision string                          `json:"decision_revision"`
	Resolution       *workflows.GatePolicyResolution `json:"resolution"`
}

// PreparePolicy captures one policy through its trusted source and returns a
// canonical detached snapshot.
func PreparePolicy(
	ctx context.Context,
	source PolicySource,
	selector PolicySelector,
) (PreparedPolicy, error) {
	resolved, err := captureResolvedPolicy(ctx, source, selector)
	if err != nil {
		return PreparedPolicy{}, err
	}
	return encodeResolvedPolicy(resolved)
}

// PrepareSnapshot resolves and canonically prepares a detached snapshot. It is
// intended for trusted source adapters and compatibility boundaries.
func PrepareSnapshot(snapshot PolicySnapshot) (PreparedPolicy, error) {
	resolved, err := resolvePolicy(snapshot)
	if err != nil {
		return PreparedPolicy{}, err
	}
	return encodeResolvedPolicy(resolved)
}

// DecodePreparedPolicy validates exact canonical bytes and returns an opaque
// detached policy. Non-canonical JSON is rejected.
func DecodePreparedPolicy(raw []byte) (PreparedPolicy, error) {
	if len(raw) == 0 || len(raw) > MaxPreparedPolicyBytes ||
		!bytes.Equal(raw, bytes.TrimSpace(raw)) {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var envelope preparedPolicyEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	if envelope.Version != preparedPolicyV1 ||
		!validSourceRevision(envelope.SourceRevision) ||
		validateResolution(envelope.Resolution) != nil {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	decisionRevision, err := decisionRevision(
		envelope.SourceRevision,
		envelope.Resolution,
	)
	if err != nil || envelope.DecisionRevision != decisionRevision {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(raw, canonical) {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	resolution, err := cloneResolution(envelope.Resolution)
	if err != nil {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	return PreparedPolicy{
		canonical:        append([]byte(nil), canonical...),
		sourceRevision:   envelope.SourceRevision,
		decisionRevision: envelope.DecisionRevision,
		resolution:       resolution,
	}, nil
}

// Canonical returns a detached copy of the exact durable policy envelope.
func (policy PreparedPolicy) Canonical() []byte {
	return append([]byte(nil), policy.canonical...)
}

func (policy PreparedPolicy) SourceRevision() string {
	return policy.sourceRevision
}

func (policy PreparedPolicy) DecisionRevision() string {
	return policy.decisionRevision
}

// Resolution returns a detached copy of the effective policy and provenance.
func (policy PreparedPolicy) Resolution() *workflows.GatePolicyResolution {
	resolution, err := cloneResolution(policy.resolution)
	if err != nil {
		return nil
	}
	return resolution
}

// EffectiveGates returns the ordered effective gate composition.
func (policy PreparedPolicy) EffectiveGates() []workflows.GateSpec {
	resolution := policy.Resolution()
	if resolution == nil {
		return nil
	}
	return resolution.Effective
}

// IsNoop reports whether every effective entry is a zero gate.
func (policy PreparedPolicy) IsNoop() bool {
	if !policy.valid() {
		return false
	}
	for _, spec := range policy.resolution.Effective {
		if spec.Kind != workflows.GateZero {
			return false
		}
	}
	return true
}

// WorkingContextAgentID returns the canonical agent needed by a working-model
// gate, or an empty string when the composition is session-independent.
func (policy PreparedPolicy) WorkingContextAgentID() string {
	if !policy.valid() {
		return ""
	}
	for _, spec := range policy.resolution.Effective {
		if spec.Kind == workflows.GateAIWorkingContext {
			return spec.AgentID
		}
	}
	return ""
}

func (policy PreparedPolicy) valid() bool {
	if len(policy.canonical) == 0 || policy.sourceRevision == "" ||
		policy.decisionRevision == "" || policy.resolution == nil {
		return false
	}
	decoded, err := DecodePreparedPolicy(policy.canonical)
	return err == nil && decoded.sourceRevision == policy.sourceRevision &&
		decoded.decisionRevision == policy.decisionRevision &&
		reflect.DeepEqual(decoded.resolution, policy.resolution)
}

type resolvedPolicy struct {
	sourceRevision   string
	decisionRevision string
	resolution       *workflows.GatePolicyResolution
}

// policyCallbackGuard enforces the PolicySource lease contract even when a
// broken source retains use and invokes it after WithAttentionPolicy returns.
// The atomic return marker closes the gap between the source returning and the
// caller acquiring the mutex to finish the lease.
type policyCallbackGuard struct {
	mu             sync.Mutex
	sourceReturned atomic.Bool
	closed         bool
	called         bool
	callbackErr    error
}

func (guard *policyCallbackGuard) invoke(operation func() error) error {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.closed || guard.sourceReturned.Load() {
		return ErrInvalidPolicySource
	}
	if guard.called || operation == nil {
		guard.callbackErr = ErrInvalidPolicySource
		return guard.callbackErr
	}
	guard.called = true
	operationErr := operation()
	// A callback that outlives its source lease is invalid even when it began
	// just before the source returned.
	if guard.sourceReturned.Load() {
		operationErr = ErrInvalidPolicySource
	}
	if operationErr != nil {
		guard.callbackErr = operationErr
	}
	return operationErr
}

func (guard *policyCallbackGuard) finish(sourceErr error) error {
	guard.sourceReturned.Store(true)
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.closed = true
	if guard.callbackErr != nil {
		return guard.callbackErr
	}
	if sourceErr != nil {
		return sourceErr
	}
	if !guard.called {
		return ErrInvalidPolicySource
	}
	return nil
}

func captureResolvedPolicy(
	ctx context.Context,
	source PolicySource,
	selector PolicySelector,
) (resolvedPolicy, error) {
	if source == nil || nilInterface(source) {
		return resolvedPolicy{}, ErrInvalidPolicySource
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return resolvedPolicy{}, err
	}
	var captured resolvedPolicy
	guard := &policyCallbackGuard{}
	sourceErr := source.WithAttentionPolicy(
		ctx,
		selector,
		func(policyCtx context.Context, snapshot PolicySnapshot) error {
			return guard.invoke(func() error {
				if policyCtx == nil {
					return ErrInvalidPolicySource
				}
				if contextErr := policyCtx.Err(); contextErr != nil {
					return contextErr
				}
				resolved, resolveErr := resolvePolicy(snapshot)
				if resolveErr != nil {
					return resolveErr
				}
				captured = resolved
				return nil
			})
		},
	)
	// Preserve callback failure independently: a buggy source cannot swallow a
	// rejected lease snapshot and turn it into launch authority.
	if err := guard.finish(sourceErr); err != nil {
		return resolvedPolicy{}, err
	}
	if captured.resolution == nil {
		return resolvedPolicy{}, ErrInvalidPolicySource
	}
	return captured, nil
}

func resolvePolicy(snapshot PolicySnapshot) (resolvedPolicy, error) {
	if !validSourceRevision(snapshot.Revision) {
		return resolvedPolicy{}, ErrInvalidPolicy
	}
	resolution, err := workflows.ResolveGatePolicy(snapshot.Global, snapshot.Repository)
	if err != nil {
		return resolvedPolicy{}, errors.Join(ErrInvalidPolicy, err)
	}
	revision, err := decisionRevision(snapshot.Revision, resolution)
	if err != nil {
		return resolvedPolicy{}, errors.Join(ErrInvalidPolicy, err)
	}
	return resolvedPolicy{
		sourceRevision:   snapshot.Revision,
		decisionRevision: revision,
		resolution:       resolution,
	}, nil
}

func encodeResolvedPolicy(policy resolvedPolicy) (PreparedPolicy, error) {
	revision, err := decisionRevision(policy.sourceRevision, policy.resolution)
	if err != nil || revision != policy.decisionRevision ||
		validateResolution(policy.resolution) != nil {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	canonical, err := json.Marshal(preparedPolicyEnvelope{
		Version:          preparedPolicyV1,
		SourceRevision:   policy.sourceRevision,
		DecisionRevision: policy.decisionRevision,
		Resolution:       policy.resolution,
	})
	if err != nil || len(canonical) == 0 || len(canonical) > MaxPreparedPolicyBytes {
		return PreparedPolicy{}, ErrInvalidPolicy
	}
	return DecodePreparedPolicy(canonical)
}

func decisionRevision(
	sourceRevision string,
	resolution *workflows.GatePolicyResolution,
) (string, error) {
	if !validSourceRevision(sourceRevision) || resolution == nil {
		return "", ErrInvalidPolicy
	}
	canonical, err := json.Marshal(struct {
		Version    int                             `json:"version"`
		Revision   string                          `json:"source_revision"`
		Resolution *workflows.GatePolicyResolution `json:"resolution"`
	}{
		Version:    preparedPolicyV1,
		Revision:   sourceRevision,
		Resolution: resolution,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validSourceRevision(revision string) bool {
	return revision != "" && revision == strings.TrimSpace(revision) &&
		utf8.ValidString(revision) && len(revision) <= maxPolicyRevisionBytes
}

func validateResolution(resolution *workflows.GatePolicyResolution) error {
	if resolution == nil || len(resolution.Effective) > workflows.MaxWorkflowGateCount ||
		len(resolution.Entries) != len(resolution.Effective) {
		return ErrInvalidPolicy
	}
	if _, err := workflows.ResolveGatePolicy(resolution.Effective, nil); err != nil {
		return errors.Join(ErrInvalidPolicy, err)
	}
	switch resolution.Mode {
	case workflows.GatePolicyInherit, workflows.GatePolicyOverlay,
		workflows.GatePolicyReplace, workflows.GatePolicyDisable:
	default:
		return ErrInvalidPolicy
	}
	if resolution.Mode == workflows.GatePolicyDisable && len(resolution.Effective) != 0 {
		return ErrInvalidPolicy
	}
	for index, entry := range resolution.Entries {
		if entry.ID != resolution.Effective[index].ID || entry.EffectivePosition != index+1 ||
			entry.GlobalPosition < 0 || entry.GlobalPosition > workflows.MaxWorkflowGateCount ||
			entry.RepositoryPosition < 0 ||
			entry.RepositoryPosition > workflows.MaxWorkflowGateCount {
			return ErrInvalidPolicy
		}
		switch resolution.Mode {
		case workflows.GatePolicyInherit:
			if entry.Action != workflows.GatePolicyResolutionInherited ||
				entry.GlobalPosition != index+1 || entry.RepositoryPosition != 0 {
				return ErrInvalidPolicy
			}
		case workflows.GatePolicyReplace:
			if entry.Action != workflows.GatePolicyResolutionSelected ||
				entry.GlobalPosition != 0 || entry.RepositoryPosition != index+1 {
				return ErrInvalidPolicy
			}
		case workflows.GatePolicyOverlay:
			switch entry.Action {
			case workflows.GatePolicyResolutionInherited:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition != 0 {
					return ErrInvalidPolicy
				}
			case workflows.GatePolicyResolutionReplaced:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition == 0 {
					return ErrInvalidPolicy
				}
			case workflows.GatePolicyResolutionTombstoned:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition == 0 ||
					resolution.Effective[index].Kind != workflows.GateZero {
					return ErrInvalidPolicy
				}
			case workflows.GatePolicyResolutionAppended:
				if entry.GlobalPosition != 0 || entry.RepositoryPosition == 0 {
					return ErrInvalidPolicy
				}
			default:
				return ErrInvalidPolicy
			}
		}
	}
	return nil
}

func cloneResolution(
	resolution *workflows.GatePolicyResolution,
) (*workflows.GatePolicyResolution, error) {
	if validateResolution(resolution) != nil {
		return nil, ErrInvalidPolicy
	}
	encoded, err := json.Marshal(resolution)
	if err != nil {
		return nil, ErrInvalidPolicy
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var cloned workflows.GatePolicyResolution
	if err = decoder.Decode(&cloned); err != nil || validateResolution(&cloned) != nil {
		return nil, ErrInvalidPolicy
	}
	return &cloned, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
