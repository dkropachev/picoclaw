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
		DiagnosticMessageWorkflowRunFailed != 274 ||
		DiagnosticMessagePRWorkspaceRepairFailed != 326 || len(diagnosticMessageLabels) != 327 {
		t.Fatalf(
			"message wire moved: first=%d last=%d labels=%d",
			DiagnosticMessageEvent,
			DiagnosticMessagePRWorkspaceRepairFailed,
			len(diagnosticMessageLabels),
		)
	}
	seenMessages := make(map[string]DiagnosticMessageID)
	for message := DiagnosticMessageEvent; message <= DiagnosticMessagePRWorkspaceRepairFailed; message++ {
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
	if _, ok := diagnosticMessageLabel(DiagnosticMessagePRWorkspaceRepairFailed + 1); ok {
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

func TestP015b2bDiagnosticMessageWireManifest(t *testing.T) {
	type expectedMessage struct {
		id    DiagnosticMessageID
		wire  int
		label string
	}
	expected := [...]expectedMessage{
		{
			id:    DiagnosticMessageAgentAccountRouterModelAliasesAreInvalid,
			wire:  154,
			label: "Account router model aliases are invalid",
		},
		{
			id:    DiagnosticMessageAgentAccountRouterAccountHasNoRunnableModelAlias,
			wire:  155,
			label: "Account router account has no runnable model alias",
		},
		{
			id:    DiagnosticMessageAgentFailedToRefreshMCPStatusForCommand,
			wire:  156,
			label: "Failed to refresh MCP status for command",
		},
		{
			id:    DiagnosticMessageAgentFailedToInitializeMCPRuntimeForCommand,
			wire:  157,
			label: "Failed to initialize MCP runtime for command",
		},
		{
			id:    DiagnosticMessageAgentFailedToInitializeEvolutionBridge,
			wire:  158,
			label: "Failed to initialize evolution bridge",
		},
		{
			id:    DiagnosticMessageAgentFailedToSubscribeEvolutionBridgeToRuntimeEvents,
			wire:  159,
			label: "Failed to subscribe evolution bridge to runtime events",
		},
		{
			id:    DiagnosticMessageAgentFailedToActivateEvolutionBridge,
			wire:  160,
			label: "Failed to activate evolution bridge",
		},
		{
			id:    DiagnosticMessageAgentFailedToInitializeAgentActivityRecorder,
			wire:  161,
			label: "Failed to initialize agent activity recorder",
		},
		{
			id:    DiagnosticMessageAgentFailedToInstallSharedRecursionToolCatalog,
			wire:  162,
			label: "Failed to install shared recursion tool catalog",
		},
		{
			id:    DiagnosticMessageVoiceTTSSendTTSEnabledButNoTTSProviderConfigured,
			wire:  163,
			label: "send_tts enabled but no TTS provider configured",
		},
		{
			id:    DiagnosticMessageAgentFailedToCreateWebSearchTool,
			wire:  164,
			label: "Failed to create web search tool",
		},
		{
			id:    DiagnosticMessageAgentFailedToCreateWebFetchTool,
			wire:  165,
			label: "Failed to create web fetch tool",
		},
		{
			id:    DiagnosticMessageAgentSpawnSpawnStatusToolsRequireSubagentToBeEnabled,
			wire:  166,
			label: "spawn/spawn_status tools require subagent to be enabled",
		},
		{
			id:    DiagnosticMessageAgentMCPIsEnabledButNoServersAreConfiguredSkippingMCPInitialization,
			wire:  167,
			label: "MCP is enabled but no servers are configured, skipping MCP initialization",
		},
		{
			id:    DiagnosticMessageAgentNoMCPServersSelectedAfterApplyingPerAgentMCPServerAllowlists,
			wire:  168,
			label: "No MCP servers selected after applying per-agent mcpServers allowlists",
		},
		{
			id:    DiagnosticMessageAgentMCPIsEnabledButNoValidServersAreConfiguredSkippingMCPInitialization,
			wire:  169,
			label: "MCP is enabled but no valid servers are configured, skipping MCP initialization",
		},
		{
			id:    DiagnosticMessageAgentFailedToInitializeMCPGeneration,
			wire:  170,
			label: "Failed to initialize MCP generation",
		},
		{
			id:    DiagnosticMessageAgentMCPAdmissionProjectionFailedAfterCatalogCommit,
			wire:  171,
			label: "MCP admission projection failed after catalog commit",
		},
		{
			id:    DiagnosticMessageAgentMCPPromptPreparationFailedAfterCatalogCommit,
			wire:  172,
			label: "MCP prompt preparation failed after catalog commit",
		},
		{
			id:    DiagnosticMessageAgentMCPPromptPublicationWasIncomplete,
			wire:  173,
			label: "MCP prompt publication was incomplete",
		},
		{
			id:    DiagnosticMessageAgentMCPFactoryCatalogInstalledSuccessfully,
			wire:  174,
			label: "MCP factory catalog installed successfully",
		},
		{
			id:    DiagnosticMessageAgentMCPPostCommitPublicationPanickedRetainedCommittedManager,
			wire:  175,
			label: "MCP post-commit publication panicked; retained committed manager",
		},
		{
			id:    DiagnosticMessageAgentMediaFileTooLargeSkipping,
			wire:  176,
			label: "Media file too large, skipping",
		},
		{
			id:    DiagnosticMessageAgentFailedToOpenMediaFile,
			wire:  177,
			label: "Failed to open media file",
		},
		{
			id:    DiagnosticMessageAgentFailedToEncodeMediaFile,
			wire:  178,
			label: "Failed to encode media file",
		},
		{
			id:    DiagnosticMessageAgentFailedToCloseBase64Encoder,
			wire:  179,
			label: "Failed to close base64 encoder",
		},
		{
			id:    DiagnosticMessageAgentSkippedStaleHistoricalMediaRef,
			wire:  180,
			label: "Skipped stale historical media ref",
		},
		{
			id:    DiagnosticMessageFailedToResolveMediaRef,
			wire:  181,
			label: "Failed to resolve media ref",
		},
		{
			id:    DiagnosticMessageAgentFailedToStatMediaFile,
			wire:  182,
			label: "Failed to stat media file",
		},
		{
			id:    DiagnosticMessageAgentReasoningPublishSkippedTimeoutCancel,
			wire:  183,
			label: "Reasoning publish skipped (timeout/cancel)",
		},
		{
			id:    DiagnosticMessageAgentFailedToPublishReasoningBestEffort,
			wire:  184,
			label: "Failed to publish reasoning (best-effort)",
		},
		{
			id:    DiagnosticMessageAgentPicoReasoningPublishSkippedTimeoutCancel,
			wire:  185,
			label: "Pico reasoning publish skipped (timeout/cancel)",
		},
		{
			id:    DiagnosticMessageAgentFailedToPublishPicoReasoningBestEffort,
			wire:  186,
			label: "Failed to publish pico reasoning (best-effort)",
		},
		{
			id:    DiagnosticMessageAgentFailedToPublishPicoReasoning,
			wire:  187,
			label: "Failed to publish pico reasoning",
		},
		{
			id:    DiagnosticMessageAgentFailedToPublishPicoInterimAssistantContent,
			wire:  188,
			label: "Failed to publish pico interim assistant content",
		},
		{
			id:    DiagnosticMessageAgentFailedToSerializePicoToolCalls,
			wire:  189,
			label: "Failed to serialize pico tool calls",
		},
		{
			id:    DiagnosticMessageAgentFailedToPublishPicoToolCalls,
			wire:  190,
			label: "Failed to publish pico tool calls",
		},
		{
			id:    DiagnosticMessageAgentSkippedOutboundMessageToolAlreadySentToSameChat,
			wire:  191,
			label: "Skipped outbound (message tool already sent to same chat)",
		},
		{
			id:    DiagnosticMessageAgentPublishedOutboundResponse,
			wire:  192,
			label: "Published outbound response",
		},
		{
			id:    DiagnosticMessageAgentContinuingQueuedSteeringAfterTurnEnd,
			wire:  193,
			label: "Continuing queued steering after turn end",
		},
		{
			id:    DiagnosticMessageAgentFailedToBuildSteeringContinuationTarget,
			wire:  194,
			label: "Failed to build steering continuation target",
		},
		{
			id:    DiagnosticMessageAgentFailedToContinueQueuedSteering,
			wire:  195,
			label: "Failed to continue queued steering",
		},
		{
			id:    DiagnosticMessageVoiceFailedToSendTranscriptionFeedback,
			wire:  196,
			label: "Failed to send transcription feedback",
		},
		{
			id:    DiagnosticMessageVoiceTranscriptionFailed,
			wire:  197,
			label: "Transcription failed",
		},
		{
			id:    DiagnosticMessageAgentMaxTokensGreaterThanOrEqualToBudgetUsing50PercentFallback,
			wire:  198,
			label: "MaxTokens >= budget, using 50% fallback",
		},
		{
			id:    DiagnosticMessageSeahorseBootstrapSnapshot,
			wire:  199,
			label: "bootstrap snapshot",
		},
		{
			id:    DiagnosticMessageAgentFailedToParseAgentMDFrontmatter,
			wire:  200,
			label: "Failed to parse AGENT.md frontmatter",
		},
		{
			id:    DiagnosticMessageAgentFailedToCloseEvolutionRuntimeSubscription,
			wire:  201,
			label: "Failed to close evolution runtime subscription",
		},
		{
			id:    DiagnosticMessageAgentEvolutionFinalizeTurnFailed,
			wire:  202,
			label: "Evolution finalize turn failed",
		},
		{
			id:    DiagnosticMessageAgentNoValidEvolutionColdPathScheduleTimesConfigured,
			wire:  203,
			label: "No valid evolution cold path schedule times configured",
		},
		{
			id:    DiagnosticMessageAgentColdPathRunFailed,
			wire:  204,
			label: "Cold path run failed",
		},
		{
			id:    DiagnosticMessageGitWorkspaceFailedToReleaseGitWorkspaceLocks,
			wire:  205,
			label: "Failed to release git workspace locks",
		},
		{
			id:    DiagnosticMessageGitWorkspaceFailedToReconcileGitWorkspaceRetention,
			wire:  206,
			label: "Failed to reconcile git workspace retention",
		},
		{
			id:    DiagnosticMessageGitWorkspaceFailedToInitializeGitWorkspaceManager,
			wire:  207,
			label: "Failed to initialize git workspace manager",
		},
		{
			id:    DiagnosticMessageAgentFailedToClosePartiallyConstructedAgent,
			wire:  208,
			label: "Failed to close partially constructed agent",
		},
		{
			id:    DiagnosticMessageAgentResolvedToolAdaptationProfile,
			wire:  209,
			label: "Resolved tool adaptation profile",
		},
		{
			id:    DiagnosticMessageAgentFailedToInitializeExecToolContinuingWithoutExec,
			wire:  210,
			label: "Failed to initialize exec tool; continuing without exec",
		},
		{
			id:    DiagnosticMessageAgentRoutingLightModelNotFoundRoutingDisabled,
			wire:  211,
			label: "Routing light model not found; routing disabled",
		},
		{
			id:    DiagnosticMessageAgentInvalidPathPatternInCompilePatterns,
			wire:  212,
			label: "invalid path pattern in compilePatterns",
		},
		{
			id:    DiagnosticMessageAgentMemoryJSONLStoreInitFailedFallingBackToJSONSessions,
			wire:  213,
			label: "Memory JSONL store init failed; falling back to json sessions",
		},
		{
			id:    DiagnosticMessageAgentMemoryMigrationFailedFallingBackToJSONSessions,
			wire:  214,
			label: "Memory migration failed; falling back to json sessions",
		},
		{
			id:    DiagnosticMessageAgentMemoryMigratedToJSONL,
			wire:  215,
			label: "Memory migrated to JSONL",
		},
		{
			id:    DiagnosticMessageAgentUnsubscribeEventsUnexpectedTypeInSubscriptionMap,
			wire:  216,
			label: "UnsubscribeEvents: unexpected type in subscription map",
		},
		{
			id:    DiagnosticMessageAgentMediaTurnRoutingSelectedModel,
			wire:  217,
			label: "Media turn routing selected model",
		},
		{
			id:    DiagnosticMessageAgentProactiveCompressionContextBudgetExceededBeforeLLMCall,
			wire:  218,
			label: "Proactive compression: context budget exceeded before LLM call",
		},
		{
			id:    DiagnosticMessageAgentProactiveCompactFailed,
			wire:  219,
			label: "Proactive compact failed",
		},
		{
			id:    DiagnosticMessageAgentTrimmedRebuiltHistoryAfterProactiveCompaction,
			wire:  220,
			label: "Trimmed rebuilt history after proactive compaction",
		},
		{
			id:    DiagnosticMessageAgentContextStillExceedsBudgetAfterProactiveCompactionRebuild,
			wire:  221,
			label: "Context still exceeds budget after proactive compaction rebuild",
		},
		{
			id:    DiagnosticMessageAgentChannelStreamingConfigDecodeFailed,
			wire:  222,
			label: "channel streaming config decode failed",
		},
		{
			id:    DiagnosticMessageAgentConfiguredStreamingNotUsed,
			wire:  223,
			label: "configured streaming not used",
		},
		{
			id:    DiagnosticMessageAgentConfiguredStreamingEnabled,
			wire:  224,
			label: "configured streaming enabled",
		},
		{
			id:    DiagnosticMessageAgentChatStreamUpdateFailedAfterVisibleOutput,
			wire:  225,
			label: "ChatStream update failed after visible output",
		},
		{
			id:    DiagnosticMessageAgentChatStreamUpdateFailedBeforeVisibleOutputRetryingWithChat,
			wire:  226,
			label: "ChatStream update failed before visible output; retrying with Chat",
		},
		{
			id:    DiagnosticMessageAgentChatStreamFailedBeforeVisibleOutputRetryingWithChat,
			wire:  227,
			label: "ChatStream failed before visible output; retrying with Chat",
		},
		{
			id:    DiagnosticMessageAgentStreamUpdateFailed,
			wire:  228,
			label: "stream update failed",
		},
		{
			id:    DiagnosticMessageAgentStreamReasoningUpdateFailed,
			wire:  229,
			label: "stream reasoning update failed",
		},
		{
			id:    DiagnosticMessageAgentStreamFinalFlushFailedAfterVisibleOutput,
			wire:  230,
			label: "stream final flush failed after visible output",
		},
		{
			id:    DiagnosticMessageAgentStreamFinalFlushFailed,
			wire:  231,
			label: "stream final flush failed",
		},
		{
			id:    DiagnosticMessageAgentConfiguredStreamingCompleted,
			wire:  232,
			label: "configured streaming completed",
		},
		{
			id:    DiagnosticMessageAgentUnregisteredPromptSourceAllowedInCompatibilityMode,
			wire:  233,
			label: "Unregistered prompt source allowed in compatibility mode",
		},
		{
			id:    DiagnosticMessageAgentFailedToRegisterBuiltinPromptSource,
			wire:  234,
			label: "Failed to register builtin prompt source",
		},
		{
			id:    DiagnosticMessageAgentRecursionAdmissionProjectionFailedAfterCatalogCommit,
			wire:  235,
			label: "Recursion admission projection failed after catalog commit",
		},
		{
			id:    DiagnosticMessageAgentFailedToCloseAgent,
			wire:  236,
			label: "Failed to close agent",
		},
		{
			id:    DiagnosticMessageAgentCreatedImplicitMainAgentNoAgentsListConfigured,
			wire:  237,
			label: "Created implicit main agent (no agents.list configured)",
		},
		{
			id:    DiagnosticMessageAgentRegisteredAgent,
			wire:  238,
			label: "Registered agent",
		},
		{
			id:    DiagnosticMessageAgentHardAbortTriggered,
			wire:  239,
			label: "Hard abort triggered",
		},
		{
			id:    DiagnosticMessageAgentSteeringMessageEnqueued,
			wire:  240,
			label: "Steering message enqueued",
		},
		{
			id:    DiagnosticMessageAgentThinkingLevelIsSetButCurrentProviderDoesNotSupportItIgnoring,
			wire:  241,
			label: "thinking_level is set but current provider does not support it, ignoring",
		},
		{
			id:    DiagnosticMessageAgentMDDeclaresUnknownMCPServerNames,
			wire:  242,
			label: "AGENT.md declares unknown MCP server names",
		},
		{
			id:    DiagnosticMessageAgentMDDeclaresUnregisteredToolNames,
			wire:  243,
			label: "AGENT.md declares unregistered tool names",
		},
		{
			id:    DiagnosticMessageAgentUnknownContextManagerFallingBackToLegacy,
			wire:  244,
			label: "Unknown context manager, falling back to legacy",
		},
		{
			id:    DiagnosticMessageAgentFailedToCreateContextManagerFallingBackToLegacy,
			wire:  245,
			label: "Failed to create context manager, falling back to legacy",
		},
		{
			id:    DiagnosticMessageAgentParentTurnEndedNonCriticalSubTurnExitingGracefully,
			wire:  246,
			label: "Parent turn ended, non-critical SubTurn exiting gracefully",
		},
		{
			id:    DiagnosticMessageAgentParentTurnEndedCriticalSubTurnContinuesRunning,
			wire:  247,
			label: "Parent turn ended, critical SubTurn continues running",
		},
		{
			id:    DiagnosticMessageAgentInjectedSteeringMessageIntoContext,
			wire:  248,
			label: "Injected steering message into context",
		},
		{
			id:    DiagnosticMessageAgentModelRouterSelectedTarget,
			wire:  249,
			label: "Model router selected target",
		},
		{
			id:    DiagnosticMessageAgentModelRoutingPrimaryModelSelected,
			wire:  250,
			label: "Model routing: primary model selected",
		},
		{
			id:    DiagnosticMessageAgentModelRoutingLightModelSelected,
			wire:  251,
			label: "Model routing: light model selected",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToAcquireRuntimeEventWorkflowRuntime,
			wire:  252,
			label: "Failed to acquire runtime-event workflow runtime",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToListRuntimeEventWorkflows,
			wire:  253,
			label: "Failed to list runtime-event workflows",
		},
		{
			id:    DiagnosticMessageWorkflowRuntimeEventSkippedUntilRevalidated,
			wire:  254,
			label: "Runtime-event workflow skipped until revalidated",
		},
		{
			id:    DiagnosticMessageWorkflowRuntimeEventTriggerEvaluationFailed,
			wire:  255,
			label: "Workflow runtime-event trigger evaluation failed",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToRetainRuntimeEventWorkflowRuntime,
			wire:  256,
			label: "Failed to retain runtime-event workflow runtime",
		},
		{
			id:    DiagnosticMessageWorkflowRuntimeEventRunFailed,
			wire:  257,
			label: "Runtime-event workflow run failed",
		},
		{
			id:    DiagnosticMessageWorkflowScheduledWorkflowSkippedUntilRevalidated,
			wire:  258,
			label: "Scheduled workflow skipped until revalidated",
		},
		{
			id:    DiagnosticMessageWorkflowInvalidWorkflowScheduleSkipped,
			wire:  259,
			label: "Invalid workflow schedule skipped",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToSubscribeWorkflowRuntimeEvents,
			wire:  260,
			label: "Failed to subscribe workflow runtime events",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToAcquireScheduledWorkflowRuntime,
			wire:  261,
			label: "Failed to acquire scheduled workflow runtime",
		},
		{
			id:    DiagnosticMessageWorkflowScheduledWorkflowHasNoBoundDefinitionSnapshot,
			wire:  262,
			label: "Scheduled workflow has no bound definition snapshot",
		},
		{
			id:    DiagnosticMessageWorkflowScheduledWorkflowContextIsInvalid,
			wire:  263,
			label: "Scheduled workflow context is invalid",
		},
		{
			id:    DiagnosticMessageWorkflowScheduledWorkflowRunFailed,
			wire:  264,
			label: "Scheduled workflow run failed",
		},
		{
			id:    DiagnosticMessageWorkflowScheduledWorkflowGenerationChangedBeforeAdmission,
			wire:  265,
			label: "Scheduled workflow generation changed before admission",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToComputeNextWorkflowSchedule,
			wire:  266,
			label: "Failed to compute next workflow schedule",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToRefreshWorkflowSchedules,
			wire:  267,
			label: "Failed to refresh workflow schedules",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToDeliverHandledWorkflowMedia,
			wire:  268,
			label: "Failed to deliver handled workflow media",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToAcquireWorkflowTriggerRuntime,
			wire:  269,
			label: "Failed to acquire workflow trigger runtime",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToListWorkflows,
			wire:  270,
			label: "Failed to list workflows",
		},
		{
			id:    DiagnosticMessageWorkflowSkippedUntilRevalidated,
			wire:  271,
			label: "Workflow skipped until revalidated",
		},
		{
			id:    DiagnosticMessageWorkflowTriggerEvaluationFailed,
			wire:  272,
			label: "Workflow trigger evaluation failed",
		},
		{
			id:    DiagnosticMessageWorkflowFailedToRetainWorkflowTriggerRuntime,
			wire:  273,
			label: "Failed to retain workflow trigger runtime",
		},
		{
			id:    DiagnosticMessageWorkflowRunFailed,
			wire:  274,
			label: "Workflow run failed",
		},
	}

	const firstWireID = 154
	if len(expected) != 121 {
		t.Fatalf("test manifest has %d messages; want 121", len(expected))
	}
	for offset, item := range expected {
		numericID := firstWireID + offset
		if int(item.id) != item.wire || item.wire != numericID {
			t.Fatalf(
				"named P015b2b diagnostic message at offset %d = %d with declared wire %d; want wire %d",
				offset,
				item.id,
				item.wire,
				numericID,
			)
		}
		label, ok := diagnosticMessageLabel(DiagnosticMessageID(numericID))
		if !ok || label != item.label {
			t.Fatalf(
				"P015b2b diagnostic message wire %d = %q, %v; want %q",
				numericID,
				label,
				ok,
				item.label,
			)
		}
	}

	sharedSourceMessages := [...]struct {
		sources string
		id      DiagnosticMessageID
		wire    int
		label   string
	}{
		{"B028/B044", DiagnosticMessageFailedToResolveMediaRef, 181, "Failed to resolve media ref"},
		{"B071-B078", DiagnosticMessageAgentConfiguredStreamingNotUsed, 223, "configured streaming not used"},
		{
			"B118/B124",
			DiagnosticMessageWorkflowFailedToAcquireScheduledWorkflowRuntime,
			261,
			"Failed to acquire scheduled workflow runtime",
		},
		{
			"B095",
			DiagnosticMessageAgentFailedToEnqueueSteeringMessage,
			129,
			"Failed to enqueue steering message",
		},
		{"B105", DiagnosticMessageLLMIteration, 15, "LLM iteration"},
	}
	for _, item := range sharedSourceMessages {
		label, ok := diagnosticMessageLabel(item.id)
		if int(item.id) != item.wire || !ok || label != item.label {
			t.Errorf(
				"shared source message %s = wire %d label %q, %v; want wire %d label %q",
				item.sources,
				item.id,
				label,
				ok,
				item.wire,
				item.label,
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
		{FieldLogLevel, 104, "log_level", safeFieldKindEnum},
	}

	const firstWireKey = 64
	if len(expectedSpecs) != 41 {
		t.Fatalf("test manifest has %d specs; want 41", len(expectedSpecs))
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
		{SafeEnumDebug, 26},
		{SafeEnumInfo, 27},
		{SafeEnumWarn, 28},
		{SafeEnumError, 29},
		{SafeEnumFatal, 30},
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
		FieldHasSummary != 102 || FieldCacheSensitive != 103 || FieldLogLevel != 104 {
		t.Fatalf(
			"field wire moved: first=%d int64=%d bool=%d enum=%d last=%d",
			FieldIteration,
			FieldDurationMilliseconds,
			FieldAsync,
			FieldState,
			FieldLogLevel,
		)
	}
	if SafeEnumPending != 1 || SafeEnumStopped != 21 ||
		SafeEnumInProcess != 22 || SafeEnumUnknown != 24 ||
		SafeEnumDeveloper != 25 || SafeEnumFatal != 30 || len(safeEnumLabels) != 31 {
		t.Fatalf(
			"safe enum wire moved: first=%d last=%d labels=%d",
			SafeEnumPending, SafeEnumFatal, len(safeEnumLabels),
		)
	}
	seenLabels := make(map[string]FieldKey)
	for key := FieldIteration; key <= FieldLogLevel; key++ {
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
	if label, kind := safeFieldSpec(FieldLogLevel + 1); label != "" || kind != 0 {
		t.Fatalf("key after append-only tail spec = %q, %d", label, kind)
	}

	for _, key := range []FieldKey{
		FieldState, FieldAction, FieldOutcome, FieldRole, FieldReason, FieldSource, FieldLogLevel,
	} {
		for value := SafeEnumPending; value <= SafeEnumFatal; value++ {
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
	if safeEnumAllowed(FieldRole, 0) || safeEnumAllowed(FieldRole, SafeEnumFatal+1) {
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
	for value := SafeEnumPending; value <= SafeEnumFatal; value++ {
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
