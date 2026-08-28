package gateway

import "testing"

func TestP015B2CStartupLoggingASTManifest(t *testing.T) {
	p015B2CValidateDescriptorGroup(t, p015B2CDescriptorGroup{
		name:         "startup/root",
		descriptors:  p015B2CStartupLoggingDescriptors(),
		loggerTotal:  10,
		consoleTotal: 15,
		levelCounts: map[string]int{
			"InfoSafeCF":  5,
			"WarnSafeCF":  2,
			"ErrorSafeCF": 2,
			"FatalSafeCF": 1,
		},
		componentCount: map[string]int{
			"ComponentConfig":  1,
			"ComponentGateway": 3,
			"ComponentLogger":  2,
			"ComponentAgent":   1,
			"ComponentVoice":   2,
			"ComponentDevice":  1,
		},
	})
}

// p015B2CStartupLoggingDescriptors returns a detached startup/root slice so
// later partition tests can combine all G/C groups without sharing mutation.
func p015B2CStartupLoggingDescriptors() []p015B2CSinkDescriptor {
	descriptors := []p015B2CSinkDescriptor{
		p015B2CStartupLogger(
			"G009", "Run", "InfoSafeCF", "ComponentConfig",
			"DiagnosticMessageConfigHotReloadEnabled",
		),
		p015B2CStartupLogger(
			"G017", "Run", "FatalSafeCF", "ComponentLogger",
			"DiagnosticMessageLoggerErrorEnablingFileLogging",
		),
		p015B2CStartupLogger(
			"G019", "Run", "InfoSafeCF", "ComponentLogger",
			"DiagnosticMessageLoggerLogLevelSet",
		),
		p015B2CStartupLogger(
			"G020", "Run", "WarnSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayWritePIDFileFailed",
		),
		p015B2CStartupLogger(
			"G021", "Run", "InfoSafeCF", "ComponentAgent",
			"DiagnosticMessageAgentInitialized",
		),
		p015B2CStartupLogger(
			"G022", "Run", "ErrorSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayStartupFailed",
		),
		p015B2CStartupLogger(
			"G023", "createStartupProvider", "WarnSafeCF", "ComponentGateway",
			"DiagnosticMessageGatewayStartedWithoutAConfiguredModelAlias",
		),
		p015B2CStartupLogger(
			"G035", "logChannelVoiceCapabilities", "InfoSafeCF", "ComponentVoice",
			"DiagnosticMessageVoiceChannelVoiceCapabilities",
		),
		p015B2CStartupLogger(
			"G039", "setupAndStartServices", "InfoSafeCF", "ComponentVoice",
			"DiagnosticMessageVoiceTranscriptionEnabledAgentLevel",
		),
		p015B2CStartupLogger(
			"G040", "setupAndStartServices", "ErrorSafeCF", "ComponentDevice",
			"DiagnosticMessageDeviceErrorStartingDeviceService",
		),
		p015B2CStartupConsole("C001", "Run", "gatewayConsoleC001GatewayStarted"),
		p015B2CStartupConsole("C002", "Run", "gatewayConsoleC002StopHint"),
		p015B2CStartupConsole("C003", "Run", "gatewayConsoleC003DebugEnabled"),
		p015B2CStartupConsole("C004", "Run", "gatewayConsoleC004AgentStatus"),
		p015B2CStartupConsole("C005", "Run", "gatewayConsoleC005ToolsLoaded"),
		p015B2CStartupConsole("C006", "Run", "gatewayConsoleC006SkillsAvailable"),
		p015B2CStartupConsole(
			"C007", "createStartupProvider", "gatewayConsoleC007NoModelConfigured",
		),
		p015B2CStartupConsole("C016", "setupAndStartServices", "gatewayConsoleC016CronStarted"),
		p015B2CStartupConsole("C017", "setupAndStartServices", "gatewayConsoleC017ChannelsEnabled"),
		p015B2CStartupConsole("C018", "setupAndStartServices", "gatewayConsoleC018NoChannelsEnabled"),
		p015B2CStartupConsole(
			"C019", "setupAndStartServices", "gatewayConsoleC019HealthEndpointsAvailable",
		),
		p015B2CStartupConsole("C020", "setupAndStartServices", "gatewayConsoleC020DeviceServiceStarted"),
		p015B2CStartupConsole("C021", "setupAndStartServices", "gatewayConsoleC021EventWorkersStarted"),
		p015B2CStartupConsole("C022", "setupAndStartServices", "gatewayConsoleC022EventInboxOpened"),
		p015B2CStartupConsole("C023", "setupAndStartServices", "gatewayConsoleC023HeartbeatStarted"),
	}
	return append([]p015B2CSinkDescriptor(nil), descriptors...)
}

func p015B2CStartupLogger(
	id, owner, level, component, message string,
) p015B2CSinkDescriptor {
	return p015B2CSinkDescriptor{
		ID: id, File: "gateway.go", Owner: owner, Kind: p015B2CLoggerSink,
		Level: level, Component: component, Message: message,
	}
}

func p015B2CStartupConsole(id, owner, site string) p015B2CSinkDescriptor {
	return p015B2CSinkDescriptor{
		ID: id, File: "gateway.go", Owner: owner, Kind: p015B2CConsoleSink,
		ConsoleSite: site,
	}
}
