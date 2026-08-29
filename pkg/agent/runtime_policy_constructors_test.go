package agent

import (
	"bytes"
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type runtimePolicyConstructorProvider struct{}

func (*runtimePolicyConstructorProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

type runtimePolicyConstructorTool struct{}

func (*runtimePolicyConstructorTool) Name() string { return "runtime_policy_constructor" }

func (*runtimePolicyConstructorTool) Description() string {
	return "Exercises the diagnostic owner cap installed by Agent construction"
}

func (*runtimePolicyConstructorTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
}

func (*runtimePolicyConstructorTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("ok")
}

func TestP015B3ARuntimePolicyConstructorsPreserveExactTupleAndCompatibilityZero(
	t *testing.T,
) {
	strictCfg := runtimePolicyConstructorConfig(t)
	strictBus := bus.NewMessageBus()
	strictExecution := isolation.NewExecutionPolicy(strictCfg.Isolation)
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	strict := NewAgentLoopWithRuntimePolicies(
		strictCfg,
		strictBus,
		&runtimePolicyConstructorProvider{},
		strictExecution,
		enabled,
	)
	t.Cleanup(func() {
		strict.Close()
		strictBus.Close()
	})
	if strict.executionPolicy != strictExecution || strict.diagnosticPolicy != enabled {
		t.Fatal("strict AgentLoop did not retain the exact runtime policy tuple")
	}
	if strict.registry == nil || strict.registry.executionPolicy != strictExecution ||
		strict.registry.diagnosticPolicy != enabled {
		t.Fatal("strict AgentRegistry did not retain the exact runtime policy tuple")
	}

	compatCfg := runtimePolicyConstructorConfig(t)
	compatBus := bus.NewMessageBus()
	compatExecution := isolation.NewExecutionPolicy(compatCfg.Isolation)
	compat := NewAgentLoopWithExecutionPolicy(
		compatCfg,
		compatBus,
		&runtimePolicyConstructorProvider{},
		compatExecution,
	)
	t.Cleanup(func() {
		compat.Close()
		compatBus.Close()
	})
	if compat.executionPolicy != compatExecution {
		t.Fatal("execution-policy compatibility constructor changed its exact policy")
	}
	if compat.diagnosticPolicy != (logger.DiagnosticPolicy{}) || compat.registry == nil ||
		compat.registry.diagnosticPolicy != (logger.DiagnosticPolicy{}) {
		t.Fatal("execution-policy compatibility constructor did not remain safe-only")
	}
}

func TestP015B3AStrictInstanceInstallsDiagnosticOwnerCap(t *testing.T) {
	const canary = "P015_B3A_STRICT_INSTANCE_ARGUMENT_8f67c5e2"
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	execution := isolation.NewExecutionPolicy(config.DefaultConfig().Isolation)

	strictCfg := runtimePolicyConstructorConfig(t)
	strict := NewAgentInstanceWithRuntimePolicies(
		&config.AgentConfig{ID: "strict"},
		&strictCfg.Agents.Defaults,
		strictCfg,
		&runtimePolicyConstructorProvider{},
		execution,
		enabled,
	)
	t.Cleanup(func() { _ = strict.Close() })
	strict.Tools.Register(&runtimePolicyConstructorTool{})
	_, strictRaw := captureP015HookRecords(t, func() {
		ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
		defer revoke()
		result := strict.Tools.Execute(
			ctx,
			"runtime_policy_constructor",
			map[string]any{"value": canary},
		)
		if result == nil || result.IsError {
			t.Fatalf("strict tool result = %#v", result)
		}
	})
	if !bytes.Contains(strictRaw, []byte(canary)) {
		t.Fatal("strict AgentInstance ToolRegistry did not receive its diagnostic owner cap")
	}

	compatCfg := runtimePolicyConstructorConfig(t)
	compat := NewAgentInstanceWithExecutionPolicy(
		&config.AgentConfig{ID: "compat"},
		&compatCfg.Agents.Defaults,
		compatCfg,
		&runtimePolicyConstructorProvider{},
		execution,
	)
	t.Cleanup(func() { _ = compat.Close() })
	compat.Tools.Register(&runtimePolicyConstructorTool{})
	_, compatRaw := captureP015HookRecords(t, func() {
		ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
		defer revoke()
		result := compat.Tools.Execute(
			ctx,
			"runtime_policy_constructor",
			map[string]any{"value": canary},
		)
		if result == nil || result.IsError {
			t.Fatalf("compatibility tool result = %#v", result)
		}
	})
	if bytes.Contains(compatRaw, []byte(canary)) {
		t.Fatal("execution-policy compatibility AgentInstance enabled a raw preview")
	}
}

func runtimePolicyConstructorConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = ""
	cfg.Agents.Defaults.AccountRef = ""
	cfg.Tools = config.ToolsConfig{}
	cfg.Workflows.Enabled = false
	return cfg
}
