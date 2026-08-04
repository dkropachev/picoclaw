package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
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
			dynamic:  true,
		},
		Agents:               &workflowAgentRunner{loop: al},
		RuntimeEvents:        al.runtimeEvents,
		RuntimeCompatibility: workflowRuntimeCompatibility(),
		MaxCallDepth:         al.cfg.Workflows.EffectiveMaxCallDepth(),
		MaxConcurrentRuns:    al.cfg.Workflows.EffectiveMaxConcurrentRuns(),
		DefaultTimeout:       al.cfg.Workflows.EffectiveDefaultTimeout(),
	}
	return tools.NewWorkflowTool(
		executor,
		agent.Workspace,
		workflowRuntimeCompatibility(),
	).ConfigureDevelopmentPublishGate(tools.WorkflowDevelopmentPublishGateConfig{
		WorkflowsEnabled: al.cfg.Workflows.Enabled,
		DefinitionsDir:   definitionsDir,
		MaxCallDepth:     al.cfg.Workflows.EffectiveMaxCallDepth(),
		Resolver:         al,
	})
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
		dynamic:  true,
	}, nil
}

type workflowToolRunner struct {
	agentID  string
	registry *tools.ToolRegistry
	loop     *AgentLoop
	dynamic  bool
}

func (r *workflowToolRunner) RunTool(ctx context.Context, req workflows.ToolRequest) (map[string]any, error) {
	if r == nil || r.registry == nil {
		return nil, fmt.Errorf("tool registry not configured")
	}
	registry := r.registry
	if r.loop != nil && r.dynamic {
		leaseCtx, releaseRuntime, err := r.loop.acquireRuntimeUse(ctx)
		if err != nil {
			return nil, err
		}
		defer releaseRuntime()
		ctx = leaseCtx
		currentRegistry := r.loop.GetRegistry()
		if currentRegistry == nil {
			return nil, fmt.Errorf("agent registry not configured")
		}
		currentAgent, ok := currentRegistry.GetAgent(r.agentID)
		if !ok || currentAgent == nil || currentAgent.Tools == nil {
			return nil, fmt.Errorf("agent %q not found for workflow tool step", r.agentID)
		}
		registry = currentAgent.Tools
	}
	if req.MCP {
		registeredTool, ok := registry.Get(req.Name)
		if !ok {
			return nil, fmt.Errorf(
				"MCP tool %q/%q is not available",
				req.MCPServer,
				req.MCPTool,
			)
		}
		if !workflowMCPToolMatches(registeredTool, req.MCPServer, req.MCPTool) {
			return nil, fmt.Errorf(
				"MCP tool %q/%q does not match the registered wrapper for %q",
				req.MCPServer,
				req.MCPTool,
				req.Name,
			)
		}
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
	result := registry.ExecuteWithContext(execCtx, req.Name, args, delivery.Channel, delivery.ChatID, nil)
	if err := r.deliverHandledMedia(ctx, req, result); err != nil {
		return workflowToolResultOutputs(result), err
	}
	outputs := workflowToolResultOutputs(result)
	if result != nil && result.IsError {
		return outputs, fmt.Errorf("%s", result.ContentForLLM())
	}
	return outputs, nil
}

func workflowMCPToolMatches(tool tools.Tool, serverName, toolName string) bool {
	wrapped, ok := tool.(*tools.MCPTool)
	if !ok || wrapped == nil {
		return false
	}
	registeredServer, registeredTool := wrapped.MCPIdentity()
	return registeredServer == serverName && registeredTool == toolName
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
	loop                   *AgentLoop
	newEphemeralSessionKey func() string
}

var _ workflows.ReadOnlySessionCapturer = (*workflowAgentRunner)(nil)

type workflowAgentRunOptions struct {
	ModelName       string
	ReasoningEffort string
	NoTools         bool
}

type workflowAgentTextRunner func(message string, noHistoryOverride bool, runOptions workflowAgentRunOptions) (string, error)

func (r *workflowAgentRunner) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	if r == nil || r.loop == nil {
		return nil, fmt.Errorf("agent loop not configured")
	}
	leaseCtx, releaseRuntime, err := r.loop.acquireRuntimeUse(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()

	agentID := strings.TrimSpace(ref.AgentID)
	if agentID != ref.AgentID || !routing.IsCanonicalAgentID(agentID) {
		return nil, fmt.Errorf("read_only workflow agent ID must be an exact canonical ID")
	}
	registry := r.loop.GetRegistry()
	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}
	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil {
		return nil, fmt.Errorf("workflow agent %q not found", agentID)
	}
	snapshot, _, err := workflowReadOnlySessionSnapshot(
		leaseCtx,
		agent,
		agentID,
		ref.Session,
	)
	if err != nil {
		return nil, err
	}
	var mediaReader media.SnapshotReader
	if reader, ok := r.loop.mediaStore.(media.SnapshotReader); ok {
		mediaReader = reader
	}
	frozenSnapshot, frozenMedia, err := session.FreezeSessionSnapshotMedia(
		leaseCtx,
		snapshot,
		mediaReader,
	)
	if err != nil {
		return nil, fmt.Errorf("read_only workflow agent session media capture: %w", err)
	}
	revision, err := workflowSessionSnapshotRevision(frozenSnapshot)
	if err != nil {
		return nil, err
	}
	return &workflows.FrozenReadOnlySession{
		AgentID:         agentID,
		Snapshot:        workflowCloneSessionSnapshot(frozenSnapshot),
		HistoryRevision: revision,
		FrozenMedia:     frozenMedia.Clone(),
	}, nil
}

func (r *workflowAgentRunner) RunAgent(ctx context.Context, req workflows.AgentRequest) (map[string]any, error) {
	if r == nil || r.loop == nil {
		return nil, fmt.Errorf("agent loop not configured")
	}
	leaseCtx, releaseRuntime, err := r.loop.acquireRuntimeUse(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()
	ctx = leaseCtx

	historyModeInput := strings.TrimSpace(req.History)
	historyMode := strings.ToLower(historyModeInput)
	readOnlyDecision := historyMode == "read_only"
	ephemeralDecision := req.EphemeralSession
	privateDecision := req.FrozenReadOnlySession != nil
	privateExecution := req.PrivateContext || privateDecision
	if privateDecision {
		if historyModeInput != "read_only" {
			return nil, fmt.Errorf("private workflow agent session requires history: read_only")
		}
		if strings.TrimSpace(req.Tools) != workflows.AgentToolsNone {
			return nil, fmt.Errorf("private workflow agent session requires tools: none")
		}
		if strings.TrimSpace(req.Session) != "" {
			return nil, fmt.Errorf("private workflow agent session cannot use a live session key")
		}
		if ephemeralDecision {
			return nil, fmt.Errorf("private workflow agent session cannot be ephemeral")
		}
	}
	if readOnlyDecision && workflowAgentToolsMode(req.Tools) != workflows.AgentToolsNone {
		return nil, fmt.Errorf("read_only workflow agent history requires tools: none")
	}
	if ephemeralDecision {
		if historyModeInput != "none" {
			return nil, fmt.Errorf("ephemeral workflow agent session requires history: none")
		}
		if strings.TrimSpace(req.Cache) != "none" {
			return nil, fmt.Errorf("ephemeral workflow agent session requires cache: none")
		}
		if strings.TrimSpace(req.Tools) != workflows.AgentToolsNone {
			return nil, fmt.Errorf("ephemeral workflow agent session requires tools: none")
		}
		if strings.TrimSpace(req.Session) != "" {
			return nil, fmt.Errorf("ephemeral workflow agent session cannot use a durable session key")
		}
	}
	isolatedDecision := readOnlyDecision || ephemeralDecision
	if !isolatedDecision {
		if hooksErr := r.loop.ensureHooksInitialized(ctx); hooksErr != nil {
			return nil, hooksErr
		}
		if strings.TrimSpace(req.Tools) != workflows.AgentToolsNone {
			if mcpErr := r.loop.ensureMCPInitialized(ctx); mcpErr != nil {
				return nil, mcpErr
			}
		}
	}
	agentID := strings.TrimSpace(req.AgentID)
	if readOnlyDecision && !routing.IsCanonicalAgentID(agentID) {
		return nil, fmt.Errorf("read_only workflow agent ID must be an exact canonical ID")
	}
	if privateDecision && agentID != req.AgentID {
		return nil, fmt.Errorf("private workflow agent ID must be an exact canonical ID")
	}
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
	if ephemeralDecision {
		sessionKey = r.ephemeralSessionKey()
	} else if sessionKey == "" && !readOnlyDecision {
		sessionKey = "workflow:agent:" + agentID
	}
	var (
		readOnlySnapshot *session.SessionSnapshot
		historyRevision  string
	)
	if readOnlyDecision {
		var (
			snapshot    session.SessionSnapshot
			revision    string
			snapshotErr error
		)
		if privateDecision {
			snapshot, revision, snapshotErr = workflowFrozenReadOnlySessionSnapshot(
				ctx,
				req.FrozenReadOnlySession,
				agentID,
			)
		} else {
			snapshot, revision, snapshotErr = workflowReadOnlySessionSnapshot(
				ctx,
				agent,
				agentID,
				sessionKey,
			)
		}
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		readOnlySnapshot = &snapshot
		historyRevision = revision
		if privateDecision {
			sessionKey = workflowPrivateSessionIdentity(agentID, revision)
		} else {
			sessionKey = snapshot.Key
		}
	}
	noHistory := historyMode == "none"
	promptCacheKey, disablePromptCache := workflowPromptCacheKey(req.Cache, agentID, sessionKey)
	if privateDecision {
		disablePromptCache = workflowCacheMode(req.Cache) == "none"
		if disablePromptCache {
			promptCacheKey = ""
		} else {
			promptCacheKey = sessionKey
		}
	}
	var inbound *bus.InboundContext
	if !ephemeralDecision && !privateDecision {
		value := workflowInboundContext(req.Delivery, agentID)
		inbound = &value
	}
	runOnce := func(runMessage string, noHistoryOverride bool, runOptions workflowAgentRunOptions) (string, error) {
		if ephemeralDecision {
			return r.loop.askSideQuestionWithOptions(
				ctx,
				agent,
				&processOptions{
					Dispatch: DispatchRequest{
						SessionKey:  sessionKey,
						UserMessage: runMessage,
					},
					ModelNameOverride:       strings.TrimSpace(runOptions.ModelName),
					ReasoningEffortOverride: strings.TrimSpace(runOptions.ReasoningEffort),
					NoHistory:               true,
					DisableTools:            true,
					DisablePromptCache:      true,
				},
				runMessage,
				sideQuestionExecutionOptions{
					disablePromptCache:     true,
					disableSessionAffinity: true,
					detachProviderMessages: true,
					skipHooks:              true,
					rejectToolCalls:        true,
					privateExecution:       privateExecution,
				},
			)
		}
		if readOnlySnapshot != nil {
			var scope *session.SessionScope
			if !privateDecision {
				scope = session.CloneScope(readOnlySnapshot.Scope)
			}
			return r.loop.askSideQuestionWithOptions(
				ctx,
				agent,
				&processOptions{
					Dispatch: DispatchRequest{
						SessionKey:     sessionKey,
						InboundContext: inbound,
						SessionScope:   scope,
						UserMessage:    runMessage,
					},
					PromptCacheKey:          promptCacheKey,
					ModelNameOverride:       strings.TrimSpace(runOptions.ModelName),
					ReasoningEffortOverride: strings.TrimSpace(runOptions.ReasoningEffort),
					DisableTools:            true,
					DisablePromptCache:      disablePromptCache,
				},
				runMessage,
				sideQuestionExecutionOptions{
					contextSnapshot: &sideQuestionContextSnapshot{
						history: readOnlySnapshot.History,
						summary: readOnlySnapshot.Summary,
					},
					promptCacheKey:     promptCacheKey,
					disablePromptCache: disablePromptCache,
					skipHooks:          true,
					rejectToolCalls:    true,
					privateExecution:   privateExecution,
				},
			)
		}
		return r.loop.runAgentLoop(ctx, agent, processOptions{
			Dispatch: DispatchRequest{
				SessionKey:     sessionKey,
				InboundContext: inbound,
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
			DisableTools:            workflowAgentToolsDisabled(req.Tools) || runOptions.NoTools,
			DisablePromptCache:      disablePromptCache,
		})
	}
	publicSessionKey := sessionKey
	publicPromptCacheKey := promptCacheKey
	publicCacheMode := req.Cache
	publicMessageID := req.MessageID
	if ephemeralDecision {
		publicSessionKey = workflows.AgentSessionEphemeral
	}
	if privateDecision {
		publicSessionKey = workflows.AgentSessionPrivate
		publicPromptCacheKey = ""
		if disablePromptCache {
			publicCacheMode = "none"
		} else {
			publicCacheMode = "session"
		}
		publicMessageID = ""
	}
	managedReq := req
	if privateDecision {
		managedReq.Delivery = workflows.Delivery{}
		managedReq.MessageID = ""
		managedReq.Cache = publicCacheMode
		managedReq.FrozenReadOnlySession = nil
	}
	if strategy := workflowManagedSplitStrategy(managedReq, agent); strategy != "" {
		if managedErr := r.ensureWorkflowManagedProviders(agent, req.Managed); managedErr != nil {
			return nil, fmt.Errorf(
				"initialize managed model aliases for workflow agent %q: %w",
				agentID,
				managedErr,
			)
		}
		outputs, managedErr := r.runManagedSplit(
			managedReq,
			agent,
			agentID,
			publicSessionKey,
			historyMode,
			publicCacheMode,
			publicPromptCacheKey,
			strategy,
			runOnce,
		)
		if readOnlySnapshot != nil && outputs != nil {
			outputs["history_revision"] = historyRevision
		}
		if privateDecision && outputs != nil {
			outputs["session_mode"] = workflows.AgentSessionPrivate
		}
		if ephemeralDecision && outputs != nil {
			outputs["session_mode"] = workflows.AgentSessionEphemeral
		}
		return outputs, managedErr
	}
	requestedRunOptions := workflowAgentRunOptions{
		NoTools: workflowAgentToolsDisabled(req.Tools),
	}
	response, err := runOnce(message, false, requestedRunOptions)
	if err != nil {
		return nil, err
	}
	outputs := workflowAgentBaseOutputs(
		response,
		agentID,
		publicSessionKey,
		historyMode,
		publicCacheMode,
		publicPromptCacheKey,
		publicMessageID,
		req.Tools,
	)
	if readOnlySnapshot != nil {
		outputs["history_revision"] = historyRevision
	}
	if privateDecision {
		outputs["session_mode"] = workflows.AgentSessionPrivate
	}
	if ephemeralDecision {
		outputs["session_mode"] = workflows.AgentSessionEphemeral
	}
	outputs["managed"] = workflowManagedMetadata(req, agent)
	if req.Output != nil && req.Output.Enabled() {
		structured := workflows.ValidateAgentStructuredOutput(response, req.Output)
		repairs := 0
		for !structured.Valid && repairs < req.Output.RepairAttempts {
			repairs++
			repairMessage := workflowStructuredRepairMessage(response, structured.Error, req.Output)
			repaired, repairErr := runOnce(repairMessage, true, requestedRunOptions)
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

func newWorkflowEphemeralSessionKey() string {
	return "workflow:ephemeral:" + rand.Text()
}

func (r *workflowAgentRunner) ephemeralSessionKey() string {
	if r != nil && r.newEphemeralSessionKey != nil {
		return r.newEphemeralSessionKey()
	}
	return newWorkflowEphemeralSessionKey()
}

const maxWorkflowReadOnlySessionKeyBytes = 4096

const workflowPrivateSessionIdentityDomain = "picoclaw/workflow/private-read-only-session/v1\x00"

func workflowCloneSessionSnapshot(snapshot session.SessionSnapshot) session.SessionSnapshot {
	snapshot.History = session.CloneMessages(snapshot.History)
	snapshot.Scope = session.CloneScope(snapshot.Scope)
	snapshot.Aliases = append([]string(nil), snapshot.Aliases...)
	return snapshot
}

func workflowFrozenReadOnlySessionSnapshot(
	ctx context.Context,
	frozen *workflows.FrozenReadOnlySession,
	agentID string,
) (session.SessionSnapshot, string, error) {
	if frozen == nil {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session snapshot is required",
		)
	}
	frozenAgentID := strings.TrimSpace(frozen.AgentID)
	if frozenAgentID != frozen.AgentID || !routing.IsCanonicalAgentID(frozenAgentID) {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session has invalid agent identity",
		)
	}
	if frozenAgentID != agentID {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session belongs to another agent",
		)
	}
	snapshot := workflowCloneSessionSnapshot(frozen.Snapshot)
	if snapshot.Key != strings.TrimSpace(snapshot.Key) ||
		snapshot.Key == "" ||
		!utf8.ValidString(snapshot.Key) ||
		len(snapshot.Key) > maxWorkflowReadOnlySessionKeyBytes {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session key is invalid",
		)
	}
	if err := workflowReadOnlySessionOwner(snapshot, agentID); err != nil {
		return session.SessionSnapshot{}, "", err
	}
	revision, err := workflowSessionSnapshotRevision(snapshot)
	if err != nil {
		return session.SessionSnapshot{}, "", err
	}
	if frozen.HistoryRevision == "" || frozen.HistoryRevision != revision {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session revision does not match its snapshot",
		)
	}
	materialized, err := session.MaterializeSessionSnapshotMedia(
		ctx,
		snapshot,
		frozen.FrozenMedia,
	)
	if err != nil {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"private workflow agent session media is invalid: %w",
			err,
		)
	}
	return materialized, revision, nil
}

func workflowPrivateSessionIdentity(agentID, historyRevision string) string {
	sum := sha256.Sum256([]byte(
		workflowPrivateSessionIdentityDomain + agentID + "\x00" + historyRevision,
	))
	return fmt.Sprintf("workflow:private:%x", sum[:])
}

func workflowReadOnlySessionSnapshot(
	ctx context.Context,
	agent *AgentInstance,
	agentID string,
	sessionKey string,
) (session.SessionSnapshot, string, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent history requires an existing session",
		)
	}
	if !utf8.ValidString(sessionKey) || len(sessionKey) > maxWorkflowReadOnlySessionKeyBytes {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent session key is invalid",
		)
	}
	if agent == nil || agent.Sessions == nil {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent session store is unavailable",
		)
	}
	reader, ok := agent.Sessions.(session.SnapshotReader)
	if !ok {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent session store does not support exact snapshots",
		)
	}
	snapshot, found, err := reader.ReadSessionSnapshot(ctx, sessionKey)
	if err != nil {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent session snapshot: %w",
			err,
		)
	}
	if !found || strings.TrimSpace(snapshot.Key) == "" {
		return session.SessionSnapshot{}, "", fmt.Errorf(
			"read_only workflow agent session was not found",
		)
	}
	if ownerErr := workflowReadOnlySessionOwner(snapshot, agentID); ownerErr != nil {
		return session.SessionSnapshot{}, "", ownerErr
	}
	revision, err := workflowSessionSnapshotRevision(snapshot)
	if err != nil {
		return session.SessionSnapshot{}, "", err
	}
	return snapshot, revision, nil
}

func workflowReadOnlySessionOwner(snapshot session.SessionSnapshot, agentID string) error {
	owner := ""
	if snapshot.Scope != nil {
		owner = strings.TrimSpace(snapshot.Scope.AgentID)
		if !routing.IsCanonicalAgentID(owner) {
			return fmt.Errorf("read_only workflow agent session has invalid owner metadata")
		}
	} else if parsed := session.ParseLegacyAgentSessionKey(snapshot.Key); parsed != nil {
		owner = strings.TrimSpace(parsed.AgentID)
		if !routing.IsCanonicalAgentID(owner) {
			return fmt.Errorf("read_only workflow agent legacy session has invalid owner")
		}
	}
	if owner == "" {
		return fmt.Errorf("read_only workflow agent session owner is unavailable")
	}
	if owner != agentID {
		return fmt.Errorf("read_only workflow agent session belongs to another agent")
	}
	return nil
}

func workflowSessionSnapshotRevision(snapshot session.SessionSnapshot) (string, error) {
	type snapshotFields struct {
		Key     string                `json:"Key"`
		History []providers.Message   `json:"History"`
		Summary string                `json:"Summary"`
		Scope   *session.SessionScope `json:"Scope"`
	}
	type toolCallInternalFields struct {
		Name             string         `json:"name,omitempty"`
		Arguments        map[string]any `json:"arguments"`
		ThoughtSignature string         `json:"thought_signature,omitempty"`
	}
	type contentBlockInternalFields struct {
		PromptLayer  string `json:"prompt_layer,omitempty"`
		PromptSlot   string `json:"prompt_slot,omitempty"`
		PromptSource string `json:"prompt_source,omitempty"`
	}
	type messageInternalFields struct {
		PromptLayer  string                       `json:"prompt_layer,omitempty"`
		PromptSlot   string                       `json:"prompt_slot,omitempty"`
		PromptSource string                       `json:"prompt_source,omitempty"`
		SystemParts  []contentBlockInternalFields `json:"system_parts,omitempty"`
		ToolCalls    []toolCallInternalFields     `json:"tool_calls,omitempty"`
	}

	internal := make([]messageInternalFields, len(snapshot.History))
	for messageIndex, message := range snapshot.History {
		projected := messageInternalFields{
			PromptLayer:  message.PromptLayer,
			PromptSlot:   message.PromptSlot,
			PromptSource: message.PromptSource,
			SystemParts:  make([]contentBlockInternalFields, len(message.SystemParts)),
			ToolCalls:    make([]toolCallInternalFields, len(message.ToolCalls)),
		}
		for blockIndex, block := range message.SystemParts {
			projected.SystemParts[blockIndex] = contentBlockInternalFields{
				PromptLayer:  block.PromptLayer,
				PromptSlot:   block.PromptSlot,
				PromptSource: block.PromptSource,
			}
		}
		for callIndex, call := range message.ToolCalls {
			projected.ToolCalls[callIndex] = toolCallInternalFields{
				Name:             call.Name,
				Arguments:        call.Arguments,
				ThoughtSignature: call.ThoughtSignature,
			}
		}
		internal[messageIndex] = projected
	}

	encoded, err := json.Marshal(struct {
		Snapshot snapshotFields          `json:"snapshot"`
		Internal []messageInternalFields `json:"internal"`
	}{
		Snapshot: snapshotFields{
			Key:     snapshot.Key,
			History: snapshot.History,
			Summary: snapshot.Summary,
			Scope:   snapshot.Scope,
		},
		Internal: internal,
	})
	if err != nil {
		return "", fmt.Errorf("read_only workflow agent snapshot is not serializable: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum[:]), nil
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
	text, agentID, sessionKey, historyMode, cacheMode, promptCacheKey, messageID, toolsMode string,
) map[string]any {
	return map[string]any{
		"text":       text,
		"agent_id":   agentID,
		"session":    sessionKey,
		"history":    historyMode,
		"cache":      workflowCacheMode(cacheMode),
		"cache_key":  promptCacheKey,
		"message_id": strings.TrimSpace(messageID),
		"tools":      workflowAgentToolsMode(toolsMode),
	}
}

func workflowAgentToolsMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), workflows.AgentToolsNone) {
		return workflows.AgentToolsNone
	}
	return workflows.AgentToolsInherit
}

func workflowAgentToolsDisabled(mode string) bool {
	return workflowAgentToolsMode(mode) == workflows.AgentToolsNone
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
			"reason":      "initial agent execution optimization layer uses one visible agent run",
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
	targetChildPromptSource        string
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
		"Agent execution optimization split calibration.",
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
