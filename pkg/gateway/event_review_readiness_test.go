package gateway

import (
	"context"
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

func TestGitHubPRDevelopmentRepairReadinessRequiresPullRequestRead(t *testing.T) {
	want := reviews.DefaultGitHubMCPServer + "/" + reviews.GitHubPullRequestReadTool
	called := 0
	if !githubPRDevelopmentRepairToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		called++
		if occurrence.Kind != workflows.WorkflowDependencyKindMCP ||
			occurrence.Name != want {
			t.Fatalf("repair dependency = %#v, want MCP %q", occurrence, want)
		}
		return workflows.WorkflowDependencyReadinessReady
	}) {
		t.Fatal("ready pull_request_read dependency reported unavailable")
	}
	if called != 1 {
		t.Fatalf("repair readiness resolver calls = %d, want 1", called)
	}
	if githubPRDevelopmentRepairToolsReady(func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return workflows.WorkflowDependencyReadinessUnavailable
	}) {
		t.Fatal("unavailable pull_request_read dependency reported ready")
	}
	if githubPRDevelopmentRepairToolsReady(nil) {
		t.Fatal("nil repair dependency resolver reported ready")
	}
}

func TestGitHubPRDevelopmentRepairReadinessOnlyRequiresProviderRead(t *testing.T) {
	runtime := &gatewayRepairReadinessFake{
		dependency: workflows.WorkflowDependencyReadinessReady,
	}
	if !githubPRDevelopmentRepairReady(context.Background(), runtime) {
		t.Fatal("ready pull_request_read dependency reported unavailable")
	}

	runtime.dependency = workflows.WorkflowDependencyReadinessUnavailable
	if githubPRDevelopmentRepairReady(context.Background(), runtime) {
		t.Fatal("unavailable MCP dependency reported ready")
	}
	if githubPRDevelopmentRepairReady(context.Background(), nil) {
		t.Fatal("nil repair runtime reported ready")
	}
}

type gatewayRepairReadinessFake struct {
	dependency workflows.WorkflowDependencyReadinessCode
}

func (runtime *gatewayRepairReadinessFake) ResolveWorkflowDependency(
	context.Context,
	workflows.WorkflowDependencyOccurrence,
) workflows.WorkflowDependencyReadinessCode {
	return runtime.dependency
}
