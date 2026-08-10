package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const maximumAttentionDecisionPointBytes = 128

// ErrAttentionSubjectTooLarge reports that the exact mandatory private
// attention subject cannot fit the shared workflow-gate subject bound. The
// condition is terminal for that snapshot and cannot be repaired by compacting
// its development ledger.
var ErrAttentionSubjectTooLarge = errors.New(
	"pull request development attention subject exceeds the workflow gate limit",
)

var errPinnedAttentionSubjectDrift = errors.New(
	"pinned pull request development attention subject is no longer reconstructable",
)

var prDevelopmentAttentionDecisionPattern = regexp.MustCompile(
	`^[a-z][a-z0-9._-]{0,127}$`,
)

type attentionLauncherStore interface {
	attentionContextStore
	eventing.PRDevelopmentAttentionDecisionRunStore
}

// AttentionLauncherConfig contains only launch-time capabilities. It has no
// trigger, controller mutation, provider, publication, or push authority.
type AttentionLauncherConfig struct {
	Store          attentionLauncherStore
	Executor       *workflows.Executor
	Runs           workflows.RunStore
	Policies       sharedattention.PolicySource
	Evidence       AttentionEvidenceStore
	Workspaces     AttentionReviewWorkspaceFactory
	AcquireRuntime AttentionContextRuntimeAcquire
}

// AttentionLauncher starts one private gate workflow for the exact atomic
// attention-required review snapshot returned by eventing.
type AttentionLauncher struct {
	store          attentionLauncherStore
	executor       *workflows.Executor
	runs           workflows.RunStore
	policies       sharedattention.PolicySource
	context        *attentionContextLoader
	acquireRuntime AttentionContextRuntimeAcquire
}

// AttentionLaunchRequest selects policy for one authoritative development
// case. Policy material, review identity, repository identity, subject, and
// workflow identity are deliberately not caller supplied.
type AttentionLaunchRequest struct {
	CaseID        string
	DecisionPoint string
}

// AttentionLaunchResult excludes private subject, session, controller,
// workflow-context, and model data while preserving the durable decision
// identity needed by a later trigger worker.
type AttentionLaunchResult struct {
	CaseID              string `json:"case_id"`
	ReviewEntryID       string `json:"-"`
	ConversationVersion int64  `json:"conversation_version"`
	DecisionPoint       string `json:"decision_point"`
	PolicyRevision      string `json:"-"`
	SubjectRevision     string `json:"-"`
	RunID               string `json:"run_id,omitempty"`
	Status              string `json:"status,omitempty"`
	Existing            bool   `json:"existing,omitempty"`
	Noop                bool   `json:"noop,omitempty"`
}

// preparedAttentionPolicy is the exact canonical policy envelope persisted by
// an automatic trigger. Keeping it opaque prevents worker code from
// reconstructing or partially trusting policy fields.
type preparedAttentionPolicy struct {
	canonical []byte
}

func NewAttentionLauncher(config AttentionLauncherConfig) (*AttentionLauncher, error) {
	if config.Store == nil || isNilServiceValue(config.Store) {
		return nil, errors.New("pull request development attention store is required")
	}
	if config.Executor == nil {
		return nil, errors.New("pull request development attention executor is required")
	}
	if config.Policies == nil || isNilServiceValue(config.Policies) {
		return nil, errors.New("pull request development attention policy source is required")
	}
	loader, err := newAttentionContextLoader(
		config.Store,
		config.Evidence,
		config.Workspaces,
		config.AcquireRuntime,
	)
	if err != nil {
		return nil, err
	}
	runs := config.Runs
	if runs == nil || isNilServiceValue(runs) {
		runs = config.Executor.Store
	}
	if runs == nil || isNilServiceValue(runs) {
		runs = workflows.NewFileRunStore(config.Executor.WorkspaceDir)
	}
	return &AttentionLauncher{
		store:          config.Store,
		executor:       config.Executor,
		runs:           runs,
		policies:       config.Policies,
		context:        loader,
		acquireRuntime: config.AcquireRuntime,
	}, nil
}

func (launcher *AttentionLauncher) Launch(
	ctx context.Context,
	request AttentionLaunchRequest,
) (AttentionLaunchResult, error) {
	if launcher == nil || launcher.store == nil || launcher.executor == nil ||
		launcher.policies == nil || launcher.context == nil || launcher.runs == nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttentionLaunchRequest(request); err != nil {
		return AttentionLaunchResult{}, err
	}
	snapshot, err := launcher.store.GetPRDevelopmentAttentionSnapshot(ctx, request.CaseID)
	if err != nil {
		return AttentionLaunchResult{}, sanitizeAttentionLaunchError(ctx, err)
	}
	if err = validateAttentionSnapshot(snapshot); err != nil {
		return AttentionLaunchResult{}, err
	}
	selector := sharedattention.PolicySelector{
		Repository:    snapshot.Case.Repository,
		DecisionPoint: request.DecisionPoint,
	}
	policy, err := sharedattention.PreparePolicy(ctx, launcher.policies, selector)
	if err != nil {
		return AttentionLaunchResult{}, sanitizeAttentionLaunchError(ctx, err)
	}
	base := AttentionLaunchResult{
		CaseID:              snapshot.Case.ID,
		ReviewEntryID:       snapshot.ReviewEntry.ID,
		ConversationVersion: snapshot.Conversation.Version,
		DecisionPoint:       request.DecisionPoint,
		PolicyRevision:      policy.DecisionRevision(),
	}
	if policy.IsNoop() {
		// Verify that the shared compiler agrees before returning. No context,
		// session, durable decision link, or workflow run is touched.
		compilation, compileErr := workflows.CompileGateWorkflow(
			sharedattention.WorkflowName,
			policy.EffectiveGates(),
			nil,
		)
		if compileErr != nil || compilation == nil || !compilation.Noop ||
			compilation.Workflow != nil || compilation.PrivateRoot != nil {
			return AttentionLaunchResult{}, ErrUnavailable
		}
		base.Noop = true
		return base, nil
	}
	for _, gate := range policy.EffectiveGates() {
		if gate.Kind == workflows.GateAIWorkingContext &&
			(gate.AgentID != snapshot.Controller.AgentID ||
				gate.AgentID != snapshot.OwnerSession.AgentID) {
			return AttentionLaunchResult{}, fmt.Errorf(
				"%w: working-context gate agent differs from the immutable repair owner",
				ErrUnavailable,
			)
		}
	}
	if launcher.acquireRuntime == nil {
		return AttentionLaunchResult{}, fmt.Errorf(
			"%w: owner runtime is not configured",
			ErrUnavailable,
		)
	}
	var result AttentionLaunchResult
	err = launcher.context.withRuntimeContext(
		ctx,
		snapshot,
		snapshot.Controller.AgentID,
		func(
			runtimeCtx context.Context,
			current eventing.PRDevelopmentAttentionSnapshot,
			rawStore session.SessionStore,
		) error {
			var launchErr error
			result, launchErr = launcher.launchPreparedAttention(
				runtimeCtx,
				base,
				request,
				selector,
				policy,
				current,
				rawStore,
				"",
				true,
			)
			return launchErr
		},
	)
	if err != nil {
		return result, sanitizeAttentionLaunchError(ctx, err)
	}
	return result, nil
}

func (launcher *AttentionLauncher) available() bool {
	return launcher != nil && launcher.store != nil && launcher.executor != nil &&
		launcher.runs != nil && launcher.policies != nil && launcher.context != nil
}

// prepareAttentionTriggerPolicy captures only trusted configured policy for
// the exact occurrence snapshot. Subject construction is deliberately a later
// stage so the canonical policy can be pinned before any Git/session/model
// effect.
func (launcher *AttentionLauncher) prepareAttentionTriggerPolicy(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
) (preparedAttentionPolicy, error) {
	if !launcher.available() {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttentionTriggerSnapshotAnchor(trigger, snapshot); err != nil {
		return preparedAttentionPolicy{}, err
	}
	prepared, err := sharedattention.PreparePolicy(
		ctx,
		launcher.policies,
		sharedattention.PolicySelector{
			Repository:    snapshot.Case.Repository,
			DecisionPoint: trigger.DecisionPoint,
		},
	)
	if err != nil {
		return preparedAttentionPolicy{}, sanitizeAttentionLaunchError(ctx, err)
	}
	return preparedAttentionPolicy{canonical: prepared.Canonical()}, nil
}

func decodePreparedAttentionPolicy(
	prepared preparedAttentionPolicy,
) (sharedattention.PreparedPolicy, error) {
	policy, err := sharedattention.DecodePreparedPolicy(prepared.canonical)
	if err != nil || len(policy.Canonical()) == 0 || policy.DecisionRevision() == "" {
		return sharedattention.PreparedPolicy{}, ErrUnavailable
	}
	return policy, nil
}

// findPinnedAttentionTrigger resolves zero-only policy and historical exact
// workflow links from immutable trigger fields alone. It must run before any
// occurrence snapshot or local context reload on every pinned retry.
func (launcher *AttentionLauncher) findPinnedAttentionTrigger(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	prepared preparedAttentionPolicy,
) (AttentionLaunchResult, bool, error) {
	if !launcher.available() {
		return AttentionLaunchResult{}, false, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	policy, err := decodePreparedAttentionPolicy(prepared)
	if err != nil || policy.DecisionRevision() != trigger.PolicyRevision ||
		validateAttentionTriggerIdentity(trigger) != nil {
		return AttentionLaunchResult{}, false, ErrUnavailable
	}
	base := attentionLaunchResultForTrigger(trigger)
	if policy.IsNoop() {
		if trigger.SubjectRevision != "" {
			return AttentionLaunchResult{}, false, ErrUnavailable
		}
		compilation, compileErr := workflows.CompileGateWorkflow(
			sharedattention.WorkflowName,
			policy.EffectiveGates(),
			nil,
		)
		if compileErr != nil || compilation == nil || !compilation.Noop ||
			compilation.Workflow != nil || compilation.PrivateRoot != nil {
			return AttentionLaunchResult{}, false, ErrUnavailable
		}
		base.Noop = true
		return base, true, nil
	}
	if !validAttentionRevision(trigger.SubjectRevision) {
		return AttentionLaunchResult{}, false, ErrUnavailable
	}
	key := attentionDecisionKeyForTrigger(trigger)
	runner, canonicalKey, runnerErr := launcher.privateAttentionRunner(
		key,
		eventing.PRDevelopmentAttentionHighWater{},
	)
	if runnerErr != nil {
		return AttentionLaunchResult{}, false, runnerErr
	}
	existing, found, findErr := runner.FindExisting(ctx, canonicalKey)
	if findErr != nil {
		return AttentionLaunchResult{}, false, sanitizeAttentionLaunchError(ctx, findErr)
	}
	if !found {
		return AttentionLaunchResult{}, false, nil
	}
	return projectAttentionLaunchResult(base, existing), true, nil
}

// prepareAttentionTriggerSubject computes the exact active-policy subject
// revision without projecting a working session or invoking a workflow/model.
func (launcher *AttentionLauncher) prepareAttentionTriggerSubject(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
	prepared preparedAttentionPolicy,
	refresh attentionRuntimeSnapshotRefresh,
) (string, error) {
	if !launcher.available() {
		return "", ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	policy, err := decodePreparedAttentionPolicy(prepared)
	if err != nil || policy.DecisionRevision() != trigger.PolicyRevision ||
		policy.IsNoop() || trigger.SubjectRevision != "" {
		return "", ErrUnavailable
	}
	if err = validateAttentionTriggerSnapshotAnchor(trigger, snapshot); err != nil {
		return "", err
	}
	if err = validateAttentionWorkingAgent(policy, snapshot); err != nil {
		return "", err
	}
	if launcher.acquireRuntime == nil {
		return "", ErrUnavailable
	}
	var subjectRevision string
	err = launcher.context.withAnchoredRuntimeContext(
		ctx,
		snapshot,
		snapshot.Controller.AgentID,
		refresh,
		func(
			runtimeCtx context.Context,
			current eventing.PRDevelopmentAttentionSnapshot,
			_ session.SessionStore,
		) error {
			loaded, loadErr := launcher.context.load(runtimeCtx, current)
			if loadErr != nil {
				return loadErr
			}
			subjectRevision = loaded.subjectRevision
			return nil
		},
	)
	if err != nil {
		return "", sanitizeAttentionLaunchError(ctx, err)
	}
	if !validAttentionRevision(subjectRevision) {
		return "", ErrUnavailable
	}
	return subjectRevision, nil
}

// launchPinnedAttentionTrigger launches a fully pinned occurrence. Historical
// lookup happens first; only a genuinely new active decision reloads and
// revalidates the anchored subject.
func (launcher *AttentionLauncher) launchPinnedAttentionTrigger(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
	prepared preparedAttentionPolicy,
	refresh attentionRuntimeSnapshotRefresh,
) (AttentionLaunchResult, error) {
	if existing, found, err := launcher.findPinnedAttentionTrigger(
		ctx,
		trigger,
		prepared,
	); err != nil || found {
		return existing, err
	}
	policy, err := decodePreparedAttentionPolicy(prepared)
	if err != nil || policy.IsNoop() ||
		policy.DecisionRevision() != trigger.PolicyRevision ||
		!validAttentionRevision(trigger.SubjectRevision) {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	if err = validateAttentionTriggerSnapshotAnchor(trigger, snapshot); err != nil {
		return AttentionLaunchResult{}, err
	}
	if err = validateAttentionWorkingAgent(policy, snapshot); err != nil {
		return AttentionLaunchResult{}, err
	}
	if launcher.acquireRuntime == nil {
		return AttentionLaunchResult{}, ErrUnavailable
	}
	request := AttentionLaunchRequest{
		CaseID:        trigger.CaseID,
		DecisionPoint: trigger.DecisionPoint,
	}
	selector := sharedattention.PolicySelector{
		Repository:    snapshot.Case.Repository,
		DecisionPoint: trigger.DecisionPoint,
	}
	base := attentionLaunchResultForTrigger(trigger)
	var result AttentionLaunchResult
	err = launcher.context.withAnchoredRuntimeContext(
		ctx,
		snapshot,
		snapshot.Controller.AgentID,
		refresh,
		func(
			runtimeCtx context.Context,
			current eventing.PRDevelopmentAttentionSnapshot,
			rawStore session.SessionStore,
		) error {
			var launchErr error
			result, launchErr = launcher.launchPreparedAttention(
				runtimeCtx,
				base,
				request,
				selector,
				policy,
				current,
				rawStore,
				trigger.SubjectRevision,
				false,
			)
			return launchErr
		},
	)
	if err != nil {
		return result, sanitizeAttentionLaunchError(ctx, err)
	}
	return result, nil
}

func (launcher *AttentionLauncher) privateAttentionRunner(
	key eventing.PRDevelopmentAttentionDecisionKey,
	highWater eventing.PRDevelopmentAttentionHighWater,
) (*sharedattention.PrivateRunner, string, error) {
	canonicalKey, err := canonicalPRDevelopmentAttentionDecisionKey(key)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	runID, err := sharedattention.RunIDForDecisionKey(canonicalKey)
	if err != nil {
		return nil, "", ErrUnavailable
	}
	binding := &prDevelopmentAttentionDecisionBinding{
		store:         launcher.store,
		key:           key,
		highWater:     highWater,
		canonicalKey:  canonicalKey,
		expectedRunID: runID,
	}
	runner, err := sharedattention.NewPrivateRunner(sharedattention.PrivateRunnerConfig{
		Executor:  launcher.executor,
		Runs:      launcher.runs,
		Policies:  launcher.policies,
		Decisions: binding,
	})
	if err != nil {
		return nil, "", ErrUnavailable
	}
	return runner, canonicalKey, nil
}

func validateAttentionWorkingAgent(
	policy sharedattention.PreparedPolicy,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
) error {
	for _, gate := range policy.EffectiveGates() {
		if gate.Kind == workflows.GateAIWorkingContext &&
			(gate.AgentID != snapshot.Controller.AgentID ||
				gate.AgentID != snapshot.OwnerSession.AgentID) {
			return eventing.ErrInvalidPRDevelopmentAttentionTrigger
		}
	}
	return nil
}

// validatePinnedAttentionCompilation classifies immutable policy/subject
// composition failures before the shared runner can enter admission. The
// runner recompiles the same canonical inputs, but every later ambiguous
// create boundary is then represented by its dedicated uncertainty sentinel.
func validatePinnedAttentionCompilation(
	policy sharedattention.PreparedPolicy,
	subject map[string]any,
) error {
	compilation, err := workflows.CompileGateWorkflow(
		sharedattention.WorkflowName,
		policy.EffectiveGates(),
		subject,
	)
	if err != nil || compilation == nil || compilation.Noop ||
		compilation.Workflow == nil || compilation.PrivateRoot == nil {
		return eventing.ErrInvalidPRDevelopmentAttentionTrigger
	}
	workingAgentID := policy.WorkingContextAgentID()
	if workingAgentID == "" {
		if compilation.RequiresSession || compilation.RequiredSessionAgentID != "" {
			return eventing.ErrInvalidPRDevelopmentAttentionTrigger
		}
		return nil
	}
	if !compilation.RequiresSession ||
		compilation.RequiredSessionAgentID != workingAgentID {
		return eventing.ErrInvalidPRDevelopmentAttentionTrigger
	}
	return nil
}

func validateAttentionTriggerSnapshotAnchor(
	trigger eventing.PRDevelopmentAttentionTrigger,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
) error {
	if validateAttentionTriggerIdentity(trigger) != nil ||
		validateAttentionSnapshot(snapshot) != nil ||
		trigger.CaseID != snapshot.Case.ID ||
		trigger.ReviewEntryID != snapshot.ReviewEntry.ID ||
		trigger.ReviewEntryHash != snapshot.ReviewEntry.EntryHash ||
		trigger.ConversationVersion != snapshot.Conversation.Version ||
		trigger.TranscriptDigest != snapshot.HighWater.TranscriptDigest {
		return ErrUnavailable
	}
	return nil
}

func validateAttentionTriggerIdentity(
	trigger eventing.PRDevelopmentAttentionTrigger,
) error {
	if !validCaseID(trigger.CaseID) ||
		!validDevelopmentID(trigger.ReviewEntryID, "pdle_") ||
		!validControllerSHA256(trigger.ReviewEntryHash) ||
		trigger.ConversationVersion < 0 ||
		!validControllerSHA256(trigger.TranscriptDigest) ||
		trigger.DecisionPoint != strings.TrimSpace(trigger.DecisionPoint) ||
		len(trigger.DecisionPoint) > maximumAttentionDecisionPointBytes ||
		!prDevelopmentAttentionDecisionPattern.MatchString(trigger.DecisionPoint) {
		return ErrUnavailable
	}
	return nil
}

func validAttentionRevision(value string) bool {
	return strings.HasPrefix(value, "sha256:") &&
		validControllerSHA256(strings.TrimPrefix(value, "sha256:"))
}

func attentionDecisionKeyForTrigger(
	trigger eventing.PRDevelopmentAttentionTrigger,
) eventing.PRDevelopmentAttentionDecisionKey {
	return eventing.PRDevelopmentAttentionDecisionKey{
		CaseID:              trigger.CaseID,
		ReviewEntryID:       trigger.ReviewEntryID,
		ReviewEntryHash:     trigger.ReviewEntryHash,
		ConversationVersion: trigger.ConversationVersion,
		SubjectRevision:     trigger.SubjectRevision,
		DecisionPoint:       trigger.DecisionPoint,
		PolicyRevision:      trigger.PolicyRevision,
	}
}

func attentionLaunchResultForTrigger(
	trigger eventing.PRDevelopmentAttentionTrigger,
) AttentionLaunchResult {
	return AttentionLaunchResult{
		CaseID:              trigger.CaseID,
		ReviewEntryID:       trigger.ReviewEntryID,
		ConversationVersion: trigger.ConversationVersion,
		DecisionPoint:       trigger.DecisionPoint,
		PolicyRevision:      trigger.PolicyRevision,
		SubjectRevision:     trigger.SubjectRevision,
	}
}

func (launcher *AttentionLauncher) launchPreparedAttention(
	ctx context.Context,
	base AttentionLaunchResult,
	request AttentionLaunchRequest,
	selector sharedattention.PolicySelector,
	policy sharedattention.PreparedPolicy,
	snapshot eventing.PRDevelopmentAttentionSnapshot,
	rawStore session.SessionStore,
	expectedSubjectRevision string,
	revalidateLive bool,
) (AttentionLaunchResult, error) {
	workingAgentID := policy.WorkingContextAgentID()
	loaded, err := launcher.context.load(ctx, snapshot)
	if err != nil {
		return AttentionLaunchResult{}, fmt.Errorf("load exact attention context: %w", err)
	}
	if expectedSubjectRevision != "" &&
		loaded.subjectRevision != expectedSubjectRevision {
		return AttentionLaunchResult{}, errPinnedAttentionSubjectDrift
	}
	if expectedSubjectRevision != "" {
		if err = validatePinnedAttentionCompilation(policy, loaded.subject); err != nil {
			return AttentionLaunchResult{}, err
		}
	}
	base.SubjectRevision = loaded.subjectRevision
	key := eventing.PRDevelopmentAttentionDecisionKey{
		CaseID:              snapshot.Case.ID,
		ReviewEntryID:       snapshot.ReviewEntry.ID,
		ReviewEntryHash:     snapshot.ReviewEntry.EntryHash,
		ConversationVersion: snapshot.Conversation.Version,
		SubjectRevision:     loaded.subjectRevision,
		DecisionPoint:       request.DecisionPoint,
		PolicyRevision:      policy.DecisionRevision(),
	}
	canonicalKey, err := canonicalPRDevelopmentAttentionDecisionKey(key)
	if err != nil {
		return AttentionLaunchResult{}, fmt.Errorf("canonicalize attention decision: %w", ErrUnavailable)
	}
	runID, err := sharedattention.RunIDForDecisionKey(canonicalKey)
	if err != nil {
		return AttentionLaunchResult{}, fmt.Errorf("derive attention run identity: %w", ErrUnavailable)
	}
	binding := &prDevelopmentAttentionDecisionBinding{
		store:         launcher.store,
		key:           key,
		highWater:     snapshot.HighWater,
		canonicalKey:  canonicalKey,
		expectedRunID: runID,
	}
	runner, err := sharedattention.NewPrivateRunner(sharedattention.PrivateRunnerConfig{
		Executor:  launcher.executor,
		Runs:      launcher.runs,
		Policies:  launcher.policies,
		Decisions: binding,
	})
	if err != nil {
		return AttentionLaunchResult{}, fmt.Errorf("construct private attention runner: %w", ErrUnavailable)
	}
	if existing, found, findErr := runner.FindExisting(ctx, canonicalKey); findErr != nil {
		return AttentionLaunchResult{}, fmt.Errorf("find existing attention run: %w", findErr)
	} else if found {
		return projectAttentionLaunchResult(base, existing), nil
	}

	launch := func(
		launchCtx context.Context,
		readOnly *workflows.ReadOnlySessionRef,
	) (sharedattention.PrivateLaunchResult, error) {
		return runner.Launch(launchCtx, sharedattention.PrivateLaunchRequest{
			DecisionKey:     canonicalKey,
			Policy:          policy,
			Selector:        selector,
			RevalidateLive:  revalidateLive,
			Subject:         loaded.subject,
			ReadOnlySession: readOnly,
		})
	}
	if workingAgentID == "" {
		result, launchErr := launch(ctx, nil)
		if launchErr != nil {
			return projectAttentionLaunchResult(base, result), fmt.Errorf(
				"launch private attention workflow: %w",
				launchErr,
			)
		}
		return projectAttentionLaunchResult(base, result), nil
	}
	working, err := launcher.context.projectWorkingContext(
		ctx,
		loaded,
		workingAgentID,
		rawStore,
	)
	if err != nil {
		return AttentionLaunchResult{}, err
	}
	if working.agentID != workingAgentID || working.sessionKey == "" ||
		working.sessionRevision == "" ||
		working.subjectRevision != loaded.subjectRevision {
		return AttentionLaunchResult{}, workflows.ErrRunAdmissionConflict
	}
	result, err := launch(ctx, &workflows.ReadOnlySessionRef{
		AgentID:          working.agentID,
		Session:          working.sessionKey,
		ExpectedRevision: working.sessionRevision,
	})
	if err != nil {
		return projectAttentionLaunchResult(base, result), fmt.Errorf(
			"launch private working-context attention workflow: %w",
			err,
		)
	}
	return projectAttentionLaunchResult(base, result), nil
}

func validateAttentionLaunchRequest(request AttentionLaunchRequest) error {
	if request.CaseID != strings.TrimSpace(request.CaseID) ||
		!validCaseID(request.CaseID) ||
		request.DecisionPoint != strings.TrimSpace(request.DecisionPoint) ||
		len(request.DecisionPoint) > maximumAttentionDecisionPointBytes ||
		!prDevelopmentAttentionDecisionPattern.MatchString(request.DecisionPoint) {
		return ErrInvalidRequest
	}
	return nil
}

type canonicalPRDevelopmentAttentionDecision struct {
	CaseID              string `json:"case_id"`
	ConversationVersion int64  `json:"conversation_version"`
	DecisionPoint       string `json:"decision_point"`
	PolicyRevision      string `json:"policy_revision"`
	ReviewEntryHash     string `json:"review_entry_hash"`
	ReviewEntryID       string `json:"review_entry_id"`
	SubjectRevision     string `json:"subject_revision"`
}

func canonicalPRDevelopmentAttentionDecisionKey(
	key eventing.PRDevelopmentAttentionDecisionKey,
) (string, error) {
	return sharedattention.CanonicalDecisionKey(canonicalPRDevelopmentAttentionDecision{
		CaseID:              key.CaseID,
		ReviewEntryID:       key.ReviewEntryID,
		ReviewEntryHash:     key.ReviewEntryHash,
		ConversationVersion: key.ConversationVersion,
		SubjectRevision:     key.SubjectRevision,
		DecisionPoint:       key.DecisionPoint,
		PolicyRevision:      key.PolicyRevision,
	})
}

type prDevelopmentAttentionDecisionBinding struct {
	store         eventing.PRDevelopmentAttentionDecisionRunStore
	key           eventing.PRDevelopmentAttentionDecisionKey
	highWater     eventing.PRDevelopmentAttentionHighWater
	canonicalKey  string
	expectedRunID string
}

func (binding *prDevelopmentAttentionDecisionBinding) Find(
	ctx context.Context,
	key string,
) (string, bool, error) {
	if !binding.validCall(key) {
		return "", false, workflows.ErrRunAdmissionConflict
	}
	link, err := binding.store.GetPRDevelopmentAttentionDecisionRun(ctx, binding.key)
	if errors.Is(err, eventing.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapAttentionAdmissionError(err)
	}
	if link.Key != binding.key || link.RunID != binding.expectedRunID ||
		link.CreatedAt.IsZero() {
		return "", false, sharedattention.ErrPrivateRunAdmissionUncertain
	}
	return link.RunID, true, nil
}

func (binding *prDevelopmentAttentionDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	if !binding.validCall(key) || create == nil {
		return "", false, workflows.ErrRunAdmissionConflict
	}
	link, existed, err := binding.store.AdmitPRDevelopmentAttentionDecisionRun(
		ctx,
		eventing.PRDevelopmentAttentionDecisionRunAdmission{
			Key:      binding.key,
			Snapshot: binding.highWater,
			RunID:    binding.expectedRunID,
		},
		create,
	)
	if err != nil {
		return "", false, mapAttentionAdmissionError(err)
	}
	if link.Key != binding.key || link.RunID != binding.expectedRunID ||
		link.CreatedAt.IsZero() || !existed && link.Snapshot != binding.highWater {
		return "", false, workflows.ErrRunAdmissionUnavailable
	}
	return link.RunID, existed, nil
}

func (binding *prDevelopmentAttentionDecisionBinding) validCall(key string) bool {
	return binding != nil && binding.store != nil &&
		key != "" && key == binding.canonicalKey && binding.expectedRunID != ""
}

func mapAttentionAdmissionError(err error) error {
	switch {
	case errors.Is(err, eventing.ErrPRDevelopmentAttentionAdmissionUncertain):
		return sharedattention.ErrPrivateRunAdmissionUncertain
	case errors.Is(err, eventing.ErrPRDevelopmentAttentionConflict),
		errors.Is(err, eventing.ErrNotFound),
		errors.Is(err, workflows.ErrRunAdmissionConflict):
		return workflows.ErrRunAdmissionConflict
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return workflows.ErrRunAdmissionUnavailable
	}
}

func projectAttentionLaunchResult(
	base AttentionLaunchResult,
	result sharedattention.PrivateLaunchResult,
) AttentionLaunchResult {
	base.RunID = result.RunID
	base.Status = result.Status
	base.Existing = result.Existing
	base.Noop = result.Noop
	return base
}

func sanitizeAttentionLaunchError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	// Admission uncertainty is the stronger classification even when the
	// underlying SQLite COMMIT error also wraps cancellation. Reclassifying it
	// as an ordinary canceled request would permit an unsafe automatic retry of
	// the deterministic run.
	if errors.Is(err, sharedattention.ErrPrivateRunAdmissionUncertain) {
		return sharedattention.ErrPrivateRunAdmissionUncertain
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
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
	case errors.Is(err, ErrAttentionSubjectTooLarge):
		return ErrAttentionSubjectTooLarge
	case errors.Is(err, ErrAIContextCompactionRequired):
		return ErrAIContextCompactionRequired
	case errors.Is(err, errPinnedAttentionSubjectDrift):
		return errPinnedAttentionSubjectDrift
	case errors.Is(err, eventing.ErrInvalidPRDevelopmentAttentionTrigger):
		return eventing.ErrInvalidPRDevelopmentAttentionTrigger
	case errors.Is(err, eventing.ErrPRDevelopmentAttentionSuperseded):
		return eventing.ErrPRDevelopmentAttentionSuperseded
	default:
		return ErrUnavailable
	}
}
