package prworkspace

import (
	"context"
	"errors"
	"time"
)

// ReviewWorkflowMode selects either a complete review invocation or one
// additional challenge of already-persisted review evidence. Both modes use
// the same implementation-neutral review contract.
type ReviewWorkflowMode string

const (
	ReviewWorkflowFull       ReviewWorkflowMode = "full"
	ReviewWorkflowAdditional ReviewWorkflowMode = "additional"
)

// ReviewWorkflowContext is the complete input contract of the reusable review
// workflow. It deliberately has no implementation candidate metrics,
// validation result, repair attempt, publication fence, or implementation
// authorization. A caller may review a different immutable candidate, but it
// cannot smuggle implementation-specific authority into the review prompt.
type ReviewWorkflowContext struct {
	WorkspaceID       string                 `json:"workspace-id"`
	Provider          ReviewProviderContext  `json:"provider"`
	Charter           Charter                `json:"charter"`
	Messages          []Message              `json:"messages,omitempty"`
	Findings          []Finding              `json:"findings,omitempty"`
	Corrections       []Correction           `json:"corrections,omitempty"`
	RepositoryLessons []RepositoryLesson     `json:"repository-lessons,omitempty"`
	NudgeLearning     []NudgeLearningExample `json:"nudge-learning,omitempty"`
	PriorEvidence     []StageEvidence        `json:"prior-evidence,omitempty"`
	Deferred          []DeferredGroup        `json:"deferred,omitempty"`
	CandidateDiff     string                 `json:"candidate-diff,omitempty"`
}

// ReviewProviderContext contains only immutable pull-request evidence needed
// by review. Write capability, ownership, authenticated-user, issue creation,
// and implementation publication fields deliberately do not cross the reusable
// review boundary.
type ReviewProviderContext struct {
	Provider         string    `json:"provider"`
	ProviderOrigin   string    `json:"provider-origin"`
	RepositoryID     string    `json:"repository-id"`
	Repository       string    `json:"repository"`
	PullRequestID    string    `json:"pull-request-id"`
	PullNumber       int64     `json:"pull-number"`
	Title            string    `json:"title"`
	Body             string    `json:"body,omitempty"`
	AuthorID         string    `json:"author-id"`
	AuthorLogin      string    `json:"author-login"`
	BaseRef          string    `json:"base-ref"`
	BaseSHA          string    `json:"base-sha"`
	HeadRepositoryID string    `json:"head-repository-id"`
	HeadRepository   string    `json:"head-repository"`
	HeadRef          string    `json:"head-ref"`
	HeadSHA          string    `json:"head-sha"`
	State            string    `json:"state"`
	ProviderRevision string    `json:"provider-revision,omitempty"`
	ObservedAt       time.Time `json:"observed-at"`
}

// ReviewWorkflowRequest is safe for the ordinary PR review lifecycle and for
// implementation re-entry to call with its own immutable candidate.
type ReviewWorkflowRequest struct {
	Mode          ReviewWorkflowMode    `json:"mode"`
	Handoff       ReviewWorkflowHandoff `json:"handoff"`
	Context       ReviewWorkflowContext `json:"context"`
	NudgePolicy   NudgePolicy           `json:"nudge-policy"`
	StrategyStats []NudgeStrategyStat   `json:"strategy-stats,omitempty"`
}

// ReviewWorkflowResult is the typed, implementation-neutral output boundary.
// Lifecycle persistence and disposition routing remain the caller's job.
type ReviewWorkflowResult struct {
	Rounds []ReviewRound `json:"rounds"`
}

// ReviewWorkflowHandoff is the typed state-machine boundary reconstructed from
// the durable workspace and confirmed charter when review starts. The
// implementation flow uses this same handoff after a scope revision is
// reconfirmed; no repair, candidate, or implementation authorization crosses
// it.
type ReviewWorkflowHandoff struct {
	WorkspaceID string `json:"workspace-id"`
	CharterID   string `json:"charter-id"`
	HeadSHA     string `json:"head-sha"`
}

// ReviewWorkflowExecutor executes the application-owned reusable review
// boundary. It is intentionally not a generic YAML reusable call: generic
// reusable children cannot yet suspend on PR Gates. This executor owns no PR
// phase transitions, implementation selection, repair, or publication logic.
type ReviewWorkflowExecutor interface {
	ExecuteReviewWorkflow(ctx context.Context, request ReviewWorkflowRequest) (ReviewWorkflowResult, error)
}

type isolatedReviewWorkflow struct {
	ai AIController
}

func newIsolatedReviewWorkflow(runner IsolatedAIRunner) ReviewWorkflowExecutor {
	return isolatedReviewWorkflow{ai: AIController{Runner: runner}}
}

func (workflow isolatedReviewWorkflow) ExecuteReviewWorkflow(
	ctx context.Context,
	request ReviewWorkflowRequest,
) (ReviewWorkflowResult, error) {
	if request.Handoff.WorkspaceID == "" || request.Handoff.CharterID == "" ||
		request.Handoff.HeadSHA == "" || request.Context.WorkspaceID != request.Handoff.WorkspaceID ||
		request.Context.Charter.ID != request.Handoff.CharterID ||
		request.Context.Charter.HeadSHA != request.Handoff.HeadSHA ||
		request.Context.Provider.HeadSHA != request.Handoff.HeadSHA {
		return ReviewWorkflowResult{}, errors.New("review workflow handoff does not match its immutable context")
	}
	if err := validateReviewWorkflowAudience(request.Context); err != nil {
		return ReviewWorkflowResult{}, err
	}
	bundle := request.Context.promptBundle()
	switch request.Mode {
	case ReviewWorkflowFull:
		policy := request.NudgePolicy
		if policy == (NudgePolicy{}) {
			policy = DefaultNudgePolicy()
		}
		rounds, err := workflow.ai.RunReviewSearch(ctx, bundle, policy, request.StrategyStats)
		return ReviewWorkflowResult{Rounds: rounds}, err
	case ReviewWorkflowAdditional:
		round, err := workflow.ai.RunReviewNudge(ctx, bundle, request.StrategyStats)
		return ReviewWorkflowResult{Rounds: []ReviewRound{round}}, err
	default:
		return ReviewWorkflowResult{}, errors.New("unsupported review workflow mode")
	}
}

func newReviewWorkflowHandoff(workspace Workspace, charter Charter) ReviewWorkflowHandoff {
	return ReviewWorkflowHandoff{
		WorkspaceID: workspace.ID,
		CharterID:   charter.ID,
		HeadSHA:     charter.HeadSHA,
	}
}

func queueReviewWorkflow(patch *AggregatePatch, handoff ReviewWorkflowHandoff) {
	phase, state, activeID := PhaseReview, ExecutionQueued, handoff.CharterID
	patch.Phase, patch.ExecutionState, patch.ActiveCharterID = &phase, &state, &activeID
}

func validateReviewWorkflowResult(
	mode ReviewWorkflowMode,
	policy NudgePolicy,
	result ReviewWorkflowResult,
	runErr error,
) error {
	if len(result.Rounds) == 0 {
		return errors.New("review workflow returned no rounds")
	}
	limit := 1
	if mode == ReviewWorkflowFull {
		if policy == (NudgePolicy{}) {
			policy = DefaultNudgePolicy()
		}
		if err := policy.Validate(); err != nil {
			return err
		}
		limit += policy.MaximumAdditionalRounds
	}
	if len(result.Rounds) > limit {
		return errors.New("review workflow returned too many rounds")
	}
	for index, round := range result.Rounds {
		switch round.State {
		case ExecutionSucceeded:
			if err := validateReviewPass(round.Result); err != nil {
				return err
			}
		case ExecutionFailed:
			if runErr == nil || index != len(result.Rounds)-1 {
				return errors.New("review workflow returned an unbound failed round")
			}
		default:
			return errors.New("review workflow returned a nonterminal round")
		}
	}
	if runErr != nil && result.Rounds[len(result.Rounds)-1].State != ExecutionFailed {
		return errors.New("review workflow error is not bound to a failed round")
	}
	return nil
}

func reviewWorkflowContext(bundle PRContextBundle) ReviewWorkflowContext {
	priorEvidence := make([]StageEvidence, 0, len(bundle.PriorEvidence))
	for _, evidence := range bundle.PriorEvidence {
		if evidence.Stage == "review" {
			evidence.Validation = nil
			priorEvidence = append(priorEvidence, evidence)
		}
	}
	nudgeLearning := make([]NudgeLearningExample, 0, len(bundle.NudgeLearning))
	for _, example := range bundle.NudgeLearning {
		if example.Stage == NudgeReviewSearch {
			nudgeLearning = append(nudgeLearning, example)
		}
	}
	return ReviewWorkflowContext{
		WorkspaceID: bundle.WorkspaceID, Provider: reviewProviderContext(bundle.Provider), Charter: bundle.Charter,
		Messages: filterMessagesFor(bundle.Messages, CorrectionReviewOnly), Findings: bundle.Findings,
		Corrections:       filterCorrectionsFor(bundle.Corrections, CorrectionReviewOnly),
		RepositoryLessons: filterLessonsFor(bundle.RepositoryLessons, CorrectionReviewOnly),
		NudgeLearning:     nudgeLearning, PriorEvidence: priorEvidence, Deferred: bundle.Deferred,
		CandidateDiff: bundle.CandidateDiff,
	}
}

func validateReviewWorkflowAudience(review ReviewWorkflowContext) error {
	if len(filterMessagesFor(review.Messages, CorrectionReviewOnly)) != len(review.Messages) ||
		len(filterCorrectionsFor(review.Corrections, CorrectionReviewOnly)) != len(review.Corrections) ||
		len(filterLessonsFor(review.RepositoryLessons, CorrectionReviewOnly)) != len(review.RepositoryLessons) {
		return errors.New("review workflow context contains implementation-only guidance")
	}
	for _, example := range review.NudgeLearning {
		if example.Stage != NudgeReviewSearch {
			return errors.New("review workflow context contains non-review nudge learning")
		}
	}
	for _, evidence := range review.PriorEvidence {
		if evidence.Stage != "review" || len(evidence.Validation) != 0 {
			return errors.New("review workflow context contains non-review stage evidence")
		}
	}
	return nil
}

func (review ReviewWorkflowContext) promptBundle() PRContextBundle {
	return PRContextBundle{
		WorkspaceID: review.WorkspaceID, Provider: review.Provider.providerSnapshot(), Charter: review.Charter,
		Messages: review.Messages, Findings: review.Findings, Corrections: review.Corrections,
		RepositoryLessons: review.RepositoryLessons, NudgeLearning: review.NudgeLearning,
		PriorEvidence: review.PriorEvidence, Deferred: review.Deferred,
		CandidateDiff: review.CandidateDiff,
	}
}

func reviewProviderContext(provider ProviderSnapshot) ReviewProviderContext {
	return ReviewProviderContext{
		Provider: provider.Provider, ProviderOrigin: provider.ProviderOrigin,
		RepositoryID: provider.RepositoryID, Repository: provider.Repository,
		PullRequestID: provider.PullRequestID, PullNumber: provider.PullNumber,
		Title: provider.Title, Body: provider.Body, AuthorID: provider.AuthorID,
		AuthorLogin: provider.AuthorLogin, BaseRef: provider.BaseRef, BaseSHA: provider.BaseSHA,
		HeadRepositoryID: provider.HeadRepositoryID, HeadRepository: provider.HeadRepository,
		HeadRef: provider.HeadRef, HeadSHA: provider.HeadSHA, State: provider.State,
		ProviderRevision: provider.ProviderRevision, ObservedAt: provider.ObservedAt,
	}
}

func (provider ReviewProviderContext) providerSnapshot() ProviderSnapshot {
	return ProviderSnapshot{
		Provider: provider.Provider, ProviderOrigin: provider.ProviderOrigin,
		RepositoryID: provider.RepositoryID, Repository: provider.Repository,
		PullRequestID: provider.PullRequestID, PullNumber: provider.PullNumber,
		Title: provider.Title, Body: provider.Body, AuthorID: provider.AuthorID,
		AuthorLogin: provider.AuthorLogin, BaseRef: provider.BaseRef, BaseSHA: provider.BaseSHA,
		HeadRepositoryID: provider.HeadRepositoryID, HeadRepository: provider.HeadRepository,
		HeadRef: provider.HeadRef, HeadSHA: provider.HeadSHA, State: provider.State,
		ProviderRevision: provider.ProviderRevision, ObservedAt: provider.ObservedAt,
	}
}
