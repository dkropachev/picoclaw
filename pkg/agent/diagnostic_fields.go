package agent

import (
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Agent diagnostic helpers are deliberately field-specific and non-emitting.
// Keeping each domain/prefix pair closed prevents a caller from relabeling an
// arbitrary string as a more trusted identity at a log sink.
func agentDiagnosticAgentField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityAgent,
		logger.ObserveIdentity(logger.ObservationDomainIdentityAgent, value),
	)
}

func agentDiagnosticSessionField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentitySession,
		logger.ObserveIdentity(logger.ObservationDomainIdentitySession, value),
	)
}

func agentDiagnosticChannelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityChannel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChannel, value),
	)
}

func agentDiagnosticChatField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityChat,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChat, value),
	)
}

func agentDiagnosticSenderField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentitySender,
		logger.ObserveIdentity(logger.ObservationDomainIdentitySender, value),
	)
}

func agentDiagnosticMessageField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityMessage,
		logger.ObserveIdentity(logger.ObservationDomainIdentityMessage, value),
	)
}

func agentDiagnosticTurnField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityTurn,
		logger.ObserveIdentity(logger.ObservationDomainIdentityTurn, value),
	)
}

func agentDiagnosticParentTurnField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityParentTurn,
		logger.ObserveIdentity(logger.ObservationDomainIdentityParentTurn, value),
	)
}

func agentDiagnosticChildTurnField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityChildTurn,
		logger.ObserveIdentity(logger.ObservationDomainIdentityChildTurn, value),
	)
}

func agentDiagnosticToolField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityTool,
		logger.ObserveIdentity(logger.ObservationDomainIdentityTool, value),
	)
}

func agentDiagnosticToolCallField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityToolCall,
		logger.ObserveIdentity(logger.ObservationDomainIdentityToolCall, value),
	)
}

func agentDiagnosticModelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityModel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityModel, value),
	)
}

func agentDiagnosticProviderModelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityProviderModel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityProviderModel, value),
	)
}

func agentDiagnosticLightModelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityLightModel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityLightModel, value),
	)
}

func agentDiagnosticProviderField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityProvider,
		logger.ObserveIdentity(logger.ObservationDomainIdentityProvider, value),
	)
}

func agentDiagnosticAccountField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityAccount,
		logger.ObserveIdentity(logger.ObservationDomainIdentityAccount, value),
	)
}

func agentDiagnosticRequestField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRequest,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRequest, value),
	)
}

func agentDiagnosticTaskField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityTask,
		logger.ObserveIdentity(logger.ObservationDomainIdentityTask, value),
	)
}

func agentDiagnosticTopicField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityTopic,
		logger.ObserveIdentity(logger.ObservationDomainIdentityTopic, value),
	)
}

func agentDiagnosticSpaceField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentitySpace,
		logger.ObserveIdentity(logger.ObservationDomainIdentitySpace, value),
	)
}

func agentDiagnosticWorkspaceField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityWorkspace,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkspace, value),
	)
}

func agentDiagnosticWorkerField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityWorker,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorker, value),
	)
}

func agentDiagnosticWorkflowField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityWorkflow,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkflow, value),
	)
}

func agentDiagnosticSkillField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentitySkill,
		logger.ObserveIdentity(logger.ObservationDomainIdentitySkill, value),
	)
}

func agentDiagnosticRouteField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRoute,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRoute, value),
	)
}

func agentDiagnosticRouteAgentField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRouteAgent,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRouteAgent, value),
	)
}

func agentDiagnosticRouteChannelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRouteChannel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRouteChannel, value),
	)
}

func agentDiagnosticRouteSessionField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRouteSession,
		logger.ObserveIdentity(logger.ObservationDomainIdentityRouteSession, value),
	)
}

func agentDiagnosticTargetChannelField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityTargetChannel,
		logger.ObserveIdentity(logger.ObservationDomainIdentityTargetChannel, value),
	)
}

func agentDiagnosticContextManagerField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityContextManager,
		logger.ObserveIdentity(logger.ObservationDomainIdentityContextManager, value),
	)
}

func agentDiagnosticPromptPartField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityPromptPart,
		logger.ObserveIdentity(logger.ObservationDomainIdentityPromptPart, value),
	)
}

func agentDiagnosticPromptSourceField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityPromptSource,
		logger.ObserveIdentity(logger.ObservationDomainIdentityPromptSource, value),
	)
}

func agentDiagnosticPromptLayerField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityPromptLayer,
		logger.ObserveIdentity(logger.ObservationDomainIdentityPromptLayer, value),
	)
}

func agentDiagnosticPromptSlotField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityPromptSlot,
		logger.ObserveIdentity(logger.ObservationDomainIdentityPromptSlot, value),
	)
}

func agentDiagnosticReasonField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityReason,
		logger.ObserveIdentity(logger.ObservationDomainIdentityReason, value),
	)
}

func agentDiagnosticScopeField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityScope,
		logger.ObserveIdentity(logger.ObservationDomainIdentityScope, value),
	)
}

func agentDiagnosticToolSurfaceField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityToolSurface,
		logger.ObserveIdentity(logger.ObservationDomainIdentityToolSurface, value),
	)
}

func agentDiagnosticMCPServerField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityMCPServer,
		logger.ObserveIdentity(logger.ObservationDomainIdentityMCPServer, value),
	)
}

func agentDiagnosticMediaRefField(value string) logger.SafeField {
	return logger.SafeObservation(logger.ObservationPrefixURL, logger.ObserveURL(value))
}

func agentDiagnosticRegexField(value string) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixRegex,
		logger.ObserveText(logger.ObservationDomainRegex, value),
	)
}

func agentDiagnosticRoleEnum(value string) logger.SafeEnumValue {
	switch value {
	case "system":
		return logger.SafeEnumSystem
	case "user":
		return logger.SafeEnumUser
	case "assistant":
		return logger.SafeEnumAssistant
	case "tool":
		return logger.SafeEnumTool
	case "developer":
		return logger.SafeEnumDeveloper
	default:
		return logger.SafeEnumUnknown
	}
}

func agentDiagnosticRuntimeEventKindField(value runtimeevents.Kind) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixIdentityRuntimeEventKind,
		logger.ObserveIdentity(
			logger.ObservationDomainIdentityRuntimeEventKind,
			string(value),
		),
	)
}

func agentDiagnosticPathField(value string) logger.SafeField {
	return logger.SafeObservation(logger.ObservationPrefixPath, logger.ObservePath(value))
}

func agentDiagnosticErrorField(class logger.ErrorClass, err error) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixError,
		logger.ObserveErrorType(class, err),
	)
}

func agentDiagnosticPanicField(recovered any) logger.SafeField {
	return logger.SafeObservation(
		logger.ObservationPrefixPanic,
		logger.ObservePanic(recovered),
	)
}
