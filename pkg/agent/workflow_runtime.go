package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newWorkflowTool(al *AgentLoop, agentID string, agent *AgentInstance) tools.Tool {
	store := workflows.NewFileRunStore(agent.Workspace)
	definitionsDir := workflowDefinitionsDir(al)
	executor := &workflows.Executor{
		WorkspaceDir:   agent.Workspace,
		DefinitionsDir: definitionsDir,
		Store:          store,
		Tools: &workflowToolRunner{
			agentID:  agentID,
			registry: agent.Tools,
			loop:     al,
		},
		Agents:               &workflowAgentRunner{loop: al},
		RuntimeEvents:        al.runtimeEvents,
		RuntimeCompatibility: workflowRuntimeCompatibility(),
		MaxCallDepth:         al.cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns:    al.cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:       al.cfg.Workflows.EffectiveDefaultTimeout(),
	}
	return tools.NewWorkflowTool(executor, agent.Workspace, workflowRuntimeCompatibility())
}

func workflowDefinitionsDir(al *AgentLoop) string {
	if al == nil || al.cfg == nil {
		return workflows.DefaultDefinitionsDir
	}
	return al.cfg.Workflows.EffectiveDefinitionsDir()
}

// NewWorkflowAgentRunner exposes the agent-step workflow runner for HTTP and
// other runtimes that own an AgentLoop but do not run inside the agent package.
func NewWorkflowAgentRunner(al *AgentLoop) workflows.AgentRunner {
	return &workflowAgentRunner{loop: al}
}

// NewWorkflowToolRunner exposes the tool-step workflow runner for HTTP and
// other runtimes that own an AgentLoop but do not run inside the agent package.
func NewWorkflowToolRunner(al *AgentLoop, agentID string) (workflows.ToolRunner, error) {
	if al == nil {
		return nil, fmt.Errorf("agent loop not configured")
	}
	registry := al.GetRegistry()
	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}
	agentID = strings.TrimSpace(agentID)
	var agent *AgentInstance
	if agentID != "" {
		var ok bool
		agent, ok = registry.GetAgent(agentID)
		if !ok {
			return nil, fmt.Errorf("agent %q not found for workflow tool step", agentID)
		}
	} else {
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return nil, fmt.Errorf("no agent available for workflow tool step")
	}
	if agentID == "" {
		agentID = agent.ID
	}
	if agent.Tools == nil {
		return nil, fmt.Errorf("tool registry not configured")
	}
	return &workflowToolRunner{
		agentID:  agentID,
		registry: agent.Tools,
		loop:     al,
	}, nil
}

type workflowToolRunner struct {
	agentID  string
	registry *tools.ToolRegistry
	loop     *AgentLoop
}

func (r *workflowToolRunner) RunTool(ctx context.Context, req workflows.ToolRequest) (map[string]any, error) {
	if r == nil || r.registry == nil {
		return nil, fmt.Errorf("tool registry not configured")
	}
	args := cloneAnyMap(req.Args)
	delivery := req.Delivery
	if strings.EqualFold(req.Name, tools.WorkflowToolName) {
		return nil, fmt.Errorf("workflow steps cannot call the workflow tool recursively")
	}
	if strings.EqualFold(req.Name, "message") && delivery.ReplyToMessageID != "" {
		if _, exists := args["reply_to_message_id"]; !exists {
			args["reply_to_message_id"] = delivery.ReplyToMessageID
		}
	}
	execCtx := tools.WithToolInboundContext(
		ctx,
		delivery.Channel,
		delivery.ChatID,
		delivery.MessageID,
		delivery.ReplyToMessageID,
	)
	execCtx = tools.WithToolTopicContext(execCtx, delivery.TopicID)
	execCtx = tools.WithToolSessionContext(
		execCtx,
		r.agentID,
		req.Session,
		workflowSessionScope(r.agentID, req.Session, delivery),
	)
	result := r.registry.ExecuteWithContext(execCtx, req.Name, args, delivery.Channel, delivery.ChatID, nil)
	if err := r.deliverHandledMedia(ctx, req, result); err != nil {
		return workflowToolResultOutputs(result), err
	}
	outputs := workflowToolResultOutputs(result)
	if result != nil && result.IsError {
		return outputs, fmt.Errorf("%s", result.ContentForLLM())
	}
	return outputs, nil
}

func (r *workflowToolRunner) deliverHandledMedia(
	ctx context.Context,
	req workflows.ToolRequest,
	result *tools.ToolResult,
) error {
	if r == nil || r.loop == nil || result == nil || len(result.Media) == 0 || !result.ResponseHandled {
		return nil
	}
	delivery := req.Delivery
	parts := make([]bus.MediaPart, 0, len(result.Media))
	for _, ref := range result.Media {
		part := bus.MediaPart{Ref: ref}
		if r.loop.mediaStore != nil {
			if _, meta, err := r.loop.mediaStore.ResolveWithMeta(ref); err == nil {
				part.Filename = meta.Filename
				part.ContentType = meta.ContentType
				part.Type = inferMediaType(meta.Filename, meta.ContentType)
			}
		}
		parts = append(parts, part)
	}
	outboundMedia := bus.OutboundMediaMessage{
		Channel:    delivery.Channel,
		ChatID:     delivery.ChatID,
		Context:    workflowInboundContext(delivery, r.agentID),
		AgentID:    r.agentID,
		SessionKey: req.Session,
		Scope:      outboundScopeFromSessionScope(workflowSessionScope(r.agentID, req.Session, delivery)),
		Parts:      parts,
	}
	if r.loop.channelManager != nil && delivery.Channel != "" && !constants.IsInternalChannel(delivery.Channel) {
		if err := r.loop.channelManager.SendMedia(ctx, outboundMedia); err != nil {
			logger.WarnCF("workflow", "Failed to deliver handled workflow media",
				map[string]any{
					"agent_id": r.agentID,
					"tool":     req.Name,
					"channel":  delivery.Channel,
					"chat_id":  delivery.ChatID,
					"error":    err.Error(),
				})
			return fmt.Errorf("failed to deliver workflow attachment: %w", err)
		}
		return nil
	}
	if r.loop.bus != nil {
		if err := r.loop.bus.PublishOutboundMedia(ctx, outboundMedia); err != nil {
			return err
		}
		result.ResponseHandled = false
	}
	return nil
}

type workflowAgentRunner struct {
	loop *AgentLoop
}

type workflowAgentRunOptions struct {
	ModelName       string
	ReasoningEffort string
	NoTools         bool
}

type workflowAgentTextRunner func(message string, noHistoryOverride bool, runOptions workflowAgentRunOptions) (string, error)

func (r *workflowAgentRunner) RunAgent(ctx context.Context, req workflows.AgentRequest) (map[string]any, error) {
	if r == nil || r.loop == nil {
		return nil, fmt.Errorf("agent loop not configured")
	}
	if err := r.loop.ensureHooksInitialized(ctx); err != nil {
		return nil, err
	}
	if err := r.loop.ensureMCPInitialized(ctx); err != nil {
		return nil, err
	}
	agentID := strings.TrimSpace(req.AgentID)
	registry := r.loop.GetRegistry()
	agent, ok := registry.GetAgent(agentID)
	if !ok {
		if agentID != "" {
			return nil, fmt.Errorf("workflow agent %q not found", agentID)
		}
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return nil, fmt.Errorf("no agent available for workflow step")
	}
	if agentID == "" {
		agentID = agent.ID
	}
	message := workflowAgentMessage(req)
	if message == "" {
		return nil, fmt.Errorf("agent workflow step message is required")
	}
	sessionKey := strings.TrimSpace(req.Session)
	if sessionKey == "" {
		sessionKey = "workflow:agent:" + agentID
	}
	historyMode := strings.ToLower(strings.TrimSpace(req.History))
	noHistory := historyMode == "none"
	promptCacheKey, disablePromptCache := workflowPromptCacheKey(req.Cache, agentID, sessionKey)
	var restoreHistory []providers.Message
	var restoreSummary string
	if historyMode == "read_only" && agent.Sessions != nil {
		restoreHistory = cloneProviderMessages(agent.Sessions.GetHistory(sessionKey))
		restoreSummary = agent.Sessions.GetSummary(sessionKey)
		defer func() {
			agent.Sessions.SetHistory(sessionKey, restoreHistory)
			agent.Sessions.SetSummary(sessionKey, restoreSummary)
		}()
	}
	inbound := workflowInboundContext(req.Delivery, agentID)
	runOnce := func(runMessage string, noHistoryOverride bool, runOptions workflowAgentRunOptions) (string, error) {
		return r.loop.runAgentLoop(ctx, agent, processOptions{
			Dispatch: DispatchRequest{
				SessionKey:     sessionKey,
				InboundContext: &inbound,
				SessionScope:   workflowSessionScope(agentID, sessionKey, req.Delivery),
				UserMessage:    runMessage,
			},
			DefaultResponse:         defaultResponse,
			PromptCacheKey:          promptCacheKey,
			ModelNameOverride:       strings.TrimSpace(runOptions.ModelName),
			ReasoningEffortOverride: strings.TrimSpace(runOptions.ReasoningEffort),
			EnableSummary:           !noHistoryOverride && !noHistory && historyMode != "read_only",
			SendResponse:            false,
			AllowInterimPicoPublish: false,
			SuppressToolFeedback:    true,
			NoHistory:               noHistory || noHistoryOverride,
			DisableTools:            runOptions.NoTools,
			DisablePromptCache:      disablePromptCache,
		})
	}
	if strategy := workflowManagedSplitStrategy(req, agent); strategy != "" {
		if err := r.ensureWorkflowManagedProviders(agent, req.Managed); err != nil {
			logger.WarnCF("workflow", "Failed to initialize managed model provider", map[string]any{
				"agent_id": agentID,
				"error":    err.Error(),
			})
		}
		return r.runManagedSplit(
			req,
			agent,
			agentID,
			sessionKey,
			historyMode,
			req.Cache,
			promptCacheKey,
			strategy,
			runOnce,
		)
	}
	response, err := runOnce(message, false, workflowAgentRunOptions{})
	if err != nil {
		return nil, err
	}
	outputs := map[string]any{
		"text":       response,
		"agent_id":   agentID,
		"session":    sessionKey,
		"history":    historyMode,
		"cache":      workflowCacheMode(req.Cache),
		"cache_key":  promptCacheKey,
		"message_id": strings.TrimSpace(req.MessageID),
		"managed":    workflowManagedMetadata(req, agent),
	}
	if req.Output != nil && req.Output.Enabled() {
		structured := workflows.ValidateAgentStructuredOutput(response, req.Output)
		repairs := 0
		for !structured.Valid && repairs < req.Output.RepairAttempts {
			repairs++
			repairMessage := workflowStructuredRepairMessage(response, structured.Error, req.Output)
			repaired, repairErr := runOnce(repairMessage, true, workflowAgentRunOptions{})
			if repairErr != nil {
				outputs["structured_valid"] = false
				outputs["structured_error"] = repairErr.Error()
				return outputs, repairErr
			}
			response = repaired
			outputs["text"] = response
			structured = workflows.ValidateAgentStructuredOutput(response, req.Output)
		}
		outputs["structured_valid"] = structured.Valid
		outputs["structured_repairs"] = repairs
		if structured.RawJSON != "" {
			outputs["structured_json"] = structured.RawJSON
		}
		if structured.Structured != nil {
			outputs["structured"] = structured.Structured
		}
		if structured.Error != "" {
			outputs["structured_error"] = structured.Error
		}
		if !structured.Valid {
			return outputs, fmt.Errorf("agent structured output invalid: %s", structured.Error)
		}
	}
	return outputs, nil
}

func workflowRunStructuredAgentWithOptions(
	message string,
	contract *workflows.AgentOutputContract,
	runOnce workflowAgentTextRunner,
	runOptions workflowAgentRunOptions,
) (string, workflows.StructuredOutputResult, int, error) {
	text, err := runOnce(message, true, runOptions)
	if err != nil {
		return "", workflows.StructuredOutputResult{Valid: false, Error: err.Error()}, 0, err
	}
	structured := workflows.ValidateAgentStructuredOutput(text, contract)
	repairs := 0
	for !structured.Valid && contract != nil && repairs < contract.RepairAttempts {
		repairs++
		repaired, repairErr := runOnce(
			workflowStructuredRepairMessage(text, structured.Error, contract),
			true,
			runOptions,
		)
		if repairErr != nil {
			return text, workflows.StructuredOutputResult{Valid: false, Error: repairErr.Error()}, repairs, repairErr
		}
		text = repaired
		structured = workflows.ValidateAgentStructuredOutput(text, contract)
	}
	if !structured.Valid {
		return text, structured, repairs, fmt.Errorf("agent structured output invalid: %s", structured.Error)
	}
	return text, structured, repairs, nil
}

func workflowAgentBaseOutputs(
	text, agentID, sessionKey, historyMode, cacheMode, promptCacheKey, messageID string,
) map[string]any {
	return map[string]any{
		"text":       text,
		"agent_id":   agentID,
		"session":    sessionKey,
		"history":    historyMode,
		"cache":      workflowCacheMode(cacheMode),
		"cache_key":  promptCacheKey,
		"message_id": strings.TrimSpace(messageID),
	}
}

func workflowAgentMessage(req workflows.AgentRequest) string {
	parts := make([]string, 0, 6)
	if strings.TrimSpace(req.Prompt) != "" {
		parts = append(parts, strings.TrimSpace(req.Prompt))
	}
	if strings.TrimSpace(req.Context) != "" {
		parts = append(parts, strings.TrimSpace(req.Context))
	}
	if scope := workflowScopeMessage(req.Scope); scope != "" {
		parts = append(parts, scope)
	}
	if strings.TrimSpace(req.Message) != "" {
		parts = append(parts, strings.TrimSpace(req.Message))
	}
	if req.Output != nil {
		if instruction := strings.TrimSpace(req.Output.Instruction()); instruction != "" {
			parts = append(parts, instruction)
		}
	}
	return strings.Join(parts, "\n\n")
}

func workflowScopeMessage(scope any) string {
	if scope == nil {
		return ""
	}
	data, err := json.MarshalIndent(scope, "", "  ")
	if err != nil {
		return fmt.Sprintf("Assigned scope:\n%v", scope)
	}
	return "Assigned scope:\n```json\n" + string(data) + "\n```"
}

func workflowStructuredRepairMessage(previous, validationError string, contract *workflows.AgentOutputContract) string {
	parts := []string{
		"Your previous response did not satisfy the required structured output contract.",
		"Return only corrected JSON. Do not include markdown or prose outside JSON.",
	}
	if strings.TrimSpace(validationError) != "" {
		parts = append(parts, "Validation error:\n"+strings.TrimSpace(validationError))
	}
	if contract != nil {
		if instruction := strings.TrimSpace(contract.Instruction()); instruction != "" {
			parts = append(parts, instruction)
		}
	}
	parts = append(parts, "Previous response:\n"+strings.TrimSpace(previous))
	return strings.Join(parts, "\n\n")
}

func workflowManagedMetadata(req workflows.AgentRequest, agent *AgentInstance) map[string]any {
	mode := workflowManagedMode(req.Managed)
	scopeItems := workflowScopeItems(req.Scope)
	tasks := []string(nil)
	model := ""
	if agent != nil && agent.Definition.Agent != nil {
		tasks = append(tasks, agent.Definition.Agent.Tasks...)
	}
	if agent != nil {
		model = strings.TrimSpace(agent.Model)
	}
	metadata := map[string]any{
		"enabled":                 mode != "off",
		"mode":                    mode,
		"strategy":                "single_run",
		"agent_tasks":             tasks,
		"agent_task_count":        len(tasks),
		"scope_count":             len(scopeItems),
		"estimated_prompt_tokens": workflows.EstimateAgentPayloadTokens(workflowAgentMessage(req)),
		"estimated_scope_tokens":  workflows.EstimateAgentPayloadTokens(req.Scope),
		"split": map[string]any{
			"status":      "not_split",
			"child_count": 0,
			"reason":      "initial managed execution layer uses one visible agent run",
		},
		"calibration": map[string]any{
			"status": "not_run",
			"reason": "single_run strategy does not require calibration",
		},
		"optimization": map[string]any{
			"model": map[string]any{
				"selected": model,
				"changed":  false,
				"reason":   "model optimization telemetry only in this layer",
			},
			"effort": map[string]any{
				"changed": false,
				"reason":  "effort optimization telemetry only in this layer",
			},
		},
	}
	if agent == nil {
		metadata["optimization"].(map[string]any)["model"] = map[string]any{
			"changed": false,
			"reason":  "agent unavailable",
		}
	}
	return metadata
}

type workflowManagedExecutionOptions struct {
	mode                           string
	maxItemsPerChunk               int
	maxTasksPerChunk               int
	maxParallelChildren            int
	adaptiveChunking               bool
	targetChildPromptTokens        int
	calibrationEnabled             bool
	calibrationSampleSize          int
	calibrationTaskSampleSize      int
	calibrationRequiredMatches     int
	calibrationMaxTrials           int
	calibrationCacheEnabled        bool
	calibrationCacheMaxInterval    int
	calibrationSimilarityThreshold float64
	modelOptimization              bool
	effortOptimization             bool
	modelCandidates                []workflowManagedModelCandidate
	requestedSplitStrategy         string
	estimatedOutputTokens          int
}

func workflowManagedOptions(raw any) workflowManagedExecutionOptions {
	options := workflowManagedExecutionOptions{
		mode:                           workflowManagedMode(raw),
		maxItemsPerChunk:               8,
		maxTasksPerChunk:               2,
		maxParallelChildren:            4,
		adaptiveChunking:               true,
		targetChildPromptTokens:        12000,
		calibrationEnabled:             true,
		calibrationSampleSize:          6,
		calibrationTaskSampleSize:      3,
		calibrationRequiredMatches:     1,
		calibrationMaxTrials:           1,
		calibrationCacheEnabled:        true,
		calibrationCacheMaxInterval:    16,
		calibrationSimilarityThreshold: 0.72,
		modelOptimization:              true,
		effortOptimization:             true,
		estimatedOutputTokens:          1000,
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return options
	}
	if n := intFromAny(values["max_items_per_chunk"]); n > 0 {
		options.maxItemsPerChunk = n
	} else if n := intFromAny(values["maxItemsPerChunk"]); n > 0 {
		options.maxItemsPerChunk = n
	}
	if n := intFromAny(values["calibration_sample_size"]); n > 0 {
		options.calibrationSampleSize = n
	} else if n := intFromAny(values["calibrationSampleSize"]); n > 0 {
		options.calibrationSampleSize = n
	}
	if n := intFromAny(values["max_tasks_per_chunk"]); n > 0 {
		options.maxTasksPerChunk = n
	} else if n := intFromAny(values["maxTasksPerChunk"]); n > 0 {
		options.maxTasksPerChunk = n
	}
	if n := intFromAny(values["max_parallel_children"]); n > 0 {
		options.maxParallelChildren = n
	} else if n := intFromAny(values["maxParallelChildren"]); n > 0 {
		options.maxParallelChildren = n
	}
	if enabled, exists := boolMapValue(values, "adaptive_chunking", "adaptiveChunking"); exists {
		options.adaptiveChunking = enabled
	}
	if n := intFromAny(values["target_child_prompt_tokens"]); n > 0 {
		options.targetChildPromptTokens = n
	} else if n := intFromAny(values["targetChildPromptTokens"]); n > 0 {
		options.targetChildPromptTokens = n
	}
	if n := intFromAny(values["estimated_output_tokens"]); n > 0 {
		options.estimatedOutputTokens = n
	} else if n := intFromAny(values["estimatedOutputTokens"]); n > 0 {
		options.estimatedOutputTokens = n
	}
	if calibration, ok := values["calibration"].(map[string]any); ok {
		if enabled, exists := calibration["enabled"].(bool); exists {
			options.calibrationEnabled = enabled
		}
		if n := intFromAny(calibration["sample_size"]); n > 0 {
			options.calibrationSampleSize = n
		} else if n := intFromAny(calibration["sampleSize"]); n > 0 {
			options.calibrationSampleSize = n
		}
		if n := intFromAny(calibration["task_sample_size"]); n > 0 {
			options.calibrationTaskSampleSize = n
		} else if n := intFromAny(calibration["taskSampleSize"]); n > 0 {
			options.calibrationTaskSampleSize = n
		}
		if n := intFromAny(calibration["required_matches"]); n > 0 {
			options.calibrationRequiredMatches = n
		} else if n := intFromAny(calibration["requiredMatches"]); n > 0 {
			options.calibrationRequiredMatches = n
		}
		if n := intFromAny(calibration["max_trials"]); n > 0 {
			options.calibrationMaxTrials = n
		} else if n := intFromAny(calibration["maxTrials"]); n > 0 {
			options.calibrationMaxTrials = n
		}
		if enabled, exists := boolMapValue(calibration, "cache_enabled", "cacheEnabled"); exists {
			options.calibrationCacheEnabled = enabled
		}
		if n := intFromAny(calibration["cache_max_interval"]); n > 0 {
			options.calibrationCacheMaxInterval = n
		} else if n := intFromAny(calibration["cacheMaxInterval"]); n > 0 {
			options.calibrationCacheMaxInterval = n
		}
		if n := floatFromAny(calibration["similarity_threshold"]); n > 0 {
			options.calibrationSimilarityThreshold = n
		} else if n := floatFromAny(calibration["similarityThreshold"]); n > 0 {
			options.calibrationSimilarityThreshold = n
		}
	}
	if optimize, ok := values["optimize"].(map[string]any); ok {
		if enabled, exists := optimize["model"].(bool); exists {
			options.modelOptimization = enabled
		}
		if enabled, exists := optimize["effort"].(bool); exists {
			options.effortOptimization = enabled
		}
		options.modelCandidates = parseWorkflowManagedModelCandidates(optimize["model_candidates"])
	}
	if optimization, ok := values["optimization"].(map[string]any); ok {
		if enabled, exists := optimization["model"].(bool); exists {
			options.modelOptimization = enabled
		} else if model, ok := optimization["model"].(map[string]any); ok {
			if enabled, exists := model["enabled"].(bool); exists {
				options.modelOptimization = enabled
			}
			options.modelCandidates = parseWorkflowManagedModelCandidates(model["candidates"])
		}
		if enabled, exists := optimization["effort"].(bool); exists {
			options.effortOptimization = enabled
		} else if effort, ok := optimization["effort"].(map[string]any); ok {
			if enabled, exists := effort["enabled"].(bool); exists {
				options.effortOptimization = enabled
			}
		}
	}
	if split := strings.TrimSpace(fmt.Sprint(values["split"])); split != "" && split != "<nil>" {
		options.requestedSplitStrategy = strings.ToLower(split)
	}
	if strategy := strings.TrimSpace(fmt.Sprint(values["strategy"])); strategy != "" && strategy != "<nil>" {
		options.requestedSplitStrategy = strings.ToLower(strategy)
	}
	return options
}

func boolMapValue(values map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}

func workflowChunkScope(scope []any, maxItems int) [][]any {
	if len(scope) == 0 {
		return nil
	}
	if maxItems <= 0 {
		maxItems = len(scope)
	}
	chunks := make([][]any, 0, (len(scope)+maxItems-1)/maxItems)
	for start := 0; start < len(scope); start += maxItems {
		end := start + maxItems
		if end > len(scope) {
			end = len(scope)
		}
		chunks = append(chunks, append([]any(nil), scope[start:end]...))
	}
	return chunks
}

func workflowManagedCalibrationMessage(req workflows.AgentRequest, label string) string {
	return strings.Join([]string{
		"Managed execution split calibration.",
		"Calibration label: " + label + ".",
		"Produce the same kind of structured output you would produce in the real run.",
		workflowAgentMessage(req),
	}, "\n\n")
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n)
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

func workflowManagedMode(raw any) string {
	switch v := raw.(type) {
	case nil:
		return "off"
	case bool:
		if v {
			return "auto"
		}
		return "off"
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "false", "off", "none":
			return "off"
		case "true", "on", "auto":
			return "auto"
		default:
			return strings.ToLower(strings.TrimSpace(v))
		}
	case map[string]any:
		if enabled, ok := v["enabled"].(bool); ok && !enabled {
			return "off"
		}
		if mode := strings.ToLower(stringMapValue(v, "mode")); mode != "" {
			return mode
		}
		return "auto"
	default:
		return "auto"
	}
}

func workflowScopeItems(scope any) []any {
	switch v := scope.(type) {
	case nil:
		return nil
	case []any:
		return v
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	case map[string]any:
		if items, ok := v["items"]; ok {
			return workflowScopeItems(items)
		}
		return []any{v}
	default:
		return []any{v}
	}
}

func workflowInboundContext(delivery workflows.Delivery, senderID string) bus.InboundContext {
	return bus.NormalizeInboundMessage(bus.InboundMessage{Context: bus.InboundContext{
		Channel:          delivery.Channel,
		ChatID:           delivery.ChatID,
		TopicID:          delivery.TopicID,
		SenderID:         senderID,
		MessageID:        delivery.MessageID,
		ReplyToMessageID: delivery.ReplyToMessageID,
		ReplyHandles:     cloneStringMap(delivery.ReplyHandles),
		Raw: map[string]string{
			"workflow": "true",
		},
	}}).Context
}

func workflowSessionScope(agentID, sessionKey string, delivery workflows.Delivery) *session.SessionScope {
	values := map[string]string{
		"workflow_session": sessionKey,
	}
	if delivery.ChatID != "" {
		values["chat"] = delivery.ChatID
	}
	if delivery.TopicID != "" {
		values["topic"] = delivery.TopicID
	}
	return &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    agentID,
		Channel:    delivery.Channel,
		Dimensions: []string{"workflow"},
		Values:     values,
	}
}

func workflowPromptCacheKey(mode, agentID, sessionKey string) (string, bool) {
	switch workflowCacheMode(mode) {
	case "none":
		return "", true
	case "agent":
		return strings.TrimSpace(agentID), false
	case "session":
		return strings.TrimSpace(sessionKey), false
	default:
		if key, ok := strings.CutPrefix(strings.TrimSpace(mode), "key:"); ok {
			return strings.TrimSpace(key), false
		}
		return strings.TrimSpace(sessionKey), false
	}
}

func workflowCacheMode(mode string) string {
	mode = strings.TrimSpace(mode)
	switch {
	case mode == "":
		return "session"
	case mode == "session", mode == "agent", mode == "none":
		return mode
	case strings.HasPrefix(mode, "key:") && strings.TrimSpace(strings.TrimPrefix(mode, "key:")) != "":
		return mode
	default:
		return "session"
	}
}

func workflowToolResultOutputs(result *tools.ToolResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"text":             result.ContentForLLM(),
		"for_llm":          result.ForLLM,
		"for_user":         result.ForUser,
		"silent":           result.Silent,
		"is_error":         result.IsError,
		"async":            result.Async,
		"media":            append([]string(nil), result.Media...),
		"artifact_tags":    append([]string(nil), result.ArtifactTags...),
		"response_handled": result.ResponseHandled,
	}
	if parsed, ok := workflowToolJSONOutput(result.ContentForLLM()); ok {
		out["json"] = parsed
		if object, ok := parsed.(map[string]any); ok {
			for key, value := range object {
				if _, exists := out[key]; !exists {
					out[key] = value
				}
			}
		}
	}
	return out
}

func workflowToolJSONOutput(text string) (any, bool) {
	text = strings.TrimSpace(text)
	if text == "" || (!strings.HasPrefix(text, "{") && !strings.HasPrefix(text, "[")) {
		return nil, false
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
