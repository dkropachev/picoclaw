package gateway

import (
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestGitHubReviewSubmissionReadinessRequiresReadAndWriteTools(t *testing.T) {
	want := []string{
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubPullRequestReadTool,
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubPullRequestReviewWriteTool,
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubPendingReviewCommentTool,
	}
	var seen []string
	if !githubReviewSubmissionToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		if occurrence.Kind != workflows.WorkflowDependencyKindMCP {
			t.Fatalf("dependency kind = %q, want MCP", occurrence.Kind)
		}
		seen = append(seen, occurrence.Name)
		return workflows.WorkflowDependencyReadinessReady
	}) {
		t.Fatal("all required tools reported not ready")
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("resolved tools = %#v, want %#v", seen, want)
	}

	for _, missing := range want {
		t.Run(missing, func(t *testing.T) {
			if githubReviewSubmissionToolsReady(func(
				occurrence workflows.WorkflowDependencyOccurrence,
			) workflows.WorkflowDependencyReadinessCode {
				if occurrence.Name == missing {
					return workflows.WorkflowDependencyReadinessUnavailable
				}
				return workflows.WorkflowDependencyReadinessReady
			}) {
				t.Fatalf("readiness true with missing tool %q", missing)
			}
		})
	}
	if githubReviewSubmissionToolsReady(nil) {
		t.Fatal("nil resolver reported ready")
	}
}
