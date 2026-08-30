package agent

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// LocalRepairUsage is the bounded, provider-neutral usage accumulated for one
// local repair run. Cached tokens are a subset of prompt tokens and reasoning
// tokens are a subset of completion tokens. No model, account, request, or
// response identity is retained.
type LocalRepairUsage struct {
	ProviderCalls      int64 `json:"provider_calls"`
	UsageReportedCalls int64 `json:"usage_reported_calls"`
	PromptTokens       int64 `json:"prompt_tokens"`
	CachedTokens       int64 `json:"cached_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	LatencyMillis      int64 `json:"latency_millis"`
}

// LocalRepairToolMetrics contains only fixed-size numeric observations for one
// of the four tools admitted to the local repair loop. It deliberately omits
// arguments, paths, result content, and error text.
type LocalRepairToolMetrics struct {
	Calls          int64 `json:"calls"`
	Failures       int64 `json:"failures"`
	DurationMillis int64 `json:"duration_millis"`
	ResultBytes    int64 `json:"result_bytes"`
}

// LocalRepairMetrics is a bounded snapshot of one local repair run. Tools can
// contain only the fixed local-repair tool names and has entries only for tools
// that were dispatched. Complete is true only when at least one provider call
// was dispatched and every dispatched call returned valid provider-reported,
// non-estimated usage.
type LocalRepairMetrics struct {
	Complete bool                              `json:"complete"`
	Usage    LocalRepairUsage                  `json:"usage"`
	Tools    map[string]LocalRepairToolMetrics `json:"tools"`
}

type localRepairMetricsCollector struct {
	mu      sync.Mutex
	usage   LocalRepairUsage
	tools   map[string]LocalRepairToolMetrics
	invalid bool // provider usage/call aggregation only; tool totals saturate independently
}

func newLocalRepairMetricsCollector() *localRepairMetricsCollector {
	return &localRepairMetricsCollector{
		tools: make(map[string]LocalRepairToolMetrics, 4),
	}
}

func (collector *localRepairMetricsCollector) snapshot() LocalRepairMetrics {
	if collector == nil {
		return LocalRepairMetrics{Tools: map[string]LocalRepairToolMetrics{}}
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	toolsSnapshot := make(map[string]LocalRepairToolMetrics, len(collector.tools))
	for name, metric := range collector.tools {
		toolsSnapshot[name] = metric
	}
	return LocalRepairMetrics{
		Complete: !collector.invalid && collector.usage.ProviderCalls > 0 &&
			collector.usage.UsageReportedCalls == collector.usage.ProviderCalls,
		Usage: collector.usage,
		Tools: toolsSnapshot,
	}
}

func cloneLocalRepairMetrics(metrics LocalRepairMetrics) LocalRepairMetrics {
	cloned := metrics
	cloned.Tools = make(map[string]LocalRepairToolMetrics, len(metrics.Tools))
	for name, metric := range metrics.Tools {
		if localRepairToolNameAllowed(name) {
			cloned.Tools[name] = metric
		}
	}
	return cloned
}

func (collector *localRepairMetricsCollector) observeProviderCall(
	response *providers.LLMResponse,
	latency time.Duration,
) error {
	if collector == nil {
		return nil
	}
	usage, reported, usageErr := normalizeLocalRepairUsage(response)
	hasUsage := response != nil && response.Usage != nil
	latencyMillis := latency.Milliseconds()
	if latencyMillis < 0 {
		latencyMillis = 0
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.usage.ProviderCalls = saturatingLocalRepairAdd(
		collector.usage.ProviderCalls,
		1,
		&collector.invalid,
	)
	collector.usage.LatencyMillis = saturatingLocalRepairAdd(
		collector.usage.LatencyMillis,
		latencyMillis,
		&collector.invalid,
	)
	if usageErr != nil {
		collector.invalid = true
		return usageErr
	}
	if !hasUsage {
		return nil
	}

	next := collector.usage
	var overflow bool
	if reported {
		next.UsageReportedCalls = checkedLocalRepairAdd(next.UsageReportedCalls, 1, &overflow)
	}
	next.PromptTokens = checkedLocalRepairAdd(next.PromptTokens, usage.PromptTokens, &overflow)
	next.CachedTokens = checkedLocalRepairAdd(next.CachedTokens, usage.CachedTokens, &overflow)
	next.CompletionTokens = checkedLocalRepairAdd(
		next.CompletionTokens,
		usage.CompletionTokens,
		&overflow,
	)
	next.ReasoningTokens = checkedLocalRepairAdd(
		next.ReasoningTokens,
		usage.ReasoningTokens,
		&overflow,
	)
	next.TotalTokens = checkedLocalRepairAdd(next.TotalTokens, usage.TotalTokens, &overflow)
	if overflow {
		collector.invalid = true
		return errors.New("local repair provider usage aggregation overflow")
	}
	collector.usage = next
	return nil
}

func normalizeLocalRepairUsage(
	response *providers.LLMResponse,
) (LocalRepairUsage, bool, error) {
	if response == nil || response.Usage == nil {
		return LocalRepairUsage{}, false, nil
	}
	raw := response.Usage
	if raw.PromptTokens < 0 || raw.CachedTokens < 0 ||
		raw.CompletionTokens < 0 || raw.ReasoningTokens < 0 || raw.TotalTokens < 0 {
		return LocalRepairUsage{}, false, errors.New("local repair provider usage is invalid")
	}
	usage := LocalRepairUsage{
		PromptTokens:     int64(raw.PromptTokens),
		CachedTokens:     int64(raw.CachedTokens),
		CompletionTokens: int64(raw.CompletionTokens),
		ReasoningTokens:  int64(raw.ReasoningTokens),
		TotalTokens:      int64(raw.TotalTokens),
	}
	if usage.CachedTokens > usage.PromptTokens ||
		usage.ReasoningTokens > usage.CompletionTokens ||
		usage.PromptTokens > math.MaxInt64-usage.CompletionTokens ||
		usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return LocalRepairUsage{}, false, errors.New("local repair provider usage is invalid")
	}
	return usage, !raw.Estimated, nil
}

func (collector *localRepairMetricsCollector) observeTool(
	kind localRepairToolKind,
	result *tools.ToolResult,
	duration time.Duration,
) {
	if collector == nil {
		return
	}
	name := localRepairToolMetricName(kind)
	if name == "" {
		return
	}
	durationMillis := duration.Milliseconds()
	if durationMillis < 0 {
		durationMillis = 0
	}
	var resultBytes int64
	failed := result == nil
	if result != nil {
		resultBytes = int64(len(result.ForLLM))
		failed = result.IsError || result.Err != nil
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()
	metric := collector.tools[name]
	metric.Calls = saturatingLocalRepairAdd(metric.Calls, 1, nil)
	if failed {
		metric.Failures = saturatingLocalRepairAdd(metric.Failures, 1, nil)
	}
	metric.DurationMillis = saturatingLocalRepairAdd(
		metric.DurationMillis,
		durationMillis,
		nil,
	)
	metric.ResultBytes = saturatingLocalRepairAdd(
		metric.ResultBytes,
		resultBytes,
		nil,
	)
	collector.tools[name] = metric
}

func localRepairToolMetricName(kind localRepairToolKind) string {
	switch kind {
	case localRepairToolRead:
		return "read_file"
	case localRepairToolList:
		return "list_dir"
	case localRepairToolEdit:
		return "edit_file"
	case localRepairToolPatch:
		return "apply_patch"
	default:
		return ""
	}
}

func checkedLocalRepairAdd(left, right int64, overflow *bool) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		if overflow != nil {
			*overflow = true
		}
		return left
	}
	return left + right
}

func saturatingLocalRepairAdd(left, right int64, invalid *bool) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		if invalid != nil {
			*invalid = true
		}
		return math.MaxInt64
	}
	return left + right
}
