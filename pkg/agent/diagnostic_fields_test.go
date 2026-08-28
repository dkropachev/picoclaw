package agent

import (
	"sync/atomic"
	"testing"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
)

type agentDiagnosticHelperHostileError struct {
	calls *atomic.Int64
}

func (value *agentDiagnosticHelperHostileError) Error() string {
	value.calls.Add(1)
	return "P015B2_HOSTILE_ERROR"
}

func (value *agentDiagnosticHelperHostileError) String() string {
	value.calls.Add(1)
	return "P015B2_HOSTILE_STRING"
}

func TestAgentDiagnosticIdentityHelpersAreSealedAndDistinct(t *testing.T) {
	const canary = "P015B2_IDENTITY_CANARY_6ea123"
	type helperContract struct {
		name   string
		helper func(string) logger.SafeField
		prefix logger.ObservationFieldPrefix
		domain logger.ObservationDomain
	}
	helpers := []helperContract{
		{
			"agent",
			agentDiagnosticAgentField,
			logger.ObservationPrefixIdentityAgent,
			logger.ObservationDomainIdentityAgent,
		},
		{
			"session",
			agentDiagnosticSessionField,
			logger.ObservationPrefixIdentitySession,
			logger.ObservationDomainIdentitySession,
		},
		{
			"channel",
			agentDiagnosticChannelField,
			logger.ObservationPrefixIdentityChannel,
			logger.ObservationDomainIdentityChannel,
		},
		{"chat", agentDiagnosticChatField, logger.ObservationPrefixIdentityChat, logger.ObservationDomainIdentityChat},
		{
			"sender",
			agentDiagnosticSenderField,
			logger.ObservationPrefixIdentitySender,
			logger.ObservationDomainIdentitySender,
		},
		{
			"message",
			agentDiagnosticMessageField,
			logger.ObservationPrefixIdentityMessage,
			logger.ObservationDomainIdentityMessage,
		},
		{"turn", agentDiagnosticTurnField, logger.ObservationPrefixIdentityTurn, logger.ObservationDomainIdentityTurn},
		{
			"parent_turn",
			agentDiagnosticParentTurnField,
			logger.ObservationPrefixIdentityParentTurn,
			logger.ObservationDomainIdentityParentTurn,
		},
		{
			"child_turn",
			agentDiagnosticChildTurnField,
			logger.ObservationPrefixIdentityChildTurn,
			logger.ObservationDomainIdentityChildTurn,
		},
		{"tool", agentDiagnosticToolField, logger.ObservationPrefixIdentityTool, logger.ObservationDomainIdentityTool},
		{
			"tool_call",
			agentDiagnosticToolCallField,
			logger.ObservationPrefixIdentityToolCall,
			logger.ObservationDomainIdentityToolCall,
		},
		{
			"model",
			agentDiagnosticModelField,
			logger.ObservationPrefixIdentityModel,
			logger.ObservationDomainIdentityModel,
		},
		{
			"provider_model",
			agentDiagnosticProviderModelField,
			logger.ObservationPrefixIdentityProviderModel,
			logger.ObservationDomainIdentityProviderModel,
		},
		{
			"light_model",
			agentDiagnosticLightModelField,
			logger.ObservationPrefixIdentityLightModel,
			logger.ObservationDomainIdentityLightModel,
		},
		{
			"provider",
			agentDiagnosticProviderField,
			logger.ObservationPrefixIdentityProvider,
			logger.ObservationDomainIdentityProvider,
		},
		{
			"account",
			agentDiagnosticAccountField,
			logger.ObservationPrefixIdentityAccount,
			logger.ObservationDomainIdentityAccount,
		},
		{
			"request",
			agentDiagnosticRequestField,
			logger.ObservationPrefixIdentityRequest,
			logger.ObservationDomainIdentityRequest,
		},
		{"task", agentDiagnosticTaskField, logger.ObservationPrefixIdentityTask, logger.ObservationDomainIdentityTask},
		{
			"topic",
			agentDiagnosticTopicField,
			logger.ObservationPrefixIdentityTopic,
			logger.ObservationDomainIdentityTopic,
		},
		{
			"space",
			agentDiagnosticSpaceField,
			logger.ObservationPrefixIdentitySpace,
			logger.ObservationDomainIdentitySpace,
		},
		{
			"workspace",
			agentDiagnosticWorkspaceField,
			logger.ObservationPrefixIdentityWorkspace,
			logger.ObservationDomainIdentityWorkspace,
		},
		{
			"worker",
			agentDiagnosticWorkerField,
			logger.ObservationPrefixIdentityWorker,
			logger.ObservationDomainIdentityWorker,
		},
		{
			"workflow",
			agentDiagnosticWorkflowField,
			logger.ObservationPrefixIdentityWorkflow,
			logger.ObservationDomainIdentityWorkflow,
		},
		{
			"skill",
			agentDiagnosticSkillField,
			logger.ObservationPrefixIdentitySkill,
			logger.ObservationDomainIdentitySkill,
		},
		{
			"route",
			agentDiagnosticRouteField,
			logger.ObservationPrefixIdentityRoute,
			logger.ObservationDomainIdentityRoute,
		},
		{
			"route_agent",
			agentDiagnosticRouteAgentField,
			logger.ObservationPrefixIdentityRouteAgent,
			logger.ObservationDomainIdentityRouteAgent,
		},
		{
			"route_channel",
			agentDiagnosticRouteChannelField,
			logger.ObservationPrefixIdentityRouteChannel,
			logger.ObservationDomainIdentityRouteChannel,
		},
		{
			"route_session",
			agentDiagnosticRouteSessionField,
			logger.ObservationPrefixIdentityRouteSession,
			logger.ObservationDomainIdentityRouteSession,
		},
		{
			"target_channel",
			agentDiagnosticTargetChannelField,
			logger.ObservationPrefixIdentityTargetChannel,
			logger.ObservationDomainIdentityTargetChannel,
		},
		{
			"context_manager",
			agentDiagnosticContextManagerField,
			logger.ObservationPrefixIdentityContextManager,
			logger.ObservationDomainIdentityContextManager,
		},
		{
			"prompt_part",
			agentDiagnosticPromptPartField,
			logger.ObservationPrefixIdentityPromptPart,
			logger.ObservationDomainIdentityPromptPart,
		},
		{
			"prompt_source",
			agentDiagnosticPromptSourceField,
			logger.ObservationPrefixIdentityPromptSource,
			logger.ObservationDomainIdentityPromptSource,
		},
		{
			"prompt_layer",
			agentDiagnosticPromptLayerField,
			logger.ObservationPrefixIdentityPromptLayer,
			logger.ObservationDomainIdentityPromptLayer,
		},
		{
			"prompt_slot",
			agentDiagnosticPromptSlotField,
			logger.ObservationPrefixIdentityPromptSlot,
			logger.ObservationDomainIdentityPromptSlot,
		},
		{
			"reason",
			agentDiagnosticReasonField,
			logger.ObservationPrefixIdentityReason,
			logger.ObservationDomainIdentityReason,
		},
		{
			"scope",
			agentDiagnosticScopeField,
			logger.ObservationPrefixIdentityScope,
			logger.ObservationDomainIdentityScope,
		},
		{
			"tool_surface",
			agentDiagnosticToolSurfaceField,
			logger.ObservationPrefixIdentityToolSurface,
			logger.ObservationDomainIdentityToolSurface,
		},
	}
	records, raw := captureP015HookRecords(t, func() {
		for _, contract := range helpers {
			logger.InfoSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageEvent,
				logger.NewSafeFields(contract.helper(canary)),
			)
		}
	})
	if len(records) != len(helpers) {
		t.Fatalf("identity records = %d, want %d", len(records), len(helpers))
	}
	seenDigests := make(map[string]struct{}, len(records))
	for index, record := range records {
		contract := helpers[index]
		p015B2AAssertRuntimeObservation(
			t,
			record,
			contract.prefix,
			logger.ObserveIdentity(contract.domain, canary),
		)
		digest := ""
		digestCount := 0
		for key, value := range record {
			if len(key) > len("_digest") && key[len(key)-len("_digest"):] == "_digest" {
				digestCount++
				digest, _ = value.(string)
			}
		}
		if digestCount != 1 || digest == "" {
			t.Fatalf("identity helper %s emitted %d digests: %#v", contract.name, digestCount, record)
		}
		if _, duplicate := seenDigests[digest]; duplicate {
			t.Fatalf("identity helper %s reused digest %q", contract.name, digest)
		}
		seenDigests[digest] = struct{}{}
	}
	assertP015CanariesAbsent(t, raw, canary)
}

func TestAgentDiagnosticErrorPanicAndPathHelpersInvokeNoMethods(t *testing.T) {
	const pathCanary = "/P015B2/private/path/canary"
	var calls atomic.Int64
	hostile := &agentDiagnosticHelperHostileError{calls: &calls}
	records, raw := captureP015HookRecords(t, func() {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageEvent,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, hostile),
				agentDiagnosticPanicField(hostile),
				agentDiagnosticPathField(pathCanary),
			),
		)
	})
	if calls.Load() != 0 {
		t.Fatalf("diagnostic helpers invoked %d hostile methods", calls.Load())
	}
	if len(records) != 1 || !p015B2ANonemptyRecordString(records[0], "error_digest") ||
		!p015B2ANonemptyRecordString(records[0], "panic_digest") ||
		!p015B2ANonemptyRecordString(records[0], "path_digest") {
		t.Fatalf("diagnostic helper record = %#v", records)
	}
	assertP015CanariesAbsent(t, raw, pathCanary, "P015B2_HOSTILE")
}

func TestAgentDiagnosticRuntimeEventKindHelperIsSealed(t *testing.T) {
	const canary = runtimeevents.Kind("P015B2_RUNTIME_EVENT_KIND_5ab86ce9")
	records, raw := captureP015HookRecords(t, func() {
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageEvent,
			logger.NewSafeFields(agentDiagnosticRuntimeEventKindField(canary)),
		)
	})
	if len(records) != 1 ||
		!p015B2ANonemptyRecordString(records[0], "identity_runtime_event_kind_digest") {
		t.Fatalf("runtime-event-kind helper record = %#v", records)
	}
	if _, invalid := records[0]["safe_fields_state"]; invalid {
		t.Fatalf("runtime-event-kind helper emitted invalid fields: %#v", records[0])
	}
	assertP015CanariesAbsent(t, raw, string(canary))
}

func TestAgentDiagnosticRoleEnumIsClosed(t *testing.T) {
	tests := map[string]logger.SafeEnumValue{
		"system":    logger.SafeEnumSystem,
		"user":      logger.SafeEnumUser,
		"assistant": logger.SafeEnumAssistant,
		"tool":      logger.SafeEnumTool,
		"developer": logger.SafeEnumDeveloper,
		"":          logger.SafeEnumUnknown,
		"USER":      logger.SafeEnumUnknown,
		"private":   logger.SafeEnumUnknown,
	}
	for role, want := range tests {
		if got := agentDiagnosticRoleEnum(role); got != want {
			t.Errorf("role %q enum = %d, want %d", role, got, want)
		}
	}

	const canary = "P015B2_UNKNOWN_ROLE_4218c90f"
	records, raw := captureP015HookRecords(t, func() {
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageEvent,
			logger.NewSafeFields(logger.SafeEnum(
				logger.FieldRole,
				agentDiagnosticRoleEnum(canary),
			)),
		)
	})
	if len(records) != 1 || records[0]["role"] != "unknown" {
		t.Fatalf("unknown role record = %#v", records)
	}
	assertP015CanariesAbsent(t, raw, canary)
}
