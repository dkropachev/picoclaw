package agent

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func admitWorkflowAgentCall(admission workflows.AgentCallAdmission) error {
	if admission == nil {
		return nil
	}
	if err := admission(); err != nil {
		return errors.Join(workflows.ErrAgentCallNotAdmitted, err)
	}
	return nil
}

// workflowAgentUsageAccumulator serializes observer delivery and maintains a
// detached, per-model aggregate. Managed children may report concurrently.
type workflowAgentUsageAccumulator struct {
	mu       sync.Mutex
	usage    map[string]workflows.AgentUsage
	observer workflows.AgentUsageObserver
	err      error
	invalid  bool
}

func newWorkflowAgentUsageAccumulator(
	observer workflows.AgentUsageObserver,
) *workflowAgentUsageAccumulator {
	return &workflowAgentUsageAccumulator{
		usage:    make(map[string]workflows.AgentUsage),
		observer: observer,
	}
}

func (a *workflowAgentUsageAccumulator) Observe(usage workflows.AgentUsage) error {
	if a == nil {
		return nil
	}
	usage.Model = strings.TrimSpace(usage.Model)
	usage.Reviewer = strings.TrimSpace(usage.Reviewer)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validWorkflowAgentUsageObservation(usage) {
		return a.recordUsageErrorWithObservation(usage)
	}
	key := usage.Reviewer + "\x00" + usage.Model
	aggregate := a.usage[key]
	aggregate.Model = usage.Model
	aggregate.Reviewer = usage.Reviewer
	var valid bool
	if aggregate.ProviderCalls, valid = checkedWorkflowUsageInt64(
		aggregate.ProviderCalls, usage.ProviderCalls,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.UsageReportedCalls, valid = checkedWorkflowUsageInt64(
		aggregate.UsageReportedCalls, usage.UsageReportedCalls,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.PromptTokens, valid = checkedWorkflowUsageInt(
		aggregate.PromptTokens, usage.PromptTokens,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.CompletionTokens, valid = checkedWorkflowUsageInt(
		aggregate.CompletionTokens, usage.CompletionTokens,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.TotalTokens, valid = checkedWorkflowUsageInt(
		aggregate.TotalTokens, usage.TotalTokens,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.CachedTokens, valid = checkedWorkflowUsageInt(
		aggregate.CachedTokens, usage.CachedTokens,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.ReasoningTokens, valid = checkedWorkflowUsageInt(
		aggregate.ReasoningTokens, usage.ReasoningTokens,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	if aggregate.LatencyMillis, valid = checkedWorkflowUsageInt64(
		aggregate.LatencyMillis, usage.LatencyMillis,
	); !valid {
		return a.recordUsageErrorWithObservation(usage)
	}
	a.usage[key] = aggregate
	if a.observer != nil {
		if err := a.observer(usage); err != nil {
			if a.err == nil {
				a.err = err
			}
		}
	}
	return a.err
}

func (a *workflowAgentUsageAccumulator) recordUsageError() error {
	a.invalid = true
	if a.err == nil {
		a.err = errors.New("workflow agent usage aggregation is invalid or overflowed")
	}
	return a.err
}

// recordUsageErrorWithObservation preserves the fact and latency of a
// dispatched call whose token fields cannot be trusted. Token/report counts
// from that call are deliberately excluded.
func (a *workflowAgentUsageAccumulator) recordUsageErrorWithObservation(
	usage workflows.AgentUsage,
) error {
	key := usage.Reviewer + "\x00" + usage.Model
	aggregate := a.usage[key]
	aggregate.Model, aggregate.Reviewer = usage.Model, usage.Reviewer
	providerCalls, callsOK := checkedWorkflowUsageInt64(
		aggregate.ProviderCalls,
		usage.ProviderCalls,
	)
	latency, latencyOK := checkedWorkflowUsageInt64(
		aggregate.LatencyMillis,
		usage.LatencyMillis,
	)
	if callsOK && latencyOK && usage.ProviderCalls > 0 {
		aggregate.ProviderCalls = providerCalls
		aggregate.LatencyMillis = latency
		a.usage[key] = aggregate
	}
	return a.recordUsageError()
}

func checkedWorkflowUsageInt(left, right int) (int, bool) {
	maximum := int(^uint(0) >> 1)
	if left < 0 || right < 0 || left > maximum-right {
		return 0, false
	}
	return left + right, true
}

func checkedWorkflowUsageInt64(left, right int64) (int64, bool) {
	const maximum = int64(^uint64(0) >> 1)
	if left < 0 || right < 0 || left > maximum-right {
		return 0, false
	}
	return left + right, true
}

func validWorkflowAgentUsageObservation(usage workflows.AgentUsage) bool {
	if usage.ProviderCalls < 0 || usage.UsageReportedCalls < 0 ||
		usage.UsageReportedCalls > usage.ProviderCalls || usage.PromptTokens < 0 ||
		usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.CachedTokens < 0 ||
		usage.ReasoningTokens < 0 || usage.LatencyMillis < 0 ||
		usage.CachedTokens > usage.PromptTokens ||
		usage.ReasoningTokens > usage.CompletionTokens {
		return false
	}
	if usage.ProviderCalls > 1 || usage.UsageReportedCalls > 1 {
		return false
	}
	if usage.ProviderCalls == 0 && (usage.UsageReportedCalls != 0 ||
		usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 ||
		usage.CachedTokens != 0 || usage.ReasoningTokens != 0 || usage.LatencyMillis != 0) {
		return false
	}
	maximum := int(^uint(0) >> 1)
	return usage.PromptTokens <= maximum-usage.CompletionTokens &&
		usage.TotalTokens == usage.PromptTokens+usage.CompletionTokens
}

func (a *workflowAgentUsageAccumulator) Snapshot() []workflows.AgentUsage {
	if a == nil {
		return []workflows.AgentUsage{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]workflows.AgentUsage, 0, len(a.usage))
	for _, usage := range a.usage {
		out = append(out, usage)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Reviewer == out[j].Reviewer {
			return out[i].Model < out[j].Model
		}
		return out[i].Reviewer < out[j].Reviewer
	})
	return out
}

func (a *workflowAgentUsageAccumulator) Err() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

func (a *workflowAgentUsageAccumulator) Complete() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.invalid {
		return false
	}
	var calls, reported int64
	for _, usage := range a.usage {
		var ok bool
		calls, ok = checkedWorkflowUsageInt64(calls, usage.ProviderCalls)
		if !ok {
			return false
		}
		reported, ok = checkedWorkflowUsageInt64(reported, usage.UsageReportedCalls)
		if !ok {
			return false
		}
	}
	return calls > 0 && calls == reported
}

func workflowAgentUsageFromResponse(
	model string,
	response *providers.LLMResponse,
	latency time.Duration,
) (workflows.AgentUsage, bool) {
	usage := workflows.AgentUsage{
		Model: strings.TrimSpace(model), ProviderCalls: 1,
		LatencyMillis: max(0, latency.Milliseconds()),
	}
	if response != nil && response.Usage != nil {
		if !response.Usage.Estimated {
			usage.UsageReportedCalls = 1
		}
		usage.PromptTokens = response.Usage.PromptTokens
		usage.CompletionTokens = response.Usage.CompletionTokens
		usage.TotalTokens = response.Usage.TotalTokens
		usage.CachedTokens = response.Usage.CachedTokens
		usage.ReasoningTokens = response.Usage.ReasoningTokens
	}
	return usage, true
}

func cloneWorkflowAgentUsage(usage []workflows.AgentUsage) []workflows.AgentUsage {
	if usage == nil {
		return []workflows.AgentUsage{}
	}
	return append([]workflows.AgentUsage(nil), usage...)
}

func (ts *turnState) observeWorkflowAgentResponse(
	model string,
	response *providers.LLMResponse,
	latency time.Duration,
) error {
	usage, ok := workflowAgentUsageFromResponse(model, response, latency)
	if !ok || ts == nil || ts.usage == nil {
		return nil
	}
	return ts.usage.Observe(usage)
}

func (ts *turnState) workflowAgentUsageSnapshot() []workflows.AgentUsage {
	if ts == nil || ts.usage == nil {
		return []workflows.AgentUsage{}
	}
	return ts.usage.Snapshot()
}

func (ts *turnState) workflowAgentUsageError() error {
	if ts == nil || ts.usage == nil {
		return nil
	}
	return ts.usage.Err()
}
