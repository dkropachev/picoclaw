package repoaudit

import (
	"os"
	"strings"
	"testing"
	"time"
)

var repositoryAuditTestNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

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

func newRepositoryAuditTestStore(t *testing.T) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return repositoryAuditTestNow }
	return store
}

func completeRepositoryAuditTestMapping(
	t *testing.T,
	store Store,
	state RepositoryState,
	findingID string,
) (RepositoryState, RepositoryFinding) {
	t.Helper()
	jobIndex := -1
	for index := range state.MappingJobs {
		if state.MappingJobs[index].ReviewFindingID == findingID {
			jobIndex = index
			break
		}
	}
	if jobIndex < 0 {
		t.Fatalf("mapping job for finding %q is missing", findingID)
	}
	claimedState, job, _, claimed, err := store.ClaimMappingJob(
		state.Repository,
		state.MappingJobs[jobIndex].ID,
		RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("claim mapping job for finding %q: claimed=%v err=%v", findingID, claimed, err)
	}
	mappedState, repositoryFinding, err := store.CompleteMappingJob(
		claimedState.Repository,
		RepositoryMappingCompletion{
			JobID:                 job.ID,
			CreateMatchState:      RepositoryMatchNew,
			DefaultBranchVerified: true,
		},
	)
	if err != nil {
		t.Fatalf("complete mapping job for finding %q: %v", findingID, err)
	}
	return mappedState, repositoryFinding
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
) FinalizeRepositoryReviewRunResult {
	t.Helper()
	state, _, err := store.Get(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !validRepositoryReviewCommitSHA(commit) {
		commit = stableID("", commit)
	}
	campaignID := NewRepositoryReviewCampaignID()
	profileHash := stableID("sha256:", repository, runID)
	scopeDigest, err := repositoryReviewCampaignScopeDigestForFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	run := ReviewRun{
		ID: runID, CampaignID: campaignID, CommitSHA: commit, InventoryHash: inventory,
		ProfileHash: profileHash, ScopeDigest: scopeDigest,
		ReviewedFiles: len(files), TargetIsDefault: true, CompletedAt: repositoryAuditTestNow,
	}
	for _, file := range files {
		state.Files[file.Path] = ReviewedFile{
			FileRef: file, CommitSHA: commit, ProfileHash: profileHash,
			RunID: runID, ReviewedAt: repositoryAuditTestNow,
		}
	}
	state.CampaignHistory[campaignID] = commit
	state.Runs = append(state.Runs, run)
	state.LastCommitSHA = commit
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = repositoryAuditTestNow
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	return FinalizeRepositoryReviewRunResult{State: state, Run: run}
}
