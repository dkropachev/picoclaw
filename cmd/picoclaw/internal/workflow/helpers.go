package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func workflowWorkspace() (string, error) {
	cfg, err := workflowConfig()
	if err != nil {
		return "", err
	}
	return cfg.WorkspacePath(), nil
}

func workflowConfig() (*config.Config, error) {
	return internal.LoadConfig()
}

func workflowRunStore(ctx context.Context) (*workflows.FileRunStore, error) {
	cfg, err := workflowConfig()
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

func workflowRuntimeCompatibility() workflows.RuntimeCompatibility {
	return workflows.NormalizeRuntimeCompatibility(workflows.RuntimeCompatibility{
		PicoclawVersion: config.GetVersion(),
		GitCommit:       config.GitCommit,
	})
}

func runWorkflowTool(ctx context.Context, args map[string]any, sessionKey string) (string, error) {
	cfg, err := workflowConfig()
	if err != nil {
		return "", err
	}
	workspace := cfg.WorkspacePath()
	runner := &cliWorkflowRuntimeRunner{configPath: internal.GetConfigPath(), cfg: cfg}
	defer runner.Close()
	executor := &workflows.Executor{
		WorkspaceDir:         workspace,
		DefinitionsDir:       cfg.Workflows.EffectiveDefinitionsDir(),
		Store:                workflows.NewFileRunStore(workspace),
		Tools:                runner,
		Agents:               runner,
		RuntimeCompatibility: workflowRuntimeCompatibility(),
		MaxCallDepth:         cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns:    cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:       cfg.Workflows.EffectiveDefaultTimeout(),
	}
	tool := tools.NewWorkflowTool(executor, workspace, workflowRuntimeCompatibility())
	execCtx := tools.WithToolInboundContext(ctx, "cli", "workflow", "", "")
	execCtx = tools.WithToolSessionContext(execCtx, "", sessionKey, nil)
	result := tool.Execute(execCtx, args)
	if result == nil {
		return "", fmt.Errorf("workflow tool returned nil result")
	}
	if result.IsError {
		return result.ContentForLLM(), fmt.Errorf("%s", result.ContentForLLM())
	}
	return result.ContentForLLM(), nil
}

type cliWorkflowRuntimeRunner struct {
	configPath string
	cfg        *config.Config
	mu         sync.Mutex
	msgBus     *bus.MessageBus
	loop       *agentloop.AgentLoop
}

func (r *cliWorkflowRuntimeRunner) RunAgent(ctx context.Context, req workflows.AgentRequest) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("agent runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return nil, err
	}
	return agentloop.NewWorkflowAgentRunner(r.loop).RunAgent(ctx, req)
}

func (r *cliWorkflowRuntimeRunner) RunTool(ctx context.Context, req workflows.ToolRequest) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("tool runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return nil, err
	}
	runner, err := agentloop.NewWorkflowToolRunner(r.loop, req.AgentID)
	if err != nil {
		return nil, err
	}
	return runner.RunTool(ctx, req)
}

func (r *cliWorkflowRuntimeRunner) ensureLoopLocked() error {
	if r.loop != nil {
		return nil
	}
	if r.cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	provider, modelID, err := providers.CreateProvider(r.cfg)
	if err != nil {
		return err
	}
	if modelID != "" {
		r.cfg.Agents.Defaults.ModelName = modelID
	}
	r.msgBus = bus.NewMessageBus()
	r.loop = agentloop.NewAgentLoop(
		r.cfg,
		r.msgBus,
		provider,
		agentloop.WithConfigPath(r.configPath),
	)
	return nil
}

func (r *cliWorkflowRuntimeRunner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loop != nil {
		r.loop.Close()
		r.loop = nil
	}
	if r.msgBus != nil {
		r.msgBus.Close()
		r.msgBus = nil
	}
	return nil
}

func workflowLocalOptions(cfg *config.Config) []workflows.LocalOption {
	if cfg == nil {
		return nil
	}
	return []workflows.LocalOption{workflows.WithDefinitionsDir(cfg.Workflows.EffectiveDefinitionsDir())}
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
	cfg, err := workflowConfig()
	if err != nil {
		return "", err
	}
	workflow, err := workflows.LoadLocal(ctx, cfg.WorkspacePath(), ref, workflowLocalOptions(cfg)...)
	if err != nil {
		return "", err
	}
	if err := workflows.Validate(workflow); err != nil {
		return "", err
	}
	return fmt.Sprintf("{\n  \"ref\": %q,\n  \"valid\": true\n}", ref), nil
}
