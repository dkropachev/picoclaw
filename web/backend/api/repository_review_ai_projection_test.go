package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryMappingAdjudicationProjectionBoundsPrivateData(t *testing.T) {
	canary := repoaudit.NewRepositoryReviewCampaignID()
	history := make([]repoaudit.RepositoryFindingPathSymbol, 20)
	commits := make([]string, 20)
	for index := range history {
		history[index] = repoaudit.RepositoryFindingPathSymbol{
			ReviewFindingID: fmt.Sprintf("rvf_%02d", index),
			Path:            fmt.Sprintf("path/%02d.go", index),
		}
		commits[index] = fmt.Sprintf("commit_%02d", index)
	}
	request := repoaudit.RepositoryMappingAIRequest{
		Finding: repoaudit.Finding{
			CampaignID:   canary,
			ID:           "rvf_occurrence",
			ContextIDs:   []string{"context-secret"},
			Models:       []string{"reviewer-secret"},
			Observations: []repoaudit.FindingObservation{{ContextID: "observation-secret"}},
			IssueDraftID: "rid_secret",
		},
		Candidates: []repoaudit.RepositoryMappingAICandidate{{
			ID: "candidate_1",
			Finding: repoaudit.RepositoryFinding{
				ID:                "rrf_secret",
				Repository:        "secret/repository",
				ReviewFindingIDs:  []string{"rvf_secret"},
				FoundCommits:      commits,
				PathSymbolHistory: history,
				Issue: repoaudit.RepositoryFindingIssueAssociation{
					URL: "https://example.invalid/issues/1",
				},
				PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{{
					CandidateID: "rrf_other",
				}},
				ResolutionHistory: []repoaudit.RepositoryFindingResolution{{
					Summary: "private resolution history",
				}},
			},
		}},
	}

	projected := repositoryMappingAdjudicationProjection(request)
	if projected.Finding.CampaignID != "" || projected.Finding.IssueDraftID != "" ||
		projected.Finding.ContextIDs != nil ||
		projected.Finding.Models != nil ||
		projected.Finding.Observations != nil {
		t.Fatalf("occurrence provenance was not stripped: %#v", projected.Finding)
	}
	if request.Finding.CampaignID != canary || request.Finding.IssueDraftID != "rid_secret" ||
		len(request.Finding.Observations) != 1 {
		t.Fatal("projection mutated the source occurrence")
	}
	if len(projected.Candidates) != 1 || projected.Candidates[0].ID != "candidate_1" {
		t.Fatalf("opaque candidates changed: %#v", projected.Candidates)
	}
	candidate := projected.Candidates[0].Finding
	if candidate.ID != "" || candidate.Repository != "" ||
		candidate.ReviewFindingIDs != nil || candidate.Issue.URL != "" ||
		candidate.PossibleDuplicates != nil || candidate.ResolutionHistory != nil {
		t.Fatalf("candidate private state was not stripped: %#v", candidate)
	}
	if len(candidate.FoundCommits) != repositoryMappingProjectionHistoryLimit ||
		candidate.FoundCommits[0] != "commit_04" {
		t.Fatalf("commits were not tail-bounded: %#v", candidate.FoundCommits)
	}
	if len(candidate.PathSymbolHistory) != repositoryMappingProjectionHistoryLimit ||
		candidate.PathSymbolHistory[0].Path != "path/04.go" {
		t.Fatalf("path history was not tail-bounded: %#v", candidate.PathSymbolHistory)
	}
	for _, item := range candidate.PathSymbolHistory {
		if item.ReviewFindingID != "" {
			t.Fatalf("review finding ID leaked through path history: %#v", item)
		}
	}
	if request.Candidates[0].Finding.PathSymbolHistory[4].ReviewFindingID != "rvf_04" {
		t.Fatal("projection mutated source candidate history")
	}
}

func TestRepositoryValidationAdjudicationProjectionBoundsAndDeduplicatesSource(t *testing.T) {
	history := make([]repoaudit.RepositoryFindingPathSymbol, 40)
	commits := make([]string, 40)
	for index := range history {
		history[index] = repoaudit.RepositoryFindingPathSymbol{
			ReviewFindingID: fmt.Sprintf("rvf_%02d", index),
			Path:            fmt.Sprintf("path/%02d.go", index),
		}
		commits[index] = fmt.Sprintf("commit_%02d", index)
	}
	evidence := make([]repoaudit.RepositoryValidationEvidence, 10)
	for index := range evidence {
		evidence[index] = repoaudit.RepositoryValidationEvidence{
			CommitSHA: fmt.Sprintf("candidate_%02d", index),
			CommitTime: time.Date(
				2026, time.August, index+1, 0, 0, 0, 0, time.UTC,
			),
			Diff: "bounded diff",
		}
	}
	evidence[1].CurrentSource = "shared current source"
	evidence[2].CurrentSource = "shared current source"
	finding := repoaudit.RepositoryFinding{
		ID:                "rrf_secret",
		Repository:        "secret/repository",
		ReviewFindingIDs:  []string{"rvf_secret"},
		FoundCommits:      commits,
		PathSymbolHistory: history,
		Version:           9,
		CreatedAt:         time.Now().UTC(),
	}

	projectedFinding, projectedEvidence, currentSource := repositoryValidationAdjudicationProjection(finding, evidence)
	if currentSource != "shared current source" {
		t.Fatalf("current source = %q, want shared current source", currentSource)
	}
	if len(projectedEvidence) != repositoryValidationProjectionEvidenceLimit {
		t.Fatalf("evidence count = %d", len(projectedEvidence))
	}
	for _, record := range projectedEvidence {
		if record.CurrentSource != "" {
			t.Fatalf("current source was repeated in evidence: %#v", record)
		}
	}
	if evidence[1].CurrentSource == "" {
		t.Fatal("projection mutated source evidence")
	}
	if len(projectedFinding.FoundCommits) != repositoryValidationProjectionHistoryLimit ||
		projectedFinding.FoundCommits[0] != "commit_08" {
		t.Fatalf("found commits were not tail-bounded: %#v", projectedFinding.FoundCommits)
	}
	if len(projectedFinding.PathSymbolHistory) != repositoryValidationProjectionHistoryLimit ||
		projectedFinding.PathSymbolHistory[0].Path != "path/08.go" {
		t.Fatalf("path history was not tail-bounded: %#v", projectedFinding.PathSymbolHistory)
	}
	for _, item := range projectedFinding.PathSymbolHistory {
		if item.ReviewFindingID != "" {
			t.Fatalf("review finding ID leaked through validation history: %#v", item)
		}
	}
	if projectedFinding.ID != "" || projectedFinding.Repository != "" ||
		projectedFinding.ReviewFindingIDs != nil || projectedFinding.Version != 0 ||
		!projectedFinding.CreatedAt.IsZero() {
		t.Fatalf("validation private state was not stripped: %#v", projectedFinding)
	}
	if finding.PathSymbolHistory[8].ReviewFindingID != "rvf_08" {
		t.Fatal("projection mutated source finding history")
	}
}
