package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	reviewAttentionWorkflowName = sharedattention.WorkflowName
	reviewAttentionWorkflowRef  = sharedattention.WorkflowRef
	maxAttentionDecisionBytes   = 128
	maxPreparedAttentionBytes   = sharedattention.MaxPreparedPolicyBytes
)

var attentionDecisionPattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

// AttentionPolicySelector identifies one trusted policy snapshot. Repository
// always comes from the authoritative review case; callers cannot select a
// different repository policy for a case.
type AttentionPolicySelector struct {
	Repository    string
	DecisionPoint string
}

// AttentionPolicySnapshot is one source-owned global policy and optional
// repository override. Revision identifies the exact source generation. The
// launcher additionally hashes the detached effective resolution so a source
// cannot reuse a revision for different policy bytes without changing the
// durable decision identity.
type AttentionPolicySnapshot struct {
	Revision   string
	Global     []workflows.GateSpec
	Repository *workflows.RepositoryGatePolicy
}

// AttentionPolicyUse runs while the source's immutable snapshot lease is held.
// A source must invoke the callback exactly once and synchronously.
type AttentionPolicyUse func(context.Context, AttentionPolicySnapshot) error

// AttentionPolicySource is the trusted policy/configuration boundary. Policy
// gates and revisions are intentionally not accepted in AttentionLaunchRequest.
type AttentionPolicySource interface {
	WithReviewAttentionPolicy(
		ctx context.Context,
		selector AttentionPolicySelector,
		use AttentionPolicyUse,
	) error
}

type AttentionLauncherConfig struct {
	Service  *Service
	Executor *workflows.Executor
	Policies AttentionPolicySource
}

// AttentionLauncher remains the source-compatible review adapter around the
// shared private attention workflow runner.
type AttentionLauncher struct {
	service   *Service
	executor  *workflows.Executor
	policies  AttentionPolicySource
	decisions eventing.ReviewDecisionRunStore
	runs      workflows.RunStore
	shared    *sharedattention.PrivateRunner
}

type AttentionLaunchRequest struct {
	CaseID              string
	ExpectedCaseVersion int64
	DecisionPoint       string
}

// AttentionLaunchResult is deliberately narrow. Gate subjects, policy source
// revisions, session capabilities, model diagnostics, and private outputs are
// unrepresentable here.
type AttentionLaunchResult struct {
	CaseID         string `json:"case_id"`
	CaseVersion    int64  `json:"case_version"`
	DecisionPoint  string `json:"decision_point"`
	PolicyRevision string `json:"policy_revision"`
	RunID          string `json:"run_id,omitempty"`
	Status         string `json:"status,omitempty"`
	Existing       bool   `json:"existing,omitempty"`
	Noop           bool   `json:"noop,omitempty"`
}

// These package-private compatibility shapes are retained because the durable
// trigger and attention bridge intentionally cannot accept arbitrary prepared
// policy bytes from an external API caller.
type resolvedAttentionPolicy struct {
	sourceRevision   string
	decisionRevision string
	resolution       *workflows.GatePolicyResolution
	shared           sharedattention.PreparedPolicy
}

type preparedAttentionPolicy struct {
	canonical []byte
}

func NewAttentionLauncher(config AttentionLauncherConfig) (*AttentionLauncher, error) {
	if config.Service == nil || config.Service.store == nil ||
		isNilWorkingContextValue(config.Service.store) {
		return nil, errors.New("review attention service is required")
	}
	if config.Executor == nil {
		return nil, errors.New("review attention workflow executor is required")
	}
	if config.Policies == nil || isNilWorkingContextValue(config.Policies) {
		return nil, errors.New("review attention policy source is required")
	}
	decisions, ok := config.Service.store.(eventing.ReviewDecisionRunStore)
	if !ok || isNilWorkingContextValue(decisions) {
		return nil, errors.New("review store does not support attention decisions")
	}
	runs := config.Executor.Store
	if runs == nil || isNilWorkingContextValue(runs) {
		runs = workflows.NewFileRunStore(config.Executor.WorkspaceDir)
	}
	policyAdapter := reviewAttentionPolicyAdapter{source: config.Policies}
	shared, err := sharedattention.NewPrivateRunner(sharedattention.PrivateRunnerConfig{
		Executor:                         config.Executor,
		Runs:                             runs,
		Policies:                         policyAdapter,
		Decisions:                        reviewAttentionDecisionBinding{store: decisions},
		ProjectLinkedAdmissionQuarantine: true,
	})
	if err != nil {
		return nil, errors.New("review attention workflow runner is unavailable")
	}
	return &AttentionLauncher{
		service:   config.Service,
		executor:  config.Executor,
		policies:  config.Policies,
		decisions: decisions,
		runs:      runs,
		shared:    shared,
	}, nil
}

func (launcher *AttentionLauncher) Launch(
	ctx context.Context,
	request AttentionLaunchRequest,
) (AttentionLaunchResult, error) {
	prepared, err := launcher.capturePreparedAttentionPolicy(ctx, request, false)
	if err != nil {
		return AttentionLaunchResult{}, err
	}
	return launcher.launchPreparedAttentionPolicy(ctx, request, prepared, true)
}

// prepareAttentionPolicy captures policy only through the trusted source and
// returns its canonical package-private envelope. The trigger worker persists
// canonical before it calls launchPreparedAttentionPolicy.
func (launcher *AttentionLauncher) prepareAttentionPolicy(
	ctx context.Context,
	request AttentionLaunchRequest,
) (preparedAttentionPolicy, error) {
	return launcher.capturePreparedAttentionPolicy(ctx, request, true)
}

func (launcher *AttentionLauncher) capturePreparedAttentionPolicy(
	ctx context.Context,
	request AttentionLaunchRequest,
	requireCurrentVersion bool,
) (preparedAttentionPolicy, error) {
	if !launcher.available() {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttentionLaunchRequest(request); err != nil {
		return preparedAttentionPolicy{}, err
	}
	detail, err := launcher.service.store.GetReviewCase(ctx, request.CaseID)
	if err != nil {
		return preparedAttentionPolicy{}, sanitizeAttentionError(ctx, err, false)
	}
	if validationErr := validateWorkingContextDetail(request.CaseID, detail); validationErr != nil {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	if requireCurrentVersion && detail.Case.Version != request.ExpectedCaseVersion {
		return preparedAttentionPolicy{}, workflows.ErrRunAdmissionConflict
	}
	prepared, err := sharedattention.PreparePolicy(
		ctx,
		reviewAttentionPolicyAdapter{source: launcher.policies},
		sharedattention.PolicySelector{
			Repository:    detail.Case.Repository,
			DecisionPoint: request.DecisionPoint,
		},
	)
	if err != nil {
		return preparedAttentionPolicy{}, sanitizeAttentionError(ctx, err, false)
	}
	return preparedAttentionPolicy{canonical: prepared.Canonical()}, nil
}

// launchPreparedAttentionPolicy is deliberately package-private. Durable
// trigger delivery passes revalidateLive=false because the canonical snapshot
// was already pinned under the trigger lease. Normal Launch passes true and
// retains the live policy fence at decision-run admission.
func (launcher *AttentionLauncher) launchPreparedAttentionPolicy(
	ctx context.Context,
	request AttentionLaunchRequest,
	prepared preparedAttentionPolicy,
	revalidateLive bool,
) (AttentionLaunchResult, error) {
	if !launcher.available() {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttentionLaunchRequest(request); err != nil {
		return AttentionLaunchResult{}, err
	}
	policy, err := decodePreparedAttentionPolicy(prepared.canonical)
	if err != nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	detail, err := launcher.service.store.GetReviewCase(ctx, request.CaseID)
	if err != nil {
		return AttentionLaunchResult{}, sanitizeAttentionError(ctx, err, false)
	}
	if validationErr := validateWorkingContextDetail(request.CaseID, detail); validationErr != nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	selector := AttentionPolicySelector{
		Repository:    detail.Case.Repository,
		DecisionPoint: request.DecisionPoint,
	}
	key := eventing.ReviewDecisionKey{
		CaseID:         request.CaseID,
		CaseVersion:    request.ExpectedCaseVersion,
		DecisionPoint:  request.DecisionPoint,
		PolicyRevision: policy.decisionRevision,
	}
	decisionKey, err := reviewAttentionDecisionKey(key)
	if err != nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	baseResult := AttentionLaunchResult{
		CaseID:         key.CaseID,
		CaseVersion:    key.CaseVersion,
		DecisionPoint:  key.DecisionPoint,
		PolicyRevision: key.PolicyRevision,
	}

	if existing, found, lookupErr := launcher.shared.FindExisting(ctx, decisionKey); lookupErr != nil {
		// An exact unlinked deterministic run can be either an orphan or a
		// concurrent create-before-link window. Continue only to ordinary
		// serialized admission: PrivateRunner.Launch converges the live creator
		// and quarantines a true orphan without executing a second workflow.
		if !errors.Is(lookupErr, sharedattention.ErrPrivateRunAdmissionUncertain) {
			return AttentionLaunchResult{}, sanitizeAttentionError(ctx, lookupErr, false)
		}
	} else if found {
		return attentionResultForShared(baseResult, existing), nil
	}
	// Historical exact duplicates above remain discoverable after the case has
	// advanced. A genuinely new decision must bind the current exact version.
	if detail.Case.Version != request.ExpectedCaseVersion {
		return AttentionLaunchResult{}, workflows.ErrRunAdmissionConflict
	}

	sharedRequest := sharedattention.PrivateLaunchRequest{
		DecisionKey: decisionKey,
		Policy:      policy.shared,
		Selector: sharedattention.PolicySelector{
			Repository:    selector.Repository,
			DecisionPoint: selector.DecisionPoint,
		},
		RevalidateLive: revalidateLive,
	}
	if policy.shared.IsNoop() {
		return launcher.launchShared(ctx, baseResult, sharedRequest)
	}

	workingAgentID := policy.shared.WorkingContextAgentID()
	if workingAgentID == "" {
		subject, subjectErr := workingContextGateSubject(detail)
		if subjectErr != nil {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		sharedRequest.Subject = subject
		return launcher.launchShared(ctx, baseResult, sharedRequest)
	}

	var result AttentionLaunchResult
	err = launcher.service.WithWorkingContext(
		ctx,
		WorkingContextRequest{CaseID: request.CaseID, AgentID: workingAgentID},
		func(runtimeCtx context.Context, working WorkingContext) error {
			if working.CaseVersion != request.ExpectedCaseVersion ||
				working.AgentID != workingAgentID || working.SessionKey == "" ||
				working.SessionRevision == "" {
				return workflows.ErrRunAdmissionConflict
			}
			sharedRequest.Subject = working.GateSubject
			sharedRequest.ReadOnlySession = &workflows.ReadOnlySessionRef{
				AgentID:          working.AgentID,
				Session:          working.SessionKey,
				ExpectedRevision: working.SessionRevision,
			}
			var launchErr error
			result, launchErr = launcher.launchShared(runtimeCtx, baseResult, sharedRequest)
			return launchErr
		},
	)
	if err != nil {
		safeErr := sanitizeAttentionError(ctx, err, true)
		if result.RunID != "" && result.Status != "" {
			return result, safeErr
		}
		return AttentionLaunchResult{}, safeErr
	}
	return result, nil
}

func (launcher *AttentionLauncher) launchShared(
	ctx context.Context,
	base AttentionLaunchResult,
	request sharedattention.PrivateLaunchRequest,
) (AttentionLaunchResult, error) {
	launched, err := launcher.shared.Launch(ctx, request)
	if err != nil {
		safeErr := sanitizeAttentionError(ctx, err, true)
		if launched.RunID != "" && launched.Status != "" {
			return attentionResultForShared(base, launched), safeErr
		}
		return AttentionLaunchResult{}, safeErr
	}
	return attentionResultForShared(base, launched), nil
}

func (launcher *AttentionLauncher) available() bool {
	return launcher != nil && launcher.service != nil && launcher.executor != nil &&
		launcher.policies != nil && launcher.decisions != nil && launcher.runs != nil &&
		launcher.shared != nil && launcher.shared.Available()
}

func validateAttentionLaunchRequest(request AttentionLaunchRequest) error {
	if !validWorkingContextPrefixedHexID(request.CaseID, "prc_") ||
		request.ExpectedCaseVersion <= 0 ||
		!validAttentionDecisionPoint(request.DecisionPoint) {
		return ErrInvalidRequest
	}
	return nil
}

func resolveAttentionPolicy(
	snapshot AttentionPolicySnapshot,
) (resolvedAttentionPolicy, error) {
	prepared, err := sharedattention.PrepareSnapshot(sharedattention.PolicySnapshot{
		Revision:   snapshot.Revision,
		Global:     snapshot.Global,
		Repository: snapshot.Repository,
	})
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	return resolvedAttentionPolicyForShared(prepared)
}

func resolvedAttentionPolicyForShared(
	prepared sharedattention.PreparedPolicy,
) (resolvedAttentionPolicy, error) {
	resolution := prepared.Resolution()
	if resolution == nil || prepared.SourceRevision() == "" ||
		prepared.DecisionRevision() == "" || len(prepared.Canonical()) == 0 {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	return resolvedAttentionPolicy{
		sourceRevision:   prepared.SourceRevision(),
		decisionRevision: prepared.DecisionRevision(),
		resolution:       resolution,
		shared:           prepared,
	}, nil
}

func encodePreparedAttentionPolicy(
	policy resolvedAttentionPolicy,
) (preparedAttentionPolicy, error) {
	if policy.sourceRevision != policy.shared.SourceRevision() ||
		policy.decisionRevision != policy.shared.DecisionRevision() ||
		!reflect.DeepEqual(policy.resolution, policy.shared.Resolution()) {
		return preparedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	canonical := policy.shared.Canonical()
	if len(canonical) == 0 || len(canonical) > maxPreparedAttentionBytes {
		return preparedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	return preparedAttentionPolicy{canonical: canonical}, nil
}

func decodePreparedAttentionPolicy(raw []byte) (resolvedAttentionPolicy, error) {
	prepared, err := sharedattention.DecodePreparedPolicy(raw)
	if err != nil {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	return resolvedAttentionPolicyForShared(prepared)
}

func attentionPolicyIsNoop(specs []workflows.GateSpec) bool {
	for _, spec := range specs {
		if spec.Kind != workflows.GateZero {
			return false
		}
	}
	return true
}

func attentionRunID(key eventing.ReviewDecisionKey) (string, error) {
	decisionKey, err := reviewAttentionDecisionKey(key)
	if err != nil {
		return "", err
	}
	return sharedattention.RunIDForDecisionKey(decisionKey)
}

func reviewAttentionDecisionKey(key eventing.ReviewDecisionKey) (string, error) {
	return sharedattention.CanonicalDecisionKey(key)
}

func attentionResultForShared(
	base AttentionLaunchResult,
	result sharedattention.PrivateLaunchResult,
) AttentionLaunchResult {
	base.RunID = result.RunID
	base.Status = result.Status
	base.Existing = result.Existing
	base.Noop = result.Noop
	return base
}

type reviewAttentionPolicyAdapter struct {
	source AttentionPolicySource
}

func (adapter reviewAttentionPolicyAdapter) WithAttentionPolicy(
	ctx context.Context,
	selector sharedattention.PolicySelector,
	use sharedattention.PolicyUse,
) error {
	if adapter.source == nil || isNilWorkingContextValue(adapter.source) {
		return ErrUnavailable
	}
	return adapter.source.WithReviewAttentionPolicy(
		ctx,
		AttentionPolicySelector{
			Repository: selector.Repository, DecisionPoint: selector.DecisionPoint,
		},
		func(policyCtx context.Context, snapshot AttentionPolicySnapshot) error {
			if use == nil {
				return errors.New("review attention policy callback is required")
			}
			return use(policyCtx, sharedattention.PolicySnapshot{
				Revision:   snapshot.Revision,
				Global:     snapshot.Global,
				Repository: snapshot.Repository,
			})
		},
	)
}

type reviewAttentionDecisionBinding struct {
	store eventing.ReviewDecisionRunStore
}

func (binding reviewAttentionDecisionBinding) Find(
	ctx context.Context,
	rawKey string,
) (string, bool, error) {
	key, err := decodeReviewAttentionDecisionKey(rawKey)
	if err != nil {
		return "", false, ErrUnavailable
	}
	link, err := binding.store.GetReviewDecisionRun(ctx, key)
	if errors.Is(err, eventing.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if link.Key != key || link.RunID == "" {
		return "", false, ErrUnavailable
	}
	return link.RunID, true, nil
}

func (binding reviewAttentionDecisionBinding) Admit(
	ctx context.Context,
	rawKey string,
	create func(context.Context) error,
) (string, bool, error) {
	key, err := decodeReviewAttentionDecisionKey(rawKey)
	if err != nil {
		return "", false, ErrUnavailable
	}
	runID, err := sharedattention.RunIDForDecisionKey(rawKey)
	if err != nil {
		return "", false, ErrUnavailable
	}
	link, existed, err := binding.store.AdmitReviewDecisionRun(
		ctx,
		eventing.ReviewDecisionRunAdmission{Key: key, RunID: runID},
		create,
	)
	if err != nil {
		if errors.Is(err, eventing.ErrReviewDecisionAdmissionUncertain) {
			return "", false, sharedattention.ErrPrivateRunAdmissionUncertain
		}
		if errors.Is(err, eventing.ErrReviewConflict) || errors.Is(err, eventing.ErrNotFound) {
			return "", false, workflows.ErrRunAdmissionConflict
		}
		return "", false, err
	}
	if link.Key != key || link.RunID != runID {
		return "", false, ErrUnavailable
	}
	return link.RunID, existed, nil
}

func decodeReviewAttentionDecisionKey(raw string) (eventing.ReviewDecisionKey, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var key eventing.ReviewDecisionKey
	if err := decoder.Decode(&key); err != nil {
		return eventing.ReviewDecisionKey{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return eventing.ReviewDecisionKey{}, errors.New("invalid review attention key")
	}
	canonical, err := json.Marshal(key)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return eventing.ReviewDecisionKey{}, errors.New("invalid review attention key")
	}
	return key, nil
}

func sanitizeAttentionError(ctx context.Context, err error, admission bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sharedattention.ErrPrivateRunAdmissionUncertain) {
		// Retain the established unavailable classification while allowing
		// trusted workers to distinguish a durable quarantined admission.
		return errors.Join(
			workflows.ErrRunAdmissionUnavailable,
			sharedattention.ErrPrivateRunAdmissionUncertain,
		)
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, workflows.ErrRunAdmissionConflict):
		return workflows.ErrRunAdmissionConflict
	case errors.Is(err, workflows.ErrRunAdmissionUnavailable):
		return workflows.ErrRunAdmissionUnavailable
	case errors.Is(err, workflows.ErrPrivateWorkflowContext):
		return workflows.ErrPrivateWorkflowContext
	case errors.Is(err, workflows.ErrPrivateWorkflowFailed):
		return workflows.ErrPrivateWorkflowFailed
	case errors.Is(err, workflows.ErrHumanTaskConflict):
		return workflows.ErrHumanTaskConflict
	case errors.Is(err, workflows.ErrRunCanceled):
		return workflows.ErrRunCanceled
	case admission && (errors.Is(err, eventing.ErrReviewConflict) ||
		errors.Is(err, eventing.ErrNotFound)):
		return workflows.ErrRunAdmissionConflict
	default:
		return ErrUnavailable
	}
}
