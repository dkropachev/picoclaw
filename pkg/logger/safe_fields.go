package logger

import (
	"math"
	"sort"

	"github.com/rs/zerolog"
)

const (
	maxSafeFieldScalars = 128

	safeFieldsStateKey  = "safe_fields_state"
	safeFieldsReasonKey = "safe_fields_reason_code"
	safeFieldsInvalid   = "invalid_fields"
	safeEnvelopeInvalid = "invalid_envelope"
)

// ComponentID selects a fixed logger component label. Values are append-only;
// zero and unknown values fail closed to the logger component.
type ComponentID uint8

const (
	ComponentAgent ComponentID = iota + 1
	ComponentTool
	ComponentTools
	ComponentToolLoop
	ComponentHooks
	ComponentEvents
	ComponentGateway
	ComponentConfig
	ComponentWorkflow
	ComponentVoice
	ComponentProvider
	ComponentAudio
	ComponentMCP
	ComponentSearch
	ComponentFilesystem
	ComponentWeb
	ComponentRuntime
	ComponentEvolution
	ComponentSubturn
	ComponentLogger
	ComponentDevice
	ComponentDiscovery
	ComponentEventing
	ComponentGitWorkspace
	ComponentPRWorkspace
	ComponentSeahorse
	ComponentShell
	ComponentVoiceTTS
	ComponentWebSearch
)

var componentLabels = [...]string{
	"",
	"agent",
	"tool",
	"tools",
	"toolloop",
	"hooks",
	"events",
	"gateway",
	"config",
	"workflow",
	"voice",
	"provider",
	"audio",
	"mcp",
	"search",
	"filesystem",
	"web",
	"runtime",
	"evolution",
	"subturn",
	"logger",
	"device",
	"discovery",
	"eventing",
	"git-workspace",
	"pr-workspace",
	"seahorse",
	"shell",
	"voice-tts",
	"web_search",
}

// DiagnosticMessageID selects a fixed literal message. Values are append-only
// so later sink migrations can extend the closed inventory without changing
// existing records.
type DiagnosticMessageID uint16

const (
	DiagnosticMessageEvent DiagnosticMessageID = iota + 1
	DiagnosticMessageInboundMessage
	DiagnosticMessageHistoryMessage
	DiagnosticMessageSystemPrompt
	DiagnosticMessageModelResponse
	DiagnosticMessageModelReasoning
	DiagnosticMessageToolArguments
	DiagnosticMessageHookToolArguments
	DiagnosticMessageToolExecutionStarted
	DiagnosticMessageToolExecutionCompleted
	DiagnosticMessageToolExecutionFailed
	DiagnosticMessageToolExecutionPanic
	DiagnosticMessageToolArgumentValidationFailed
	DiagnosticMessageToolNotFound
	DiagnosticMessageLLMIteration
	DiagnosticMessageLLMCallFailed
	DiagnosticMessageLLMDirectResponse
	DiagnosticMessageLLMRequestedToolCalls
	DiagnosticMessageProcessHookStderr
	DiagnosticMessageHookDecodeFailed
	DiagnosticMessageRuntimeEvent
	DiagnosticMessageToolCall
	DiagnosticMessageToolRegistrationSkipped
	DiagnosticMessageToolRegistrationCollision
	DiagnosticMessageToolRegistrationOverwritten
	DiagnosticMessageToolRegistered
	DiagnosticMessageToolUnregistered
	DiagnosticMessageToolPromotionCompleted
	DiagnosticMessageToolAsyncStarted
	DiagnosticMessageSubagentRunnerPanic
	DiagnosticMessageSubagentCallbackPanic
	DiagnosticMessageSubagentFinalizerPanic
	DiagnosticMessageHookEventSubscribeFailed
	DiagnosticMessageHookEventSubscriptionCloseFailed
	DiagnosticMessageHookUntrustedMutationDiscarded
	DiagnosticMessageHookBeforeLLMRequestInvalid
	DiagnosticMessageHookBeforeLLMInputInvalid
	DiagnosticMessageHookBeforeLLMMutationInvalid
	DiagnosticMessageHookAfterLLMResponseInvalid
	DiagnosticMessageHookAfterLLMInputInvalid
	DiagnosticMessageHookAfterLLMMutationInvalid
	DiagnosticMessageHookSystemPromptMutationRejected
	DiagnosticMessageHookToolDefinitionsMutationRejected
	DiagnosticMessageHookAfterToolResultInvalid
	DiagnosticMessageHookAfterToolInputInvalid
	DiagnosticMessageHookAfterToolMutationInvalid
	DiagnosticMessageHookRuntimeObserverFailed
	DiagnosticMessageHookRuntimeObserverTimedOut
	DiagnosticMessageHookInterceptorFailed
	DiagnosticMessageHookInterceptorTimedOut
	DiagnosticMessageHookApprovalFailed
	DiagnosticMessageHookApprovalTimedOut
	DiagnosticMessageHookUnsupportedAction
	DiagnosticMessageHookCloseFailed
	DiagnosticMessageAgentAccountRouterReselectedAfterContextCompression
	DiagnosticMessageAgentApplyingPendingSkillOverride
	DiagnosticMessageAgentAsyncToolCompletedPublishingResult
	DiagnosticMessageAgentContextOverflowCompactFailed
	DiagnosticMessageAgentContextStillExceedsBudgetAfterRetryCompactionRebuild
	DiagnosticMessageAgentContextWindowErrorDetectedAttemptingCompression
	DiagnosticMessageAgentDroppingAssistantMessageWithEmptyToolCallID
	DiagnosticMessageAgentDroppingAssistantMessageWithIncompleteToolResults
	DiagnosticMessageAgentDroppingAssistantToolCallTurnAtHistoryStart
	DiagnosticMessageAgentDroppingAssistantToolCallTurnWithInvalidPredecessor
	DiagnosticMessageAgentDroppingDuplicateToolResultInToolBlock
	DiagnosticMessageAgentDroppingOrphanedLeadingToolMessage
	DiagnosticMessageAgentDroppingOrphanedToolMessageAfterValidation
	DiagnosticMessageAgentDroppingOrphanedToolMessage
	DiagnosticMessageAgentDroppingSystemMessageFromHistory
	DiagnosticMessageAgentDroppingToolResultWithoutToolCallID
	DiagnosticMessageAgentDroppingUnexpectedToolResult
	DiagnosticMessageAgentFailedToApplySimpleToolSurface
	DiagnosticMessageAgentFailedToDeliverHandledToolMedia
	DiagnosticMessageAgentFailedToDeliverHookMedia
	DiagnosticMessageAgentFailedToFinalizeStreamedPicoReasoning
	DiagnosticMessageAgentFailedToRegisterAgentDiscoveryPromptContributor
	DiagnosticMessageAgentFailedToRegisterThreadPolicyPromptContributor
	DiagnosticMessageAgentFailedToRegisterToolDiscoveryPromptContributor
	DiagnosticMessageAgentFailedToSaveSessionAfterToolDelivery
	DiagnosticMessageAgentForcedCompressionExecuted
	DiagnosticMessageAgentFullLLMRequest
	DiagnosticMessageAgentHookReturnedRespondActionButNoHookResultProvided
	DiagnosticMessageAgentLLMRequest
	DiagnosticMessageAgentLLMResponseWithoutToolCallsDirectAnswer
	DiagnosticMessageAgentLLMResponse
	DiagnosticMessageAgentMemoryThresholdReachedOptimizingConversationHistory
	DiagnosticMessageAgentObservedToolAdaptationCacheBehavior
	DiagnosticMessageAgentObservedToolAdaptationOutcome
	DiagnosticMessageAgentPendingSteeringAfterPartialToolExecutionContinuingTurn
	DiagnosticMessageAgentProcessingSystemMessage
	DiagnosticMessageAgentPromptContributorCollectionFailed
	DiagnosticMessageAgentProviderReloadGracePeriodExpiredWithInFlightRequests
	DiagnosticMessageAgentProviderReloadInterruptedWhileWaitingForInFlightRequests
	DiagnosticMessageAgentRoutedMessage
	DiagnosticMessageAgentSentToolResultToUser
	DiagnosticMessageAgentSkippingInvalidPromptOverlay
	DiagnosticMessageAgentSkippingInvalidPromptPart
	DiagnosticMessageAgentSteeringArrivedAfterDirectLLMResponseContinuingTurn
	DiagnosticMessageAgentSteeringArrivedAfterToolDeliveryContinuingTurn
	DiagnosticMessageAgentSubagentCompletedInternalChannel
	DiagnosticMessageAgentSummarizationPanicRecovered
	DiagnosticMessageAgentSystemPromptBuilt
	DiagnosticMessageAgentSystemPromptCacheInvalidated
	DiagnosticMessageAgentSystemPromptCached
	DiagnosticMessageAgentSystemPromptPreview
	DiagnosticMessageAgentTTLTickAfterToolExecution
	DiagnosticMessageAgentToolOutputSatisfiedDeliveryEndingTurnWithoutFollowUpLLM
	DiagnosticMessageAgentTrackedSpawnCompletionHasNoValidParentRoute
	DiagnosticMessageAgentTransientLLMErrorRetryingAfterBackoff
	DiagnosticMessageAgentTrimmedRebuiltHistoryAfterContextRetryCompaction
	DiagnosticMessageAgentTurnCheckpointSkippingRemainingToolsAfterHookRespond
	DiagnosticMessageAgentTurnCheckpointSkippingRemainingTools
	DiagnosticMessageAgentSkillsWalkError
	DiagnosticMessageAgentFallbackSucceeded
	DiagnosticMessageAgentConfiguredHooksFailedToReinitializeAfterReload
	DiagnosticMessageAgentContextManagerIngestFailed
	DiagnosticMessageAgentDeferredTurnResourceCleanupFailed
	DiagnosticMessageAgentDepthLimitExceeded
	DiagnosticMessageAgentFailedToAcquireInboundMessageRuntime
	DiagnosticMessageAgentFailedToActivateReloadedEvolutionBridge
	DiagnosticMessageAgentFailedToCloseMCPManager
	DiagnosticMessageAgentFailedToCloseContextManager
	DiagnosticMessageAgentFailedToCloseEvolutionBridge
	DiagnosticMessageAgentFailedToClosePreviousMCPManagerDuringReload
	DiagnosticMessageAgentFailedToClosePreviousContextManagerDuringReload
	DiagnosticMessageAgentFailedToClosePreviousEvolutionBridgeDuringReload
	DiagnosticMessageAgentFailedToCloseReloadedEvolutionCandidate
	DiagnosticMessageAgentFailedToCloseRuntimeEventBus
	DiagnosticMessageAgentFailedToEnqueueSteeringMessage
	DiagnosticMessageAgentFailedToPublishFollowUpAfterTurn
	DiagnosticMessageAgentFailedToRecordLastChannel
	DiagnosticMessageAgentFailedToReinitializeEvolutionBridgeDuringReload
	DiagnosticMessageAgentFailedToResumeSteeringAfterReservationAbandonment
	DiagnosticMessageAgentFailedToRetainInboundMessageRuntime
	DiagnosticMessageAgentFailedToSubscribeReloadedEvolutionBridgeToRuntimeEvents
	DiagnosticMessageAgentMCPFailedToReinitializeAfterReload
	DiagnosticMessageAgentPanicDuringRegistryCreation
	DiagnosticMessageAgentPostCommitSeahorseCatalogHandlingPanicked
	DiagnosticMessageAgentProviderAndConfigReloadedSuccessfully
	DiagnosticMessageAgentSeahorseAdmissionProjectionFailedAfterCatalogCommit
	DiagnosticMessageAgentSteeringRescuePanicked
	DiagnosticMessageAgentSubTurnPanicked
	DiagnosticMessageAgentTrackedSubagentResultContinuationFailed
	DiagnosticMessageAgentTrackedSubagentResultContinuationRejected
	DiagnosticMessageAgentTrackedSubagentResultOutboundWasNotAccepted
	DiagnosticMessageAgentTrackedSubagentSteeringRescueFailed
	DiagnosticMessageAgentTrackedSubagentSteeringRescueRecheckFailed
	DiagnosticMessageAgentTrackedSubagentSteeringRescueRejected
	DiagnosticMessageAgentWorkerGoroutinePanicked
	DiagnosticMessageAgentTrackedSubagentEventPanicRecovered
	DiagnosticMessageAgentTrackedSubagentTurnTerminalPanicRecovered
	DiagnosticMessageAgentTrackedSubagentResultPumpPanicRecovered
	DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered
	DiagnosticMessageAgentAccountRouterModelAliasesAreInvalid
	DiagnosticMessageAgentAccountRouterAccountHasNoRunnableModelAlias
	DiagnosticMessageAgentFailedToRefreshMCPStatusForCommand
	DiagnosticMessageAgentFailedToInitializeMCPRuntimeForCommand
	DiagnosticMessageAgentFailedToInitializeEvolutionBridge
	DiagnosticMessageAgentFailedToSubscribeEvolutionBridgeToRuntimeEvents
	DiagnosticMessageAgentFailedToActivateEvolutionBridge
	DiagnosticMessageAgentFailedToInitializeAgentActivityRecorder
	DiagnosticMessageAgentFailedToInstallSharedRecursionToolCatalog
	DiagnosticMessageVoiceTTSSendTTSEnabledButNoTTSProviderConfigured
	DiagnosticMessageAgentFailedToCreateWebSearchTool
	DiagnosticMessageAgentFailedToCreateWebFetchTool
	DiagnosticMessageAgentSpawnSpawnStatusToolsRequireSubagentToBeEnabled
	DiagnosticMessageAgentMCPIsEnabledButNoServersAreConfiguredSkippingMCPInitialization
	DiagnosticMessageAgentNoMCPServersSelectedAfterApplyingPerAgentMCPServerAllowlists
	DiagnosticMessageAgentMCPIsEnabledButNoValidServersAreConfiguredSkippingMCPInitialization
	DiagnosticMessageAgentFailedToInitializeMCPGeneration
	DiagnosticMessageAgentMCPAdmissionProjectionFailedAfterCatalogCommit
	DiagnosticMessageAgentMCPPromptPreparationFailedAfterCatalogCommit
	DiagnosticMessageAgentMCPPromptPublicationWasIncomplete
	DiagnosticMessageAgentMCPFactoryCatalogInstalledSuccessfully
	DiagnosticMessageAgentMCPPostCommitPublicationPanickedRetainedCommittedManager
	DiagnosticMessageAgentMediaFileTooLargeSkipping
	DiagnosticMessageAgentFailedToOpenMediaFile
	DiagnosticMessageAgentFailedToEncodeMediaFile
	DiagnosticMessageAgentFailedToCloseBase64Encoder
	DiagnosticMessageAgentSkippedStaleHistoricalMediaRef
	DiagnosticMessageFailedToResolveMediaRef
	DiagnosticMessageAgentFailedToStatMediaFile
	DiagnosticMessageAgentReasoningPublishSkippedTimeoutCancel
	DiagnosticMessageAgentFailedToPublishReasoningBestEffort
	DiagnosticMessageAgentPicoReasoningPublishSkippedTimeoutCancel
	DiagnosticMessageAgentFailedToPublishPicoReasoningBestEffort
	DiagnosticMessageAgentFailedToPublishPicoReasoning
	DiagnosticMessageAgentFailedToPublishPicoInterimAssistantContent
	DiagnosticMessageAgentFailedToSerializePicoToolCalls
	DiagnosticMessageAgentFailedToPublishPicoToolCalls
	DiagnosticMessageAgentSkippedOutboundMessageToolAlreadySentToSameChat
	DiagnosticMessageAgentPublishedOutboundResponse
	DiagnosticMessageAgentContinuingQueuedSteeringAfterTurnEnd
	DiagnosticMessageAgentFailedToBuildSteeringContinuationTarget
	DiagnosticMessageAgentFailedToContinueQueuedSteering
	DiagnosticMessageVoiceFailedToSendTranscriptionFeedback
	DiagnosticMessageVoiceTranscriptionFailed
	DiagnosticMessageAgentMaxTokensGreaterThanOrEqualToBudgetUsing50PercentFallback
	DiagnosticMessageSeahorseBootstrapSnapshot
	DiagnosticMessageAgentFailedToParseAgentMDFrontmatter
	DiagnosticMessageAgentFailedToCloseEvolutionRuntimeSubscription
	DiagnosticMessageAgentEvolutionFinalizeTurnFailed
	DiagnosticMessageAgentNoValidEvolutionColdPathScheduleTimesConfigured
	DiagnosticMessageAgentColdPathRunFailed
	DiagnosticMessageGitWorkspaceFailedToReleaseGitWorkspaceLocks
	DiagnosticMessageGitWorkspaceFailedToReconcileGitWorkspaceRetention
	DiagnosticMessageGitWorkspaceFailedToInitializeGitWorkspaceManager
	DiagnosticMessageAgentFailedToClosePartiallyConstructedAgent
	DiagnosticMessageAgentResolvedToolAdaptationProfile
	DiagnosticMessageAgentFailedToInitializeExecToolContinuingWithoutExec
	DiagnosticMessageAgentRoutingLightModelNotFoundRoutingDisabled
	DiagnosticMessageAgentInvalidPathPatternInCompilePatterns
	DiagnosticMessageAgentMemoryJSONLStoreInitFailedFallingBackToJSONSessions
	DiagnosticMessageAgentMemoryMigrationFailedFallingBackToJSONSessions
	DiagnosticMessageAgentMemoryMigratedToJSONL
	DiagnosticMessageAgentUnsubscribeEventsUnexpectedTypeInSubscriptionMap
	DiagnosticMessageAgentMediaTurnRoutingSelectedModel
	DiagnosticMessageAgentProactiveCompressionContextBudgetExceededBeforeLLMCall
	DiagnosticMessageAgentProactiveCompactFailed
	DiagnosticMessageAgentTrimmedRebuiltHistoryAfterProactiveCompaction
	DiagnosticMessageAgentContextStillExceedsBudgetAfterProactiveCompactionRebuild
	DiagnosticMessageAgentChannelStreamingConfigDecodeFailed
	DiagnosticMessageAgentConfiguredStreamingNotUsed
	DiagnosticMessageAgentConfiguredStreamingEnabled
	DiagnosticMessageAgentChatStreamUpdateFailedAfterVisibleOutput
	DiagnosticMessageAgentChatStreamUpdateFailedBeforeVisibleOutputRetryingWithChat
	DiagnosticMessageAgentChatStreamFailedBeforeVisibleOutputRetryingWithChat
	DiagnosticMessageAgentStreamUpdateFailed
	DiagnosticMessageAgentStreamReasoningUpdateFailed
	DiagnosticMessageAgentStreamFinalFlushFailedAfterVisibleOutput
	DiagnosticMessageAgentStreamFinalFlushFailed
	DiagnosticMessageAgentConfiguredStreamingCompleted
	DiagnosticMessageAgentUnregisteredPromptSourceAllowedInCompatibilityMode
	DiagnosticMessageAgentFailedToRegisterBuiltinPromptSource
	DiagnosticMessageAgentRecursionAdmissionProjectionFailedAfterCatalogCommit
	DiagnosticMessageAgentFailedToCloseAgent
	DiagnosticMessageAgentCreatedImplicitMainAgentNoAgentsListConfigured
	DiagnosticMessageAgentRegisteredAgent
	DiagnosticMessageAgentHardAbortTriggered
	DiagnosticMessageAgentSteeringMessageEnqueued
	DiagnosticMessageAgentThinkingLevelIsSetButCurrentProviderDoesNotSupportItIgnoring
	DiagnosticMessageAgentMDDeclaresUnknownMCPServerNames
	DiagnosticMessageAgentMDDeclaresUnregisteredToolNames
	DiagnosticMessageAgentUnknownContextManagerFallingBackToLegacy
	DiagnosticMessageAgentFailedToCreateContextManagerFallingBackToLegacy
	DiagnosticMessageAgentParentTurnEndedNonCriticalSubTurnExitingGracefully
	DiagnosticMessageAgentParentTurnEndedCriticalSubTurnContinuesRunning
	DiagnosticMessageAgentInjectedSteeringMessageIntoContext
	DiagnosticMessageAgentModelRouterSelectedTarget
	DiagnosticMessageAgentModelRoutingPrimaryModelSelected
	DiagnosticMessageAgentModelRoutingLightModelSelected
	DiagnosticMessageWorkflowFailedToAcquireRuntimeEventWorkflowRuntime
	DiagnosticMessageWorkflowFailedToListRuntimeEventWorkflows
	DiagnosticMessageWorkflowRuntimeEventSkippedUntilRevalidated
	DiagnosticMessageWorkflowRuntimeEventTriggerEvaluationFailed
	DiagnosticMessageWorkflowFailedToRetainRuntimeEventWorkflowRuntime
	DiagnosticMessageWorkflowRuntimeEventRunFailed
	DiagnosticMessageWorkflowScheduledWorkflowSkippedUntilRevalidated
	DiagnosticMessageWorkflowInvalidWorkflowScheduleSkipped
	DiagnosticMessageWorkflowFailedToSubscribeWorkflowRuntimeEvents
	DiagnosticMessageWorkflowFailedToAcquireScheduledWorkflowRuntime
	DiagnosticMessageWorkflowScheduledWorkflowHasNoBoundDefinitionSnapshot
	DiagnosticMessageWorkflowScheduledWorkflowContextIsInvalid
	DiagnosticMessageWorkflowScheduledWorkflowRunFailed
	DiagnosticMessageWorkflowScheduledWorkflowGenerationChangedBeforeAdmission
	DiagnosticMessageWorkflowFailedToComputeNextWorkflowSchedule
	DiagnosticMessageWorkflowFailedToRefreshWorkflowSchedules
	DiagnosticMessageWorkflowFailedToDeliverHandledWorkflowMedia
	DiagnosticMessageWorkflowFailedToAcquireWorkflowTriggerRuntime
	DiagnosticMessageWorkflowFailedToListWorkflows
	DiagnosticMessageWorkflowSkippedUntilRevalidated
	DiagnosticMessageWorkflowTriggerEvaluationFailed
	DiagnosticMessageWorkflowFailedToRetainWorkflowTriggerRuntime
	DiagnosticMessageWorkflowRunFailed
)

var diagnosticMessageLabels = [...]string{
	"",
	"Diagnostic event",
	"Processing inbound message",
	"History message diagnostics",
	"System prompt diagnostics",
	"Model response diagnostics",
	"Model reasoning diagnostics",
	"Tool arguments diagnostics",
	"Hook tool arguments diagnostics",
	"Tool execution started",
	"Tool execution completed",
	"Tool execution failed",
	"Tool execution panic recovered",
	"Tool argument validation failed",
	"Tool not found",
	"LLM iteration",
	"LLM call failed",
	"LLM response without tool calls",
	"LLM requested tool calls",
	"Process hook stderr",
	"Failed to decode process hook message",
	"Runtime event",
	"Tool call",
	"Tool registration skipped",
	"Tool registration collides with private dependency",
	"Tool registration overwrites existing tool",
	"Tool registered",
	"Tool unregistered",
	"Tool promotion completed",
	"Tool started asynchronously",
	"Subagent task runner panic recovered",
	"Subagent callback panic recovered",
	"Subagent finalizer panic recovered",
	"Failed to subscribe runtime events for hooks",
	"Failed to close runtime event hook subscription",
	"Discarded mutation from untrusted hook",
	"Skipping BeforeLLM hooks for invalid detached request",
	"Skipping BeforeLLM hook for invalid detached request",
	"Discarded invalid BeforeLLM hook mutation",
	"Skipping AfterLLM hooks for invalid detached response",
	"Skipping AfterLLM hook for invalid detached response",
	"Discarded invalid AfterLLM hook mutation",
	"Hook attempted to modify system prompt",
	"Hook attempted to modify tool definitions",
	"Skipping AfterTool hooks for invalid detached result",
	"Skipping AfterTool hook for invalid detached result",
	"Discarded invalid AfterTool hook mutation",
	"Runtime event observer failed",
	"Runtime event observer timed out",
	"Interceptor hook failed",
	"Interceptor hook timed out",
	"Approval hook failed",
	"Approval hook timed out",
	"Hook returned unsupported action for stage",
	"Failed to close hook",
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
	"Account router model aliases are invalid",
	"Account router account has no runnable model alias",
	"Failed to refresh MCP status for command",
	"Failed to initialize MCP runtime for command",
	"Failed to initialize evolution bridge",
	"Failed to subscribe evolution bridge to runtime events",
	"Failed to activate evolution bridge",
	"Failed to initialize agent activity recorder",
	"Failed to install shared recursion tool catalog",
	"send_tts enabled but no TTS provider configured",
	"Failed to create web search tool",
	"Failed to create web fetch tool",
	"spawn/spawn_status tools require subagent to be enabled",
	"MCP is enabled but no servers are configured, skipping MCP initialization",
	"No MCP servers selected after applying per-agent mcpServers allowlists",
	"MCP is enabled but no valid servers are configured, skipping MCP initialization",
	"Failed to initialize MCP generation",
	"MCP admission projection failed after catalog commit",
	"MCP prompt preparation failed after catalog commit",
	"MCP prompt publication was incomplete",
	"MCP factory catalog installed successfully",
	"MCP post-commit publication panicked; retained committed manager",
	"Media file too large, skipping",
	"Failed to open media file",
	"Failed to encode media file",
	"Failed to close base64 encoder",
	"Skipped stale historical media ref",
	"Failed to resolve media ref",
	"Failed to stat media file",
	"Reasoning publish skipped (timeout/cancel)",
	"Failed to publish reasoning (best-effort)",
	"Pico reasoning publish skipped (timeout/cancel)",
	"Failed to publish pico reasoning (best-effort)",
	"Failed to publish pico reasoning",
	"Failed to publish pico interim assistant content",
	"Failed to serialize pico tool calls",
	"Failed to publish pico tool calls",
	"Skipped outbound (message tool already sent to same chat)",
	"Published outbound response",
	"Continuing queued steering after turn end",
	"Failed to build steering continuation target",
	"Failed to continue queued steering",
	"Failed to send transcription feedback",
	"Transcription failed",
	"MaxTokens >= budget, using 50% fallback",
	"bootstrap snapshot",
	"Failed to parse AGENT.md frontmatter",
	"Failed to close evolution runtime subscription",
	"Evolution finalize turn failed",
	"No valid evolution cold path schedule times configured",
	"Cold path run failed",
	"Failed to release git workspace locks",
	"Failed to reconcile git workspace retention",
	"Failed to initialize git workspace manager",
	"Failed to close partially constructed agent",
	"Resolved tool adaptation profile",
	"Failed to initialize exec tool; continuing without exec",
	"Routing light model not found; routing disabled",
	"invalid path pattern in compilePatterns",
	"Memory JSONL store init failed; falling back to json sessions",
	"Memory migration failed; falling back to json sessions",
	"Memory migrated to JSONL",
	"UnsubscribeEvents: unexpected type in subscription map",
	"Media turn routing selected model",
	"Proactive compression: context budget exceeded before LLM call",
	"Proactive compact failed",
	"Trimmed rebuilt history after proactive compaction",
	"Context still exceeds budget after proactive compaction rebuild",
	"channel streaming config decode failed",
	"configured streaming not used",
	"configured streaming enabled",
	"ChatStream update failed after visible output",
	"ChatStream update failed before visible output; retrying with Chat",
	"ChatStream failed before visible output; retrying with Chat",
	"stream update failed",
	"stream reasoning update failed",
	"stream final flush failed after visible output",
	"stream final flush failed",
	"configured streaming completed",
	"Unregistered prompt source allowed in compatibility mode",
	"Failed to register builtin prompt source",
	"Recursion admission projection failed after catalog commit",
	"Failed to close agent",
	"Created implicit main agent (no agents.list configured)",
	"Registered agent",
	"Hard abort triggered",
	"Steering message enqueued",
	"thinking_level is set but current provider does not support it, ignoring",
	"AGENT.md declares unknown MCP server names",
	"AGENT.md declares unregistered tool names",
	"Unknown context manager, falling back to legacy",
	"Failed to create context manager, falling back to legacy",
	"Parent turn ended, non-critical SubTurn exiting gracefully",
	"Parent turn ended, critical SubTurn continues running",
	"Injected steering message into context",
	"Model router selected target",
	"Model routing: primary model selected",
	"Model routing: light model selected",
	"Failed to acquire runtime-event workflow runtime",
	"Failed to list runtime-event workflows",
	"Runtime-event workflow skipped until revalidated",
	"Workflow runtime-event trigger evaluation failed",
	"Failed to retain runtime-event workflow runtime",
	"Runtime-event workflow run failed",
	"Scheduled workflow skipped until revalidated",
	"Invalid workflow schedule skipped",
	"Failed to subscribe workflow runtime events",
	"Failed to acquire scheduled workflow runtime",
	"Scheduled workflow has no bound definition snapshot",
	"Scheduled workflow context is invalid",
	"Scheduled workflow run failed",
	"Scheduled workflow generation changed before admission",
	"Failed to compute next workflow schedule",
	"Failed to refresh workflow schedules",
	"Failed to deliver handled workflow media",
	"Failed to acquire workflow trigger runtime",
	"Failed to list workflows",
	"Workflow skipped until revalidated",
	"Workflow trigger evaluation failed",
	"Failed to retain workflow trigger runtime",
	"Workflow run failed",
}

// FieldKey selects one fixed structured key and its required value type.
// Values are append-only because source/AST manifests may inventory them.
type FieldKey uint8

const (
	// FieldIteration begins fixed int fields.
	FieldIteration FieldKey = iota + 1
	FieldMaxIterations
	FieldAttempt
	FieldRetryCount
	FieldDepth
	FieldMaxDepth
	FieldCount
	FieldMessageCount
	FieldHistoryMessageCount
	FieldMediaCount
	FieldToolCount
	FieldToolCallCount
	FieldArgumentCount
	FieldResultCount
	FieldModelCount
	FieldPromptPartCount
	FieldOverlayCount
	FieldSystemMessageCount
	FieldUserMessageCount
	FieldAssistantMessageCount
	FieldToolMessageCount
	FieldUnknownCount
	FieldActiveCount
	FieldSuccessCount
	FieldFailureCount
	FieldDroppedCount
	FieldQueueDepth
	FieldIndex

	// FieldDurationMilliseconds begins fixed int64 fields.
	FieldDurationMilliseconds
	FieldBytes
	FieldStaticBytes
	FieldDynamicBytes
	FieldTotalBytes
	FieldContentBytes
	FieldArgumentsBytes
	FieldResultBytes
	FieldSchemaBytes
	FieldInputBytes
	FieldOutputBytes
	FieldLimitBytes
	FieldTimeoutMilliseconds

	// FieldAsync begins fixed bool fields.
	FieldAsync
	FieldSuccess
	FieldTruncated
	FieldStreaming
	FieldCached
	FieldSuppressed
	FieldAccepted
	FieldEnabled
	FieldRetryable
	FieldDelivered
	FieldHandled
	FieldChanged
	FieldAvailable

	// FieldState begins fixed-enum fields.
	FieldState
	FieldAction
	FieldOutcome
	FieldRole
	FieldReason
	FieldRequestedCount
	FieldPromotedCount
	FieldCore
	FieldSource
	// FieldMaxTokens begins the closed cross-slice P015b2 field block. Its
	// numeric meanings were allocated from the exact A/B/G/C source census;
	// fields not first consumed by P015b2a are intentionally reserved for the
	// immediately following P015b2b/P015b2c migrations and must not be reused.
	FieldMaxTokens
	FieldContextWindow
	FieldPromptTokens
	FieldCompletionTokens
	FieldTotalTokens
	FieldCachedTokens
	FieldReasoningTokens
	FieldMaxRetries
	FieldChunkCount
	FieldExpectedCount
	FieldFoundCount
	FieldPendingCount
	FieldRemainingCount
	FieldCompletedCount
	FieldSkippedCount
	FieldAgentCount
	FieldServerCount
	FieldSkillCount
	FieldAvailableCount
	FieldNotificationCount
	FieldMatchedCount
	FieldInsertedCount
	FieldBackoffMilliseconds
	FieldGraceMilliseconds
	FieldChunkSpanMilliseconds
	FieldTemperature
	FieldScore
	FieldThreshold
	FieldCacheHitRatio
	FieldHasReasoning
	FieldGracefulTerminal
	FieldASREnabled
	FieldTTSEnabled
	FieldDebugEnabled
	FieldAllowEmpty
	FieldLimitedMode
	FieldCacheHit
	FieldFallback
	FieldHasSummary
	FieldCacheSensitive
)

type safeFieldKind uint8

const (
	safeFieldKindInt safeFieldKind = iota + 1
	safeFieldKindInt64
	safeFieldKindBool
	safeFieldKindEnum
	safeFieldKindObservation
	safeFieldKindFloat64
)

// SafeEnumValue is a fixed value for enum-valued SafeFields. It cannot carry
// caller-controlled text.
type SafeEnumValue uint8

const (
	SafeEnumPending SafeEnumValue = iota + 1
	SafeEnumStarted
	SafeEnumCompleted
	SafeEnumFailed
	SafeEnumCanceled
	SafeEnumAllowed
	SafeEnumDenied
	SafeEnumDirect
	SafeEnumToolCalls
	SafeEnumSync
	SafeEnumAsync
	SafeEnumSystem
	SafeEnumUser
	SafeEnumAssistant
	SafeEnumTool
	SafeEnumTimeout
	SafeEnumSkipped
	SafeEnumUnavailable
	SafeEnumReady
	SafeEnumRunning
	SafeEnumStopped
	SafeEnumInProcess
	SafeEnumProcess
	SafeEnumUnknown
	// SafeEnumDeveloper is reserved from the provider-neutral role census for
	// the deferred Agent history/lifecycle slice.
	SafeEnumDeveloper
)

var safeEnumLabels = [...]string{
	"",
	"pending",
	"started",
	"completed",
	"failed",
	"canceled",
	"allowed",
	"denied",
	"direct",
	"tool_calls",
	"sync",
	"async",
	"system",
	"user",
	"assistant",
	"tool",
	"timeout",
	"skipped",
	"unavailable",
	"ready",
	"running",
	"stopped",
	"in_process",
	"process",
	"unknown",
	"developer",
}

// SafeField is one opaque typed field constructor result.
type SafeField struct {
	key          FieldKey
	kind         safeFieldKind
	intValue     int
	int64Value   int64
	float64Value float64
	boolValue    bool
	enumValue    SafeEnumValue
	prefix       ObservationFieldPrefix
	observation  Observation
	valid        bool
}

// SafeInt constructs a non-negative int field only for an int FieldKey.
func SafeInt(key FieldKey, value int) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindInt, intValue: value,
		valid: kind == safeFieldKindInt && value >= 0,
	}
}

// SafeInt64 constructs a non-negative int64 field only for an int64 FieldKey.
func SafeInt64(key FieldKey, value int64) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindInt64, int64Value: value,
		valid: kind == safeFieldKindInt64 && value >= 0,
	}
}

// SafeFloat64 constructs a finite float64 field only for a float FieldKey.
func SafeFloat64(key FieldKey, value float64) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindFloat64, float64Value: value,
		valid: kind == safeFieldKindFloat64 && !math.IsNaN(value) && !math.IsInf(value, 0),
	}
}

// SafeBool constructs a bool field only for a bool FieldKey.
func SafeBool(key FieldKey, value bool) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindBool, boolValue: value,
		valid: kind == safeFieldKindBool,
	}
}

// SafeEnum constructs a fixed enum field only for an enum FieldKey.
func SafeEnum(key FieldKey, value SafeEnumValue) SafeField {
	_, kind := safeFieldSpec(key)
	return SafeField{
		key: key, kind: safeFieldKindEnum, enumValue: value,
		valid: kind == safeFieldKindEnum && safeEnumAllowed(key, value),
	}
}

// SafeObservation expands one sealed Observation under its fixed prefix.
func SafeObservation(prefix ObservationFieldPrefix, observation Observation) SafeField {
	_, prefixOK := observationPrefixLabel(prefix)
	return SafeField{
		kind: safeFieldKindObservation, prefix: prefix, observation: observation,
		valid: prefixOK && observation.valid &&
			observation.expectedPrefix == prefix && validObservation(observation),
	}
}

// SafeFields is an immutable collection of typed safe fields. NewSafeFields
// copies the source slice. Zero is invalid; use NewSafeFields() for valid empty
// fields.
type SafeFields struct {
	entries     []SafeField
	preview     *sensitivePreviewWire
	scalarCount int
	valid       bool
}

// NewSafeFields validates and detaches entries. Duplicate keys/prefixes and
// invalid constructor results fail the whole collection closed.
func NewSafeFields(entries ...SafeField) SafeFields {
	if len(entries) > maxSafeFieldScalars {
		return SafeFields{}
	}
	copyEntries := append([]SafeField(nil), entries...)
	seenKeys := make(map[FieldKey]struct{}, len(copyEntries))
	seenPrefixes := make(map[ObservationFieldPrefix]struct{}, len(copyEntries))
	scalarCount := 0
	for _, entry := range copyEntries {
		if !safeFieldValid(entry) {
			return SafeFields{}
		}
		if entry.kind == safeFieldKindObservation {
			scalarCount += 8
			if _, exists := seenPrefixes[entry.prefix]; exists {
				return SafeFields{}
			}
			seenPrefixes[entry.prefix] = struct{}{}
			continue
		}
		scalarCount++
		if _, exists := seenKeys[entry.key]; exists {
			return SafeFields{}
		}
		seenKeys[entry.key] = struct{}{}
	}
	if scalarCount > maxSafeFieldScalars {
		return SafeFields{}
	}
	sort.Slice(copyEntries, func(left, right int) bool {
		return safeFieldSortLabel(copyEntries[left]) < safeFieldSortLabel(copyEntries[right])
	})
	return SafeFields{entries: copyEntries, scalarCount: scalarCount, valid: true}
}

func (fields SafeFields) withSensitivePreview(preview sensitivePreviewWire) SafeFields {
	if !fields.valid || fields.scalarCount >= maxSafeFieldScalars {
		return fields
	}
	fields.preview = &preview
	fields.scalarCount++
	return fields
}

// DebugSafeCF emits a safe Debug record with a fixed component and typed fields.
func DebugSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(DEBUG, component, message, fields, "")
}

// InfoSafeCF emits a safe Info record with a fixed component and typed fields.
func InfoSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(INFO, component, message, fields, "")
}

// WarnSafeCF emits a safe Warn record with a fixed component and typed fields.
func WarnSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(WARN, component, message, fields, "")
}

// ErrorSafeCF emits a safe Error record with a fixed component and typed fields.
func ErrorSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	_ = emitSafeRecord(ERROR, component, message, fields, "")
}

// FatalSafeCF emits a safe Fatal record with a fixed component and typed fields.
func FatalSafeCF(component ComponentID, message DiagnosticMessageID, fields SafeFields) {
	if emitSafeRecord(FATAL, component, message, fields, "") {
		return
	}
	// Fatal termination is independent of the package's output threshold. A
	// disabled zerolog logger invokes the configured fatal exit callback without
	// writing, preserving suppression while retaining conventional Fatal
	// semantics.
	disabled := zerolog.Nop()
	disabled.Fatal().Msg("")
}

func emitSafeRecord(
	level LogLevel,
	component ComponentID,
	message DiagnosticMessageID,
	fields SafeFields,
	invalidReason string,
) bool {
	lease, ok := acquireEmission(level)
	if !ok {
		return false
	}
	defer lease.release()

	componentText, componentOK := componentLabel(component)
	messageText, messageOK := diagnosticMessageLabel(message)
	if !componentOK || !messageOK {
		componentText = componentLabels[ComponentLogger]
		messageText = "Safe diagnostic rejected"
		fields = SafeFields{}
		invalidReason = safeEnvelopeInvalid
	}

	skip, _ := getCallerSkip()
	event := getEvent(lease.logger, level)
	event.Str(Component, componentText)
	appendSafeFields(event, fields, invalidReason)
	event.CallerSkipFrame(skip).Msg(messageText)
	return true
}

func appendSafeFields(event *zerolog.Event, fields SafeFields, invalidReason string) {
	if invalidReason == "" && !fields.valid {
		invalidReason = safeFieldsInvalid
	}
	if invalidReason != "" {
		event.Str(safeFieldsStateKey, observationStateUnavailable)
		event.Str(safeFieldsReasonKey, invalidReason)
		return
	}

	for _, field := range fields.entries {
		label, _ := safeFieldSpec(field.key)
		switch field.kind {
		case safeFieldKindInt:
			event.Int(label, field.intValue)
		case safeFieldKindInt64:
			event.Int64(label, field.int64Value)
		case safeFieldKindBool:
			event.Bool(label, field.boolValue)
		case safeFieldKindEnum:
			event.Str(label, safeEnumLabels[field.enumValue])
		case safeFieldKindObservation:
			appendSafeObservation(event, field.prefix, field.observation)
		case safeFieldKindFloat64:
			event.Float64(label, field.float64Value)
		}
	}
	if fields.preview != nil {
		event.RawJSON(sensitivePreviewField, fields.preview.serialized)
	}
}

func appendSafeObservation(
	event *zerolog.Event,
	prefix ObservationFieldPrefix,
	observation Observation,
) {
	label, ok := observationPrefixLabel(prefix)
	if !ok {
		prefix = ObservationPrefixError
		label = observationPrefixLabels[prefix]
	}
	fields := ObservationFields(prefix, observation)
	event.Str(label+"_class", fields[label+"_class"].(string))
	event.Int64(label+"_bytes", fields[label+"_bytes"].(int64))
	event.Int64(label+"_runes", fields[label+"_runes"].(int64))
	event.Bool(label+"_utf8_valid", fields[label+"_utf8_valid"].(bool))
	event.Int64(label+"_count", fields[label+"_count"].(int64))
	event.Str(label+"_digest", fields[label+"_digest"].(string))
	event.Str(label+"_state", fields[label+"_state"].(string))
	event.Str(label+"_reason_code", fields[label+"_reason_code"].(string))
}

func safeFieldValid(field SafeField) bool {
	if !field.valid {
		return false
	}
	if field.kind == safeFieldKindObservation {
		_, ok := observationPrefixLabel(field.prefix)
		return ok && field.observation.valid &&
			field.observation.expectedPrefix == field.prefix &&
			validObservation(field.observation)
	}
	_, expectedKind := safeFieldSpec(field.key)
	if expectedKind != field.kind {
		return false
	}
	switch field.kind {
	case safeFieldKindInt:
		return field.intValue >= 0
	case safeFieldKindInt64:
		return field.int64Value >= 0
	case safeFieldKindBool:
		return true
	case safeFieldKindEnum:
		return safeEnumAllowed(field.key, field.enumValue)
	case safeFieldKindFloat64:
		return !math.IsNaN(field.float64Value) && !math.IsInf(field.float64Value, 0)
	default:
		return false
	}
}

func componentLabel(component ComponentID) (string, bool) {
	if component == 0 || int(component) >= len(componentLabels) {
		return "", false
	}
	return componentLabels[component], true
}

func diagnosticMessageLabel(message DiagnosticMessageID) (string, bool) {
	if message == 0 || int(message) >= len(diagnosticMessageLabels) {
		return "", false
	}
	return diagnosticMessageLabels[message], true
}

func validSafeEnum(value SafeEnumValue) bool {
	return value > 0 && int(value) < len(safeEnumLabels)
}

func safeEnumAllowed(key FieldKey, value SafeEnumValue) bool {
	if !validSafeEnum(value) {
		return false
	}
	switch key {
	case FieldState:
		switch value {
		case SafeEnumPending, SafeEnumStarted, SafeEnumCompleted, SafeEnumFailed,
			SafeEnumCanceled, SafeEnumUnavailable, SafeEnumReady,
			SafeEnumRunning, SafeEnumStopped:
			return true
		}
	case FieldAction:
		switch value {
		case SafeEnumAllowed, SafeEnumDenied, SafeEnumDirect, SafeEnumToolCalls,
			SafeEnumSync, SafeEnumAsync, SafeEnumSkipped:
			return true
		}
	case FieldOutcome:
		switch value {
		case SafeEnumCompleted, SafeEnumFailed, SafeEnumCanceled, SafeEnumTimeout,
			SafeEnumSkipped:
			return true
		}
	case FieldRole:
		switch value {
		case SafeEnumSystem, SafeEnumUser, SafeEnumAssistant, SafeEnumTool,
			SafeEnumDeveloper, SafeEnumUnknown:
			return true
		}
	case FieldReason:
		switch value {
		case SafeEnumCanceled, SafeEnumDenied, SafeEnumTimeout, SafeEnumSkipped,
			SafeEnumUnavailable:
			return true
		}
	case FieldSource:
		switch value {
		case SafeEnumInProcess, SafeEnumProcess, SafeEnumUnknown:
			return true
		}
	}
	return false
}

func safeFieldSortLabel(field SafeField) string {
	if field.kind == safeFieldKindObservation {
		label, _ := observationPrefixLabel(field.prefix)
		return label
	}
	label, _ := safeFieldSpec(field.key)
	return label
}

func safeFieldSpec(key FieldKey) (string, safeFieldKind) {
	switch key {
	case FieldIteration:
		return "iteration", safeFieldKindInt
	case FieldMaxIterations:
		return "max_iterations", safeFieldKindInt
	case FieldAttempt:
		return "attempt", safeFieldKindInt
	case FieldRetryCount:
		return "retry_count", safeFieldKindInt
	case FieldDepth:
		return "depth", safeFieldKindInt
	case FieldMaxDepth:
		return "max_depth", safeFieldKindInt
	case FieldCount:
		return "count", safeFieldKindInt
	case FieldMessageCount:
		return "message_count", safeFieldKindInt
	case FieldHistoryMessageCount:
		return "history_message_count", safeFieldKindInt
	case FieldMediaCount:
		return "media_count", safeFieldKindInt
	case FieldToolCount:
		return "tool_count", safeFieldKindInt
	case FieldToolCallCount:
		return "tool_call_count", safeFieldKindInt
	case FieldArgumentCount:
		return "argument_count", safeFieldKindInt
	case FieldResultCount:
		return "result_count", safeFieldKindInt
	case FieldModelCount:
		return "model_count", safeFieldKindInt
	case FieldPromptPartCount:
		return "prompt_part_count", safeFieldKindInt
	case FieldOverlayCount:
		return "overlay_count", safeFieldKindInt
	case FieldSystemMessageCount:
		return "system_message_count", safeFieldKindInt
	case FieldUserMessageCount:
		return "user_message_count", safeFieldKindInt
	case FieldAssistantMessageCount:
		return "assistant_message_count", safeFieldKindInt
	case FieldToolMessageCount:
		return "tool_message_count", safeFieldKindInt
	case FieldUnknownCount:
		return "unknown_count", safeFieldKindInt
	case FieldActiveCount:
		return "active_count", safeFieldKindInt
	case FieldSuccessCount:
		return "success_count", safeFieldKindInt
	case FieldFailureCount:
		return "failure_count", safeFieldKindInt
	case FieldDroppedCount:
		return "dropped_count", safeFieldKindInt
	case FieldQueueDepth:
		return "queue_depth", safeFieldKindInt
	case FieldIndex:
		return "index", safeFieldKindInt
	case FieldDurationMilliseconds:
		return "duration_ms", safeFieldKindInt64
	case FieldBytes:
		return "bytes", safeFieldKindInt64
	case FieldStaticBytes:
		return "static_bytes", safeFieldKindInt64
	case FieldDynamicBytes:
		return "dynamic_bytes", safeFieldKindInt64
	case FieldTotalBytes:
		return "total_bytes", safeFieldKindInt64
	case FieldContentBytes:
		return "content_bytes", safeFieldKindInt64
	case FieldArgumentsBytes:
		return "arguments_bytes", safeFieldKindInt64
	case FieldResultBytes:
		return "result_bytes", safeFieldKindInt64
	case FieldSchemaBytes:
		return "schema_bytes", safeFieldKindInt64
	case FieldInputBytes:
		return "input_bytes", safeFieldKindInt64
	case FieldOutputBytes:
		return "output_bytes", safeFieldKindInt64
	case FieldLimitBytes:
		return "limit_bytes", safeFieldKindInt64
	case FieldTimeoutMilliseconds:
		return "timeout_ms", safeFieldKindInt64
	case FieldAsync:
		return "async", safeFieldKindBool
	case FieldSuccess:
		return "success", safeFieldKindBool
	case FieldTruncated:
		return "truncated", safeFieldKindBool
	case FieldStreaming:
		return "streaming", safeFieldKindBool
	case FieldCached:
		return "cached", safeFieldKindBool
	case FieldSuppressed:
		return "suppressed", safeFieldKindBool
	case FieldAccepted:
		return "accepted", safeFieldKindBool
	case FieldEnabled:
		return "enabled", safeFieldKindBool
	case FieldRetryable:
		return "retryable", safeFieldKindBool
	case FieldDelivered:
		return "delivered", safeFieldKindBool
	case FieldHandled:
		return "handled", safeFieldKindBool
	case FieldChanged:
		return "changed", safeFieldKindBool
	case FieldAvailable:
		return "available", safeFieldKindBool
	case FieldState:
		return "state", safeFieldKindEnum
	case FieldAction:
		return "action", safeFieldKindEnum
	case FieldOutcome:
		return "outcome", safeFieldKindEnum
	case FieldRole:
		return "role", safeFieldKindEnum
	case FieldReason:
		return "reason", safeFieldKindEnum
	case FieldRequestedCount:
		return "requested_count", safeFieldKindInt
	case FieldPromotedCount:
		return "promoted_count", safeFieldKindInt
	case FieldCore:
		return "core", safeFieldKindBool
	case FieldSource:
		return "source", safeFieldKindEnum
	case FieldMaxTokens:
		return "max_tokens", safeFieldKindInt
	case FieldContextWindow:
		return "context_window", safeFieldKindInt
	case FieldPromptTokens:
		return "prompt_tokens", safeFieldKindInt
	case FieldCompletionTokens:
		return "completion_tokens", safeFieldKindInt
	case FieldTotalTokens:
		return "total_tokens", safeFieldKindInt
	case FieldCachedTokens:
		return "cached_tokens", safeFieldKindInt
	case FieldReasoningTokens:
		return "reasoning_tokens", safeFieldKindInt
	case FieldMaxRetries:
		return "max_retries", safeFieldKindInt
	case FieldChunkCount:
		return "chunk_count", safeFieldKindInt
	case FieldExpectedCount:
		return "expected_count", safeFieldKindInt
	case FieldFoundCount:
		return "found_count", safeFieldKindInt
	case FieldPendingCount:
		return "pending_count", safeFieldKindInt
	case FieldRemainingCount:
		return "remaining_count", safeFieldKindInt
	case FieldCompletedCount:
		return "completed_count", safeFieldKindInt
	case FieldSkippedCount:
		return "skipped_count", safeFieldKindInt
	case FieldAgentCount:
		return "agent_count", safeFieldKindInt
	case FieldServerCount:
		return "server_count", safeFieldKindInt
	case FieldSkillCount:
		return "skill_count", safeFieldKindInt
	case FieldAvailableCount:
		return "available_count", safeFieldKindInt
	case FieldNotificationCount:
		return "notification_count", safeFieldKindInt
	case FieldMatchedCount:
		return "matched_count", safeFieldKindInt
	case FieldInsertedCount:
		return "inserted_count", safeFieldKindInt
	case FieldBackoffMilliseconds:
		return "backoff_ms", safeFieldKindInt64
	case FieldGraceMilliseconds:
		return "grace_ms", safeFieldKindInt64
	case FieldChunkSpanMilliseconds:
		return "chunk_span_ms", safeFieldKindInt64
	case FieldTemperature:
		return "temperature", safeFieldKindFloat64
	case FieldScore:
		return "score", safeFieldKindFloat64
	case FieldThreshold:
		return "threshold", safeFieldKindFloat64
	case FieldCacheHitRatio:
		return "cache_hit_ratio", safeFieldKindFloat64
	case FieldHasReasoning:
		return "has_reasoning", safeFieldKindBool
	case FieldGracefulTerminal:
		return "graceful_terminal", safeFieldKindBool
	case FieldASREnabled:
		return "asr_enabled", safeFieldKindBool
	case FieldTTSEnabled:
		return "tts_enabled", safeFieldKindBool
	case FieldDebugEnabled:
		return "debug_enabled", safeFieldKindBool
	case FieldAllowEmpty:
		return "allow_empty", safeFieldKindBool
	case FieldLimitedMode:
		return "limited_mode", safeFieldKindBool
	case FieldCacheHit:
		return "cache_hit", safeFieldKindBool
	case FieldFallback:
		return "fallback", safeFieldKindBool
	case FieldHasSummary:
		return "has_summary", safeFieldKindBool
	case FieldCacheSensitive:
		return "cache_sensitive", safeFieldKindBool
	default:
		return "", 0
	}
}
