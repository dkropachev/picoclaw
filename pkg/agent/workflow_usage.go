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
	key := usage.Reviewer + "\x00" + usage.Model
	aggregate := a.usage[key]
	aggregate.Model = usage.Model
	aggregate.Reviewer = usage.Reviewer
	aggregate.PromptTokens += usage.PromptTokens
	aggregate.CompletionTokens += usage.CompletionTokens
	aggregate.TotalTokens += usage.TotalTokens
	aggregate.CachedTokens += usage.CachedTokens
	aggregate.ReasoningTokens += usage.ReasoningTokens
	aggregate.LatencyMillis += usage.LatencyMillis
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

func workflowAgentUsageFromResponse(
	model string,
	response *providers.LLMResponse,
	latency time.Duration,
) (workflows.AgentUsage, bool) {
	if response == nil {
		return workflows.AgentUsage{}, false
	}
	usage := workflows.AgentUsage{
		Model: strings.TrimSpace(model), LatencyMillis: max(0, latency.Milliseconds()),
	}
	if response.Usage != nil {
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
