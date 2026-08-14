package prworkspace

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectGateEvidenceIsAllowlistedAndDecisionRelevant(t *testing.T) {
	evidence := projectGateEvidence(map[string]any{
		"charter": Charter{Type: PRTypeFix, Goal: "fix only the retry race"},
		"repair": RepairAttempt{
			Instruction:  "private repair prompt must stay private",
			CandidateSHA: "candidate-1",
			ChangedFiles: []string{"pkg/retry/run.go", "pkg/retry/run.go"},
			FindingIDs:   []string{"finding-2"},
		},
		"validation": ValidationRun{
			State: ExecutionSucceeded, CandidateSHA: "candidate-1",
			Checks: []ValidationCheck{{ID: "unit", Name: "unit tests", Status: "passed"}},
		},
		"scope": ScopeAssessment{
			Distance: ScopeExact, Size: ChangeSizeS, Files: 2,
			ChangeEvidence: []ScopeChange{{Path: "pkg/retry/test.go", Hunk: "private diff hunk"}},
		},
		"findings": []Finding{{ID: "finding-1", File: "pkg/retry/run.go"}},
		"publication": Publication{
			Kind: PublicationBranchPush, ExpectedHeadSHA: "head-1", PayloadDigest: "sha256:payload",
		},
		"provider_revision": "provider-v7",
		"access_token":      "must-never-project",
	})

	require.Equal(t, PRTypeFix, evidence.CharterType)
	require.Equal(t, "fix only the retry race", evidence.CharterGoal)
	require.Equal(t, "candidate-1", evidence.CandidateSHA)
	require.Equal(t, []string{"pkg/retry/run.go", "pkg/retry/test.go"}, evidence.ChangedFiles)
	require.Equal(t, []string{"finding-1", "finding-2"}, evidence.FindingIDs)
	require.Equal(t, 2, evidence.FindingCount)
	require.Equal(t, ExecutionSucceeded, evidence.ValidationState)
	require.Equal(t, PublicationBranchPush, evidence.PublicationKind)
	require.Equal(t, "sha256:payload", evidence.PayloadDigest)
	require.Equal(t, "head-1", evidence.ExpectedHeadSHA)
	require.Equal(t, "provider-v7", evidence.ProviderRevision)
	require.NotNil(t, evidence.Scope)

	encoded, err := json.Marshal(evidence)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private repair prompt")
	require.NotContains(t, string(encoded), "private diff hunk")
	require.NotContains(t, string(encoded), "must-never-project")
}

func TestPublicationGateEvidenceShowsExactSafePreviewWithoutPrivateInputs(t *testing.T) {
	line := 17
	review := projectGateEvidence(map[string]any{
		"publication": Publication{Kind: PublicationGitHubReview, PayloadDigest: "sha256:review"},
		"request": reviewPublicationPayload{
			Provider: ProviderSnapshot{Repository: "octo/repo"}, Summary: "Review summary",
			Findings: []Finding{{
				ID: "finding-1", Title: "Retry race", File: "pkg/retry.go", Line: &line,
				Message: "Retry races with cancellation", Evidence: "private model chain",
			}},
		},
		"access_token": "secret-token",
	})
	require.Equal(t, "octo/repo", review.Repository)
	require.Equal(t, "Review summary", review.ReviewSummary)
	require.Equal(t, []GateFindingEvidence{{
		ID: "finding-1", Title: "Retry race", File: "pkg/retry.go", Line: &line,
		Message: "Retry races with cancellation",
	}}, review.PublicationFindings)

	branch := projectGateEvidence(map[string]any{
		"publication": Publication{Kind: PublicationBranchPush, PayloadDigest: "sha256:branch"},
		"request": branchPublicationPayload{
			Provider: ProviderSnapshot{Repository: "octo/repo"},
			Repair: RepairAttempt{
				CandidateSHA: "candidate", ResultSummary: "Fixed retry", ChangedFiles: []string{"pkg/retry.go"},
				Instruction: "private edit instruction",
			},
		},
		"validation": ValidationRun{Checks: []ValidationCheck{{
			ID: "test", Name: "go test", Status: "passed", Summary: "private CI output",
		}}},
	})
	require.Equal(t, "candidate", branch.CandidateSHA)
	require.Equal(t, "Fixed retry", branch.RepairSummary)
	require.Equal(t, "", branch.ValidationChecks[0].Summary)

	issue := projectGateEvidence(map[string]any{
		"publication": Publication{Kind: PublicationGitHubIssue, PayloadDigest: "sha256:issue"},
		"request": issuePublicationPayload{
			Repository: "octo/repo", Title: "Follow-up", Body: "Bounded issue body",
			Labels: []string{"follow-up"}, FindingIDs: []string{"finding-2"},
		},
	})
	require.Equal(t, "Follow-up", issue.IssueTitle)
	require.Equal(t, "Bounded issue body", issue.IssueBody)

	encoded, err := json.Marshal([]GateEvidence{review, branch, issue})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-token")
	require.NotContains(t, string(encoded), "private model chain")
	require.NotContains(t, string(encoded), "private edit instruction")
	require.NotContains(t, string(encoded), "private CI output")
}

func TestProjectGateEvidencePinsHardScopeAndOnlyResolutionFindings(t *testing.T) {
	repairFinding := Finding{
		ID:    "pfn_11111111111111111111111111111111",
		Scope: ScopeAssessment{Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true},
	}
	drift := Finding{
		ID:    "pfn_22222222222222222222222222222222",
		Scope: ScopeAssessment{Presence: WorkCandidatePresent, Distance: ScopeRelatedFollowup, Size: ChangeSizeXS, TypeCompatible: true},
	}
	evidence := projectGateEvidence(map[string]any{
		"repair": RepairAttempt{FindingIDs: []string{repairFinding.ID}},
		"scope":  repairFinding.Scope, "candidate_drift": []Finding{drift},
	})
	require.True(t, evidence.HardScope)
	require.Equal(t, []string{drift.ID}, evidence.HardScopeFindingIDs)
	require.Equal(t, []string{repairFinding.ID, drift.ID}, evidence.FindingIDs)
}
