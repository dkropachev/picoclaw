package reviews

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	reviewAttentionWorkflowName = "Review attention gates"
	reviewAttentionWorkflowRef  = "inline/review-attention-gates/v1"
	maxAttentionDecisionBytes   = 128
	maxAttentionRevisionBytes   = 256
	preparedAttentionPolicyV1   = 1
	// A prepared envelope contains the effective gate inputs plus stable
	// provenance, source identity, and its decision digest. Keep that wrapper
	// independently bounded without reducing the workflow compiler's existing
	// 2 MiB input contract for custom trusted policy sources.
	maxPreparedAttentionBytes = workflows.MaxWorkflowGateInputsBytes + (1 << 20)
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

// AttentionLauncher starts one private gate workflow for an exact case version
// and trusted effective policy. Its durable decision link is the idempotency
// authority across concurrent launches and process restarts.
type AttentionLauncher struct {
	service   *Service
	executor  *workflows.Executor
	policies  AttentionPolicySource
	decisions eventing.ReviewDecisionRunStore
	runs      workflows.RunStore
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

type resolvedAttentionPolicy struct {
	sourceRevision   string
	decisionRevision string
	resolution       *workflows.GatePolicyResolution
}

// preparedAttentionPolicy is the package-private authority used by the
// durable trigger worker. Its canonical envelope can be persisted before any
// model, session, or workflow-run effect and later decoded into the exact same
// detached policy after a crash or configuration reload.
//
// Keeping this type and the prepared launch entry point private prevents an
// HTTP/API caller from supplying effective gates or a policy revision. The
// normal Launch method can still only capture policy from AttentionPolicySource.
type preparedAttentionPolicy struct {
	canonical []byte
}

type preparedAttentionPolicyEnvelope struct {
	Version          int                             `json:"version"`
	SourceRevision   string                          `json:"source_revision"`
	DecisionRevision string                          `json:"decision_revision"`
	Resolution       *workflows.GatePolicyResolution `json:"resolution"`
}

var errAttentionPolicyChanged = errors.New("review attention policy changed")

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
	return &AttentionLauncher{
		service:   config.Service,
		executor:  config.Executor,
		policies:  config.Policies,
		decisions: decisions,
		runs:      runs,
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
	policy, err := launcher.capturePolicy(ctx, AttentionPolicySelector{
		Repository:    detail.Case.Repository,
		DecisionPoint: request.DecisionPoint,
	})
	if err != nil {
		return preparedAttentionPolicy{}, sanitizeAttentionError(ctx, err, false)
	}
	prepared, err := encodePreparedAttentionPolicy(policy)
	if err != nil {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	return prepared, nil
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
	runID, err := attentionRunID(key)
	if err != nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	baseResult := AttentionLaunchResult{
		CaseID:         key.CaseID,
		CaseVersion:    key.CaseVersion,
		DecisionPoint:  key.DecisionPoint,
		PolicyRevision: key.PolicyRevision,
	}

	if existing, found, lookupErr := launcher.findExisting(ctx, key, runID); lookupErr != nil {
		return AttentionLaunchResult{}, lookupErr
	} else if found {
		return attentionResultForRun(baseResult, existing, true), nil
	}
	// Historical exact duplicates above remain discoverable after the case has
	// advanced. A genuinely new decision must bind the current exact version.
	if detail.Case.Version != request.ExpectedCaseVersion {
		return AttentionLaunchResult{}, workflows.ErrRunAdmissionConflict
	}

	if attentionPolicyIsNoop(policy.resolution.Effective) {
		compilation, compileErr := workflows.CompileGateWorkflow(
			reviewAttentionWorkflowName,
			policy.resolution.Effective,
			nil,
		)
		if compileErr != nil || compilation == nil || !compilation.Noop ||
			compilation.Workflow != nil || compilation.PrivateRoot != nil {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		baseResult.Noop = true
		return baseResult, nil
	}

	workingAgentID := attentionWorkingAgentID(policy.resolution.Effective)
	if workingAgentID == "" {
		subject, subjectErr := workingContextGateSubject(detail)
		if subjectErr != nil {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		compilation, compileErr := workflows.CompileGateWorkflow(
			reviewAttentionWorkflowName,
			policy.resolution.Effective,
			subject,
		)
		if compileErr != nil {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		if compilation == nil || compilation.Noop || compilation.RequiresSession ||
			compilation.RequiredSessionAgentID != "" {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		return launcher.launchCompilation(
			ctx,
			selector,
			policy,
			revalidateLive,
			key,
			runID,
			baseResult,
			compilation,
		)
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
			compilation, compileErr := workflows.CompileGateWorkflow(
				reviewAttentionWorkflowName,
				policy.resolution.Effective,
				working.GateSubject,
			)
			if compileErr != nil || compilation == nil || compilation.Noop ||
				!compilation.RequiresSession ||
				compilation.RequiredSessionAgentID != working.AgentID ||
				compilation.PrivateRoot == nil {
				return ErrUnavailable
			}
			compilation.PrivateRoot.ReadOnlySession = &workflows.ReadOnlySessionRef{
				AgentID:          working.AgentID,
				Session:          working.SessionKey,
				ExpectedRevision: working.SessionRevision,
			}
			var launchErr error
			result, launchErr = launcher.launchCompilation(
				runtimeCtx,
				selector,
				policy,
				revalidateLive,
				key,
				runID,
				baseResult,
				compilation,
			)
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

func (launcher *AttentionLauncher) available() bool {
	return launcher != nil && launcher.service != nil && launcher.executor != nil &&
		launcher.policies != nil && launcher.decisions != nil && launcher.runs != nil
}

func validateAttentionLaunchRequest(request AttentionLaunchRequest) error {
	if !validWorkingContextPrefixedHexID(request.CaseID, "prc_") ||
		request.ExpectedCaseVersion <= 0 ||
		!validAttentionDecisionPoint(request.DecisionPoint) {
		return ErrInvalidRequest
	}
	return nil
}

func (launcher *AttentionLauncher) capturePolicy(
	ctx context.Context,
	selector AttentionPolicySelector,
) (resolvedAttentionPolicy, error) {
	var captured resolvedAttentionPolicy
	called := 0
	var callbackErr error
	err := launcher.policies.WithReviewAttentionPolicy(
		ctx,
		selector,
		func(policyCtx context.Context, snapshot AttentionPolicySnapshot) error {
			called++
			if called != 1 || policyCtx == nil {
				callbackErr = ErrUnavailable
				return callbackErr
			}
			if contextErr := policyCtx.Err(); contextErr != nil {
				callbackErr = contextErr
				return callbackErr
			}
			resolved, resolveErr := resolveAttentionPolicy(snapshot)
			if resolveErr != nil {
				callbackErr = ErrUnavailable
				return callbackErr
			}
			captured = resolved
			return nil
		},
	)
	// A trusted source is required to propagate the callback result. Retaining
	// it independently keeps a buggy adapter from converting a rejected lease
	// snapshot into successful launch authority.
	if callbackErr != nil {
		return resolvedAttentionPolicy{}, callbackErr
	}
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	if called != 1 || captured.resolution == nil {
		return resolvedAttentionPolicy{}, ErrUnavailable
	}
	return captured, nil
}

func resolveAttentionPolicy(
	snapshot AttentionPolicySnapshot,
) (resolvedAttentionPolicy, error) {
	if !validAttentionSourceRevision(snapshot.Revision) {
		return resolvedAttentionPolicy{}, errors.New("invalid attention policy revision")
	}
	resolution, err := workflows.ResolveGatePolicy(snapshot.Global, snapshot.Repository)
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	decisionRevision, err := attentionPolicyDecisionRevision(snapshot.Revision, resolution)
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	return resolvedAttentionPolicy{
		sourceRevision:   snapshot.Revision,
		decisionRevision: decisionRevision,
		resolution:       resolution,
	}, nil
}

func attentionPolicyDecisionRevision(
	sourceRevision string,
	resolution *workflows.GatePolicyResolution,
) (string, error) {
	if !validAttentionSourceRevision(sourceRevision) || resolution == nil {
		return "", errors.New("invalid attention policy")
	}
	canonical, err := json.Marshal(struct {
		Version    int                             `json:"version"`
		Revision   string                          `json:"source_revision"`
		Resolution *workflows.GatePolicyResolution `json:"resolution"`
	}{
		Version:    preparedAttentionPolicyV1,
		Revision:   sourceRevision,
		Resolution: resolution,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func encodePreparedAttentionPolicy(
	policy resolvedAttentionPolicy,
) (preparedAttentionPolicy, error) {
	decisionRevision, err := attentionPolicyDecisionRevision(
		policy.sourceRevision,
		policy.resolution,
	)
	if err != nil || decisionRevision != policy.decisionRevision ||
		validatePreparedAttentionResolution(policy.resolution) != nil {
		return preparedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	canonical, err := json.Marshal(preparedAttentionPolicyEnvelope{
		Version:          preparedAttentionPolicyV1,
		SourceRevision:   policy.sourceRevision,
		DecisionRevision: policy.decisionRevision,
		Resolution:       policy.resolution,
	})
	if err != nil || len(canonical) == 0 || len(canonical) > maxPreparedAttentionBytes {
		return preparedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	return preparedAttentionPolicy{canonical: canonical}, nil
}

func decodePreparedAttentionPolicy(raw []byte) (resolvedAttentionPolicy, error) {
	if len(raw) == 0 || len(raw) > maxPreparedAttentionBytes ||
		!bytes.Equal(raw, bytes.TrimSpace(raw)) {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var envelope preparedAttentionPolicyEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	if envelope.Version != preparedAttentionPolicyV1 ||
		!validAttentionSourceRevision(envelope.SourceRevision) ||
		validatePreparedAttentionResolution(envelope.Resolution) != nil {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	decisionRevision, err := attentionPolicyDecisionRevision(
		envelope.SourceRevision,
		envelope.Resolution,
	)
	if err != nil || envelope.DecisionRevision != decisionRevision {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(raw, canonical) {
		return resolvedAttentionPolicy{}, errors.New("invalid prepared attention policy")
	}
	return resolvedAttentionPolicy{
		sourceRevision:   envelope.SourceRevision,
		decisionRevision: envelope.DecisionRevision,
		resolution:       envelope.Resolution,
	}, nil
}

func validAttentionSourceRevision(revision string) bool {
	return revision != "" && revision == strings.TrimSpace(revision) &&
		utf8.ValidString(revision) && len(revision) <= maxAttentionRevisionBytes
}

func validatePreparedAttentionResolution(resolution *workflows.GatePolicyResolution) error {
	if resolution == nil || len(resolution.Effective) > workflows.MaxWorkflowGateCount ||
		len(resolution.Entries) != len(resolution.Effective) {
		return errors.New("invalid attention policy resolution")
	}
	if _, err := workflows.ResolveGatePolicy(resolution.Effective, nil); err != nil {
		return err
	}
	switch resolution.Mode {
	case workflows.GatePolicyInherit, workflows.GatePolicyOverlay,
		workflows.GatePolicyReplace, workflows.GatePolicyDisable:
	default:
		return errors.New("invalid attention policy resolution mode")
	}
	if resolution.Mode == workflows.GatePolicyDisable && len(resolution.Effective) != 0 {
		return errors.New("disabled attention policy is not empty")
	}
	for index, entry := range resolution.Entries {
		if entry.ID != resolution.Effective[index].ID || entry.EffectivePosition != index+1 ||
			entry.GlobalPosition < 0 || entry.GlobalPosition > workflows.MaxWorkflowGateCount ||
			entry.RepositoryPosition < 0 ||
			entry.RepositoryPosition > workflows.MaxWorkflowGateCount {
			return errors.New("invalid attention policy resolution entry")
		}
		switch resolution.Mode {
		case workflows.GatePolicyInherit:
			if entry.Action != workflows.GatePolicyResolutionInherited ||
				entry.GlobalPosition != index+1 || entry.RepositoryPosition != 0 {
				return errors.New("invalid inherited attention policy entry")
			}
		case workflows.GatePolicyReplace:
			if entry.Action != workflows.GatePolicyResolutionSelected ||
				entry.GlobalPosition != 0 || entry.RepositoryPosition != index+1 {
				return errors.New("invalid replacement attention policy entry")
			}
		case workflows.GatePolicyOverlay:
			switch entry.Action {
			case workflows.GatePolicyResolutionInherited:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition != 0 {
					return errors.New("invalid overlaid attention policy entry")
				}
			case workflows.GatePolicyResolutionReplaced:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition == 0 {
					return errors.New("invalid overlaid attention policy entry")
				}
			case workflows.GatePolicyResolutionTombstoned:
				if entry.GlobalPosition == 0 || entry.RepositoryPosition == 0 ||
					resolution.Effective[index].Kind != workflows.GateZero {
					return errors.New("invalid overlaid attention policy entry")
				}
			case workflows.GatePolicyResolutionAppended:
				if entry.GlobalPosition != 0 || entry.RepositoryPosition == 0 {
					return errors.New("invalid overlaid attention policy entry")
				}
			default:
				return errors.New("invalid overlaid attention policy action")
			}
		}
	}
	return nil
}

func attentionPolicyIsNoop(specs []workflows.GateSpec) bool {
	for _, spec := range specs {
		if spec.Kind != workflows.GateZero {
			return false
		}
	}
	return true
}

func attentionWorkingAgentID(specs []workflows.GateSpec) string {
	for _, spec := range specs {
		if spec.Kind == workflows.GateAIWorkingContext {
			return spec.AgentID
		}
	}
	return ""
}

func attentionRunID(key eventing.ReviewDecisionKey) (string, error) {
	canonical, err := json.Marshal(struct {
		Version int                        `json:"version"`
		Key     eventing.ReviewDecisionKey `json:"key"`
	}{Version: 1, Key: key})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "wr_" + hex.EncodeToString(digest[:16]), nil
}

func (launcher *AttentionLauncher) findExisting(
	ctx context.Context,
	key eventing.ReviewDecisionKey,
	runID string,
) (*workflows.Run, bool, error) {
	link, err := launcher.decisions.GetReviewDecisionRun(ctx, key)
	if err == nil {
		run, runErr := launcher.loadLinkedRun(ctx, key, runID, link)
		if runErr != nil {
			return nil, false, runErr
		}
		return run, true, nil
	}
	if !errors.Is(err, eventing.ErrNotFound) {
		return nil, false, sanitizeAttentionError(ctx, err, false)
	}
	return nil, false, nil
}

func (launcher *AttentionLauncher) loadLinkedRun(
	ctx context.Context,
	key eventing.ReviewDecisionKey,
	runID string,
	link eventing.ReviewDecisionRunLink,
) (*workflows.Run, error) {
	if link.Key != key || link.RunID != runID {
		return nil, ErrUnavailable
	}
	run, err := launcher.runs.GetRun(ctx, link.RunID)
	if err != nil || !validAttentionRun(run, runID) {
		return nil, ErrUnavailable
	}
	return run, nil
}

func validAttentionRun(run *workflows.Run, runID string) bool {
	if run == nil || run.ID != runID || run.WorkflowRef != reviewAttentionWorkflowRef ||
		run.ContextVisibility != workflows.WorkflowContextVisibilityPrivate ||
		!workflows.IsPrivateWorkflowRun(run) || run.ParentRunID != "" ||
		run.CallerJobID != "" || len(run.ChildRunIDs) != 0 ||
		run.RetryOfRunID != "" || run.Session != "" ||
		!attentionDeliveryIsEmpty(run.Delivery) || len(run.Inputs) != 0 ||
		len(run.Event) != 0 || run.Origin != nil {
		return false
	}
	switch run.Status {
	case workflows.RunStatusRunning, workflows.RunStatusWaiting,
		workflows.RunStatusSucceeded, workflows.RunStatusFailed,
		workflows.RunStatusCanceled, workflows.RunStatusSkipped:
		return true
	default:
		return false
	}
}

func (launcher *AttentionLauncher) launchCompilation(
	ctx context.Context,
	selector AttentionPolicySelector,
	policy resolvedAttentionPolicy,
	revalidateLive bool,
	key eventing.ReviewDecisionKey,
	runID string,
	baseResult AttentionLaunchResult,
	compilation *workflows.GateCompilation,
) (AttentionLaunchResult, error) {
	if compilation == nil || compilation.Noop || compilation.Workflow == nil ||
		compilation.PrivateRoot == nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	executor := *launcher.executor
	executor.Store = launcher.runs
	baseAdmission := executor.AdmittedRunCreate
	var duplicate *eventing.ReviewDecisionRunLink
	executor.AdmittedRunCreate = func(
		admissionCtx context.Context,
		candidate *workflows.Run,
		create func() error,
	) error {
		if !validAttentionAdmissionCandidate(candidate, runID) {
			return workflows.ErrRunAdmissionConflict
		}
		admit := func(admitCtx context.Context) error {
			link, existed, admitErr := launcher.decisions.AdmitReviewDecisionRun(
				admitCtx,
				eventing.ReviewDecisionRunAdmission{Key: key, RunID: runID},
				func(createCtx context.Context) error {
					createCalls := 0
					var durableCreateErr error
					checkedCreate := func() error {
						createCalls++
						if createCalls != 1 {
							durableCreateErr = workflows.ErrRunAdmissionUnavailable
							return durableCreateErr
						}
						durableCreateErr = create()
						return durableCreateErr
					}
					var createErr error
					if baseAdmission != nil {
						createErr = baseAdmission(createCtx, candidate, checkedCreate)
					} else {
						createErr = checkedCreate()
					}
					if createErr != nil {
						return createErr
					}
					if durableCreateErr != nil {
						return durableCreateErr
					}
					if createCalls != 1 {
						return workflows.ErrRunAdmissionUnavailable
					}
					return nil
				},
			)
			if admitErr != nil {
				return admitErr
			}
			if existed {
				linked := link
				duplicate = &linked
				// Executor must not execute the duplicate in-memory candidate.
				return workflows.ErrRunAdmissionConflict
			}
			if link.Key != key || link.RunID != runID {
				return ErrUnavailable
			}
			return nil
		}

		called := 0
		var callbackErr error
		var policyErr error
		if revalidateLive {
			policyErr = launcher.policies.WithReviewAttentionPolicy(
				admissionCtx,
				selector,
				func(policyCtx context.Context, snapshot AttentionPolicySnapshot) error {
					called++
					if called != 1 || policyCtx == nil {
						callbackErr = ErrUnavailable
						return callbackErr
					}
					if contextErr := policyCtx.Err(); contextErr != nil {
						callbackErr = contextErr
						return callbackErr
					}
					current, resolveErr := resolveAttentionPolicy(snapshot)
					if resolveErr != nil {
						callbackErr = ErrUnavailable
						return callbackErr
					}
					if current.sourceRevision != policy.sourceRevision ||
						current.decisionRevision != policy.decisionRevision {
						callbackErr = errAttentionPolicyChanged
						return callbackErr
					}
					callbackErr = admit(policyCtx)
					return callbackErr
				},
			)
			if callbackErr != nil {
				policyErr = callbackErr
			}
		} else {
			policyErr = admit(admissionCtx)
			called = 1
		}
		if policyErr != nil {
			switch {
			case errors.Is(policyErr, errAttentionPolicyChanged),
				errors.Is(policyErr, eventing.ErrReviewConflict),
				errors.Is(policyErr, eventing.ErrNotFound),
				errors.Is(policyErr, workflows.ErrRunAdmissionConflict):
				return workflows.ErrRunAdmissionConflict
			case errors.Is(policyErr, context.Canceled):
				return context.Canceled
			case errors.Is(policyErr, context.DeadlineExceeded):
				return context.DeadlineExceeded
			default:
				return workflows.ErrRunAdmissionUnavailable
			}
		}
		if called != 1 {
			return workflows.ErrRunAdmissionUnavailable
		}
		return nil
	}

	runResult, runErr := executor.Run(ctx, workflows.RunRequest{
		RunID:       runID,
		Workflow:    compilation.Workflow,
		WorkflowRef: reviewAttentionWorkflowRef,
		PrivateRoot: compilation.PrivateRoot,
	})
	if duplicate != nil {
		run, loadErr := launcher.loadLinkedRun(ctx, key, runID, *duplicate)
		if loadErr != nil {
			return AttentionLaunchResult{}, loadErr
		}
		return attentionResultForRun(baseResult, run, true), nil
	}
	if runErr != nil {
		safeErr := sanitizeAttentionError(ctx, runErr, true)
		if runResult != nil && runResult.RunID == runID && runResult.Status != "" {
			run, loadErr := launcher.runs.GetRun(ctx, runID)
			if loadErr == nil && validAttentionRun(run, runID) &&
				run.Status == runResult.Status {
				return attentionResultForRun(baseResult, run, false), safeErr
			}
		}
		return AttentionLaunchResult{}, safeErr
	}
	if runResult == nil || runResult.RunID != runID || runResult.Status == "" {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	run, err := launcher.runs.GetRun(ctx, runID)
	if err != nil || !validAttentionRun(run, runID) || run.Status != runResult.Status {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	return attentionResultForRun(baseResult, run, false), nil
}

func validAttentionAdmissionCandidate(candidate *workflows.Run, runID string) bool {
	return candidate != nil && candidate.ID == runID &&
		candidate.WorkflowRef == reviewAttentionWorkflowRef &&
		candidate.Status == workflows.RunStatusRunning &&
		candidate.ContextVisibility == workflows.WorkflowContextVisibilityPrivate &&
		workflows.IsPrivateWorkflowRun(candidate) && candidate.ParentRunID == "" &&
		candidate.CallerJobID == "" && len(candidate.ChildRunIDs) == 0 &&
		candidate.RetryOfRunID == "" && candidate.Session == "" &&
		attentionDeliveryIsEmpty(candidate.Delivery) && len(candidate.Inputs) == 0 &&
		len(candidate.Event) == 0 && candidate.Origin == nil
}

func attentionDeliveryIsEmpty(delivery workflows.Delivery) bool {
	return delivery.Channel == "" && delivery.ChatID == "" && delivery.TopicID == "" &&
		delivery.ThreadTS == "" && delivery.MessageID == "" &&
		delivery.ReplyToMessageID == "" && len(delivery.ReplyHandles) == 0
}

func attentionResultForRun(
	base AttentionLaunchResult,
	run *workflows.Run,
	existing bool,
) AttentionLaunchResult {
	base.RunID = run.ID
	base.Status = run.Status
	base.Existing = existing
	return base
}

func sanitizeAttentionError(ctx context.Context, err error, admission bool) error {
	if err == nil {
		return nil
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
