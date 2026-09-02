//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tokenizer"
)

// seahorseContextManager adapts seahorse.Engine to agent.ContextManager.
type seahorseContextManager struct {
	engine      *seahorse.Engine
	sessions    session.SessionStore // for startup bootstrap
	al          *AgentLoop           // for resolving the agent that owns a session
	engines     map[string]*seahorse.Engine
	engineIDs   []string
	closeEngine seahorseEngineCloser
	closeMu     sync.Mutex
	closed      bool
}

// newSeahorseContextManager creates a seahorse-backed ContextManager.
func newSeahorseContextManager(
	raw json.RawMessage,
	al *AgentLoop,
) (ContextManager, error) {
	return newSeahorseContextManagerWithContext(context.Background(), raw, al)
}

func newSeahorseContextManagerWithContext(
	ctx context.Context,
	raw json.RawMessage,
	al *AgentLoop,
) (ContextManager, error) {
	return newSeahorseContextManagerWithDependencies(
		ctx,
		raw,
		al,
		defaultSeahorseContextDependencies(),
	)
}

// providerToCompleteFn wraps providers.LLMProvider as a seahorse.CompleteFn.
func providerToCompleteFn(provider providers.LLMProvider, model string) seahorse.CompleteFn {
	return func(ctx context.Context, prompt string, opts seahorse.CompleteOptions) (string, error) {
		resp, err := provider.Chat(
			ctx,
			[]providers.Message{{Role: "user", Content: prompt}},
			nil, // no tools for summarization
			model,
			map[string]any{
				"max_tokens":       opts.MaxTokens,
				"temperature":      opts.Temperature,
				"prompt_cache_key": "seahorse",
			},
		)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}
}

func agentProviderToCompleteFn(al *AgentLoop, agent *AgentInstance) seahorse.CompleteFn {
	agentID := ""
	if agent != nil {
		agentID = agent.ID
	}
	return func(ctx context.Context, prompt string, opts seahorse.CompleteOptions) (string, error) {
		if al == nil {
			return "", fmt.Errorf("seahorse completion: agent loop is not initialized")
		}
		if strings.TrimSpace(agentID) == "" {
			return "", fmt.Errorf("seahorse completion: agent ID is not configured")
		}

		origin, _ := al.runtimeDiagnosticOriginFromLease(ctx)
		leaseCtx, releaseRuntime, err := al.acquireRuntimeUseFromOrigin(ctx, origin)
		if err != nil {
			return "", fmt.Errorf("seahorse completion: acquire current runtime: %w", err)
		}
		defer releaseRuntime()

		registry := al.GetRegistry()
		if registry == nil {
			return "", fmt.Errorf("seahorse completion: agent registry is not configured")
		}
		currentAgent, ok := registry.GetAgent(agentID)
		if !ok || currentAgent == nil {
			return "", fmt.Errorf(
				"seahorse completion: agent %q is not present in the current runtime",
				agentID,
			)
		}
		provider, model, err := al.resolveContextCompletionTarget(
			currentAgent,
			prompt,
			agentID+":seahorse",
		)
		if err != nil {
			return "", err
		}
		return providerToCompleteFn(provider, model)(leaseCtx, prompt, opts)
	}
}

func (m *seahorseContextManager) engineForSession(
	sessionKey string,
) (*seahorse.Engine, *AgentInstance) {
	if m == nil {
		return nil, nil
	}
	var resolvedAgent *AgentInstance
	if m.al != nil {
		if agent := m.al.agentForSession(sessionKey); agent != nil {
			resolvedAgent = agent
		}
	}
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if m.closed {
		return nil, resolvedAgent
	}
	if resolvedAgent != nil {
		if engine := m.engines[resolvedAgent.ID]; engine != nil {
			return engine, resolvedAgent
		}
	}
	return m.engine, resolvedAgent
}

// Close releases every per-agent Seahorse engine. It is idempotent so reload
// and shutdown paths can safely converge on the same manager.
func (m *seahorseContextManager) Close() error {
	if m == nil {
		return nil
	}

	m.closeMu.Lock()
	if m.closed {
		m.closeMu.Unlock()
		return nil
	}
	m.closed = true
	engines := m.engines
	defaultEngine := m.engine
	engineIDs := append([]string(nil), m.engineIDs...)
	closeEngine := m.closeEngine
	m.engines = nil
	m.engine = nil
	m.engineIDs = nil
	m.closeMu.Unlock()

	seen := make(map[*seahorse.Engine]struct{}, len(engines))
	seenIDs := make(map[string]struct{}, len(engineIDs))
	for _, agentID := range engineIDs {
		seenIDs[agentID] = struct{}{}
	}
	for agentID := range engines {
		if _, exists := seenIDs[agentID]; !exists {
			engineIDs = append(engineIDs, agentID)
		}
	}
	sort.Strings(engineIDs)
	closeErrors := make([]error, 0)
	for _, agentID := range engineIDs {
		engine := engines[agentID]
		if engine == nil {
			continue
		}
		if _, duplicate := seen[engine]; duplicate {
			continue
		}
		seen[engine] = struct{}{}
		if err := closeSeahorseEngine(closeEngine, engine); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf(
				"close Seahorse engine for agent %q: %w",
				agentID,
				err,
			))
		}
	}
	if defaultEngine != nil {
		if _, duplicate := seen[defaultEngine]; !duplicate {
			if err := closeSeahorseEngine(closeEngine, defaultEngine); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf(
					"close default Seahorse engine: %w",
					err,
				))
			}
		}
	}
	return errors.Join(closeErrors...)
}

// Assemble builds budget-aware context from seahorse SQLite.
func (m *seahorseContextManager) Assemble(ctx context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("seahorse assemble: nil request")
	}

	budget := req.Budget
	if budget <= 0 {
		budget = 100000
	}

	// Reserve space for model response (spec lines 1400-1410)
	effectiveBudget := budget - req.MaxTokens
	if effectiveBudget <= 0 {
		// MaxTokens >= budget is a configuration problem
		// Use 50% as minimum to avoid guaranteed overflow
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentMaxTokensGreaterThanOrEqualToBudgetUsing50PercentFallback,
			logger.NewSafeFields(
				logger.SafeInt(logger.FieldContextWindow, budget),
				logger.SafeInt(logger.FieldMaxTokens, req.MaxTokens),
				logger.SafeBool(logger.FieldFallback, true),
			),
		)
		effectiveBudget = budget / 2
	}

	engine, _ := m.engineForSession(req.SessionKey)
	if engine == nil {
		return nil, fmt.Errorf("seahorse assemble: no engine for session")
	}
	result, err := engine.Assemble(ctx, req.SessionKey, seahorse.AssembleInput{
		Budget: effectiveBudget,
	})
	if err != nil {
		return nil, fmt.Errorf("seahorse assemble: %w", err)
	}

	history := seahorseToProviderMessages(result)

	// Summary is already formatted as XML with system prompt addition by assembler
	return &AssembleResponse{
		History: history,
		Summary: result.Summary,
	}, nil
}

// Compact compresses conversation history via seahorse summarization.
func (m *seahorseContextManager) Compact(ctx context.Context, req *CompactRequest) error {
	if req == nil {
		return nil
	}
	engine, _ := m.engineForSession(req.SessionKey)
	if engine == nil {
		return fmt.Errorf("seahorse compact: no engine for session")
	}

	// For retry (LLM overflow), use aggressive CompactUntilUnder to guarantee
	// context shrinks below budget (spec lines ~1410).
	if req.Reason == ContextCompressReasonRetry && req.Budget > 0 {
		_, err := engine.CompactUntilUnder(ctx, req.SessionKey, req.Budget)
		return err
	}

	_, err := engine.Compact(ctx, req.SessionKey, seahorse.CompactInput{
		Force:  req.Reason == ContextCompressReasonRetry,
		Budget: &req.Budget,
	})
	return err
}

// Ingest records a message into seahorse SQLite.
// All existing sessions are bootstrapped at startup, so this only ingests new messages.
func (m *seahorseContextManager) Ingest(ctx context.Context, req *IngestRequest) error {
	if req == nil {
		return nil
	}
	engine, agent := m.engineForSession(req.SessionKey)
	if engine == nil {
		return fmt.Errorf("seahorse ingest: no engine for session")
	}
	sessions := m.sessions
	if agent != nil && agent.Sessions != nil {
		sessions = agent.Sessions
	}
	excluded, err := isReviewScopedSession(ctx, sessions, req.SessionKey)
	if err != nil {
		return fmt.Errorf("seahorse ingest scope: %w", err)
	}
	if excluded {
		return nil
	}

	msg := providerToSeahorseMessage(req.Message)
	_, err = engine.Ingest(ctx, req.SessionKey, []seahorse.Message{msg})
	return err
}

// Clear removes all stored context for a session (Seahorse and session SQLite).
func (m *seahorseContextManager) Clear(ctx context.Context, sessionKey string) error {
	engine, agent := m.engineForSession(sessionKey)
	if engine == nil {
		return fmt.Errorf("seahorse clear: no engine for session")
	}
	if err := engine.ClearSession(ctx, sessionKey); err != nil {
		return err
	}
	// The session may belong to a routed (non-default) agent whose session
	// store differs from the bootstrap store, so clear the owner's store.
	sessions := m.sessions
	if agent != nil && agent.Sessions != nil {
		sessions = agent.Sessions
	}
	if sessions != nil {
		sessions.SetHistory(sessionKey, []providers.Message{})
		sessions.SetSummary(sessionKey, "")
		return sessions.Save(sessionKey)
	}
	return nil
}

func (m *seahorseContextManager) bootstrapAgentSession(
	ctx context.Context,
	agent *AgentInstance,
	engine *seahorse.Engine,
	sessionKey string,
) error {
	if agent == nil || agent.Sessions == nil || engine == nil {
		return nil
	}

	bootstrapKey := sessionKey
	var history []providers.Message
	if reader, ok := agent.Sessions.(session.SnapshotReader); ok {
		snapshot, found, err := reader.ReadSessionSnapshot(ctx, sessionKey)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			logger.WarnSafeCF(
				logger.ComponentSeahorse,
				logger.DiagnosticMessageSeahorseBootstrapSnapshot,
				logger.NewSafeFields(
					agentDiagnosticSessionField(sessionKey),
					agentDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
			return nil
		}
		if !found || strings.TrimSpace(snapshot.Key) == "" {
			return nil
		}
		if isReviewSessionScope(snapshot.Scope) {
			return nil
		}
		bootstrapKey = snapshot.Key
		history = snapshot.History
	} else {
		if metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok &&
			isReviewSessionScope(metadata.GetSessionScope(sessionKey)) {
			return nil
		}
		history = agent.Sessions.GetHistory(sessionKey)
	}
	if len(history) == 0 {
		return nil
	}

	// Convert provider messages to seahorse messages
	msgs := make([]seahorse.Message, len(history))
	for i, h := range history {
		msgs[i] = providerToSeahorseMessage(h)
	}

	if err := engine.Bootstrap(ctx, bootstrapKey, msgs); err != nil {
		return fmt.Errorf("bootstrap session %q: %w", bootstrapKey, err)
	}
	return nil
}

// providerToSeahorseMessage converts a providers.Message to a seahorse.Message.
func providerToSeahorseMessage(msg protocoltypes.Message) seahorse.Message {
	result := seahorse.Message{
		Role:             msg.Role,
		Content:          msg.Content,
		ModelName:        msg.ModelName,
		ReasoningContent: msg.ReasoningContent,
		TokenCount:       tokenizer.EstimateMessageTokens(msg),
		CreatedAt:        normalizeSeahorseMessageCreatedAt(msg.CreatedAt),
	}

	// Convert ToolCalls → MessageParts
	for _, tc := range msg.ToolCalls {
		name := tc.Name
		args := "{}"
		if tc.Function != nil {
			if name == "" {
				name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				args = tc.Function.Arguments
			}
		}
		if name == "" {
			continue
		}
		part := seahorse.MessagePart{
			Type:       "tool_use",
			Name:       name,
			Arguments:  args,
			ToolCallID: tc.ID,
		}
		result.Parts = append(result.Parts, part)
	}

	// Convert tool result
	if msg.ToolCallID != "" {
		part := seahorse.MessagePart{
			Type:       "tool_result",
			ToolCallID: msg.ToolCallID,
			Text:       msg.Content,
		}
		result.Parts = append(result.Parts, part)
	}

	for _, part := range msg.Parts {
		switch part.Type {
		case string(promptir.PartTypeText), "":
			if part.Text != "" {
				result.Parts = append(result.Parts, seahorse.MessagePart{
					Type: "text",
					Text: part.Text,
				})
			}
		case string(promptir.PartTypeImage), string(promptir.PartTypeAudio), string(promptir.PartTypeFile):
			if part.URI != "" {
				result.Parts = append(result.Parts, seahorse.MessagePart{
					Type:     "media",
					MediaURI: part.URI,
					MimeType: part.MIMEType,
				})
			}
		}
	}

	// Convert media attachments
	if len(msg.Parts) == 0 {
		for _, mediaURI := range msg.Media {
			part := seahorse.MessagePart{
				Type:     "media",
				MediaURI: mediaURI,
			}
			result.Parts = append(result.Parts, part)
		}
	}

	return result
}

func normalizeSeahorseMessageCreatedAt(createdAt *time.Time) time.Time {
	if createdAt == nil || createdAt.IsZero() {
		return time.Time{}
	}
	return createdAt.UTC().Truncate(time.Second)
}

// seahorseToProviderMessages converts a seahorse.AssembleResult to []providers.Message.
func seahorseToProviderMessages(result *seahorse.AssembleResult) []protocoltypes.Message {
	messages := make([]protocoltypes.Message, 0, len(result.Messages))

	// Convert assembled messages (which already include summary XML messages)
	for _, msg := range result.Messages {
		pm := protocoltypes.Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ModelName:        msg.ModelName,
			ReasoningContent: msg.ReasoningContent,
		}

		// Reconstruct ToolCalls from parts
		for _, part := range msg.Parts {
			if part.Type == "tool_use" {
				pm.ToolCalls = append(pm.ToolCalls, protocoltypes.ToolCall{
					ID:   part.ToolCallID,
					Type: "function", // Required by OpenAI-compatible APIs (GLM, etc.)
					Function: &protocoltypes.FunctionCall{
						Name:      part.Name,
						Arguments: part.Arguments,
					},
				})
			}
			if part.Type == "tool_result" {
				pm.ToolCallID = part.ToolCallID
				if pm.Content == "" && part.Text != "" {
					pm.Content = part.Text
				}
			}
			if part.Type == "text" && part.Text != "" {
				pm.Parts = append(pm.Parts, promptir.Part{
					Type: string(promptir.PartTypeText),
					Text: part.Text,
				})
			}
			if part.Type == "media" && part.MediaURI != "" {
				pm.Media = append(pm.Media, part.MediaURI)
				pm.Parts = append(pm.Parts, promptir.Part{
					Type:     promptIRPartTypeFromMime(part.MimeType),
					URI:      part.MediaURI,
					MIMEType: part.MimeType,
				})
			}
		}

		messages = append(messages, pm)
	}

	return messages
}

func promptIRPartTypeFromMime(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		return string(promptir.PartTypeImage)
	case strings.HasPrefix(mimeType, "audio/"):
		return string(promptir.PartTypeAudio)
	default:
		return string(promptir.PartTypeFile)
	}
}

func init() {
	if err := registerContextManagerWithContext(
		"seahorse",
		newSeahorseContextManager,
		newSeahorseContextManagerWithContext,
	); err != nil {
		panic(fmt.Sprintf("register seahorse context manager: %v", err))
	}
}
