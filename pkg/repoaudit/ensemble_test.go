package repoaudit

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestStorePlanInvalidatesUnchangedBlobWhenReviewProfileChanges(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "a", 120)
	plan, err := store.PlanWithProfile(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{file}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "profile-run",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}}},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	next, err := store.PlanWithProfile(
		context.Background(), "owner/repo", "commit-b", "inventory-b", "profile-b",
		[]FileRef{file}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.PendingFiles, []FileRef{file}) {
		t.Fatalf("pending=%#v, want unchanged blob re-reviewed for new profile", next.PendingFiles)
	}
}

func TestFinalizeNoopPlanRecordsNewestAuthoritativeCommit(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "a", 120)
	seed, err := store.PlanWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := store.Record(context.Background(), RecordRequest{
		Plan: seed, RunID: "noop-seed",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", "commit-b", "inventory-b", "profile-a",
		[]FileRef{file}, false, 1, true,
	)
	if err != nil || len(plan.PendingFiles) != 0 {
		t.Fatalf("noop plan=%#v err=%v", plan, err)
	}
	finalized, err := store.FinalizeNoopPlan(plan, 7)
	if err != nil || finalized.LastCommitSHA != "commit-b" ||
		finalized.ReviewVersion != seeded.State.ReviewVersion+1 ||
		finalized.LastExcludedFiles != 7 || Summarize(finalized).ExcludedFileCount != 7 {
		t.Fatalf("finalized=%#v err=%v", finalized, err)
	}
}

func TestFindingContextsRemainImmutableAcrossForceRuns(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "b", 120)
	candidate := FindingCandidate{
		Severity: "high", Title: "Lost update", Symbol: "Save", File: file.Path,
		Evidence: "No version fence.", Impact: "Data loss.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
	}
	first, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Record(context.Background(), RecordRequest{
		Plan: first, RunID: "immutable-context-first",
		Observations: []Observation{{
			Model: "review-a", ScopeFiles: []FileRef{file}, RawDigest: "sha256:first",
			Findings: []FindingCandidate{candidate},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, true)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.Record(context.Background(), RecordRequest{
		Plan: second, RunID: "immutable-context-second",
		Observations: []Observation{{
			Model: "review-a", ScopeFiles: []FileRef{file}, RawDigest: "sha256:second",
			Findings: []FindingCandidate{candidate},
		}},
	})
	if err != nil || len(repeated.State.Contexts) != 2 ||
		repeated.State.Contexts[0].ID == repeated.State.Contexts[1].ID ||
		repeated.State.Contexts[0].RunID != "immutable-context-first" ||
		repeated.State.Contexts[1].RunID != "immutable-context-second" ||
		len(repeated.State.Findings) != 2 || len(repeated.State.Findings[0].Observations) != 1 ||
		len(repeated.State.Findings[1].Observations) != 1 ||
		repeated.State.Findings[0].ID == repeated.State.Findings[1].ID ||
		initial.State.Contexts[0].ID != repeated.State.Contexts[0].ID {
		t.Fatalf("immutable context history=%#v err=%v", repeated.State, err)
	}
}

func TestStoreMergesCorroboratingModelParaphrasesInSameBlobContext(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "b", 240)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	line, nearby := 42, 44
	validation := Validation{Status: "confirmed", Summary: "two concurrent writers reproduce the lost update"}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "ensemble-run",
		Observations: []Observation{
			{
				Model: "openai/gpt-5.4", ModelAlias: "review-a", Account: "openai-work",
				ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
					Severity:   "high",
					Title:      "Concurrent writers lose updates",
					Symbol:     "Save",
					File:       file.Path,
					Line:       &line,
					Message:    "A stale writer overwrites a newer value.",
					Evidence:   "Both writers save without a version fence.",
					Impact:     "A successful update disappears.",
					Validation: validation,
				}},
			},
			{
				Model: "anthropic/claude-4.6", ModelAlias: "review-b", Account: "anthropic-work",
				ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
					Severity:   "high",
					Title:      "Concurrent write loses an update",
					Symbol:     "Save",
					File:       file.Path,
					Line:       &nearby,
					Message:    "The stale writer can overwrite the newer stored value.",
					Evidence:   "The two writers save with no version fence.",
					Impact:     "A completed update is lost.",
					Validation: validation,
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.Findings) != 2 || len(result.State.RawFindings) != 2 ||
		len(result.State.DeduplicatedFindings) != 2 {
		t.Fatalf("candidate-limit-zero compatibility findings=%#v", result.State.Findings)
	}
	first, second := result.State.Findings[0], result.State.Findings[1]
	if !reflect.DeepEqual(first.Models, []string{"review-a"}) ||
		!reflect.DeepEqual(second.Models, []string{"review-b"}) ||
		first.Evidence == second.Evidence ||
		len(first.Observations) != 1 || len(second.Observations) != 1 {
		t.Fatalf("independent raw diagnoses were not retained: %#v", result.State.Findings)
	}
	firstObservation, secondObservation := first.Observations[0], second.Observations[0]
	if firstObservation.Model != "openai/gpt-5.4" ||
		firstObservation.ModelAlias != "review-a" ||
		firstObservation.Account != "openai-work" ||
		secondObservation.Model != "anthropic/claude-4.6" ||
		secondObservation.ModelAlias != "review-b" ||
		secondObservation.Account != "anthropic-work" {
		t.Fatalf("raw finding provenance was not retained: %#v", result.State.Findings)
	}
	if result.State.Contexts[0].ProfileHash != plan.ProfileHash {
		t.Fatalf("context profile=%q want=%q", result.State.Contexts[0].ProfileHash, plan.ProfileHash)
	}
}

func TestStoreDoesNotCountDuplicateCandidateTwiceWithinOneResponse(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "d", 120)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "Lost update", Symbol: "Save", File: file.Path,
		Evidence: "Save has no version fence.", Impact: "Data loss.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "duplicate-candidate",
		Observations: []Observation{{
			Model: "review-a", ScopeFiles: []FileRef{file},
			Findings: []FindingCandidate{candidate, candidate},
		}},
	})
	if err != nil || len(result.State.Findings) != 2 || len(result.State.RawFindings) != 2 ||
		len(result.State.DeduplicatedFindings) != 2 {
		t.Fatalf("duplicate response finding=%#v err=%v", result.State.Findings, err)
	}
}

func TestStoreBoundsFindingContextHistoryWithObservationVariants(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "e", 120)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "Lost update", Symbol: "Save", File: file.Path,
		Evidence: "Save has no version fence.", Impact: "Data loss.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
	}
	observations := make([]Observation, 65)
	for index := range observations {
		observations[index] = Observation{
			Model: "review-a", Reviewer: fmt.Sprintf("reviewer-%d", index),
			RawDigest: fmt.Sprintf("sha256:%d", index), ScopeFiles: []FileRef{file},
			Findings: []FindingCandidate{candidate},
		}
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "bounded-context-history", Observations: observations,
	})
	if err != nil || len(result.State.Findings) != 65 || len(result.State.RawFindings) != 65 ||
		len(result.State.DeduplicatedFindings) != 65 || len(result.State.Contexts) != 65 {
		t.Fatalf(
			"bounded context history finding=%#v contexts=%d err=%v",
			result.State.Findings,
			len(result.State.Contexts),
			err,
		)
	}
}

func TestStoreDoesNotCollapseNearbyDistinctAuthorizationFindings(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("handlers.go", "f", 100)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	readLine, deleteLine := 40, 42
	validation := Validation{Status: "confirmed", Summary: "Traced unauthorized request"}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "distinct-nearby",
		Observations: []Observation{
			{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{
				{
					Severity:   "high",
					Title:      "Missing authorization check in read handler",
					Symbol:     "Read",
					File:       file.Path,
					Line:       &readLine,
					Message:    "Read returns private records without checking ownership.",
					Evidence:   "Read calls load directly.",
					Impact:     "Private data leaks.",
					Validation: validation,
				},
			}},
			{Model: "review-b", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{
				{
					Severity:   "critical",
					Title:      "Missing authorization check in delete handler",
					Symbol:     "Delete",
					File:       file.Path,
					Line:       &deleteLine,
					Message:    "Delete removes records without checking ownership.",
					Evidence:   "Delete calls remove directly.",
					Impact:     "Attackers delete other users' data.",
					Validation: validation,
				},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.Findings) != 2 {
		t.Fatalf("distinct nearby bugs collapsed: %#v", result.State.Findings)
	}
}

func TestStoreDoesNotCollapseIdenticalCopiedBlobFindingsAcrossPaths(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	left := repositoryAuditTestFile("left/service.go", "9", 100)
	right := left
	right.Path = "right/service.go"
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{left, right},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate := func(file FileRef) FindingCandidate {
		return FindingCandidate{
			Severity: "high", Title: "Copied handler loses updates", Symbol: "Save", File: file.Path,
			Evidence: "Save lacks a version fence.", Impact: "Updates disappear.",
			Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
		}
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "copied-blobs",
		Observations: []Observation{
			{Model: "review-a", ScopeFiles: []FileRef{left}, Findings: []FindingCandidate{candidate(left)}},
			{Model: "review-b", ScopeFiles: []FileRef{right}, Findings: []FindingCandidate{candidate(right)}},
		},
	})
	if err != nil || len(result.State.Findings) != 2 {
		t.Fatalf("copied blob findings=%#v err=%v", result.State.Findings, err)
	}
}

func TestStoreSemanticCorroborationUsesWorstSeverity(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "0", 100)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	line := 20
	base := FindingCandidate{
		Severity: "low", Title: "Concurrent writers lose updates", Symbol: "Save", File: file.Path, Line: &line,
		Message: "A stale writer overwrites a newer value.", Evidence: "Both writers save without a version fence.",
		Impact:     "A completed update disappears.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
	}
	critical := base
	critical.Severity = "critical"
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "severity-corroboration",
		Observations: []Observation{
			{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{base}},
			{Model: "review-b", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{critical}},
		},
	})
	if err != nil || len(result.State.Findings) != 2 ||
		result.State.Findings[0].Severity != "low" || result.State.Findings[1].Severity != "critical" {
		t.Fatalf("severity corroboration=%#v err=%v", result.State.Findings, err)
	}
}

func TestStorePreparesEditableIssueFromSelectedFindingSnapshot(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "c", 120)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "issue-run",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
			Severity: "high", Title: "Lost update", File: file.Path,
			Evidence: "A write is not fenced.", Impact: "Data disappears.",
			Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := completeRepositoryAuditTestMapping(
		t, store, result.State, result.State.Findings[0].ID,
	)
	updated, draft, err := store.PrepareIssue(IssueDraftRequest{
		Repository: "owner/repo", FindingIDs: []string{result.State.Findings[0].ID},
		ExpectedVersion: mapped.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.State != IssueDraftEditing || !strings.Contains(draft.Body, file.BlobSHA) ||
		!strings.Contains(draft.Body, plan.CommitSHA) || len(updated.IssueDrafts) != 1 {
		t.Fatalf("issue draft=%#v", draft)
	}
	replayedState, replayedDraft, err := store.PrepareIssue(IssueDraftRequest{
		Repository: "owner/repo", FindingIDs: []string{result.State.Findings[0].ID},
		ExpectedVersion: mapped.Version,
	})
	if err != nil || replayedState.Version != updated.Version || replayedDraft.ID != draft.ID ||
		len(replayedState.IssueDrafts) != 1 {
		t.Fatalf("issue replay state=%#v draft=%#v err=%v", replayedState, replayedDraft, err)
	}
}

func TestDefaultIssueTitleBoundsVerboseFindingTitle(t *testing.T) {
	title := defaultIssueTitle([]Finding{{Title: strings.Repeat("é", 300)}})
	if len(title) > 256 || !utf8.ValidString(title) || title == "" {
		t.Fatalf("bounded default issue title bytes=%d valid=%v", len(title), utf8.ValidString(title))
	}
}

func TestDefaultIssueBodyBoundsLargeFindingSelection(t *testing.T) {
	findings := make([]Finding, 200)
	for index := range findings {
		findings[index] = Finding{
			ID: fmt.Sprintf("finding-%03d", index), Title: strings.Repeat("title", 100),
			Severity: "high", CommitSHA: "commit", File: FileRef{Path: "service.go", BlobSHA: strings.Repeat("a", 40)},
			Evidence: strings.Repeat("e", 64<<10), Impact: strings.Repeat("i", 64<<10),
			Validation: Validation{Summary: strings.Repeat("v", 64<<10)},
		}
	}
	body := defaultIssueBody(RepositoryState{Repository: "owner/repo", LastCommitSHA: "commit"}, findings)
	if len(body) > maxIssueDraftBodyBytes || !strings.Contains(body, "omitted here") {
		t.Fatalf("bounded issue body bytes=%d overflow_note=%v", len(body), strings.Contains(body, "omitted here"))
	}
}

func TestStoreIssuePublicationIsIdempotentAndMarksSelectedFindings(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "d", 120)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "post-run",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
			Severity: "high", Title: "Lost update", File: file.Path,
			Evidence: "A write is not fenced.", Impact: "Data disappears.",
			Validation: Validation{Status: "confirmed", Summary: "Reproduced"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := completeRepositoryAuditTestMapping(
		t, store, recorded.State, recorded.State.Findings[0].ID,
	)
	withDraft, draft, err := store.PrepareIssue(IssueDraftRequest{
		Repository: "owner/repo", FindingIDs: []string{recorded.State.Findings[0].ID},
		ExpectedVersion: mapped.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedState, claimed, didClaim, err := store.ClaimIssueDraftPublication(
		"owner/repo", draft.ID, draft.Version,
	)
	if err != nil || !didClaim || claimed.State != IssueDraftPublishing {
		t.Fatalf("claim state=%#v draft=%#v claimed=%v err=%v", claimedState, claimed, didClaim, err)
	}
	postedState, posted, err := store.SetIssueDraftPublication(
		"owner/repo", draft.ID, claimed.Version, IssueDraftPosted,
		"12", "https://github.com/owner/repo/issues/12",
	)
	if err != nil {
		t.Fatal(err)
	}
	if posted.State != IssueDraftPosted || postedState.Findings[0].Status != FindingPosted ||
		postedState.Version != withDraft.Version+2 {
		t.Fatalf("posted state=%#v draft=%#v", postedState, posted)
	}
	replayed, replayedDraft, err := store.SetIssueDraftPublication(
		"owner/repo", draft.ID, draft.Version, IssueDraftPosted,
		"12", "https://github.com/owner/repo/issues/12",
	)
	if err != nil || replayed.Version != postedState.Version || replayedDraft.Version != posted.Version {
		t.Fatalf("idempotent replay state=%#v draft=%#v err=%v", replayed, replayedDraft, err)
	}
}

func TestForcedReviewCampaignAdvancesAcrossBoundedBatches(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := []FileRef{
		repositoryAuditTestFile("a.go", "a", 10),
		repositoryAuditTestFile("b.go", "b", 10),
		repositoryAuditTestFile("c.go", "c", 10),
	}
	baseline, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		files, false, len(files),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: baseline, RunID: "baseline",
		Observations: []Observation{{Model: "review-a", ScopeFiles: files}},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	seen := make([]string, 0, len(files))
	for index := range files {
		batchPlan, planErr := store.PlanWithProfileLimit(
			context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
			files, true, 1,
		)
		if planErr != nil || len(batchPlan.PendingFiles) != 1 {
			t.Fatalf("force plan %d=%#v err=%v", index, batchPlan, planErr)
		}
		seen = append(seen, batchPlan.PendingFiles[0].Path)
		if _, recordErr := store.Record(context.Background(), RecordRequest{
			Plan: batchPlan, RunID: fmt.Sprintf("force-%d", index),
			Observations: []Observation{{Model: "review-a", ScopeFiles: batchPlan.PendingFiles}},
		}); recordErr != nil {
			t.Fatal(recordErr)
		}
	}
	if !reflect.DeepEqual(seen, []string{"a.go", "b.go", "c.go"}) {
		t.Fatalf("forced batches=%#v, want every file once", seen)
	}
	state, _, err := store.Get("owner/repo")
	if err != nil || state.ActiveForceCampaignID != "" {
		t.Fatalf("completed force campaign remained active: %#v err=%v", state, err)
	}
}

func TestForcedReviewCampaignDoesNotRepeatTerminalUnsupportedFile(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	binary := repositoryAuditTestFile("a.bin", "a", 10)
	code := repositoryAuditTestFile("b.go", "b", 10)
	first, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{binary, code}, true, 1,
	)
	if err != nil || len(first.PendingFiles) != 1 || first.PendingFiles[0].Path != binary.Path {
		t.Fatalf("first force plan=%#v err=%v", first, err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: first, RunID: "force-unsupported-first", CompletedFiles: []FileRef{},
		UnsupportedFiles: []UnsupportedFile{{FileRef: binary, Reason: "binary"}},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	second, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{binary, code}, true, 1,
	)
	if err != nil || len(second.PendingFiles) != 1 || second.PendingFiles[0].Path != code.Path ||
		len(second.UnsupportedFiles) != 1 ||
		second.UnsupportedFiles[0].ForceCampaignID != first.ForceCampaignID {
		t.Fatalf("second force plan=%#v err=%v", second, err)
	}
}

func TestReviewRecordMergesAcrossConcurrentFindingStatusMutation(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	first := repositoryAuditTestFile("a.go", "a", 10)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{first}, false)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "seed",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{first}, Findings: []FindingCandidate{{
			Severity: "low", Title: "Seed finding", File: first.Path, Evidence: "seed evidence",
			Impact:     "seed impact",
			Validation: Validation{Status: "confirmed", Summary: "seed validation"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second := repositoryAuditTestFile("b.go", "b", 10)
	concurrentPlan, err := store.Plan(
		context.Background(), "owner/repo", "commit-b", "inventory-b", []FileRef{second}, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, statusErr := store.SetFindingStatus(
		"owner/repo", seed.State.Findings[0].ID, FindingDismissed, seed.State.Version,
	); statusErr != nil {
		t.Fatal(statusErr)
	}
	merged, err := store.Record(context.Background(), RecordRequest{
		Plan: concurrentPlan, RunID: "concurrent-review",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{second}}},
	})
	if err != nil {
		t.Fatalf("review record conflicted with UI-only mutation: %v", err)
	}
	if merged.State.Findings[0].Status != FindingDismissed || len(merged.State.Files) != 2 {
		t.Fatalf("merged state lost UI or review data: %#v", merged.State)
	}
}

func TestAuthoritativeReviewPrunesOnlyObsoleteCheckpointMetadata(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	left := repositoryAuditTestFile("left.go", "1", 10)
	right := repositoryAuditTestFile("right.go", "2", 10)
	seed, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{left, right},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: seed, RunID: "authoritative-seed",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{left, right}}},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	rightChanged := right
	rightChanged.BlobSHA = strings.Repeat("3", 40)
	current, err := store.PlanWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", "commit-b", "inventory-b",
		"repository-bug-finder-v1", []FileRef{rightChanged}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: current, RunID: "authoritative-current",
		Observations: []Observation{{Model: "review-a", ScopeFiles: current.PendingFiles}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := result.State.Files[left.Path]; stale || len(result.State.Files) != 1 {
		t.Fatalf("authoritative state retained obsolete checkpoint: %#v", result.State.Files)
	}
}

func TestIssuePublicationClaimHasExactlyOneWinner(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("a.go", "e", 10)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "claim-seed",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
			Severity: "low", Title: "Claim seed", File: file.Path, Evidence: "evidence",
			Impact:     "impact",
			Validation: Validation{Status: "confirmed", Summary: "confirmed"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, _ := completeRepositoryAuditTestMapping(
		t, store, recorded.State, recorded.State.Findings[0].ID,
	)
	_, draft, err := store.PrepareIssue(IssueDraftRequest{
		Repository: "owner/repo", FindingIDs: []string{recorded.State.Findings[0].ID},
		ExpectedVersion: mapped.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	var winners int
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, claimed, claimErr := store.ClaimIssueDraftPublication(
				"owner/repo", draft.ID, draft.Version,
			)
			if claimErr != nil {
				t.Errorf("claim error=%v", claimErr)
				return
			}
			if claimed {
				mutex.Lock()
				winners++
				mutex.Unlock()
			}
		}()
	}
	wait.Wait()
	if winners != 1 {
		t.Fatalf("publication claim winners=%d, want 1", winners)
	}
}

func TestUnreviewedFileRotatesBehindUnattemptedBatch(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := []FileRef{
		repositoryAuditTestFile("a-large.go", "1", 10),
		repositoryAuditTestFile("b.go", "2", 10),
		repositoryAuditTestFile("c.go", "3", 10),
	}
	first, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		files, false, 1,
	)
	if err != nil || first.PendingFiles[0].Path != "a-large.go" {
		t.Fatalf("first plan=%#v err=%v", first, err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: first, RunID: "unavailable-a",
		Observations:   []Observation{{Model: "review-a", ScopeFiles: first.PendingFiles}},
		CompletedFiles: []FileRef{},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	second, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		files, false, 1,
	)
	if err != nil || second.PendingFiles[0].Path != "b.go" {
		t.Fatalf("second plan=%#v err=%v, unavailable file starved unattempted work", second, err)
	}
}

func TestChangedBlobDoesNotInheritPriorReviewAttempts(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	failed := repositoryAuditTestFile("a.go", "1", 10)
	first, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{failed}, false, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := store.Record(context.Background(), RecordRequest{
		Plan: first, RunID: "failed-old-blob",
		Observations:   []Observation{{Model: "review-a", ScopeFiles: first.PendingFiles}},
		CompletedFiles: []FileRef{},
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	changed := failed
	changed.BlobSHA = strings.Repeat("2", 40)
	unattempted := repositoryAuditTestFile("b.go", "3", 10)
	next, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-b", "inventory-b", "profile-a",
		[]FileRef{changed, unattempted}, false, 1,
	)
	if err != nil || len(next.PendingFiles) != 1 || next.PendingFiles[0].Path != changed.Path {
		t.Fatalf("changed-content attempt ordering=%#v err=%v", next.PendingFiles, err)
	}
}

func TestTerminalUnsupportedFileIsSkippedUntilBlobOrProfileChanges(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("fixture.bin", "7", 10)
	plan, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-a", "inventory-a", "profile-a",
		[]FileRef{file}, false, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "unsupported",
		Observations:     []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}}},
		CompletedFiles:   []FileRef{},
		UnsupportedFiles: []UnsupportedFile{{FileRef: file, Reason: "binary"}},
	})
	if err != nil || result.Run.RemainingFiles != 0 || result.Run.UnsupportedCount != 1 {
		t.Fatalf("unsupported result=%#v err=%v", result.Run, err)
	}
	next, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-b", "inventory-b", "profile-a",
		[]FileRef{file}, false, 1,
	)
	if err != nil || len(next.PendingFiles) != 0 || len(next.UnsupportedFiles) != 1 {
		t.Fatalf("unsupported skip plan=%#v err=%v", next, err)
	}
	changedProfile, err := store.PlanWithProfileLimit(
		context.Background(), "owner/repo", "commit-b", "inventory-b", "profile-b",
		[]FileRef{file}, false, 1,
	)
	if err != nil || len(changedProfile.PendingFiles) != 1 {
		t.Fatalf("profile change plan=%#v err=%v", changedProfile, err)
	}
}
