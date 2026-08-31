package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewPublicationPreflightRejectsBeforeProviderInitialization(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})
	store, state, editable := repositoryReviewPublicationTestDraft(
		t, cfg.WorkspacePath(), "owner/repo",
	)
	state, err := store.DeleteIssueDraft(state.Repository, editable.ID, editable.Version)
	if err != nil {
		t.Fatal(err)
	}
	state, blocked, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: state.Findings[0].ID,
		GenerationID:         "rrig_gateway_preflight",
		ResolvedInstructions: "Present the diagnosis.",
		InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:       "writer", GeneratorAccount: "account",
	})
	if err != nil || !reserved || blocked.State != repoaudit.IssueDraftGenerating {
		t.Fatalf("reserve state=%#v draft=%#v reserved=%v err=%v", state, blocked, reserved, err)
	}
	eligibility := repoaudit.EvaluateIssuePublication(state, blocked)
	if eligibility.CanPublish || len(eligibility.PublishBlockers) != 1 ||
		eligibility.PublishBlockers[0].Code != repoaudit.IssuePublicationStateNotPublishable {
		t.Fatalf("blocked eligibility=%#v", eligibility)
	}

	runnerCalls, providerCalls := 0, 0
	handler := newRepositoryReviewPublicationHandler(loop)
	handler.newToolRunner = func(*agent.AgentLoop, string) (workflows.ToolRunner, error) {
		runnerCalls++
		return nil, errors.New("tool runner must not initialize")
	}
	handler.newGitHubProvider = func(
		workflows.ToolRunner,
		string,
	) (*reviews.GitHubProvider, error) {
		providerCalls++
		return nil, errors.New("provider must not initialize")
	}
	request := httptest.NewRequest(
		http.MethodPost,
		repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+blocked.ID+"/publish",
		strings.NewReader(`{"expected_version":`+strconv.FormatInt(blocked.Version, 10)+`}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || runnerCalls != 0 || providerCalls != 0 ||
		!strings.Contains(
			response.Body.String(),
			`"code":"`+string(repoaudit.IssuePublicationStateNotPublishable)+`"`,
		) || !strings.Contains(response.Body.String(), `"publish_blockers"`) {
		t.Fatalf(
			"response=%d runner_calls=%d provider_calls=%d body=%s",
			response.Code,
			runnerCalls,
			providerCalls,
			response.Body.String(),
		)
	}
	persisted, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("reload found=%v err=%v", found, err)
	}
	persistedDraft, found := repositoryReviewGatewayDraftByID(persisted, blocked.ID)
	if !found || persistedDraft.State != repoaudit.IssueDraftGenerating ||
		persistedDraft.Version != blocked.Version {
		t.Fatalf("preflight mutated draft=%#v found=%v", persistedDraft, found)
	}
}

func TestRepositoryReviewPublicationEligibilityErrorBoundaries(t *testing.T) {
	empty := httptest.NewRecorder()
	writeRepositoryReviewPublicationEligibilityError(
		empty,
		repoaudit.IssuePublicationEligibility{},
	)
	if empty.Code != http.StatusConflict ||
		!strings.Contains(empty.Body.String(), `"code":"publication_not_allowed"`) {
		t.Fatalf("empty eligibility response=%d %s", empty.Code, empty.Body.String())
	}

	nonGitHub := httptest.NewRecorder()
	writeRepositoryReviewPublicationEligibilityError(
		nonGitHub,
		repoaudit.IssuePublicationEligibility{PublishBlockers: []repoaudit.IssuePublicationBlocker{{
			Code: repoaudit.IssuePublicationRepositoryNotGitHub, Count: 1,
			Message: "This repository is not a canonical GitHub repository.",
		}}},
	)
	if nonGitHub.Code != http.StatusBadRequest ||
		!strings.Contains(nonGitHub.Body.String(), `"code":"repository_not_github"`) {
		t.Fatalf("non-GitHub response=%d %s", nonGitHub.Code, nonGitHub.Body.String())
	}
}

func repositoryReviewGatewayDraftByID(
	state repoaudit.RepositoryState,
	id string,
) (repoaudit.IssueDraft, bool) {
	for _, draft := range state.IssueDrafts {
		if draft.ID == id {
			return draft, true
		}
	}
	return repoaudit.IssueDraft{}, false
}
