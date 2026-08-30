package prworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/prlifecycle"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	defaultWorkspaceListLimit = 50
	maxCharterItems           = 128
	maxCharterTextBytes       = 64 << 10
)

type ResolveRequest struct {
	PullRequestURL string
	ProviderOrigin string
	Repository     string
	PullNumber     int64
}

type ProviderResolver interface {
	ResolvePullRequest(ctx context.Context, request ResolveRequest) (ProviderSnapshot, error)
}

type IssueResolveRequest struct {
	IssueURL string
}

type IssueProviderResolver interface {
	ResolveIssue(ctx context.Context, request IssueResolveRequest) (ProviderSnapshot, error)
}

type RepositoryResolveRequest struct {
	RepositoryIdentity string
	Brief              string
}

type RepositoryProviderResolver interface {
	ResolveRepository(ctx context.Context, request RepositoryResolveRequest) (ProviderSnapshot, error)
}

type ConfiguredRepository struct {
	Identity      string `json:"identity"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CanImplement  bool   `json:"can_implement"`
}

type ConfiguredRepositoryLister interface {
	ListConfiguredRepositories(ctx context.Context) ([]ConfiguredRepository, error)
}

type ConfiguredRepositoryVerifier interface {
	VerifyRepository(ctx context.Context, identity string) (ConfiguredRepository, error)
}

type GateRequest struct {
	WorkspaceID      string
	WorkspaceVersion int64
	ProviderOrigin   string
	RepositoryID     string
	DecisionPoint    string
	Subject          map[string]any
	SubjectDigest    string
	WorkingContext   PRContextBundle
}

type GateEvaluator interface {
	Start(ctx context.Context, request GateRequest) (GateRun, error)
	Respond(ctx context.Context, gate GateRun, fieldValues map[string]any) (GateRun, error)
}

// CandidateEvidenceLoader recovers the exact immutable candidate reviewed by
// a standalone completion audit. Implementations must fence the returned diff
// to the persisted repair publication tuple, including after process restart.
type CandidateEvidenceLoader interface {
	LoadCandidateEvidence(ctx context.Context, repair RepairAttempt) (CandidateEvidence, error)
}

type PlanningEvidenceLoader interface {
	LoadPlanningEvidence(
		ctx context.Context,
		workspaceID string,
		provider ProviderSnapshot,
	) (json.RawMessage, error)
}

type CandidateEvidence struct {
	CandidateSHA   string           `json:"candidate_sha"`
	CandidateDiff  string           `json:"candidate_diff"`
	Metrics        CandidateMetrics `json:"metrics"`
	EvidenceDigest string           `json:"evidence_digest"`
}

type ServiceConfig struct {
	Store                          Store
	Provider                       ProviderResolver
	ReviewEvidence                 ReviewEvidenceLoader
	CandidateEvidence              CandidateEvidenceLoader
	PlanningEvidence               PlanningEvidenceLoader
	AI                             IsolatedAIRunner
	ReviewWorkflow                 ReviewWorkflowExecutor
	Gates                          GateEvaluator
	DeferredIssueMode              DeferredIssueMode
	DeferredIssueModeForRepository func(providerOrigin, repositoryID string) DeferredIssueMode
	ScopeDispositionForRepository  func(providerOrigin, repositoryID string) ScopeDispositionPolicy
	Now                            func() time.Time
}

type Service struct {
	store                          Store
	provider                       ProviderResolver
	reviewEvidence                 ReviewEvidenceLoader
	candidateEvidence              CandidateEvidenceLoader
	planningEvidence               PlanningEvidenceLoader
	ai                             AIController
	reviewWorkflow                 ReviewWorkflowExecutor
	gates                          GateEvaluator
	deferredIssueMode              DeferredIssueMode
	deferredIssueModeForRepository func(providerOrigin, repositoryID string) DeferredIssueMode
	scopeDispositionForRepository  func(providerOrigin, repositoryID string) ScopeDispositionPolicy
	now                            func() time.Time
}

var implementationSideEffectClaims = struct {
	sync.Mutex
	workspaces map[string]struct{}
}{workspaces: make(map[string]struct{})}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Store == nil {
		return nil, errors.New("PR workspace store is required")
	}
	if config.DeferredIssueMode == "" {
		config.DeferredIssueMode = DeferredIssuesAsk
	}
	if !validDeferredIssueMode(config.DeferredIssueMode) {
		return nil, errors.New("invalid PR workspace deferred issue mode")
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	reviewWorkflow := config.ReviewWorkflow
	if reviewWorkflow == nil {
		reviewWorkflow = newIsolatedReviewWorkflow(config.AI)
	}
	return &Service{
		store: newPublicationFencedStore(
			config.Store,
		),
		provider:                       config.Provider,
		reviewEvidence:                 config.ReviewEvidence,
		candidateEvidence:              config.CandidateEvidence,
		planningEvidence:               config.PlanningEvidence,
		ai:                             AIController{Runner: config.AI},
		reviewWorkflow:                 reviewWorkflow,
		gates:                          config.Gates,
		deferredIssueMode:              config.DeferredIssueMode,
		deferredIssueModeForRepository: config.DeferredIssueModeForRepository,
		scopeDispositionForRepository:  config.ScopeDispositionForRepository,
		now:                            now,
	}, nil
}

func (service *Service) scopeDisposition(aggregate Aggregate) ScopeDispositionPolicy {
	if service != nil && service.scopeDispositionForRepository != nil {
		return service.scopeDispositionForRepository(
			aggregate.ProviderSnapshot.ProviderOrigin, aggregate.ProviderSnapshot.RepositoryID,
		)
	}
	return DefaultScopeDispositionPolicy()
}

func (service *Service) deferredMode(aggregate Aggregate) DeferredIssueMode {
	if service != nil && service.deferredIssueModeForRepository != nil {
		mode := service.deferredIssueModeForRepository(
			aggregate.Workspace.ProviderOrigin,
			aggregate.Workspace.RepositoryID,
		)
		if validDeferredIssueMode(mode) {
			return mode
		}
		return DeferredIssuesOff
	}
	if service == nil || !validDeferredIssueMode(service.deferredIssueMode) {
		return DeferredIssuesOff
	}
	return service.deferredIssueMode
}

// claimImplementation serializes the side-effecting implementation operation
// for one PR workspace inside a service process. The aggregate CAS remains the
// durable authority; this claim closes the window in which two callers at the
// same aggregate version could both edit and validate before either CAS runs.
func (service *Service) claimImplementation(workspaceID string) bool {
	implementationSideEffectClaims.Lock()
	defer implementationSideEffectClaims.Unlock()
	if _, claimed := implementationSideEffectClaims.workspaces[workspaceID]; claimed {
		return false
	}
	implementationSideEffectClaims.workspaces[workspaceID] = struct{}{}
	return true
}

func (service *Service) releaseImplementation(workspaceID string) {
	implementationSideEffectClaims.Lock()
	delete(implementationSideEffectClaims.workspaces, workspaceID)
	implementationSideEffectClaims.Unlock()
}

type CreateWorkspaceRequest struct {
	RequestID          string
	Intent             DevelopmentIntent
	SourceKind         SourceKind
	PullRequestURL     string
	IssueURL           string
	RepositoryIdentity string
	Brief              string
}

func (service *Service) Create(ctx context.Context, request CreateWorkspaceRequest) (Aggregate, error) {
	if service == nil || service.store == nil || service.provider == nil {
		return Aggregate{}, errors.New("PR workspace intake is unavailable")
	}
	if !validRequestID(request.RequestID) || !validCreateWorkspaceRequest(request) {
		return Aggregate{}, ErrInvalid
	}
	var provider ProviderSnapshot
	var resolveErr error
	switch request.SourceKind {
	case SourcePullRequest:
		provider, resolveErr = service.provider.ResolvePullRequest(ctx, ResolveRequest{
			PullRequestURL: request.PullRequestURL,
		})
	case SourceIssue:
		resolver, ok := service.provider.(IssueProviderResolver)
		if !ok {
			return Aggregate{}, errors.New("GitHub issue intake is unavailable")
		}
		provider, resolveErr = resolver.ResolveIssue(ctx, IssueResolveRequest{IssueURL: request.IssueURL})
	case SourceBrief:
		resolver, ok := service.provider.(RepositoryProviderResolver)
		if !ok {
			return Aggregate{}, errors.New("configured repository intake is unavailable")
		}
		provider, resolveErr = resolver.ResolveRepository(ctx, RepositoryResolveRequest{
			RepositoryIdentity: request.RepositoryIdentity,
			Brief:              request.Brief,
		})
	}
	if resolveErr != nil {
		return Aggregate{}, resolveErr
	}
	provider.Intent = request.Intent
	provider.SourceKind = request.SourceKind
	if request.SourceKind == SourcePullRequest {
		provider.SourceID = provider.PullRequestID
		provider.SourceNumber = provider.PullNumber
		provider.SourceURL = request.PullRequestURL
		if provider.State != "open" || !provider.HeadWritable {
			return Aggregate{}, errors.New("pull request must be open and writable")
		}
	}
	if request.SourceKind == SourceBrief {
		// A brief is a request to start new work, not a reusable provider entity.
		// Include the idempotency key so retries replay while two identical briefs
		// intentionally create independent development workspaces.
		digest := sha256.Sum256([]byte(strings.Join([]string{
			"development-brief-request-v1", provider.ProviderOrigin,
			provider.RepositoryID, request.RequestID,
		}, "\x00")))
		provider.SourceID = "sha256:" + hex.EncodeToString(digest[:])
		provider.Body = request.Brief
	}
	if request.Intent == IntentImplementFeature && !provider.CanCreatePullRequest {
		return Aggregate{}, errors.New("draft pull request creation is unavailable")
	}
	if err := validateProviderSnapshot(provider); err != nil {
		return Aggregate{}, fmt.Errorf("%w: provider snapshot: %v", ErrInvalid, err)
	}
	now := service.now().UTC()
	workspaceID := stableID("devw_", provider.ProviderOrigin, provider.RepositoryID,
		string(provider.SourceKind), provider.SourceID)
	result, createErr := service.store.Create(ctx, CreateInput{
		RequestID: request.RequestID,
		Workspace: Workspace{
			ID: workspaceID, Intent: provider.Intent, SourceKind: provider.SourceKind,
			SourceID: provider.SourceID, SourceNumber: provider.SourceNumber,
			Provider: provider.Provider, ProviderOrigin: provider.ProviderOrigin,
			RepositoryID: provider.RepositoryID, PullRequestID: provider.PullRequestID,
			Repository: provider.Repository, PullNumber: provider.PullNumber,
			Phase: PhaseCharter, ExecutionState: ExecutionWaitingUser,
			ProviderHeadSHA: provider.HeadSHA, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Provider: provider,
	})
	if createErr != nil {
		return Aggregate{}, createErr
	}
	return result.Aggregate, nil
}

// FailUnsafeProvider records a stable terminal outcome before any AI,
// candidate, validation, or publication side effect can begin.
func (service *Service) FailUnsafeProvider(
	ctx context.Context,
	aggregate Aggregate,
	requestID string,
) (Aggregate, error) {
	if service == nil || service.store == nil || aggregate.Workspace.Intent != IntentImplementFeature ||
		!validMutationEnvelope(aggregate.Workspace.ID, aggregate.Workspace.Version, requestID) {
		return Aggregate{}, ErrInvalid
	}
	if unsafeProviderFailureRecorded(aggregate) {
		return aggregate, nil
	}
	now := service.now().UTC()
	for _, publication := range aggregate.Publications {
		if publication.Kind != PublicationBranchPush || publication.State != ExecutionRunning {
			continue
		}
		publication.State = ExecutionFailed
		publication.PublicErrorCode = "unsafe_provider"
		publication.UpdatedAt = now
		closed, err := service.store.Mutate(ctx, Mutation{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID:                stableID("req_", requestID, "unsafe-running-publication", publication.ID),
			Patch:                    AggregatePatch{ReplacePublications: []Publication{publication}},
			branchPublicationLeaseID: publication.ID,
		})
		if err != nil {
			return closed.Aggregate, err
		}
		aggregate = closed.Aggregate
		break
	}
	state := ExecutionFailed
	failedPublications := make([]Publication, 0)
	for _, publication := range aggregate.Publications {
		if publication.Kind != PublicationBranchPush ||
			publication.State == ExecutionSucceeded || publication.State == ExecutionFailed ||
			publication.State == ExecutionCanceled || publication.State == ExecutionStale {
			continue
		}
		publication.State = ExecutionFailed
		publication.PublicErrorCode = "unsafe_provider"
		publication.UpdatedAt = now
		failedPublications = append(failedPublications, publication)
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID,
		Patch: AggregatePatch{
			ExecutionState:      &state,
			ReplacePublications: failedPublications,
			Activity: []Activity{{
				Kind: "development.failed", Actor: "system",
				Summary:   "Development provider is unsupported",
				Metadata:  map[string]any{"code": "unsafe_provider"},
				CreatedAt: now,
			}},
		},
	})
	return result.Aggregate, err
}

// FailImplementationUnavailable terminates feature work that a restarted
// Gateway can no longer execute because repair or validation capability is
// absent. This prevents a durable queued/running aggregate from polling
// forever while preserving the no-overall-timeout contract for runnable work.
func (service *Service) FailImplementationUnavailable(
	ctx context.Context,
	aggregate Aggregate,
	requestID string,
) (Aggregate, error) {
	if service == nil || service.store == nil || aggregate.Workspace.Intent != IntentImplementFeature ||
		!validMutationEnvelope(aggregate.Workspace.ID, aggregate.Workspace.Version, requestID) {
		return Aggregate{}, ErrInvalid
	}
	if developmentFailureRecorded(aggregate, "implementation_unavailable") {
		return aggregate, nil
	}
	state := ExecutionFailed
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID,
		Patch: AggregatePatch{
			ExecutionState: &state,
			Activity: []Activity{{
				Kind: "development.failed", Actor: "system",
				Summary:   "Development implementation runtime is unavailable",
				Metadata:  map[string]any{"code": "implementation_unavailable"},
				CreatedAt: service.now().UTC(),
			}},
		},
	})
	return result.Aggregate, err
}

func unsafeProviderFailureRecorded(aggregate Aggregate) bool {
	return developmentFailureRecorded(aggregate, "unsafe_provider")
}

func developmentFailureRecorded(aggregate Aggregate, code string) bool {
	for index := len(aggregate.Activity) - 1; index >= 0; index-- {
		activity := aggregate.Activity[index]
		if activity.Kind == "development.failed" && activity.Metadata["code"] == code {
			return true
		}
	}
	return false
}

func (service *Service) Get(ctx context.Context, workspaceID string) (Aggregate, error) {
	if service == nil || service.store == nil || !validOpaqueID(workspaceID, "devw_") {
		return Aggregate{}, ErrInvalid
	}
	return service.store.Get(ctx, workspaceID)
}

func validCreateWorkspaceRequest(request CreateWorkspaceRequest) bool {
	switch request.Intent {
	case IntentPickupPR:
		validPull := request.PullRequestURL != "" &&
			validateResolveRequest(ResolveRequest{PullRequestURL: request.PullRequestURL}) == nil
		return request.SourceKind == SourcePullRequest && validPull &&
			request.IssueURL == "" && request.RepositoryIdentity == "" && request.Brief == ""
	case IntentImplementFeature:
		switch request.SourceKind {
		case SourceIssue:
			parsed, err := url.ParseRequestURI(request.IssueURL)
			return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
				request.PullRequestURL == "" && request.RepositoryIdentity == "" && request.Brief == ""
		case SourceBrief:
			return request.PullRequestURL == "" && request.IssueURL == "" &&
				validBoundedText(request.RepositoryIdentity, 1024, false) &&
				validBoundedText(request.Brief, maxCharterTextBytes, false)
		}
	}
	return false
}

func (service *Service) List(ctx context.Context, filter ListFilter) (Page, error) {
	if service == nil || service.store == nil {
		return Page{}, errors.New("PR workspace service is unavailable")
	}
	if filter.Limit == 0 {
		filter.Limit = defaultWorkspaceListLimit
	}
	return service.store.List(ctx, filter)
}

func (service *Service) ListConfiguredRepositories(ctx context.Context) ([]ConfiguredRepository, error) {
	if service == nil || service.provider == nil {
		return nil, errors.New("configured repositories are unavailable")
	}
	lister, ok := service.provider.(ConfiguredRepositoryLister)
	if !ok {
		return nil, errors.New("configured repositories are unavailable")
	}
	return lister.ListConfiguredRepositories(ctx)
}

func (service *Service) VerifyConfiguredRepository(
	ctx context.Context, repositoryURL string,
) (ConfiguredRepository, error) {
	if service == nil || service.provider == nil {
		return ConfiguredRepository{}, errors.New("repository verification is unavailable")
	}
	verifier, ok := service.provider.(ConfiguredRepositoryVerifier)
	if !ok {
		return ConfiguredRepository{}, errors.New("repository verification is unavailable")
	}
	return verifier.VerifyRepository(ctx, repositoryURL)
}

type DraftCharterRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
}

type CharterDraftOutput struct {
	Type                  PRType   `json:"type"`
	Goal                  string   `json:"goal"`
	AcceptanceCriteria    []string `json:"acceptance_criteria"`
	IncludedAreas         []string `json:"included_areas"`
	ExcludedAreas         []string `json:"excluded_areas"`
	NonGoals              []string `json:"non_goals"`
	ClarificationNeeded   bool     `json:"clarification_needed"`
	ClarificationQuestion string   `json:"clarification_question"`
}

func (service *Service) DraftCharter(ctx context.Context, request DraftCharterRequest) (Aggregate, error) {
	if service == nil || service.ai.Runner == nil ||
		!validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, getErr := service.store.Get(ctx, request.WorkspaceID)
	if getErr != nil {
		return Aggregate{}, getErr
	}
	if aggregate.Workspace.Version != request.ExpectedVersion ||
		aggregate.Workspace.ProviderHeadSHA != aggregate.ProviderSnapshot.HeadSHA ||
		!charterEditingReady(aggregate) {
		return aggregate, ErrConflict
	}
	bundle := contextBundle(aggregate)
	prompt, promptErr := CompilePrompt(PromptCharterDraft, bundle, "")
	if promptErr != nil {
		return Aggregate{}, promptErr
	}
	value, runErr := service.ai.Runner.RunIsolated(ctx, IsolatedAIRequest{
		Operation: "charter.draft", SystemPrompt: prompt.SystemPrompt,
		UserPrompt: prompt.UserPrompt, Schema: charterDraftSchema(),
	})
	if runErr != nil {
		return Aggregate{}, runErr
	}
	var draft CharterDraftOutput
	if err := decodeStructured(value, &draft); err != nil || validateCharterDraft(draft) != nil {
		return Aggregate{}, errors.New("AI charter draft is invalid")
	}
	now := service.now().UTC()
	charter := Charter{
		ID:       stableID("pcr_", aggregate.Workspace.ID, request.RequestID),
		Revision: int64(len(aggregate.Charters) + 1), Type: draft.Type, Goal: draft.Goal,
		AcceptanceCriteria: draft.AcceptanceCriteria, IncludedAreas: draft.IncludedAreas,
		ExcludedAreas: draft.ExcludedAreas, NonGoals: draft.NonGoals,
		ClarificationNeeded:   draft.ClarificationNeeded,
		ClarificationQuestion: strings.TrimSpace(draft.ClarificationQuestion),
		BaseSHA:               aggregate.ProviderSnapshot.BaseSHA, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		CreatedAt: now,
	}
	phase, state := PhaseCharter, ExecutionWaitingUser
	result, mutateErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			Phase:          &phase,
			ExecutionState: &state,
			AppendCharters: []Charter{charter},
			Activity: []Activity{
				{
					Kind:      "charter.drafted",
					Actor:     "ai",
					EntityID:  charter.ID,
					Summary:   "AI drafted PR charter",
					CreatedAt: now,
				},
			},
		},
	})
	if mutateErr != nil {
		return result.Aggregate, mutateErr
	}
	return result.Aggregate, nil
}

type SaveCharterRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	Draft           CharterDraftOutput
}

func (service *Service) SaveCharter(ctx context.Context, request SaveCharterRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		validateCharterDraft(request.Draft) != nil {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	if aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	if !charterEditingReady(aggregate) {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	charter := Charter{
		ID:       stableID("pcr_", aggregate.Workspace.ID, request.RequestID),
		Revision: int64(len(aggregate.Charters) + 1), Type: request.Draft.Type,
		Goal: request.Draft.Goal, AcceptanceCriteria: request.Draft.AcceptanceCriteria,
		IncludedAreas: request.Draft.IncludedAreas, ExcludedAreas: request.Draft.ExcludedAreas,
		NonGoals: request.Draft.NonGoals, BaseSHA: aggregate.ProviderSnapshot.BaseSHA,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			AppendCharters: []Charter{charter},
			Activity: []Activity{
				{
					Kind:      "charter.edited",
					Actor:     "user",
					EntityID:  charter.ID,
					Summary:   "User edited PR charter",
					CreatedAt: now,
				},
			},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

type ReviseCharterRequest struct {
	SaveCharterRequest
	ExpectedCharterRevision int64
}

func (service *Service) ReviseCharter(ctx context.Context, request ReviseCharterRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		validateCharterDraft(request.Draft) != nil || request.ExpectedCharterRevision < 1 {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	active, ok := aggregate.ActiveCharter()
	if !ok || active.Revision != request.ExpectedCharterRevision ||
		aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	charter := Charter{
		ID:       stableID("pcr_", aggregate.Workspace.ID, request.RequestID),
		Revision: int64(len(aggregate.Charters) + 1), Type: request.Draft.Type,
		Goal: request.Draft.Goal, AcceptanceCriteria: request.Draft.AcceptanceCriteria,
		IncludedAreas: request.Draft.IncludedAreas, ExcludedAreas: request.Draft.ExcludedAreas,
		NonGoals: request.Draft.NonGoals, BaseSHA: aggregate.ProviderSnapshot.BaseSHA,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	phase, state, noActive := PhaseCharter, ExecutionWaitingUser, ""
	patch := AggregatePatch{
		Phase:           &phase,
		ExecutionState:  &state,
		ActiveCharterID: &noActive,
		AppendCharters:  []Charter{charter},
		Activity: []Activity{
			{
				Kind:      "charter.revised",
				Actor:     "user",
				EntityID:  charter.ID,
				Summary:   "PR charter revised; dependent evidence invalidated",
				CreatedAt: now,
			},
		},
	}
	for _, stage := range aggregate.StageRuns {
		if stage.CharterID == active.ID && stage.State != ExecutionCanceled && stage.State != ExecutionStale {
			stage.State, stage.PublicError = ExecutionStale, "charter_revised"
			patch.ReplaceStageRuns = append(patch.ReplaceStageRuns, stage)
		}
	}
	for _, gate := range aggregate.Gates {
		if gate.State == ExecutionQueued || gate.State == ExecutionRunning || gate.State == ExecutionWaitingGate ||
			gate.State == ExecutionWaitingUser {
			gate.State = ExecutionStale
			patch.ReplaceGates = append(patch.ReplaceGates, gate)
		}
	}
	for _, publication := range aggregate.Publications {
		if publication.State == ExecutionQueued || publication.State == ExecutionRunning ||
			publication.State == ExecutionWaitingGate {
			publication.State, publication.PublicErrorCode = ExecutionStale, "charter_revised"
			publication.UpdatedAt = now
			patch.ReplacePublications = append(patch.ReplacePublications, publication)
		}
	}
	result, err := service.store.Mutate(
		ctx,
		Mutation{
			WorkspaceID:     request.WorkspaceID,
			ExpectedVersion: request.ExpectedVersion,
			RequestID:       request.RequestID,
			Patch:           patch,
		},
	)
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

type ConfirmCharterRequest struct {
	WorkspaceID     string
	CharterID       string
	ExpectedVersion int64
	RequestID       string
}

func (service *Service) ConfirmCharterAutomatically(
	ctx context.Context,
	request ConfirmCharterRequest,
) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.CharterID, "pcr_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, _, ready := charterConfirmationReady(aggregate, request.CharterID)
	if !ready || aggregate.Workspace.Version != request.ExpectedVersion ||
		charterConfirmationWasHumanGated(aggregate) {
		return aggregate, ErrConflict
	}
	if charter.ClarificationNeeded {
		return aggregate, ErrConflict
	}
	now := service.now().UTC()
	charter.Confirmed, charter.ConfirmedAt = true, &now
	activeID := charter.ID
	phase, state := PhaseReview, ExecutionQueued
	if aggregate.Workspace.Intent == IntentImplementFeature {
		phase = PhasePlanning
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &activeID,
			ReplaceCharters: []Charter{charter},
			Activity: []Activity{{
				Kind: "charter.confirmed", Actor: "system", EntityID: charter.ID,
				Summary: "AI-drafted charter confirmed for autonomous development", CreatedAt: now,
			}},
		},
	})
	return result.Aggregate, err
}

func (service *Service) ConfirmCharter(ctx context.Context, request ConfirmCharterRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.CharterID, "pcr_") {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, decisionPoint, ready := charterConfirmationReady(aggregate, request.CharterID)
	if !ready || aggregate.Workspace.Version != request.ExpectedVersion || charterConfirmationPending(aggregate) {
		return aggregate, ErrConflict
	}
	gate, err := service.startGate(ctx, aggregate, decisionPoint, map[string]any{"charter": charter})
	if err != nil {
		return Aggregate{}, err
	}
	gate.TargetID = charter.ID
	now := service.now().UTC()
	patch := AggregatePatch{
		AppendGates: []GateRun{gate},
		Activity: []Activity{
			{
				Kind:      "gate.started",
				Actor:     "system",
				EntityID:  gate.ID,
				Summary:   "Charter confirmation gate started",
				CreatedAt: now,
			},
		},
	}
	if charter.ClarificationNeeded {
		charter.ClarificationNeeded = false
		charter.ClarificationQuestion = ""
		patch.ReplaceCharters = []Charter{charter}
		patch.Activity = append(patch.Activity, Activity{
			Kind: "charter.clarification_acknowledged", Actor: "user", EntityID: charter.ID,
			Summary: "User accepted the drafted charter for confirmation", CreatedAt: now,
		})
	}
	if gateCompletedWith(gate, "approve") {
		charter.Confirmed = true
		charter.ConfirmedAt = &now
		patch.ReplaceCharters = []Charter{charter}
		if aggregate.Workspace.Intent == IntentImplementFeature {
			phase, state, activeID := PhasePlanning, ExecutionQueued, charter.ID
			patch.Phase, patch.ExecutionState, patch.ActiveCharterID = &phase, &state, &activeID
		} else {
			queueReviewWorkflow(&patch, newReviewWorkflowHandoff(aggregate.Workspace, charter))
		}
		patch.Activity = append(
			patch.Activity,
			Activity{
				Kind:      "charter.confirmed",
				Actor:     "gate",
				EntityID:  charter.ID,
				Summary:   "PR charter confirmed",
				CreatedAt: now,
			},
		)
	} else {
		state := ExecutionWaitingGate
		if gate.State == ExecutionWaitingUser {
			state = ExecutionWaitingUser
		}
		patch.ExecutionState = &state
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func charterEditingReady(aggregate Aggregate) bool {
	if aggregate.Workspace.Phase != PhaseCharter || aggregate.Workspace.ActiveCharterID != "" {
		return false
	}
	return !charterConfirmationPending(aggregate)
}

func charterConfirmationPending(aggregate Aggregate) bool {
	for _, gate := range aggregate.Gates {
		if !charterConfirmationDecisionPoint(gate.DecisionPoint) {
			continue
		}
		switch gate.State {
		case ExecutionQueued, ExecutionRunning, ExecutionWaitingGate, ExecutionWaitingUser:
			return true
		}
	}
	return false
}

func charterConfirmationReady(aggregate Aggregate, charterID string) (Charter, string, bool) {
	if aggregate.Workspace.Phase != PhaseCharter || aggregate.Workspace.ActiveCharterID != "" ||
		len(aggregate.Charters) == 0 {
		return Charter{}, "", false
	}
	charter := aggregate.Charters[len(aggregate.Charters)-1]
	if charter.ID != charterID || charter.Confirmed ||
		charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA ||
		charter.BaseSHA != aggregate.ProviderSnapshot.BaseSHA {
		return Charter{}, "", false
	}
	decisionPoint := "pr.charter.confirm"
	for _, previous := range aggregate.Charters[:len(aggregate.Charters)-1] {
		if previous.Confirmed {
			decisionPoint = "pr.charter.reconfirm"
			break
		}
	}
	for _, gate := range aggregate.Gates {
		if !charterConfirmationDecisionPoint(gate.DecisionPoint) {
			continue
		}
		switch gate.State {
		case ExecutionQueued, ExecutionRunning, ExecutionWaitingGate, ExecutionWaitingUser:
			if gate.TargetID != charter.ID || gate.DecisionPoint != decisionPoint {
				return Charter{}, "", false
			}
		}
	}
	return charter, decisionPoint, true
}

func charterConfirmationDecisionPoint(value string) bool {
	return value == "pr.charter.confirm" || value == "pr.charter.reconfirm"
}

func charterConfirmationWasHumanGated(aggregate Aggregate) bool {
	for _, gate := range aggregate.Gates {
		if charterConfirmationDecisionPoint(gate.DecisionPoint) {
			return true
		}
	}
	return false
}

type RunReviewRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	NudgePolicy     NudgePolicy
}

func (service *Service) RunReview(ctx context.Context, request RunReviewRequest) (Aggregate, error) {
	return service.runReview(ctx, request, false)
}

func (service *Service) runReview(ctx context.Context, request RunReviewRequest, manualNudge bool) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) {
		return Aggregate{}, ErrInvalid
	}
	if request.NudgePolicy == (NudgePolicy{}) {
		request.NudgePolicy = DefaultNudgePolicy()
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	charter, ready := aggregate.ActiveCharter()
	if !ready || !charter.Confirmed || charter.HeadSHA != aggregate.ProviderSnapshot.HeadSHA ||
		aggregate.Workspace.Version != request.ExpectedVersion ||
		(!manualNudge && aggregate.Workspace.Phase != PhaseReview) ||
		(manualNudge && aggregate.Workspace.Phase != PhaseTriage) {
		return aggregate, ErrConflict
	}
	if manualNudge && !hasSuccessfulStageAtHead(aggregate.StageRuns, "review", charter.ID, charter.HeadSHA) {
		return aggregate, ErrConflict
	}
	startGate, startGateNew, err := service.ensureGate(ctx, aggregate, "pr.review.start", map[string]any{
		"charter":           charter,
		"provider_revision": aggregate.ProviderSnapshot.ProviderRevision,
		"base_sha":          aggregate.ProviderSnapshot.BaseSHA,
		"head_sha":          aggregate.ProviderSnapshot.HeadSHA,
	})
	if err != nil {
		return Aggregate{}, err
	}
	startGate.TargetID = charter.ID
	if !gateCompletedWith(startGate, "continue") {
		state := ExecutionWaitingGate
		patch := AggregatePatch{ExecutionState: &state}
		if startGateNew {
			patch.AppendGates = []GateRun{startGate}
		}
		result, mutateErr := service.store.Mutate(ctx, Mutation{
			WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
			RequestID: request.RequestID, Patch: patch,
		})
		if mutateErr != nil {
			return result.Aggregate, mutateErr
		}
		return result.Aggregate, nil
	}
	if service.reviewEvidence == nil {
		return aggregate, errors.New("immutable PR review evidence is unavailable")
	}
	evidence, err := service.reviewEvidence.LoadReviewEvidence(ctx, aggregate.ProviderSnapshot)
	if err != nil {
		return aggregate, err
	}
	if evidence.ProviderRevision != aggregate.ProviderSnapshot.ProviderRevision ||
		evidence.BaseSHA != aggregate.ProviderSnapshot.BaseSHA ||
		evidence.HeadSHA != aggregate.ProviderSnapshot.HeadSHA {
		return aggregate, ErrConflict
	}
	bundle := reviewContextBundle(aggregate)
	bundle.CandidateDiff = evidence.UnifiedDiff
	bundle.ScopePolicyPrompt = service.scopeDisposition(aggregate).Rule(charter.Type).Prompt
	mode := ReviewWorkflowFull
	if manualNudge {
		mode = ReviewWorkflowAdditional
	}
	reviewResult, runErr := service.reviewWorkflow.ExecuteReviewWorkflow(ctx, ReviewWorkflowRequest{
		Mode: mode, Handoff: newReviewWorkflowHandoff(aggregate.Workspace, charter),
		Context: reviewWorkflowContext(bundle), NudgePolicy: request.NudgePolicy,
		StrategyStats: NudgeStrategyStats(aggregate.NudgeRounds, NudgeReviewSearch),
	})
	rounds := reviewResult.Rounds
	if runErr != nil && len(rounds) == 0 {
		return Aggregate{}, runErr
	}
	if resultErr := validateReviewWorkflowResult(mode, request.NudgePolicy, reviewResult, runErr); resultErr != nil {
		return aggregate, resultErr
	}
	now := service.now().UTC()
	runID := stableID("psr_", aggregate.Workspace.ID, request.RequestID)
	stageState := ExecutionSucceeded
	phase, state := PhaseTriage, ExecutionQueued
	publicError := ""
	if runErr != nil {
		stageState, phase, state, publicError = ExecutionFailed, PhaseReview, ExecutionFailed, "review_nudge_failed"
	}
	stage := StageRun{
		ID: runID, Stage: "review", State: stageState, CharterID: charter.ID,
		HeadSHA: charter.HeadSHA, Attempt: countStageRuns(aggregate.StageRuns, "review") + 1,
		Summary: rounds[len(rounds)-1].Result.Summary, PromptDigest: rounds[0].PromptDigest,
		PublicError: publicError, StartedAt: now, FinishedAt: &now,
	}
	findings, nudges := materializeReviewRounds(
		aggregate, runID, rounds, request.NudgePolicy, service.scopeDisposition(aggregate), now,
	)
	stage.Evidence = reviewStageEvidence(runID, stage.Summary, stage.PromptDigest, rounds, findings, now)
	classificationGates, classificationWaiting, needsCharterRevision, classifyErr := service.classifyReviewFindings(
		ctx,
		aggregate,
		charter,
		findings,
	)
	if classifyErr != nil {
		return aggregate, classifyErr
	}
	patch := AggregatePatch{
		Phase:             &phase,
		ExecutionState:    &state,
		AppendStageRuns:   []StageRun{stage},
		UpsertFindings:    findings,
		AppendNudgeRounds: nudges,
		AppendGates:       classificationGates,
		Activity: []Activity{
			{
				Kind:      "review.completed",
				Actor:     "ai",
				EntityID:  runID,
				Summary:   fmt.Sprintf("Review search completed with %d rounds", len(rounds)),
				CreatedAt: now,
			},
		},
	}
	if startGateNew {
		patch.AppendGates = append(patch.AppendGates, startGate)
	}
	if runErr == nil {
		completeGate, completeGateNew, gateErr := service.ensureGate(
			ctx,
			aggregate,
			"pr.review.complete",
			map[string]any{
				"charter": charter, "stage": stage, "findings": findings, "nudge_rounds": nudges,
			},
		)
		if gateErr != nil {
			return aggregate, gateErr
		}
		completeGate.TargetID = runID
		if completeGateNew {
			patch.AppendGates = append(patch.AppendGates, completeGate)
		}
		if !gateCompletedWith(completeGate, "accept") {
			stage.State = ExecutionWaitingGate
			patch.AppendStageRuns[0] = stage
			phase, state = PhaseReview, ExecutionWaitingGate
			patch.Phase, patch.ExecutionState = &phase, &state
		}
	}
	if runErr == nil && classificationWaiting && phase == PhaseTriage {
		state = ExecutionWaitingGate
		patch.ExecutionState = &state
	}
	if runErr == nil && needsCharterRevision && phase == PhaseTriage {
		phase, state = PhaseCharter, ExecutionWaitingUser
		patch.Phase, patch.ExecutionState = &phase, &state
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID, Patch: patch,
	})
	if err != nil {
		return result.Aggregate, err
	}
	if runErr != nil {
		return result.Aggregate, runErr
	}
	return result.Aggregate, nil
}

func (service *Service) classifyReviewFindings(
	ctx context.Context,
	aggregate Aggregate,
	charter Charter,
	findings []Finding,
) ([]GateRun, bool, bool, error) {
	var gates []GateRun
	waiting, needsCharterRevision := false, false
	for index := range findings {
		finding := &findings[index]
		if finding.Disposition != FindingOpen {
			continue
		}
		gate, isNew, err := service.ensureGate(ctx, aggregate, "pr.finding.classify", map[string]any{
			"charter": charter, "finding": *finding, "scope_action": DecideScope(finding.Scope),
		})
		if err != nil {
			return nil, false, false, err
		}
		gate.TargetID = finding.ID
		if isNew {
			gates = append(gates, gate)
		}
		if gate.State != ExecutionSucceeded {
			waiting = true
			continue
		}
		switch gateAction(gate) {
		case "keep-in-pr":
			finding.Disposition = FindingInScope
		case "defer-follow-up":
			finding.Disposition = FindingDeferred
		case "dismiss":
			finding.Disposition = FindingDismissed
		case "revise-charter":
			needsCharterRevision = true
		}
	}
	return gates, waiting, needsCharterRevision, nil
}

type FindingDecisionRequest struct {
	WorkspaceID     string
	FindingID       string
	ExpectedVersion int64
	RequestID       string
	Disposition     FindingDisposition
	Scope           ScopeAssessment
	Reason          string
}

func (service *Service) DecideFinding(ctx context.Context, request FindingDecisionRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validOpaqueID(request.FindingID, "pfn_") || !validFindingDisposition(request.Disposition) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	finding, index := findFinding(aggregate.Findings, request.FindingID)
	if index < 0 || aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	if hardScopeFindingPinned(aggregate.Gates, finding.ID) {
		previousScope, previousErr := fingerprintValue(finding.Scope)
		requestedScope, requestedErr := fingerprintValue(request.Scope)
		if previousErr != nil || requestedErr != nil || previousScope != requestedScope {
			return aggregate, fmt.Errorf(
				"%w: hard candidate scope is frozen; resolve its gate or revise the charter",
				ErrInvalid,
			)
		}
	}
	if HardCandidateScopeBlocker(finding.Scope) &&
		(request.Disposition == FindingDeferred || request.Disposition == FindingDismissed || request.Disposition == FindingFixed) {
		return aggregate, fmt.Errorf(
			"%w: candidate-present hard scope must be removed or resolved by a revised charter",
			ErrInvalid,
		)
	}
	var classificationGates []GateRun
	if request.Disposition == FindingInScope {
		action := DecideScope(request.Scope)
		if action != ScopeActionProceed {
			if !scopeCanUseClassificationGate(request.Scope) || finding.Disposition != FindingOpen {
				return aggregate, fmt.Errorf(
					"%w: in-scope disposition requires charter revision or deferral (%s)",
					ErrInvalid,
					action,
				)
			}
			currentScopeDigest, currentDigestErr := fingerprintValue(finding.Scope)
			proposedScopeDigest, proposedDigestErr := fingerprintValue(request.Scope)
			if currentDigestErr != nil || proposedDigestErr != nil || currentScopeDigest != proposedScopeDigest {
				return aggregate, fmt.Errorf(
					"%w: update the finding scope before requesting classification",
					ErrInvalid,
				)
			}
			charter, ready := aggregate.ActiveCharter()
			if !ready || !charter.Confirmed {
				return aggregate, ErrConflict
			}
			gate, isNew, gateErr := service.ensureGate(ctx, aggregate, "pr.finding.classify", map[string]any{
				"charter": charter, "finding": finding, "proposed_scope": request.Scope, "scope_action": action,
			})
			if gateErr != nil {
				return aggregate, gateErr
			}
			gate.TargetID = finding.ID
			if isNew {
				classificationGates = append(classificationGates, gate)
			}
			if gate.State != ExecutionSucceeded {
				state := ExecutionWaitingGate
				result, mutateErr := service.store.Mutate(ctx, Mutation{
					WorkspaceID:     request.WorkspaceID,
					ExpectedVersion: request.ExpectedVersion,
					RequestID:       request.RequestID,
					Patch: AggregatePatch{
						ExecutionState: &state,
						AppendGates:    classificationGates,
						Activity: []Activity{{
							Kind: "finding.classification_requested", Actor: "user", EntityID: finding.ID,
							Summary: "Finding classification gate requested", CreatedAt: service.now().UTC(),
						}},
					},
				})
				if mutateErr != nil {
					return result.Aggregate, mutateErr
				}
				return result.Aggregate, nil
			}
			switch gateAction(gate) {
			case "keep-in-pr":
				// Continue below and persist the accepted in-scope correction.
			case "defer-follow-up":
				request.Disposition = FindingDeferred
			case "dismiss":
				request.Disposition = FindingDismissed
			case "revise-charter":
				phase, state := PhaseCharter, ExecutionWaitingUser
				result, mutateErr := service.store.Mutate(ctx, Mutation{
					WorkspaceID:     request.WorkspaceID,
					ExpectedVersion: request.ExpectedVersion,
					RequestID:       request.RequestID,
					Patch: AggregatePatch{
						Phase:          &phase,
						ExecutionState: &state,
						AppendGates:    classificationGates,
						Activity: []Activity{{
							Kind: "finding.needs_charter_revision", Actor: "gate", EntityID: finding.ID,
							Summary: "Finding requires a charter revision", CreatedAt: service.now().UTC(),
						}},
					},
				})
				if mutateErr != nil {
					return result.Aggregate, mutateErr
				}
				return result.Aggregate, nil
			}
		}
	}
	if request.Disposition == FindingFixed && finding.Disposition != FindingInScope {
		return aggregate, fmt.Errorf("%w: only in-scope findings can become fixed", ErrInvalid)
	}
	now := service.now().UTC()
	previous := finding
	finding.Scope = request.Scope
	finding.Disposition = request.Disposition
	if reward, ok := nudgeRewardForDisposition(request.Disposition); ok {
		finding = setFindingNudgeReward(finding, reward, "user_disposition:"+string(request.Disposition))
	}
	finding.Version++
	finding.UpdatedAt = now
	correction := Correction{
		ID: stableID("pco_", aggregate.Workspace.ID, request.RequestID), Kind: CorrectionScope,
		Applicability: CorrectionReviewAndImpl, TargetType: "finding", TargetID: finding.ID,
		OriginalClaim: fmt.Sprintf("disposition=%s scope=%s", previous.Disposition, previous.Scope.Distance),
		Correction:    fmt.Sprintf("disposition=%s scope=%s", finding.Disposition, finding.Scope.Distance),
		Evidence:      strings.TrimSpace(request.Reason), CharterID: aggregate.Workspace.ActiveCharterID,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			UpsertFindings: []Finding{
				finding,
			},
			AppendCorrections: []Correction{correction},
			AppendGates:       classificationGates,
			ReplaceNudgeRounds: recomputeNudgeRoundRewards(
				aggregate.NudgeRounds,
				upsertByID(aggregate.Findings, []Finding{finding}, func(value Finding) string { return value.ID }),
			),
			Activity: []Activity{
				{
					Kind:      "finding.decided",
					Actor:     "user",
					EntityID:  finding.ID,
					Summary:   "Finding disposition corrected",
					CreatedAt: now,
				},
			},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func scopeCanUseClassificationGate(scope ScopeAssessment) bool {
	if !scope.TypeCompatible {
		return false
	}
	if scope.Distance == ScopeNecessaryAdjacent {
		return true
	}
	return scope.Distance == ScopeExact && (scope.Size == ChangeSizeM || scope.Size == ChangeSizeL)
}

type AddCorrectionRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	Correction      Correction
}

func (service *Service) AddCorrection(ctx context.Context, request AddCorrectionRequest) (Aggregate, error) {
	if !validMutationEnvelope(request.WorkspaceID, request.ExpectedVersion, request.RequestID) ||
		!validCorrection(request.Correction) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	if aggregate.Workspace.Version != request.ExpectedVersion {
		return aggregate, ErrConflict
	}
	correction := request.Correction
	correction.ID = stableID("pco_", aggregate.Workspace.ID, request.RequestID)
	correction.CharterID = aggregate.Workspace.ActiveCharterID
	correction.HeadSHA = aggregate.ProviderSnapshot.HeadSHA
	correction.Promoted = false
	correction.CreatedAt = service.now().UTC()
	var correctedFindings []Finding
	switch correction.TargetType {
	case "workspace":
		if correction.TargetID != aggregate.Workspace.ID {
			return aggregate, ErrInvalid
		}
	case "finding":
		finding, index := findFinding(aggregate.Findings, correction.TargetID)
		if index < 0 {
			return aggregate, ErrInvalid
		}
		if finding.NudgeRoundID != "" {
			finding = setFindingNudgeReward(
				finding,
				NudgeReward(RewardRejected),
				"user_correction:"+string(correction.Kind),
			)
			finding.Version++
			finding.UpdatedAt = correction.CreatedAt
			correctedFindings = append(correctedFindings, finding)
		}
	default:
		return aggregate, ErrInvalid
	}
	findingsForReward := upsertByID(
		aggregate.Findings,
		correctedFindings,
		func(value Finding) string { return value.ID },
	)
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: request.WorkspaceID, ExpectedVersion: request.ExpectedVersion,
		RequestID: request.RequestID,
		Patch: AggregatePatch{
			AppendCorrections:  []Correction{correction},
			UpsertFindings:     correctedFindings,
			ReplaceNudgeRounds: recomputeNudgeRoundRewards(aggregate.NudgeRounds, findingsForReward),
			Activity: []Activity{
				{
					Kind:      "correction.added",
					Actor:     "user",
					EntityID:  correction.ID,
					Summary:   "User corrected AI",
					CreatedAt: correction.CreatedAt,
				},
			},
		},
	})
	if err != nil {
		return result.Aggregate, err
	}
	return result.Aggregate, nil
}

func (service *Service) startGate(
	ctx context.Context,
	aggregate Aggregate,
	point string,
	subject map[string]any,
) (GateRun, error) {
	gate, _, err := service.ensureGate(ctx, aggregate, point, subject)
	return gate, err
}

// ensureGate reuses the exact pinned gate for a subject. This makes human and
// staged gates resumable: approving a gate authorizes the next retry without
// evaluating a second policy or appending a duplicate gate run.
func (service *Service) ensureGate(
	ctx context.Context,
	aggregate Aggregate,
	point string,
	subject map[string]any,
) (GateRun, bool, error) {
	if !prlifecycle.IsDecisionPoint(point) {
		return GateRun{}, false, fmt.Errorf("%w: unknown PR lifecycle decision point %q", ErrInvalid, point)
	}
	digest, err := fingerprintGateSubject(subject)
	if err != nil {
		return GateRun{}, false, err
	}
	for index := len(aggregate.Gates) - 1; index >= 0; index-- {
		gate := aggregate.Gates[index]
		if gate.DecisionPoint == point && gate.SubjectRevision == digest {
			return gate, false, nil
		}
	}
	if service.gates != nil {
		gate, startErr := service.gates.Start(ctx, GateRequest{
			WorkspaceID: aggregate.Workspace.ID, WorkspaceVersion: aggregate.Workspace.Version,
			ProviderOrigin: aggregate.Workspace.ProviderOrigin,
			RepositoryID:   aggregate.Workspace.RepositoryID, DecisionPoint: point,
			Subject: subject, SubjectDigest: digest, WorkingContext: contextBundle(aggregate),
		})
		return gate, true, startErr
	}
	// A service without an executor may still apply the published workflow's
	// literal Human or deterministic default. It never substitutes Human for a
	// different action mode; AI/workflow defaults fail closed.
	entry, catalogErr := prLifecycleGateCatalogEntry(point)
	if catalogErr != nil {
		return GateRun{}, false, catalogErr
	}
	now := service.now().UTC()
	gate := GateRun{
		ID:            stableID("pgr_", aggregate.Workspace.ID, point, digest),
		DecisionPoint: point, SubjectRevision: digest,
		PolicyRevision: "builtin:workflow-default-v3", Evidence: projectGateEvidence(subject),
		CreatedAt: now,
	}
	if entry.Gate.DefaultAction == nil {
		return GateRun{}, false, fmt.Errorf("gate %q has no default-action", entry.GateRef)
	}
	switch entry.Gate.DefaultAction.Type {
	case gatetypes.GateActionHuman:
		gate.State = ExecutionWaitingUser
		gate.Turns = []GateTurn{{
			StageID: "fallback-human", Kind: "human", ActorKind: "human",
			Title: entry.Gate.Prompt, Status: "waiting",
			GateForm: &GateForm{GateRef: entry.GateRef, Prompt: entry.Gate.Prompt, Fields: entry.Gate.Fields},
		}}
	case gatetypes.GateActionDeterministic:
		values, valueErr := workflows.ValidateGateFieldValues(
			entry.Gate.Fields,
			entry.Gate.DefaultAction.Fields,
		)
		if valueErr != nil {
			return GateRun{}, false, fmt.Errorf("gate %q deterministic default: %w", entry.GateRef, valueErr)
		}
		gate.State, gate.FinishedAt = ExecutionSucceeded, &now
		gate.Turns = []GateTurn{{
			StageID: "fallback-deterministic", Kind: "deterministic", ActorKind: "deterministic",
			Title: entry.Gate.Prompt, Status: "answered", FieldValues: values,
		}}
	default:
		return GateRun{}, false, fmt.Errorf(
			"gate %q default action %q requires the workflow executor",
			entry.GateRef,
			entry.Gate.DefaultAction.Type,
		)
	}
	return gate, true, nil
}

func materializeReviewRounds(
	aggregate Aggregate,
	runID string,
	rounds []ReviewRound,
	policy NudgePolicy,
	scopePolicy ScopeDispositionPolicy,
	now time.Time,
) ([]Finding, []NudgeRoundRecord) {
	charter, hasCharter := aggregate.ActiveCharter()
	scopeRule := scopePolicy.Rule(charter.Type)
	scopePolicyRevision, scopePromptDigest := scopeDispositionEvidence(scopeRule, charter.Type)
	current := currentContextFindings(aggregate, charter, hasCharter)
	known := newSemanticFindingSet()
	for _, finding := range current {
		known.addStored(finding)
	}
	var findings []Finding
	var nudges []NudgeRoundRecord
	for roundIndex, round := range rounds {
		roundID := ""
		if !round.Initial {
			roundID = stableID("pnr_", aggregate.Workspace.ID, runID, fmt.Sprint(round.Round))
		}
		var roundFindingIDs []string
		for findingIndex, candidate := range round.Result.Findings {
			fingerprint := agentFindingFingerprint(candidate)
			id := stableID(
				"pfn_",
				aggregate.Workspace.ID,
				runID,
				fmt.Sprint(roundIndex),
				fmt.Sprint(findingIndex),
				fingerprint,
			)
			if _, exists := known.add(candidate, fingerprint, id); exists {
				continue
			}
			roundFindingIDs = append(roundFindingIDs, id)
			origin := FindingOriginReview
			if roundID != "" {
				origin = FindingOriginNudge
			}
			findings = append(findings, Finding{
				ID:           id,
				Fingerprint:  fingerprint,
				Origin:       origin,
				OriginRunID:  runID,
				NudgeRoundID: roundID,
				Severity:     candidate.Severity,
				Title:        candidate.Title,
				File:         candidate.File,
				Line:         candidate.Line,
				Message:      candidate.Message,
				Evidence:     candidate.Evidence,
				Impact:       candidate.Impact,
				Validation:   candidate.Validation,
				Scope: agentFindingScope(
					candidate,
				),
				Disposition: decideFindingDisposition(
					agentFindingScope(candidate), candidate, charter, scopePolicy,
				),
				ScopePolicyMode: scopeRule.Mode, ScopePolicyRevision: scopePolicyRevision,
				ScopePolicyPromptDigest: scopePromptDigest,
				SourceAvailable:         round.Source != nil,
				source:                  cloneAIExecutionSource(round.Source),
				Version:                 1,
				CreatedAt:               now,
				UpdatedAt:               now,
			})
		}
		if round.Initial {
			continue
		}
		nudges = append(nudges, NudgeRoundRecord{
			ID:         roundID,
			StageRunID: runID, Stage: NudgeReviewSearch, Round: round.Round,
			MinimumRounds: policy.MinimumAdditionalRounds, HardCap: policy.MaximumAdditionalRounds,
			Strategy: round.Strategy, Challenge: round.Challenge,
			VariantDigest: round.VariantDigest, PromptDigest: round.PromptDigest,
			State: round.State, PublicError: round.PublicError,
			NovelFindings: round.NovelFindings, DuplicateCount: round.DuplicateCount,
			FindingIDs: roundFindingIDs,
			CreatedAt:  now,
		})
	}
	return findings, nudges
}

func agentFindingScope(candidate AgentFinding) ScopeAssessment {
	files, modules := 0, 0
	if candidate.File != "" {
		files, modules = 1, 1
	}
	return ScopeAssessment{
		Distance: candidate.ScopeDistance, Size: candidate.ChangeSize,
		Presence: WorkCandidatePresent, Files: files, Modules: modules, Estimated: true,
		TypeCompatible: candidate.TypeCompatible, Confidence: candidate.ScopeConfidence,
		CharterClauses: append([]string(nil), candidate.CharterClauses...),
		Explanation:    candidate.ScopeExplanation,
	}
}

func completionFindingScope(candidate CompletionFinding) ScopeAssessment {
	scope := agentFindingScope(candidate.AgentFinding)
	scope.Presence = candidate.Presence
	if candidate.Presence == WorkCandidatePresent {
		scope.SemanticLines = candidate.SemanticLines
		scope.Estimated = false
		scope.ChangeEvidence = []ScopeChange{{
			Path: candidate.File, Hunk: candidate.Hunk, Module: candidate.Module,
			SemanticLines: candidate.SemanticLines, Presence: candidate.Presence,
			Distance: candidate.ScopeDistance, Size: candidate.ChangeSize,
			TypeCompatible: candidate.TypeCompatible, Confidence: candidate.ScopeConfidence,
			CharterClauses: append([]string(nil), candidate.CharterClauses...),
			Explanation:    candidate.ScopeExplanation,
		}}
	}
	return scope
}

// Review findings with an unambiguous charter fit are immediately actionable;
// clearly external or PR-type-incompatible work is immediately deferred. S1,
// large S0, and otherwise policy-ambiguous classifications stay open for
// explicit triage.
func reviewFindingDisposition(scope ScopeAssessment) FindingDisposition {
	if !scope.TypeCompatible || scope.Distance == ScopeRelatedFollowup || scope.Distance == ScopeUnrelated {
		return FindingDeferred
	}
	if DecideScope(scope) == ScopeActionProceed {
		return FindingInScope
	}
	return FindingOpen
}

func hasSuccessfulStageAtHead(values []StageRun, stage, charterID, headSHA string) bool {
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if value.Stage == stage && value.CharterID == charterID && value.HeadSHA == headSHA &&
			value.State == ExecutionSucceeded {
			return true
		}
	}
	return false
}

func validateResolveRequest(request ResolveRequest) error {
	if request.PullRequestURL != "" {
		parsed, err := url.ParseRequestURI(request.PullRequestURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || request.ProviderOrigin != "" ||
			request.Repository != "" ||
			request.PullNumber != 0 {
			return ErrInvalid
		}
		return nil
	}
	if request.ProviderOrigin == "" || request.Repository == "" || request.PullNumber < 1 {
		return ErrInvalid
	}
	return nil
}

func validateProviderSnapshot(snapshot ProviderSnapshot) error {
	if snapshot.Provider != "github" || snapshot.ProviderOrigin == "" || snapshot.RepositoryID == "" ||
		snapshot.Repository == "" || snapshot.SourceID == "" ||
		snapshot.HeadRepositoryID == "" || snapshot.HeadRepository == "" ||
		snapshot.HeadSHA == "" || snapshot.BaseSHA == "" || snapshot.ObservedAt.IsZero() {
		return errors.New("required provider identity is missing")
	}
	if snapshot.Intent == IntentPickupPR {
		if snapshot.SourceKind != SourcePullRequest || snapshot.PullRequestID == "" || snapshot.PullNumber < 1 {
			return errors.New("pickup source must be one pull request")
		}
		return nil
	}
	if snapshot.Intent != IntentImplementFeature ||
		(snapshot.SourceKind != SourceIssue && snapshot.SourceKind != SourceBrief) {
		return errors.New("feature source must be one issue or brief")
	}
	return nil
}

func validateCharterDraft(draft CharterDraftOutput) error {
	if !validPRType(draft.Type) || !validBoundedText(draft.Goal, maxCharterTextBytes, false) ||
		len(draft.AcceptanceCriteria) == 0 {
		return ErrInvalid
	}
	if draft.ClarificationNeeded != (strings.TrimSpace(draft.ClarificationQuestion) != "") ||
		!validBoundedText(draft.ClarificationQuestion, 8<<10, !draft.ClarificationNeeded) {
		return ErrInvalid
	}
	for _, values := range [][]string{draft.AcceptanceCriteria, draft.IncludedAreas, draft.ExcludedAreas, draft.NonGoals} {
		if len(values) > maxCharterItems {
			return ErrInvalid
		}
		for _, value := range values {
			if !validBoundedText(value, maxCharterTextBytes, false) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func validPRType(value PRType) bool {
	switch value {
	case PRTypeFix, PRTypeRefactor, PRTypeFeature, PRTypeDocumentation, PRTypeTest:
		return true
	default:
		return false
	}
}

func validMutationEnvelope(workspaceID string, version int64, requestID string) bool {
	return validOpaqueID(workspaceID, "devw_") && version > 0 && validRequestID(requestID)
}

func validOpaqueID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	suffix := strings.TrimPrefix(value, prefix)
	if suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func stableID(prefix string, values ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pr-workspace-id-v1\x00" + prefix + "\x00"))
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(digest.Sum(nil)[:16])
}

func findFinding(values []Finding, id string) (Finding, int) {
	for index := range values {
		if values[index].ID == id {
			return values[index], index
		}
	}
	return Finding{}, -1
}

func countStageRuns(values []StageRun, stage string) int {
	count := 0
	for _, value := range values {
		if value.Stage == stage {
			count++
		}
	}
	return count
}

func validFindingDisposition(value FindingDisposition) bool {
	switch value {
	case FindingOpen, FindingInScope, FindingFixed, FindingDeferred, FindingDismissed:
		return true
	default:
		return false
	}
}

func validCorrection(value Correction) bool {
	switch value.Kind {
	case CorrectionFactual, CorrectionFindingQuality, CorrectionScope, CorrectionPRType,
		CorrectionImplementation, CorrectionValidation, CorrectionRepositoryPreference:
	default:
		return false
	}
	switch value.Applicability {
	case CorrectionReviewOnly, CorrectionImplementationOnly, CorrectionReviewAndImpl:
	default:
		return false
	}
	return validBoundedText(value.TargetType, 128, false) &&
		validBoundedText(value.TargetID, 256, false) &&
		validBoundedText(value.OriginalClaim, maxCorrectionText, false) &&
		validBoundedText(value.Correction, maxCorrectionText, false) &&
		validBoundedText(value.Evidence, maxCorrectionText, true)
}

func charterDraftSchema() map[string]any {
	stringsArray := map[string]any{
		"type":     "array",
		"maxItems": maxCharterItems,
		"items":    map[string]any{"type": "string"},
	}
	requiredStringsArray := map[string]any{
		"type": "array", "minItems": 1, "maxItems": maxCharterItems,
		"items": map[string]any{"type": "string"},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []any{
			"type",
			"goal",
			"acceptance_criteria",
			"included_areas",
			"excluded_areas",
			"non_goals",
			"clarification_needed",
			"clarification_question",
		},
		"properties": map[string]any{
			"type": map[string]any{
				"type": "string",
				"enum": []any{
					string(PRTypeFix),
					string(PRTypeRefactor),
					string(PRTypeFeature),
					string(PRTypeDocumentation),
					string(PRTypeTest),
				},
			},
			"goal":                   map[string]any{"type": "string"},
			"acceptance_criteria":    requiredStringsArray,
			"included_areas":         stringsArray,
			"excluded_areas":         stringsArray,
			"non_goals":              stringsArray,
			"clarification_needed":   map[string]any{"type": "boolean"},
			"clarification_question": map[string]any{"type": "string"},
		},
	}
}
