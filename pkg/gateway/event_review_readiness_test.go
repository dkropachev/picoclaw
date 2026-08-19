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

func TestGitHubReviewProviderReadReadiness(t *testing.T) {
	want := []string{
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubPullRequestReadTool,
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubGetMeTool,
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubSearchRepositoriesTool,
	}
	readiness := map[string]workflows.WorkflowDependencyReadinessCode{
		want[0]: workflows.WorkflowDependencyReadinessReady,
		want[1]: workflows.WorkflowDependencyReadinessReady,
		want[2]: workflows.WorkflowDependencyReadinessReady,
	}
	var seen []string
	resolve := func(occurrence workflows.WorkflowDependencyOccurrence) workflows.WorkflowDependencyReadinessCode {
		if occurrence.Kind != workflows.WorkflowDependencyKindMCP {
			t.Fatalf("dependency kind = %q", occurrence.Kind)
		}
		seen = append(seen, occurrence.Name)
		return readiness[occurrence.Name]
	}
	if !githubReviewProviderReadToolsReady(resolve) {
		t.Fatal("ready provider read reported unavailable")
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("resolved tools = %#v, want %#v", seen, want)
	}
	for _, missing := range want {
		t.Run(missing, func(t *testing.T) {
			readiness[missing] = workflows.WorkflowDependencyReadinessUnavailable
			if githubReviewProviderReadToolsReady(resolve) {
				t.Fatalf("provider read ready with missing tool %q", missing)
			}
			readiness[missing] = workflows.WorkflowDependencyReadinessReady
		})
	}
	if githubReviewProviderReadToolsReady(nil) {
		t.Fatal("nil resolver reported provider readiness")
	}
}

func TestGitHubPRWorkspaceIssuePublicationReadinessRequiresCreateAndSearch(t *testing.T) {
	want := []string{
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubIssueWriteTool,
		reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubSearchIssuesTool,
	}
	var seen []string
	if !githubPRWorkspaceIssuePublicationToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		if occurrence.Kind != workflows.WorkflowDependencyKindMCP {
			t.Fatalf("dependency kind = %q, want MCP", occurrence.Kind)
		}
		seen = append(seen, occurrence.Name)
		return workflows.WorkflowDependencyReadinessReady
	}) {
		t.Fatal("ready issue publication tools reported unavailable")
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("resolved tools = %#v, want %#v", seen, want)
	}
	for _, missing := range want {
		t.Run(missing, func(t *testing.T) {
			if githubPRWorkspaceIssuePublicationToolsReady(func(
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
	if githubPRWorkspaceIssuePublicationToolsReady(nil) {
		t.Fatal("nil resolver reported issue publication ready")
	}
}
