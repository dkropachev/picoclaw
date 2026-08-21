package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var repositoryAuditTestNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func TestStorePlanSkipsSameBlobAndSize(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := []FileRef{repositoryAuditTestFile("pkg/service.go", "1", 120)}
	recordRepositoryAuditCoverage(t, store, "owner/repo", "commit-a", "inventory-a", files, "run-a")

	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-b",
		"inventory-b",
		files,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.PendingFiles) != 0 || !reflect.DeepEqual(plan.UnchangedFiles, files) {
		t.Fatalf("plan pending=%#v unchanged=%#v, want unchanged file only", plan.PendingFiles, plan.UnchangedFiles)
	}
}

func TestStoreListSummariesDoesNotProjectFindingPayloads(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "8", 120)
	plan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "summary-run",
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{{
			Severity: "high", Title: "Large private evidence", Symbol: "Save", File: file.Path,
			Evidence: strings.Repeat("private-evidence", 1000), Impact: "loss", Recommendation: "fix",
			Validation: Validation{Status: "confirmed", Summary: "confirmed"},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := store.ListSummaries()
	if err != nil || len(summaries) != 1 || summaries[0].FindingCount != 1 ||
		summaries[0].OpenFindingCount != 1 || summaries[0].ID != result.State.ID {
		t.Fatalf("summaries=%#v err=%v", summaries, err)
	}
}

func TestStoreGetByIDDoesNotScanUnrelatedLedgers(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "9", 10)
	result := recordRepositoryAuditCoverage(
		t, store, "owner/repo", "commit-a", "inventory-a", []FileRef{file}, "direct-id-run",
	)
	if err := os.WriteFile(store.path("unrelated/repo"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.GetByID(result.State.ID)
	if err != nil || !found || loaded.Repository != "owner/repo" {
		t.Fatalf("direct ID state=%#v found=%v err=%v", loaded, found, err)
	}
}

func TestStorePlanReviewsChangedBlobEvenWhenSizeMatches(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	original := []FileRef{repositoryAuditTestFile("pkg/service.go", "1", 120)}
	recordRepositoryAuditCoverage(t, store, "owner/repo", "commit-a", "inventory-a", original, "run-a")
	changed := []FileRef{repositoryAuditTestFile("pkg/service.go", "2", 120)}

	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-b",
		"inventory-b",
		changed,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.PendingFiles, changed) || len(plan.UnchangedFiles) != 0 {
		t.Fatalf("plan pending=%#v unchanged=%#v, want changed blob pending", plan.PendingFiles, plan.UnchangedFiles)
	}
}

func TestStorePlanReviewsModeOnlyChange(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	original := repositoryAuditTestFile("scripts/run.sh", "a", 120)
	recordRepositoryAuditCoverage(t, store, "owner/repo", "commit-a", "inventory-a", []FileRef{original}, "run-a")
	changed := original
	changed.Mode = "100755"
	plan, err := store.Plan(
		context.Background(), "owner/repo", "commit-b", "inventory-b", []FileRef{changed}, false,
	)
	if err != nil || !reflect.DeepEqual(plan.PendingFiles, []FileRef{changed}) {
		t.Fatalf("mode-only plan=%#v err=%v", plan, err)
	}
}

func TestStorePlanForceReviewsUnchangedBlob(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := []FileRef{repositoryAuditTestFile("pkg/service.go", "1", 120)}
	recordRepositoryAuditCoverage(t, store, "owner/repo", "commit-a", "inventory-a", files, "run-a")

	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-b",
		"inventory-b",
		files,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.PendingFiles, files) || len(plan.UnchangedFiles) != 0 {
		t.Fatalf("forced plan pending=%#v unchanged=%#v, want file pending", plan.PendingFiles, plan.UnchangedFiles)
	}
}

func TestStoreRecordCheckpointsOnlyFilesCoveredBySuccessfulObservations(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	files := []FileRef{
		repositoryAuditTestFile("pkg/covered.go", "1", 80),
		repositoryAuditTestFile("pkg/uncovered.go", "2", 90),
	}
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		files,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan:  plan,
		RunID: "partial-run",
		Observations: []Observation{{
			Model: "review-model-a", ScopeFiles: []FileRef{files[0]}, Summary: "covered",
		}},
		CompletedAt: repositoryAuditTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.ReviewedFiles != 1 {
		t.Errorf("reviewed files = %d, want only the one covered file", result.Run.ReviewedFiles)
	}
	if len(result.State.Files) != 1 {
		t.Errorf("checkpointed files = %#v, want only the one covered file", result.State.Files)
	}
	if _, ok := result.State.Files[files[1].Path]; ok {
		t.Errorf("uncovered file %q was checkpointed", files[1].Path)
	}

	next, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-b",
		"inventory-b",
		files,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.PendingFiles, []FileRef{files[1]}) ||
		!reflect.DeepEqual(next.UnchangedFiles, []FileRef{files[0]}) {
		t.Errorf(
			"next plan pending=%#v unchanged=%#v, want uncovered file retried",
			next.PendingFiles,
			next.UnchangedFiles,
		)
	}
}

func TestStoreRecordPersistsCommitBlobContextAndModelProvenance(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	primary := repositoryAuditTestFile("pkg/service.go", "a", 120)
	dependency := repositoryAuditTestFile("pkg/store.go", "b", 240)
	files := []FileRef{primary, dependency}
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		files,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	line := 42
	result, err := store.Record(context.Background(), RecordRequest{
		Plan:  plan,
		RunID: "provenance-run",
		Observations: []Observation{{
			Model: "review-model-a", Reviewer: "reviewer-a", ScopeFiles: files,
			RawDigest: "sha256:raw-observation",
			Findings: []FindingCandidate{
				{
					Severity:       "high",
					Title:          "Lost update",
					File:           primary.Path,
					Line:           &line,
					Message:        "Concurrent writers can overwrite state.",
					Evidence:       "The write lacks a version fence.",
					Impact:         "A completed review can disappear.",
					Recommendation: "Use compare-and-swap.",
					Validation: Validation{
						Status:  "confirmed",
						Summary: "Reproduced with two writers",
						Checks:  []string{"race test"},
					},
				},
			},
		}},
		CompletedAt: repositoryAuditTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.Findings) != 1 || len(result.State.Contexts) != 1 {
		t.Fatalf("findings=%#v contexts=%#v, want one of each", result.State.Findings, result.State.Contexts)
	}
	finding := result.State.Findings[0]
	contextRecord := result.State.Contexts[0]
	if finding.CommitSHA != plan.CommitSHA || !reflect.DeepEqual(finding.File, primary) {
		t.Errorf(
			"finding provenance = commit %q file %#v, want %q %#v",
			finding.CommitSHA,
			finding.File,
			plan.CommitSHA,
			primary,
		)
	}
	if !reflect.DeepEqual(finding.Models, []string{"review-model-a"}) || finding.ObservationCount != 1 {
		t.Errorf("finding model provenance = models %#v count %d", finding.Models, finding.ObservationCount)
	}
	if !reflect.DeepEqual(finding.ContextIDs, []string{contextRecord.ID}) {
		t.Errorf("finding context IDs = %#v, want %q", finding.ContextIDs, contextRecord.ID)
	}
	if contextRecord.CommitSHA != plan.CommitSHA || contextRecord.InventoryHash != plan.InventoryHash ||
		contextRecord.RunID != "provenance-run" || contextRecord.Model != "review-model-a" ||
		contextRecord.Reviewer != "reviewer-a" || contextRecord.RawDigest != "sha256:raw-observation" ||
		!reflect.DeepEqual(contextRecord.Files, files) {
		t.Errorf("context provenance = %#v", contextRecord)
	}

	persisted, found, err := store.Get("owner/repo")
	if err != nil || !found {
		t.Fatalf("Get() = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(persisted.Findings, result.State.Findings) ||
		!reflect.DeepEqual(persisted.Contexts, result.State.Contexts) {
		t.Errorf("persisted provenance differs from Record result")
	}
}

func TestStoreRecordRejectsUnconfirmedFindingWithoutMaterializingIt(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "1", 120)
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{file},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan:  plan,
		RunID: "unconfirmed-run",
		Observations: []Observation{{
			Model: "review-model-a", ScopeFiles: []FileRef{file},
			Findings: []FindingCandidate{{
				Severity: "high", Title: "Unconfirmed", File: file.Path,
				Evidence: "possible", Impact: "unknown", Recommendation: "investigate",
				Validation: Validation{Status: "unconfirmed", Summary: "not reproduced"},
			}},
		}},
		CompletedAt: repositoryAuditTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.RejectedFindings != 1 || len(result.State.Findings) != 0 || len(result.AcceptedFindingIDs) != 0 {
		t.Fatalf("unconfirmed result = %#v state findings=%#v", result.Run, result.State.Findings)
	}
}

func TestStoreRecordExactReplayIsIdempotent(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "1", 120)
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{file},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := RecordRequest{
		Plan: plan, RunID: "replay-run", CompletedAt: repositoryAuditTestNow,
		Observations: []Observation{{Model: "review-model-a", ScopeFiles: []FileRef{file}, Summary: "reviewed"}},
	}
	first, err := store.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Record(context.Background(), request)
	if err != nil {
		t.Fatalf("exact Record replay returned error: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Errorf("exact replay changed result:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	persisted, _, err := store.Get("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Version != first.State.Version || len(persisted.Runs) != 1 || len(persisted.Contexts) != 0 {
		t.Errorf("exact replay duplicated durable state: %#v", persisted)
	}
}

func TestStoreRecordRejectsStalePlan(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("pkg/service.go", "1", 120)
	firstPlan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	stalePlan, err := store.Plan(context.Background(), "owner/repo", "commit-a", "inventory-a", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Record(context.Background(), RecordRequest{
		Plan: firstPlan, RunID: "first-run", CompletedAt: repositoryAuditTestNow,
		Observations: []Observation{{Model: "review-model-a", ScopeFiles: []FileRef{file}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Record(context.Background(), RecordRequest{
		Plan: stalePlan, RunID: "stale-run", CompletedAt: repositoryAuditTestNow,
		Observations: []Observation{{Model: "review-model-a", ScopeFiles: []FileRef{file}}},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Record error = %v, want ErrConflict", err)
	}
}

func TestStorePlanRejectsRepositoryPathTraversal(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	_, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{{Path: "pkg/../../outside.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1}},
		false,
	)
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("traversal Plan error = %v, want ErrInvalidPlan", err)
	}
}

func TestStoreRejectsSymlinkedStorageRoot(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, storeDirectory)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store := NewStore(workspace)
	_, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]FileRef{repositoryAuditTestFile("pkg/service.go", "1", 120)},
		false,
	)
	if err == nil {
		t.Fatal("Plan accepted a symlinked repository-review storage root")
	}
}

func TestStoreRejectsSymlinkedRepositoryStateFile(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		t.Fatal(err)
	}
	repository := "owner/repo"
	outside := filepath.Join(t.TempDir(), "outside-state.json")
	data, err := json.Marshal(RepositoryState{
		SchemaVersion: SchemaVersion,
		Repository:    repository,
		Version:       1,
		Files:         map[string]ReviewedFile{},
		UpdatedAt:     repositoryAuditTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, store.path(repository)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := store.Get(repository); err == nil {
		t.Fatal("Get followed a symlinked repository-review state file")
	}
	if _, err := store.List(); err == nil {
		t.Fatal("List followed a symlinked repository-review state file")
	}
}

func newRepositoryAuditTestStore(t *testing.T) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return repositoryAuditTestNow }
	return store
}

func repositoryAuditTestFile(path, marker string, size int64) FileRef {
	return FileRef{
		Path: path, BlobSHA: strings.Repeat(marker, 40), SizeBytes: size,
		Category: "code", Mode: "100644",
	}
}

func recordRepositoryAuditCoverage(
	t *testing.T,
	store Store,
	repository string,
	commit string,
	inventory string,
	files []FileRef,
	runID string,
) RecordResult {
	t.Helper()
	plan, err := store.Plan(context.Background(), repository, commit, inventory, files, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: runID, CompletedAt: repositoryAuditTestNow,
		Observations: []Observation{{Model: "review-model-a", ScopeFiles: files, Summary: "reviewed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
