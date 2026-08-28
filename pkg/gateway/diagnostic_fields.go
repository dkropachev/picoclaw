package gateway

import (
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Gateway diagnostic helpers are field-specific and non-emitting. Each
// dynamic value is projected through its reviewed domain before a safe sink.
func gatewayDiagnosticErrorField(class logger.ErrorClass, err error) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixError,
		logger.ObserveErrorType(class, err),
	)
}

func gatewayDiagnosticWorkerField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityWorker,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorker, value),
	)
}

func gatewayDiagnosticChannelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityChannel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChannel, value),
	)
}

func gatewayDiagnosticModelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityModel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityModel, value),
	)
}

func gatewayDiagnosticProviderField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityProvider,
		logger.ObserveIdentity(logger.ObservationDomainIdentityProvider, value),
	)
}

func gatewayDiagnosticWorkspaceField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityWorkspace,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkspace, value),
	)
}

func gatewayDiagnosticConfigPathField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixConfigPath,
		logger.ObserveConfigPath(value),
	)
}

func gatewayDiagnosticHomePathField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixHomePath,
		logger.ObserveHomePath(value),
	)
}

func gatewayDiagnosticLogLevelField(value string) logger.SafeField {
	level, ok := logger.ParseLevel(value)
	safeLevel := logger.SafeEnumUnknown
	if ok {
		switch level {
		case logger.DEBUG:
			safeLevel = logger.SafeEnumDebug
		case logger.INFO:
			safeLevel = logger.SafeEnumInfo
		case logger.WARN:
			safeLevel = logger.SafeEnumWarn
		case logger.ERROR:
			safeLevel = logger.SafeEnumError
		case logger.FATAL:
			safeLevel = logger.SafeEnumFatal
		}
	}
	return logger.SafeEnum(logger.FieldLogLevel, safeLevel)
}

// gatewayConfiguredVoiceProvider resolves the normalized provider from the
// already validated voice configuration. It deliberately never calls methods
// on the selected transcriber interface merely to construct diagnostics.
func gatewayConfiguredVoiceProvider(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	model, err := cfg.ResolveVoiceASRModelConfig()
	if err != nil || model == nil {
		return ""
	}
	return model.Provider
}
