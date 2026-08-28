package logger

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestSafeLoggerClosedEnumsAreExhaustive(t *testing.T) {
	if ComponentAgent != 1 || ComponentWebSearch != 29 || len(componentLabels) != 30 {
		t.Fatalf(
			"component wire moved: first=%d last=%d labels=%d",
			ComponentAgent, ComponentWebSearch, len(componentLabels),
		)
	}
	seenComponents := make(map[string]ComponentID)
	for component := ComponentAgent; component <= ComponentWebSearch; component++ {
		label, ok := componentLabel(component)
		if !ok || label == "" {
			t.Fatalf("component %d invalid", component)
		}
		if prior, duplicate := seenComponents[label]; duplicate {
			t.Fatalf("component label %q shared by %d and %d", label, prior, component)
		}
		seenComponents[label] = component
	}
	if _, ok := componentLabel(0); ok {
		t.Fatal("zero component accepted")
	}
	if _, ok := componentLabel(ComponentWebSearch + 1); ok {
		t.Fatal("component after append-only tail accepted")
	}

	if DiagnosticMessageEvent != 1 || DiagnosticMessageRuntimeEvent != 21 ||
		DiagnosticMessageToolCall != 22 || DiagnosticMessageHookCloseFailed != 54 ||
		DiagnosticMessageAgentAccountRouterReselectedAfterContextCompression != 55 ||
		DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered != 153 ||
		len(diagnosticMessageLabels) != 154 {
		t.Fatalf(
			"message wire moved: first=%d last=%d labels=%d",
			DiagnosticMessageEvent,
			DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered,
			len(diagnosticMessageLabels),
		)
	}
	seenMessages := make(map[string]DiagnosticMessageID)
	for message := DiagnosticMessageEvent; message <= DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered; message++ {
		label, ok := diagnosticMessageLabel(message)
		if !ok || label == "" {
			t.Fatalf("message %d invalid", message)
		}
		if prior, duplicate := seenMessages[label]; duplicate {
			t.Fatalf("message label %q shared by %d and %d", label, prior, message)
		}
		seenMessages[label] = message
	}
	if _, ok := diagnosticMessageLabel(0); ok {
		t.Fatal("zero message accepted")
	}
	if _, ok := diagnosticMessageLabel(DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered + 1); ok {
		t.Fatal("message after append-only tail accepted")
	}
}

func TestP015b2aDiagnosticMessageWireManifest(t *testing.T) {
	type namedWireID struct {
		id   DiagnosticMessageID
		wire int
	}
	namedIDs := [...]namedWireID{
		{DiagnosticMessageAgentAccountRouterReselectedAfterContextCompression, 55},
		{DiagnosticMessageAgentApplyingPendingSkillOverride, 56},
		{DiagnosticMessageAgentAsyncToolCompletedPublishingResult, 57},
		{DiagnosticMessageAgentContextOverflowCompactFailed, 58},
		{DiagnosticMessageAgentContextStillExceedsBudgetAfterRetryCompactionRebuild, 59},
		{DiagnosticMessageAgentContextWindowErrorDetectedAttemptingCompression, 60},
		{DiagnosticMessageAgentDroppingAssistantMessageWithEmptyToolCallID, 61},
		{DiagnosticMessageAgentDroppingAssistantMessageWithIncompleteToolResults, 62},
		{DiagnosticMessageAgentDroppingAssistantToolCallTurnAtHistoryStart, 63},
		{DiagnosticMessageAgentDroppingAssistantToolCallTurnWithInvalidPredecessor, 64},
		{DiagnosticMessageAgentDroppingDuplicateToolResultInToolBlock, 65},
		{DiagnosticMessageAgentDroppingOrphanedLeadingToolMessage, 66},
		{DiagnosticMessageAgentDroppingOrphanedToolMessageAfterValidation, 67},
		{DiagnosticMessageAgentDroppingOrphanedToolMessage, 68},
		{DiagnosticMessageAgentDroppingSystemMessageFromHistory, 69},
		{DiagnosticMessageAgentDroppingToolResultWithoutToolCallID, 70},
		{DiagnosticMessageAgentDroppingUnexpectedToolResult, 71},
		{DiagnosticMessageAgentFailedToApplySimpleToolSurface, 72},
		{DiagnosticMessageAgentFailedToDeliverHandledToolMedia, 73},
		{DiagnosticMessageAgentFailedToDeliverHookMedia, 74},
		{DiagnosticMessageAgentFailedToFinalizeStreamedPicoReasoning, 75},
		{DiagnosticMessageAgentFailedToRegisterAgentDiscoveryPromptContributor, 76},
		{DiagnosticMessageAgentFailedToRegisterThreadPolicyPromptContributor, 77},
		{DiagnosticMessageAgentFailedToRegisterToolDiscoveryPromptContributor, 78},
		{DiagnosticMessageAgentFailedToSaveSessionAfterToolDelivery, 79},
		{DiagnosticMessageAgentForcedCompressionExecuted, 80},
		{DiagnosticMessageAgentFullLLMRequest, 81},
		{DiagnosticMessageAgentHookReturnedRespondActionButNoHookResultProvided, 82},
		{DiagnosticMessageAgentLLMRequest, 83},
		{DiagnosticMessageAgentLLMResponseWithoutToolCallsDirectAnswer, 84},
		{DiagnosticMessageAgentLLMResponse, 85},
		{DiagnosticMessageAgentMemoryThresholdReachedOptimizingConversationHistory, 86},
		{DiagnosticMessageAgentObservedToolAdaptationCacheBehavior, 87},
		{DiagnosticMessageAgentObservedToolAdaptationOutcome, 88},
		{DiagnosticMessageAgentPendingSteeringAfterPartialToolExecutionContinuingTurn, 89},
		{DiagnosticMessageAgentProcessingSystemMessage, 90},
		{DiagnosticMessageAgentPromptContributorCollectionFailed, 91},
		{DiagnosticMessageAgentProviderReloadGracePeriodExpiredWithInFlightRequests, 92},
		{DiagnosticMessageAgentProviderReloadInterruptedWhileWaitingForInFlightRequests, 93},
		{DiagnosticMessageAgentRoutedMessage, 94},
		{DiagnosticMessageAgentSentToolResultToUser, 95},
		{DiagnosticMessageAgentSkippingInvalidPromptOverlay, 96},
		{DiagnosticMessageAgentSkippingInvalidPromptPart, 97},
		{DiagnosticMessageAgentSteeringArrivedAfterDirectLLMResponseContinuingTurn, 98},
		{DiagnosticMessageAgentSteeringArrivedAfterToolDeliveryContinuingTurn, 99},
		{DiagnosticMessageAgentSubagentCompletedInternalChannel, 100},
		{DiagnosticMessageAgentSummarizationPanicRecovered, 101},
		{DiagnosticMessageAgentSystemPromptBuilt, 102},
		{DiagnosticMessageAgentSystemPromptCacheInvalidated, 103},
		{DiagnosticMessageAgentSystemPromptCached, 104},
		{DiagnosticMessageAgentSystemPromptPreview, 105},
		{DiagnosticMessageAgentTTLTickAfterToolExecution, 106},
		{DiagnosticMessageAgentToolOutputSatisfiedDeliveryEndingTurnWithoutFollowUpLLM, 107},
		{DiagnosticMessageAgentTrackedSpawnCompletionHasNoValidParentRoute, 108},
		{DiagnosticMessageAgentTransientLLMErrorRetryingAfterBackoff, 109},
		{DiagnosticMessageAgentTrimmedRebuiltHistoryAfterContextRetryCompaction, 110},
		{DiagnosticMessageAgentTurnCheckpointSkippingRemainingToolsAfterHookRespond, 111},
		{DiagnosticMessageAgentTurnCheckpointSkippingRemainingTools, 112},
		{DiagnosticMessageAgentSkillsWalkError, 113},
		{DiagnosticMessageAgentFallbackSucceeded, 114},
		{DiagnosticMessageAgentConfiguredHooksFailedToReinitializeAfterReload, 115},
		{DiagnosticMessageAgentContextManagerIngestFailed, 116},
		{DiagnosticMessageAgentDeferredTurnResourceCleanupFailed, 117},
		{DiagnosticMessageAgentDepthLimitExceeded, 118},
		{DiagnosticMessageAgentFailedToAcquireInboundMessageRuntime, 119},
		{DiagnosticMessageAgentFailedToActivateReloadedEvolutionBridge, 120},
		{DiagnosticMessageAgentFailedToCloseMCPManager, 121},
		{DiagnosticMessageAgentFailedToCloseContextManager, 122},
		{DiagnosticMessageAgentFailedToCloseEvolutionBridge, 123},
		{DiagnosticMessageAgentFailedToClosePreviousMCPManagerDuringReload, 124},
		{DiagnosticMessageAgentFailedToClosePreviousContextManagerDuringReload, 125},
		{DiagnosticMessageAgentFailedToClosePreviousEvolutionBridgeDuringReload, 126},
		{DiagnosticMessageAgentFailedToCloseReloadedEvolutionCandidate, 127},
		{DiagnosticMessageAgentFailedToCloseRuntimeEventBus, 128},
		{DiagnosticMessageAgentFailedToEnqueueSteeringMessage, 129},
		{DiagnosticMessageAgentFailedToPublishFollowUpAfterTurn, 130},
		{DiagnosticMessageAgentFailedToRecordLastChannel, 131},
		{DiagnosticMessageAgentFailedToReinitializeEvolutionBridgeDuringReload, 132},
		{DiagnosticMessageAgentFailedToResumeSteeringAfterReservationAbandonment, 133},
		{DiagnosticMessageAgentFailedToRetainInboundMessageRuntime, 134},
		{DiagnosticMessageAgentFailedToSubscribeReloadedEvolutionBridgeToRuntimeEvents, 135},
		{DiagnosticMessageAgentMCPFailedToReinitializeAfterReload, 136},
		{DiagnosticMessageAgentPanicDuringRegistryCreation, 137},
		{DiagnosticMessageAgentPostCommitSeahorseCatalogHandlingPanicked, 138},
		{DiagnosticMessageAgentProviderAndConfigReloadedSuccessfully, 139},
		{DiagnosticMessageAgentSeahorseAdmissionProjectionFailedAfterCatalogCommit, 140},
		{DiagnosticMessageAgentSteeringRescuePanicked, 141},
		{DiagnosticMessageAgentSubTurnPanicked, 142},
		{DiagnosticMessageAgentTrackedSubagentResultContinuationFailed, 143},
		{DiagnosticMessageAgentTrackedSubagentResultContinuationRejected, 144},
		{DiagnosticMessageAgentTrackedSubagentResultOutboundWasNotAccepted, 145},
		{DiagnosticMessageAgentTrackedSubagentSteeringRescueFailed, 146},
		{DiagnosticMessageAgentTrackedSubagentSteeringRescueRecheckFailed, 147},
		{DiagnosticMessageAgentTrackedSubagentSteeringRescueRejected, 148},
		{DiagnosticMessageAgentWorkerGoroutinePanicked, 149},
		{DiagnosticMessageAgentTrackedSubagentEventPanicRecovered, 150},
		{DiagnosticMessageAgentTrackedSubagentTurnTerminalPanicRecovered, 151},
		{DiagnosticMessageAgentTrackedSubagentResultPumpPanicRecovered, 152},
		{DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered, 153},
	}
	expectedLabels := [...]string{
		"Account router reselected after context compression",
		"Applying pending skill override",
		"Async tool completed, publishing result",
		"Context overflow compact failed",
		"Context still exceeds budget after retry compaction rebuild",
		"Context window error detected, attempting compression",
		"Dropping assistant message with empty tool_call_id",
		"Dropping assistant message with incomplete tool results",
		"Dropping assistant tool-call turn at history start",
		"Dropping assistant tool-call turn with invalid predecessor",
		"Dropping duplicate tool result in tool block",
		"Dropping orphaned leading tool message",
		"Dropping orphaned tool message after validation",
		"Dropping orphaned tool message",
		"Dropping system message from history",
		"Dropping tool result without tool_call_id",
		"Dropping unexpected tool result",
		"Failed to apply simple tool surface",
		"Failed to deliver handled tool media",
		"Failed to deliver hook media",
		"Failed to finalize streamed pico reasoning",
		"Failed to register agent discovery prompt contributor",
		"Failed to register thread policy prompt contributor",
		"Failed to register tool discovery prompt contributor",
		"Failed to save session after tool delivery",
		"Forced compression executed",
		"Full LLM request",
		"Hook returned respond action but no HookResult provided",
		"LLM request",
		"LLM response without tool calls (direct answer)",
		"LLM response",
		"Memory threshold reached. Optimizing conversation history...",
		"Observed tool adaptation cache behavior",
		"Observed tool adaptation outcome",
		"Pending steering after partial tool execution; continuing turn",
		"Processing system message",
		"Prompt contributor collection failed",
		"Provider reload grace period expired with in-flight requests still running",
		"Provider reload interrupted while waiting for in-flight requests",
		"Routed message",
		"Sent tool result to user",
		"Skipping invalid prompt overlay",
		"Skipping invalid prompt part",
		"Steering arrived after direct LLM response; continuing turn",
		"Steering arrived after tool delivery; continuing turn",
		"Subagent completed (internal channel)",
		"Summarization panic recovered",
		"System prompt built",
		"System prompt cache invalidated",
		"System prompt cached",
		"System prompt preview",
		"TTL tick after tool execution",
		"Tool output satisfied delivery; ending turn without follow-up LLM",
		"Tracked spawn completion has no valid parent route",
		"Transient LLM error, retrying after backoff",
		"Trimmed rebuilt history after context retry compaction",
		"Turn checkpoint: skipping remaining tools after hook respond",
		"Turn checkpoint: skipping remaining tools",
		"skills walk error",
		"Fallback succeeded",
		"Configured hooks failed to reinitialize after reload",
		"Context manager ingest failed",
		"Deferred turn resource cleanup failed",
		"Depth limit exceeded",
		"Failed to acquire inbound message runtime",
		"Failed to activate reloaded evolution bridge",
		"Failed to close MCP manager",
		"Failed to close context manager",
		"Failed to close evolution bridge",
		"Failed to close previous MCP manager during reload",
		"Failed to close previous context manager during reload",
		"Failed to close previous evolution bridge during reload",
		"Failed to close reloaded evolution candidate",
		"Failed to close runtime event bus",
		"Failed to enqueue steering message",
		"Failed to publish follow-up after turn",
		"Failed to record last channel",
		"Failed to reinitialize evolution bridge during reload",
		"Failed to resume steering after reservation abandonment",
		"Failed to retain inbound message runtime",
		"Failed to subscribe reloaded evolution bridge to runtime events",
		"MCP failed to reinitialize after reload",
		"Panic during registry creation",
		"Post-commit Seahorse catalog handling panicked; retained context manager",
		"Provider and config reloaded successfully",
		"Seahorse admission projection failed after catalog commit; retained context manager",
		"Steering rescue panicked",
		"SubTurn panicked",
		"Tracked subagent result continuation failed",
		"Tracked subagent result continuation rejected",
		"Tracked subagent result outbound was not accepted",
		"Tracked subagent steering rescue failed",
		"Tracked subagent steering rescue recheck failed",
		"Tracked subagent steering rescue rejected",
		"Worker goroutine panicked",
		"Tracked subagent event panic recovered",
		"Tracked subagent turn-terminal panic recovered",
		"Tracked subagent result-pump panic recovered",
		"Tracked subagent steering-rescue panic recovered",
	}

	const firstWireID = 55
	if len(namedIDs) != 99 || len(expectedLabels) != len(namedIDs) {
		t.Fatalf(
			"test manifest sizes: named IDs=%d labels=%d; want 99 each",
			len(namedIDs),
			len(expectedLabels),
		)
	}
	for offset, expectedLabel := range expectedLabels {
		numericID := firstWireID + offset
		if named := namedIDs[offset]; int(named.id) != named.wire || named.wire != numericID {
			t.Fatalf(
				"named diagnostic message at offset %d = %d with declared wire %d; want wire %d",
				offset,
				named.id,
				named.wire,
				numericID,
			)
		}
		label, ok := diagnosticMessageLabel(DiagnosticMessageID(numericID))
		if !ok || label != expectedLabel {
			t.Fatalf(
				"diagnostic message wire %d = %q, %v; want %q",
				numericID,
				label,
				ok,
				expectedLabel,
			)
		}
	}
}

func TestP015b2aFieldWireManifest(t *testing.T) {
	type expectedSpec struct {
		key   FieldKey
		wire  int
		label string
		kind  safeFieldKind
	}
	expectedSpecs := [...]expectedSpec{
		{FieldMaxTokens, 64, "max_tokens", safeFieldKindInt},
		{FieldContextWindow, 65, "context_window", safeFieldKindInt},
		{FieldPromptTokens, 66, "prompt_tokens", safeFieldKindInt},
		{FieldCompletionTokens, 67, "completion_tokens", safeFieldKindInt},
		{FieldTotalTokens, 68, "total_tokens", safeFieldKindInt},
		{FieldCachedTokens, 69, "cached_tokens", safeFieldKindInt},
		{FieldReasoningTokens, 70, "reasoning_tokens", safeFieldKindInt},
		{FieldMaxRetries, 71, "max_retries", safeFieldKindInt},
		{FieldChunkCount, 72, "chunk_count", safeFieldKindInt},
		{FieldExpectedCount, 73, "expected_count", safeFieldKindInt},
		{FieldFoundCount, 74, "found_count", safeFieldKindInt},
		{FieldPendingCount, 75, "pending_count", safeFieldKindInt},
		{FieldRemainingCount, 76, "remaining_count", safeFieldKindInt},
		{FieldCompletedCount, 77, "completed_count", safeFieldKindInt},
		{FieldSkippedCount, 78, "skipped_count", safeFieldKindInt},
		{FieldAgentCount, 79, "agent_count", safeFieldKindInt},
		{FieldServerCount, 80, "server_count", safeFieldKindInt},
		{FieldSkillCount, 81, "skill_count", safeFieldKindInt},
		{FieldAvailableCount, 82, "available_count", safeFieldKindInt},
		{FieldNotificationCount, 83, "notification_count", safeFieldKindInt},
		{FieldMatchedCount, 84, "matched_count", safeFieldKindInt},
		{FieldInsertedCount, 85, "inserted_count", safeFieldKindInt},
		{FieldBackoffMilliseconds, 86, "backoff_ms", safeFieldKindInt64},
		{FieldGraceMilliseconds, 87, "grace_ms", safeFieldKindInt64},
		{FieldChunkSpanMilliseconds, 88, "chunk_span_ms", safeFieldKindInt64},
		{FieldTemperature, 89, "temperature", safeFieldKindFloat64},
		{FieldScore, 90, "score", safeFieldKindFloat64},
		{FieldThreshold, 91, "threshold", safeFieldKindFloat64},
		{FieldCacheHitRatio, 92, "cache_hit_ratio", safeFieldKindFloat64},
		{FieldHasReasoning, 93, "has_reasoning", safeFieldKindBool},
		{FieldGracefulTerminal, 94, "graceful_terminal", safeFieldKindBool},
		{FieldASREnabled, 95, "asr_enabled", safeFieldKindBool},
		{FieldTTSEnabled, 96, "tts_enabled", safeFieldKindBool},
		{FieldDebugEnabled, 97, "debug_enabled", safeFieldKindBool},
		{FieldAllowEmpty, 98, "allow_empty", safeFieldKindBool},
		{FieldLimitedMode, 99, "limited_mode", safeFieldKindBool},
		{FieldCacheHit, 100, "cache_hit", safeFieldKindBool},
		{FieldFallback, 101, "fallback", safeFieldKindBool},
		{FieldHasSummary, 102, "has_summary", safeFieldKindBool},
		{FieldCacheSensitive, 103, "cache_sensitive", safeFieldKindBool},
	}

	const firstWireKey = 64
	if len(expectedSpecs) != 40 {
		t.Fatalf("test manifest has %d specs; want 40", len(expectedSpecs))
	}
	for offset, expected := range expectedSpecs {
		numericKey := firstWireKey + offset
		if int(expected.key) != expected.wire || expected.wire != numericKey {
			t.Fatalf(
				"named field at offset %d = %d with declared wire %d; want wire %d",
				offset,
				expected.key,
				expected.wire,
				numericKey,
			)
		}
		label, kind := safeFieldSpec(FieldKey(numericKey))
		if label != expected.label || kind != expected.kind {
			t.Fatalf(
				"field wire %d = %q, kind %d; want %q, kind %d",
				numericKey,
				label,
				kind,
				expected.label,
				expected.kind,
			)
		}
	}

	namedEnums := [...]struct {
		value SafeEnumValue
		wire  int
	}{
		{SafeEnumDeveloper, 25},
	}
	for _, expected := range namedEnums {
		if int(expected.value) != expected.wire {
			t.Fatalf(
				"named enum value = %d; want wire %d",
				expected.value,
				expected.wire,
			)
		}
	}
}

func TestSafeFieldKeyKindsAndEnumFamiliesAreExhaustive(t *testing.T) {
	if FieldIteration != 1 || FieldDurationMilliseconds != 29 ||
		FieldAsync != 42 || FieldState != 55 || FieldReason != 59 ||
		FieldRequestedCount != 60 || FieldSource != 63 ||
		FieldMaxTokens != 64 || FieldFallback != 101 ||
		FieldHasSummary != 102 || FieldCacheSensitive != 103 {
		t.Fatalf(
			"field wire moved: first=%d int64=%d bool=%d enum=%d last=%d",
			FieldIteration,
			FieldDurationMilliseconds,
			FieldAsync,
			FieldState,
			FieldCacheSensitive,
		)
	}
	if SafeEnumPending != 1 || SafeEnumStopped != 21 ||
		SafeEnumInProcess != 22 || SafeEnumUnknown != 24 ||
		SafeEnumDeveloper != 25 || len(safeEnumLabels) != 26 {
		t.Fatalf(
			"safe enum wire moved: first=%d last=%d labels=%d",
			SafeEnumPending, SafeEnumDeveloper, len(safeEnumLabels),
		)
	}
	seenLabels := make(map[string]FieldKey)
	for key := FieldIteration; key <= FieldCacheSensitive; key++ {
		label, kind := safeFieldSpec(key)
		if label == "" || kind == 0 {
			t.Fatalf("field key %d missing spec", key)
		}
		if prior, duplicate := seenLabels[label]; duplicate {
			t.Fatalf("field label %q shared by %d and %d", label, prior, key)
		}
		seenLabels[label] = key

		var field SafeField
		switch kind {
		case safeFieldKindInt:
			field = SafeInt(key, 1)
		case safeFieldKindInt64:
			field = SafeInt64(key, 1)
		case safeFieldKindBool:
			field = SafeBool(key, true)
		case safeFieldKindEnum:
			field = SafeEnum(key, firstAllowedSafeEnum(t, key))
		case safeFieldKindFloat64:
			field = SafeFloat64(key, 1.5)
		default:
			t.Fatalf("field key %d has unsupported kind %d", key, kind)
		}
		if !field.valid || !safeFieldValid(field) {
			t.Fatalf("field key %d rejected matching constructor", key)
		}
	}
	if label, kind := safeFieldSpec(0); label != "" || kind != 0 {
		t.Fatalf("zero key spec = %q, %d", label, kind)
	}
	if label, kind := safeFieldSpec(FieldCacheSensitive + 1); label != "" || kind != 0 {
		t.Fatalf("key after append-only tail spec = %q, %d", label, kind)
	}

	for _, key := range []FieldKey{
		FieldState, FieldAction, FieldOutcome, FieldRole, FieldReason, FieldSource,
	} {
		for value := SafeEnumPending; value <= SafeEnumDeveloper; value++ {
			if got := SafeEnum(key, value).valid; got != safeEnumAllowed(key, value) {
				t.Fatalf("key %d enum %d validity = %v", key, value, got)
			}
		}
	}
	if !SafeEnum(FieldRole, SafeEnumUnknown).valid {
		t.Fatal("unknown role rejected")
	}
	if SafeEnum(FieldRole, SafeEnumFailed).valid ||
		SafeEnum(FieldState, SafeEnumUser).valid {
		t.Fatal("enum value crossed its fixed family")
	}
}

func TestSafeFieldsRejectInvalidDuplicateAndExpandedOverflow(t *testing.T) {
	tooManyEntries := make([]SafeField, maxSafeFieldScalars+1)
	mutatedObservation := ObserveText(ObservationDomainPrompt, "mutation-canary")
	mutatedObservation.Digest = ObserveText(ObservationDomainPrompt, "other").Digest
	invalidCollections := []SafeFields{
		{},
		NewSafeFields(SafeField{}),
		NewSafeFields(SafeInt(FieldIteration, -1)),
		NewSafeFields(SafeInt(FieldDurationMilliseconds, 1)),
		NewSafeFields(SafeInt64(FieldIteration, 1)),
		NewSafeFields(SafeBool(FieldIteration, true)),
		NewSafeFields(SafeEnum(FieldRole, SafeEnumFailed)),
		NewSafeFields(SafeInt(FieldCount, 1), SafeInt(FieldCount, 2)),
		NewSafeFields(SafeObservation(0, Observation{})),
		NewSafeFields(SafeObservation(ObservationPrefixPrompt, mutatedObservation)),
		NewSafeFields(SafeObservation(
			ObservationPrefixPath,
			ObserveText(ObservationDomainPrompt, "prefix-canary"),
		)),
		NewSafeFields(
			SafeObservation(ObservationPrefixPrompt, ObserveText(ObservationDomainPrompt, "a")),
			SafeObservation(ObservationPrefixPrompt, ObserveText(ObservationDomainPrompt, "b")),
		),
		NewSafeFields(tooManyEntries...),
	}
	for index, fields := range invalidCollections {
		if fields.valid {
			t.Fatalf("invalid collection %d accepted: %#v", index, fields)
		}
	}
	if fields := NewSafeFields(); !fields.valid || fields.scalarCount != 0 {
		t.Fatalf("valid empty fields = %#v", fields)
	}

	entries := make([]SafeField, 0, 17)
	for domain := ObservationDomainPrompt; len(entries) < 17; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		prefix, ok := prefixForDomain(domain)
		if !ok {
			t.Fatalf("domain %d has no prefix", domain)
		}
		entries = append(entries, SafeObservation(prefix, ObserveText(domain, "bounded")))
	}
	if fields := NewSafeFields(entries[:16]...); !fields.valid || fields.scalarCount != 128 {
		t.Fatalf("128 expanded fields rejected: %#v", fields)
	}
	if fields := NewSafeFields(entries...); fields.valid {
		t.Fatalf("136 expanded fields accepted: %#v", fields)
	}
}

func TestSafeFieldsDetachSourceAndEmitDeterministically(t *testing.T) {
	observation := ObserveText(ObservationDomainPrompt, "private-observation")
	source := []SafeField{
		SafeBool(FieldSuccess, true),
		SafeObservation(ObservationPrefixPrompt, observation),
		SafeInt(FieldAttempt, 3),
	}
	fields := NewSafeFields(source...)
	source[0] = SafeBool(FieldSuccess, false)
	observation.Digest = "private-mutation"

	records, raw := captureSafeJSONRecords(t, func() {
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, fields)
	})
	if len(records) != 1 || records[0]["success"] != true ||
		records[0]["attempt"] != float64(3) ||
		records[0]["prompt_digest"] == "private-mutation" {
		t.Fatalf("detached record = %#v", records)
	}
	if strings.Index(raw, `"attempt"`) > strings.Index(raw, `"prompt_class"`) ||
		strings.Index(raw, `"prompt_class"`) > strings.Index(raw, `"success"`) {
		t.Fatalf("safe fields not deterministically sorted: %s", raw)
	}
	if strings.Contains(raw, "safe_fields.go") || strings.Contains(raw, "sensitive_preview.go") {
		t.Fatalf("logger helper reported as caller: %s", raw)
	}
}

func TestSafeFieldsTypedProjectionAndDefensiveValidation(t *testing.T) {
	fields := NewSafeFields(
		SafeInt64(FieldDurationMilliseconds, 17),
		SafeBool(FieldHandled, true),
		SafeEnum(FieldRole, SafeEnumAssistant),
		SafeFloat64(FieldTemperature, 0.75),
	)
	records, raw := captureSafeJSONRecords(t, func() {
		InfoSafeCF(ComponentLogger, DiagnosticMessageEvent, fields)
	})
	if len(records) != 1 || records[0]["duration_ms"] != float64(17) ||
		records[0]["handled"] != true || records[0]["role"] != "assistant" ||
		records[0]["temperature"] != 0.75 {
		t.Fatalf("typed projection = %#v; raw=%s", records, raw)
	}

	preview := sensitivePreviewWire{serialized: marshalSensitivePreview([]byte("safe"), false)}
	if got := (SafeFields{}).withSensitivePreview(preview); got.preview != nil {
		t.Fatalf("invalid fields accepted preview: %#v", got)
	}
	fullEntries := make([]SafeField, 0, maxSafeFieldScalars/8)
	for domain := ObservationDomainPrompt; len(fullEntries) < maxSafeFieldScalars/8; domain++ {
		if domain == ObservationDomainErrorType {
			continue
		}
		prefix, ok := prefixForDomain(domain)
		if !ok {
			t.Fatalf("domain %d has no prefix", domain)
		}
		fullEntries = append(fullEntries, SafeObservation(prefix, ObserveText(domain, "safe")))
	}
	full := NewSafeFields(fullEntries...)
	if !full.valid || full.withSensitivePreview(preview).preview != nil {
		t.Fatalf("scalar-cap fields accepted preview: %#v", full)
	}

	for index, field := range []SafeField{
		{key: FieldIteration, kind: safeFieldKindInt64, int64Value: 1, valid: true},
		{key: FieldIteration, kind: safeFieldKind(255), valid: true},
	} {
		if safeFieldValid(field) {
			t.Fatalf("forged safe field %d accepted: %#v", index, field)
		}
	}
	if safeEnumAllowed(FieldRole, 0) || safeEnumAllowed(FieldRole, SafeEnumDeveloper+1) {
		t.Fatal("out-of-range safe enum accepted")
	}
	if SafeFloat64(FieldTemperature, math.NaN()).valid ||
		SafeFloat64(FieldTemperature, math.Inf(1)).valid ||
		SafeFloat64(FieldIteration, 1).valid {
		t.Fatal("invalid float safe field accepted")
	}

	var buffer bytes.Buffer
	eventLogger := zerolog.New(&buffer)
	event := eventLogger.Info()
	appendSafeObservation(event, 0, Observation{})
	event.Msg("defensive-prefix")
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &record); err != nil {
		t.Fatalf("Unmarshal(defensive observation) error = %v", err)
	}
	if record["error_state"] != observationStateUnavailable ||
		record["error_reason_code"] != reasonInvalidPrefix {
		t.Fatalf("defensive observation = %#v", record)
	}
}

func TestSafeEmittersFailClosedAndCoverAllLevels(t *testing.T) {
	var fatalExitCalls int
	records, raw := captureSafeJSONRecords(t, func() {
		zerolog.FatalExitFunc = func() { fatalExitCalls++ }
		DebugSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		WarnSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		ErrorSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		FatalSafeCF(ComponentAgent, DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentID(255), DiagnosticMessageEvent, NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageID(65535), NewSafeFields())
		InfoSafeCF(ComponentAgent, DiagnosticMessageEvent, SafeFields{})
	})
	if len(records) != 8 {
		t.Fatalf("record count = %d; raw=%s", len(records), raw)
	}
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
	for _, index := range []int{5, 6} {
		if records[index]["message"] != "Safe diagnostic rejected" ||
			records[index][safeFieldsReasonKey] != safeEnvelopeInvalid {
			t.Fatalf("invalid envelope record %d = %#v", index, records[index])
		}
	}
	if records[7][safeFieldsReasonKey] != safeFieldsInvalid ||
		records[7][safeFieldsStateKey] != observationStateUnavailable {
		t.Fatalf("zero fields record = %#v", records[7])
	}
	if strings.Contains(raw, "private-canary") {
		t.Fatalf("unexpected canary in output: %s", raw)
	}
}

func TestFatalSafeCFExitsWithoutOutputWhenLoggingDisabled(t *testing.T) {
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "disabled-fatal.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	var fatalExitCalls int
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }
	SetLevel(zerolog.Disabled)

	FatalSafeCF(ComponentLogger, DiagnosticMessageEvent, NewSafeFields())
	if fatalExitCalls != 1 {
		t.Fatalf("fatal exit calls = %d, want 1", fatalExitCalls)
	}
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("disabled Fatal emitted output: %q", data)
	}
}

func TestLegacyFatalConvenienceVariantsExit(t *testing.T) {
	prepareLoggerStateTest(t)
	var fatalExitCalls int
	zerolog.FatalExitFunc = func() { fatalExitCalls++ }

	FatalC("logger-test", "fatal component")
	Fatalf("fatal %s", "formatted")
	FatalF("fatal fields", map[string]any{"count": 1})
	FatalCF("logger-test", "fatal component fields", map[string]any{"count": 2})
	if fatalExitCalls != 4 {
		t.Fatalf("fatal exit calls = %d, want 4", fatalExitCalls)
	}
}

func firstAllowedSafeEnum(t *testing.T, key FieldKey) SafeEnumValue {
	t.Helper()
	for value := SafeEnumPending; value <= SafeEnumDeveloper; value++ {
		if safeEnumAllowed(key, value) {
			return value
		}
	}
	t.Fatalf("field key %d has no allowed enum", key)
	return 0
}

func captureSafeJSONRecords(
	t *testing.T,
	emit func(),
) ([]map[string]any, string) {
	t.Helper()
	prepareLoggerStateTest(t)
	path := filepath.Join(t.TempDir(), "safe.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	emit()
	DisableFileLogging()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("Unmarshal(%q) error = %v", line, err)
		}
		records = append(records, record)
	}
	return records, string(data)
}
