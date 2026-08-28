package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type p015B2BTransportHostileError struct {
	calls  *atomic.Int64
	secret string
}

func (value *p015B2BTransportHostileError) Error() string {
	value.calls.Add(1)
	return value.secret
}

type p015B2BTransportMediaStore struct {
	path string
	meta media.MediaMeta
	err  error
}

func (*p015B2BTransportMediaStore) Store(
	string,
	media.MediaMeta,
	string,
) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (store *p015B2BTransportMediaStore) Resolve(string) (string, error) {
	return store.path, store.err
}

func (store *p015B2BTransportMediaStore) ResolveWithMeta(
	string,
) (string, media.MediaMeta, error) {
	return store.path, store.meta, store.err
}

func (*p015B2BTransportMediaStore) ReleaseAll(string) error { return nil }

type p015B2BTransportTranscriber struct {
	err error
}

func (*p015B2BTransportTranscriber) Name() string { return "p015b2b-transport" }

func (transcriber *p015B2BTransportTranscriber) Transcribe(
	context.Context,
	string,
) (*asr.TranscriptionResponse, error) {
	return nil, transcriber.err
}

func TestP015B2BMediaAndTranscriptionDiagnosticsPreserveBehaviorAndRedact(t *testing.T) {
	const (
		mediaRef    = "media://P015B2B_MEDIA_REF_32d4df80"
		errorCanary = "P015B2B_MEDIA_ERROR_0e839cf5"
	)
	var errorCalls atomic.Int64
	hostile := &p015B2BTransportHostileError{calls: &errorCalls, secret: errorCanary}

	failedStore := &p015B2BTransportMediaStore{err: hostile}
	input := []providers.Message{{Role: "user", Content: "unchanged", Media: []string{mediaRef}}}
	var resolved []providers.Message
	resolveRecords, resolveRaw := captureP015HookRecords(t, func() {
		resolved = resolveMediaRefs(input, failedStore, config.DefaultMaxMediaSize, 0)
	})
	if len(resolved) != 1 || resolved[0].Content != input[0].Content || len(resolved[0].Media) != 0 {
		t.Fatalf("resolveMediaRefs() = %#v, want unchanged content and failed ref removed", resolved)
	}
	resolveRecord := p015B2ARequireRuntimeRecord(
		t,
		resolveRecords,
		"Failed to resolve media ref",
		nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		resolveRecord,
		logger.ObservationPrefixURL,
		logger.ObserveURL(mediaRef),
	)
	p015B2AAssertRuntimeObservation(
		t,
		resolveRecord,
		logger.ObservationPrefixError,
		logger.ObserveErrorType(logger.ErrorClassNotFound, hostile),
	)
	if errorCalls.Load() != 0 {
		t.Fatalf("media diagnostic invoked hostile Error() %d times", errorCalls.Load())
	}
	assertP015CanariesAbsent(t, resolveRaw, mediaRef, errorCanary)

	transcriptionStore := &p015B2BTransportMediaStore{
		path: "/private/P015B2B_AUDIO_PATH_589728dd.wav",
		meta: media.MediaMeta{Filename: "voice.wav", ContentType: "audio/wav"},
	}
	loop := &AgentLoop{
		cfg:         config.DefaultConfig(),
		mediaStore:  transcriptionStore,
		transcriber: &p015B2BTransportTranscriber{err: hostile},
	}
	inbound := bus.InboundMessage{Content: "[audio]", Media: []string{mediaRef}}
	var transcribed bus.InboundMessage
	var changed bool
	transcribeRecords, transcribeRaw := captureP015HookRecords(t, func() {
		transcribed, changed = loop.transcribeAudioInMessage(context.Background(), inbound)
	})
	if !changed || transcribed.Content != inbound.Content || len(transcribed.Media) != 1 ||
		transcribed.Media[0] != mediaRef {
		t.Fatalf("failed transcription result = (%#v, %v), want unchanged annotation/ref", transcribed, changed)
	}
	transcribeRecord := p015B2ARequireRuntimeRecord(t, transcribeRecords, "Transcription failed", nil)
	p015B2AAssertRuntimeObservation(
		t,
		transcribeRecord,
		logger.ObservationPrefixURL,
		logger.ObserveURL(mediaRef),
	)
	p015B2AAssertRuntimeObservation(
		t,
		transcribeRecord,
		logger.ObservationPrefixError,
		logger.ObserveErrorType(logger.ErrorClassProvider, hostile),
	)
	if errorCalls.Load() != 0 {
		t.Fatalf("transcription diagnostic invoked hostile Error() %d times", errorCalls.Load())
	}
	assertP015CanariesAbsent(t, transcribeRaw, mediaRef, errorCanary, transcriptionStore.path)
}

func TestP015B2BMediaOpenFailureUsesInternalClassAndSealsPath(t *testing.T) {
	const pathCanary = "P015B2B_MEDIA_OPEN_PATH_7d9a31c4"
	fixture := filepath.Join(t.TempDir(), "size-fixture")
	if err := os.WriteFile(fixture, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(fixture)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), pathCanary)
	var encoded string
	records, raw := captureP015HookRecords(t, func() {
		encoded = encodeImageToDataURL(missing, "image/png", info, 1024)
	})
	if encoded != "" {
		t.Fatalf("failed media open returned data URL %q", encoded)
	}
	record := p015B2ARequireRuntimeRecord(t, records, "Failed to open media file", nil)
	p015B2AAssertRuntimeObservation(
		t,
		record,
		logger.ObservationPrefixPath,
		logger.ObservePath(missing),
	)
	if record["error_class"] != "internal" || record["error_digest"] == nil {
		t.Fatalf("media-open diagnostic = %#v", record)
	}
	assertP015CanariesAbsent(t, raw, missing, pathCanary)
}

func TestP015B2BOutboundDiagnosticsPreservePayloadAndRedact(t *testing.T) {
	const (
		channelCanary = "P015B2B_CHANNEL_93a4c0b1"
		chatCanary    = "P015B2B_CHAT_f3d3ad72"
		contentCanary = "P015B2B_CONTENT_2de6051b"
	)
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := &AgentLoop{bus: messageBus}
	var accepted bool
	records, raw := captureP015HookRecords(t, func() {
		accepted = loop.publishResponseIfNeeded(
			context.Background(),
			channelCanary,
			chatCanary,
			"",
			contentCanary,
			nil,
		)
	})
	if !accepted {
		t.Fatal("publishResponseIfNeeded() rejected a successful bus publication")
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Content != contentCanary || outbound.Channel != channelCanary ||
			outbound.ChatID != chatCanary {
			t.Fatalf("functional outbound changed: %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("successful publication did not retain the outbound payload")
	}
	record := p015B2ARequireRuntimeRecord(t, records, "Published outbound response", nil)
	p015B2AAssertRuntimeObservation(
		t,
		record,
		logger.ObservationPrefixIdentityChannel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChannel, channelCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		record,
		logger.ObservationPrefixIdentityChat,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChat, chatCanary),
	)
	if record["content_bytes"] != float64(len(contentCanary)) {
		t.Fatalf("outbound content_bytes = %#v, want %d", record["content_bytes"], len(contentCanary))
	}
	assertP015CanariesAbsent(t, raw, channelCanary, chatCanary, contentCanary)

	failedBus := bus.NewMessageBus()
	failedBus.Close()
	failedLoop := &AgentLoop{bus: failedBus}
	failedRecords, failedRaw := captureP015HookRecords(t, func() {
		accepted = failedLoop.publishResponseIfNeeded(
			context.Background(),
			channelCanary,
			chatCanary,
			"",
			contentCanary,
			nil,
		)
	})
	if accepted {
		t.Fatal("publishResponseIfNeeded() accepted a closed-bus publication")
	}
	failedRecord := p015B2ARequireRuntimeRecord(
		t,
		failedRecords,
		"Published outbound response",
		nil,
	)
	if failedRecord["content_bytes"] != float64(len(contentCanary)) {
		t.Fatalf("failed outbound diagnostic = %#v", failedRecord)
	}
	assertP015CanariesAbsent(t, failedRaw, channelCanary, chatCanary, contentCanary)
}

func TestP015B2BStreamingFallbackDiagnosticsPreserveBehaviorAndRedact(t *testing.T) {
	const (
		modelCanary    = "P015B2B_MODEL_538ce4c2"
		streamCanary   = "P015B2B_STREAM_ERROR_33ce176f"
		responseCanary = "P015B2B_FALLBACK_RESPONSE_24f874eb"
	)
	var errorCalls atomic.Int64
	hostile := &p015B2BTransportHostileError{calls: &errorCalls, secret: streamCanary}
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	cfg.Agents.Defaults.ModelName = modelCanary
	cfg.ModelList[0].ModelName = modelCanary
	cfg.ModelList[0].Model = "openai/" + modelCanary
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	messageBus.SetStreamDelegate(configuredStreamingDelegate{streamer: &recordingStreamer{}})
	provider := &configuredStreamingProvider{
		streamPlan: []configuredStreamingCall{{err: hostile}},
		chatResponse: &providers.LLMResponse{
			Content: responseCanary,
		},
	}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(loop.Close)
	var response string
	records, raw := captureP015HookRecords(t, func() {
		response = runConfiguredStreamingTurn(t, loop, "pico")
	})
	if response != responseCanary || provider.streamCalls != 1 || provider.chatCalls != 1 {
		t.Fatalf(
			"fallback = (%q, stream=%d, chat=%d), want (%q, 1, 1)",
			response,
			provider.streamCalls,
			provider.chatCalls,
			responseCanary,
		)
	}
	record := p015B2ARequireRuntimeRecord(
		t,
		records,
		"ChatStream failed before visible output; retrying with Chat",
		nil,
	)
	if record["fallback"] != true {
		t.Fatalf("stream fallback marker = %#v, want true; record=%#v", record["fallback"], record)
	}
	p015B2AAssertRuntimeObservation(
		t,
		record,
		logger.ObservationPrefixError,
		logger.ObserveErrorType(logger.ErrorClassProvider, hostile),
	)
	if !p015B2ANonemptyRecordString(record, "identity_model_digest") {
		t.Fatalf("stream fallback record lacks a model observation: %#v", record)
	}
	if errorCalls.Load() != 0 {
		t.Fatalf("streaming diagnostic invoked hostile Error() %d times", errorCalls.Load())
	}
	assertP015CanariesAbsent(t, raw, modelCanary, streamCanary, responseCanary)
}

func TestP015B2BStreamingEligibilityReasonsAreClosedAndDistinct(t *testing.T) {
	const (
		agentCanary   = "P015B2B_ELIGIBILITY_AGENT_a63c915e"
		channelCanary = "pico"
		chatCanary    = "P015B2B_ELIGIBILITY_CHAT_614eb373"
		modelCanary   = "P015B2B_ELIGIBILITY_MODEL_88c75120"
	)
	type fixture struct {
		pipeline *Pipeline
		state    *turnState
		exec     *turnExecution
	}
	newFixture := func(channelStreaming, modelStreaming bool) fixture {
		cfg := newConfiguredStreamingTestConfig(t, channelStreaming, modelStreaming, nil)
		messageBus := bus.NewMessageBus()
		t.Cleanup(messageBus.Close)
		return fixture{
			pipeline: &Pipeline{Bus: messageBus, Cfg: cfg},
			state: &turnState{
				agent:   &AgentInstance{ID: agentCanary},
				opts:    processOptions{SendResponse: true},
				channel: channelCanary,
				chatID:  chatCanary,
			},
			exec: &turnExecution{
				activeCandidates:  []providers.FallbackCandidate{{}},
				activeModel:       modelCanary,
				activeModelConfig: cfg.ModelList[0],
				llmModel:          modelCanary,
			},
		}
	}

	tests := make([]struct {
		name   string
		reason string
		coarse string
		invoke func()
	}, 0, 8)
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{
		name:   "missing pipeline state",
		reason: "missing_pipeline_state",
		coarse: "unavailable",
		invoke: func() { (*Pipeline)(nil).configuredStreamingEligible(nil, nil) },
	})

	missingChannel := newFixture(true, true)
	missingChannel.state.channel = ""
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"missing channel context", "missing_channel_context", "unavailable", func() {
		missingChannel.pipeline.configuredStreamingEligible(missingChannel.state, missingChannel.exec)
	}})

	outputDisabled := newFixture(true, true)
	outputDisabled.state.opts.SendResponse = false
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"turn output disabled", "turn_output_disabled", "denied", func() {
		outputDisabled.pipeline.configuredStreamingEligible(outputDisabled.state, outputDisabled.exec)
	}})

	fallbacks := newFixture(true, true)
	fallbacks.exec.activeCandidates = []providers.FallbackCandidate{{}, {}}
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"fallback candidates", "fallback_candidates_enabled", "skipped", func() {
		fallbacks.pipeline.configuredStreamingEligible(fallbacks.state, fallbacks.exec)
	}})

	modelDisabled := newFixture(true, false)
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"model disabled", "model_streaming_disabled", "denied", func() {
		modelDisabled.pipeline.configuredStreamingEligible(modelDisabled.state, modelDisabled.exec)
	}})

	channelDisabled := newFixture(false, true)
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"channel disabled", "channel_streaming_disabled", "denied", func() {
		channelDisabled.pipeline.configuredStreamingEligible(channelDisabled.state, channelDisabled.exec)
	}})

	providerUnavailable := newFixture(true, true)
	providerUnavailable.exec.activeProvider = &plainProvider{}
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"provider unavailable", "provider_not_streaming", "unavailable", func() {
		_, _, _ = providerUnavailable.pipeline.tryConfiguredStreamingLLM(
			context.Background(), providerUnavailable.state, providerUnavailable.exec, nil, nil,
		)
	}})

	streamerUnavailable := newFixture(true, true)
	streamerUnavailable.exec.activeProvider = &configuredStreamingProvider{}
	tests = append(tests, struct {
		name   string
		reason string
		coarse string
		invoke func()
	}{"streamer unavailable", "streamer_unavailable", "unavailable", func() {
		_, _, _ = streamerUnavailable.pipeline.tryConfiguredStreamingLLM(
			context.Background(), streamerUnavailable.state, streamerUnavailable.exec, nil, nil,
		)
	}})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, raw := captureP015HookRecords(t, test.invoke)
			record := p015B2ARequireRuntimeRecord(
				t, records, "configured streaming not used", nil,
			)
			if record["reason"] != test.coarse {
				t.Fatalf("coarse reason = %#v, want %q; record=%#v", record["reason"], test.coarse, record)
			}
			p015B2AAssertRuntimeObservation(
				t,
				record,
				logger.ObservationPrefixIdentityReason,
				logger.ObserveIdentity(logger.ObservationDomainIdentityReason, test.reason),
			)
			assertP015CanariesAbsent(t, raw, agentCanary, chatCanary, modelCanary)
		})
	}
}

func TestP015B2BStreamingSummaryUsesOneClosedFieldShape(t *testing.T) {
	absentRecords, _ := captureP015HookRecords(t, func() {
		logConfiguredStreamingSummary(nil, nil, 0, time.Time{}, time.Time{}, nil)
	})
	absent := p015B2ARequireRuntimeRecord(
		t, absentRecords, "configured streaming completed", nil,
	)
	if absent["chunk_count"] != float64(0) || absent["chunk_span_ms"] != float64(0) ||
		absent["error_class"] != "none" {
		t.Fatalf("absent streaming summary = %#v", absent)
	}
	p015B2AAssertRuntimeObservation(
		t,
		absent,
		logger.ObservationPrefixIdentityAgent,
		logger.ObserveIdentity(logger.ObservationDomainIdentityAgent, ""),
	)

	const (
		agentCanary   = "P015B2B_SUMMARY_AGENT_6d0e9491"
		channelCanary = "P015B2B_SUMMARY_CHANNEL_d8fe1924"
		modelCanary   = "P015B2B_SUMMARY_MODEL_5d7a2cf8"
		errorCanary   = "P015B2B_SUMMARY_ERROR_fa377b51"
	)
	var methodCalls atomic.Int64
	hostile := &p015B2BTransportHostileError{calls: &methodCalls, secret: errorCanary}
	first := time.Unix(10, 0)
	last := first.Add(37 * time.Millisecond)
	presentRecords, presentRaw := captureP015HookRecords(t, func() {
		logConfiguredStreamingSummary(
			&turnState{agent: &AgentInstance{ID: agentCanary}, channel: channelCanary},
			&turnExecution{llmModel: modelCanary},
			4,
			first,
			last,
			hostile,
		)
	})
	present := p015B2ARequireRuntimeRecord(
		t, presentRecords, "configured streaming completed", nil,
	)
	if present["chunk_count"] != float64(4) || present["chunk_span_ms"] != float64(37) ||
		present["error_class"] != "provider" || methodCalls.Load() != 0 {
		t.Fatalf("present streaming summary/calls = %#v/%d", present, methodCalls.Load())
	}
	p015B2AAssertRuntimeObservation(
		t,
		present,
		logger.ObservationPrefixIdentityModel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityModel, modelCanary),
	)
	assertP015CanariesAbsent(
		t, presentRaw, agentCanary, channelCanary, modelCanary, errorCanary,
	)
}

func TestP015B2BTurnAndSteeringDiagnosticsPreserveFallbackAndRedact(t *testing.T) {
	const (
		managerCanary = "P015B2B_CONTEXT_MANAGER_faa6b325"
		scopeCanary   = "P015B2B_SCOPE_9b7bfe26"
		errorCanary   = "P015B2B_STEERING_ERROR_6681d5c4"
	)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ContextManager = managerCanary
	loop := &AgentLoop{cfg: cfg}
	var manager ContextManager
	managerRecords, managerRaw := captureP015HookRecords(t, func() {
		manager = loop.resolveContextManagerWithContext(context.Background())
	})
	if _, ok := manager.(*legacyContextManager); !ok {
		t.Fatalf("unknown context manager resolved to %T, want legacy fallback", manager)
	}
	managerRecord := p015B2ARequireRuntimeRecord(
		t,
		managerRecords,
		"Unknown context manager, falling back to legacy",
		nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		managerRecord,
		logger.ObservationPrefixIdentityContextManager,
		logger.ObserveIdentity(logger.ObservationDomainIdentityContextManager, managerCanary),
	)
	assertP015CanariesAbsent(t, managerRaw, managerCanary)

	var errorCalls atomic.Int64
	hostile := &p015B2BTransportHostileError{calls: &errorCalls, secret: errorCanary}
	steeringRecords, steeringRaw := captureP015HookRecords(t, func() {
		loop.reportSteeringEnqueue(
			scopeCanary,
			"P015B2B_AGENT_06ee75e9",
			providers.Message{Role: "P015B2B_ROLE_28729cb6"},
			7,
			hostile,
		)
	})
	steeringRecord := p015B2ARequireRuntimeRecord(
		t,
		steeringRecords,
		"Failed to enqueue steering message",
		nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		steeringRecord,
		logger.ObservationPrefixIdentityScope,
		logger.ObserveIdentity(logger.ObservationDomainIdentityScope, scopeCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		steeringRecord,
		logger.ObservationPrefixError,
		logger.ObserveErrorType(logger.ErrorClassInternal, hostile),
	)
	if steeringRecord["role"] != "unknown" || errorCalls.Load() != 0 {
		t.Fatalf(
			"steering record/calls = (%#v, %d), want unknown role and no Error()",
			steeringRecord,
			errorCalls.Load(),
		)
	}
	assertP015CanariesAbsent(
		t,
		steeringRaw,
		scopeCanary,
		errorCanary,
		"P015B2B_AGENT_06ee75e9",
		"P015B2B_ROLE_28729cb6",
	)
}
