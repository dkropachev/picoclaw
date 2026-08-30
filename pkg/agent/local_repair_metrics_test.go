package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func localRepairTestUsage(
	prompt, cached, completion, reasoning int,
) *providers.UsageInfo {
	return &providers.UsageInfo{
		PromptTokens:     prompt,
		CachedTokens:     cached,
		CompletionTokens: completion,
		ReasoningTokens:  reasoning,
		TotalTokens:      prompt + completion,
	}
}

func TestLocalRepairMetricsAccumulateProviderAndFixedToolTotals(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	provider.handler = func(
		index int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		switch index {
		case 0:
			return &providers.LLMResponse{
				Usage: localRepairTestUsage(100, 40, 20, 5),
				ToolCalls: []providers.ToolCall{
					localRepairTestToolCall(
						"read-one",
						"read_file",
						map[string]any{"path": "README.md"},
					),
					localRepairTestToolCall(
						"list-one",
						"list_dir",
						map[string]any{"path": "."},
					),
					localRepairTestToolCall(
						"edit-failure",
						"edit_file",
						map[string]any{
							"path":     "README.md",
							"old_text": "not present",
							"new_text": "replacement",
						},
					),
					localRepairTestToolCall(
						"patch-one",
						"apply_patch",
						map[string]any{"patch": "*** Begin Patch\n" +
							"*** Add File: metrics.txt\n" +
							"+numeric only\n" +
							"*** End Patch"},
					),
				},
			}, nil
		case 1:
			return &providers.LLMResponse{
				Content: "metrics complete",
				Usage:   localRepairTestUsage(200, 50, 30, 10),
			}, nil
		default:
			t.Fatalf("unexpected provider call %d", index+1)
			return nil, nil
		}
	}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantUsage := LocalRepairUsage{
		ProviderCalls:      2,
		UsageReportedCalls: 2,
		PromptTokens:       300,
		CachedTokens:       90,
		CompletionTokens:   50,
		ReasoningTokens:    15,
		TotalTokens:        350,
	}
	gotUsage := result.Metrics.Usage
	gotUsage.LatencyMillis = 0
	if !result.Metrics.Complete || gotUsage != wantUsage {
		t.Fatalf("Run() metrics = %#v, want complete usage %#v", result.Metrics, wantUsage)
	}
	if len(result.Metrics.Tools) != 4 {
		t.Fatalf("tool metric names = %#v, want exactly four dispatched tools", result.Metrics.Tools)
	}
	for _, name := range []string{"read_file", "list_dir", "edit_file", "apply_patch"} {
		metric, ok := result.Metrics.Tools[name]
		if !ok || metric.Calls != 1 || metric.ResultBytes <= 0 || metric.DurationMillis < 0 {
			t.Errorf("tool metric %q = %#v", name, metric)
		}
		wantFailures := int64(0)
		if name == "edit_file" {
			wantFailures = 1
		}
		if metric.Failures != wantFailures {
			t.Errorf("tool metric %q failures = %d, want %d", name, metric.Failures, wantFailures)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "metrics.txt")); statErr != nil {
		t.Fatalf("patched file is unavailable: %v", statErr)
	}
	encoded, err := json.Marshal(result.Metrics)
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	for _, secret := range []string{root, "README.md", "numeric only", "not present"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("numeric metrics exposed sensitive value %q: %s", secret, encoded)
		}
	}
}

func TestLocalRepairMetricsRetainMissingAndErrorUsage(t *testing.T) {
	tests := []struct {
		name         string
		second       func() (*providers.LLMResponse, error)
		wantReported int64
		wantComplete bool
		wantError    bool
	}{
		{
			name: "missing usage",
			second: func() (*providers.LLMResponse, error) {
				return &providers.LLMResponse{Content: "done"}, nil
			},
			wantReported: 1,
		},
		{
			name: "provider error with usage",
			second: func() (*providers.LLMResponse, error) {
				return &providers.LLMResponse{
					Usage: localRepairTestUsage(7, 2, 3, 1),
				}, errors.New("provider unavailable")
			},
			wantReported: 2,
			wantComplete: true,
			wantError:    true,
		},
		{
			name: "provider error without usage",
			second: func() (*providers.LLMResponse, error) {
				return nil, errors.New("provider unavailable")
			},
			wantReported: 1,
			wantError:    true,
		},
		{
			name: "estimated usage remains numeric but incomplete",
			second: func() (*providers.LLMResponse, error) {
				usage := localRepairTestUsage(7, 2, 3, 1)
				usage.Estimated = true
				return &providers.LLMResponse{Content: "done", Usage: usage}, nil
			},
			wantReported: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin, workspace, _ := newLocalRepairTestWorkspace(t)
			acquirer := &localRepairTestAcquirer{workspace: workspace}
			provider := &localRepairTestProvider{}
			provider.handler = func(
				index int,
				_ []providers.Message,
				_ []providers.ToolDefinition,
				_ string,
				_ map[string]any,
			) (*providers.LLMResponse, error) {
				if index == 0 {
					return &providers.LLMResponse{
						Usage: localRepairTestUsage(11, 4, 5, 2),
						ToolCalls: []providers.ToolCall{localRepairTestToolCall(
							"read", "read_file", map[string]any{"path": "README.md"},
						)},
					}, nil
				}
				return test.second()
			}

			result, err := newLocalRepairTestRunner(t, acquirer, provider, 3).Run(
				t.Context(),
				localRepairTestRunRequest(pin),
			)
			if (err != nil) != test.wantError {
				t.Fatalf("Run() error = %v, wantError %v", err, test.wantError)
			}
			if result.Metrics.Complete != test.wantComplete ||
				result.Metrics.Usage.ProviderCalls != 2 ||
				result.Metrics.Usage.UsageReportedCalls != test.wantReported ||
				result.Metrics.Usage.PromptTokens < 11 {
				t.Fatalf("Run() partial metrics = %#v", result.Metrics)
			}
			if metric := result.Metrics.Tools["read_file"]; metric.Calls != 1 {
				t.Fatalf("read tool metric = %#v", metric)
			}
		})
	}
}

func TestLocalRepairMetricsRetainProviderPanic(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{handler: func(
		_ int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		_ map[string]any,
	) (*providers.LLMResponse, error) {
		panic("private provider panic")
	}}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		t.Context(),
		localRepairTestRunRequest(pin),
	)
	if err == nil || !strings.Contains(err.Error(), "provider panicked") {
		t.Fatalf("Run() panic error = %v", err)
	}
	if result.Metrics.Complete || result.Metrics.Usage.ProviderCalls != 1 ||
		result.Metrics.Usage.UsageReportedCalls != 0 {
		t.Fatalf("panic metrics = %#v", result.Metrics)
	}
}

func TestLocalRepairMetricsRetainUsageAfterCancellation(t *testing.T) {
	pin, workspace, root := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &localRepairTestProvider{
		handler: func(
			_ int,
			_ []providers.Message,
			_ []providers.ToolDefinition,
			_ string,
			_ map[string]any,
		) (*providers.LLMResponse, error) {
			cancel()
			return &providers.LLMResponse{
				Usage: localRepairTestUsage(13, 3, 5, 2),
				ToolCalls: []providers.ToolCall{localRepairTestToolCall(
					"late-edit",
					"edit_file",
					map[string]any{
						"path": "README.md", "old_text": "before\n", "new_text": "after\n",
					},
				)},
			}, nil
		},
	}

	result, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
		ctx,
		localRepairTestRunRequest(pin),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !result.Metrics.Complete || result.Metrics.Usage.ProviderCalls != 1 ||
		result.Metrics.Usage.UsageReportedCalls != 1 ||
		result.Metrics.Usage.TotalTokens != 18 || len(result.Metrics.Tools) != 0 {
		t.Fatalf("Run() cancellation metrics = %#v", result.Metrics)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "README.md"))
	if readErr != nil || string(content) != "before\n" {
		t.Fatalf("README after cancellation = %q, error = %v", content, readErr)
	}
}

func TestLocalRepairMetricsRejectInvalidUsageAndOverflow(t *testing.T) {
	invalidUsage := []struct {
		name  string
		usage providers.UsageInfo
	}{
		{name: "negative reasoning", usage: providers.UsageInfo{ReasoningTokens: -1}},
		{name: "cached exceeds prompt", usage: providers.UsageInfo{
			PromptTokens: 1, CachedTokens: 2, TotalTokens: 1,
		}},
		{name: "reasoning exceeds completion", usage: providers.UsageInfo{
			CompletionTokens: 1, ReasoningTokens: 2, TotalTokens: 1,
		}},
		{name: "total mismatch", usage: providers.UsageInfo{
			PromptTokens: 2, CompletionTokens: 3, TotalTokens: 4,
		}},
	}
	for _, test := range invalidUsage {
		t.Run(test.name, func(t *testing.T) {
			pin, workspace, _ := newLocalRepairTestWorkspace(t)
			acquirer := &localRepairTestAcquirer{workspace: workspace}
			provider := &localRepairTestProvider{handler: func(
				_ int,
				_ []providers.Message,
				_ []providers.ToolDefinition,
				_ string,
				_ map[string]any,
			) (*providers.LLMResponse, error) {
				usage := test.usage
				return &providers.LLMResponse{Content: "must fail", Usage: &usage}, nil
			}}
			result, err := newLocalRepairTestRunner(t, acquirer, provider, 2).Run(
				t.Context(),
				localRepairTestRunRequest(pin),
			)
			if err == nil || !strings.Contains(err.Error(), "usage is invalid") {
				t.Fatalf("Run() error = %v, want invalid usage", err)
			}
			if result.Metrics.Complete || result.Metrics.Usage.ProviderCalls != 1 ||
				result.Metrics.Usage.UsageReportedCalls != 0 {
				t.Fatalf("invalid usage metrics = %#v", result.Metrics)
			}
		})
	}

	collector := newLocalRepairMetricsCollector()
	collector.usage = LocalRepairUsage{
		ProviderCalls:      1,
		UsageReportedCalls: 1,
		PromptTokens:       math.MaxInt64,
		TotalTokens:        math.MaxInt64,
	}
	err := collector.observeProviderCall(&providers.LLMResponse{
		Usage: localRepairTestUsage(1, 0, 0, 0),
	}, 0)
	if err == nil || !strings.Contains(err.Error(), "aggregation overflow") {
		t.Fatalf("observeProviderCall() error = %v, want aggregation overflow", err)
	}
	snapshot := collector.snapshot()
	if snapshot.Complete || snapshot.Usage.ProviderCalls != 2 ||
		snapshot.Usage.UsageReportedCalls != 1 || snapshot.Usage.PromptTokens != math.MaxInt64 {
		t.Fatalf("overflow metrics = %#v", snapshot)
	}
}

func TestLocalRepairMetricsSnapshotsAreDetachedAndConcurrent(t *testing.T) {
	collector := newLocalRepairMetricsCollector()
	const calls = 64
	var group sync.WaitGroup
	for range calls {
		group.Add(2)
		go func() {
			defer group.Done()
			if err := collector.observeProviderCall(&providers.LLMResponse{
				Usage: localRepairTestUsage(2, 1, 1, 1),
			}, time.Millisecond); err != nil {
				t.Errorf("observeProviderCall() error = %v", err)
			}
		}()
		go func() {
			defer group.Done()
			collector.observeTool(
				localRepairToolRead,
				&tools.ToolResult{ForLLM: "ok"},
				time.Millisecond,
			)
		}()
	}
	group.Wait()

	first := collector.snapshot()
	if !first.Complete || first.Usage.ProviderCalls != calls ||
		first.Usage.UsageReportedCalls != calls || first.Usage.PromptTokens != calls*2 ||
		first.Usage.TotalTokens != calls*3 || first.Usage.LatencyMillis != calls {
		t.Fatalf("concurrent provider metrics = %#v", first)
	}
	if metric := first.Tools["read_file"]; metric.Calls != calls ||
		metric.DurationMillis != calls || metric.ResultBytes != calls*2 {
		t.Fatalf("concurrent tool metric = %#v", metric)
	}
	first.Tools["read_file"] = LocalRepairToolMetrics{Calls: math.MaxInt64}
	first.Tools["not_allowed"] = LocalRepairToolMetrics{Calls: 1}
	second := collector.snapshot()
	if second.Tools["read_file"].Calls != calls {
		t.Fatalf("snapshot map was retained by collector: %#v", second.Tools)
	}
	cloned := cloneLocalRepairMetrics(first)
	if _, ok := cloned.Tools["not_allowed"]; ok {
		t.Fatalf("clone retained an unbounded tool name: %#v", cloned.Tools)
	}
	cloned.Tools["read_file"] = LocalRepairToolMetrics{}
	if first.Tools["read_file"].Calls != math.MaxInt64 {
		t.Fatalf("clone shares its tool map with the source: %#v", first.Tools)
	}
}

func TestLocalRepairMetricsSaturateBoundedToolTotals(t *testing.T) {
	collector := newLocalRepairMetricsCollector()
	if err := collector.observeProviderCall(&providers.LLMResponse{
		Usage: localRepairTestUsage(2, 1, 1, 0),
	}, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	collector.tools["read_file"] = LocalRepairToolMetrics{
		Calls:          math.MaxInt64,
		Failures:       math.MaxInt64,
		DurationMillis: math.MaxInt64,
		ResultBytes:    math.MaxInt64,
	}
	collector.observeTool(
		localRepairToolRead,
		tools.ErrorResult("x"),
		time.Millisecond,
	)
	snapshot := collector.snapshot()
	metric := snapshot.Tools["read_file"]
	if metric.Calls != math.MaxInt64 || metric.Failures != math.MaxInt64 ||
		metric.DurationMillis != math.MaxInt64 || metric.ResultBytes != math.MaxInt64 ||
		!snapshot.Complete {
		t.Fatalf("saturated tool metrics = %#v", snapshot)
	}
}
