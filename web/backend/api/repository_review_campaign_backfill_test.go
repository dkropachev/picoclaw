package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reposcope"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewBackfillRunSpec struct {
	selected         []int
	inspected        []int
	unsupported      []int
	occurrences      int
	requiredChildren int
	complete         bool
}

type repositoryReviewBackfillFixture struct {
	workspace  string
	store      repoaudit.Store
	runStore   *workflows.FileRunStore
	automation repoaudit.RepositoryReviewAutomation
	state      repoaudit.RepositoryState
	runs       map[string]*workflows.Run
}

func newRepositoryReviewBackfillFixture(
	t *testing.T,
	fileCount int,
	specs ...repositoryReviewBackfillRunSpec,
) repositoryReviewBackfillFixture {
	t.Helper()
	workspace := t.TempDir()
	store := repoaudit.NewStore(workspace)
	runStore := workflows.NewFileRunStore(workspace)
	repository := "owner/backfill"
	commitSHA := strings.Repeat("a", 40)
	files := make([]repoaudit.FileRef, 0, fileCount)
	for index := range fileCount {
		files = append(files, repoaudit.FileRef{
			Path: fmt.Sprintf("pkg/file_%03d.go", index), BlobSHA: fmt.Sprintf("%040x", index+1),
			SizeBytes: int64(100 + index), Category: "code", Mode: "100644",
		})
	}
	inventoryFiles := make([]reposcope.FileMetadata, 0, len(files))
	for _, file := range files {
		inventoryFiles = append(inventoryFiles, reposcope.FileMetadata{
			Path: file.Path, BlobID: file.BlobSHA, Size: file.SizeBytes,
			Kind: reposcope.FileKindRegular, Sample: []byte("package fixture\nfunc retained() {}\n"),
		})
	}
	candidates, rejections, err := reposcope.BuildCandidates(reposcope.Inventory{
		CommitID: commitSHA, ID: "inventory-backfill", Files: inventoryFiles,
	}, reposcope.Scope{CodeTypes: []reposcope.CodeType{
		reposcope.CodeTypeHotpath, reposcope.CodeTypeCode,
	}}, reposcope.BuildOptions{})
	if err != nil || len(rejections) != 0 || len(candidates) != len(files) {
		t.Fatalf("build candidate catalog candidates=%d rejections=%#v err=%v", len(candidates), rejections, err)
	}
	startedAt := time.Now().UTC().Add(-time.Hour)
	fixture := repositoryReviewBackfillFixture{
		workspace: workspace, store: store, runStore: runStore,
		runs: make(map[string]*workflows.Run),
		automation: repoaudit.RepositoryReviewAutomation{
			ID: "rra_backfill", Name: "Backfill", Repository: repository,
			ResolvedCommitSHA:   commitSHA,
			EffectiveAccountRef: "account-backfill",
			Target:              "all", ReviewFocus: "Recover retained evidence.",
			ReviewerModels: []string{"review-a"}, IssueWriterModel: "review-a",
			MaxFilesPerRun: fileCount, MaxContentBytes: 512 << 10, MaxParallelChildren: 2,
			Status:      repoaudit.RepositoryReviewAutomationPaused,
			PauseReason: repoaudit.RepositoryReviewPauseManual,
			PauseDetail: "Retained legacy campaign.", StartedAt: startedAt,
		},
	}
	normalizedPolicy, policyErr := repoaudit.NormalizeRepositoryReviewScopePolicy(
		fixture.automation.ScopePolicy,
	)
	if policyErr != nil {
		t.Fatal(policyErr)
	}
	fixture.automation.ScopePolicy = normalizedPolicy
	for runIndex, spec := range specs {
		selectedFiles := files
		if len(spec.selected) > 0 {
			selectedFiles = make([]repoaudit.FileRef, 0, len(spec.selected))
			for _, index := range spec.selected {
				selectedFiles = append(selectedFiles, files[index])
			}
		}
		selectedPaths := make([]string, 0, len(selectedFiles))
		for _, file := range selectedFiles {
			selectedPaths = append(selectedPaths, file.Path)
		}
		legacySelection, legacyScopePlan, scopeErr := workflows.RecoverRepositoryReviewFrozenScope(
			candidates, fixture.automation.ScopePolicy, commitSHA, "inventory-backfill", selectedPaths,
		)
		if scopeErr != nil {
			t.Fatal(scopeErr)
		}
		fixture.automation.ScopePlan = legacyScopePlan
		fixture.automation.ScopeSelection = &legacySelection
		scopePolicyJSON, marshalErr := json.Marshal(fixture.automation.ScopePolicy)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		legacyProfileInput := workflows.NewRepositoryBugFinderProfileHashInput(
			fixture.automation.EffectiveAccountRef, fixture.automation.Target,
			fixture.automation.ReviewFocus, string(scopePolicyJSON), legacyScopePlan.Hash,
			strings.Join(fixture.automation.ReviewerModels, ","), "legacy-automation-profile",
			fixture.automation.ReviewerModels, false, fixture.automation.MaxContentBytes,
		)
		legacyProfileHash, hashErr := workflows.RepositoryBugFinderLegacyResolvedProfileHash(
			legacyProfileInput,
		)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		plan, planErr := store.PlanWithProfileLimitAuthoritative(
			t.Context(), repository, commitSHA, "inventory-backfill", legacyProfileHash,
			selectedFiles, false, fileCount, true,
		)
		if planErr != nil {
			t.Fatal(planErr)
		}
		inspected := make([]repoaudit.FileRef, 0, len(spec.inspected))
		for _, index := range spec.inspected {
			inspected = append(inspected, files[index])
		}
		unsupportedIndexes := make(map[int]struct{}, len(spec.unsupported))
		unsupported := make([]repoaudit.UnsupportedFile, 0, len(spec.unsupported))
		for _, index := range spec.unsupported {
			unsupportedIndexes[index] = struct{}{}
			unsupported = append(unsupported, repoaudit.UnsupportedFile{
				FileRef: files[index], Reason: "file_too_large",
			})
		}
		reviewable := make([]repoaudit.FileRef, 0, len(selectedFiles)-len(unsupported))
		for _, file := range selectedFiles {
			index := -1
			for candidateIndex, candidateFile := range files {
				if candidateFile == file {
					index = candidateIndex
					break
				}
			}
			if _, terminal := unsupportedIndexes[index]; !terminal {
				reviewable = append(reviewable, file)
			}
		}
		findingCandidates := make([]repoaudit.FindingCandidate, 0, spec.occurrences)
		for index := range spec.occurrences {
			primary := inspected[index%len(inspected)]
			findingCandidates = append(
				findingCandidates, repositoryReviewBackfillFinding(primary, runIndex, index),
			)
		}
		runID := fmt.Sprintf("wr_backfill_%02d", runIndex)
		completedAt := startedAt.Add(time.Duration(runIndex+1) * time.Minute)
		observations := []repoaudit.Observation(nil)
		if len(reviewable) > 0 {
			observations = []repoaudit.Observation{{
				Model: "review-a", Reviewer: "correctness", ScopeFiles: reviewable,
				Findings:  findingCandidates,
				RawDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("validated"))),
			}}
		}
		completedFiles := []repoaudit.FileRef{}
		if spec.complete {
			completedFiles = append(completedFiles, reviewable...)
		}
		recorded, recordErr := store.Record(t.Context(), repoaudit.RecordRequest{
			Plan: plan, RunID: runID, CompletedAt: completedAt,
			CompletedFiles: completedFiles, UnsupportedFiles: unsupported,
			Observations: observations,
		})
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		for findingIndex, findingID := range recorded.Run.FindingIDs {
			for _, finding := range recorded.State.Findings {
				if finding.ID != findingID {
					continue
				}
				recorded.Run.FindingIDs[findingIndex] = repositoryReviewBackfillLegacyFindingID(
					finding, runID,
				)
				break
			}
		}
		structured := repositoryReviewBackfillStructured(t, inspected, findingCandidates)
		scope := repositoryReviewBackfillFileMaps(reviewable)
		managedChildren := []map[string]any(nil)
		reviewStatus := workflows.RunStatusSkipped
		if len(reviewable) > 0 {
			reviewStatus = workflows.RunStatusSucceeded
			requiredChildren := spec.requiredChildren
			if requiredChildren == 0 {
				requiredChildren = 4
			}
			managedChildren = append(managedChildren, map[string]any{
				"label": "correctness", "required": true, "valid": true,
				"tasks": []string{workflows.RepositoryBugFinderFocuses()[0].Task},
				"scope": scope, "structured": structured, "text": "validated",
				"model": map[string]any{"selected": "review-a"},
			})
			for childIndex := 1; childIndex < requiredChildren; childIndex++ {
				if spec.complete {
					managedChildren = append(managedChildren, map[string]any{
						"label":    fmt.Sprintf("challenge-%d", childIndex),
						"tasks":    []string{workflows.RepositoryBugFinderFocuses()[childIndex%4].Task},
						"required": true, "valid": true, "scope": scope,
						"structured": structured, "text": "validated",
						"model": map[string]any{"selected": "review-a"},
					})
					continue
				}
				managedChildren = append(managedChildren, map[string]any{
					"label":    fmt.Sprintf("challenge-%d", childIndex),
					"tasks":    []string{workflows.RepositoryBugFinderFocuses()[childIndex%4].Task},
					"required": true, "valid": false, "scope": scope,
					"run_error": "deadline exceeded",
					"model":     map[string]any{"selected": "review-b"},
				})
			}
		}
		completed := completedAt
		workflowRun := &workflows.Run{
			ID: runID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
			Status: workflows.RunStatusSucceeded, CreatedAt: completedAt.Add(-time.Minute),
			CompletedAt: &completed,
			Steps: map[string]workflows.StepExecution{
				"find_bugs/plan": {
					ID: "plan", Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{
						"plan": plan, "accountRef": fixture.automation.EffectiveAccountRef,
						"reviewerModels":         fixture.automation.ReviewerModels,
						"includeDefaultReviewer": false,
						"maxContentBytes":        int(fixture.automation.MaxContentBytes),
					},
				},
				"find_bugs/full_scope_catalog": {
					ID: "full_scope_catalog", Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{"candidates": candidates},
				},
				"find_bugs/scope": {
					ID: "scope", Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{"scopePlan": legacyScopePlan, "selectedPaths": selectedPaths},
				},
				"find_bugs/review": {
					ID: "review", Status: reviewStatus,
					Outputs: map[string]any{"managed_children": managedChildren},
				},
				"find_bugs/record": {
					ID: "record", Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{"run": recorded.Run},
				},
			},
		}
		if createErr := runStore.CreateRun(t.Context(), workflowRun); createErr != nil {
			t.Fatal(createErr)
		}
		fixture.automation.RunIDs = append(fixture.automation.RunIDs, runID)
		fixture.runs[runID] = workflowRun
	}
	state, found, err := store.Get(repository)
	if err != nil || !found {
		t.Fatalf("load repository state found=%v err=%v", found, err)
	}
	legacyIDs := make(map[string]string)
	for runIndex := range state.Runs {
		for findingIndex, findingID := range state.Runs[runIndex].FindingIDs {
			for _, finding := range state.Findings {
				if finding.ID != findingID {
					continue
				}
				legacyID := repositoryReviewBackfillLegacyFindingID(finding, state.Runs[runIndex].ID)
				legacyIDs[findingID] = legacyID
				state.Runs[runIndex].FindingIDs[findingIndex] = legacyID
				break
			}
		}
	}
	for index := range state.Findings {
		if legacyID := legacyIDs[state.Findings[index].ID]; legacyID != "" {
			state.Findings[index].ID = legacyID
		}
	}
	// This fixture exercises pre-v4 recovery. Explicitly discard the current
	// Record gate so it cannot accidentally become a native rdf/rrw fixture as
	// the production admission pipeline evolves.
	state.RawFindings = nil
	state.DeduplicatedFindings = nil
	state.DeduplicationJobs = nil
	state.MappingJobs = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	state.FindingCount = len(state.Findings)
	persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
	fixture.state = state
	created, err := store.CreateAutomation(t.Context(), fixture.automation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.automation = created
	return fixture
}

func repositoryReviewBackfillLegacyFindingID(
	finding repoaudit.Finding,
	runID string,
) string {
	hash := sha256.New()
	for _, value := range []string{
		finding.Repository, finding.CommitSHA, runID, finding.Fingerprint,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("rfn_%x", hash.Sum(nil))
}

func authorizeRepositoryReviewBackfillCampaign(
	t *testing.T,
	fixture *repositoryReviewBackfillFixture,
	campaignID string,
) {
	t.Helper()
	updated, err := fixture.store.UpdateAutomation(
		t.Context(), fixture.automation.ID, fixture.automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.CampaignID = campaignID
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.automation = updated
}

func repositoryReviewBackfillFinding(
	file repoaudit.FileRef,
	runIndex int,
	index int,
) repoaudit.FindingCandidate {
	line := index + 1
	identity := fmt.Sprintf("%02d-%03d", runIndex, index)
	return repoaudit.FindingCandidate{
		Severity: "high", Title: "retained defect " + identity, Symbol: "Process" + identity,
		File: file.Path, Line: &line, Message: "The retained path loses state " + identity + ".",
		Evidence: "The immutable branch reaches the failing state " + identity + ".",
		Impact:   "A caller observes lost state " + identity + ".",
		Validation: repoaudit.Validation{
			Status: "confirmed", Summary: "traced retained path " + identity,
			Checks: []string{"checked branch " + identity},
		},
		MatchHints: repoaudit.MatchHints{
			Component: "backfill", Operation: "process " + identity,
			FailureMode: "state is lost " + identity, Trigger: "retained input " + identity,
			ViolatedInvariant: "state remains visible " + identity,
			ObservableOutcome: "caller observes loss " + identity,
			RelatedSymbols:    []string{"Process" + identity}, SourceAnchors: []string{"anchor_" + identity},
			DistinguishingFacts: []string{"unique retained path " + identity},
		},
		FixEffort: repoaudit.FixEffort{
			Quick: repoaudit.FixEffortEstimate{
				LOCMin: 1, LOCMax: 10, Class: "tiny", Rationale: "Localized containment.",
			},
			Quality: repoaudit.FixEffortEstimate{
				LOCMin: 20, LOCMax: 80, Class: "medium", Rationale: "Invariant spans units.",
			},
		},
	}
}

func repositoryReviewBackfillStructured(
	t *testing.T,
	inspected []repoaudit.FileRef,
	findings []repoaudit.FindingCandidate,
) map[string]any {
	t.Helper()
	data, err := json.Marshal(findings)
	if err != nil {
		t.Fatal(err)
	}
	var rawFindings []map[string]any
	if err := json.Unmarshal(data, &rawFindings); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(inspected))
	for _, file := range inspected {
		paths = append(paths, file.Path)
	}
	return map[string]any{
		"summary": "retained review", "reviewedFiles": paths,
		"findings": rawFindings, "residualRisks": []string{},
	}
}

func repositoryReviewBackfillFileMaps(files []repoaudit.FileRef) []map[string]any {
	out := make([]map[string]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"path": file.Path, "fileHash": file.BlobSHA, "sizeBytes": file.SizeBytes,
			"category": file.Category, "mode": file.Mode, "contentComplete": true,
		})
	}
	return out
}

func TestRepositoryReviewLegacyCampaignBackfillRecoversLiveLikeExactEvidence(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 27, repositoryReviewBackfillRunSpec{
		inspected: func() []int {
			values := make([]int, 27)
			for index := range values {
				values[index] = index
			}
			return values
		}(),
		occurrences: 87,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID, fixture.runStore,
	)
	if err != nil || !prepared.Available || !prepared.Exact ||
		prepared.InspectedFiles != 27 || prepared.CompletedFiles != 0 ||
		prepared.FindingOccurrences != 87 || len(prepared.Request.Runs) != 1 {
		t.Fatalf("prepared backfill = %#v err=%v", prepared, err)
	}
	if len(prepared.Request.Coverage.Paths) != 27 {
		t.Fatalf("coverage paths = %d, want 27", len(prepared.Request.Coverage.Paths))
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRestoresFiftyThreeAssignmentCredits(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 27, repositoryReviewBackfillRunSpec{
		inspected: func() []int {
			values := make([]int, 27)
			for index := range values {
				values[index] = index
			}
			return values
		}(),
	})
	runID := fixture.automation.RunIDs[0]
	run := fixture.runs[runID]
	review := run.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]map[string]any)
	firstScope := children[0]["scope"].([]map[string]any)
	secondScope := append([]map[string]any(nil), firstScope...)
	secondPaths := make([]string, 0, 26)
	for _, file := range secondScope[:26] {
		secondPaths = append(secondPaths, file["path"].(string))
	}
	children[1] = map[string]any{
		"label": "security", "required": true, "valid": true,
		"tasks": []string{workflows.RepositoryBugFinderFocuses()[1].Task},
		"scope": secondScope,
		"structured": map[string]any{
			"summary": "second retained focus", "reviewedFiles": secondPaths,
			"findings": []map[string]any{}, "residualRisks": []string{},
		},
		"text": "validated", "model": map[string]any{"selected": "review-a"},
	}
	review.Outputs["managed_children"] = children
	run.Steps["find_bugs/review"] = review

	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || !prepared.Available || !prepared.Exact || prepared.InspectedFiles != 27 ||
		prepared.CompletedFiles != 0 {
		t.Fatalf("prepared 53-credit recovery = %#v err=%v", prepared, err)
	}
	progress := repoaudit.CurrentCampaignAssignmentProgress(repoaudit.RepositoryState{
		CurrentCampaign: &prepared.Request.Coverage,
	}, campaignID)
	if progress.Completed != 53 || progress.Total != 108 {
		t.Fatalf("recovered assignment progress = %#v", progress)
	}
	_, prepared, err = installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.store.PlanAssignmentsForCampaign(
		t.Context(), fixture.automation.Repository,
		prepared.Request.Coverage.CommitSHA,
		prepared.Request.Coverage.InventoryHash,
		prepared.Request.Coverage.ProfileHash,
		campaignID,
		prepared.Request.Coverage.AssignmentCatalog,
		prepared.Request.SelectedScope,
		false,
		27,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	missingPairs := 0
	for _, assignmentPlan := range plan.AssignmentPlans {
		missingPairs += len(assignmentPlan.Files)
	}
	if missingPairs != 55 {
		t.Fatalf("missing assignment pairs = %d, want 55; plans=%#v", missingPairs, plan.AssignmentPlans)
	}
	if got := repoaudit.CurrentCampaignAssignmentProgress(applied, campaignID).Completed; got != 53 {
		t.Fatalf("applied assignment credits = %d, want 53", got)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRecoversMultiProfileLiveShape(t *testing.T) {
	const fileCount = 444
	specs := make([]repositoryReviewBackfillRunSpec, 19)
	for index := range specs {
		selectedCount := 301 + index*65/18
		specs[index].selected = make([]int, selectedCount)
		for pathIndex := range selectedCount {
			specs[index].selected[pathIndex] = pathIndex
		}
		specs[index].requiredChildren = 4
	}
	// Two 366-file scopes differ by one path, producing the observed 367 union.
	specs[17].selected = append(specs[17].selected, 365)
	specs[18].selected = append([]int(nil), specs[18].selected[:365]...)
	specs[18].selected = append(specs[18].selected, 366)
	specs[0].inspected = make([]int, 24)
	for index := range specs[0].inspected {
		specs[0].inspected[index] = index
	}
	specs[0].occurrences = 87
	for index, pathIndex := range []int{0, 0, 1, 2} {
		specs[index+1].inspected = []int{pathIndex}
		specs[index+1].occurrences = 1
	}
	for index, pathIndex := range []int{301, 305, 310} {
		specs[index+5].inspected = []int{pathIndex}
	}
	fixture := newRepositoryReviewBackfillFixture(t, fileCount, specs...)

	// Model the retained ledger's corroborated IDs: 91 per-run references map
	// to 87 immutable occurrences, and repeated occurrences carry contexts from
	// each original run/profile.
	removed := make(map[string]struct{})
	targetIndexes := []int{0, 0, 1, 2}
	for offset, targetIndex := range targetIndexes {
		runIndex := offset + 1
		extraID := fixture.state.Runs[runIndex].FindingIDs[0]
		target := &fixture.state.Findings[targetIndex]
		var extra repoaudit.Finding
		for _, finding := range fixture.state.Findings {
			if finding.ID == extraID {
				extra = finding
				break
			}
		}
		target.ContextIDs = append(target.ContextIDs, extra.ContextIDs...)
		target.Observations = append(target.Observations, extra.Observations...)
		target.ObservationCount = len(target.Observations)
		fixture.state.Runs[runIndex].FindingIDs = []string{target.ID}
		runID := fixture.state.Runs[runIndex].ID
		fixture.runs[runID].Steps["find_bugs/record"] = workflows.StepExecution{
			ID: "record", Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"run": fixture.state.Runs[runIndex]},
		}
		removed[extraID] = struct{}{}
	}
	retainedFindings := fixture.state.Findings[:0]
	for _, finding := range fixture.state.Findings {
		if _, drop := removed[finding.ID]; !drop {
			retainedFindings = append(retainedFindings, finding)
		}
	}
	fixture.state.Findings = retainedFindings

	updated, err := fixture.store.UpdateAutomation(
		t.Context(), fixture.automation.ID, fixture.automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			for index := range 4 {
				candidate.RunIDs = append(candidate.RunIDs, fmt.Sprintf("wr_failed_%d", index))
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.automation = updated
	for index := range 4 {
		runID := fmt.Sprintf("wr_failed_%d", index)
		completed := fixture.automation.StartedAt.Add(time.Duration(30+index) * time.Minute)
		failed := &workflows.Run{
			ID: runID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
			Status: workflows.RunStatusFailed, CreatedAt: completed.Add(-time.Minute), CompletedAt: &completed,
		}
		if createErr := fixture.runStore.CreateRun(t.Context(), failed); createErr != nil {
			t.Fatal(createErr)
		}
		fixture.runs[runID] = failed
	}

	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), repositoryReviewBackfillLoader{
			runs: fixture.runs, err: map[string]error{},
		},
	)
	if err != nil || !prepared.Available || !prepared.Exact ||
		prepared.Request.Coverage.SelectedFiles != 367 ||
		prepared.InspectedFiles != 27 || prepared.CompletedFiles != 0 ||
		prepared.FindingOccurrences != 87 || len(prepared.Request.Runs) != 19 ||
		len(prepared.Request.FindingIDs) != 87 || len(prepared.ScopeSelection.CandidateIDs) != 367 ||
		prepared.ScopePlan.Counts.TotalFiles != 444 || prepared.ScopePlan.Counts.SelectedFiles != 367 {
		t.Fatalf(
			"live-shaped recovery available=%v exact=%v paths=%d inspected=%d completed=%d "+
				"occurrences=%d runs=%d/%d contexts=%d findings=%d selection=%d counts=%#v err=%v",
			prepared.Available, prepared.Exact, prepared.Request.Coverage.SelectedFiles,
			prepared.InspectedFiles, prepared.CompletedFiles, prepared.FindingOccurrences,
			prepared.RecoveredRuns, prepared.ExpectedRuns, prepared.RecoveredContexts,
			prepared.RecoveredFindings, len(prepared.ScopeSelection.CandidateIDs),
			prepared.ScopePlan.Counts, err,
		)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillCountsZeroFindingAcknowledgement(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{1}, occurrences: 0,
	})
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
	)
	if err != nil || !prepared.Exact || prepared.InspectedFiles != 1 ||
		prepared.CompletedFiles != 0 || prepared.FindingOccurrences != 0 {
		t.Fatalf("zero-finding acknowledgement = %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillCanonicalizesAutomationRepository(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	automation := fixture.automation
	automation.Repository = "https://github.com/owner/backfill.git"
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
	)
	if err != nil || !prepared.Available || !prepared.Exact || prepared.FindingOccurrences != 1 {
		t.Fatalf("canonical identity recovery = %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillUsesResolvedProfileClamp(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	fixture.automation.MaxContentBytes = 512 << 10
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "model-graph", AccountRef: "resolved-account",
		ReviewerModels: []string{"resolved-reviewer"}, MaxContentBytes: 282624,
	}
	runID := fixture.automation.RunIDs[0]
	fixture.runs[runID].Steps["find_bugs/plan"].Outputs["accountRef"] = resolved.AccountRef
	fixture.runs[runID].Steps["find_bugs/plan"].Outputs["reviewerModels"] = resolved.ReviewerModels
	fixture.runs[runID].Steps["find_bugs/plan"].Outputs["maxContentBytes"] = resolved.MaxContentBytes
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), repositoryReviewBackfillLoader{
			runs: fixture.runs, err: map[string]error{},
		}, resolved,
	)
	if err != nil {
		t.Fatalf("resolved recovery = %#v err=%v", prepared, err)
	}
	scopePolicy, _ := json.Marshal(fixture.automation.ScopePolicy)
	want, err := workflows.RepositoryBugFinderProfileHash(
		workflows.NewRepositoryBugFinderProfileHashInput(
			resolved.AccountRef, fixture.automation.Target, fixture.automation.ReviewFocus,
			string(scopePolicy), prepared.ScopePlan.Hash,
			strings.Join(repositoryReviewExecutionModels(fixture.automation), ","),
			resolved.Revision, resolved.ReviewerModels, resolved.IncludeDefaultReviewer,
			int64(resolved.MaxContentBytes),
		),
	)
	if err != nil || prepared.Request.Coverage.ProfileHash != want {
		t.Fatalf("resolved profile hash = %q, want %q err=%v", prepared.Request.Coverage.ProfileHash, want, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillGatesCompletionOnResolvedRevision(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0, 1}, complete: true, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	compatible, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || !compatible.Available || compatible.InspectedFiles != 2 ||
		compatible.CompletedFiles != 2 {
		t.Fatalf("compatible completion = %#v err=%v", compatible, err)
	}
	drifted, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
		workflows.RepositoryReviewModelProfile{
			Revision: "different-model-graph", AccountRef: fixture.automation.EffectiveAccountRef,
			ReviewerModels:  fixture.automation.ReviewerModels,
			MaxContentBytes: int(fixture.automation.MaxContentBytes),
		},
	)
	if err != nil || !drifted.Available || !drifted.Exact || drifted.InspectedFiles != 0 ||
		drifted.CompletedFiles != 0 || drifted.FindingOccurrences != 1 ||
		len(drifted.Request.FindingIDs) != 1 || len(drifted.Request.ContextIDs) != 1 {
		t.Fatalf("revision-drift completion = %#v err=%v", drifted, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRecoversFreezeUnsupportedFiles(t *testing.T) {
	for name, spec := range map[string]repositoryReviewBackfillRunSpec{
		"mixed":        {inspected: []int{0}, unsupported: []int{2}},
		"all terminal": {unsupported: []int{0, 1, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 3, spec)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
			)
			wantInspected := len(spec.inspected)
			if err != nil || !prepared.Available || !prepared.Exact ||
				prepared.InspectedFiles != wantInspected ||
				prepared.UnsupportedFiles != len(spec.unsupported) {
				t.Fatalf("unsupported recovery = %#v err=%v", prepared, err)
			}
		})
	}
}

type repositoryReviewBackfillLoader struct {
	runs map[string]*workflows.Run
	err  map[string]error
}

func (l repositoryReviewBackfillLoader) GetRun(
	ctx context.Context,
	runID string,
) (*workflows.Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.err[runID]; err != nil {
		return nil, err
	}
	if run := l.runs[runID]; run != nil {
		data, _ := json.Marshal(run)
		var clone workflows.Run
		_ = json.Unmarshal(data, &clone)
		return &clone, nil
	}
	return nil, os.ErrNotExist
}

func TestRepositoryReviewLegacyCampaignBackfillFailuresRemainInexactNotZero(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 3,
		repositoryReviewBackfillRunSpec{inspected: []int{0}, occurrences: 1},
		repositoryReviewBackfillRunSpec{inspected: []int{1}, occurrences: 1},
	)
	secondID := fixture.automation.RunIDs[1]
	for name, mutate := range map[string]func(*repositoryReviewBackfillLoader, *repoaudit.RepositoryState){
		"missing workflow": func(loader *repositoryReviewBackfillLoader, _ *repoaudit.RepositoryState) {
			loader.err[secondID] = os.ErrNotExist
		},
		"corrupt workflow": func(loader *repositoryReviewBackfillLoader, _ *repoaudit.RepositoryState) {
			loader.runs[secondID].Steps["find_bugs/review"] = workflows.StepExecution{
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"managed_children": "corrupt"},
			}
		},
		"truncated ledger": func(_ *repositoryReviewBackfillLoader, state *repoaudit.RepositoryState) {
			state.Runs = state.Runs[:1]
		},
	} {
		t.Run(name, func(t *testing.T) {
			loader := repositoryReviewBackfillLoader{
				runs: make(map[string]*workflows.Run, len(fixture.runs)), err: make(map[string]error),
			}
			for id, run := range fixture.runs {
				data, _ := json.Marshal(run)
				var clone workflows.Run
				_ = json.Unmarshal(data, &clone)
				loader.runs[id] = &clone
			}
			state := fixture.state
			state.Runs = append([]repoaudit.ReviewRun(nil), fixture.state.Runs...)
			mutate(&loader, &state)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, state,
				repoaudit.NewRepositoryReviewCampaignID(), loader,
			)
			if err != nil || prepared.Available || prepared.Exact {
				t.Fatalf("inexact lower bound = %#v err=%v", prepared, err)
			}
		})
	}
}

func TestRepositoryReviewLegacyCampaignBackfillIdempotentCASAndCancellation(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID, fixture.runStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	replayedInstall, replayedPrepared, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil || replayedInstall.Version != installed.Version ||
		replayedPrepared.AutomationVersion != prepared.AutomationVersion {
		t.Fatalf("install replay = %#v %#v err=%v", replayedInstall, replayedPrepared, err)
	}
	first, err := applyRepositoryReviewLegacyCampaignBackfill(t.Context(), fixture.store, prepared)
	if err != nil {
		t.Fatal(err)
	}
	second, err := applyRepositoryReviewLegacyCampaignBackfill(t.Context(), fixture.store, prepared)
	if err != nil || second.Version != first.Version || second.ReviewVersion != first.ReviewVersion ||
		second.CurrentCampaign == nil || !second.CurrentCampaign.Exact ||
		second.Runs[0].CampaignID != campaignID || second.Findings[0].CampaignID != campaignID {
		t.Fatalf("idempotent replay = %#v err=%v", second, err)
	}
	reloadedAutomation, found, err := fixture.store.GetAutomation(t.Context(), fixture.automation.ID)
	if err != nil || !found {
		t.Fatalf("reload automation found=%v err=%v", found, err)
	}
	reloadedState, found, err := fixture.store.ResolveRepositoryState(
		reloadedAutomation.Repository, reloadedAutomation.RunIDs,
	)
	if err != nil || !found {
		t.Fatalf("reload state found=%v err=%v", found, err)
	}
	rescanned, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), reloadedAutomation, reloadedState, campaignID,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || !rescanned.Available || !rescanned.Exact ||
		rescanned.Request.Coverage.ScopeDigest != prepared.Request.Coverage.ScopeDigest ||
		!repositoryReviewScopePlansEqual(rescanned.ScopePlan, prepared.ScopePlan) {
		t.Fatalf("fresh-process rescan = %#v err=%v", rescanned, err)
	}
	replayedState, err := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, rescanned,
	)
	if err != nil || replayedState.Version != second.Version ||
		replayedState.ReviewVersion != second.ReviewVersion {
		t.Fatalf("fresh-process apply replay = %#v err=%v", replayedState, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareRepositoryReviewLegacyCampaignBackfill(
		canceled, fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepare error = %v", err)
	}
	if _, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), repositoryReviewBackfillLoader{
			runs: fixture.runs,
			err:  map[string]error{fixture.automation.RunIDs[0]: context.DeadlineExceeded},
		},
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("loader cancellation error = %v", err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillFencesAutomationRaceAndDelete(t *testing.T) {
	for _, remove := range []bool{false, true} {
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0}, occurrences: 1,
		})
		campaignID := repoaudit.NewRepositoryReviewCampaignID()
		prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.automation, fixture.state, campaignID, fixture.runStore,
		)
		if err != nil {
			t.Fatal(err)
		}
		installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(
			t.Context(), fixture.store, prepared,
		)
		if err != nil {
			t.Fatal(err)
		}
		if remove {
			if deleteErr := fixture.store.DeleteAutomation(
				t.Context(), installed.ID, installed.Version,
			); deleteErr != nil {
				t.Fatal(deleteErr)
			}
		} else {
			_, err = fixture.store.UpdateAutomation(
				t.Context(), installed.ID, installed.Version,
				func(candidate *repoaudit.RepositoryReviewAutomation) error {
					candidate.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
					candidate.CampaignRecoveryPending = false
					candidate.ScopeSelection = nil
					candidate.ScopePlan = repoaudit.RepositoryReviewScopePlan{}
					return nil
				},
			)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := applyRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.store, prepared,
		); err == nil {
			t.Fatalf("remove=%v stale automation apply succeeded", remove)
		}
	}
}

func TestRepositoryReviewLegacyCampaignRecoveryAdapterClearsDurableMarker(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels:  fixture.automation.ReviewerModels,
		MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	updated, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, fixture.workspace, fixture.automation,
		fixture.automation.ResolvedCommitSHA, resolved,
	)
	if err != nil || updated.CampaignID == "" || updated.CampaignRecoveryPending ||
		updated.ScopeSelection == nil || updated.Version <= fixture.automation.Version {
		t.Fatalf("recovered automation = %#v err=%v", updated, err)
	}
	if !updated.Progress.CoverageAvailable || !updated.Progress.CoverageExact ||
		updated.Progress.SelectedFiles != 1 || updated.Progress.InspectedFiles != 1 ||
		updated.Progress.ReviewedFiles != 0 || updated.Progress.RemainingFiles != 1 ||
		updated.Progress.UnsupportedFiles != 0 || updated.Progress.RawFindings != 1 ||
		updated.Progress.DeduplicatedFindings != 0 || updated.Progress.Findings != 0 ||
		updated.Progress.FindingAggregates != 0 || updated.Progress.PendingFindingMappings != 0 {
		t.Fatalf("recovered public metrics = %#v", updated.Progress)
	}
	state, found, err := fixture.store.ResolveRepositoryState(updated.Repository, updated.RunIDs)
	if err != nil || !found || state.CurrentCampaign == nil || !state.CurrentCampaign.Exact {
		t.Fatalf("recovered state = %#v found=%v err=%v", state, found, err)
	}
}

func TestRepositoryReviewLegacyCampaignFinalizationRejectsMarkerRace(t *testing.T) {
	candidate := &repoaudit.RepositoryReviewAutomation{
		CampaignID: repoaudit.NewRepositoryReviewCampaignID(),
	}
	if err := finalizeRepositoryReviewLegacyCampaign(
		candidate, candidate.CampaignID, repoaudit.RepositoryState{},
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("marker race finalization error=%v", err)
	}
}

func TestRepositoryReviewRecoveredBaselineDoesNotCountAsFirstBatchProgress(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(
		t, 2,
		repositoryReviewBackfillRunSpec{selected: []int{0}, inspected: []int{0}, complete: true},
		repositoryReviewBackfillRunSpec{selected: []int{1}, inspected: []int{1}},
	)
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels:  fixture.automation.ReviewerModels,
		MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	controller := newRepositoryReviewController(nil)
	recovered, err := controller.recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, fixture.workspace, fixture.automation,
		fixture.automation.ResolvedCommitSHA, resolved,
	)
	if err != nil || recovered.Progress.ReviewedFiles != 1 ||
		recovered.Progress.RemainingFiles != 1 || !recovered.Progress.CoverageExact {
		t.Fatalf("recovered baseline = %#v err=%v", recovered.Progress, err)
	}

	runID := "wr_first_zero_progress"
	running, err := fixture.store.UpdateAutomation(
		t.Context(), recovered.ID, recovered.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Status = repoaudit.RepositoryReviewAutomationRunning
			candidate.PauseReason = ""
			candidate.PauseDetail = ""
			candidate.ActiveRunID = runID
			candidate.RunIDs = append(candidate.RunIDs, runID)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[running.ID] = &repositoryReviewActiveRun{runID: runID, store: fixture.store}
	controller.mu.Unlock()
	controller.finishAutomationRun(
		running.ID,
		runID,
		&workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 1, "reviewedFiles": 0},
		},
		nil,
		true,
		&workflows.Run{Steps: map[string]workflows.StepExecution{
			"find_bugs/record": {
				Status: workflows.RunStatusSucceeded,
				Outputs: map[string]any{"run": map[string]any{
					"remaining_files": 1, "reviewed_files": 0, "unsupported_files": 0,
				}},
			},
		}},
	)
	paused, found, err := fixture.store.GetAutomation(t.Context(), running.ID)
	if err != nil || !found || paused.Status != repoaudit.RepositoryReviewAutomationPaused ||
		paused.PauseReason != repoaudit.RepositoryReviewPauseNoProgress ||
		paused.Progress.ReviewedFiles != 1 || paused.Progress.RemainingFiles != 1 ||
		paused.Progress.CompletedBatches != running.Progress.CompletedBatches+1 {
		t.Fatalf("first recovered batch = %#v found=%v err=%v", paused, found, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRejectsStaleCAS(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	authorizeRepositoryReviewBackfillCampaign(t, &fixture, campaignID)
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		campaignID, fixture.runStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: fixture.state.Repository, CampaignID: repoaudit.NewRepositoryReviewCampaignID(),
		CommitSHA:             prepared.Request.Coverage.CommitSHA,
		ExpectedReviewVersion: fixture.state.ReviewVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, prepared,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillBoundsAndNoContextInference(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	bounded := fixture.automation
	for len(bounded.RunIDs) < repositoryReviewLegacyBackfillMaxRuns {
		bounded.RunIDs = append(bounded.RunIDs, fmt.Sprintf("wr_noop_%04d", len(bounded.RunIDs)))
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), bounded, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), repositoryReviewBackfillLoader{
			runs: fixture.runs, err: map[string]error{},
		},
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("bounded recovery = %#v err=%v", prepared, err)
	}
	cappedState := fixture.state
	cappedState.Runs = append([]repoaudit.ReviewRun(nil), fixture.state.Runs...)
	for len(cappedState.Runs) < repositoryReviewLegacyBackfillMaxRuns {
		cappedState.Runs = append(cappedState.Runs, repoaudit.ReviewRun{
			ID: fmt.Sprintf("unrelated_%04d", len(cappedState.Runs)),
		})
	}
	capped, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, cappedState,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
	)
	if err != nil || capped.Available || capped.Exact {
		t.Fatalf("retained-run cap recovery = %#v err=%v", capped, err)
	}

	runID := fixture.automation.RunIDs[0]
	corrupt := repositoryReviewBackfillLoader{
		runs: make(map[string]*workflows.Run), err: make(map[string]error),
	}
	data, _ := json.Marshal(fixture.runs[runID])
	var workflowRun workflows.Run
	_ = json.Unmarshal(data, &workflowRun)
	children := workflowRun.Steps["find_bugs/review"].Outputs["managed_children"].([]any)
	for _, raw := range children {
		child := raw.(map[string]any)
		child["valid"] = false
		delete(child, "structured")
	}
	corrupt.runs[runID] = &workflowRun
	unknown, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), corrupt,
	)
	if err != nil || unknown.Available || unknown.Exact || unknown.InspectedFiles != 0 {
		t.Fatalf("context/sketch inference escaped evidence boundary: %#v err=%v", unknown, err)
	}
}

func TestRepositoryReviewLegacyNonLedgerRunClassificationFailsClosed(t *testing.T) {
	completed := time.Now().UTC()
	base := workflows.Run{
		ID: "run", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusFailed, CompletedAt: &completed,
	}
	for name, mutate := range map[string]func(*workflows.Run){
		"wrong workflow": func(run *workflows.Run) { run.WorkflowRef = "other" },
		"running":        func(run *workflows.Run) { run.Status = workflows.RunStatusRunning },
		"scope succeeded": func(run *workflows.Run) {
			run.Steps = map[string]workflows.StepExecution{
				"find_bugs/scope": {Status: workflows.RunStatusSucceeded},
			}
		},
		"plan succeeded": func(run *workflows.Run) {
			run.Steps = map[string]workflows.StepExecution{
				"find_bugs/plan": {Status: workflows.RunStatusSucceeded},
			}
		},
		"failed review retained child": func(run *workflows.Run) {
			run.Steps = map[string]workflows.StepExecution{
				"find_bugs/review": {
					Status:  workflows.RunStatusFailed,
					Outputs: map[string]any{"managed_children": []map[string]any{{"valid": true}}},
				},
			}
		},
		"succeeded without record": func(run *workflows.Run) {
			run.Status = workflows.RunStatusSucceeded
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			if allowed, _ := repositoryReviewLegacyNonLedgerRunAllowed(&candidate); allowed {
				t.Fatal("ambiguous non-ledger run was accepted")
			}
		})
	}
	if allowed, plan := repositoryReviewLegacyNonLedgerRunAllowed(&base); !allowed || plan != nil {
		t.Fatalf("pre-scope terminal failure = %v, %#v", allowed, plan)
	}
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, complete: true,
	})
	var firstPlan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		repositoryReviewRunStep(fixture.runs[fixture.automation.RunIDs[0]], "plan").Outputs["plan"],
		&firstPlan,
	) {
		t.Fatal("fixture plan was not decodable")
	}
	noopPlan, err := fixture.store.PlanWithProfileLimitAuthoritative(
		t.Context(), firstPlan.Repository, firstPlan.CommitSHA, firstPlan.InventoryHash,
		firstPlan.ProfileHash, firstPlan.PendingFiles, false, 1, true,
	)
	if err != nil || len(noopPlan.PendingFiles) != 0 {
		t.Fatalf("no-op plan = %#v err=%v", noopPlan, err)
	}
	noop := workflows.Run{
		ID: "noop", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, CompletedAt: &completed,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/plan": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"plan": noopPlan, "pendingCount": 0},
			},
			"find_bugs/result": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"run": map[string]any{"remaining_files": 0}},
			},
		},
	}
	if allowed, plan := repositoryReviewLegacyNonLedgerRunAllowed(&noop); !allowed || plan == nil {
		t.Fatalf("durable no-op = %v, %#v", allowed, plan)
	}
	for name, mutate := range map[string]func(*workflows.Run){
		"top-level only": func(run *workflows.Run) {
			result := run.Steps["find_bugs/result"]
			result.Outputs = map[string]any{"remainingFiles": 0}
			run.Steps["find_bugs/result"] = result
		},
		"pending mismatch": func(run *workflows.Run) {
			plan := run.Steps["find_bugs/plan"]
			plan.Outputs["pendingCount"] = 1
			run.Steps["find_bugs/plan"] = plan
		},
		"nonempty deferred": func(run *workflows.Run) {
			planStep := run.Steps["find_bugs/plan"]
			planValue := noopPlan
			planValue.DeferredFiles = append([]repoaudit.FileRef(nil), firstPlan.PendingFiles...)
			planStep.Outputs["plan"] = planValue
			run.Steps["find_bugs/plan"] = planStep
		},
	} {
		t.Run("no-op "+name, func(t *testing.T) {
			encoded, _ := json.Marshal(noop)
			var candidate workflows.Run
			_ = json.Unmarshal(encoded, &candidate)
			mutate(&candidate)
			if allowed, _ := repositoryReviewLegacyNonLedgerRunAllowed(&candidate); allowed {
				t.Fatal("invalid no-op was accepted")
			}
		})
	}
}

func TestRepositoryReviewLegacyFindingProjectionRejectsCorruption(t *testing.T) {
	base := repoaudit.Finding{
		ContextIDs: []string{"context"}, Models: []string{"review"}, ObservationCount: 1,
		Observations: []repoaudit.FindingObservation{{ContextID: "context", Model: "review"}},
	}
	if !repositoryReviewLegacyFindingProjectionValid(base) {
		t.Fatal("valid finding projection was rejected")
	}
	for name, mutate := range map[string]func(*repoaudit.Finding){
		"count": func(finding *repoaudit.Finding) { finding.ObservationCount++ },
		"orphan observation": func(finding *repoaudit.Finding) {
			finding.Observations = append(finding.Observations, repoaudit.FindingObservation{
				ContextID: "orphan", Model: "review",
			})
			finding.ObservationCount++
		},
		"models": func(finding *repoaudit.Finding) { finding.Models = []string{"other"} },
		"duplicate context": func(finding *repoaudit.Finding) {
			finding.ContextIDs = append(finding.ContextIDs, "context")
		},
		"duplicate observation": func(finding *repoaudit.Finding) {
			finding.Observations = append(finding.Observations, finding.Observations[0])
			finding.ObservationCount++
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.ContextIDs = append([]string(nil), base.ContextIDs...)
			candidate.Models = append([]string(nil), base.Models...)
			candidate.Observations = append([]repoaudit.FindingObservation(nil), base.Observations...)
			mutate(&candidate)
			if repositoryReviewLegacyFindingProjectionValid(candidate) {
				t.Fatal("corrupt finding projection was accepted")
			}
		})
	}
}

func TestRepositoryReviewLegacyIdentityValidatorsRejectTampering(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	contextRecord := fixture.state.Contexts[0]
	finding := fixture.state.Findings[0]
	candidate := repositoryReviewBackfillFinding(finding.File, 0, 0)
	if !repoaudit.ValidateRepositoryReviewLegacyContextIdentity(contextRecord) ||
		!repoaudit.ValidateRepositoryReviewLegacyFindingIdentity(finding, contextRecord, candidate) {
		t.Fatal("valid legacy identities were rejected")
	}
	tamperedContext := contextRecord
	tamperedContext.ID += "x"
	if repoaudit.ValidateRepositoryReviewLegacyContextIdentity(tamperedContext) {
		t.Fatal("tampered context identity was accepted")
	}
	for name, mutate := range map[string]func(*repoaudit.Finding){
		"id":          func(value *repoaudit.Finding) { value.ID += "x" },
		"fingerprint": func(value *repoaudit.Finding) { value.Fingerprint += "x" },
		"title":       func(value *repoaudit.Finding) { value.Title += "x" },
	} {
		t.Run(name, func(t *testing.T) {
			tampered := finding
			mutate(&tampered)
			if repoaudit.ValidateRepositoryReviewLegacyFindingIdentity(
				tampered, contextRecord, candidate,
			) {
				t.Fatal("tampered finding identity was accepted")
			}
		})
	}
	severityTamper := fixture.state
	severityTamper.Findings = append([]repoaudit.Finding(nil), fixture.state.Findings...)
	severityTamper.Findings[0].Severity = "low"
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, severityTamper,
		repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("severity-tampered recovery = %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRejectsRunProjectionAndUnsupportedDrift(t *testing.T) {
	for _, name := range []string{"models", "rejected findings", "extra unsupported"} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
				inspected: []int{0}, occurrences: 0,
			})
			runID := fixture.automation.RunIDs[0]
			workflowRun := fixture.runs[runID]
			switch name {
			case "models":
				fixture.state.Runs[0].Models = []string{"forged"}
				workflowRun.Steps["find_bugs/record"] = workflows.StepExecution{
					Status:  workflows.RunStatusSucceeded,
					Outputs: map[string]any{"run": fixture.state.Runs[0]},
				}
			case "rejected findings":
				fixture.state.Runs[0].RejectedFindings = 1
				workflowRun.Steps["find_bugs/record"] = workflows.StepExecution{
					Status:  workflows.RunStatusSucceeded,
					Outputs: map[string]any{"run": fixture.state.Runs[0]},
				}
			case "extra unsupported":
				review := workflowRun.Steps["find_bugs/review"]
				children := review.Outputs["managed_children"].([]map[string]any)
				scopeFile := maps.Clone(children[0]["scope"].([]map[string]any)[0])
				scopeFile["contentComplete"] = false
				scopeFile["contentUnavailable"] = "binary"
				children = append(children, map[string]any{
					"required": false, "valid": false, "scope": []map[string]any{scopeFile},
				})
				review.Outputs["managed_children"] = children
				workflowRun.Steps["find_bugs/review"] = review
			}
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(),
				repositoryReviewBackfillLoader{
					runs: fixture.runs, err: map[string]error{},
				},
			)
			if err != nil || prepared.Available || prepared.Exact {
				t.Fatalf("corrupt recovery = %#v err=%v", prepared, err)
			}
		})
	}
}

func TestRepositoryReviewLegacyCampaignBackfillRejectsTamperedUnreviewedUnsupportedFile(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 0,
	})
	runID := fixture.automation.RunIDs[0]
	workflowRun := fixture.runs[runID]
	review := workflowRun.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]map[string]any)
	scope := children[0]["scope"].([]map[string]any)
	forgedUnsupported := maps.Clone(scope[1])
	forgedUnsupported["contentComplete"] = false
	forgedUnsupported["contentUnavailable"] = "binary"
	children = append(children, map[string]any{
		"required": false, "valid": false, "scope": []map[string]any{forgedUnsupported},
	})
	review.Outputs["managed_children"] = children
	workflowRun.Steps["find_bugs/review"] = review

	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("tampered unsupported recovery = %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyCampaignRecoveryCrashPhasesAreIdempotent(t *testing.T) {
	for _, phase := range []string{"install", "begin", "reconcile"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
				inspected: []int{0}, occurrences: 1,
			})
			resolved := workflows.RepositoryReviewModelProfile{
				Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
				ReviewerModels:  fixture.automation.ReviewerModels,
				MaxContentBytes: int(fixture.automation.MaxContentBytes),
			}
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore, resolved,
			)
			if err != nil {
				t.Fatal(err)
			}
			installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(
				t.Context(), fixture.store, prepared,
			)
			if err != nil || !installed.CampaignRecoveryPending {
				t.Fatalf("install = %#v err=%v", installed, err)
			}
			if phase == "begin" {
				if _, beginErr := fixture.store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
					Repository: prepared.Request.Repository, CampaignID: prepared.Request.Coverage.ID,
					CommitSHA:             prepared.Request.Coverage.CommitSHA,
					ExpectedReviewVersion: prepared.Request.ExpectedReviewVersion,
				}); beginErr != nil {
					t.Fatal(beginErr)
				}
			}
			if phase == "reconcile" {
				if _, applyErr := applyRepositoryReviewLegacyCampaignBackfill(
					t.Context(), fixture.store, prepared,
				); applyErr != nil {
					t.Fatal(applyErr)
				}
			}
			current, found, err := fixture.store.GetAutomation(t.Context(), installed.ID)
			if err != nil || !found {
				t.Fatalf("reload found=%v err=%v", found, err)
			}
			final, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
				t.Context(), fixture.store, fixture.workspace, current,
				fixture.automation.ResolvedCommitSHA, resolved,
			)
			if err != nil || final.CampaignRecoveryPending || final.CampaignID == "" {
				t.Fatalf("phase %s final=%#v err=%v", phase, final, err)
			}
			responseLost, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
				t.Context(), fixture.store, fixture.workspace, final,
				fixture.automation.ResolvedCommitSHA, resolved,
			)
			if err != nil || responseLost.Version != final.Version {
				t.Fatalf("clear replay=%#v err=%v", responseLost, err)
			}
		})
	}
}
