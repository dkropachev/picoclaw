package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
)

func TestProjectLocalRepairResultRetainsPartialNumericUsageOnly(t *testing.T) {
	t.Parallel()

	const privateToolValue = "private/path/or/tool-result-content"
	projected := projectLocalRepairResult(agent.LocalRepairResult{
		Content: "bounded summary", WorkspaceID: "workspace-id",
		PromptDigest: "sha256:prompt", ProfileDigest: "sha256:profile",
		Metrics: agent.LocalRepairMetrics{
			Complete: false,
			Usage: agent.LocalRepairUsage{
				ProviderCalls: 3, UsageReportedCalls: 2,
				PromptTokens: 20, CachedTokens: 8,
				CompletionTokens: 5, ReasoningTokens: 2, TotalTokens: 25,
				LatencyMillis: 40,
			},
			Tools: map[string]agent.LocalRepairToolMetrics{
				privateToolValue: {Calls: 99, Failures: 99, ResultBytes: 99},
			},
		},
	})
	if projected.Summary != "bounded summary" || projected.WorkspaceID != "workspace-id" ||
		projected.PromptDigest != "sha256:prompt" || projected.ProfileDigest != "sha256:profile" ||
		projected.UsageComplete || projected.Usage.ProviderCalls != 3 ||
		projected.Usage.UsageReportedCalls != 2 || projected.Usage.PromptTokens != 20 ||
		projected.Usage.CachedTokens != 8 || projected.Usage.CompletionTokens != 5 ||
		projected.Usage.ReasoningTokens != 2 || projected.Usage.TotalTokens != 25 ||
		projected.Usage.LatencyMillis != 40 {
		t.Fatalf("local repair projection = %#v", projected)
	}
	encoded, err := json.Marshal(map[string]any{
		"summary": projected.Summary, "workspace_id": projected.WorkspaceID,
		"prompt_digest": projected.PromptDigest, "profile_digest": projected.ProfileDigest,
		"usage": projected.Usage, "usage_complete": projected.UsageComplete,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privateToolValue) || strings.Contains(string(encoded), "Tools") {
		t.Fatalf("local repair projection exposed internal tool telemetry: %s", encoded)
	}
}
