package agent

import (
	"strings"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentLoopOption configures an AgentLoop at construction time.
type AgentLoopOption func(*AgentLoop)

// WithToolPolicy installs the immutable policy used by model-authored tool
// actions. Passing nil is intentional and fails closed; constructors preserve
// legacy behavior by installing CompatibilityAllowToolPolicy explicitly.
func WithToolPolicy(policy tools.ToolPolicy) AgentLoopOption {
	return func(al *AgentLoop) {
		al.toolPolicy = policy
	}
}

// WithRuntimeEvents injects the runtime event bus used for new observation APIs.
//
// The injected bus is treated as externally owned and will not be closed by
// AgentLoop.Close. Passing nil leaves the default owned runtime bus enabled.
func WithRuntimeEvents(bus runtimeevents.Bus) AgentLoopOption {
	return func(al *AgentLoop) {
		if bus == nil {
			return
		}
		al.runtimeEvents = bus
		al.ownsRuntimeEvents = false
	}
}

func WithConfigPath(path string) AgentLoopOption {
	return func(al *AgentLoop) {
		al.configPath = strings.TrimSpace(path)
	}
}

// WithRuntimeStartupBarrier constructs the AgentLoop with root runtime
// admission paused. Gateway startup uses this so constructor-owned background
// services cannot run before the gateway is ready.
func WithRuntimeStartupBarrier() AgentLoopOption {
	return func(al *AgentLoop) {
		al.runtimeGatePaused = true
		al.runtimeGatePauses++
		al.runtimeStartupBarrier = true
	}
}

// WithDeferredEvolutionActivation keeps the constructed evolution bridge inert
// until ActivateEvolution is called. Gateway startup combines this with the
// runtime startup barrier so a scheduled cold path cannot start in the
// constructor-to-readiness window.
func WithDeferredEvolutionActivation() AgentLoopOption {
	return func(al *AgentLoop) {
		al.deferEvolutionActivation = true
	}
}
