package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func (p *Pipeline) tryConfiguredStreamingLLM(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
	messagesForCall []providers.Message,
	toolDefsForCall []providers.ToolDefinition,
) (*providers.LLMResponse, bool, error) {
	exec.streamingPublisher = nil
	exec.streamingFallback = false
	if !p.configuredStreamingEligible(ts, exec) {
		return nil, false, nil
	}
	streamProvider, ok := exec.activeProvider.(providers.StreamingProvider)
	if !ok {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticModelField(exec.activeModel),
				agentDiagnosticReasonField("provider_not_streaming"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumUnavailable),
			),
		)
		return nil, false, nil
	}

	var streamer bus.Streamer
	var streamerOK bool
	if scopedBus, scoped := p.Bus.(interfaces.TurnScopedMessageBus); scoped {
		streamer, streamerOK = scopedBus.GetStreamerForTurn(
			ctx,
			ts.channel,
			ts.chatID,
			ts.sessionKey,
			ts.turnUXID,
		)
	} else {
		streamer, streamerOK = p.Bus.GetStreamer(
			ctx,
			ts.channel,
			ts.chatID,
			ts.sessionKey,
		)
	}
	if !streamerOK || streamer == nil {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticChatField(ts.chatID),
				agentDiagnosticModelField(exec.activeModel),
				agentDiagnosticReasonField("streamer_unavailable"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumUnavailable),
			),
		)
		return nil, false, nil
	}
	if admissionErr := admitWorkflowAgentCall(ts.opts.callAdmission); admissionErr != nil {
		return nil, true, admissionErr
	}

	publisher := &streamingChunkPublisher{
		streamer:  streamer,
		channel:   ts.channel,
		chatID:    ts.chatID,
		modelName: exec.llmModelName,
		ts:        ts,
	}

	logger.DebugSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentConfiguredStreamingEnabled,
		logger.NewSafeFields(
			agentDiagnosticAgentField(ts.agent.ID),
			agentDiagnosticChannelField(ts.channel),
			agentDiagnosticChatField(ts.chatID),
			agentDiagnosticModelField(exec.llmModel),
			logger.SafeBool(logger.FieldStreaming, true),
		),
	)

	chunkCount := 0
	firstChunkAt := time.Time{}
	lastChunkAt := time.Time{}
	recordChunk := func() {
		now := time.Now()
		chunkCount++
		if firstChunkAt.IsZero() {
			firstChunkAt = now
		}
		lastChunkAt = now
	}
	var response *providers.LLMResponse
	var streamErr error
	if eventProvider, ok := exec.activeProvider.(providers.StreamingEventProvider); ok {
		response, streamErr = eventProvider.ChatStreamEvents(
			ctx,
			messagesForCall,
			cloneToolDefinitions(toolDefsForCall),
			exec.llmModel,
			exec.llmOpts,
			func(chunk providers.StreamChunk) {
				recordChunk()
				if !exec.suppressReasoning && strings.TrimSpace(chunk.ReasoningContent) != "" {
					publisher.UpdateReasoning(ctx, chunk.ReasoningContent)
				}
				if strings.TrimSpace(chunk.Content) != "" {
					publisher.Update(ctx, chunk.Content)
				}
			},
		)
	} else {
		response, streamErr = streamProvider.ChatStream(
			ctx,
			messagesForCall,
			cloneToolDefinitions(toolDefsForCall),
			exec.llmModel,
			exec.llmOpts,
			func(accumulated string) {
				recordChunk()
				publisher.Update(ctx, accumulated)
			},
		)
	}
	logConfiguredStreamingSummary(ts, exec, chunkCount, firstChunkAt, lastChunkAt, streamErr)
	if streamErr == nil {
		if updateErr := publisher.Err(); updateErr != nil {
			if publisher.Published() {
				logger.WarnSafeCF(
					logger.ComponentAgent,
					logger.DiagnosticMessageAgentChatStreamUpdateFailedAfterVisibleOutput,
					logger.NewSafeFields(
						agentDiagnosticAgentField(ts.agent.ID),
						agentDiagnosticChannelField(ts.channel),
						agentDiagnosticModelField(exec.llmModel),
						agentDiagnosticErrorField(logger.ErrorClassTransport, updateErr),
					),
				)
				return nil, true, configuredStreamingVisibleError{err: updateErr}
			}
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentChatStreamUpdateFailedBeforeVisibleOutputRetryingWithChat,
				logger.NewSafeFields(
					agentDiagnosticAgentField(ts.agent.ID),
					agentDiagnosticChannelField(ts.channel),
					agentDiagnosticModelField(exec.llmModel),
					agentDiagnosticErrorField(logger.ErrorClassTransport, updateErr),
					logger.SafeBool(logger.FieldFallback, true),
				),
			)
			publisher.Cancel(ctx)
			fallbackResponse, err := exec.activeProvider.Chat(
				ctx,
				messagesForCall,
				cloneToolDefinitions(toolDefsForCall),
				exec.llmModel,
				exec.llmOpts,
			)
			if err == nil && fallbackResponse != nil {
				exec.streamingFallback = true
			}
			return fallbackResponse, true, err
		}
	}
	if streamErr != nil {
		if !publisher.Published() {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentChatStreamFailedBeforeVisibleOutputRetryingWithChat,
				logger.NewSafeFields(
					agentDiagnosticAgentField(ts.agent.ID),
					agentDiagnosticChannelField(ts.channel),
					agentDiagnosticModelField(exec.llmModel),
					agentDiagnosticErrorField(logger.ErrorClassProvider, streamErr),
					logger.SafeBool(logger.FieldFallback, true),
				),
			)
			publisher.Cancel(ctx)
			fallbackResponse, err := exec.activeProvider.Chat(
				ctx,
				messagesForCall,
				cloneToolDefinitions(toolDefsForCall),
				exec.llmModel,
				exec.llmOpts,
			)
			if err == nil && fallbackResponse != nil {
				exec.streamingFallback = true
			}
			return fallbackResponse, true, err
		}
		return nil, true, configuredStreamingVisibleError{err: streamErr}
	}

	if response != nil {
		exec.streamingPublisher = publisher
	}

	return response, true, nil
}

func logConfiguredStreamingSummary(
	ts *turnState,
	exec *turnExecution,
	chunkCount int,
	firstChunkAt time.Time,
	lastChunkAt time.Time,
	streamErr error,
) {
	// Use one closed field shape for this stable sink. Missing state is encoded
	// as empty identity observations, zero span, and the sealed none error.
	agentID := ""
	channel := ""
	if ts != nil {
		agentID = ts.agent.ID
		channel = ts.channel
	}
	model := ""
	if exec != nil {
		model = exec.llmModel
	}
	chunkSpan := int64(0)
	if !firstChunkAt.IsZero() && !lastChunkAt.IsZero() {
		chunkSpan = lastChunkAt.Sub(firstChunkAt).Milliseconds()
	}
	logger.DebugSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentConfiguredStreamingCompleted,
		logger.NewSafeFields(
			logger.SafeInt(logger.FieldChunkCount, chunkCount),
			agentDiagnosticAgentField(agentID),
			agentDiagnosticChannelField(channel),
			agentDiagnosticModelField(model),
			logger.SafeInt64(logger.FieldChunkSpanMilliseconds, chunkSpan),
			agentDiagnosticErrorField(logger.ErrorClassProvider, streamErr),
		),
	)
}

type configuredStreamingVisibleError struct {
	err error
}

func (e configuredStreamingVisibleError) Error() string {
	if e.err == nil {
		return "configured streaming failed after visible output"
	}
	return e.err.Error()
}

func (e configuredStreamingVisibleError) Unwrap() error {
	return e.err
}

func isConfiguredStreamingVisibleError(err error) bool {
	var visibleErr configuredStreamingVisibleError
	return errors.As(err, &visibleErr)
}

func finalizeConfiguredStreamingLLM(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
	content string,
	contextUsage *bus.ContextUsage,
) error {
	if exec == nil || exec.streamingPublisher == nil {
		return nil
	}
	publisher := exec.streamingPublisher
	exec.streamingPublisher = nil
	visibleBeforeFinalize := publisher.Published()
	if err := publisher.Finalize(ctx, content, contextUsage); err != nil {
		if visibleBeforeFinalize {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentStreamFinalFlushFailedAfterVisibleOutput,
				logger.NewSafeFields(
					agentDiagnosticAgentField(ts.agent.ID),
					agentDiagnosticChannelField(ts.channel),
					agentDiagnosticModelField(exec.llmModel),
					agentDiagnosticErrorField(logger.ErrorClassTransport, err),
				),
			)
			return configuredStreamingVisibleError{err: err}
		}
		publisher.Cancel(ctx)
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentStreamFinalFlushFailed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticModelField(exec.llmModel),
				agentDiagnosticErrorField(logger.ErrorClassTransport, err),
			),
		)
		return err
	}
	return nil
}

func cancelConfiguredStreamingLLM(ctx context.Context, exec *turnExecution) {
	if exec == nil || exec.streamingPublisher == nil {
		return
	}
	publisher := exec.streamingPublisher
	exec.streamingPublisher = nil
	publisher.Cancel(ctx)
}

func (p *Pipeline) configuredStreamingEligible(ts *turnState, exec *turnExecution) bool {
	if p == nil || ts == nil || exec == nil || p.Bus == nil {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticReasonField("missing_pipeline_state"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumUnavailable),
			),
		)
		return false
	}
	if strings.TrimSpace(ts.channel) == "" || strings.TrimSpace(ts.chatID) == "" {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticChatField(ts.chatID),
				agentDiagnosticModelField(exec.activeModel),
				agentDiagnosticReasonField("missing_channel_context"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumUnavailable),
			),
		)
		return false
	}
	if !ts.opts.SendResponse && !ts.opts.AllowInterimPicoPublish {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticChatField(ts.chatID),
				agentDiagnosticModelField(exec.activeModel),
				agentDiagnosticReasonField("turn_output_disabled"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumDenied),
			),
		)
		return false
	}
	if len(exec.activeCandidates) != 1 {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticModelField(exec.activeModel),
				logger.SafeInt(logger.FieldModelCount, len(exec.activeCandidates)),
				agentDiagnosticReasonField("fallback_candidates_enabled"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumSkipped),
			),
		)
		return false
	}
	if exec.activeModelConfig == nil || !exec.activeModelConfig.Streaming.Enabled {
		modelName := ""
		modelStreaming := false
		if exec.activeModelConfig != nil {
			modelName = exec.activeModelConfig.ModelName
			modelStreaming = exec.activeModelConfig.Streaming.Enabled
		}
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticModelField(exec.activeModel),
				agentDiagnosticProviderModelField(modelName),
				logger.SafeBool(logger.FieldStreaming, modelStreaming),
				logger.SafeBool(logger.FieldAvailable, exec.activeModelConfig != nil),
				agentDiagnosticReasonField("model_streaming_disabled"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumDenied),
			),
		)
		return false
	}
	channelStreaming, ok := p.channelStreamingConfig(ts.channel)
	if !ok || !channelStreaming.Enabled {
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentConfiguredStreamingNotUsed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(ts.agent.ID),
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticModelField(exec.activeModel),
				logger.SafeBool(logger.FieldStreaming, channelStreaming.Enabled),
				logger.SafeBool(logger.FieldAvailable, ok),
				agentDiagnosticReasonField("channel_streaming_disabled"),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumDenied),
			),
		)
		return false
	}
	return true
}

func (p *Pipeline) channelStreamingConfig(channelName string) (config.StreamingConfig, bool) {
	if p == nil || p.Cfg == nil || p.Cfg.Channels == nil {
		return config.StreamingConfig{}, false
	}
	ch := p.Cfg.Channels[channelName]
	if ch == nil {
		return config.StreamingConfig{}, false
	}
	decoded, err := ch.GetDecoded()
	if err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentChannelStreamingConfigDecodeFailed,
			logger.NewSafeFields(
				agentDiagnosticChannelField(channelName),
				agentDiagnosticErrorField(logger.ErrorClassValidation, err),
			),
		)
		return config.StreamingConfig{}, false
	}
	return streamingConfigFromDecodedSettings(decoded)
}

func streamingConfigFromDecodedSettings(decoded any) (config.StreamingConfig, bool) {
	value := reflect.ValueOf(decoded)
	if !value.IsValid() {
		return config.StreamingConfig{}, false
	}
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return config.StreamingConfig{}, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return config.StreamingConfig{}, false
	}

	field := value.FieldByName("Streaming")
	if !field.IsValid() || !field.CanInterface() {
		return config.StreamingConfig{}, false
	}
	streaming, ok := field.Interface().(config.StreamingConfig)
	return streaming, ok
}

type streamingChunkPublisher struct {
	streamer           bus.Streamer
	channel            string
	chatID             string
	modelName          string
	published          bool
	reasoningPublished bool
	err                error
	ts                 *turnState
}

func (p *streamingChunkPublisher) Update(ctx context.Context, accumulated string) {
	if p == nil || p.streamer == nil || strings.TrimSpace(accumulated) == "" {
		return
	}
	if setter, ok := p.streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName(p.modelName)
	}
	if err := p.streamer.Update(ctx, accumulated); err != nil {
		p.err = err
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentStreamUpdateFailed,
			logger.NewSafeFields(
				agentDiagnosticChannelField(p.channel),
				agentDiagnosticChatField(p.chatID),
				agentDiagnosticErrorField(logger.ErrorClassTransport, err),
			),
		)
		return
	}
	p.published = true
}

func (p *streamingChunkPublisher) UpdateReasoning(ctx context.Context, accumulated string) {
	if p == nil || p.streamer == nil || strings.TrimSpace(accumulated) == "" {
		return
	}
	if setter, ok := p.streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName(p.modelName)
	}
	reasoningStreamer, ok := p.streamer.(bus.ReasoningStreamer)
	if !ok {
		return
	}
	if err := reasoningStreamer.UpdateReasoning(ctx, accumulated); err != nil {
		p.err = err
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentStreamReasoningUpdateFailed,
			logger.NewSafeFields(
				agentDiagnosticChannelField(p.channel),
				agentDiagnosticChatField(p.chatID),
				agentDiagnosticErrorField(logger.ErrorClassTransport, err),
			),
		)
		return
	}
	p.reasoningPublished = true
}

func (p *streamingChunkPublisher) Published() bool {
	return p != nil && p.published
}

func (p *streamingChunkPublisher) ReasoningPublished() bool {
	return p != nil && p.reasoningPublished
}

func (p *streamingChunkPublisher) Err() error {
	if p == nil {
		return nil
	}
	return p.err
}

func (p *streamingChunkPublisher) Finalize(ctx context.Context, content string, contextUsage *bus.ContextUsage) error {
	if p == nil || p.streamer == nil {
		return nil
	}
	if strings.TrimSpace(content) == "" && !p.published {
		return nil
	}
	if setter, ok := p.streamer.(interface{ SetModelName(modelName string) }); ok {
		setter.SetModelName(p.modelName)
	}
	if usage := p.ts.GetLastUsage(); usage != nil {
		if setter, ok := p.streamer.(interface{ SetTurnUsage(in, out int) }); ok {
			setter.SetTurnUsage(usage.PromptTokens, usage.CompletionTokens)
		}
	}
	var err error
	if streamer, ok := p.streamer.(bus.ContextUsageStreamer); ok {
		err = streamer.FinalizeWithContext(ctx, content, contextUsage)
	} else {
		err = p.streamer.Finalize(ctx, content)
	}
	if err != nil {
		return fmt.Errorf("stream finalize: %w", err)
	}
	p.published = true
	return nil
}

func (p *streamingChunkPublisher) FinalizeReasoning(ctx context.Context, content string) error {
	if p == nil || p.streamer == nil || !p.reasoningPublished || strings.TrimSpace(content) == "" {
		return nil
	}
	reasoningStreamer, ok := p.streamer.(bus.ReasoningStreamer)
	if !ok {
		return nil
	}
	if err := reasoningStreamer.FinalizeReasoning(ctx, content); err != nil {
		return fmt.Errorf("stream reasoning finalize: %w", err)
	}
	return nil
}

func (p *streamingChunkPublisher) ClearFinalizedStreamMarker() {
	if p == nil || p.streamer == nil {
		return
	}
	if cleaner, ok := p.streamer.(interface{ ClearFinalizedStreamMarker() }); ok {
		cleaner.ClearFinalizedStreamMarker()
	}
}

func (p *streamingChunkPublisher) Cancel(ctx context.Context) {
	if p == nil || p.streamer == nil {
		return
	}
	p.streamer.Cancel(ctx)
}
