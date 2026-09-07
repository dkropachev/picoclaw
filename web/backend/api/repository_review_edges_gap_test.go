package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalCollectionAndIdentityEdges(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID

	for _, target := range []string{
		base + "/raw-findings?unknown=1",
		base + "/findings-processing?unknown=1",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed canonical collection %s=%d %s", target, response.Code, response.Body.String())
		}
	}

	now := time.Now().UTC()
	rawSummary := projectRepositoryReviewRawFindingSummary(state.RawFindings[0])
	rawQuery, err := collectionquery.Parse("ALL", repositoryReviewRawFindingCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	rawCursor, err := collectionquery.CursorFor(
		rawQuery,
		rawSummary,
		now,
		repositoryReviewRawFindingPageOptions(
			repositoryReviewCollectionCursorContext("raw-findings", "rra_other", "rrc_other"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	processingQuery, err := collectionquery.Parse("ALL", repositoryReviewFindingsProcessingCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	processingCursor, err := collectionquery.CursorFor(
		processingQuery,
		rawSummary,
		now,
		repositoryReviewFindingsProcessingPageOptions(
			repositoryReviewCollectionCursorContext("findings-processing", "rra_other", "rrp_other"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	issueQuery, err := collectionquery.Parse("ALL", repositoryReviewIssueCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	issueCursor, err := collectionquery.CursorFor(
		issueQuery,
		repositoryReviewIssueCollectionSummary{
			ID: "rid_other", Repository: state.Repository, Title: "Other",
			GenerationID: "rrig_other", State: repoaudit.IssueDraftEditing,
			CreatedAt: now, UpdatedAt: now,
		},
		now,
		repositoryReviewIssuePageOptions(
			repositoryReviewCollectionCursorContext("issues", "rra_other", "rrig_other"),
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		base + "/raw-findings?query=ALL&cursor=" + url.QueryEscape(rawCursor),
		base + "/findings-processing?query=ALL&cursor=" + url.QueryEscape(processingCursor),
		base + "/issues?query=ALL&cursor=" + url.QueryEscape(issueCursor),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"invalid_cursor"`) {
			t.Fatalf("cross-context cursor %s=%d %s", target, response.Code, response.Body.String())
		}
	}

	for _, target := range []string{
		base + "/issues?query=ALL&generation_id=rrig_edge",
		base + "/issues?query=ALL&generation_id=" + url.QueryEscape(strings.Repeat("x", 257)),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if strings.Contains(target, "rrig_edge") {
			if response.Code != http.StatusOK ||
				!strings.Contains(response.Body.String(), `"generation_id":"rrig_edge"`) {
				t.Fatalf("generation-filtered collection=%d %s", response.Code, response.Body.String())
			}
		} else if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"invalid_generation_id"`) {
			t.Fatalf("invalid generation collection=%d %s", response.Code, response.Body.String())
		}
	}

	missingFinding := httptest.NewRecorder()
	mux.ServeHTTP(missingFinding, httptest.NewRequest(
		http.MethodGet,
		base+"/findings/rdf_missing",
		nil,
	))
	if missingFinding.Code != http.StatusNotFound {
		t.Fatalf("missing canonical finding=%d %s", missingFinding.Code, missingFinding.Body.String())
	}

	store := repoaudit.NewSQLiteStore(workspace)
	newCampaignID := repoaudit.NewRepositoryReviewCampaignID()
	updated, err := store.UpdateAutomation(
		t.Context(),
		automation.ID,
		automation.Version,
		func(value *repoaudit.RepositoryReviewAutomation) error {
			value.CampaignID = newCampaignID
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedCampaign := httptest.NewRecorder()
	mux.ServeHTTP(mismatchedCampaign, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+updated.ID+"/raw-findings/"+state.RawFindings[0].ID,
		nil,
	))
	if mismatchedCampaign.Code != http.StatusNotFound {
		t.Fatalf("cross-campaign raw source=%d %s", mismatchedCampaign.Code, mismatchedCampaign.Body.String())
	}
}

func TestRepositoryReviewCanonicalSourceProjectionAndPagingEdges(t *testing.T) {
	if _, found := repositoryReviewContextByID(repoaudit.RepositoryState{}, "rrx_missing"); found {
		t.Fatal("missing canonical context found")
	}
	finding := repoaudit.Finding{
		ID: "rdf_edge", Repository: "owner/repo", Severity: "high", Title: "Edge",
		RawSourceIDs: []string{"rrw_missing", "rrw_present"},
	}
	summary := projectRepositoryReviewDeduplicatedFindingSummary(
		finding,
		newRepositoryReviewRunFindingStatusIndex(repoaudit.RepositoryState{}),
		map[string]repoaudit.RawReviewFinding{
			"rrw_present": {ID: "rrw_present", Model: "provider/model"},
		},
	)
	if len(summary.Contributors) != 1 || summary.Contributors[0] != "provider/model" {
		t.Fatalf("canonical contributor fallback=%#v", summary.Contributors)
	}
	if got := appendUniqueRepositoryReviewContributor([]string{"model"}, " "); len(got) != 1 {
		t.Fatalf("blank contributor changed values=%#v", got)
	}
	if got := appendUniqueRepositoryReviewContributor([]string{"model"}, "model"); len(got) != 1 {
		t.Fatalf("duplicate contributor changed values=%#v", got)
	}

	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewMultiSourceFinding(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	var aggregate repoaudit.Finding
	for _, candidate := range state.Findings {
		if len(candidate.RawSourceIDs) > 1 {
			aggregate = candidate
			break
		}
	}
	if aggregate.ID == "" {
		t.Fatalf("canonical deduplication did not retain multi-source finding: %#v", state.Findings)
	}
	base := "/api/repository-reviews/automations/" + automation.ID
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(
		http.MethodGet,
		base+"/findings/"+aggregate.ID+"/sources?offset=0&limit=1",
		nil,
	))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"next_offset":1`) {
		t.Fatalf("multi-source canonical page=%d %s", page.Code, page.Body.String())
	}
}

func TestRepositoryReviewCanonicalSourceAssociationFence(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 2)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	if len(state.Findings) < 2 || len(state.Findings[0].RawSourceIDs) == 0 || len(state.Findings[1].RawSourceIDs) == 0 {
		t.Fatalf("two canonical source chains required: %#v", state.Findings)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/findings/"+state.Findings[0].ID+"/sources/"+state.Findings[1].RawSourceIDs[0],
		nil,
	))
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-finding source association=%d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewCanonicalLifecycleEdges(t *testing.T) {
	t.Run("resolve provisional duplicate", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 2)
		store := repoaudit.NewSQLiteStore(workspace)
		_, firstJob, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			repoaudit.RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim first canonical mapping=%v err=%v", claimed, err)
		}
		_, candidate, err := store.CompleteMappingJob(
			state.Repository,
			repoaudit.RepositoryMappingCompletion{
				JobID: firstJob.ID, CreateMatchState: repoaudit.RepositoryMatchNew,
				DefaultBranchVerified: true,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, secondJob, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[1].ID,
			repoaudit.RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim second canonical mapping=%v err=%v", claimed, err)
		}
		adjudication := repoaudit.RepositoryMappingAdjudication{
			Decision: "uncertain", CandidateID: candidate.ID, Confidence: .72,
			MatchingAnchors: []string{"bounded behavior"}, Explanation: "Evidence remains ambiguous.",
		}
		_, secondJob, err = store.SaveMappingAdjudication(
			state.Repository,
			secondJob.ID,
			adjudication,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = store.CompleteMappingJob(
			state.Repository,
			repoaudit.RepositoryMappingCompletion{
				JobID: secondJob.ID, CreateMatchState: repoaudit.RepositoryMatchProvisional,
				DefaultBranchVerified: true, ExpectedUniverse: secondJob.CandidateUniverse,
				PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{{
					CandidateID: candidate.ID, Relation: "uncertain", Confidence: .72,
					MatchingAnchors: []string{"bounded behavior"}, Explanation: "Evidence remains ambiguous.",
				}},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		mapped, found, err := store.Get(state.Repository)
		if err != nil || !found {
			t.Fatalf("mapped state found=%v err=%v", found, err)
		}
		var provisional repoaudit.RepositoryFinding
		for _, finding := range mapped.RepositoryFindings {
			if finding.MatchState == repoaudit.RepositoryMatchProvisional {
				provisional = finding
			}
		}
		if provisional.ID == "" || candidate.ID == "" {
			t.Fatalf("canonical provisional pair=%#v", mapped.RepositoryFindings)
		}
		automation := seedRepositoryReviewDetailAutomation(t, handler, mapped.Repository, mapped.Runs[0].ID)
		response := repositoryReviewAutomationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+
				"/repository-findings/"+provisional.ID+"/duplicates",
			map[string]any{
				"candidate_id": candidate.ID, "decision": "distinct",
				"expected_provisional_version": provisional.Version,
			},
		)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"match_state":"new"`) {
			t.Fatalf("resolve canonical duplicate=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("stale issue and duplicate validation selection", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		state := completeRepositoryReviewAPIMappingJobs(t, workspace, seedRepositoryReviewAPIState(t, workspace))
		store := repoaudit.NewSQLiteStore(workspace)
		aggregate := state.RepositoryFindings[0]
		state, aggregate, err := store.UpdateRepositoryFindingIssueSnapshot(
			state.Repository,
			repoaudit.RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
				ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
				Origin: repoaudit.IssueDraftOriginLinked, State: repoaudit.RepositoryFindingIssueOpen,
				Title: "Tracked defect", SnapshotAt: time.Now().UTC().Add(-repoaudit.RepositoryIssueSnapshotTTL),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		path := "/api/repository-reviews/automations/" + automation.ID + "/repository-findings/validations"
		stale := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, map[string]any{
			"repository_finding_ids": []string{aggregate.ID},
		})
		if stale.Code != http.StatusBadRequest || !strings.Contains(stale.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("stale issue validation=%d %s", stale.Code, stale.Body.String())
		}

		_, aggregate, err = store.UpdateRepositoryFindingIssueSnapshot(
			state.Repository,
			repoaudit.RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
				ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
				Origin: repoaudit.IssueDraftOriginLinked, State: repoaudit.RepositoryFindingIssueOpen,
				Title: "Tracked defect", SnapshotAt: time.Now().UTC(),
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		duplicate := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, map[string]any{
			"repository_finding_ids": []string{aggregate.ID, aggregate.ID},
		})
		if duplicate.Code != http.StatusBadRequest {
			t.Fatalf("duplicate validation selection=%d %s", duplicate.Code, duplicate.Body.String())
		}
	})
}

func TestRepositoryReviewCanonicalRequestAndPublicationEdges(t *testing.T) {
	if err := decodeRepositoryReviewRequest(nil, &struct{}{}); err == nil {
		t.Fatal("nil repository review request decoded")
	}
	purge := httptest.NewRecorder()
	writeRepositoryReviewError(purge, repoaudit.ErrRepositoryReviewPurgeInProgress)
	if purge.Code != http.StatusConflict {
		t.Fatalf("purge error status=%d %s", purge.Code, purge.Body.String())
	}

	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := completeRepositoryReviewAPIMappingJobs(t, workspace, seedRepositoryReviewAPIState(t, workspace))
	store := repoaudit.NewSQLiteStore(workspace)
	state, draft, _, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: state.Findings[0].ID,
		GenerationID: "rrig_edge_blocked", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
		InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:   "cheap", GeneratorAccount: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	response := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/publish",
		map[string]any{
			"confirmed": true,
			"issues":    []map[string]any{{"id": draft.ID, "expected_version": draft.Version}},
		},
	)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"success":false`) ||
		!strings.Contains(response.Body.String(), `"publish_blockers"`) {
		t.Fatalf("blocked canonical batch publication=%d %s", response.Code, response.Body.String())
	}
}

func seedRepositoryReviewMultiSourceFinding(t *testing.T, workspace string) repoaudit.RepositoryState {
	t.Helper()
	store := repoaudit.NewSQLiteStore(workspace)
	file := repoaudit.FileRef{
		Path: "pkg/multi.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 100,
		Category: "code", Mode: "100644",
	}
	commit := strings.Repeat("b", 40)
	profileHash := "sha256:" + strings.Repeat("c", 64)
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0, len(repoaudit.RepositoryReviewFocusIDs()))
	for _, focus := range repoaudit.RepositoryReviewFocusIDs() {
		assignment, err := repoaudit.NewRepositoryReviewAssignment(
			focus, "review-model", "prompt-v1", profileHash, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, assignment)
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: "owner/repo-multi-source", CampaignID: campaignID, CommitSHA: commit,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review-model", DeduplicationModel: "review-model",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		t.Context(), "owner/repo-multi-source", commit, "inventory-multi", profileHash,
		campaignID, catalog, []repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "multi-source-run"
	if _, beginErr := store.BeginRepositoryReviewRun(t.Context(), repoaudit.BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: runID, ReviewableFiles: []repoaudit.FileRef{file},
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	line := 9
	for index, assignment := range plan.AssignmentPlans {
		observation := repoaudit.Observation{
			Model: "provider/review-model", ModelAlias: "review-model", Account: "api",
			Reviewer: assignment.FocusID, ScopeFiles: assignment.Files,
			RawDigest: "sha256:" + strings.Repeat("d", 64),
		}
		if index < 2 {
			observation.Findings = []repoaudit.FindingCandidate{{
				Severity: "high", Title: "Lost update", Symbol: "save", File: file.Path, Line: &line,
				Message: "Concurrent writers lose an update.", Evidence: "Both writers replace the same value.",
				Impact:     "Persisted state is lost.",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "Confirmed from immutable source."},
				MatchHints: repoaudit.MatchHints{
					Component: "state", Operation: "save", FailureMode: "lost update",
					Trigger: "concurrent writers", ViolatedInvariant: "updates are retained",
					ObservableOutcome: "state is lost", RelatedSymbols: []string{"save"},
					SourceAnchors: []string{"save"}, DistinguishingFacts: []string{"same write target"},
				},
				FixEffort: testRepositoryReviewFixEffort(),
			}}
		}
		if _, checkpointErr := store.CheckpointRepositoryReviewAssignment(
			t.Context(),
			repoaudit.CheckpointRepositoryReviewAssignmentRequest{
				Plan: plan, RunID: runID, AssignmentID: assignment.AssignmentID,
				AutomationID: "rra_multi_source", AgentID: "main", ChildIndex: index + 1,
				Digest:            "sha256:" + strings.Repeat(fmt.Sprintf("%x", index+4), 64),
				AcknowledgedFiles: []repoaudit.FileRef{file}, Observation: observation,
			},
		); checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
	}
	if _, finalizeErr := store.FinalizeRepositoryReviewRun(
		t.Context(), repoaudit.FinalizeRepositoryReviewRunRequest{Plan: plan, RunID: runID},
	); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	if _, processErr := store.ProcessPendingDeduplicationJobs(
		t.Context(),
		"owner/repo-multi-source",
		repoaudit.DeduplicationProcessOptions{
			Score: func(
				_ context.Context,
				_ repoaudit.RepositoryReviewDeduplicationSnapshot,
				_ string,
				request repoaudit.DeduplicationScoringRequest,
			) (repoaudit.DeduplicationScoringResponse, error) {
				scores := make([]repoaudit.DeduplicationCandidateScore, 0, len(request.Candidates))
				for _, candidate := range request.Candidates {
					scores = append(scores, repoaudit.DeduplicationCandidateScore{
						CandidateID: candidate.ID, Score: 100, Explanation: "Same canonical diagnosis.",
					})
				}
				return repoaudit.DeduplicationScoringResponse{Scores: scores}, nil
			},
			Judge: func(
				_ context.Context,
				_ repoaudit.RepositoryReviewDeduplicationSnapshot,
				_ string,
				request repoaudit.DeduplicationJudgeRequest,
			) (repoaudit.DeduplicationJudgment, error) {
				return repoaudit.DeduplicationJudgment{
					Decision: "duplicate", CandidateID: request.Candidates[0].OpaqueID,
				}, nil
			},
		},
	); processErr != nil {
		t.Fatal(processErr)
	}
	state, found, err := store.Get("owner/repo-multi-source")
	if err != nil || !found {
		t.Fatalf("multi-source canonical state found=%v err=%v", found, err)
	}
	return state
}
