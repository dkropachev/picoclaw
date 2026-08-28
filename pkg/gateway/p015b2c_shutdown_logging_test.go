package gateway

import "testing"

func TestP015B2CShutdownLoggingASTManifest(t *testing.T) {
	p015B2CValidateDescriptorGroup(t, p015B2CDescriptorGroup{
		name:         "shutdown",
		descriptors:  p015B2CShutdownLoggingDescriptors(),
		loggerTotal:  7,
		consoleTotal: 0,
		levelCounts: map[string]int{
			"InfoSafeCF":  2,
			"ErrorSafeCF": 5,
		},
		componentCount: map[string]int{
			"ComponentGateway": 7,
		},
	})
}

func p015B2CShutdownLoggingDescriptors() []p015B2CSinkDescriptor {
	descriptors := []p015B2CSinkDescriptor{
		p015B2CShutdownLogger(
			"G010", "Run", "InfoSafeCF",
			"DiagnosticMessageGatewayShuttingDown",
		),
		p015B2CShutdownLogger(
			"G048", "shutdownGateway", "ErrorSafeCF",
			"DiagnosticMessageGatewayFailedToStopRuntimeProducersCleanly",
		),
		p015B2CShutdownLogger(
			"G049", "shutdownGateway", "ErrorSafeCF",
			"DiagnosticMessageGatewayAgentLoopDidNotStopCleanly",
		),
		p015B2CShutdownLogger(
			"G050", "shutdownGateway", "ErrorSafeCF",
			"DiagnosticMessageGatewayAgentRuntimeDidNotDrainCleanly",
		),
		p015B2CShutdownLogger(
			"G051", "shutdownGateway", "ErrorSafeCF",
			"DiagnosticMessageGatewayChannelEventAdmissionDidNotCloseCleanly",
		),
		p015B2CShutdownLogger(
			"G052", "shutdownGateway", "ErrorSafeCF",
			"DiagnosticMessageGatewayFailedToStopRuntimeDependenciesCleanly",
		),
		p015B2CShutdownLogger(
			"G053", "shutdownGateway", "InfoSafeCF",
			"DiagnosticMessageGatewayStopped",
		),
	}
	return append([]p015B2CSinkDescriptor(nil), descriptors...)
}

func p015B2CShutdownLogger(
	id, owner, level, message string,
) p015B2CSinkDescriptor {
	return p015B2CSinkDescriptor{
		ID: id, File: "gateway.go", Owner: owner, Kind: p015B2CLoggerSink,
		Level: level, Component: "ComponentGateway", Message: message,
	}
}
