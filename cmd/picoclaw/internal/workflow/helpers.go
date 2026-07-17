package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func workflowWorkspace() (string, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return "", err
	}
	return cfg.WorkspacePath(), nil
}

func workflowRunStore(ctx context.Context) (*workflows.FileRunStore, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return nil, err
	}
	store := workflows.NewFileRunStore(cfg.WorkspacePath())
	days := cfg.Workflows.EffectiveRetentionDays()
	if days > 0 {
		if _, err := store.PruneTerminalRuns(ctx, time.Now().UTC().AddDate(0, 0, -days)); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func runWorkflowTool(ctx context.Context, args map[string]any, sessionKey string) (string, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return "", err
	}
	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return "", err
	}
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	loop := agentloop.NewAgentLoop(cfg, msgBus, provider)
	defer loop.Close()
	defaultAgent := loop.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		return "", fmt.Errorf("no default agent configured")
	}
	tool, ok := defaultAgent.Tools.Get(tools.WorkflowToolName)
	if !ok {
		return "", fmt.Errorf("workflow tool is not registered")
	}
	execCtx := tools.WithToolInboundContext(ctx, "cli", "workflow", "", "")
	execCtx = tools.WithToolSessionContext(execCtx, defaultAgent.ID, sessionKey, nil)
	result := tool.Execute(execCtx, args)
	if result == nil {
		return "", fmt.Errorf("workflow tool returned nil result")
	}
	if result.IsError {
		return result.ContentForLLM(), fmt.Errorf("%s", result.ContentForLLM())
	}
	return result.ContentForLLM(), nil
}

func parseJSONMap(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func parseJSONSecrets(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out, nil
}

func loadAndValidate(ctx context.Context, ref string) (string, error) {
	workspace, err := workflowWorkspace()
	if err != nil {
		return "", err
	}
	workflow, err := workflows.LoadLocal(ctx, workspace, ref)
	if err != nil {
		return "", err
	}
	if err := workflows.Validate(workflow); err != nil {
		return "", err
	}
	return fmt.Sprintf("{\n  \"ref\": %q,\n  \"valid\": true\n}", ref), nil
}
