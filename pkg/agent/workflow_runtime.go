package agent

import (
	"context"
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
	executor := &workflows.Executor{
		WorkspaceDir: agent.Workspace,
		Store:        store,
		Tools: &workflowToolRunner{
			agentID:  agentID,
			registry: agent.Tools,
			loop:     al,
		},
		Agents:            &workflowAgentRunner{loop: al},
		MaxCallDepth:      al.cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns: al.cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:    al.cfg.Workflows.EffectiveDefaultTimeout(),
	}
	return tools.NewWorkflowTool(executor, agent.Workspace)
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

func (r *workflowToolRunner) deliverHandledMedia(ctx context.Context, req workflows.ToolRequest, result *tools.ToolResult) error {
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
	response, err := r.loop.runAgentLoop(ctx, agent, processOptions{
		Dispatch: DispatchRequest{
			SessionKey:     sessionKey,
			InboundContext: &inbound,
			SessionScope:   workflowSessionScope(agentID, sessionKey, req.Delivery),
			UserMessage:    message,
		},
		DefaultResponse:         defaultResponse,
		PromptCacheKey:          promptCacheKey,
		EnableSummary:           !noHistory && historyMode != "read_only",
		SendResponse:            false,
		AllowInterimPicoPublish: false,
		SuppressToolFeedback:    true,
		NoHistory:               noHistory,
		DisablePromptCache:      disablePromptCache,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"text":       response,
		"agent_id":   agentID,
		"session":    sessionKey,
		"history":    historyMode,
		"cache":      workflowCacheMode(req.Cache),
		"cache_key":  promptCacheKey,
		"message_id": strings.TrimSpace(req.MessageID),
	}, nil
}

func workflowAgentMessage(req workflows.AgentRequest) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(req.Prompt) != "" {
		parts = append(parts, strings.TrimSpace(req.Prompt))
	}
	if strings.TrimSpace(req.Context) != "" {
		parts = append(parts, strings.TrimSpace(req.Context))
	}
	if strings.TrimSpace(req.Message) != "" {
		parts = append(parts, strings.TrimSpace(req.Message))
	}
	return strings.Join(parts, "\n\n")
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
	return map[string]any{
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
