package repoaudit

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCurrentCampaignNewSelectionBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 21, 0, 0, 0, time.UTC)
	campaignID := NewRepositoryReviewCampaignID()
	state := RepositoryState{
		Findings: []Finding{
			{ID: "rdf_projection", CampaignID: campaignID},
			{ID: "legacy-direct", ContextIDs: []string{"ctx-current"}},
			{ID: "legacy-before", ContextIDs: []string{"ctx-before"}},
		},
		Contexts: []FindingContext{
			{ID: "ctx-current", RunID: "wr_current", CreatedAt: now},
			{ID: "ctx-before", RunID: "wr_current", CreatedAt: now.Add(-time.Hour)},
		},
		RawFindings: []RawReviewFinding{
			{ID: "raw-current", RunID: "wr_current", CreatedAt: now},
			{ID: "raw-zero-time", RunID: "wr_current"},
			{ID: "raw-before", RunID: "wr_current", CreatedAt: now.Add(-time.Hour)},
			{ID: "raw-foreign", RunID: "wr_foreign", CreatedAt: now},
		},
		DeduplicatedFindings: []DeduplicatedReviewFinding{
			{ID: "rdf_direct", CampaignID: campaignID},
			{ID: "rdf_projection", CampaignID: "other"},
			{ID: "legacy-direct"},
			{ID: "raw-selected", RawSourceIDs: []string{"raw-current"}},
			{ID: "zero-time-selected", RawSourceIDs: []string{"raw-zero-time"}},
			{ID: "before", RawSourceIDs: []string{"raw-before"}},
			{ID: "foreign", RawSourceIDs: []string{"raw-foreign"}},
		},
	}
	direct := CurrentCampaignDeduplicatedFindings(state, campaignID, nil, time.Time{})
	if got := []string{direct[0].ID, direct[1].ID}; !slices.Equal(
		got, []string{"rdf_direct", "rdf_projection"},
	) {
		t.Fatalf("direct campaign selection=%#v", direct)
	}
	legacy := CurrentCampaignDeduplicatedFindings(
		state, "", []string{"", "wr_current"}, now.Add(-time.Minute),
	)
	if got := []string{legacy[0].ID, legacy[1].ID, legacy[2].ID}; !slices.Equal(
		got, []string{"legacy-direct", "raw-selected", "zero-time-selected"},
	) {
		t.Fatalf("legacy deduplicated selection=%#v", legacy)
	}

	state.RawFindings = append(state.RawFindings,
		RawReviewFinding{ID: "campaign-raw", CampaignID: campaignID},
		RawReviewFinding{ID: "parent-raw", DeduplicatedFindingID: "rdf_direct"},
	)
	raw := CurrentCampaignRawFindings(state, campaignID, nil, time.Time{})
	if len(raw) != 2 || raw[0].ID != "campaign-raw" || raw[1].ID != "parent-raw" {
		t.Fatalf("direct campaign raw selection=%#v", raw)
	}
}

func TestProjectLegacyRawFindingFallbacks(t *testing.T) {
	now := time.Date(2026, 8, 30, 22, 0, 0, 0, time.UTC)
	commit := strings.Repeat("a", 40)
	validFile := FileRef{Path: "pkg/a.go", BlobSHA: strings.Repeat("b", 40)}
	base := Finding{
		ID: "rfn_fallback", Repository: "owner/repo", CommitSHA: commit,
		File: validFile, Severity: "high", Title: "title", Evidence: "evidence",
		Impact: "impact", Validation: Validation{Status: "confirmed", Summary: "confirmed"},
	}
	fromRun := base
	fromRun.ContextIDs = []string{"missing"}
	fromRun.Models = []string{" model-list "}
	state := RepositoryState{
		Repository: "owner/repo", UpdatedAt: now,
		Runs: []ReviewRun{{ID: "wr_retained", FindingIDs: []string{fromRun.ID}}},
	}
	raw := projectLegacyRawReviewFinding(state, fromRun, 3)
	if raw.RunID != "wr_retained" || raw.Model != "model-list" ||
		raw.Reviewer != "model-list" || raw.CreatedAt != now || raw.UpdatedAt != now ||
		!ValidRepositoryReviewCampaignID(raw.CampaignID) || raw.InsertionOrdinal != 3 {
		t.Fatalf("run/model fallback raw=%#v", raw)
	}

	fromObservation := base
	fromObservation.ID = "rfn_observation"
	fromObservation.Observations = []FindingObservation{{
		Model: " observation-model ", Reviewer: " observation-reviewer ",
	}}
	fromObservation.CreatedAt = now
	fromObservation.UpdatedAt = now.Add(-time.Minute)
	observationRaw := projectLegacyRawReviewFinding(
		RepositoryState{Repository: "owner/repo"}, fromObservation, 1,
	)
	if observationRaw.RunID != "legacy:"+fromObservation.ID ||
		observationRaw.Model != "observation-model" ||
		observationRaw.Reviewer != "observation-reviewer" ||
		observationRaw.UpdatedAt != now {
		t.Fatalf("observation fallback raw=%#v", observationRaw)
	}

	defaults := base
	defaults.ID = "rfn_defaults"
	defaults.File = FileRef{}
	defaultRaw := projectLegacyRawReviewFinding(RepositoryState{}, defaults, 1)
	if defaultRaw.Model != "historical-review" || defaultRaw.Reviewer != "historical-review" ||
		defaultRaw.CreatedAt != time.Unix(0, 0).UTC() ||
		!strings.HasPrefix(defaultRaw.AdmissionBucket, "rdb_") {
		t.Fatalf("default/bucket fallback raw=%#v", defaultRaw)
	}

	contextCampaign := NewRepositoryReviewCampaignID()
	fromContext := base
	fromContext.ID = "rfn_context"
	fromContext.CampaignID = contextCampaign
	fromContext.ContextIDs = []string{"ctx"}
	contextRaw := projectLegacyRawReviewFinding(RepositoryState{
		Contexts: []FindingContext{{
			ID: "ctx", RunID: "wr_context", Model: "context-model",
			ModelAlias: "context-model-alias", Account: "context-account",
			Reviewer: "context-reviewer",
		}},
	}, fromContext, 1)
	if contextRaw.ContextID != "ctx" || contextRaw.RunID != "wr_context" ||
		contextRaw.CampaignID != contextCampaign || contextRaw.Model != "context-model" ||
		contextRaw.ModelAlias != "context-model-alias" || contextRaw.Account != "context-account" ||
		contextRaw.Reviewer != "context-reviewer" {
		t.Fatalf("context projection raw=%#v", contextRaw)
	}

	mixedContexts := fromContext
	mixedContexts.ID = "rfn_mixed_contexts"
	mixedContexts.ContextIDs = []string{"missing", "first", "later"}
	mixedRaw := projectLegacyRawReviewFinding(RepositoryState{
		Contexts: []FindingContext{
			{
				ID: "first", RunID: " first-run ", Model: " first-model ",
				ModelAlias: "unpaired-alias", Reviewer: " first-reviewer ",
			},
			{
				ID: "later", RunID: "later-run", Model: "later-model",
				ModelAlias: "later-alias", Account: "later-account", Reviewer: "later-reviewer",
			},
		},
	}, mixedContexts, 1)
	if mixedRaw.ContextID != "first" || mixedRaw.RunID != "first-run" ||
		mixedRaw.Model != "first-model" || mixedRaw.ModelAlias != "" || mixedRaw.Account != "" ||
		mixedRaw.Reviewer != "first-reviewer" {
		t.Fatalf("mixed context projection raw=%#v", mixedRaw)
	}
}

func TestBindHistoricalDeduplicationCampaignBranches(t *testing.T) {
	commit := strings.Repeat("a", 40)
	otherCommit := strings.Repeat("b", 40)
	campaignID := NewRepositoryReviewCampaignID()
	otherCampaign := NewRepositoryReviewCampaignID()
	if !errors.Is(bindHistoricalDeduplicationCampaign(
		nil, "legacy", "raw", campaignID, commit,
	), ErrInvalidPlan) || !errors.Is(bindHistoricalDeduplicationCampaign(
		&RepositoryState{}, "legacy", "raw", "wr_invalid", commit,
	), ErrInvalidPlan) {
		t.Fatal("invalid campaign binding was accepted")
	}
	base := func() RepositoryState {
		return RepositoryState{
			Findings: []Finding{{
				ID: "legacy", CommitSHA: commit, CampaignID: campaignID,
				ContextIDs: []string{"original"},
			}},
			Contexts: []FindingContext{
				{ID: "raw", CommitSHA: commit, InventoryHash: "historical-replay", ProfileHash: "historical-replay"},
				{ID: "original", CommitSHA: commit, CampaignID: campaignID},
			},
		}
	}
	state := base()
	if err := bindHistoricalDeduplicationCampaign(
		&state, "legacy", "raw", campaignID, commit,
	); err != nil || state.CampaignHistory[campaignID] != commit ||
		state.Contexts[0].CampaignID != campaignID {
		t.Fatalf("campaign bind=%#v err=%v", state, err)
	}
	if err := bindHistoricalDeduplicationCampaign(
		&state, "legacy", "raw", campaignID, commit,
	); err != nil {
		t.Fatalf("idempotent bind error=%v", err)
	}
	state.Contexts[0].CampaignID = otherCampaign
	if err := bindHistoricalDeduplicationCampaign(
		&state, "legacy", "raw", campaignID, commit,
	); err != nil || state.Contexts[0].CampaignID != campaignID {
		t.Fatalf("replay context rebind state=%#v err=%v", state.Contexts[0], err)
	}

	checks := []struct {
		name   string
		mutate func(*RepositoryState)
	}{
		{"history conflict", func(s *RepositoryState) {
			s.CampaignHistory = map[string]string{campaignID: otherCommit}
		}},
		{"missing raw context", func(s *RepositoryState) { s.Contexts = s.Contexts[1:] }},
		{"raw commit mismatch", func(s *RepositoryState) { s.Contexts[0].CommitSHA = otherCommit }},
		{"raw campaign conflict", func(s *RepositoryState) {
			s.Contexts[0].CampaignID = otherCampaign
			s.Contexts[0].InventoryHash = "native"
		}},
		{"missing finding", func(s *RepositoryState) { s.Findings = nil }},
		{"finding commit mismatch", func(s *RepositoryState) { s.Findings[0].CommitSHA = otherCommit }},
		{"finding campaign mismatch", func(s *RepositoryState) { s.Findings[0].CampaignID = otherCampaign }},
		{"original context conflict", func(s *RepositoryState) { s.Contexts[1].CampaignID = otherCampaign }},
	}
	for _, test := range checks {
		t.Run(test.name, func(t *testing.T) {
			candidate := base()
			test.mutate(&candidate)
			if err := bindHistoricalDeduplicationCampaign(
				&candidate, "legacy", "raw", campaignID, commit,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("error=%v state=%#v", err, candidate)
			}
		})
	}
	untagged := base()
	untagged.Findings[0].CampaignID = ""
	untagged.Findings[0].ContextIDs = nil
	if err := bindHistoricalDeduplicationCampaign(
		&untagged, "legacy", "raw", campaignID, commit,
	); err != nil || untagged.Findings[0].CampaignID != "" {
		t.Fatalf("untagged provenance bind=%#v err=%v", untagged, err)
	}
}

func TestHistoricalLifecycleHelperBranches(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC)
	historical := RawReviewFinding{
		ID: "rrw_historical", LegacyFindingID: "legacy", AssignmentID: historicalReplayAssignmentID,
	}
	if _, err := rehomeHistoricalDeduplicatedLifecycle(
		nil, DeduplicatedReviewFinding{}, nil, nil, now,
	); err == nil {
		t.Fatal("nil rehome state accepted")
	}
	state := RepositoryState{RawFindings: []RawReviewFinding{historical}}
	if _, err := rehomeHistoricalDeduplicatedLifecycle(
		&state, DeduplicatedReviewFinding{RawSourceIDs: []string{"live", historical.ID}},
		map[string]struct{}{historical.ID: {}}, map[string]int{}, now,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing raw index error=%v", err)
	}
	if _, err := rehomeHistoricalDeduplicatedLifecycle(
		&state, DeduplicatedReviewFinding{RawSourceIDs: []string{"live"}},
		map[string]struct{}{historical.ID: {}}, map[string]int{historical.ID: 0}, now,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing legacy identity error=%v", err)
	}
	state.Findings = []Finding{{ID: "legacy", Status: FindingPosted, IssueDraftID: "keep", RepositoryFindingID: "repo"}}
	if _, err := rehomeHistoricalDeduplicatedLifecycle(
		&state, DeduplicatedReviewFinding{
			RawSourceIDs: []string{historical.ID}, Status: FindingStatus("invalid"),
		}, map[string]struct{}{historical.ID: {}}, map[string]int{historical.ID: 0}, now,
	); err != nil || state.Findings[0].Status != FindingPosted ||
		state.Findings[0].IssueDraftID != "keep" || state.Findings[0].RepositoryFindingID != "repo" {
		t.Fatalf("empty lifecycle fields overwrote retained state=%#v err=%v", state.Findings[0], err)
	}

	if err := restoreHistoricalDeduplicatedLifecycle(nil, historical, "target", now); err != nil {
		t.Fatalf("nil restore should be ignored: %v", err)
	}
	if err := restoreHistoricalDeduplicatedLifecycle(
		&RepositoryState{}, RawReviewFinding{}, "target", now,
	); err != nil {
		t.Fatalf("native restore should be ignored: %v", err)
	}
	for name, candidate := range map[string]RepositoryState{
		"missing legacy": {
			DeduplicatedFindings: []DeduplicatedReviewFinding{{ID: "target"}},
			Findings:             []Finding{{ID: "target"}},
		},
		"missing target": {Findings: []Finding{{ID: "legacy"}, {ID: "target"}}},
		"missing projection": {
			DeduplicatedFindings: []DeduplicatedReviewFinding{{ID: "target"}},
			Findings:             []Finding{{ID: "legacy"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := restoreHistoricalDeduplicatedLifecycle(
				&candidate, historical, "target", now,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("error=%v state=%#v", err, candidate)
			}
		})
	}

	statusCases := []struct {
		left, right, want FindingStatus
	}{
		{FindingOpen, FindingPosted, FindingPosted},
		{FindingPosted, FindingDismissed, FindingPosted},
		{FindingStatus(""), FindingStatus(""), FindingOpen},
		{FindingDismissed, FindingOpen, FindingDismissed},
	}
	for _, test := range statusCases {
		if got := mergeHistoricalFindingStatus(test.left, test.right); got != test.want {
			t.Errorf("merge status %q + %q = %q, want %q", test.left, test.right, got, test.want)
		}
	}
}

func TestCompleteDeduplicationReturnsHistoricalRestoreConflict(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "restore-conflict-run", ReviewableFiles: fixture.files,
		},
	); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(
		fixture, "restore-conflict-run", 0, fixture.files,
	)
	checkpoint.Observation.Findings = []FindingCandidate{
		repositoryReviewCampaignFinding(fixture.files[0], "historical restore conflict"),
	}
	result, err := fixture.store.CheckpointRepositoryReviewAssignment(
		t.Context(), checkpoint,
	)
	if err != nil {
		t.Fatal(err)
	}
	state := result.State
	raw := &state.RawFindings[0]
	raw.LegacyFindingID = "missing-legacy"
	raw.AssignmentID = historicalReplayAssignmentID
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
	state.FindingsProcessing.UpdatedAt = repositoryAuditTestNow
	if saveErr := fixture.store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	_, claim, ok, err := fixture.store.ClaimDeduplicationJob(
		state.Repository, state.DeduplicationJobs[0].ID, time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("claim=%#v ok=%v err=%v", claim, ok, err)
	}
	_, _, created, err := fixture.store.CompleteDeduplicationJob(
		state.Repository,
		DeduplicationCompletion{
			JobID:                   claim.Job.ID,
			LeaseID:                 claim.Job.LeaseID,
			CandidateUniverseDigest: claim.UniverseDigest,
			Decision:                DeduplicationJudgment{Decision: "new"},
		},
	)
	if !errors.Is(err, ErrConflict) || created {
		t.Fatalf("historical restore conflict=%v created=%v", err, created)
	}
}

func TestHistoricalResetMigrationErrorBranches(t *testing.T) {
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	commit := strings.Repeat("a", 40)
	campaignID := NewRepositoryReviewCampaignID()
	file := FileRef{Path: "pkg/a.go", BlobSHA: strings.Repeat("b", 40)}
	snapshot := historicalReplayCoverageSnapshot()
	base := func() RepositoryState {
		raw := RawReviewFinding{
			ID: "rrw_historical", LegacyFindingID: "legacy",
			AssignmentID: historicalReplayAssignmentID, CampaignID: "wr_old",
			Repository: "owner/repo", CommitSHA: commit, File: file,
			ContextID: "ctx", RunID: "wr_old", Model: "model",
			State: RawFindingDeduplicationFailed, Disposition: RawFindingDispositionUndecided,
		}
		return RepositoryState{
			Repository: "owner/repo",
			Findings: []Finding{{
				ID: "legacy", CampaignID: campaignID, CommitSHA: commit,
				File: file, ContextIDs: []string{"ctx"},
			}},
			Contexts:    []FindingContext{{ID: "ctx", CampaignID: campaignID, CommitSHA: commit}},
			RawFindings: []RawReviewFinding{raw},
			DeduplicationJobs: []DeduplicationJob{{
				ID: "job", RawFindingID: raw.ID, State: DeduplicationJobFailed,
			}},
		}
	}
	missingJob := base()
	missingJob.DeduplicationJobs = nil
	if err := resetHistoricalDeduplicationModelWork(&missingJob, snapshot, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing migration job error=%v", err)
	}

	fallback := base()
	fallback.Findings[0].CampaignID = ""
	fallback.Findings[0].ContextIDs = nil
	fallback.Contexts[0].CampaignID = ""
	fallback.DeduplicatedFindings = []DeduplicatedReviewFinding{{ID: "legacy"}}
	if err := resetHistoricalDeduplicationModelWork(&fallback, snapshot, now); err != nil ||
		!ValidRepositoryReviewCampaignID(fallback.RawFindings[0].CampaignID) ||
		fallback.RawFindings[0].State != RawFindingDeduplicationPending {
		t.Fatalf("fallback migration state=%#v err=%v", fallback, err)
	}

	missingLegacy := base()
	missingLegacy.Findings = nil
	if err := resetHistoricalDeduplicationModelWork(&missingLegacy, snapshot, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing fallback legacy error=%v", err)
	}

	missingContext := base()
	missingContext.Contexts = nil
	if err := resetHistoricalDeduplicationModelWork(&missingContext, snapshot, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing reset context error=%v", err)
	}

	invalidBucket := base()
	invalidBucket.Findings[0].File = FileRef{}
	invalidBucket.RawFindings[0].File = FileRef{}
	if err := resetHistoricalDeduplicationModelWork(&invalidBucket, snapshot, now); err == nil {
		t.Fatal("invalid reset admission bucket was accepted")
	}

	rehomeFailure := RepositoryState{
		RawFindings: []RawReviewFinding{{
			ID: "rrw_historical", LegacyFindingID: "missing",
			AssignmentID: historicalReplayAssignmentID,
		}},
		DeduplicationJobs: []DeduplicationJob{{RawFindingID: "rrw_historical"}},
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "rdf_historical", RawSourceIDs: []string{"rrw_historical"},
		}},
	}
	if err := resetHistoricalDeduplicationModelWork(&rehomeFailure, snapshot, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("rehome failure error=%v", err)
	}
}

func TestHistoricalReplayRemainingStoreBranches(t *testing.T) {
	now := time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC)
	snapshot := historicalReplayCoverageSnapshot()
	wrongState := NewStore(t.TempDir())
	wrongState.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{HistoricalDeduplication: HistoricalDeduplicationReplay{
			Required: true, Status: HistoricalDeduplicationReplayStatus("unknown"), UpdatedAt: now,
		}}, nil
	}
	if _, _, err := wrongState.RetryHistoricalDeduplicationReplay("owner/repo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong retry state error=%v", err)
	}

	locked := NewStore(t.TempDir())
	if err := os.Mkdir(repositoryReviewTestLockPath(t, locked.root, "store.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := locked.RetryHistoricalDeduplicationReplay("owner/repo"); err == nil {
		t.Fatal("retry lock failure hidden")
	}
	if _, _, err := locked.AdmitNextHistoricalDeduplicationBatch("owner/repo"); err == nil {
		t.Fatal("admission lock failure hidden")
	}
	if _, _, err := locked.FreezeHistoricalDeduplicationReplay("owner/repo", snapshot); err == nil {
		t.Fatal("freeze lock failure hidden")
	}
	if _, _, _, err := locked.AcquireHistoricalDeduplicationMerge(
		"owner/repo", "lease", nil,
	); err == nil {
		t.Fatal("acquire lock failure hidden")
	}
	if _, _, err := locked.CompleteHistoricalDeduplicationMerge("owner/repo", "lease"); err == nil {
		t.Fatal("complete lock failure hidden")
	}
	if _, _, err := locked.FailHistoricalDeduplicationReplay("owner/repo", ""); err == nil {
		t.Fatal("fail lock failure hidden")
	}

	commit := strings.Repeat("a", 40)
	file := FileRef{Path: "pkg/a.go", BlobSHA: strings.Repeat("b", 40)}
	campaignID := NewRepositoryReviewCampaignID()
	otherCampaign := NewRepositoryReviewCampaignID()
	admission := NewStore(t.TempDir())
	admission.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: "owner/repo",
			HistoricalDeduplication: HistoricalDeduplicationReplay{
				Required: true, Status: HistoricalDeduplicationReplaying,
				ProfileSnapshot: snapshot,
			},
			Findings: []Finding{{
				ID: "legacy", CampaignID: campaignID, CommitSHA: commit,
				File: file, ContextIDs: []string{"ctx"},
			}},
			Contexts: []FindingContext{{
				ID: "ctx", CampaignID: otherCampaign, CommitSHA: commit,
				InventoryHash: "native", ProfileHash: "native", RunID: "wr_old",
			}},
		}, nil
	}
	if _, _, err := admission.AdmitNextHistoricalDeduplicationBatch("owner/repo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("admission campaign bind error=%v", err)
	}

	finding := Finding{
		ID: "legacy", Repository: "owner/repo", CommitSHA: commit, File: file,
		Models: []string{"model"},
	}
	raw, _, err := historicalRawFindingAndJob(
		RepositoryState{
			Repository: "owner/repo",
			Runs:       []ReviewRun{{ID: "wr_selected", FindingIDs: []string{finding.ID}}},
		}, finding,
		HistoricalDeduplicationReplayBatch{CampaignID: campaignID}, nil, snapshot, 1, now,
	)
	if err != nil || raw.RunID != "wr_selected" {
		t.Fatalf("historical run fallback raw=%#v err=%v", raw, err)
	}
}
