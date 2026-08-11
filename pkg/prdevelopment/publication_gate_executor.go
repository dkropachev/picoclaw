package prdevelopment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const publicationGateSubjectModeActive = "active"

// PublicationGateExecutionStore is the least-authority durable boundary for
// one already-claimed active gate. It deliberately omits queue claim/renewal,
// lifecycle completion, push, reconciliation, and provider-write authority.
type PublicationGateExecutionStore interface {
	eventing.PRDevelopmentPublicationGateClaimAuthenticator
	eventing.PRDevelopmentPublicationGateContextSnapshotReader
	eventing.PRDevelopmentPublicationPinnedGateContextSnapshotReader
	eventing.PRDevelopmentPublicationDecisionRunStore
	GetPRDevelopmentCase(
		ctx context.Context,
		id string,
	) (eventing.PRDevelopmentCase, error)
	GetPRDevelopmentPublication(
		ctx context.Context,
		publicationID string,
	) (eventing.PRDevelopmentPublication, error)
	PinPRDevelopmentPublicationSubject(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationSubjectPin,
	) (eventing.PRDevelopmentPublication, bool, error)
	PinPRDevelopmentPublicationProvider(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationProviderPin,
	) (eventing.PRDevelopmentPublication, bool, error)
}

// PublicationGateExecutorConfig contains only immutable local-context readers,
// a read-only provider observer, private workflow execution, and exact decision
// admission. Policy configuration is intentionally absent: execution consumes
// only the already-pinned canonical policy.
type PublicationGateExecutorConfig struct {
	Store          PublicationGateExecutionStore   `json:"-"`
	Executor       *workflows.Executor             `json:"-"`
	Runs           workflows.RunStore              `json:"-"`
	Evidence       AttentionEvidenceStore          `json:"-"`
	Workspaces     AttentionReviewWorkspaceFactory `json:"-"`
	AcquireRuntime AttentionContextRuntimeAcquire  `json:"-"`
	Provider       PublicationProviderObserver     `json:"-"`
}

// PublicationGateExecutionResult is private process-local state describing the
// exact linked private run. Publication never contains its scheduling bearer.
type PublicationGateExecutionResult struct {
	Publication eventing.PRDevelopmentPublication `json:"-"`
	RunID       string                            `json:"-"`
	Status      string                            `json:"-"`
	Existing    bool                              `json:"-"`
}

// PublicationGateExecutor pins and runs an active before-push gate composition
// through the shared private workflow runner. It never changes the publication
// lifecycle and never owns claim renewal, Git, push, or provider writes.
type PublicationGateExecutor struct {
	store          PublicationGateExecutionStore
	executor       *workflows.Executor
	runs           workflows.RunStore
	evidence       AttentionEvidenceStore
	workspaces     AttentionReviewWorkspaceFactory
	acquireRuntime AttentionContextRuntimeAcquire
	provider       PublicationProviderObserver
}

func NewPublicationGateExecutor(
	config PublicationGateExecutorConfig,
) (*PublicationGateExecutor, error) {
	if config.Store == nil || isNilServiceValue(config.Store) {
		return nil, fmt.Errorf("%w: publication gate execution store is required", ErrUnavailable)
	}
	if config.Executor == nil {
		return nil, fmt.Errorf("%w: publication gate workflow executor is required", ErrUnavailable)
	}
	if config.Evidence == nil || isNilServiceValue(config.Evidence) {
		return nil, fmt.Errorf("%w: publication gate CI evidence is required", ErrUnavailable)
	}
	if config.Workspaces == nil || isNilServiceValue(config.Workspaces) {
		return nil, fmt.Errorf("%w: publication gate Git reader is required", ErrUnavailable)
	}
	if config.Provider == nil || isNilServiceValue(config.Provider) {
		return nil, fmt.Errorf("%w: publication provider observer is required", ErrUnavailable)
	}
	runs := config.Runs
	if runs == nil || isNilServiceValue(runs) {
		runs = config.Executor.Store
	}
	if runs == nil || isNilServiceValue(runs) {
		runs = workflows.NewFileRunStore(config.Executor.WorkspaceDir)
	}
	return &PublicationGateExecutor{
		store:          config.Store,
		executor:       config.Executor,
		runs:           runs,
		evidence:       config.Evidence,
		workspaces:     config.Workspaces,
		acquireRuntime: config.AcquireRuntime,
		provider:       config.Provider,
	}, nil
}

func (executor *PublicationGateExecutor) available() bool {
	return executor != nil && executor.store != nil &&
		!isNilServiceValue(executor.store) && executor.executor != nil &&
		executor.runs != nil && !isNilServiceValue(executor.runs) &&
		executor.evidence != nil && !isNilServiceValue(executor.evidence) &&
		executor.workspaces != nil && !isNilServiceValue(executor.workspaces) &&
		executor.provider != nil && !isNilServiceValue(executor.provider)
}

// ExecuteClaim consumes only a live claim originating from pending. Complete
// pins are resolved against their deterministic linked run before any mutable
// context, provider, Git, CI, session, or model read.
func (executor *PublicationGateExecutor) ExecuteClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) (PublicationGateExecutionResult, error) {
	if !executor.available() {
		return PublicationGateExecutionResult{}, ErrUnavailable
	}
	ctx = ctxOrBackground(ctx)
	if err := validatePublicationGateClaimAuthority(claim); err != nil ||
		claim.Status != eventing.PRDevelopmentPublicationClaimed ||
		claim.ClaimFrom != eventing.PRDevelopmentPublicationPending {
		return PublicationGateExecutionResult{}, ErrInvalidRequest
	}
	publication, repository, err := authenticatePublicationGateClaim(
		ctx,
		executor.store,
		claim,
	)
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	policy, found, err := decodePublicationGatePolicy(publication)
	if err != nil || !found || policy.IsNoop() {
		return PublicationGateExecutionResult{}, errPublicationGateCorrupt
	}

	envelope, subject, subjectFound, err := decodePublicationActiveSubject(
		publication,
		policy,
	)
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	if subjectFound && hasPublicationProviderPin(publication) {
		result, existingFound, findErr := executor.findExisting(
			ctx,
			claim,
			publication,
			policy,
		)
		if findErr != nil || existingFound {
			return result, findErr
		}
	}

	var snapshot *eventing.PRDevelopmentPublicationGateContextSnapshot
	if !subjectFound {
		loaded, loadErr := executor.loadCurrentContext(
			ctx,
			claim,
			publication,
			repository,
		)
		if loadErr != nil {
			return PublicationGateExecutionResult{}, loadErr
		}
		snapshot = &loaded
		if err = validatePublicationWorkingAgent(policy, loaded); err != nil {
			return PublicationGateExecutionResult{}, err
		}
		publication, envelope, subject, err = executor.pinActiveSubject(
			ctx,
			claim,
			publication,
			policy,
			loaded,
		)
		if err != nil {
			return PublicationGateExecutionResult{}, err
		}
		if envelope.ConversationVersion != snapshot.Conversation.Version ||
			envelope.TranscriptDigest != snapshot.TranscriptDigest {
			// A concurrent caller may have won the one-time subject pin from a
			// different append-only conversation prefix. Its durable subject is
			// authoritative; reload that exact anchor before provider observation.
			snapshot = nil
		}
	}
	if !hasPublicationProviderPin(publication) {
		if snapshot == nil {
			loaded, loadErr := executor.loadPinnedContext(
				ctx,
				claim,
				publication,
				envelope,
				repository,
			)
			if loadErr != nil {
				return PublicationGateExecutionResult{}, loadErr
			}
			if err = validatePublicationWorkingAgent(policy, loaded); err != nil {
				return PublicationGateExecutionResult{}, err
			}
			snapshot = &loaded
		}
		publication, err = executor.pinProvider(
			ctx,
			claim,
			publication,
			*snapshot,
		)
		if err != nil {
			return PublicationGateExecutionResult{}, err
		}
	}
	if err = validateCompletePublicationProviderPin(publication); err != nil {
		return PublicationGateExecutionResult{}, err
	}

	result, found, err := executor.findExisting(
		ctx,
		claim,
		publication,
		policy,
	)
	if err != nil || found {
		return result, err
	}
	if err = validatePinnedPublicationCompilation(policy, subject); err != nil {
		return PublicationGateExecutionResult{}, err
	}
	return executor.launch(
		ctx,
		claim,
		publication,
		repository,
		policy,
		envelope,
		subject,
	)
}

func (executor *PublicationGateExecutor) loadCurrentContext(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	repository string,
) (eventing.PRDevelopmentPublicationGateContextSnapshot, error) {
	snapshot, err := executor.store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	if err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{},
			publicationGateStoreFailure(err, errPublicationGateLocalEvidence)
	}
	if snapshot.Case.Repository != repository ||
		validatePublicationGateSnapshot(claim, publication, snapshot) != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, errPublicationGateCorrupt
	}
	return snapshot, nil
}

func (executor *PublicationGateExecutor) loadPinnedContext(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	envelope publicationActiveGateSubject,
	repository string,
) (eventing.PRDevelopmentPublicationGateContextSnapshot, error) {
	snapshot, err := executor.store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
		eventing.PRDevelopmentPublicationGateContextAnchor{
			SubjectRevision:     publication.SubjectRevision,
			ConversationVersion: envelope.ConversationVersion,
			TranscriptDigest:    envelope.TranscriptDigest,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{},
			publicationGateStoreFailure(err, errPublicationGateLocalEvidence)
	}
	if snapshot.Case.Repository != repository ||
		validatePublicationGateSnapshot(claim, publication, snapshot) != nil ||
		snapshot.Conversation.Version != envelope.ConversationVersion ||
		snapshot.TranscriptDigest != envelope.TranscriptDigest ||
		envelope.Repository != snapshot.Case.Repository ||
		envelope.SelectedOrdinal != snapshot.SelectedOrdinal ||
		envelope.ThreadCasesDigest != snapshot.Thread.CasesDigest ||
		envelope.LedgerEntriesDigest != snapshot.Ledger.EntriesDigest ||
		envelope.LedgerCheckpointsDigest != snapshot.Ledger.CheckpointsDigest {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, errPublicationGateCorrupt
	}
	return snapshot, nil
}

func (executor *PublicationGateExecutor) pinActiveSubject(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) (
	eventing.PRDevelopmentPublication,
	publicationActiveGateSubject,
	map[string]any,
	error,
) {
	converted, err := publicationGateAttentionSnapshot(snapshot)
	if err != nil {
		return publication, publicationActiveGateSubject{}, nil, err
	}
	loader, err := executor.contextLoader()
	if err != nil {
		return publication, publicationActiveGateSubject{}, nil, err
	}
	loaded, err := loader.loadForReviewOutcome(
		ctx,
		converted,
		eventing.PRDevelopmentLedgerReviewPassed,
	)
	if err != nil {
		return publication, publicationActiveGateSubject{}, nil, err
	}
	if err = validatePinnedPublicationCompilation(policy, loaded.subject); err != nil {
		return publication, publicationActiveGateSubject{}, nil, err
	}
	modelCanonical, err := json.Marshal(loaded.subject)
	if err != nil || len(modelCanonical) == 0 ||
		len(modelCanonical) > workflows.MaxWorkflowGateSubjectBytes {
		return publication, publicationActiveGateSubject{}, nil, errPublicationGateCorrupt
	}
	_, canonical, revision, err := buildPublicationActiveSubject(
		snapshot,
		policy,
		modelCanonical,
	)
	if err != nil {
		return publication, publicationActiveGateSubject{}, nil, err
	}
	pinned, _, pinErr := executor.store.PinPRDevelopmentPublicationSubject(
		ctx,
		eventing.PRDevelopmentPublicationSubjectPin{
			PublicationID:               claim.ID,
			ClaimToken:                  claim.ClaimToken,
			ClaimEpoch:                  claim.ClaimEpoch,
			PolicyRevision:              policy.DecisionRevision(),
			SubjectRevision:             revision,
			PinnedSubject:               canonical,
			ExpectedConversationVersion: snapshot.Conversation.Version,
			ExpectedTranscriptDigest:    snapshot.TranscriptDigest,
		},
	)
	if pinErr != nil {
		current, loadErr := executor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil {
			recovered, recoveredSubject, found, decodeErr := decodePublicationActiveSubject(current, policy)
			if decodeErr == nil && found &&
				validClaimedPublicationGateResponse(claim, current) {
				return current, recovered, recoveredSubject, nil
			}
		}
		return publication, publicationActiveGateSubject{}, nil,
			publicationGateStoreFailure(pinErr, errPublicationGateLocalEvidence)
	}
	decoded, subject, found, err := decodePublicationActiveSubject(pinned, policy)
	if err != nil || !found ||
		!validClaimedPublicationGateResponse(claim, pinned) {
		return publication, publicationActiveGateSubject{}, nil, errPublicationGateCorrupt
	}
	return pinned, decoded, subject, nil
}

func (executor *PublicationGateExecutor) pinProvider(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) (eventing.PRDevelopmentPublication, error) {
	if hasPublicationProviderPin(publication) {
		if err := validateCompletePublicationProviderPin(publication); err != nil {
			return publication, err
		}
		return publication, nil
	}
	timed, err := executor.provider.ObservePublication(
		ctx,
		snapshot.Case,
		snapshot.Thread.Identity,
	)
	if err != nil {
		return publication, err
	}
	if err = ctx.Err(); err != nil {
		return publication, err
	}
	if timed.ObservedAt.IsZero() ||
		!publicationProviderMatchesGateContext(timed.Observation, snapshot) {
		return publication, errPublicationGateProviderChanged
	}
	pinned, _, pinErr := executor.store.PinPRDevelopmentPublicationProvider(
		ctx,
		eventing.PRDevelopmentPublicationProviderPin{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Observation:   timed.Observation,
			ObservedAt:    timed.ObservedAt,
		},
	)
	if pinErr != nil {
		current, loadErr := executor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil && hasPublicationProviderPin(current) &&
			validateRecoveredPublicationProviderStage(claim, current, snapshot) == nil {
			return current, nil
		}
		return publication,
			publicationGateStoreFailure(pinErr, errPublicationGateProviderChanged)
	}
	if err = validatePublicationProviderStage(claim, pinned, timed); err != nil {
		return publication, err
	}
	return pinned, nil
}

func (executor *PublicationGateExecutor) findExisting(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) (PublicationGateExecutionResult, bool, error) {
	runner, canonical, err := executor.privateRunner(claim, publication)
	if err != nil {
		return PublicationGateExecutionResult{}, false, err
	}
	result, found, err := runner.FindExisting(ctx, canonical)
	if err != nil {
		return PublicationGateExecutionResult{}, false, err
	}
	if !found {
		return PublicationGateExecutionResult{}, false, nil
	}
	if result.Noop || result.RunID == "" || result.Status == "" ||
		policy.IsNoop() {
		return PublicationGateExecutionResult{}, false, errPublicationGateCorrupt
	}
	return projectPublicationGateExecution(publication, result), true, nil
}

func (executor *PublicationGateExecutor) launch(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	repository string,
	policy sharedattention.PreparedPolicy,
	envelope publicationActiveGateSubject,
	subject map[string]any,
) (PublicationGateExecutionResult, error) {
	runner, canonical, err := executor.privateRunner(claim, publication)
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	request := sharedattention.PrivateLaunchRequest{
		DecisionKey: canonical,
		Policy:      policy,
		Selector: sharedattention.PolicySelector{
			Repository:    repository,
			DecisionPoint: eventing.PRDevelopmentPublicationDecisionBeforePush,
		},
		RevalidateLive: false,
		Subject:        subject,
	}
	workingAgentID := policy.WorkingContextAgentID()
	if workingAgentID == "" {
		result, launchErr := runner.Launch(ctx, request)
		return projectPublicationGateLaunch(publication, result, launchErr)
	}
	if executor.acquireRuntime == nil {
		return PublicationGateExecutionResult{}, fmt.Errorf(
			"%w: owner runtime is not configured",
			ErrUnavailable,
		)
	}
	expectedContext, err := executor.loadPinnedContext(
		ctx,
		claim,
		publication,
		envelope,
		repository,
	)
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	if err = validatePublicationWorkingAgent(policy, expectedContext); err != nil {
		return PublicationGateExecutionResult{}, err
	}
	expected, err := publicationGateAttentionSnapshot(expectedContext)
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	loader, err := executor.contextLoader()
	if err != nil {
		return PublicationGateExecutionResult{}, err
	}
	var result sharedattention.PrivateLaunchResult
	err = loader.withRuntimeContextRefreshForReviewOutcome(
		ctx,
		expected,
		workingAgentID,
		eventing.PRDevelopmentLedgerReviewPassed,
		func(refreshCtx context.Context) (eventing.PRDevelopmentAttentionSnapshot, error) {
			current, refreshErr := executor.loadPinnedContext(
				refreshCtx,
				claim,
				publication,
				envelope,
				repository,
			)
			if refreshErr != nil {
				return eventing.PRDevelopmentAttentionSnapshot{}, refreshErr
			}
			return publicationGateAttentionSnapshot(current)
		},
		func(
			runtimeCtx context.Context,
			current eventing.PRDevelopmentAttentionSnapshot,
			rawStore session.SessionStore,
		) error {
			working, projectErr := loader.projectWorkingContext(
				runtimeCtx,
				attentionContext{
					snapshot:        current,
					subject:         subject,
					canonical:       append([]byte(nil), envelope.ModelSubject...),
					subjectRevision: publication.SubjectRevision,
				},
				workingAgentID,
				rawStore,
			)
			if projectErr != nil {
				return projectErr
			}
			request.ReadOnlySession = &workflows.ReadOnlySessionRef{
				AgentID:          working.agentID,
				Session:          working.sessionKey,
				ExpectedRevision: working.sessionRevision,
			}
			var launchErr error
			result, launchErr = runner.Launch(runtimeCtx, request)
			return launchErr
		},
	)
	return projectPublicationGateLaunch(publication, result, err)
}

func (executor *PublicationGateExecutor) contextLoader() (*attentionContextLoader, error) {
	return newAttentionContextLoader(
		publicationAttentionContextStore{cases: executor.store},
		executor.evidence,
		executor.workspaces,
		executor.acquireRuntime,
	)
}

type publicationAttentionContextStore struct {
	cases interface {
		GetPRDevelopmentCase(
			ctx context.Context,
			caseID string,
		) (eventing.PRDevelopmentCase, error)
	}
}

func (store publicationAttentionContextStore) GetPRDevelopmentCase(
	ctx context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	return store.cases.GetPRDevelopmentCase(ctx, caseID)
}

func (publicationAttentionContextStore) GetPRDevelopmentAttentionSnapshot(
	context.Context,
	string,
) (eventing.PRDevelopmentAttentionSnapshot, error) {
	return eventing.PRDevelopmentAttentionSnapshot{}, ErrUnavailable
}

func (executor *PublicationGateExecutor) privateRunner(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
) (*sharedattention.PrivateRunner, string, error) {
	key := publicationDecisionKey(publication)
	canonical, err := canonicalPRDevelopmentPublicationDecisionKey(key)
	if err != nil {
		return nil, "", errPublicationGateCorrupt
	}
	runID, err := sharedattention.RunIDForDecisionKey(canonical)
	if err != nil {
		return nil, "", errPublicationGateCorrupt
	}
	binding := &prDevelopmentPublicationDecisionBinding{
		store:         executor.store,
		key:           key,
		canonicalKey:  canonical,
		expectedRunID: runID,
		claimToken:    claim.ClaimToken,
		claimEpoch:    claim.ClaimEpoch,
	}
	runner, err := sharedattention.NewPrivateRunner(sharedattention.PrivateRunnerConfig{
		Executor:  executor.executor,
		Runs:      executor.runs,
		Policies:  publicationPinnedPolicySource,
		Decisions: binding,
	})
	if err != nil {
		return nil, "", ErrUnavailable
	}
	return runner, canonical, nil
}

var publicationPinnedPolicySource = sharedattention.PolicySourceFunc(func(
	context.Context,
	sharedattention.PolicySelector,
	sharedattention.PolicyUse,
) error {
	return sharedattention.ErrPolicyChanged
})

func publicationDecisionKey(
	publication eventing.PRDevelopmentPublication,
) eventing.PRDevelopmentPublicationDecisionKey {
	return eventing.PRDevelopmentPublicationDecisionKey{
		PublicationID:           publication.ID,
		ReviewLedgerEntryID:     publication.ReviewLedgerEntryID,
		ReviewLedgerEntryHash:   publication.ReviewLedgerEntryHash,
		PolicyRevision:          publication.PolicyRevision,
		SubjectRevision:         publication.SubjectRevision,
		ProviderObservationHash: publication.ProviderObservationHash,
	}
}

type prDevelopmentPublicationDecisionBinding struct {
	store         eventing.PRDevelopmentPublicationDecisionRunStore
	key           eventing.PRDevelopmentPublicationDecisionKey
	canonicalKey  string
	expectedRunID string
	claimToken    string
	claimEpoch    int64
}

func (binding *prDevelopmentPublicationDecisionBinding) Find(
	ctx context.Context,
	key string,
) (string, bool, error) {
	if !binding.validCall(key) {
		return "", false, workflows.ErrRunAdmissionConflict
	}
	link, err := binding.store.GetPRDevelopmentPublicationDecisionRun(ctx, binding.key)
	if errors.Is(err, eventing.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapPublicationDecisionLookupError(err)
	}
	if link.Key != binding.key || link.RunID != binding.expectedRunID {
		return "", false, sharedattention.ErrPrivateRunAdmissionUncertain
	}
	return link.RunID, true, nil
}

func (binding *prDevelopmentPublicationDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	if !binding.validCall(key) || create == nil {
		return "", false, workflows.ErrRunAdmissionConflict
	}
	link, existed, err := binding.store.AdmitPRDevelopmentPublicationDecisionRun(
		ctx,
		eventing.PRDevelopmentPublicationDecisionRunAdmission{
			Key:        binding.key,
			RunID:      binding.expectedRunID,
			ClaimToken: binding.claimToken,
			ClaimEpoch: binding.claimEpoch,
		},
		create,
	)
	if err != nil {
		return "", false, mapPublicationDecisionAdmissionError(err)
	}
	if link.Key != binding.key || link.RunID != binding.expectedRunID {
		return "", false, sharedattention.ErrPrivateRunAdmissionUncertain
	}
	return link.RunID, existed, nil
}

func (binding *prDevelopmentPublicationDecisionBinding) validCall(key string) bool {
	return binding != nil && binding.store != nil &&
		key != "" && key == binding.canonicalKey && binding.expectedRunID != "" &&
		binding.claimToken != "" && binding.claimEpoch > 0
}

func mapPublicationDecisionLookupError(err error) error {
	switch {
	case errors.Is(err, eventing.ErrPRDevelopmentPublicationAdmissionUncertain),
		errors.Is(err, eventing.ErrPRDevelopmentPublicationConflict),
		errors.Is(err, eventing.ErrPRDevelopmentPublicationSuperseded),
		errors.Is(err, eventing.ErrInvalidPRDevelopmentPublication),
		errors.Is(err, workflows.ErrRunAdmissionConflict):
		// An exact historical lookup has no mutable-state conflict path. Any
		// durable identity/admission error here means the decision link cannot
		// be proven safe to replay or recreate.
		return sharedattention.ErrPrivateRunAdmissionUncertain
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return workflows.ErrRunAdmissionUnavailable
	}
}

func mapPublicationDecisionAdmissionError(err error) error {
	switch {
	case errors.Is(err, eventing.ErrPRDevelopmentPublicationAdmissionUncertain):
		return sharedattention.ErrPrivateRunAdmissionUncertain
	case errors.Is(err, eventing.ErrPRDevelopmentPublicationConflict),
		errors.Is(err, eventing.ErrPRDevelopmentPublicationSuperseded),
		errors.Is(err, eventing.ErrStaleLease),
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

func projectPublicationGateExecution(
	publication eventing.PRDevelopmentPublication,
	result sharedattention.PrivateLaunchResult,
) PublicationGateExecutionResult {
	return PublicationGateExecutionResult{
		Publication: redactPublicationGateClaim(publication),
		RunID:       result.RunID,
		Status:      result.Status,
		Existing:    result.Existing,
	}
}

func projectPublicationGateLaunch(
	publication eventing.PRDevelopmentPublication,
	result sharedattention.PrivateLaunchResult,
	err error,
) (PublicationGateExecutionResult, error) {
	// PrivateRunner deliberately returns no result when deterministic admission
	// is uncertain. Do not attach the claimed publication to that empty result:
	// the lifecycle owner must quarantine the claim based on the error alone.
	if err != nil && result.RunID == "" && result.Status == "" {
		return PublicationGateExecutionResult{}, err
	}
	return projectPublicationGateExecution(publication, result), err
}

type publicationActiveGateSubject struct {
	Format                   string          `json:"format"`
	Mode                     string          `json:"mode"`
	DecisionPoint            string          `json:"decision_point"`
	PublicationID            string          `json:"publication_id"`
	CaseID                   string          `json:"case_id"`
	Repository               string          `json:"repository"`
	ThreadID                 string          `json:"thread_id"`
	SelectedOrdinal          int             `json:"selected_ordinal"`
	ThreadCasesDigest        string          `json:"thread_cases_digest"`
	PolicyRevision           string          `json:"policy_revision"`
	ConversationVersion      int64           `json:"conversation_version"`
	TranscriptDigest         string          `json:"transcript_digest"`
	AttemptID                string          `json:"attempt_id"`
	AttemptEntryID           string          `json:"attempt_entry_id"`
	AttemptEntryHash         string          `json:"attempt_entry_hash"`
	ReviewEntryID            string          `json:"review_entry_id"`
	ReviewEntryHash          string          `json:"review_entry_hash"`
	FenceOrdinal             int             `json:"fence_ordinal"`
	FenceHash                string          `json:"fence_hash"`
	OrchestrationReceiptHash string          `json:"orchestration_receipt_hash"`
	CIPlanDigest             string          `json:"ci_plan_digest"`
	CIResultDigest           string          `json:"ci_result_digest"`
	LedgerEntriesDigest      string          `json:"ledger_entries_digest"`
	LedgerCheckpointsDigest  string          `json:"ledger_checkpoints_digest"`
	TipCommit                string          `json:"tip_commit"`
	Tree                     string          `json:"tree"`
	NoChanges                bool            `json:"no_changes"`
	ModelSubject             json.RawMessage `json:"model_subject"`
}

func buildPublicationActiveSubject(
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
	policy sharedattention.PreparedPolicy,
	modelSubject []byte,
) (publicationActiveGateSubject, []byte, string, error) {
	publication := snapshot.Publication
	envelope := publicationActiveGateSubject{
		Format:                   publicationGateSubjectFormat,
		Mode:                     publicationGateSubjectModeActive,
		DecisionPoint:            eventing.PRDevelopmentPublicationDecisionBeforePush,
		PublicationID:            publication.ID,
		CaseID:                   publication.CaseID,
		Repository:               snapshot.Case.Repository,
		ThreadID:                 publication.ThreadID,
		SelectedOrdinal:          snapshot.SelectedOrdinal,
		ThreadCasesDigest:        snapshot.Thread.CasesDigest,
		PolicyRevision:           policy.DecisionRevision(),
		ConversationVersion:      snapshot.Conversation.Version,
		TranscriptDigest:         snapshot.TranscriptDigest,
		AttemptID:                publication.AttemptID,
		AttemptEntryID:           publication.AttemptLedgerEntryID,
		AttemptEntryHash:         publication.AttemptLedgerEntryHash,
		ReviewEntryID:            publication.ReviewLedgerEntryID,
		ReviewEntryHash:          publication.ReviewLedgerEntryHash,
		FenceOrdinal:             publication.FenceOrdinal,
		FenceHash:                publication.FenceHash,
		OrchestrationReceiptHash: publication.OrchestrationReceiptHash,
		CIPlanDigest:             publication.CIPlanDigest,
		CIResultDigest:           publication.CIResultDigest,
		LedgerEntriesDigest:      snapshot.Ledger.EntriesDigest,
		LedgerCheckpointsDigest:  snapshot.Ledger.CheckpointsDigest,
		TipCommit:                publication.TipCommit,
		Tree:                     publication.Tree,
		NoChanges:                publication.NoChanges,
		ModelSubject:             append(json.RawMessage(nil), modelSubject...),
	}
	if err := validatePublicationActiveSubject(envelope, publication, policy); err != nil {
		return publicationActiveGateSubject{}, nil, "", err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || len(canonical) > eventing.MaxPRDevelopmentPublicationSubjectBytes {
		return publicationActiveGateSubject{}, nil, "", ErrAttentionSubjectTooLarge
	}
	return envelope, canonical, publicationZeroSubjectRevision(canonical), nil
}

func decodePublicationActiveSubject(
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) (publicationActiveGateSubject, map[string]any, bool, error) {
	hasRevision := publication.SubjectRevision != ""
	hasCanonical := len(publication.PinnedSubject) != 0
	hasHash := publication.PinnedSubjectHash != ""
	if !hasRevision && !hasCanonical && !hasHash {
		return publicationActiveGateSubject{}, nil, false, nil
	}
	if !hasRevision || !hasCanonical || !hasHash ||
		!validControllerSHA256(publication.PinnedSubjectHash) ||
		len(publication.PinnedSubject) > eventing.MaxPRDevelopmentPublicationSubjectBytes {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(publication.PinnedSubject))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var envelope publicationActiveGateSubject
	if err := decoder.Decode(&envelope); err != nil {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(canonical, publication.PinnedSubject) ||
		publication.SubjectRevision != publicationZeroSubjectRevision(canonical) ||
		validatePublicationActiveSubject(envelope, publication, policy) != nil {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	modelDecoder := json.NewDecoder(bytes.NewReader(envelope.ModelSubject))
	modelDecoder.UseNumber()
	var subject map[string]any
	if err = modelDecoder.Decode(&subject); err != nil || subject == nil {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	if err = modelDecoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	modelCanonical, err := json.Marshal(subject)
	if err != nil || !bytes.Equal(modelCanonical, envelope.ModelSubject) ||
		len(modelCanonical) > workflows.MaxWorkflowGateSubjectBytes {
		return publicationActiveGateSubject{}, nil, true, errPublicationGateCorrupt
	}
	return envelope, subject, true, nil
}

func validatePublicationActiveSubject(
	envelope publicationActiveGateSubject,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) error {
	if envelope.Format != publicationGateSubjectFormat ||
		envelope.Mode != publicationGateSubjectModeActive ||
		envelope.DecisionPoint != eventing.PRDevelopmentPublicationDecisionBeforePush ||
		envelope.PublicationID != publication.ID || envelope.CaseID != publication.CaseID ||
		envelope.ThreadID != publication.ThreadID || envelope.SelectedOrdinal < 0 ||
		envelope.SelectedOrdinal >= eventing.MaxPRDevelopmentThreadCases ||
		envelope.PolicyRevision != policy.DecisionRevision() || policy.IsNoop() ||
		envelope.ConversationVersion < 0 ||
		envelope.ConversationVersion > eventing.MaxPRDevelopmentMessagesPerCase ||
		!validProviderRepositoryIdentity(envelope.Repository) ||
		envelope.AttemptID != publication.AttemptID ||
		envelope.AttemptEntryID != publication.AttemptLedgerEntryID ||
		envelope.AttemptEntryHash != publication.AttemptLedgerEntryHash ||
		envelope.ReviewEntryID != publication.ReviewLedgerEntryID ||
		envelope.ReviewEntryHash != publication.ReviewLedgerEntryHash ||
		envelope.FenceOrdinal != publication.FenceOrdinal ||
		envelope.FenceHash != publication.FenceHash ||
		envelope.OrchestrationReceiptHash != publication.OrchestrationReceiptHash ||
		envelope.CIPlanDigest != publication.CIPlanDigest ||
		envelope.CIResultDigest != publication.CIResultDigest ||
		envelope.TipCommit != publication.TipCommit || envelope.Tree != publication.Tree ||
		envelope.NoChanges != publication.NoChanges || len(envelope.ModelSubject) == 0 ||
		len(envelope.ModelSubject) > workflows.MaxWorkflowGateSubjectBytes ||
		!validControllerSHA256(envelope.ThreadCasesDigest) ||
		!validControllerSHA256(envelope.TranscriptDigest) ||
		!validControllerSHA256(envelope.AttemptEntryHash) ||
		!validControllerSHA256(envelope.ReviewEntryHash) ||
		!validControllerSHA256(envelope.FenceHash) ||
		!validControllerSHA256(envelope.OrchestrationReceiptHash) ||
		!validControllerSHA256(envelope.CIPlanDigest) ||
		!validControllerSHA256(envelope.CIResultDigest) ||
		!validControllerSHA256(envelope.LedgerEntriesDigest) ||
		!validControllerSHA256(envelope.LedgerCheckpointsDigest) ||
		!validAttentionRevision(envelope.PolicyRevision) {
		return errPublicationGateCorrupt
	}
	return nil
}

func publicationGateAttentionSnapshot(
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) (eventing.PRDevelopmentAttentionSnapshot, error) {
	publication := snapshot.Publication
	// Attempt evidence is appended at the mutation-stage fence, before the
	// immutable review lease fields produce the final reviewed-fence hash. The
	// event-store gate snapshot carries both proofs; bind them explicitly before
	// adapting the snapshot to the shared attention projection.
	if !validControllerSHA256(snapshot.AttemptEntry.FenceHash) ||
		snapshot.AttemptEntry.FenceHash == snapshot.Fence.FenceHash ||
		snapshot.AttemptEntry.FenceHash != snapshot.Orchestration.FenceHash ||
		snapshot.ReviewEntry.FenceHash != snapshot.Fence.FenceHash {
		return eventing.PRDevelopmentAttentionSnapshot{}, errPublicationGateCorrupt
	}
	attemptOrdinal := -1
	for index, attempt := range snapshot.OwnerSession.Attempts {
		if attempt.ID == publication.AttemptID {
			if attemptOrdinal >= 0 {
				return eventing.PRDevelopmentAttentionSnapshot{}, errPublicationGateCorrupt
			}
			attemptOrdinal = index
		}
	}
	if attemptOrdinal < 0 {
		return eventing.PRDevelopmentAttentionSnapshot{}, errPublicationGateCorrupt
	}
	return eventing.PRDevelopmentAttentionSnapshot{
		Case:         snapshot.Case,
		Thread:       snapshot.Thread,
		Conversation: snapshot.Conversation,
		OwnerSession: snapshot.OwnerSession,
		Controller:   snapshot.Controller,
		Fence:        snapshot.Fence,
		Ledger:       snapshot.Ledger,
		ReviewEntry:  snapshot.ReviewEntry,
		HighWater: eventing.PRDevelopmentAttentionHighWater{
			CaseID:                  publication.CaseID,
			SelectedOrdinal:         snapshot.SelectedOrdinal,
			ConversationVersion:     snapshot.Conversation.Version,
			TranscriptDigest:        snapshot.TranscriptDigest,
			ThreadID:                publication.ThreadID,
			ThreadCaseCount:         snapshot.Thread.CaseCount,
			ThreadCasesDigest:       snapshot.Thread.CasesDigest,
			LedgerEntryCount:        len(snapshot.Ledger.Entries),
			LedgerEntriesDigest:     snapshot.Ledger.EntriesDigest,
			LedgerCheckpointCount:   len(snapshot.Ledger.Checkpoints),
			LedgerCheckpointsDigest: snapshot.Ledger.CheckpointsDigest,
			ReviewEntryID:           snapshot.ReviewEntry.ID,
			ReviewEntryOrdinal:      snapshot.ReviewEntry.Ordinal,
			ReviewEntryHash:         snapshot.ReviewEntry.EntryHash,
			AttemptID:               publication.AttemptID,
			AttemptOrdinal:          attemptOrdinal,
			FenceOrdinal:            publication.FenceOrdinal,
			FenceHash:               publication.FenceHash,
			ControllerID:            publication.ControllerID,
			ControllerRevision:      publication.ControllerRevision,
			ControllerLineVersion:   snapshot.Controller.LineVersion,
			ControllerFenceCount:    snapshot.Controller.FenceCount,
			ControllerFencesDigest:  snapshot.Controller.FencesDigest,
			OwnerSessionID:          publication.OwnerSessionID,
			OwnerSessionVersion:     snapshot.OwnerSession.Version,
			OwnerAttemptCount:       len(snapshot.OwnerSession.Attempts),
		},
	}, nil
}

func validatePublicationWorkingAgent(
	policy sharedattention.PreparedPolicy,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) error {
	for _, gate := range policy.EffectiveGates() {
		if gate.Kind == workflows.GateAIWorkingContext &&
			(!routing.IsCanonicalAgentID(gate.AgentID) ||
				gate.AgentID != snapshot.Controller.AgentID ||
				gate.AgentID != snapshot.OwnerSession.AgentID) {
			return errPublicationGateCorrupt
		}
	}
	return nil
}

func validatePinnedPublicationCompilation(
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
		return errPublicationGateCorrupt
	}
	workingAgentID := policy.WorkingContextAgentID()
	if workingAgentID == "" {
		if compilation.RequiresSession || compilation.RequiredSessionAgentID != "" {
			return errPublicationGateCorrupt
		}
		return nil
	}
	if !compilation.RequiresSession ||
		compilation.RequiredSessionAgentID != workingAgentID {
		return errPublicationGateCorrupt
	}
	return nil
}

var (
	_ sharedattention.DecisionBinding = (*prDevelopmentPublicationDecisionBinding)(nil)
	_ attentionContextStore           = publicationAttentionContextStore{}
)
