package logger

import "testing"

func TestP015B2CGatewayDiagnosticMessageWireManifest(t *testing.T) {
	expected := [...]struct {
		id    DiagnosticMessageID
		wire  int
		label string
	}{
		{DiagnosticMessageEventingPRWorkspaceLocalCIIsUnavailable, 275, "PR workspace local CI is unavailable"},
		{
			DiagnosticMessageEventingPRWorkspaceImplementationIsUnavailable,
			276,
			"PR workspace implementation is unavailable",
		},
		{DiagnosticMessageEventingEventWorkflowWorkerIterationFailed, 277, "Event workflow worker iteration failed"},
		{DiagnosticMessageEventingEventRetentionMaintenanceFailed, 278, "Event retention maintenance failed"},
		{DiagnosticMessageEventingPrunedExpiredDurableEvents, 279, "Pruned expired durable events"},
		{DiagnosticMessageEventingGitHubNotificationPollingFailed, 280, "GitHub notification polling failed"},
		{DiagnosticMessageEventingStoredGitHubNotifications, 281, "Stored GitHub notifications"},
		{DiagnosticMessageConfigHotReloadEnabled, 282, "Config hot reload enabled"},
		{DiagnosticMessageGatewayShuttingDown, 283, "Shutting down..."},
		{
			DiagnosticMessageConfigReloadSkippedAnotherReloadIsInProgress,
			284,
			"Config reload skipped: another reload is in progress",
		},
		{DiagnosticMessageConfigReloadFailed, 285, "Config reload failed"},
		{
			DiagnosticMessageConfigManualReloadTriggeredViaReloadEndpoint,
			286,
			"Manual reload triggered via /reload endpoint",
		},
		{DiagnosticMessageConfigErrorLoadingConfigForManualReload, 287, "Error loading config for manual reload"},
		{DiagnosticMessageConfigValidationFailed, 288, "Config validation failed"},
		{DiagnosticMessageConfigManualReloadFailed, 289, "Manual reload failed"},
		{DiagnosticMessageLoggerErrorEnablingFileLogging, 290, "error enabling file logging"},
		{DiagnosticMessageConfigManualReloadCompletedSuccessfully, 291, "Manual reload completed successfully"},
		{DiagnosticMessageLoggerLogLevelSet, 292, "Log level set"},
		{DiagnosticMessageGatewayWritePIDFileFailed, 293, "write pid file failed"},
		{DiagnosticMessageAgentInitialized, 294, "Agent initialized"},
		{DiagnosticMessageGatewayStartupFailed, 295, "Gateway startup failed"},
		{
			DiagnosticMessageGatewayStartedWithoutAConfiguredModelAlias,
			296,
			"Gateway started without a configured model alias",
		},
		{DiagnosticMessageConfigFileChangedReloading, 297, "🔄 Config file changed, reloading..."},
		{
			DiagnosticMessageConfigProviderConfigurationAndServicesReloadedSuccessfully,
			298,
			"  ✓ Provider, configuration, and services reloaded successfully (thread-safe)",
		},
		{DiagnosticMessageLoggerLogLevelChangingFromCurrent, 299, "Log level changing from current"},
		{
			DiagnosticMessageProviderNewModelSelectedRecreatingProvider,
			300,
			" New model selected, recreating provider...",
		},
		{DiagnosticMessageProviderErrorCreatingNewProvider, 301, "  ⚠ Error creating new provider"},
		{DiagnosticMessageGatewayStoppingAllServices, 302, "  Stopping all services..."},
		{DiagnosticMessageAgentErrorReloadingAgentLoop, 303, "  ⚠ Error reloading agent loop"},
		{
			DiagnosticMessageGatewayAttemptingToRestartServicesWithOldProviderAndConfig,
			304,
			"  Attempting to restart services with old provider and config...",
		},
		{DiagnosticMessageGatewayFailedToRestartServices, 305, "  ⚠ Failed to restart services"},
		{
			DiagnosticMessageGatewayPreflightingAndRestartingAllServicesWithNewConfiguration,
			306,
			"  Preflighting and restarting all services with new configuration...",
		},
		{DiagnosticMessageGatewayErrorRestartingServices, 307, "  ⚠ Error restarting services"},
		{DiagnosticMessageVoiceChannelVoiceCapabilities, 308, "Channel voice capabilities"},
		{DiagnosticMessageDeviceFailedToRestartDeviceService, 309, "Failed to restart device service"},
		{DiagnosticMessageVoiceTranscriptionReEnabledAgentLevel, 310, "Transcription re-enabled (agent-level)"},
		{DiagnosticMessageVoiceTranscriptionDisabled, 311, "Transcription disabled"},
		{DiagnosticMessageVoiceTranscriptionEnabledAgentLevel, 312, "Transcription enabled (agent-level)"},
		{DiagnosticMessageDeviceErrorStartingDeviceService, 313, "Error starting device service"},
		{DiagnosticMessageConfigFileChangeDetected, 314, "🔍 Config file change detected"},
		{DiagnosticMessageConfigErrorLoadingNewConfig, 315, "⚠ Error loading new config"},
		{DiagnosticMessageConfigUsingPreviousValidConfig, 316, "  Using previous valid config"},
		{DiagnosticMessageConfigNewConfigValidationFailed, 317, "  ⚠ New config validation failed"},
		{DiagnosticMessageConfigFileValidatedAndLoaded, 318, "✓ Config file validated and loaded"},
		{
			DiagnosticMessageConfigPreviousReloadStillInProgressSkipping,
			319,
			"⚠ Previous config reload still in progress, skipping",
		},
		{DiagnosticMessageGatewayFailedToStopRuntimeProducersCleanly, 320, "Failed to stop runtime producers cleanly"},
		{DiagnosticMessageGatewayAgentLoopDidNotStopCleanly, 321, "Agent loop did not stop cleanly"},
		{DiagnosticMessageGatewayAgentRuntimeDidNotDrainCleanly, 322, "Agent runtime did not drain cleanly"},
		{
			DiagnosticMessageGatewayChannelEventAdmissionDidNotCloseCleanly,
			323,
			"Channel event admission did not close cleanly",
		},
		{
			DiagnosticMessageGatewayFailedToStopRuntimeDependenciesCleanly,
			324,
			"Failed to stop runtime dependencies cleanly",
		},
		{DiagnosticMessageGatewayStopped, 325, "✓ Gateway stopped"},
		{DiagnosticMessagePRWorkspaceRepairFailed, 326, "PR workspace repair failed"},
	}

	if len(expected) != 52 {
		t.Fatalf("gateway message manifest has %d entries; want 52", len(expected))
	}
	for offset, item := range expected {
		wire := 275 + offset
		if int(item.id) != item.wire || item.wire != wire {
			t.Fatalf("message offset %d = wire %d/%d; want %d", offset, item.id, item.wire, wire)
		}
		label, ok := diagnosticMessageLabel(DiagnosticMessageID(wire))
		if !ok || label != item.label {
			t.Fatalf("message wire %d = %q, %v; want %q", wire, label, ok, item.label)
		}
	}
}

func TestP015B2CLogLevelFieldAndEnumWireManifest(t *testing.T) {
	if FieldLogLevel != 104 {
		t.Fatalf("FieldLogLevel = %d; want 104", FieldLogLevel)
	}
	if label, kind := safeFieldSpec(FieldLogLevel); label != "log_level" || kind != safeFieldKindEnum {
		t.Fatalf("FieldLogLevel spec = %q/%d; want log_level/%d", label, kind, safeFieldKindEnum)
	}
	expected := [...]struct {
		value SafeEnumValue
		wire  int
		label string
	}{
		{SafeEnumDebug, 26, "debug"},
		{SafeEnumInfo, 27, "info"},
		{SafeEnumWarn, 28, "warn"},
		{SafeEnumError, 29, "error"},
		{SafeEnumFatal, 30, "fatal"},
	}
	for _, item := range expected {
		if int(item.value) != item.wire || safeEnumLabels[item.wire] != item.label {
			t.Fatalf("log enum = %d/%q; want %d/%q", item.value, safeEnumLabels[item.value], item.wire, item.label)
		}
		field := SafeEnum(FieldLogLevel, item.value)
		if !field.valid || !safeFieldValid(field) {
			t.Fatalf("log enum %d rejected matching constructor", item.value)
		}
	}
	if field := SafeEnum(FieldLogLevel, SafeEnumUnknown); !field.valid || !safeFieldValid(field) {
		t.Fatal("unknown log level rejected")
	}
	for _, value := range []SafeEnumValue{SafeEnumDeveloper, SafeEnumPending, SafeEnumUser} {
		if SafeEnum(FieldLogLevel, value).valid {
			t.Fatalf("unrelated enum %d accepted for log level", value)
		}
	}
	if SafeEnum(FieldState, SafeEnumDebug).valid {
		t.Fatal("log-level enum crossed into state family")
	}
}

func TestP015B2CRolePathObservationWireAndConstructors(t *testing.T) {
	if ObservationDomainConfigPath != 69 || ObservationDomainHomePath != 70 ||
		ObservationPrefixConfigPath != 76 || ObservationPrefixHomePath != 77 {
		t.Fatalf(
			"role path wires moved: domains=%d/%d prefixes=%d/%d",
			ObservationDomainConfigPath,
			ObservationDomainHomePath,
			ObservationPrefixConfigPath,
			ObservationPrefixHomePath,
		)
	}
	if observationDomainLabels[ObservationDomainConfigPath] != "config_path" ||
		observationDomainLabels[ObservationDomainHomePath] != "home_path" ||
		observationPrefixLabels[ObservationPrefixConfigPath] != "config_path" ||
		observationPrefixLabels[ObservationPrefixHomePath] != "home_path" {
		t.Fatal("role path labels moved")
	}
	for _, item := range []struct {
		domain ObservationDomain
		prefix ObservationFieldPrefix
	}{
		{ObservationDomainConfigPath, ObservationPrefixConfigPath},
		{ObservationDomainHomePath, ObservationPrefixHomePath},
	} {
		prefix, ok := prefixForDomain(item.domain)
		if !ok || prefix != item.prefix {
			t.Fatalf("domain %d maps to prefix %d, %v; want %d", item.domain, prefix, ok, item.prefix)
		}
	}

	const value = "/tmp/p015b2c-role-path"
	configPath := ObserveConfigPath(value)
	homePath := ObserveHomePath(value)
	genericPath := ObservePath(value)
	for name, observation := range map[string]Observation{
		"config":  configPath,
		"home":    homePath,
		"generic": genericPath,
	} {
		if observation.State != observationStateComplete || observation.Class != "absolute" {
			t.Fatalf("%s path observation = %#v", name, observation)
		}
	}
	if configPath.Digest == homePath.Digest || configPath.Digest == genericPath.Digest ||
		homePath.Digest == genericPath.Digest {
		t.Fatal("role path digest domains are not distinct")
	}
	fields := NewSafeFields(
		SafeObservation(ObservationPrefixConfigPath, configPath),
		SafeObservation(ObservationPrefixHomePath, homePath),
	)
	if !fields.valid {
		t.Fatal("config and home path observations could not coexist")
	}
	if SafeObservation(ObservationPrefixHomePath, configPath).valid ||
		SafeObservation(ObservationPrefixConfigPath, homePath).valid {
		t.Fatal("role path constructors accepted a crossed prefix")
	}
}
