package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestDevelopmentPullCompositionAndAuthorityValidation(t *testing.T) {
	provider := prworkspace.ProviderSnapshot{
		SourceKind: prworkspace.SourceBrief, SourceID: "brief-1", ProviderOrigin: "https://github.com",
		RepositoryID: "42", Repository: "octo/repo", Title: "Feature brief",
		Body: "Add a focused notification inbox\nMore detail.", BaseRef: "main", BaseSHA: "base-sha",
	}
	repair := prworkspace.RepairAttempt{
		ResultSummary: "Added a sortable notification inbox.", ChangedFiles: []string{"web/inbox.tsx"},
	}
	validation := prworkspace.ValidationRun{
		State: prworkspace.ExecutionSucceeded,
		Checks: []prworkspace.ValidationCheck{{
			Name: "frontend tests", Status: "passed", Summary: "All notification tests passed",
		}},
	}
	marker := "<!-- picoclaw-development:brief-1:candidate-sha -->"
	title := developmentPullTitle(provider)
	require.Equal(t, "Add a focused notification inbox", title)
	body := developmentPullBody(provider, repair, validation, marker)
	require.Contains(t, body, "## Source")
	require.Contains(t, body, "## Changes")
	require.Contains(t, body, "## Validation")
	require.Contains(t, body, marker)

	repository := &prWorkspaceGitHubRepo{
		ID: json.RawMessage(`42`), FullName: "octo/repo", HTMLURL: "https://github.com/octo/repo",
	}
	pull := prWorkspaceGitHubPull{
		ID: json.RawMessage(`99`), Number: 7, Title: title, Body: body, State: "open", Draft: true,
		HTMLURL: "https://github.com/octo/repo/pull/7",
		Base:    &prWorkspaceGitHubBranch{Ref: "main", SHA: "base-sha", Repo: repository},
		Head:    &prWorkspaceGitHubBranch{Ref: "picoclaw/feature-abc", SHA: "candidate-sha", Repo: repository},
	}
	require.NoError(t, validateDevelopmentPull(
		pull, provider, "picoclaw/feature-abc", "candidate-sha", marker, title,
	))

	unsafe := pull
	unsafe.Draft = false
	require.Error(t, validateDevelopmentPull(
		unsafe, provider, "picoclaw/feature-abc", "candidate-sha", marker, title,
	))
	unsafe = pull
	unsafe.Head = &prWorkspaceGitHubBranch{Ref: "picoclaw/feature-abc", SHA: "other", Repo: repository}
	require.Error(t, validateDevelopmentPull(
		unsafe, provider, "picoclaw/feature-abc", "candidate-sha", marker, title,
	))
}

func TestFindOrCreateDevelopmentPullRequestLifecycle(t *testing.T) {
	provider, request, branch, candidateSHA, marker, pull := developmentPublicationFixture()

	t.Run("reuses exact existing draft", func(t *testing.T) {
		var calls []workflows.ToolRequest
		githubProvider := newDevelopmentPublicationProvider(
			t,
			func(request workflows.ToolRequest) (map[string]any, error) {
				calls = append(calls, request)
				require.Equal(t, reviews.GitHubListPullRequestsTool, request.MCPTool)
				return map[string]any{"text": developmentPublicationJSON(t, []prWorkspaceGitHubPull{pull})}, nil
			},
		)
		runtime := &prWorkspaceImplementationRuntime{provider: githubProvider}
		result, found, err := runtime.findOrCreateDevelopmentPullRequest(
			t.Context(), request, branch, candidateSHA, false,
		)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "99", result.ExternalID)
		require.Equal(t, int64(7), result.PullNumber)
		require.Equal(t, branch, result.HeadRef)
		require.Equal(t, candidateSHA, result.HeadSHA)
		require.Len(t, calls, 1)
		require.Equal(t, "octo:"+branch, calls[0].Args["head"])
		require.Equal(t, provider.BaseRef, calls[0].Args["base"])
	})

	t.Run("reconciliation reports absent without creating", func(t *testing.T) {
		githubProvider := newDevelopmentPublicationProvider(
			t,
			func(request workflows.ToolRequest) (map[string]any, error) {
				require.Equal(t, reviews.GitHubListPullRequestsTool, request.MCPTool)
				return map[string]any{"text": `{"items":[]}`}, nil
			},
		)
		runtime := &prWorkspaceImplementationRuntime{provider: githubProvider}
		result, found, err := runtime.findOrCreateDevelopmentPullRequest(
			t.Context(), request, branch, candidateSHA, false,
		)
		require.NoError(t, err)
		require.False(t, found)
		require.Equal(t, prworkspace.BranchPublicationResult{}, result)
	})

	t.Run("creates one bounded draft", func(t *testing.T) {
		var calls []workflows.ToolRequest
		githubProvider := newDevelopmentPublicationProvider(
			t,
			func(toolRequest workflows.ToolRequest) (map[string]any, error) {
				calls = append(calls, toolRequest)
				switch toolRequest.MCPTool {
				case reviews.GitHubListPullRequestsTool:
					return map[string]any{"text": `[]`}, nil
				case reviews.GitHubCreatePullRequestTool:
					require.Equal(t, "octo", toolRequest.Args["owner"])
					require.Equal(t, "repo", toolRequest.Args["repo"])
					require.Equal(t, branch, toolRequest.Args["head"])
					require.Equal(t, "main", toolRequest.Args["base"])
					require.Equal(t, true, toolRequest.Args["draft"])
					require.Equal(t, true, toolRequest.Args["maintainer_can_modify"])
					body, ok := toolRequest.Args["body"].(string)
					require.True(t, ok)
					require.Contains(t, body, marker)
					require.Contains(t, body, "Closes #7")
					require.Contains(t, body, "Changed files")
					require.Contains(t, body, "unit tests")
					return map[string]any{"text": developmentPublicationJSON(t, pull)}, nil
				default:
					return nil, errors.New("unexpected tool: " + toolRequest.MCPTool)
				}
			},
		)
		runtime := &prWorkspaceImplementationRuntime{provider: githubProvider}
		result, found, err := runtime.findOrCreateDevelopmentPullRequest(
			t.Context(), request, branch, candidateSHA, true,
		)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "99", result.PullRequestID)
		require.Len(t, calls, 2)
	})

	t.Run("rejects duplicate and invalid matching effects", func(t *testing.T) {
		for name, pulls := range map[string][]prWorkspaceGitHubPull{
			"duplicate": {pull, pull},
			"non draft": func() []prWorkspaceGitHubPull {
				invalid := pull
				invalid.Draft = false
				return []prWorkspaceGitHubPull{invalid}
			}(),
		} {
			t.Run(name, func(t *testing.T) {
				githubProvider := newDevelopmentPublicationProvider(
					t,
					func(workflows.ToolRequest) (map[string]any, error) {
						return map[string]any{"text": developmentPublicationJSON(t, pulls)}, nil
					},
				)
				runtime := &prWorkspaceImplementationRuntime{provider: githubProvider}
				_, _, err := runtime.findOrCreateDevelopmentPullRequest(
					t.Context(), request, branch, candidateSHA, false,
				)
				require.Error(t, err)
			})
		}
	})

	t.Run("rejects malformed provider payloads", func(t *testing.T) {
		for _, payload := range []string{"{", `{"items":` + developmentPublicationJSON(t, make([]prWorkspaceGitHubPull, 101)) + `}`} {
			githubProvider := newDevelopmentPublicationProvider(t, func(workflows.ToolRequest) (map[string]any, error) {
				return map[string]any{"text": payload}, nil
			})
			runtime := &prWorkspaceImplementationRuntime{provider: githubProvider}
			_, _, err := runtime.findOrCreateDevelopmentPullRequest(
				t.Context(), request, branch, candidateSHA, false,
			)
			require.Error(t, err)
		}
	})
}

func TestDevelopmentPublicationHelpersBoundAndFailClosed(t *testing.T) {
	provider, request, branch, candidateSHA, marker, pull := developmentPublicationFixture()

	var nilRuntime *prWorkspaceImplementationRuntime
	_, err := nilRuntime.PublishBranch(t.Context(), request)
	require.ErrorContains(t, err, "unavailable")
	_, found, err := nilRuntime.ReconcileBranch(t.Context(), request)
	require.ErrorContains(t, err, "unavailable")
	require.False(t, found)
	zeroManagerRuntime := &prWorkspaceImplementationRuntime{manager: &gitworkspace.Manager{}}
	_, err = zeroManagerRuntime.PublishBranch(t.Context(), prworkspace.BranchPublicationRequest{})
	require.ErrorContains(t, err, "unavailable")
	pinned := gitworkspace.PinnedAcquireRequest{
		Repository: "https://github.com/octo/repo.git", SourceRef: "main",
		ExpectedCommit: strings.Repeat("a", 40), ReservationKey: "pr-workspace:devw_1", AgentID: "agent",
	}
	require.True(t, samePRWorkspacePin(pinned, pinned))
	changedPin := pinned
	changedPin.ExpectedCommit = strings.Repeat("b", 40)
	require.False(t, samePRWorkspacePin(pinned, changedPin))
	leaseRuntime := &prWorkspaceImplementationRuntime{acquireRuntime: func(
		context.Context,
	) (context.Context, func(), error) {
		return nil, nil, errors.New("runtime lease unavailable")
	}}
	_, _, err = leaseRuntime.acquire(t.Context())
	require.ErrorContains(t, err, "runtime lease unavailable")

	require.True(t, strings.HasPrefix(developmentFeatureBranch(provider), "picoclaw/issue-7-"))
	provider.SourceKind = prworkspace.SourceBrief
	provider.SourceNumber = 0
	require.True(t, strings.HasPrefix(developmentFeatureBranch(provider), "picoclaw/feature-"))

	pull.ID = nil
	result := developmentPullResult(pull, branch, candidateSHA)
	require.Equal(t, "7", result.ExternalID)
	require.Equal(t, "7", result.PullRequestID)

	pulls, err := decodeDevelopmentPullList(
		[]byte(developmentPublicationJSON(t, []prWorkspaceGitHubPull{pull})),
	)
	require.NoError(t, err)
	require.Len(t, pulls, 1)
	pulls, err = decodeDevelopmentPullList([]byte(`{"items":[]}`))
	require.NoError(t, err)
	require.Empty(t, pulls)
	_, err = decodeDevelopmentPullList(
		[]byte(`{"items":` + developmentPublicationJSON(t, make([]prWorkspaceGitHubPull, 101)) + `}`),
	)
	require.ErrorContains(t, err, "exceeds bound")

	provider.Body = "Source text\n\nProvider issue comments (untrusted evidence):\nignored"
	provider.Title = ""
	request.Provider = provider
	request.Repair.ResultSummary = ""
	request.Repair.ChangedFiles = make([]string, 51)
	for index := range request.Repair.ChangedFiles {
		request.Repair.ChangedFiles[index] = "pkg/file.go"
	}
	request.Validation.Checks = make([]prworkspace.ValidationCheck, 51)
	for index := range request.Validation.Checks {
		request.Validation.Checks[index] = prworkspace.ValidationCheck{
			Name: "check`name", Status: "passed", Summary: "ok",
		}
	}
	body := developmentPullBody(provider, request.Repair, request.Validation, marker)
	require.NotContains(t, body, "ignored")
	require.Contains(t, body, "Implemented the confirmed development charter")
	require.Contains(t, body, "additional files omitted")
	require.Contains(t, body, "additional checks omitted")
	require.Contains(t, body, "checkname")

	long := strings.Repeat("x", 119) + "éoutside"
	bounded := boundedDevelopmentMarkdown(long, 120)
	require.True(t, strings.HasSuffix(bounded, "…"))
	require.True(t, len(bounded) <= 123)
	require.Equal(t, "Implement requested feature", developmentPullTitle(prworkspace.ProviderSnapshot{}))
}

func TestDevelopmentPlanningEvidenceUsesExactPinnedCommit(t *testing.T) {
	manager, repositoryRoot, head := newGatewayDevelopmentManager(t)
	released := false
	runtime := &prWorkspaceImplementationRuntime{
		manager: manager, agentID: "planning-agent",
		acquireRuntime: func(ctx context.Context) (context.Context, func(), error) {
			return ctx, func() { released = true }, nil
		},
	}
	provider := prworkspace.ProviderSnapshot{
		ProviderOrigin: repositoryRoot, HeadRepository: "octo/repo",
		HeadRef: "main", HeadSHA: head,
	}
	raw, err := runtime.LoadPlanningEvidence(
		t.Context(), "devw_11111111111111111111111111111111", provider,
	)
	require.NoError(t, err)
	require.True(t, released)
	var evidence gitworkspace.PlanningEvidence
	require.NoError(t, json.Unmarshal(raw, &evidence))
	require.Equal(t, head, evidence.Commit)
	require.NotEmpty(t, evidence.Files)
	require.Equal(t, "README.md", evidence.Files[0].Path)
	require.Contains(t, evidence.Files[0].Content, "repair fixture")

	_, err = runtime.LoadPlanningEvidence(t.Context(), "", provider)
	require.ErrorContains(t, err, "unavailable")
	runtime.acquireRuntime = func(context.Context) (context.Context, func(), error) {
		return nil, nil, errors.New("runtime lease unavailable")
	}
	_, err = runtime.LoadPlanningEvidence(
		t.Context(), "devw_11111111111111111111111111111111", provider,
	)
	require.ErrorContains(t, err, "runtime lease unavailable")
	require.DirExists(t, repositoryRoot)
}

func TestDevelopmentCandidateEvidenceAndCodeBrowseStayOnParkedFence(t *testing.T) {
	manager, repositoryRoot, head := newGatewayDevelopmentManager(t)
	ctx := t.Context()
	workspaceID := "devw_22222222222222222222222222222222"
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: filepath.Join(repositoryRoot, "octo", "repo.git"),
		SourceRef:  "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: "candidate-agent",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	require.NoError(t, err)
	baseline, err := manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	require.NoError(t, err)
	lineID := "pdln_22222222222222222222222222222222"
	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, ExpectedTree: baseline.Tree,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace.Path, "feature.txt"), []byte("durable candidate\n"), 0o644,
	))
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	require.NoError(t, err)
	commit, err := manager.CommitPinned(ctx, gitworkspace.PinnedCommitRequest{
		Pin: pin, WorkspaceID: workspace.ID,
		IntentID:       "pdcmt_22222222222222222222222222222222",
		ExpectedParent: candidate.ParentCommit, ExpectedTree: candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
		Message:                 "Add durable candidate", AuthoredAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	parkIntent := "pdlnpark_gateway_candidate_2222222222222222"
	parked, err := manager.ParkPinnedLine(ctx, gitworkspace.PinnedLineParkRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, IntentID: parkIntent,
		ExpectedVersion: lease.Version, MutationEpoch: lease.MutationEpoch,
		PreviousTip: lease.Tip, Tip: commit.Commit, Tree: commit.Tree,
	})
	require.NoError(t, err)
	repair := prworkspace.RepairAttempt{
		CandidateSHA: parked.Tip,
		PublicationFence: &prworkspace.ImplementationPublicationFence{
			GitWorkspaceID: workspace.ID, LineID: lineID, LineVersion: parked.Version,
			MutationEpoch: parked.MutationEpoch, ParkIntentID: parkIntent,
			BaseCommit: parked.PreviousTip, Tip: parked.Tip, Tree: parked.Tree,
		},
	}
	runtime := &prWorkspaceImplementationRuntime{manager: manager}
	evidence, err := runtime.LoadCandidateEvidence(ctx, repair)
	require.NoError(t, err)
	require.Equal(t, repair.CandidateSHA, evidence.CandidateSHA)
	require.Contains(t, evidence.CandidateDiff, "feature.txt")
	require.Equal(t, []string{"feature.txt"}, evidence.Metrics.ChangedFiles)
	require.Equal(t, 1, evidence.Metrics.Files)
	require.Equal(t, 1, evidence.Metrics.Modules)
	require.NotZero(t, evidence.Metrics.SemanticLines)
	require.NotEmpty(t, evidence.EvidenceDigest)

	tree, err := runtime.ListCodeTree(ctx, repair, parked.Tip, "", "")
	require.NoError(t, err)
	require.Equal(t, repair.CandidateSHA, tree.Revision)
	require.Contains(t, tree.Entries, prworkspace.CodeTreeEntry{
		Name: "feature.txt", Path: "feature.txt", Type: "file",
	})
	blob, err := runtime.ReadCodeBlob(ctx, repair, parked.Tip, "feature.txt")
	require.NoError(t, err)
	require.Equal(t, "feature.txt", blob.Path)
	require.Equal(t, "durable candidate\n", blob.Content)

	invalid := repair
	invalid.CandidateSHA = strings.Repeat("f", 40)
	_, err = runtime.LoadCandidateEvidence(ctx, invalid)
	require.ErrorContains(t, err, "fence is invalid")
	_, err = runtime.ListCodeTree(ctx, prworkspace.RepairAttempt{}, parked.Tip, "", "")
	require.ErrorContains(t, err, "unavailable")
	_, err = runtime.ReadCodeBlob(ctx, prworkspace.RepairAttempt{}, parked.Tip, "feature.txt")
	require.ErrorContains(t, err, "unavailable")
}

func newGatewayDevelopmentManager(t *testing.T) (*gitworkspace.Manager, string, string) {
	t.Helper()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	repositoryRoot := t.TempDir()
	remote := filepath.Join(repositoryRoot, "octo", "repo.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
	runPRWorkspaceRepairGit(
		t, source, "clone", "--quiet", "--bare", source, remote,
	)
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: t.TempDir()})
	require.NoError(t, err)
	return manager, repositoryRoot, head
}

func developmentPublicationFixture() (
	prworkspace.ProviderSnapshot,
	prworkspace.BranchPublicationRequest,
	string,
	string,
	string,
	prWorkspaceGitHubPull,
) {
	provider := prworkspace.ProviderSnapshot{
		Intent: prworkspace.IntentImplementFeature, SourceKind: prworkspace.SourceIssue,
		SourceID: "77", SourceNumber: 7, ProviderOrigin: "https://github.com",
		RepositoryID: "42", Repository: "octo/repo", Title: "Add durable notifications",
		Body: "Build a focused notification inbox.", BaseRef: "main", BaseSHA: strings.Repeat("a", 40),
		HeadRepositoryID: "42", HeadRepository: "octo/repo", HeadRef: "main",
		HeadSHA: strings.Repeat("a", 40), CanCreatePullRequest: true,
	}
	branch := developmentFeatureBranch(provider)
	candidateSHA := strings.Repeat("b", 40)
	marker := "<!-- picoclaw-development:77:" + candidateSHA + " -->"
	repository := &prWorkspaceGitHubRepo{
		ID: json.RawMessage(`42`), FullName: provider.Repository,
		HTMLURL: provider.ProviderOrigin + "/" + provider.Repository,
	}
	pull := prWorkspaceGitHubPull{
		ID: json.RawMessage(`99`), Number: 7, Title: developmentPullTitle(provider),
		Body: marker, State: "open", Draft: true,
		HTMLURL: provider.ProviderOrigin + "/" + provider.Repository + "/pull/7",
		Base: &prWorkspaceGitHubBranch{
			Ref: provider.BaseRef, SHA: provider.BaseSHA, Repo: repository,
		},
		Head: &prWorkspaceGitHubBranch{Ref: branch, SHA: candidateSHA, Repo: repository},
	}
	request := prworkspace.BranchPublicationRequest{
		Provider: provider,
		Repair: prworkspace.RepairAttempt{
			CandidateSHA: candidateSHA, ResultSummary: "Added notification delivery.",
			ChangedFiles: []string{"pkg/notifications.go"},
		},
		Validation: prworkspace.ValidationRun{Checks: []prworkspace.ValidationCheck{{
			Name: "unit tests", Status: "passed", Summary: "all passed",
		}}},
	}
	return provider, request, branch, candidateSHA, marker, pull
}

func newDevelopmentPublicationProvider(
	t *testing.T,
	run func(workflows.ToolRequest) (map[string]any, error),
) *reviews.GitHubProvider {
	t.Helper()
	provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
		_ context.Context, request workflows.ToolRequest,
	) (map[string]any, error) {
		return run(request)
	}), "")
	require.NoError(t, err)
	return provider
}

func developmentPublicationJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func TestPRWorkspaceRepairBaselineAdoptsCleanFreshCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("gitworkspace.NewManager() error = %v", err)
	}
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:devw_11111111111111111111111111111111",
		AgentID:        "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	request := gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID}

	baseline, err := snapshotPRWorkspaceRepairBaseline(ctx, manager, request)
	if err != nil {
		t.Fatalf("snapshotPRWorkspaceRepairBaseline(clean) error = %v", err)
	}
	if baseline.WorkspaceID != workspace.ID || baseline.ParentCommit != head ||
		baseline.Tree == "" || baseline.CandidateDigest == "" || baseline.ChangedFiles != 0 {
		t.Fatalf("clean repair baseline = %#v", baseline)
	}
	if _, strictErr := manager.SnapshotPinnedCandidate(ctx, request); strictErr == nil {
		t.Fatal("strict SnapshotPinnedCandidate unexpectedly accepted the clean checkout")
	}

	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID,
		LineID: "pdln_11111111111111111111111111111111", ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine(clean baseline) error = %v", err)
	}
	if lease.WorkspaceID != workspace.ID || lease.Version != 0 || lease.MutationEpoch <= 0 ||
		lease.Tip != head || lease.Tree != baseline.Tree {
		t.Fatalf("adopted clean repair line = %#v", lease)
	}
}

func TestPRWorkspaceCandidateLookupIsScopedByPRWorkspaceAndTree(t *testing.T) {
	const tree = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	firstWorkspace := "devw_11111111111111111111111111111111"
	secondWorkspace := "devw_22222222222222222222222222222222"
	first := prWorkspaceCandidate{candidate: gitworkspace.PinnedCandidate{WorkspaceID: "gw-first", Tree: tree}}
	second := prWorkspaceCandidate{candidate: gitworkspace.PinnedCandidate{WorkspaceID: "gw-second", Tree: tree}}
	runtime := &prWorkspaceImplementationRuntime{candidates: map[prWorkspaceCandidateKey]prWorkspaceCandidate{
		{workspaceID: firstWorkspace, tree: tree}:  first,
		{workspaceID: secondWorkspace, tree: tree}: second,
	}}
	if got, ok := runtime.lookup(firstWorkspace, tree); !ok || got.candidate.WorkspaceID != "gw-first" {
		t.Fatalf("first workspace lookup = %#v, %v", got, ok)
	}
	if got, ok := runtime.lookup(secondWorkspace, tree); !ok || got.candidate.WorkspaceID != "gw-second" {
		t.Fatalf("second workspace lookup = %#v, %v", got, ok)
	}
	if _, ok := runtime.lookup("devw_33333333333333333333333333333333", tree); ok {
		t.Fatal("same-tree candidate leaked across PR workspaces")
	}
}

func initPRWorkspaceRepairTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runPRWorkspaceRepairGit(t, repository, "init")
	runPRWorkspaceRepairGit(t, repository, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# repair fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPRWorkspaceRepairGit(t, repository, "add", "README.md")
	runPRWorkspaceRepairGit(
		t, repository,
		"-c", "user.name=PicoClaw Tests", "-c", "user.email=picoclaw@example.invalid",
		"commit", "-m", "initial fixture",
	)
	return repository
}

func runPRWorkspaceRepairGit(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}
