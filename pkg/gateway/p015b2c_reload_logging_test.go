package gateway

import "testing"

func TestP015B2CReloadLoggingASTManifest(t *testing.T) {
	p015B2CValidateDescriptorGroup(t, p015B2CDescriptorGroup{
		name:         "reload",
		descriptors:  p015B2CReloadLoggingDescriptors(),
		loggerTotal:  28,
		consoleTotal: 8,
		levelCounts: map[string]int{
			"DebugSafeCF": 1,
			"InfoSafeCF":  11,
			"WarnSafeCF":  6,
			"ErrorSafeCF": 10,
		},
		componentCount: map[string]int{
			"ComponentConfig":   16,
			"ComponentLogger":   1,
			"ComponentProvider": 2,
			"ComponentGateway":  5,
			"ComponentAgent":    1,
			"ComponentDevice":   1,
			"ComponentVoice":    2,
		},
	})
}

func p015B2CReloadLoggingDescriptors() []p015B2CSinkDescriptor {
	descriptors := []p015B2CSinkDescriptor{
		p015B2CReloadLogger(
			"G011", "Run", "WarnSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigReloadSkippedAnotherReloadIsInProgress",
		),
		p015B2CReloadLogger(
			"G012", "Run", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigReloadFailed",
		),
		p015B2CReloadLogger(
			"G013", "Run", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigManualReloadTriggeredViaReloadEndpoint",
		),
		p015B2CReloadLogger(
			"G014", "Run", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigErrorLoadingConfigForManualReload",
		),
		p015B2CReloadLogger(
			"G015", "Run", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigValidationFailed",
		),
		p015B2CReloadLogger(
			"G016", "Run", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigManualReloadFailed",
		),
		p015B2CReloadLogger(
			"G018", "Run", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigManualReloadCompletedSuccessfully",
		),
		p015B2CReloadLogger(
			"G024", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigFileChangedReloading",
		),
		p015B2CReloadLogger(
			"G025", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigProviderConfigurationAndServicesReloadedSuccessfully",
		),
		p015B2CReloadLogger(
			"G026", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentLogger",
			"DiagnosticMessageLoggerLogLevelChangingFromCurrent",
		),
		p015B2CReloadLogger(
			"G027", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentProvider",
			"DiagnosticMessageProviderNewModelSelectedRecreatingProvider",
		),
		p015B2CReloadLogger(
			"G028", "handleConfigReloadWithServiceOps", "ErrorSafeCF", "ComponentProvider",
			"DiagnosticMessageProviderErrorCreatingNewProvider",
		),
		p015B2CReloadLogger(
			"G029", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayStoppingAllServices",
		),
		p015B2CReloadLogger(
			"G030", "handleConfigReloadWithServiceOps", "ErrorSafeCF", "ComponentAgent",
			"DiagnosticMessageAgentErrorReloadingAgentLoop",
		),
		p015B2CReloadLogger(
			"G031", "handleConfigReloadWithServiceOps", "WarnSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayAttemptingToRestartServicesWithOldProviderAndConfig",
		),
		p015B2CReloadLogger(
			"G032", "handleConfigReloadWithServiceOps", "ErrorSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayFailedToRestartServices",
		),
		p015B2CReloadLogger(
			"G033", "handleConfigReloadWithServiceOps", "InfoSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayPreflightingAndRestartingAllServicesWithNewConfiguration",
		),
		p015B2CReloadLogger(
			"G034", "handleConfigReloadWithServiceOps", "ErrorSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayErrorRestartingServices",
		),
		p015B2CReloadLogger(
			"G036", "restartServices", "WarnSafeCF", "ComponentDevice",
			"DiagnosticMessageDeviceFailedToRestartDeviceService",
		),
		p015B2CReloadLogger(
			"G037", "restartServices", "InfoSafeCF", "ComponentVoice",
			"DiagnosticMessageVoiceTranscriptionReEnabledAgentLevel",
		),
		p015B2CReloadLogger(
			"G038", "restartServices", "InfoSafeCF", "ComponentVoice",
			"DiagnosticMessageVoiceTranscriptionDisabled",
		),
		p015B2CReloadLogger(
			"G041", "setupConfigWatcherPolling", "DebugSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigFileChangeDetected",
		),
		p015B2CReloadLogger(
			"G042", "setupConfigWatcherPolling", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigErrorLoadingNewConfig",
		),
		p015B2CReloadLoggerOccurrence(
			"G043", "setupConfigWatcherPolling", 1, "WarnSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigUsingPreviousValidConfig",
		),
		p015B2CReloadLogger(
			"G044", "setupConfigWatcherPolling", "ErrorSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigNewConfigValidationFailed",
		),
		p015B2CReloadLoggerOccurrence(
			"G045", "setupConfigWatcherPolling", 2, "WarnSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigUsingPreviousValidConfig",
		),
		p015B2CReloadLogger(
			"G046", "setupConfigWatcherPolling", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigFileValidatedAndLoaded",
		),
		p015B2CReloadLogger(
			"G047", "setupConfigWatcherPolling", "WarnSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigPreviousReloadStillInProgressSkipping",
		),
		p015B2CReloadConsole("C008", "gatewayConsoleC008HeartbeatRestarted"),
		p015B2CReloadConsole("C009", "gatewayConsoleC009EventInboxReopened"),
		p015B2CReloadConsole("C010", "gatewayConsoleC010CronRestarted"),
		p015B2CReloadConsole("C011", "gatewayConsoleC011ChannelsRestarted"),
		p015B2CReloadConsole("C012", "gatewayConsoleC012RestartedChannelsEnabled"),
		p015B2CReloadConsole("C013", "gatewayConsoleC013NoRestartedChannelsEnabled"),
		p015B2CReloadConsole("C014", "gatewayConsoleC014DeviceServiceRestarted"),
		p015B2CReloadConsole("C015", "gatewayConsoleC015EventWorkersRestarted"),
	}
	return append([]p015B2CSinkDescriptor(nil), descriptors...)
}

func p015B2CReloadLogger(
	id, owner, level, component, message string,
) p015B2CSinkDescriptor {
	return p015B2CReloadLoggerOccurrence(id, owner, 1, level, component, message)
}

func p015B2CReloadLoggerOccurrence(
	id, owner string,
	occurrence int,
	level, component, message string,
) p015B2CSinkDescriptor {
	return p015B2CSinkDescriptor{
		ID: id, File: "gateway.go", Owner: owner, Kind: p015B2CLoggerSink,
		Occurrence: occurrence, Level: level, Component: component, Message: message,
	}
}

func p015B2CReloadConsole(id, site string) p015B2CSinkDescriptor {
	return p015B2CSinkDescriptor{
		ID: id, File: "gateway.go", Owner: "restartServices", Kind: p015B2CConsoleSink,
		ConsoleSite: site,
	}
}
