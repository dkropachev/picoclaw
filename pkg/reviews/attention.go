package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	if launcher == nil || launcher.service == nil || launcher.executor == nil ||
		launcher.policies == nil || launcher.decisions == nil || launcher.runs == nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttentionLaunchRequest(request); err != nil {
		return AttentionLaunchResult{}, err
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
	policy, err := launcher.capturePolicy(ctx, selector)
	if err != nil {
		return AttentionLaunchResult{}, sanitizeAttentionError(ctx, err, false)
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

func validateAttentionLaunchRequest(request AttentionLaunchRequest) error {
	if !validWorkingContextPrefixedHexID(request.CaseID, "prc_") ||
		request.ExpectedCaseVersion <= 0 ||
		request.DecisionPoint != strings.TrimSpace(request.DecisionPoint) ||
		!utf8.ValidString(request.DecisionPoint) ||
		len(request.DecisionPoint) > maxAttentionDecisionBytes ||
		!attentionDecisionPattern.MatchString(request.DecisionPoint) {
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
	if snapshot.Revision == "" || snapshot.Revision != strings.TrimSpace(snapshot.Revision) ||
		!utf8.ValidString(snapshot.Revision) || len(snapshot.Revision) > maxAttentionRevisionBytes {
		return resolvedAttentionPolicy{}, errors.New("invalid attention policy revision")
	}
	resolution, err := workflows.ResolveGatePolicy(snapshot.Global, snapshot.Repository)
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	canonical, err := json.Marshal(struct {
		Version    int                             `json:"version"`
		Revision   string                          `json:"source_revision"`
		Resolution *workflows.GatePolicyResolution `json:"resolution"`
	}{
		Version:    1,
		Revision:   snapshot.Revision,
		Resolution: resolution,
	})
	if err != nil {
		return resolvedAttentionPolicy{}, err
	}
	digest := sha256.Sum256(canonical)
	return resolvedAttentionPolicy{
		sourceRevision:   snapshot.Revision,
		decisionRevision: "sha256:" + hex.EncodeToString(digest[:]),
		resolution:       resolution,
	}, nil
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
		called := 0
		var callbackErr error
		policyErr := launcher.policies.WithReviewAttentionPolicy(
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
				link, existed, admitErr := launcher.decisions.AdmitReviewDecisionRun(
					policyCtx,
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
					callbackErr = admitErr
					return callbackErr
				}
				if existed {
					linked := link
					duplicate = &linked
					// Executor must not execute the duplicate in-memory candidate.
					callbackErr = workflows.ErrRunAdmissionConflict
					return callbackErr
				}
				if link.Key != key || link.RunID != runID {
					callbackErr = ErrUnavailable
					return callbackErr
				}
				return nil
			},
		)
		if callbackErr != nil {
			policyErr = callbackErr
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
