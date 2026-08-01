import { launcherFetch } from "@/api/http"

export interface WorkflowDefinition {
  ref: string
  name?: string
  path?: string
  error?: string
  workflow_call?: WorkflowCallDefinition
  event_trigger?: WorkflowEventTrigger | null
}

export interface WorkflowCallDefinition {
  inputs?: Record<string, WorkflowInputDefinition>
  secrets?: Record<string, WorkflowSecretDefinition>
  outputs?: Record<string, WorkflowOutputDefinition>
}

export interface WorkflowInputDefinition {
  type?: string
  required?: boolean
  default?: unknown
}

export interface WorkflowSecretDefinition {
  required?: boolean
}

export interface WorkflowOutputDefinition {
  value?: string
}

export interface WorkflowValidationIssue {
  path?: string
  message: string
}

export interface WorkflowValidationStamp {
  workflow_ref: string
  workflow_hash?: string
  validated_against_picoclaw_version: string
  validated_against_git_commit?: string
  workflow_engine_version: string
  workflow_schema_version: string
  validator_fingerprint: string
  status: string
  errors?: WorkflowValidationIssue[]
  warnings?: WorkflowValidationIssue[]
  validated_at: string
}

export interface WorkflowRuntimeCompatibility {
  picoclaw_version: string
  git_commit?: string
  workflow_engine_version: string
  workflow_schema_version: string
  validator_fingerprint: string
}

export interface WorkflowCompatibilitySummary {
  current: WorkflowRuntimeCompatibility
  manifest_runtime?: WorkflowRuntimeCompatibility
  workflows: WorkflowValidationStamp[]
  counts: Record<string, number>
  version_changed: boolean
  manifest_missing: boolean
  has_blocking: boolean
}

export interface WorkflowDevelopmentValidation {
  valid: boolean
  errors?: WorkflowValidationIssue[]
  warnings?: WorkflowValidationIssue[]
  validated_at: string
}

export interface WorkflowDevelopmentTestSnapshot {
  draft_key: string
  draft_revision?: string
  target_workflow_ref: string
  run_id?: string
  event_id?: string
  status: string
  error?: string
  tested_at: string
}

export interface WorkflowDevelopmentSession {
  id: string
  session_revision: string
  draft_revision: string
  base_target_revision: string
  reason: "new" | "edit" | "version_revalidation" | string
  status: string
  prompt?: string
  source_workflow_ref?: string
  target_workflow_ref: string
  target_picoclaw_version?: string
  target_git_commit?: string
  yaml: string
  validation?: WorkflowDevelopmentValidation
  last_test?: WorkflowDevelopmentTestSnapshot
  created_at: string
  updated_at: string
}

export interface WorkflowDevelopmentTestReconciliation {
  state: "degraded"
  reason:
    | "draft_test_snapshot_not_recorded"
    | "draft_test_run_unavailable"
    | "draft_test_terminal_snapshot_not_recorded"
    | "draft_test_response_truncated"
  run_id: string
  message: string
}

export interface WorkflowDevelopmentResult {
  session: WorkflowDevelopmentSession | null
  reconciliation?: WorkflowDevelopmentTestReconciliation
}

export interface WorkflowEventEntityTrigger {
  ids?: string[]
  types?: string[]
  attributes?: Record<string, string[]>
}

export interface WorkflowEventTrigger {
  sources?: string[]
  connectors?: string[]
  types?: string[]
  actor?: WorkflowEventEntityTrigger
  subject?: WorkflowEventEntityTrigger
  attributes?: Record<string, string[]>
}

export interface WorkflowEventTriggerInspection {
  revision: string
  editable: boolean
  reason?: string
  event_trigger?: WorkflowEventTrigger | null
  validation?: WorkflowDevelopmentValidation
}

export interface WorkflowEventTriggerRenderResult extends WorkflowEventTriggerInspection {
  yaml: string
}

export const workflowTriggerKinds = [
  "manual",
  "schedule",
  "channel_message",
  "command",
  "runtime_event",
  "event",
  "workflow_call",
] as const

export type WorkflowTriggerKind = (typeof workflowTriggerKinds)[number]

export type WorkflowManualTrigger = Record<string, never>

export interface WorkflowScheduleTrigger {
  cron?: string
}

export interface WorkflowConversationSpec {
  session?: "discussion" | "sender" | "global" | string
  delivery?: "same_discussion" | "none" | string
}

export interface WorkflowChannelMessageTrigger {
  channels?: string[]
  chats?: string[]
  senders?: string[]
  mentioned?: boolean
  command?: string
  text_matches?: string
  passthrough?: boolean
  conversation?: WorkflowConversationSpec
}

export interface WorkflowCommandTrigger {
  name?: string
  channels?: string[]
  chats?: string[]
  senders?: string[]
  args?: Record<string, WorkflowInputDefinition>
  passthrough?: boolean
  conversation?: WorkflowConversationSpec
}

export interface WorkflowRuntimeEventTrigger {
  kinds?: string[]
  sources?: string[]
  agents?: string[]
  sessions?: string[]
  channels?: string[]
  chats?: string[]
}

export type WorkflowCallTrigger = WorkflowCallDefinition

export interface WorkflowTriggerValueMap {
  manual: WorkflowManualTrigger
  schedule: WorkflowScheduleTrigger[]
  channel_message: WorkflowChannelMessageTrigger
  command: WorkflowCommandTrigger
  runtime_event: WorkflowRuntimeEventTrigger
  event: WorkflowEventTrigger
  workflow_call: WorkflowCallTrigger
}

export type WorkflowTriggerProjectionMap = {
  [Kind in WorkflowTriggerKind]: {
    present: boolean
    editable: boolean
    reason?: string
    value: WorkflowTriggerValueMap[Kind] | null
  }
}

export interface WorkflowTriggersInspection {
  revision: string
  validation?: WorkflowDevelopmentValidation
  triggers: WorkflowTriggerProjectionMap
}

export interface WorkflowTriggerRenderResult extends WorkflowTriggersInspection {
  yaml: string
}

export type WorkflowEditorJSONValue =
  | null
  | boolean
  | number
  | string
  | WorkflowEditorJSONValue[]
  | { [key: string]: WorkflowEditorJSONValue }

export interface WorkflowEditorField<Value> {
  present: boolean
  value: Value | null
}

export interface WorkflowJobEditorContext {
  session?: string
  delivery?: string
}

export interface WorkflowJobEditorJobFields {
  name: WorkflowEditorField<string>
  runs_on: WorkflowEditorField<string>
  needs: WorkflowEditorField<string[]>
  uses: WorkflowEditorField<string>
  if: WorkflowEditorField<string>
  continue_on_error: WorkflowEditorField<boolean>
  with: WorkflowEditorField<Record<string, WorkflowEditorJSONValue>>
  secrets: WorkflowEditorField<
    "inherit" | Record<string, WorkflowEditorJSONValue>
  >
  outputs: WorkflowEditorField<Record<string, string>>
  context: WorkflowEditorField<WorkflowJobEditorContext>
}

export interface WorkflowJobEditorStepFields {
  id: WorkflowEditorField<string>
  name: WorkflowEditorField<string>
  uses: WorkflowEditorField<string>
  if: WorkflowEditorField<string>
  continue_on_error: WorkflowEditorField<boolean>
  with: WorkflowEditorField<Record<string, WorkflowEditorJSONValue>>
  context: WorkflowEditorField<WorkflowJobEditorContext>
}

export interface WorkflowJobEditorStep {
  index: number
  editable: boolean
  reason?: string
  advanced_fields_present: boolean
  fields: WorkflowJobEditorStepFields
}

export interface WorkflowJobEditorJob {
  id: string
  index: number
  editable: boolean
  reason?: string
  advanced_fields_present: boolean
  steps_present: boolean
  fields: WorkflowJobEditorJobFields
  steps: WorkflowJobEditorStep[]
}

export type WorkflowJobEditorLimit =
  | "jobs_truncated"
  | "steps_truncated"
  | "unsafe_fields_omitted"
  | "validation_truncated"

export interface WorkflowJobsInspection {
  revision: string
  editable: boolean
  reason?: string
  complete: boolean
  limits: WorkflowJobEditorLimit[]
  jobs: WorkflowJobEditorJob[]
  validation: WorkflowDevelopmentValidation
}

export interface WorkflowJobsRenderResult extends WorkflowJobsInspection {
  yaml: string
}

export type WorkflowEditorSetMutation<Value> = {
  mode: "set"
  value: Value
}

export type WorkflowEditorRemoveMutation = {
  mode: "remove"
}

export type WorkflowEditorFieldMutation<Value> =
  | WorkflowEditorSetMutation<Value>
  | WorkflowEditorRemoveMutation

export type WorkflowJobEditorJobPatch = {
  [Key in keyof WorkflowJobEditorJobFields]?: WorkflowEditorFieldMutation<
    NonNullable<WorkflowJobEditorJobFields[Key]["value"]>
  >
}

export type WorkflowJobEditorStepPatch = {
  [Key in keyof WorkflowJobEditorStepFields]?: WorkflowEditorFieldMutation<
    NonNullable<WorkflowJobEditorStepFields[Key]["value"]>
  >
}

export type WorkflowJobEditorJobInsertFields = {
  [Key in keyof WorkflowJobEditorJobFields]?: WorkflowEditorSetMutation<
    NonNullable<WorkflowJobEditorJobFields[Key]["value"]>
  >
}

export type WorkflowJobEditorStepInsertFields = {
  [Key in keyof WorkflowJobEditorStepFields]?: WorkflowEditorSetMutation<
    NonNullable<WorkflowJobEditorStepFields[Key]["value"]>
  >
}

export type WorkflowJobEditorOperation =
  | {
      type: "job.insert"
      job_id: string
      index: number
      fields: WorkflowJobEditorJobInsertFields
    }
  | {
      type: "job.delete"
      job_id: string
    }
  | {
      type: "job.patch"
      job_id: string
      fields: WorkflowJobEditorJobPatch
      new_job_id?: WorkflowEditorSetMutation<string>
    }
  | {
      type: "step.insert"
      job_id: string
      index: number
      fields: WorkflowJobEditorStepInsertFields
    }
  | {
      type: "step.delete"
      job_id: string
      step_index: number
    }
  | {
      type: "step.move"
      job_id: string
      step_index: number
      to_index: number
    }
  | {
      type: "step.patch"
      job_id: string
      step_index: number
      fields: WorkflowJobEditorStepPatch
    }

export interface WorkflowEventTriggerMatchCheck {
  path: string
  present: boolean
  value?: unknown
  matched: boolean
}

export interface WorkflowEventTriggerMatchResult {
  event_id: string
  matched: boolean
  checks: WorkflowEventTriggerMatchCheck[]
  validation?: WorkflowDevelopmentValidation
}

export interface WorkflowRun {
  id: string
  workflow_ref: string
  status: string
  origin?: WorkflowRunOrigin
  parent_run_id?: string
  child_run_ids?: string[]
  caller_job_id?: string
  retry_of_run_id?: string
  session?: string
  delivery?: Record<string, unknown>
  event?: Record<string, unknown>
  inputs?: Record<string, unknown>
  outputs?: Record<string, unknown>
  jobs?: Record<string, WorkflowJobExecution>
  steps?: Record<string, WorkflowStepExecution>
  error?: string
  cancel_reason?: string
  created_at: string
  updated_at: string
  completed_at?: string
  cancel_requested_at?: string
}

export interface WorkflowRunOrigin {
  kind: "external_event" | "external_event_draft_test"
  event_id: string
  dispatch_id?: string
  root_run_id: string
}

export interface WorkflowJobExecution {
  id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowStepExecution {
  id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowRunEvent {
  time: string
  kind: string
  run_id: string
  job_id?: string
  step_id?: string
  message?: string
  payload?: Record<string, unknown>
}

export interface WorkflowRunGraph {
  run_id: string
  nodes: Array<{
    id: string
    workflow_ref: string
    status: string
    parent_run_id?: string
    caller_job_id?: string
    retry_of_run_id?: string
  }>
  edges: Array<{
    from: string
    to: string
    job_id?: string
    kind: string
  }>
}

export interface WorkflowReloadResult {
  reloaded_at: string
  workflows: WorkflowDefinition[]
  errors: Array<{ ref: string; error: string }>
}

export interface WorkflowRunResult {
  run_id: string
  status: string
  outputs?: Record<string, unknown>
  error?: string
}

export interface WorkflowDevelopmentTestResult {
  session?: WorkflowDevelopmentSession
  result?: WorkflowRunResult
  reconciliation?: WorkflowDevelopmentTestReconciliation
  error?: string
}

export interface WorkflowRunLaunchResult {
  result: WorkflowRunResult
  error?: string
}

export type WorkflowTemplateState =
  | "available"
  | "installed"
  | "modified"
  | "blocked"

export interface WorkflowTemplateCatalogEntry {
  name: string
  ref: string
  state: WorkflowTemplateState
  blocked_reason?:
    | "configuration_invalid"
    | "target_not_regular"
    | "target_unavailable"
    | string
}

export interface WorkflowTemplateInstallResult {
  name: string
  ref: string
  state: WorkflowTemplateState
  installed: boolean
  overwritten?: boolean
  revalidated: boolean
}

export interface WorkflowTemplateCatalog {
  templates: WorkflowTemplateCatalogEntry[]
}

export type WorkflowDefinitionInspectionSource =
  | {
      kind: "published"
      ref: string
    }
  | {
      kind: "template"
      template_name: string
    }

export type WorkflowDefinitionInspectionValidationCode =
  | "invalid_yaml"
  | "jobs_required"
  | "schedule_cron_required"
  | "schedule_cron_invalid"
  | "input_name_required"
  | "input_type_unsupported"
  | "input_default_invalid"
  | "output_required"
  | "output_expression_invalid"
  | "conversation_session_unsupported"
  | "conversation_delivery_unsupported"
  | "channel_pattern_invalid"
  | "command_name_required"
  | "runtime_filter_required"
  | "event_filter_required"
  | "event_entity_filter_required"
  | "event_pattern_required"
  | "event_attribute_required"
  | "job_id_required"
  | "job_dependency_unknown"
  | "job_dependency_cycle"
  | "reusable_target_invalid"
  | "reusable_steps_unsupported"
  | "job_runner_required"
  | "job_steps_required"
  | "step_id_duplicate"
  | "step_target_required"
  | "reusable_step_unsupported"
  | "step_target_unsupported"
  | "run_session_unsupported"
  | "run_delivery_unsupported"
  | "agent_history_unsupported"
  | "agent_cache_unsupported"
  | "agent_tools_unsupported"
  | "definition_invalid"

export type WorkflowDefinitionInspectionValidationScope =
  | "workflow"
  | "jobs"
  | "trigger.manual"
  | "trigger.schedule"
  | "trigger.channel_message"
  | "trigger.command"
  | "trigger.runtime_event"
  | "trigger.event"
  | "trigger.workflow_call"

export interface WorkflowDefinitionInspectionValidationIssue {
  code: WorkflowDefinitionInspectionValidationCode
  scope: WorkflowDefinitionInspectionValidationScope
}

export interface WorkflowDefinitionInspectionValidation {
  valid: boolean
  issue_count: number
  issues: WorkflowDefinitionInspectionValidationIssue[]
  truncated: boolean
}

export interface WorkflowDefinitionInspectionInput {
  type?: string
  required: boolean
  has_default: boolean
}

export interface WorkflowDefinitionInspectionChannelTrigger {
  channels?: string[]
  chats?: string[]
  senders?: string[]
  mentioned?: boolean
  command?: string
  text_matches?: string
  passthrough?: boolean
  session_configured: boolean
  delivery_configured: boolean
}

export interface WorkflowDefinitionInspectionCommandTrigger {
  name?: string
  channels?: string[]
  chats?: string[]
  senders?: string[]
  args?: Record<string, WorkflowDefinitionInspectionInput>
  passthrough?: boolean
  session_configured: boolean
  delivery_configured: boolean
}

export interface WorkflowDefinitionInspectionRuntimeEventTrigger {
  kinds?: string[]
  sources?: string[]
  agents?: string[]
  session_filter_present: boolean
  session_filter_count: number
  channels?: string[]
  chats?: string[]
}

export interface WorkflowDefinitionInspectionSecret {
  required: boolean
}

export interface WorkflowDefinitionInspectionWorkflowCallTrigger {
  inputs?: Record<string, WorkflowDefinitionInspectionInput>
  secrets?: Record<string, WorkflowDefinitionInspectionSecret>
  outputs?: string[]
}

export interface WorkflowDefinitionInspectionTriggerValueMap {
  manual: Record<string, never>
  schedule: WorkflowScheduleTrigger[]
  channel_message: WorkflowDefinitionInspectionChannelTrigger
  command: WorkflowDefinitionInspectionCommandTrigger
  runtime_event: WorkflowDefinitionInspectionRuntimeEventTrigger
  event: WorkflowEventTrigger
  workflow_call: WorkflowDefinitionInspectionWorkflowCallTrigger
}

export interface WorkflowDefinitionInspectionTrigger<
  Value = WorkflowDefinitionInspectionTriggerValueMap[WorkflowTriggerKind],
> {
  present: boolean
  projected: boolean
  value?: Value
}

export type WorkflowDefinitionInspectionTriggers = {
  [Kind in WorkflowTriggerKind]: WorkflowDefinitionInspectionTrigger<
    WorkflowDefinitionInspectionTriggerValueMap[Kind]
  >
}

export interface WorkflowDefinitionInspectionStep {
  index: number
  id?: string
  kind: "agent" | "tool" | "mcp" | "function" | "unknown"
  target?: string
}

export interface WorkflowDefinitionInspectionJob {
  id: string
  kind: "steps" | "reusable"
  reusable_target?: string
  steps: WorkflowDefinitionInspectionStep[]
}

export interface WorkflowDefinitionInspectionDependency {
  kind: WorkflowDependencyKind
  target: string
  occurrences: number
}

export interface WorkflowDefinitionInspectionEffect {
  kind:
    | "model_or_delegated_action_possible"
    | "state_change_possible"
    | "external_state_change_possible"
    | "transitive_effects_unknown"
    | "unclassified_action"
  target?: string
  occurrences: number
}

export interface WorkflowDefinitionInspection {
  source: WorkflowDefinitionInspectionSource
  revision: string
  complete: boolean
  validation: WorkflowDefinitionInspectionValidation
  triggers: WorkflowDefinitionInspectionTriggers
  jobs: WorkflowDefinitionInspectionJob[]
  dependencies: WorkflowDefinitionInspectionDependency[]
  effects: WorkflowDefinitionInspectionEffect[]
  limits: Array<
    | "jobs_truncated"
    | "steps_truncated"
    | "dependencies_truncated"
    | "effects_truncated"
    | "triggers_truncated"
    | "unsafe_fields_omitted"
    | "validation_issues_truncated"
  >
}

export const workflowAuthoringCapabilitiesQueryKey = [
  "workflows",
  "authoring",
  "capabilities",
] as const

export type WorkflowCapabilityReadiness =
  | "ready"
  | "unchecked"
  | "not_configured"
  | "disabled"
  | "not_allowed"
  | "not_connected"
  | "not_found"
  | "invalid_configuration"
  | "name_collision"
  | "unavailable"

export type WorkflowCapabilityParameterType =
  | "object"
  | "array"
  | "string"
  | "number"
  | "integer"
  | "boolean"
  | "null"

export type WorkflowCapabilityParameterEnumValue =
  | string
  | number
  | boolean
  | null

export interface WorkflowCapabilityParameterProperty {
  name: string
  required: boolean
  shape: WorkflowCapabilityParameterShape
}

export type WorkflowCapabilityAdditionalProperties =
  | { allowed: boolean }
  | { shape: WorkflowCapabilityParameterShape }

export interface WorkflowCapabilityParameterShape {
  type?: WorkflowCapabilityParameterType
  properties?: WorkflowCapabilityParameterProperty[]
  items?: WorkflowCapabilityParameterShape
  enum?: WorkflowCapabilityParameterEnumValue[]
  additional_properties?: WorkflowCapabilityAdditionalProperties
}

export interface WorkflowAgentCapability {
  id: string
  target: string
  is_default: boolean
  readiness: WorkflowCapabilityReadiness
}

export interface WorkflowToolCapability {
  name: string
  target: string
  readiness: WorkflowCapabilityReadiness
  parameter_shape_projected: boolean
  parameter_shape?: WorkflowCapabilityParameterShape
}

export interface WorkflowMCPCapability {
  server: string
  tool: string
  target: string
  readiness: WorkflowCapabilityReadiness
  parameter_shape_projected: boolean
  parameter_shape?: WorkflowCapabilityParameterShape
}

export interface WorkflowFunctionCapability {
  name: string
  target: string
  readiness: WorkflowCapabilityReadiness
}

export type WorkflowAuthoringCapabilityLimit =
  | "agents_truncated"
  | "tools_truncated"
  | "mcp_tools_truncated"
  | "functions_truncated"
  | "parameter_shapes_omitted"
  | "unsafe_fields_omitted"

export interface WorkflowAuthoringCapabilities {
  complete: boolean
  mcp_status: "ready" | "disabled" | "unavailable"
  agents: WorkflowAgentCapability[]
  tools: WorkflowToolCapability[]
  mcp_tools: WorkflowMCPCapability[]
  functions: WorkflowFunctionCapability[]
  limits: WorkflowAuthoringCapabilityLimit[]
}

export interface WorkflowSettingsValues {
  enabled: boolean
  tool_enabled: boolean
  definitions_dir: string
  max_concurrent_runs: number
  default_timeout_seconds: number
  max_call_depth: number
  retention_days: number
}

export interface WorkflowSettingsEffects {
  launcher_effect: string
  catalog_effect: string
  gateway_effect: string
}

export interface WorkflowSettingsResponse {
  configured: WorkflowSettingsValues
  effective: WorkflowSettingsValues
  config_revision: string
  effects: WorkflowSettingsEffects
}

export interface WorkflowSettingsPatch extends Partial<WorkflowSettingsValues> {
  expected_config_revision: string
}

export type WorkflowDependencyKind =
  | "agent"
  | "tool"
  | "mcp"
  | "function"
  | "human"
  | "reusable"

export interface WorkflowDependencyOccurrence {
  kind: WorkflowDependencyKind
  name: string
  workflow_ref: string
  path: string
}

export type WorkflowDependencyIssueCode =
  | "invalid_reusable_ref"
  | "reusable_unavailable"
  | "reusable_invalid"
  | "reusable_cycle"
  | "call_depth_exceeded"
  | "missing_required_input"
  | "input_type_mismatch"
  | "invalid_secrets"
  | "missing_required_secret"
  | "human_task_reusable_unsupported"
  | "analysis_limit_exceeded"

export interface WorkflowDependencyIssue {
  code: WorkflowDependencyIssueCode
  workflow_ref: string
  path: string
  dependency_kind?: WorkflowDependencyKind
  dependency_name?: string
}

export type WorkflowDependencyReadinessCode =
  | "ready"
  | "unchecked"
  | "not_configured"
  | "disabled"
  | "not_allowed"
  | "not_connected"
  | "not_found"
  | "invalid_configuration"
  | "name_collision"
  | "unavailable"

export interface WorkflowDependencyReadiness {
  dependency: WorkflowDependencyOccurrence
  code: WorkflowDependencyReadinessCode
  ready: boolean
}

export interface WorkflowDependencyCheckResponse {
  root_ref: string
  revision: string
  ready: boolean
  workflow_enabled: boolean
  structural_ready: boolean
  runtime_ready: boolean
  dependencies: WorkflowDependencyReadiness[]
  structural_issues: WorkflowDependencyIssue[]
}

export type WorkflowDependencyCheckRequest =
  | {
      draft: {
        target_ref: string
        yaml: string
      }
      ref?: never
    }
  | {
      ref: string
      draft?: never
    }

export interface WorkflowDevelopmentPublishRequest {
  session_id: string
  expected_session_revision: string
  expected_draft_revision: string
  expected_base_target_revision: string
  expected_dependency_revision: string
}

export type WorkflowDeliveryPayload = Record<string, unknown>

export interface WorkflowTriggerSimulationRequestBase {
  session_id: string
  expected_session_revision: string
  expected_draft_revision: string
  prompt: string
  target_ref: string
  yaml: string
}

export interface WorkflowTriggerInvocationScenario {
  inputs?: Record<string, unknown>
  secrets?: Record<string, string>
  session?: string
  delivery?: WorkflowDeliveryPayload
}

export interface WorkflowTriggerMessageEnvelope {
  channel?: string
  account?: string
  chat_id?: string
  chat_type?: string
  topic_id?: string
  space_id?: string
  space_type?: string
  sender_id?: string
  sender_username?: string
  sender_name?: string
  message_id?: string
  reply_to_message_id?: string
  mentioned?: boolean
  text?: string
  media?: string[]
  reply_handles?: Record<string, string>
  raw?: Record<string, string>
}

export interface WorkflowTriggerRuntimeEventEnvelope {
  id: string
  kind: string
  time: string
  source: {
    component: string
    name?: string
  }
  scope?: Record<string, unknown>
  correlation?: Record<string, unknown>
  severity?: string
  payload?: unknown
  attrs?: Record<string, unknown>
}

export type WorkflowTriggerSimulationRequest =
  WorkflowTriggerSimulationRequestBase &
    (
      | {
          trigger: { type: "manual" }
          scenario: WorkflowTriggerInvocationScenario
        }
      | {
          trigger: { type: "workflow_call" }
          scenario: WorkflowTriggerInvocationScenario
        }
      | {
          trigger: { type: "schedule"; schedule_index: number }
          scenario: { scheduled_at: string }
        }
      | {
          trigger: { type: "channel_message" }
          scenario: { message: WorkflowTriggerMessageEnvelope }
        }
      | {
          trigger: { type: "command" }
          scenario: { message: WorkflowTriggerMessageEnvelope }
        }
      | {
          trigger: { type: "runtime_event" }
          scenario: { event: WorkflowTriggerRuntimeEventEnvelope }
        }
      | {
          trigger: { type: "event" }
          scenario: { event_id: string }
        }
    )

export type WorkflowTriggerSimulationReason =
  | "matched"
  | "invalid_workflow"
  | "trigger_absent"
  | "schedule_index_required"
  | "schedule_index_out_of_range"
  | "invalid_scenario"
  | "not_matched"
  | "shadowed_by_command"
  | "runtime_feedback_suppressed"
  | "trigger_evaluation_failed"
  | "review_incomplete"

export interface WorkflowTriggerSimulationContextSummary {
  input_count: number
  secret_count: number
  has_session: boolean
  has_delivery: boolean
  has_event: boolean
}

export interface WorkflowTriggerSimulation {
  selected_kind: WorkflowTriggerKind
  effective_kind?: WorkflowTriggerKind
  schedule_index?: number
  present: boolean
  matched: boolean
  executable: boolean
  reason: WorkflowTriggerSimulationReason
  passthrough?: boolean
  context_summary: WorkflowTriggerSimulationContextSummary
}

export interface WorkflowTriggerSimulationReview {
  job_count: number
  step_count: number
  targets: string[]
  effects: WorkflowDefinitionInspectionEffect[]
  complete: boolean
  validation: WorkflowDefinitionInspectionValidation
  limits: WorkflowDefinitionInspection["limits"]
}

export interface WorkflowTriggerSimulationResponse {
  simulation: WorkflowTriggerSimulation
  review: WorkflowTriggerSimulationReview
  review_token?: string
}

export class WorkflowAPIError extends Error {
  readonly status: number
  readonly candidateValidation?: WorkflowDevelopmentValidation

  constructor(
    message: string,
    status: number,
    candidateValidation?: WorkflowDevelopmentValidation,
  ) {
    super(message)
    this.name = "WorkflowAPIError"
    this.status = status
    this.candidateValidation = candidateValidation
  }
}

export class WorkflowJobsEditorAPIError extends WorkflowAPIError {
  readonly inspection?: WorkflowJobsInspection

  constructor(
    message: string,
    status: number,
    inspection?: WorkflowJobsInspection,
  ) {
    super(message, status, inspection?.validation)
    this.name = "WorkflowJobsEditorAPIError"
    this.inspection = inspection
  }
}

export const WORKFLOW_CANCEL_REASON_MAX_BYTES = 1024

const workflowRunIDPattern = /^wr_[A-Za-z0-9_-]+$/
const maximumWorkflowRunIDBytes = 1024

function validWorkflowRunID(value: string): boolean {
  return (
    new TextEncoder().encode(value).byteLength <= maximumWorkflowRunIDBytes &&
    workflowRunIDPattern.test(value)
  )
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const text = await res.text()
    const details = apiErrorDetails(text, res.status, res.statusText)
    throw new WorkflowAPIError(
      details.message,
      res.status,
      details.candidateValidation,
    )
  }
  return res.json() as Promise<T>
}

function apiErrorMessage(text: string, status: number, statusText: string) {
  return apiErrorDetails(text, status, statusText).message
}

function apiErrorDetails(text: string, status: number, statusText: string) {
  let message = text.trim()
  let candidateValidation: WorkflowDevelopmentValidation | undefined
  try {
    const body = JSON.parse(text) as {
      error?: string
      errors?: string[]
      candidate_validation?: unknown
    }
    if (typeof body.error === "string" && body.error.trim() !== "") {
      message = body.error
    } else if (Array.isArray(body.errors) && body.errors.length > 0) {
      message = body.errors.join("; ")
    }
    candidateValidation = workflowCandidateValidation(body.candidate_validation)
  } catch {
    // Keep the plain-text response when the backend did not return JSON.
  }
  return {
    message: message || `API error: ${status} ${statusText}`,
    candidateValidation,
  }
}

function workflowCandidateValidation(
  value: unknown,
): WorkflowDevelopmentValidation | undefined {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return undefined
  }
  const candidate = value as Record<string, unknown>
  if (candidate.valid !== false || typeof candidate.validated_at !== "string") {
    return undefined
  }
  const errors = workflowCandidateValidationIssues(candidate.errors)
  const warnings = workflowCandidateValidationIssues(candidate.warnings)
  if (errors == null || warnings == null) {
    return undefined
  }
  return {
    valid: false,
    validated_at: candidate.validated_at.slice(0, 128),
    ...(errors.length > 0 ? { errors } : {}),
    ...(warnings.length > 0 ? { warnings } : {}),
  }
}

function workflowCandidateValidationIssues(
  value: unknown,
): WorkflowValidationIssue[] | null {
  if (value == null) {
    return []
  }
  if (!Array.isArray(value) || value.length > 128) {
    return null
  }
  const issues: WorkflowValidationIssue[] = []
  for (const item of value) {
    if (item == null || typeof item !== "object" || Array.isArray(item)) {
      return null
    }
    const issue = item as Record<string, unknown>
    if (
      typeof issue.message !== "string" ||
      issue.message === "" ||
      issue.message.length > 4096 ||
      (issue.path != null &&
        (typeof issue.path !== "string" || issue.path.length > 1024))
    ) {
      return null
    }
    issues.push({
      message: issue.message,
      ...(typeof issue.path === "string" ? { path: issue.path } : {}),
    })
  }
  return issues
}

export async function listWorkflows(): Promise<{
  workflows: WorkflowDefinition[]
  compatibility?: WorkflowCompatibilitySummary
}> {
  const payload = await request<{
    workflows?: WorkflowDefinition[] | null
    compatibility?: WorkflowCompatibilitySummary | null
  }>("/api/workflows")
  return {
    workflows: arrayOrEmpty(payload.workflows),
    compatibility:
      payload.compatibility == null
        ? undefined
        : normalizeWorkflowCompatibilitySummary(payload.compatibility),
  }
}

export async function getWorkflowCompatibility(): Promise<WorkflowCompatibilitySummary> {
  return normalizeWorkflowCompatibilitySummary(
    await request<WorkflowCompatibilitySummary>("/api/workflows/compatibility"),
  )
}

export async function revalidateWorkflows(): Promise<WorkflowCompatibilitySummary> {
  return normalizeWorkflowCompatibilitySummary(
    await request<WorkflowCompatibilitySummary>("/api/workflows/revalidate", {
      method: "POST",
    }),
  )
}

export async function listWorkflowTemplates(): Promise<WorkflowTemplateCatalog> {
  const payload = await requestWorkflowControl<{
    templates?: WorkflowTemplateCatalogEntry[] | null
  }>(
    "/api/workflows/templates",
    undefined,
    "Built-in workflow templates are unavailable.",
  )
  return { templates: arrayOrEmpty(payload.templates) }
}

export async function installWorkflowTemplate(
  name: string,
  overwrite: boolean,
): Promise<{
  result: WorkflowTemplateInstallResult
  templates: WorkflowTemplateCatalogEntry[]
}> {
  const payload = await requestWorkflowControl<{
    result: WorkflowTemplateInstallResult
    templates?: WorkflowTemplateCatalogEntry[] | null
  }>(
    `/api/workflows/templates/${encodeURIComponent(name)}/install`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ overwrite }),
    },
    "The workflow template could not be installed.",
  )
  return { ...payload, templates: arrayOrEmpty(payload.templates) }
}

export async function inspectPublishedWorkflowDefinition(
  ref: string,
  signal?: AbortSignal,
): Promise<WorkflowDefinitionInspection> {
  const payload = await requestWorkflowInspection(
    "/api/workflows/definitions/inspect",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ref }),
      signal,
    },
  )
  return parseWorkflowDefinitionInspection(payload, {
    kind: "published",
    ref,
  })
}

export async function inspectWorkflowTemplate(
  name: string,
  signal?: AbortSignal,
): Promise<WorkflowDefinitionInspection> {
  const payload = await requestWorkflowInspection(
    `/api/workflows/templates/${encodeURIComponent(name)}/inspect`,
    { signal },
  )
  return parseWorkflowDefinitionInspection(payload, {
    kind: "template",
    template_name: name,
  })
}

export async function getWorkflowAuthoringCapabilities(
  signal?: AbortSignal,
): Promise<WorkflowAuthoringCapabilities> {
  const payload = await requestWorkflowAuthoringCapabilities(signal)
  return parseWorkflowAuthoringCapabilities(payload)
}

export async function getWorkflowSettings(): Promise<WorkflowSettingsResponse> {
  return requestWorkflowControl(
    "/api/workflows/settings",
    undefined,
    "Workflow settings are unavailable.",
  )
}

export async function patchWorkflowSettings(
  payload: WorkflowSettingsPatch,
): Promise<WorkflowSettingsResponse> {
  return requestWorkflowControl(
    "/api/workflows/settings",
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
    "Workflow settings could not be saved.",
  )
}

export async function checkWorkflowDependencies(
  payload: WorkflowDependencyCheckRequest,
  signal?: AbortSignal,
): Promise<WorkflowDependencyCheckResponse> {
  const result = await requestWorkflowControl<WorkflowDependencyCheckResponse>(
    "/api/workflows/dependencies/check",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal,
    },
    "Workflow dependency readiness is unavailable.",
  )
  return {
    ...result,
    dependencies: arrayOrEmpty(result.dependencies),
    structural_issues: arrayOrEmpty(result.structural_issues),
  }
}

export async function getWorkflowDevelopment(): Promise<WorkflowDevelopmentResult> {
  return request("/api/workflows/development")
}

export async function startWorkflowDevelopment(payload: {
  reason?: "new" | "edit" | "version_revalidation" | string
  prompt?: string
  ref?: string
  target_ref?: string
}): Promise<{ session: WorkflowDevelopmentSession; conflict?: boolean }> {
  const res = await launcherFetch("/api/workflows/development/start", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  const text = await res.text()
  if (res.ok) {
    return JSON.parse(text) as {
      session: WorkflowDevelopmentSession
    }
  }
  if (res.status === 409) {
    try {
      const body = JSON.parse(text) as {
        session?: WorkflowDevelopmentSession
      }
      if (body.session != null) {
        return { session: body.session, conflict: true }
      }
    } catch {
      // Fall through to the normal error message path.
    }
  }
  throw new Error(apiErrorMessage(text, res.status, res.statusText))
}

export async function reviseWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
  regenerate?: boolean
}): Promise<{ session: WorkflowDevelopmentSession }> {
  return request("/api/workflows/development/revise", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function inspectWorkflowEventTrigger(
  yaml: string,
  signal?: AbortSignal,
): Promise<WorkflowEventTriggerInspection> {
  return request("/api/workflows/development/event-trigger/inspect", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ yaml }),
    signal,
  })
}

export async function renderWorkflowEventTrigger(payload: {
  yaml: string
  revision: string
  event_trigger: WorkflowEventTrigger | null
}): Promise<WorkflowEventTriggerRenderResult> {
  return request("/api/workflows/development/event-trigger/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function inspectWorkflowTriggers(
  yaml: string,
  signal?: AbortSignal,
): Promise<WorkflowTriggersInspection> {
  return request("/api/workflows/development/triggers/inspect", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ yaml }),
    signal,
  })
}

export async function renderWorkflowTrigger<Kind extends WorkflowTriggerKind>(
  payload: {
    yaml: string
    revision: string
    trigger_type: Kind
    trigger: WorkflowTriggerValueMap[Kind] | null
  },
  signal?: AbortSignal,
): Promise<WorkflowTriggerRenderResult> {
  return request("/api/workflows/development/triggers/render", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    signal,
  })
}

export async function inspectWorkflowJobs(
  yaml: string,
  signal?: AbortSignal,
): Promise<WorkflowJobsInspection> {
  const body = workflowJobsEditorRequestBody({ yaml })
  const payload = await requestWorkflowJobsEditor(
    "/api/workflows/development/jobs/inspect",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      signal,
    },
  )
  return parseWorkflowJobsInspection(payload)
}

export async function renderWorkflowJobs(
  payload: {
    yaml: string
    revision: string
    operation: WorkflowJobEditorOperation
  },
  signal?: AbortSignal,
): Promise<WorkflowJobsRenderResult> {
  if (!workflowJobEditorOperationIDsValid(payload.operation)) {
    throw new WorkflowJobsEditorAPIError(
      "Workflow job and action IDs must be single-line values no larger than 256 UTF-8 bytes.",
      400,
    )
  }
  if (!workflowJobEditorOperationValuesValid(payload.operation)) {
    throw new WorkflowJobsEditorAPIError(
      "The jobs and actions change contains a value that cannot be rendered safely.",
      400,
    )
  }
  const body = workflowJobsEditorRequestBody(payload)
  const response = await requestWorkflowJobsEditor(
    "/api/workflows/development/jobs/render",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      signal,
    },
  )
  return parseWorkflowJobsRenderResult(response)
}

export async function matchWorkflowEventTrigger(
  payload: {
    yaml: string
    event_id: string
  },
  signal?: AbortSignal,
): Promise<WorkflowEventTriggerMatchResult> {
  return request("/api/workflows/development/event-trigger/match", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    signal,
  })
}

export async function aiReviseWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
}): Promise<{ session: WorkflowDevelopmentSession }> {
  return request("/api/workflows/development/ai-revise", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
}

export async function validateWorkflowDevelopment(): Promise<{
  session: WorkflowDevelopmentSession
}> {
  return request("/api/workflows/development/validate", { method: "POST" })
}

export async function testWorkflowDevelopment(payload: {
  prompt?: string
  target_ref?: string
  yaml?: string
  inputs?: Record<string, unknown>
  secrets?: Record<string, string>
  session?: string
  delivery?: WorkflowDeliveryPayload
  event_id?: string
  async?: boolean
}): Promise<WorkflowDevelopmentTestResult> {
  const res = await launcherFetch("/api/workflows/development/test", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  const text = await res.text()
  if (res.ok) {
    return JSON.parse(text) as WorkflowDevelopmentTestResult
  }
  try {
    const body = JSON.parse(text) as Partial<WorkflowDevelopmentTestResult>
    if (body.session != null) {
      return {
        session: body.session,
        result: body.result,
        reconciliation: body.reconciliation,
        error:
          typeof body.error === "string" && body.error.trim() !== ""
            ? body.error
            : apiErrorMessage(text, res.status, res.statusText),
      }
    }
  } catch {
    // Fall through to the normal error message path.
  }
  throw new Error(apiErrorMessage(text, res.status, res.statusText))
}

export function workflowTriggerSimulationRequestBody(
  request: WorkflowTriggerSimulationRequest,
): string {
  const base = {
    session_id: request.session_id,
    expected_session_revision: request.expected_session_revision,
    expected_draft_revision: request.expected_draft_revision,
    prompt: request.prompt,
    target_ref: request.target_ref,
    yaml: request.yaml,
  }
  switch (request.trigger.type) {
    case "manual":
    case "workflow_call":
      return JSON.stringify({
        ...base,
        trigger: { type: request.trigger.type },
        scenario: workflowTriggerInvocationScenarioBody(
          request.scenario as WorkflowTriggerInvocationScenario,
        ),
      })
    case "schedule":
      return JSON.stringify({
        ...base,
        trigger: {
          type: request.trigger.type,
          schedule_index: request.trigger.schedule_index,
        },
        scenario: {
          scheduled_at: (request.scenario as { scheduled_at: string })
            .scheduled_at,
        },
      })
    case "channel_message":
    case "command":
      return JSON.stringify({
        ...base,
        trigger: { type: request.trigger.type },
        scenario: {
          message: (
            request.scenario as { message: WorkflowTriggerMessageEnvelope }
          ).message,
        },
      })
    case "runtime_event":
      return JSON.stringify({
        ...base,
        trigger: { type: request.trigger.type },
        scenario: {
          event: (
            request.scenario as { event: WorkflowTriggerRuntimeEventEnvelope }
          ).event,
        },
      })
    case "event":
      return JSON.stringify({
        ...base,
        trigger: { type: request.trigger.type },
        scenario: {
          event_id: (request.scenario as { event_id: string }).event_id,
        },
      })
  }
}

export function workflowTriggerSimulationIdentity(
  request: WorkflowTriggerSimulationRequest,
  response?: WorkflowTriggerSimulationResponse,
): string {
  const requestBody = workflowTriggerSimulationRequestBody(request)
  if (response == null) {
    return requestBody
  }
  return JSON.stringify([
    requestBody,
    response.review_token ?? "",
    response.simulation,
    response.review,
  ])
}

export async function simulateWorkflowDevelopmentTrigger(
  payload: WorkflowTriggerSimulationRequest,
  signal?: AbortSignal,
): Promise<WorkflowTriggerSimulationResponse> {
  const res = await launcherFetch(
    "/api/workflows/development/triggers/simulate",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: workflowTriggerSimulationRequestBody(payload),
      signal,
    },
  )
  const value = await workflowTriggerSimulationResponseValue(
    res,
    "Workflow trigger simulation is unavailable.",
  )
  return parseWorkflowTriggerSimulationResponse(value)
}

export async function executeWorkflowDevelopmentTrigger(
  payload: WorkflowTriggerSimulationRequest,
  reviewToken: string,
  signal?: AbortSignal,
): Promise<WorkflowDevelopmentTestResult> {
  const requestValue = JSON.parse(
    workflowTriggerSimulationRequestBody(payload),
  ) as Record<string, unknown>
  const res = await launcherFetch("/api/workflows/development/test/execute", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...requestValue, review_token: reviewToken }),
    signal,
  })
  const value = await workflowTriggerSimulationResponseValue(
    res,
    "Workflow trigger execution is unavailable.",
    202,
  )
  return parseWorkflowTriggerExecutionResult(value, payload.session_id)
}

export async function publishWorkflowDevelopment(
  payload: WorkflowDevelopmentPublishRequest,
): Promise<{
  workflow_ref: string
  session: WorkflowDevelopmentSession
}> {
  return requestWorkflowControl(
    "/api/workflows/development/publish",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
    "The workflow could not be published.",
  )
}

export async function discardWorkflowDevelopment(): Promise<{
  session: WorkflowDevelopmentSession
}> {
  return request("/api/workflows/development/discard", { method: "POST" })
}

export async function reloadWorkflows(): Promise<WorkflowReloadResult> {
  return normalizeWorkflowReloadResult(
    await request<WorkflowReloadResult>("/api/workflows/reload", {
      method: "POST",
    }),
  )
}

export async function runWorkflow(payload: {
  ref: string
  expected_dependency_revision: string
  inputs?: Record<string, unknown>
  secrets?: Record<string, string>
  session?: string
  delivery?: WorkflowDeliveryPayload
  async?: boolean
}): Promise<WorkflowRunLaunchResult> {
  const res = await launcherFetch("/api/workflows/run", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  return workflowRunLaunchResultFromResponse(res)
}

async function workflowRunLaunchResultFromResponse(
  res: Response,
): Promise<WorkflowRunLaunchResult> {
  const text = await res.text()
  if (res.ok) {
    return { result: JSON.parse(text) as WorkflowRunResult }
  }
  try {
    const body = JSON.parse(text) as {
      result?: WorkflowRunResult
      error?: string
    }
    if (body.result != null) {
      return {
        result: body.result,
        error:
          typeof body.error === "string" && body.error.trim() !== ""
            ? body.error
            : apiErrorMessage(text, res.status, res.statusText),
      }
    }
  } catch {
    // Fall through to the normal error message path.
  }
  throw new WorkflowAPIError(
    workflowLaunchErrorMessage(text, res.status, res.statusText),
    res.status,
  )
}

export async function listWorkflowRuns(): Promise<{ runs: WorkflowRun[] }> {
  const payload = await request<{ runs?: WorkflowRun[] | null }>(
    "/api/workflows/runs",
  )
  return { runs: arrayOrEmpty(payload.runs).map(normalizeWorkflowRun) }
}

export async function getWorkflowRun(runID: string): Promise<WorkflowRun> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const run = normalizeWorkflowRun(
    await request<WorkflowRun>(
      `/api/workflows/runs/${encodeURIComponent(runID)}`,
    ),
  )
  if (run.id !== runID) {
    throw new WorkflowAPIError(
      "The workflow service returned a mismatched run.",
      502,
    )
  }
  return run
}

export async function getWorkflowRunEvents(
  runID: string,
): Promise<{ run_id: string; events: WorkflowRunEvent[] }> {
  const payload = await request<{
    run_id: string
    events?: WorkflowRunEvent[] | null
  }>(`/api/workflows/runs/${encodeURIComponent(runID)}/events`)
  return { ...payload, events: arrayOrEmpty(payload.events) }
}

export function workflowRunEventsStreamURL(runID: string): string {
  return `/api/workflows/runs/${encodeURIComponent(runID)}/events/stream`
}

export async function getWorkflowRunGraph(
  runID: string,
): Promise<WorkflowRunGraph> {
  return normalizeWorkflowRunGraph(
    await request<WorkflowRunGraph>(
      `/api/workflows/runs/${encodeURIComponent(runID)}/graph`,
    ),
  )
}

export async function cancelWorkflowRun(
  runID: string,
  reason: string,
): Promise<WorkflowRun> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const normalizedReason = reason.trim()
  if (
    normalizedReason === "" ||
    new TextEncoder().encode(normalizedReason).byteLength >
      WORKFLOW_CANCEL_REASON_MAX_BYTES
  ) {
    throw new WorkflowAPIError(
      "Cancel reason must be between 1 and 1024 UTF-8 bytes.",
      400,
    )
  }
  const run = normalizeWorkflowRun(
    await request<WorkflowRun>(
      `/api/workflows/runs/${encodeURIComponent(runID)}/cancel`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: normalizedReason }),
      },
    ),
  )
  if (run.id !== runID) {
    throw new WorkflowAPIError(
      "The workflow service returned a mismatched run.",
      502,
    )
  }
  return run
}

export async function retryWorkflowRun(
  runID: string,
  payload: {
    expected_dependency_revision: string
    secrets?: Record<string, string>
  },
): Promise<WorkflowRunLaunchResult> {
  if (!validWorkflowRunID(runID)) {
    throw new WorkflowAPIError("Invalid workflow run identifier.", 400)
  }
  const res = await launcherFetch(
    `/api/workflows/runs/${encodeURIComponent(runID)}/retry`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
  )
  return workflowRunLaunchResultFromResponse(res)
}

function workflowLaunchErrorMessage(
  text: string,
  status: number,
  statusText: string,
) {
  const code = workflowErrorCode(text)
  switch (code) {
    case "dependency_revision_mismatch":
      return "Workflow dependencies changed. Wait for a fresh readiness check and try again."
    case "workflow_dependencies_not_ready":
      return "Resolve the workflow dependency blockers and try again."
    case "dependency_check_unavailable":
      return "Workflow dependency readiness is temporarily unavailable."
    default:
      return apiErrorMessage(text, status, statusText)
  }
}

async function requestWorkflowControl<T>(
  path: string,
  options: RequestInit | undefined,
  fallbackMessage: string,
): Promise<T> {
  const res = await launcherFetch(path, options)
  const text = await res.text()
  if (!res.ok) {
    throw new Error(workflowControlErrorMessage(text, fallbackMessage))
  }
  try {
    return JSON.parse(text) as T
  } catch {
    throw new Error(fallbackMessage)
  }
}

async function requestWorkflowInspection(
  path: string,
  options?: RequestInit,
): Promise<unknown> {
  const res = await launcherFetch(path, options)
  let text: string
  try {
    text = await boundedWorkflowInspectionResponseText(res)
  } catch {
    throw new Error(
      res.ok
        ? "Workflow definition inspection returned an invalid response."
        : "Workflow definition inspection is unavailable.",
    )
  }
  if (!res.ok) {
    throw new Error(workflowInspectionErrorMessage(text))
  }
  try {
    return JSON.parse(text) as unknown
  } catch {
    throw new Error(
      "Workflow definition inspection returned an invalid response.",
    )
  }
}

async function requestWorkflowAuthoringCapabilities(
  signal?: AbortSignal,
): Promise<unknown> {
  const res = await launcherFetch("/api/workflows/authoring/capabilities", {
    signal,
  })
  let text: string
  try {
    text = await boundedWorkflowResponseText(
      res,
      workflowAuthoringCapabilitiesResponseMaxBytes,
    )
  } catch {
    throw new Error(
      res.ok
        ? "Workflow capabilities returned an invalid response."
        : "Workflow capabilities are unavailable.",
    )
  }
  if (!res.ok) {
    throw new Error(workflowAuthoringCapabilitiesErrorMessage(text))
  }
  if (
    !workflowAuthoringCapabilitiesJSONContentType(
      res.headers.get("Content-Type"),
    )
  ) {
    throw new Error("Workflow capabilities returned an invalid response.")
  }
  try {
    return JSON.parse(text) as unknown
  } catch {
    throw new Error("Workflow capabilities returned an invalid response.")
  }
}

async function requestWorkflowJobsEditor(
  path: string,
  options: RequestInit,
): Promise<unknown> {
  const res = await launcherFetch(path, options)
  let text: string
  try {
    text = await boundedWorkflowResponseText(
      res,
      workflowJobsEditorResponseMaxBytes,
    )
  } catch {
    throw new WorkflowJobsEditorAPIError(
      res.ok
        ? "The jobs and actions editor returned an invalid response."
        : "The jobs and actions editor is unavailable.",
      res.status,
    )
  }
  if (!workflowJSONContentType(res.headers.get("Content-Type"))) {
    throw new WorkflowJobsEditorAPIError(
      res.ok
        ? "The jobs and actions editor returned an invalid response."
        : "The jobs and actions editor is unavailable.",
      res.status,
    )
  }
  let payload: unknown
  try {
    payload = JSON.parse(text) as unknown
  } catch {
    throw new WorkflowJobsEditorAPIError(
      res.ok
        ? "The jobs and actions editor returned an invalid response."
        : "The jobs and actions editor is unavailable.",
      res.status,
    )
  }
  if (res.ok) {
    return payload
  }
  let inspection: WorkflowJobsInspection | undefined
  if (
    payload != null &&
    typeof payload === "object" &&
    !Array.isArray(payload) &&
    Object.prototype.hasOwnProperty.call(payload, "inspection")
  ) {
    try {
      inspection = parseWorkflowJobsInspection(
        (payload as Record<string, unknown>).inspection,
      )
    } catch {
      // Error details are advisory; malformed details must not replace the
      // bounded, sanitized error shown to the operator.
    }
  }
  throw new WorkflowJobsEditorAPIError(
    workflowJobsEditorErrorMessage(text),
    res.status,
    inspection,
  )
}

function workflowAuthoringCapabilitiesJSONContentType(value: string | null) {
  return workflowJSONContentType(value)
}

function workflowJSONContentType(value: string | null) {
  if (value == null) {
    return false
  }
  const [mediaType, ...rawParameters] = value.split(";")
  if (mediaType.trim().toLowerCase() !== "application/json") {
    return false
  }
  if (rawParameters.length > 1) {
    return false
  }
  return rawParameters.every((rawParameter) => {
    const separator = rawParameter.indexOf("=")
    if (separator < 0) {
      return false
    }
    const name = rawParameter.slice(0, separator).trim().toLowerCase()
    const parameterValue = rawParameter
      .slice(separator + 1)
      .trim()
      .replace(/^"(.*)"$/, "$1")
      .toLowerCase()
    return name === "charset" && parameterValue === "utf-8"
  })
}

const workflowInspectionResponseMaxBytes = 32 << 20
const workflowInspectionSourceRefMaxBytes = 16 << 10
const workflowAuthoringCapabilitiesResponseMaxBytes = 4 << 20
const workflowJobsEditorResponseMaxBytes = 8 << 20
const workflowJobsEditorSourceMaxBytes = 1 << 20

async function boundedWorkflowInspectionResponseText(res: Response) {
  return boundedWorkflowResponseText(res, workflowInspectionResponseMaxBytes)
}

async function boundedWorkflowResponseText(
  res: Response,
  maximumBytes: number,
) {
  const lengthHeader = res.headers.get("Content-Length")
  if (
    lengthHeader != null &&
    /^\d+$/.test(lengthHeader) &&
    Number(lengthHeader) > maximumBytes
  ) {
    throw new Error("response too large")
  }
  if (res.body == null) {
    const text = await res.text()
    if (new TextEncoder().encode(text).byteLength > maximumBytes) {
      throw new Error("response too large")
    }
    return text
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder("utf-8", { fatal: true })
  let total = 0
  let text = ""
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) {
        return text + decoder.decode()
      }
      total += value.byteLength
      if (total > maximumBytes) {
        await reader.cancel()
        throw new Error("response too large")
      }
      text += decoder.decode(value, { stream: true })
    }
  } finally {
    reader.releaseLock()
  }
}

function workflowAuthoringCapabilitiesErrorMessage(text: string) {
  switch (workflowErrorCode(text)) {
    case "workflow_authoring_capabilities_unavailable":
      return "Workflow capabilities are temporarily unavailable."
    default:
      return "Workflow capabilities are unavailable."
  }
}

function workflowJobsEditorErrorMessage(text: string) {
  switch (workflowErrorCode(text)) {
    case "workflow_jobs_revision_mismatch":
      return "The jobs and actions source revision is stale. Refresh the current YAML and try again."
    case "workflow_jobs_raw_only":
      return "This job or step has source features that must be edited in Workflow YAML."
    case "invalid_workflow_jobs_operation":
      return "The jobs and actions change is invalid. Review the highlighted fields and try again."
    case "unsupported_workflow_jobs_operation":
      return "That jobs and actions change is not supported."
    case "invalid_workflow_jobs_request":
      return "The jobs and actions request is invalid. Refresh and try again."
    case "workflow_jobs_request_too_large":
      return "This workflow is too large for the structured jobs and actions editor."
    default:
      return "The jobs and actions editor is unavailable."
  }
}

function workflowInspectionErrorMessage(text: string) {
  switch (workflowErrorCode(text)) {
    case "invalid_definition_inspection_request":
    case "invalid_definition_inspection_content_type":
      return "The workflow inspection request is invalid. Refresh and try again."
    case "definition_inspection_request_too_large":
      return "The workflow reference is too large to inspect."
    case "workflow_definition_too_large":
      return "This workflow definition is too large to inspect safely."
    case "workflow_not_found":
      return "That published workflow is no longer available."
    case "template_not_found":
      return "That built-in workflow template is no longer available."
    case "workflow_inspection_unavailable":
      return "Workflow definition inspection is temporarily unavailable."
    default:
      return "Workflow definition inspection is unavailable."
  }
}

function workflowControlErrorMessage(text: string, fallbackMessage: string) {
  const code = workflowErrorCode(text)
  switch (code) {
    case "template_not_found":
      return "That built-in workflow template is no longer available."
    case "template_overwrite_required":
      return "This workflow has local changes. Confirm Restore built-in to replace them."
    case "template_target_blocked":
      return "The template target is blocked and must be resolved manually."
    case "template_catalog_unavailable":
      return "Built-in workflow templates are unavailable."
    case "template_revalidation_failed":
      return "The template was not installed because workflow revalidation failed."
    case "template_rollback_failed":
      return "Template recovery needs operator attention. No further changes were attempted."
    case "template_recovery_failed":
      return "Template recovery needs operator attention. No further changes were attempted."
    case "workflow_development_active":
      return "Finish or discard the active workflow draft before changing workflow definitions or templates."
    case "config_revision_mismatch":
      return "Workflow settings changed elsewhere. Reload them and try again."
    case "invalid_workflow_settings":
      return "Workflow settings are invalid. Check the directory and numeric values."
    case "dependency_request_too_large":
      return "This workflow draft is too large to check for dependencies."
    case "invalid_dependency_request":
      return "Set a valid local workflow target before checking dependencies."
    case "workflow_not_found":
      return "The workflow dependency root is no longer available."
    case "workflow_invalid":
      return "Fix workflow validation errors before checking dependencies."
    case "dependency_check_unavailable":
      return "Workflow dependency readiness is temporarily unavailable."
    case "publish_request_too_large":
    case "invalid_publish_request":
      return "The publish request is invalid. Reload the draft and try again."
    case "workflow_development_not_found":
      return "The active workflow draft is no longer available."
    case "workflow_development_busy":
      return "Another workflow change is in progress. Wait and try again."
    case "session_revision_mismatch":
    case "draft_revision_mismatch":
    case "target_revision_mismatch":
      return "The workflow draft changed elsewhere. Reload it, test it, and check dependencies again."
    case "dependency_revision_mismatch":
      return "Workflow dependencies changed. Wait for a fresh readiness check and try again."
    case "workflow_dependencies_not_ready":
    case "workflow_publish_not_ready":
      return "Resolve workflow publish blockers and run a fresh test before publishing."
    case "workflow_publish_unavailable":
      return "Workflow publishing is temporarily unavailable."
    case "workflow_publish_recovery_failed":
    case "workflow_publish_rollback_failed":
      return "Workflow publish recovery needs operator attention. No further changes were attempted."
    case "workflow_transaction_recovery_conflict":
      return "Workflow recovery found files changed outside the interrupted transaction. Operator reconciliation is required; no files were changed."
    case "workflow_publish_gate_required":
      return "Workflow publishing is unavailable until dependency enforcement is restored."
    case "workflow_publish_failed":
      return "The workflow could not be published. Reload the draft and try again."
    default:
      return fallbackMessage
  }
}

function workflowErrorCode(text: string) {
  try {
    const body = JSON.parse(text) as { error?: unknown }
    return typeof body.error === "string" ? body.error : ""
  } catch {
    return ""
  }
}

function arrayOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : []
}

function recordOrEmpty<T>(
  value: Record<string, T> | null | undefined,
): Record<string, T> {
  return value == null ? {} : value
}

const workflowInspectionValidationCodes =
  new Set<WorkflowDefinitionInspectionValidationCode>([
    "invalid_yaml",
    "jobs_required",
    "schedule_cron_required",
    "schedule_cron_invalid",
    "input_name_required",
    "input_type_unsupported",
    "input_default_invalid",
    "output_required",
    "output_expression_invalid",
    "conversation_session_unsupported",
    "conversation_delivery_unsupported",
    "channel_pattern_invalid",
    "command_name_required",
    "runtime_filter_required",
    "event_filter_required",
    "event_entity_filter_required",
    "event_pattern_required",
    "event_attribute_required",
    "job_id_required",
    "job_dependency_unknown",
    "job_dependency_cycle",
    "reusable_target_invalid",
    "reusable_steps_unsupported",
    "job_runner_required",
    "job_steps_required",
    "step_id_duplicate",
    "step_target_required",
    "reusable_step_unsupported",
    "step_target_unsupported",
    "run_session_unsupported",
    "run_delivery_unsupported",
    "agent_history_unsupported",
    "agent_cache_unsupported",
    "agent_tools_unsupported",
    "definition_invalid",
  ])
const workflowInspectionValidationScopes =
  new Set<WorkflowDefinitionInspectionValidationScope>([
    "workflow",
    "jobs",
    "trigger.manual",
    "trigger.schedule",
    "trigger.channel_message",
    "trigger.command",
    "trigger.runtime_event",
    "trigger.event",
    "trigger.workflow_call",
  ])
const workflowInspectionJobKinds = new Set(["steps", "reusable"] as const)
const workflowInspectionStepKinds = new Set([
  "agent",
  "tool",
  "mcp",
  "function",
  "unknown",
] as const)
const workflowInspectionDependencyKinds = new Set<WorkflowDependencyKind>([
  "agent",
  "tool",
  "mcp",
  "function",
  "reusable",
])
const workflowInspectionEffectKinds = new Set<
  WorkflowDefinitionInspectionEffect["kind"]
>([
  "model_or_delegated_action_possible",
  "state_change_possible",
  "external_state_change_possible",
  "transitive_effects_unknown",
  "unclassified_action",
])
const workflowInspectionLimitKinds = new Set<
  WorkflowDefinitionInspection["limits"][number]
>([
  "jobs_truncated",
  "steps_truncated",
  "dependencies_truncated",
  "effects_truncated",
  "triggers_truncated",
  "unsafe_fields_omitted",
  "validation_issues_truncated",
])
const workflowCapabilityReadinessValues = new Set<WorkflowCapabilityReadiness>([
  "ready",
  "unchecked",
  "not_configured",
  "disabled",
  "not_allowed",
  "not_connected",
  "not_found",
  "invalid_configuration",
  "name_collision",
  "unavailable",
])
const workflowCapabilityParameterTypes =
  new Set<WorkflowCapabilityParameterType>([
    "object",
    "array",
    "string",
    "number",
    "integer",
    "boolean",
    "null",
  ])
const workflowAuthoringCapabilityLimitValues =
  new Set<WorkflowAuthoringCapabilityLimit>([
    "agents_truncated",
    "tools_truncated",
    "mcp_tools_truncated",
    "functions_truncated",
    "parameter_shapes_omitted",
    "unsafe_fields_omitted",
  ])
const workflowNativeFunctionNames = new Set([
  "git.diff",
  "git.filter",
  "git.inventory",
  "workflow.artifact",
  "workflow.state",
])

const workflowCapabilityBounds = {
  agents: 128,
  tools: 256,
  mcpTools: 256,
  functions: 5,
  identityBytes: 256,
  targetBytes: 1024,
  schemaDepth: 6,
  schemaProperties: 128,
  schemaEnum: 64,
  schemaUnits: 4096,
  schemaStringBytes: 256,
} as const

const workflowJobEditorLimitValues = new Set<WorkflowJobEditorLimit>([
  "jobs_truncated",
  "steps_truncated",
  "unsafe_fields_omitted",
  "validation_truncated",
])

const workflowJobEditorJobFieldNames = [
  "name",
  "runs_on",
  "needs",
  "uses",
  "if",
  "continue_on_error",
  "with",
  "secrets",
  "outputs",
  "context",
] as const

const workflowJobEditorStepFieldNames = [
  "id",
  "name",
  "uses",
  "if",
  "continue_on_error",
  "with",
  "context",
] as const

const workflowJobEditorBounds = {
  jobs: 256,
  steps: 4096,
  fieldStringBytes: 16 << 10,
  identityBytes: 256,
  reasonBytes: 16 << 10,
  issues: 1024,
  issueBytes: 16 << 10,
  listEntries: 4096,
  objectEntries: 4096,
  jsonDepth: 16,
  jsonEntries: 4096,
  jsonValueBytes: 256 << 10,
} as const

function parseWorkflowJobsInspection(value: unknown): WorkflowJobsInspection {
  return parseWorkflowJobsEnvelope(value, false)
}

function parseWorkflowJobsRenderResult(
  value: unknown,
): WorkflowJobsRenderResult {
  return parseWorkflowJobsEnvelope(value, true)
}

function parseWorkflowJobsEnvelope(
  value: unknown,
  render: false,
): WorkflowJobsInspection
function parseWorkflowJobsEnvelope(
  value: unknown,
  render: true,
): WorkflowJobsRenderResult
function parseWorkflowJobsEnvelope(
  value: unknown,
  render: boolean,
): WorkflowJobsInspection | WorkflowJobsRenderResult {
  const invalid = (): never => {
    throw new Error("The jobs and actions editor returned an invalid response.")
  }
  const allowedRootFields = [
    "revision",
    "editable",
    "reason",
    "complete",
    "limits",
    "jobs",
    "validation",
    ...(render ? ["yaml"] : []),
  ]
  const root = inspectionObject(value, allowedRootFields, invalid)
  const requiredRootFields = [
    "revision",
    "editable",
    "complete",
    "limits",
    "jobs",
    "validation",
    ...(render ? ["yaml"] : []),
  ]
  if (
    requiredRootFields.some((field) => !hasInspectionField(root, field)) ||
    (!render && hasInspectionField(root, "yaml"))
  ) {
    return invalid()
  }

  const jobIDs = new Set<string>()
  let totalSteps = 0
  const jobs = inspectionArray(
    root.jobs,
    workflowJobEditorBounds.jobs,
    invalid,
  ).map((jobValue, expectedIndex) => {
    const job = inspectionObject(
      jobValue,
      [
        "id",
        "index",
        "editable",
        "reason",
        "advanced_fields_present",
        "steps_present",
        "fields",
        "steps",
      ],
      invalid,
    )
    if (
      [
        "id",
        "index",
        "editable",
        "advanced_fields_present",
        "steps_present",
        "fields",
        "steps",
      ].some((field) => !hasInspectionField(job, field))
    ) {
      return invalid()
    }
    const id = workflowJobEditorIdentity(job.id, invalid)
    if (jobIDs.has(id)) {
      return invalid()
    }
    jobIDs.add(id)
    const index = inspectionInteger(job.index, invalid)
    if (index !== expectedIndex) {
      return invalid()
    }
    const steps = inspectionArray(
      job.steps,
      workflowJobEditorBounds.steps,
      invalid,
    ).map((stepValue, expectedStepIndex) => {
      totalSteps += 1
      if (totalSteps > workflowJobEditorBounds.steps) {
        return invalid()
      }
      const step = inspectionObject(
        stepValue,
        ["index", "editable", "reason", "advanced_fields_present", "fields"],
        invalid,
      )
      if (
        ["index", "editable", "advanced_fields_present", "fields"].some(
          (field) => !hasInspectionField(step, field),
        )
      ) {
        return invalid()
      }
      const stepIndex = inspectionInteger(step.index, invalid)
      if (stepIndex !== expectedStepIndex) {
        return invalid()
      }
      return {
        index: stepIndex,
        editable: inspectionBoolean(step.editable, invalid),
        ...(hasInspectionField(step, "reason")
          ? {
              reason: workflowJobEditorString(step.reason, invalid, {
                maximumBytes: workflowJobEditorBounds.reasonBytes,
              }),
            }
          : {}),
        advanced_fields_present: inspectionBoolean(
          step.advanced_fields_present,
          invalid,
        ),
        fields: parseWorkflowJobEditorStepFields(step.fields, invalid),
      }
    })
    const stepsPresent = inspectionBoolean(job.steps_present, invalid)
    if (!stepsPresent && steps.length !== 0) {
      return invalid()
    }
    return {
      id,
      index,
      editable: inspectionBoolean(job.editable, invalid),
      ...(hasInspectionField(job, "reason")
        ? {
            reason: workflowJobEditorString(job.reason, invalid, {
              maximumBytes: workflowJobEditorBounds.reasonBytes,
            }),
          }
        : {}),
      advanced_fields_present: inspectionBoolean(
        job.advanced_fields_present,
        invalid,
      ),
      steps_present: stepsPresent,
      fields: parseWorkflowJobEditorJobFields(job.fields, invalid),
      steps,
    }
  })

  let previousLimit: string | undefined
  const limits = inspectionArray(
    root.limits,
    workflowJobEditorLimitValues.size,
    invalid,
  ).map((limitValue) => {
    const limit = inspectionEnum(
      limitValue,
      workflowJobEditorLimitValues,
      invalid,
    )
    if (
      previousLimit != null &&
      compareInspectionUTF8(previousLimit, limit) >= 0
    ) {
      return invalid()
    }
    previousLimit = limit
    return limit
  })
  const complete = inspectionBoolean(root.complete, invalid)
  if (complete !== (limits.length === 0)) {
    return invalid()
  }

  const inspection: WorkflowJobsInspection = {
    revision: workflowJobEditorString(root.revision, invalid, {
      maximumBytes: 256,
    }),
    editable: inspectionBoolean(root.editable, invalid),
    ...(hasInspectionField(root, "reason")
      ? {
          reason: workflowJobEditorString(root.reason, invalid, {
            maximumBytes: workflowJobEditorBounds.reasonBytes,
          }),
        }
      : {}),
    complete,
    limits,
    jobs,
    validation: parseWorkflowJobEditorValidation(root.validation, invalid),
  }
  if (!render) {
    return inspection
  }
  return {
    ...inspection,
    yaml: workflowJobEditorString(root.yaml, invalid, {
      allowEmpty: true,
      maximumBytes: workflowJobsEditorResponseMaxBytes,
      allowFormattingControls: true,
    }),
  }
}

function parseWorkflowJobEditorJobFields(
  value: unknown,
  invalid: () => never,
): WorkflowJobEditorJobFields {
  const fields = workflowJobEditorFields(
    value,
    workflowJobEditorJobFieldNames,
    invalid,
  )
  return {
    name: workflowJobEditorField(fields.name, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    runs_on: workflowJobEditorField(fields.runs_on, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    needs: workflowJobEditorField(fields.needs, invalid, (fieldValue) =>
      workflowJobEditorIDReferenceArray(fieldValue, invalid),
    ),
    uses: workflowJobEditorField(fields.uses, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    if: workflowJobEditorField(fields.if, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    continue_on_error: workflowJobEditorField(
      fields.continue_on_error,
      invalid,
      (fieldValue) => inspectionBoolean(fieldValue, invalid),
    ),
    with: workflowJobEditorField(fields.with, invalid, (fieldValue) =>
      workflowJobEditorDynamicJSONObject(fieldValue, invalid),
    ),
    secrets: workflowJobEditorField(fields.secrets, invalid, (fieldValue) => {
      if (fieldValue === "inherit") {
        return fieldValue
      }
      return workflowJobEditorDynamicJSONObject(fieldValue, invalid)
    }),
    outputs: workflowJobEditorField(fields.outputs, invalid, (fieldValue) =>
      workflowJobEditorStringRecord(fieldValue, invalid),
    ),
    context: workflowJobEditorField(fields.context, invalid, (fieldValue) =>
      workflowJobEditorContext(fieldValue, invalid),
    ),
  }
}

function parseWorkflowJobEditorStepFields(
  value: unknown,
  invalid: () => never,
): WorkflowJobEditorStepFields {
  const fields = workflowJobEditorFields(
    value,
    workflowJobEditorStepFieldNames,
    invalid,
  )
  return {
    id: workflowJobEditorField(fields.id, invalid, (fieldValue) =>
      workflowJobEditorIDReference(fieldValue, invalid),
    ),
    name: workflowJobEditorField(fields.name, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    uses: workflowJobEditorField(fields.uses, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    if: workflowJobEditorField(fields.if, invalid, (fieldValue) =>
      workflowJobEditorFieldString(fieldValue, invalid),
    ),
    continue_on_error: workflowJobEditorField(
      fields.continue_on_error,
      invalid,
      (fieldValue) => inspectionBoolean(fieldValue, invalid),
    ),
    with: workflowJobEditorField(fields.with, invalid, (fieldValue) =>
      workflowJobEditorDynamicJSONObject(fieldValue, invalid),
    ),
    context: workflowJobEditorField(fields.context, invalid, (fieldValue) =>
      workflowJobEditorContext(fieldValue, invalid),
    ),
  }
}

function workflowJobEditorFields(
  value: unknown,
  fieldNames: readonly string[],
  invalid: () => never,
) {
  const fields = inspectionObject(value, fieldNames, invalid)
  if (
    Object.keys(fields).length !== fieldNames.length ||
    fieldNames.some((field) => !hasInspectionField(fields, field))
  ) {
    return invalid()
  }
  return fields
}

function workflowJobEditorField<Value>(
  value: unknown,
  invalid: () => never,
  parse: (value: unknown) => Value,
): WorkflowEditorField<Value> {
  const field = inspectionObject(value, ["present", "value"], invalid)
  if (
    !hasInspectionField(field, "present") ||
    !hasInspectionField(field, "value")
  ) {
    return invalid()
  }
  const present = inspectionBoolean(field.present, invalid)
  if (!present) {
    if (field.value !== null) {
      return invalid()
    }
    return { present: false, value: null }
  }
  return { present: true, value: parse(field.value) }
}

function workflowJobEditorFieldString(value: unknown, invalid: () => never) {
  return workflowJobEditorString(value, invalid, {
    allowEmpty: true,
    maximumBytes: workflowJobEditorBounds.fieldStringBytes,
    allowFormattingControls: true,
  })
}

function workflowJobEditorIDReferenceArray(
  value: unknown,
  invalid: () => never,
) {
  return inspectionArray(
    value,
    workflowJobEditorBounds.listEntries,
    invalid,
  ).map((entry) => workflowJobEditorIDReference(entry, invalid))
}

function workflowJobEditorStringRecord(value: unknown, invalid: () => never) {
  const record = inspectionObject(value, undefined, invalid)
  if (Object.keys(record).length > workflowJobEditorBounds.objectEntries) {
    return invalid()
  }
  return Object.fromEntries(
    Object.entries(record).map(([key, entryValue]) => [
      workflowJobEditorIdentity(key, invalid),
      workflowJobEditorFieldString(entryValue, invalid),
    ]),
  )
}

function workflowJobEditorContext(
  value: unknown,
  invalid: () => never,
): WorkflowJobEditorContext {
  const context = inspectionObject(value, ["session", "delivery"], invalid)
  return {
    ...(hasInspectionField(context, "session")
      ? { session: workflowJobEditorFieldString(context.session, invalid) }
      : {}),
    ...(hasInspectionField(context, "delivery")
      ? { delivery: workflowJobEditorFieldString(context.delivery, invalid) }
      : {}),
  }
}

function workflowJobEditorJSONObject(
  value: unknown,
  depth: number,
  budget: { remaining: number },
  invalid: () => never,
): Record<string, WorkflowEditorJSONValue> {
  const parsed = workflowJobEditorJSONValue(value, depth, budget, invalid)
  return parsed != null && typeof parsed === "object" && !Array.isArray(parsed)
    ? parsed
    : invalid()
}

function workflowJobEditorDynamicJSONObject(
  value: unknown,
  invalid: () => never,
) {
  let encoded: string
  try {
    encoded = JSON.stringify(value)
  } catch {
    return invalid()
  }
  if (
    typeof encoded !== "string" ||
    new TextEncoder().encode(encoded).byteLength >
      workflowJobEditorBounds.jsonValueBytes
  ) {
    return invalid()
  }
  return workflowJobEditorJSONObject(
    value,
    1,
    { remaining: workflowJobEditorBounds.jsonEntries },
    invalid,
  )
}

function workflowJobEditorJSONValue(
  value: unknown,
  depth: number,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowEditorJSONValue {
  budget.remaining -= 1
  if (budget.remaining < 0) {
    return invalid()
  }
  if (depth > workflowJobEditorBounds.jsonDepth) {
    return invalid()
  }
  if (value === null || typeof value === "boolean") {
    return value
  }
  if (typeof value === "number") {
    return Number.isFinite(value) &&
      (!Number.isInteger(value) || Number.isSafeInteger(value))
      ? value
      : invalid()
  }
  if (typeof value === "string") {
    return workflowJobEditorFieldString(value, invalid)
  }
  if (Array.isArray(value)) {
    if (value.length > workflowJobEditorBounds.listEntries) {
      return invalid()
    }
    return value.map((entry) =>
      workflowJobEditorJSONValue(entry, depth + 1, budget, invalid),
    )
  }
  const record = inspectionObject(value, undefined, invalid)
  const entries = Object.entries(record)
  if (entries.length > workflowJobEditorBounds.objectEntries) {
    return invalid()
  }
  return Object.fromEntries(
    entries.map(([key, entryValue]) => [
      workflowJobEditorString(key, invalid, {
        maximumBytes: workflowJobEditorBounds.identityBytes,
      }),
      workflowJobEditorJSONValue(entryValue, depth + 1, budget, invalid),
    ]),
  )
}

function parseWorkflowJobEditorValidation(
  value: unknown,
  invalid: () => never,
): WorkflowDevelopmentValidation {
  const validation = inspectionObject(
    value,
    ["valid", "errors", "warnings", "validated_at"],
    invalid,
  )
  if (
    !hasInspectionField(validation, "valid") ||
    !hasInspectionField(validation, "validated_at")
  ) {
    return invalid()
  }
  const errors = hasInspectionField(validation, "errors")
    ? workflowJobEditorValidationIssues(validation.errors, invalid)
    : undefined
  const warnings = hasInspectionField(validation, "warnings")
    ? workflowJobEditorValidationIssues(validation.warnings, invalid)
    : undefined
  const valid = inspectionBoolean(validation.valid, invalid)
  if (valid && (errors?.length ?? 0) !== 0) {
    return invalid()
  }
  return {
    valid,
    ...(errors != null ? { errors } : {}),
    ...(warnings != null ? { warnings } : {}),
    validated_at: workflowJobEditorString(validation.validated_at, invalid, {
      maximumBytes: 256,
    }),
  }
}

function workflowJobEditorValidationIssues(
  value: unknown,
  invalid: () => never,
) {
  return inspectionArray(value, workflowJobEditorBounds.issues, invalid).map(
    (issueValue) => {
      const issue = inspectionObject(issueValue, ["path", "message"], invalid)
      if (!hasInspectionField(issue, "message")) {
        return invalid()
      }
      return {
        ...(hasInspectionField(issue, "path")
          ? {
              path: workflowJobEditorString(issue.path, invalid, {
                allowEmpty: true,
                maximumBytes: workflowJobEditorBounds.issueBytes,
              }),
            }
          : {}),
        message: workflowJobEditorString(issue.message, invalid, {
          maximumBytes: workflowJobEditorBounds.issueBytes,
          allowFormattingControls: true,
        }),
      }
    },
  )
}

function workflowJobEditorIdentity(value: unknown, invalid: () => never) {
  return workflowJobEditorString(value, invalid, {
    maximumBytes: workflowJobEditorBounds.identityBytes,
  })
}

function workflowJobEditorIDReference(value: unknown, invalid: () => never) {
  return workflowJobEditorString(value, invalid, {
    allowEmpty: true,
    maximumBytes: workflowJobEditorBounds.identityBytes,
  })
}

function workflowJobEditorOperationIDsValid(
  operation: WorkflowJobEditorOperation,
) {
  if (!workflowJobEditorRequestIDValid(operation.job_id, false, true)) {
    return false
  }
  if (
    operation.type === "job.patch" &&
    operation.new_job_id != null &&
    !workflowJobEditorRequestIDValid(operation.new_job_id.value, false, true)
  ) {
    return false
  }
  if (
    (operation.type === "step.insert" || operation.type === "step.patch") &&
    operation.fields.id?.mode === "set" &&
    !workflowJobEditorRequestIDValid(operation.fields.id.value, true)
  ) {
    return false
  }
  if (
    (operation.type === "job.insert" || operation.type === "job.patch") &&
    operation.fields.needs?.mode === "set" &&
    !operation.fields.needs.value.every((value) =>
      workflowJobEditorRequestIDValid(value, false, true),
    )
  ) {
    return false
  }
  return true
}

function workflowJobEditorOperationValuesValid(
  operation: WorkflowJobEditorOperation,
) {
  if (!("fields" in operation)) {
    return true
  }
  const step = operation.type.startsWith("step.")
  const insert =
    operation.type === "job.insert" || operation.type === "step.insert"
  const fields = operation.fields as Record<string, unknown>
  return Object.entries(fields).every(([field, candidate]) => {
    if (
      candidate == null ||
      typeof candidate !== "object" ||
      Array.isArray(candidate)
    ) {
      return false
    }
    const mutation = candidate as Record<string, unknown>
    const keys = Object.keys(mutation)
    if (mutation.mode === "remove") {
      return !insert && keys.length === 1 && keys[0] === "mode"
    }
    if (
      mutation.mode !== "set" ||
      keys.length !== 2 ||
      !keys.includes("mode") ||
      !keys.includes("value")
    ) {
      return false
    }
    return workflowJobEditorMutationValueValid(field, mutation.value, step)
  })
}

function workflowJobEditorMutationValueValid(
  field: string,
  value: unknown,
  step: boolean,
) {
  switch (field) {
    case "id":
      return (
        step &&
        typeof value === "string" &&
        workflowJobEditorRequestIDValid(value, true)
      )
    case "name":
    case "if":
      return workflowJobEditorMutationStringValid(value, true)
    case "runs_on":
      return !step && workflowJobEditorMutationStringValid(value, true)
    case "uses":
      return workflowJobEditorUsesValid(value, step)
    case "continue_on_error":
      return typeof value === "boolean"
    case "needs":
      return (
        !step &&
        Array.isArray(value) &&
        value.length <= workflowJobEditorBounds.listEntries &&
        value.every(
          (item) =>
            typeof item === "string" &&
            workflowJobEditorRequestIDValid(item, false, true),
        )
      )
    case "with":
      return workflowJobEditorDynamicJSONObjectValid(value)
    case "secrets":
      return (
        !step &&
        (value === "inherit" || workflowJobEditorDynamicJSONObjectValid(value))
      )
    case "outputs":
      return !step && workflowJobEditorStringRecordValid(value)
    case "context":
      return workflowJobEditorContextMutationValid(value)
    default:
      return false
  }
}

function workflowJobEditorMutationStringValid(
  value: unknown,
  multiline: boolean,
) {
  if (
    typeof value !== "string" ||
    new TextEncoder().encode(value).byteLength >
      workflowJobEditorBounds.fieldStringBytes
  ) {
    return false
  }
  for (const character of value) {
    if (/[\p{Cf}\p{Cs}]/u.test(character)) {
      return false
    }
    if (
      /\p{Cc}/u.test(character) &&
      character !== "\t" &&
      character !== "\n" &&
      character !== "\r"
    ) {
      return false
    }
    if (!multiline && (character === "\n" || character === "\r")) {
      return false
    }
  }
  return true
}

function workflowJobEditorMutationKeyValid(value: string) {
  return (
    value !== "" &&
    new TextEncoder().encode(value).byteLength <=
      workflowJobEditorBounds.identityBytes &&
    !/[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)
  )
}

function workflowJobEditorUsesValid(value: unknown, step: boolean) {
  if (
    !workflowJobEditorMutationStringValid(value, false) ||
    typeof value !== "string"
  ) {
    return false
  }
  if (value === "") {
    return true
  }
  if (value.trim() !== value) {
    return false
  }
  if (step) {
    return ["agent/", "tool/", "mcp/", "function/"].some(
      (prefix) =>
        value.startsWith(prefix) && value.slice(prefix.length).trim() !== "",
    )
  }
  return workflowJobEditorCanonicalLocalRefValid(value)
}

function workflowJobEditorCanonicalLocalRefValid(value: string) {
  if (
    value.startsWith("/") ||
    value.startsWith("./") ||
    !value.startsWith("workflows/")
  ) {
    return false
  }
  const parts = value.split("/")
  if (parts.some((part) => part === "" || part === "." || part === "..")) {
    return false
  }
  const finalPart = parts[parts.length - 1].toLowerCase()
  return finalPart.endsWith(".yml") || finalPart.endsWith(".yaml")
}

function workflowJobEditorDynamicJSONObjectValid(value: unknown) {
  return (
    workflowJobEditorPlainObject(value) &&
    workflowJobEditorJSONValueValid(value)
  )
}

function workflowJobEditorJSONValueValid(value: unknown) {
  let encoded: string | undefined
  try {
    encoded = JSON.stringify(value)
  } catch {
    return false
  }
  if (
    encoded == null ||
    workflowJobEditorGoJSONEncodedBytes(encoded) >
      workflowJobEditorBounds.jsonValueBytes
  ) {
    return false
  }

  let entries = 0
  const visit = (candidate: unknown, depth: number): boolean => {
    if (depth > workflowJobEditorBounds.jsonDepth) {
      return false
    }
    entries += 1
    if (entries > workflowJobEditorBounds.jsonEntries) {
      return false
    }
    if (
      candidate === null ||
      typeof candidate === "boolean" ||
      workflowJobEditorMutationStringValid(candidate, true)
    ) {
      return true
    }
    if (typeof candidate === "number") {
      return (
        Number.isFinite(candidate) &&
        (!Number.isInteger(candidate) || Number.isSafeInteger(candidate))
      )
    }
    if (Array.isArray(candidate)) {
      return candidate.every((item) => visit(item, depth + 1))
    }
    if (!workflowJobEditorPlainObject(candidate)) {
      return false
    }
    return Object.entries(candidate).every(
      ([key, item]) =>
        workflowJobEditorMutationKeyValid(key) && visit(item, depth + 1),
    )
  }
  return visit(value, 0)
}

function workflowJobEditorGoJSONEncodedBytes(encoded: string) {
  let bytes = new TextEncoder().encode(encoded).byteLength
  for (const character of encoded) {
    if (character === "<" || character === ">" || character === "&") {
      bytes += 5
    } else if (character === "\u2028" || character === "\u2029") {
      bytes += 3
    }
  }
  return bytes
}

function workflowJobEditorStringRecordValid(value: unknown) {
  return (
    workflowJobEditorPlainObject(value) &&
    Object.keys(value).length <= workflowJobEditorBounds.objectEntries &&
    Object.entries(value).every(
      ([key, item]) =>
        workflowJobEditorMutationKeyValid(key) &&
        workflowJobEditorMutationStringValid(item, true),
    )
  )
}

function workflowJobEditorContextMutationValid(value: unknown) {
  return (
    workflowJobEditorPlainObject(value) &&
    Object.entries(value).every(
      ([key, item]) =>
        (key === "session" || key === "delivery") &&
        workflowJobEditorMutationStringValid(item, true),
    )
  )
}

function workflowJobEditorPlainObject(
  value: unknown,
): value is Record<string, unknown> {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return false
  }
  const prototype = Object.getPrototypeOf(value) as unknown
  return prototype === Object.prototype || prototype === null
}

function workflowJobEditorRequestIDValid(
  value: string,
  allowEmpty: boolean,
  requireTrimmedValue = false,
) {
  return (
    (allowEmpty || value !== "") &&
    (!requireTrimmedValue || (value.trim() !== "" && value.trim() === value)) &&
    new TextEncoder().encode(value).byteLength <=
      workflowJobEditorBounds.identityBytes &&
    !/[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)
  )
}

function workflowJobEditorString(
  value: unknown,
  invalid: () => never,
  {
    allowEmpty = false,
    maximumBytes,
    allowFormattingControls = false,
  }: {
    allowEmpty?: boolean
    maximumBytes: number
    allowFormattingControls?: boolean
  },
) {
  if (
    typeof value !== "string" ||
    (!allowEmpty && value === "") ||
    new TextEncoder().encode(value).byteLength > maximumBytes ||
    value.includes("\u0000") ||
    (!allowFormattingControls && /[\p{Cc}\p{Cf}\p{Cs}]/u.test(value)) ||
    (allowFormattingControls && workflowJobEditorUnsafeControl(value))
  ) {
    return invalid()
  }
  return value
}

function workflowJobEditorUnsafeControl(value: string) {
  for (const character of value) {
    if (/[\p{Cf}\p{Cs}]/u.test(character)) {
      return true
    }
    if (
      /\p{Cc}/u.test(character) &&
      character !== "\t" &&
      character !== "\n" &&
      character !== "\r"
    ) {
      return true
    }
  }
  return false
}

function workflowJobsEditorRequestBody(payload: unknown) {
  const body = JSON.stringify(payload)
  if (
    new TextEncoder().encode(body).byteLength > workflowJobsEditorSourceMaxBytes
  ) {
    throw new WorkflowJobsEditorAPIError(
      "This workflow is too large for the structured jobs and actions editor.",
      413,
    )
  }
  return body
}

function parseWorkflowAuthoringCapabilities(
  value: unknown,
): WorkflowAuthoringCapabilities {
  const invalid = (): never => {
    throw new Error("Workflow capabilities returned an invalid response.")
  }
  const root = inspectionObject(
    value,
    [
      "complete",
      "mcp_status",
      "agents",
      "tools",
      "mcp_tools",
      "functions",
      "limits",
    ],
    invalid,
  )
  const schemaBudget = { remaining: workflowCapabilityBounds.schemaUnits }
  const targets = new Set<string>()
  let defaultAgentSeen = false

  const agents = parseSortedWorkflowCapabilities(
    root.agents,
    workflowCapabilityBounds.agents,
    (itemValue) => {
      const item = inspectionObject(
        itemValue,
        ["id", "target", "is_default", "readiness"],
        invalid,
      )
      const id = workflowCapabilityAgentIdentity(item.id, invalid)
      const target = workflowCapabilityTarget(
        item.target,
        `agent/${id}`,
        invalid,
      )
      const isDefault = inspectionBoolean(item.is_default, invalid)
      if (isDefault && defaultAgentSeen) {
        return invalid()
      }
      defaultAgentSeen ||= isDefault
      addWorkflowCapabilityTarget(targets, target, invalid)
      return {
        sortKey: id,
        value: {
          id,
          target,
          is_default: isDefault,
          readiness: inspectionEnum(
            item.readiness,
            workflowCapabilityReadinessValues,
            invalid,
          ),
        },
      }
    },
    invalid,
  )

  let parameterShapeOmitted = false
  const tools = parseSortedWorkflowCapabilities(
    root.tools,
    workflowCapabilityBounds.tools,
    (itemValue) => {
      const item = inspectionObject(
        itemValue,
        [
          "name",
          "target",
          "readiness",
          "parameter_shape_projected",
          "parameter_shape",
        ],
        invalid,
      )
      const name = workflowCapabilityIdentity(item.name, invalid)
      if (name.toLowerCase() === "workflow") {
        return invalid()
      }
      const target = workflowCapabilityTarget(
        item.target,
        `tool/${name}`,
        invalid,
      )
      addWorkflowCapabilityTarget(targets, target, invalid)
      const parameterShapeProjected = inspectionBoolean(
        item.parameter_shape_projected,
        invalid,
      )
      const hasShape = hasInspectionField(item, "parameter_shape")
      if (parameterShapeProjected !== hasShape) {
        return invalid()
      }
      const readiness = inspectionEnum(
        item.readiness,
        workflowCapabilityReadinessValues,
        invalid,
      )
      if (readiness !== "ready") {
        return invalid()
      }
      parameterShapeOmitted ||= !parameterShapeProjected
      return {
        sortKey: name,
        value: {
          name,
          target,
          readiness,
          parameter_shape_projected: parameterShapeProjected,
          ...(hasShape
            ? {
                parameter_shape: parseWorkflowCapabilityParameterShape(
                  item.parameter_shape,
                  1,
                  schemaBudget,
                  invalid,
                ),
              }
            : {}),
        },
      }
    },
    invalid,
  )

  const mcpTools = parseSortedWorkflowCapabilities(
    root.mcp_tools,
    workflowCapabilityBounds.mcpTools,
    (itemValue) => {
      const item = inspectionObject(
        itemValue,
        [
          "server",
          "tool",
          "target",
          "readiness",
          "parameter_shape_projected",
          "parameter_shape",
        ],
        invalid,
      )
      const server = workflowCapabilityIdentity(item.server, invalid)
      if (server.includes("/")) {
        return invalid()
      }
      const tool = workflowCapabilityIdentity(item.tool, invalid)
      if (tool.includes("/")) {
        return invalid()
      }
      const target = workflowCapabilityTarget(
        item.target,
        `mcp/${server}/${tool}`,
        invalid,
      )
      addWorkflowCapabilityTarget(targets, target, invalid)
      const parameterShapeProjected = inspectionBoolean(
        item.parameter_shape_projected,
        invalid,
      )
      const hasShape = hasInspectionField(item, "parameter_shape")
      if (parameterShapeProjected !== hasShape) {
        return invalid()
      }
      const readiness = inspectionEnum(
        item.readiness,
        workflowCapabilityReadinessValues,
        invalid,
      )
      if (readiness !== "ready") {
        return invalid()
      }
      parameterShapeOmitted ||= !parameterShapeProjected
      return {
        sortKey: `${server}\u0000${tool}`,
        value: {
          server,
          tool,
          target,
          readiness,
          parameter_shape_projected: parameterShapeProjected,
          ...(hasShape
            ? {
                parameter_shape: parseWorkflowCapabilityParameterShape(
                  item.parameter_shape,
                  1,
                  schemaBudget,
                  invalid,
                ),
              }
            : {}),
        },
      }
    },
    invalid,
  )

  const functions = parseSortedWorkflowCapabilities(
    root.functions,
    workflowCapabilityBounds.functions,
    (itemValue) => {
      const item = inspectionObject(
        itemValue,
        ["name", "target", "readiness"],
        invalid,
      )
      const name = workflowCapabilityIdentity(item.name, invalid)
      if (!workflowNativeFunctionNames.has(name)) {
        return invalid()
      }
      const target = workflowCapabilityTarget(
        item.target,
        `function/${name}`,
        invalid,
      )
      addWorkflowCapabilityTarget(targets, target, invalid)
      const readiness = inspectionEnum(
        item.readiness,
        workflowCapabilityReadinessValues,
        invalid,
      )
      if (readiness !== "ready") {
        return invalid()
      }
      return {
        sortKey: name,
        value: {
          name,
          target,
          readiness,
        },
      }
    },
    invalid,
  )

  const limits = parseSortedWorkflowCapabilities(
    root.limits,
    workflowAuthoringCapabilityLimitValues.size,
    (limitValue) => {
      const limit = inspectionEnum(
        limitValue,
        workflowAuthoringCapabilityLimitValues,
        invalid,
      )
      return { sortKey: limit, value: limit }
    },
    invalid,
  )
  const complete = inspectionBoolean(root.complete, invalid)
  const mcpStatus = inspectionEnum(
    root.mcp_status,
    new Set(["ready", "disabled", "unavailable"] as const),
    invalid,
  )
  const hasParameterShapeLimit = limits.includes("parameter_shapes_omitted")
  if (
    complete !== (limits.length === 0 && mcpStatus !== "unavailable") ||
    (mcpStatus !== "ready" && mcpTools.length !== 0) ||
    parameterShapeOmitted !== hasParameterShapeLimit ||
    !defaultAgentSeen ||
    (limits.includes("agents_truncated") &&
      agents.length !== workflowCapabilityBounds.agents) ||
    (limits.includes("tools_truncated") &&
      tools.length !== workflowCapabilityBounds.tools) ||
    (limits.includes("mcp_tools_truncated") &&
      mcpTools.length !== workflowCapabilityBounds.mcpTools) ||
    (limits.includes("functions_truncated") &&
      functions.length !== workflowCapabilityBounds.functions) ||
    (!limits.includes("functions_truncated") &&
      functions.length !== workflowCapabilityBounds.functions)
  ) {
    return invalid()
  }

  return {
    complete,
    mcp_status: mcpStatus,
    agents,
    tools,
    mcp_tools: mcpTools,
    functions,
    limits,
  }
}

function parseSortedWorkflowCapabilities<Value>(
  value: unknown,
  maximum: number,
  parse: (item: unknown) => { sortKey: string; value: Value },
  invalid: () => never,
): Value[] {
  let previousKey: string | undefined
  return inspectionArray(value, maximum, invalid).map((item) => {
    const parsed = parse(item)
    if (
      previousKey != null &&
      compareInspectionUTF8(previousKey, parsed.sortKey) >= 0
    ) {
      return invalid()
    }
    previousKey = parsed.sortKey
    return parsed.value
  })
}

function workflowCapabilityIdentity(
  value: unknown,
  invalid: () => never,
): string {
  const identity = inspectionString(value, invalid, {
    maximumBytes: workflowCapabilityBounds.identityBytes,
  })
  return identity === identity.trim() ? identity : invalid()
}

const workflowCapabilityAgentIdentityPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

function workflowCapabilityAgentIdentity(
  value: unknown,
  invalid: () => never,
): string {
  const identity = workflowCapabilityIdentity(value, invalid)
  return workflowCapabilityAgentIdentityPattern.test(identity)
    ? identity
    : invalid()
}

function workflowCapabilityTarget(
  value: unknown,
  expected: string,
  invalid: () => never,
) {
  const target = inspectionString(value, invalid, {
    maximumBytes: workflowCapabilityBounds.targetBytes,
  })
  return target === target.trim() && target === expected ? target : invalid()
}

function addWorkflowCapabilityTarget(
  targets: Set<string>,
  target: string,
  invalid: () => never,
) {
  if (targets.has(target)) {
    return invalid()
  }
  targets.add(target)
}

function parseWorkflowCapabilityParameterShape(
  value: unknown,
  depth: number,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowCapabilityParameterShape {
  consumeWorkflowCapabilitySchemaBudget(budget, 1, invalid)
  if (depth > workflowCapabilityBounds.schemaDepth) {
    return invalid()
  }
  const shape = inspectionObject(
    value,
    ["type", "properties", "items", "enum", "additional_properties"],
    invalid,
  )
  const parsed: WorkflowCapabilityParameterShape = {}
  if (hasInspectionField(shape, "type")) {
    parsed.type = inspectionEnum(
      shape.type,
      workflowCapabilityParameterTypes,
      invalid,
    )
  }

  if (hasInspectionField(shape, "properties")) {
    let previousName: string | undefined
    parsed.properties = inspectionArray(
      shape.properties,
      workflowCapabilityBounds.schemaProperties,
      invalid,
    ).map((propertyValue) => {
      consumeWorkflowCapabilitySchemaBudget(budget, 1, invalid)
      const property = inspectionObject(
        propertyValue,
        ["name", "required", "shape"],
        invalid,
      )
      const name = workflowCapabilityIdentity(property.name, invalid)
      if (
        previousName != null &&
        compareInspectionUTF8(previousName, name) >= 0
      ) {
        return invalid()
      }
      previousName = name
      const required = inspectionBoolean(property.required, invalid)
      if (required) {
        consumeWorkflowCapabilitySchemaBudget(budget, 1, invalid)
      }
      return {
        name,
        required,
        shape: parseWorkflowCapabilityParameterShape(
          property.shape,
          depth + 1,
          budget,
          invalid,
        ),
      }
    })
  }

  if (hasInspectionField(shape, "items")) {
    parsed.items = parseWorkflowCapabilityParameterShape(
      shape.items,
      depth + 1,
      budget,
      invalid,
    )
  }
  if (hasInspectionField(shape, "enum")) {
    const enumKeys = new Set<string>()
    parsed.enum = inspectionArray(
      shape.enum,
      workflowCapabilityBounds.schemaEnum,
      invalid,
    ).map((enumValue) => {
      consumeWorkflowCapabilitySchemaBudget(budget, 1, invalid)
      const parsedValue = workflowCapabilityEnumValue(enumValue, invalid)
      const key = `${typeof parsedValue}:${String(parsedValue)}`
      if (enumKeys.has(key)) {
        return invalid()
      }
      enumKeys.add(key)
      return parsedValue
    })
  }
  if (hasInspectionField(shape, "additional_properties")) {
    const additionalProperties = inspectionObject(
      shape.additional_properties,
      ["allowed", "shape"],
      invalid,
    )
    const hasAllowed = hasInspectionField(additionalProperties, "allowed")
    const hasShape = hasInspectionField(additionalProperties, "shape")
    if (hasAllowed === hasShape) {
      return invalid()
    }
    parsed.additional_properties = hasAllowed
      ? {
          allowed: inspectionBoolean(additionalProperties.allowed, invalid),
        }
      : {
          shape: parseWorkflowCapabilityParameterShape(
            additionalProperties.shape,
            depth + 1,
            budget,
            invalid,
          ),
        }
  }
  return parsed
}

function workflowCapabilityEnumValue(
  value: unknown,
  invalid: () => never,
): WorkflowCapabilityParameterEnumValue {
  if (value === null || typeof value === "boolean") {
    return value
  }
  if (typeof value === "number") {
    return Number.isFinite(value) &&
      (!Number.isInteger(value) || Number.isSafeInteger(value))
      ? value
      : invalid()
  }
  if (typeof value === "string") {
    return inspectionString(value, invalid, {
      allowEmpty: true,
      maximumBytes: workflowCapabilityBounds.schemaStringBytes,
    })
  }
  return invalid()
}

function consumeWorkflowCapabilitySchemaBudget(
  budget: { remaining: number },
  amount: number,
  invalid: () => never,
) {
  budget.remaining -= amount
  if (budget.remaining < 0) {
    invalid()
  }
}

function parseWorkflowDefinitionInspection(
  value: unknown,
  expectedSource: WorkflowDefinitionInspectionSource,
): WorkflowDefinitionInspection {
  const invalid = (): never => {
    throw new Error(
      "Workflow definition inspection returned an invalid response.",
    )
  }
  const root = inspectionObject(
    value,
    [
      "source",
      "revision",
      "complete",
      "validation",
      "triggers",
      "jobs",
      "dependencies",
      "effects",
      "limits",
    ],
    invalid,
  )
  const sourceValue = inspectionObject(
    root.source,
    ["kind", "ref", "template_name"],
    invalid,
  )
  const sourceKind = inspectionString(sourceValue.kind, invalid, {
    maximumBytes: 16,
  })
  let source: WorkflowDefinitionInspectionSource
  if (
    sourceKind === "published" &&
    hasInspectionField(sourceValue, "ref") &&
    !hasInspectionField(sourceValue, "template_name")
  ) {
    source = {
      kind: "published",
      ref: inspectionString(sourceValue.ref, invalid, {
        maximumBytes: workflowInspectionSourceRefMaxBytes,
      }),
    }
  } else if (
    sourceKind === "template" &&
    hasInspectionField(sourceValue, "template_name") &&
    !hasInspectionField(sourceValue, "ref")
  ) {
    source = {
      kind: "template",
      template_name: inspectionString(sourceValue.template_name, invalid, {
        maximumBytes: 256,
      }),
    }
  } else {
    return invalid()
  }
  if (
    source.kind !== expectedSource.kind ||
    (source.kind === "published" &&
      expectedSource.kind === "published" &&
      source.ref !== expectedSource.ref) ||
    (source.kind === "template" &&
      expectedSource.kind === "template" &&
      source.template_name !== expectedSource.template_name)
  ) {
    return invalid()
  }

  const validationValue = inspectionObject(
    root.validation,
    ["valid", "issue_count", "issues", "truncated"],
    invalid,
  )
  const issues = inspectionArray(validationValue.issues, 128, invalid).map(
    (issueValue) => {
      const issue = inspectionObject(issueValue, ["code", "scope"], invalid)
      return {
        code: inspectionEnum(
          issue.code,
          workflowInspectionValidationCodes,
          invalid,
        ),
        scope: inspectionEnum(
          issue.scope,
          workflowInspectionValidationScopes,
          invalid,
        ),
      }
    },
  )
  const issueCount = inspectionInteger(validationValue.issue_count, invalid)
  const validationTruncated = inspectionBoolean(
    validationValue.truncated,
    invalid,
  )
  const validationValid = inspectionBoolean(validationValue.valid, invalid)
  if (
    issueCount < issues.length ||
    (!validationTruncated && issueCount !== issues.length) ||
    validationValid !== (issueCount === 0)
  ) {
    return invalid()
  }

  const triggerValues = inspectionObject(
    root.triggers,
    [...workflowTriggerKinds],
    invalid,
  )
  if (Object.keys(triggerValues).length !== workflowTriggerKinds.length) {
    return invalid()
  }
  const triggerBudget = { remaining: 4096 }
  const triggers = Object.fromEntries(
    workflowTriggerKinds.map((kind) => {
      const trigger = inspectionObject(
        triggerValues[kind],
        ["present", "projected", "value"],
        invalid,
      )
      const present = inspectionBoolean(trigger.present, invalid)
      const projected = inspectionBoolean(trigger.projected, invalid)
      const hasValue = hasInspectionField(trigger, "value")
      if ((present && projected) !== hasValue) {
        return invalid()
      }
      const next: WorkflowDefinitionInspectionTrigger = {
        present,
        projected,
      }
      if (hasValue) {
        next.value = parseWorkflowInspectionTriggerValue(
          kind,
          trigger.value,
          triggerBudget,
          invalid,
        ) as WorkflowDefinitionInspectionTrigger["value"]
      }
      return [kind, next]
    }),
  ) as WorkflowDefinitionInspectionTriggers

  let stepCount = 0
  let hasOmittedUnsafeStepTarget = false
  const jobs = inspectionArray(root.jobs, 256, invalid).map((jobValue) => {
    const job = inspectionObject(
      jobValue,
      ["id", "kind", "reusable_target", "steps"],
      invalid,
    )
    const id = inspectionString(job.id, invalid, {
      maximumBytes: 256,
    })
    const kind = inspectionEnum(job.kind, workflowInspectionJobKinds, invalid)
    if (kind === "steps" && hasInspectionField(job, "reusable_target")) {
      return invalid()
    }
    const steps = inspectionArray(job.steps, 4096, invalid).map(
      (stepValue, expectedIndex) => {
        stepCount += 1
        if (stepCount > 4096) {
          return invalid()
        }
        const step = inspectionObject(
          stepValue,
          ["index", "id", "kind", "target"],
          invalid,
        )
        const index = inspectionInteger(step.index, invalid)
        if (index !== expectedIndex) {
          return invalid()
        }
        const stepKind = inspectionEnum(
          step.kind,
          workflowInspectionStepKinds,
          invalid,
        )
        const parsed: WorkflowDefinitionInspectionStep = {
          index,
          kind: stepKind,
        }
        if (hasInspectionField(step, "id")) {
          const stepID = inspectionString(step.id, invalid, {
            maximumBytes: 256,
          })
          parsed.id = stepID
        }
        if (hasInspectionField(step, "target")) {
          parsed.target = inspectionString(step.target, invalid, {
            maximumBytes: 512,
          })
        } else if (stepKind !== "unknown") {
          hasOmittedUnsafeStepTarget = true
        }
        return parsed
      },
    )
    const parsed: WorkflowDefinitionInspectionJob = {
      id,
      kind,
      steps,
    }
    if (hasInspectionField(job, "reusable_target")) {
      parsed.reusable_target = inspectionString(job.reusable_target, invalid, {
        maximumBytes: 1024,
      })
    }
    return parsed
  })

  let previousDependencyKey: string | undefined
  const dependencies = inspectionArray(root.dependencies, 4096, invalid).map(
    (dependencyValue) => {
      const dependency = inspectionObject(
        dependencyValue,
        ["kind", "target", "occurrences"],
        invalid,
      )
      const kind = inspectionEnum(
        dependency.kind,
        workflowInspectionDependencyKinds,
        invalid,
      )
      const target = inspectionString(dependency.target, invalid, {
        maximumBytes: 1024,
      })
      const key = `${kind}\u0000${target}`
      if (
        previousDependencyKey != null &&
        compareInspectionUTF8(previousDependencyKey, key) >= 0
      ) {
        return invalid()
      }
      previousDependencyKey = key
      return {
        kind,
        target,
        occurrences: inspectionPositiveInteger(dependency.occurrences, invalid),
      }
    },
  )
  let previousEffectKey: string | undefined
  const effects = inspectionArray(root.effects, 4096, invalid).map(
    (effectValue) => {
      const effect = inspectionObject(
        effectValue,
        ["kind", "target", "occurrences"],
        invalid,
      )
      const kind = inspectionEnum(
        effect.kind,
        workflowInspectionEffectKinds,
        invalid,
      )
      const parsed: WorkflowDefinitionInspectionEffect = {
        kind,
        occurrences: inspectionPositiveInteger(effect.occurrences, invalid),
      }
      if (hasInspectionField(effect, "target")) {
        parsed.target = inspectionString(effect.target, invalid, {
          maximumBytes: 1024,
        })
      }
      const key = `${kind}\u0000${parsed.target ?? ""}`
      if (
        previousEffectKey != null &&
        compareInspectionUTF8(previousEffectKey, key) >= 0
      ) {
        return invalid()
      }
      previousEffectKey = key
      return parsed
    },
  )
  let previousLimit: string | undefined
  const limits = inspectionArray(root.limits, 7, invalid).map((limitValue) => {
    const limit = inspectionEnum(
      limitValue,
      workflowInspectionLimitKinds,
      invalid,
    )
    if (
      previousLimit != null &&
      compareInspectionUTF8(previousLimit, limit) >= 0
    ) {
      return invalid()
    }
    previousLimit = limit
    return limit
  })
  const complete = inspectionBoolean(root.complete, invalid)
  if (
    complete !== (limits.length === 0) ||
    validationTruncated !== limits.includes("validation_issues_truncated") ||
    (hasOmittedUnsafeStepTarget && !limits.includes("unsafe_fields_omitted"))
  ) {
    return invalid()
  }

  return {
    source,
    revision: inspectionString(root.revision, invalid, { maximumBytes: 256 }),
    complete,
    validation: {
      valid: validationValid,
      issue_count: issueCount,
      issues,
      truncated: validationTruncated,
    },
    triggers,
    jobs,
    dependencies,
    effects,
    limits,
  }
}

function parseWorkflowInspectionTriggerValue(
  kind: WorkflowTriggerKind,
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowDefinitionInspectionTrigger["value"] {
  switch (kind) {
    case "manual": {
      const manual = inspectionObject(value, [], invalid)
      if (Object.keys(manual).length !== 0) {
        return invalid()
      }
      return {}
    }
    case "schedule":
      return inspectionArray(value, 256, invalid).map((scheduleValue) => {
        consumeWorkflowInspectionBudget(budget, 1, invalid)
        const schedule = inspectionObject(scheduleValue, ["cron"], invalid)
        return {
          cron: inspectionString(schedule.cron, invalid, {
            allowEmpty: true,
            maximumBytes: 4096,
          }),
        }
      })
    case "channel_message":
      return parseWorkflowInspectionChannelTrigger(value, budget, invalid)
    case "command":
      return parseWorkflowInspectionCommandTrigger(value, budget, invalid)
    case "runtime_event":
      return parseWorkflowInspectionRuntimeTrigger(value, budget, invalid)
    case "event":
      return parseWorkflowInspectionEventTrigger(value, budget, invalid)
    case "workflow_call":
      return parseWorkflowInspectionCallTrigger(value, budget, invalid)
  }
}

function parseWorkflowInspectionChannelTrigger(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowDefinitionInspectionChannelTrigger {
  const trigger = inspectionObject(
    value,
    [
      "channels",
      "chats",
      "senders",
      "mentioned",
      "command",
      "text_matches",
      "passthrough",
      "session_configured",
      "delivery_configured",
    ],
    invalid,
  )
  return {
    ...inspectionOptionalStringLists(
      trigger,
      ["channels", "chats", "senders"],
      budget,
      invalid,
    ),
    ...(hasInspectionField(trigger, "mentioned")
      ? { mentioned: inspectionBoolean(trigger.mentioned, invalid) }
      : {}),
    ...(hasInspectionField(trigger, "command")
      ? {
          command: inspectionString(trigger.command, invalid, {
            allowEmpty: true,
          }),
        }
      : {}),
    ...(hasInspectionField(trigger, "text_matches")
      ? {
          text_matches: inspectionString(trigger.text_matches, invalid, {
            allowEmpty: true,
          }),
        }
      : {}),
    ...(hasInspectionField(trigger, "passthrough")
      ? { passthrough: inspectionBoolean(trigger.passthrough, invalid) }
      : {}),
    session_configured: inspectionBoolean(trigger.session_configured, invalid),
    delivery_configured: inspectionBoolean(
      trigger.delivery_configured,
      invalid,
    ),
  }
}

function parseWorkflowInspectionCommandTrigger(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowDefinitionInspectionCommandTrigger {
  const trigger = inspectionObject(
    value,
    [
      "name",
      "channels",
      "chats",
      "senders",
      "args",
      "passthrough",
      "session_configured",
      "delivery_configured",
    ],
    invalid,
  )
  return {
    ...(hasInspectionField(trigger, "name")
      ? {
          name: inspectionString(trigger.name, invalid, {
            allowEmpty: true,
          }),
        }
      : {}),
    ...inspectionOptionalStringLists(
      trigger,
      ["channels", "chats", "senders"],
      budget,
      invalid,
    ),
    ...(hasInspectionField(trigger, "args")
      ? {
          args: parseWorkflowInspectionInputs(trigger.args, budget, invalid),
        }
      : {}),
    ...(hasInspectionField(trigger, "passthrough")
      ? { passthrough: inspectionBoolean(trigger.passthrough, invalid) }
      : {}),
    session_configured: inspectionBoolean(trigger.session_configured, invalid),
    delivery_configured: inspectionBoolean(
      trigger.delivery_configured,
      invalid,
    ),
  }
}

function parseWorkflowInspectionRuntimeTrigger(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowDefinitionInspectionRuntimeEventTrigger {
  const trigger = inspectionObject(
    value,
    [
      "kinds",
      "sources",
      "agents",
      "session_filter_present",
      "session_filter_count",
      "channels",
      "chats",
    ],
    invalid,
  )
  const sessionFilterPresent = inspectionBoolean(
    trigger.session_filter_present,
    invalid,
  )
  const sessionFilterCount = inspectionInteger(
    trigger.session_filter_count,
    invalid,
  )
  if (!sessionFilterPresent && sessionFilterCount !== 0) {
    return invalid()
  }
  return {
    ...inspectionOptionalStringLists(
      trigger,
      ["kinds", "sources", "agents", "channels", "chats"],
      budget,
      invalid,
    ),
    session_filter_present: sessionFilterPresent,
    session_filter_count: sessionFilterCount,
  }
}

function parseWorkflowInspectionEventTrigger(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowEventTrigger {
  const trigger = inspectionObject(
    value,
    ["sources", "connectors", "types", "actor", "subject", "attributes"],
    invalid,
  )
  return {
    ...inspectionOptionalStringLists(
      trigger,
      ["sources", "connectors", "types"],
      budget,
      invalid,
    ),
    ...(hasInspectionField(trigger, "actor")
      ? {
          actor: parseWorkflowInspectionEventEntity(
            trigger.actor,
            budget,
            invalid,
          ),
        }
      : {}),
    ...(hasInspectionField(trigger, "subject")
      ? {
          subject: parseWorkflowInspectionEventEntity(
            trigger.subject,
            budget,
            invalid,
          ),
        }
      : {}),
    ...(hasInspectionField(trigger, "attributes")
      ? {
          attributes: parseWorkflowInspectionStringListMap(
            trigger.attributes,
            budget,
            invalid,
          ),
        }
      : {}),
  }
}

function parseWorkflowInspectionEventEntity(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowEventEntityTrigger {
  const entity = inspectionObject(
    value,
    ["ids", "types", "attributes"],
    invalid,
  )
  return {
    ...inspectionOptionalStringLists(entity, ["ids", "types"], budget, invalid),
    ...(hasInspectionField(entity, "attributes")
      ? {
          attributes: parseWorkflowInspectionStringListMap(
            entity.attributes,
            budget,
            invalid,
          ),
        }
      : {}),
  }
}

function parseWorkflowInspectionCallTrigger(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
): WorkflowDefinitionInspectionWorkflowCallTrigger {
  const trigger = inspectionObject(
    value,
    ["inputs", "secrets", "outputs"],
    invalid,
  )
  return {
    ...(hasInspectionField(trigger, "inputs")
      ? {
          inputs: parseWorkflowInspectionInputs(
            trigger.inputs,
            budget,
            invalid,
          ),
        }
      : {}),
    ...(hasInspectionField(trigger, "secrets")
      ? {
          secrets: parseWorkflowInspectionSecrets(
            trigger.secrets,
            budget,
            invalid,
          ),
        }
      : {}),
    ...(hasInspectionField(trigger, "outputs")
      ? {
          outputs: inspectionStringArray(trigger.outputs, budget, invalid, 256),
        }
      : {}),
  }
}

function parseWorkflowInspectionInputs(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
) {
  const inputs = inspectionObject(value, undefined, invalid)
  const entries = Object.entries(inputs)
  consumeWorkflowInspectionBudget(budget, entries.length, invalid)
  return Object.fromEntries(
    entries.map(([name, inputValue]) => {
      const input = inspectionObject(
        inputValue,
        ["type", "required", "has_default"],
        invalid,
      )
      return [
        inspectionString(name, invalid, {
          allowEmpty: true,
          maximumBytes: 256,
        }),
        {
          ...(hasInspectionField(input, "type")
            ? {
                type: inspectionString(input.type, invalid, {
                  allowEmpty: true,
                  maximumBytes: 64,
                }),
              }
            : {}),
          required: inspectionBoolean(input.required, invalid),
          has_default: inspectionBoolean(input.has_default, invalid),
        },
      ]
    }),
  )
}

function parseWorkflowInspectionSecrets(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
) {
  const secrets = inspectionObject(value, undefined, invalid)
  const entries = Object.entries(secrets)
  consumeWorkflowInspectionBudget(budget, entries.length, invalid)
  return Object.fromEntries(
    entries.map(([name, secretValue]) => {
      const secret = inspectionObject(secretValue, ["required"], invalid)
      return [
        inspectionString(name, invalid, {
          allowEmpty: true,
          maximumBytes: 256,
        }),
        { required: inspectionBoolean(secret.required, invalid) },
      ]
    }),
  )
}

function parseWorkflowInspectionStringListMap(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
) {
  const record = inspectionObject(value, undefined, invalid)
  const entries = Object.entries(record)
  consumeWorkflowInspectionBudget(budget, entries.length, invalid)
  return Object.fromEntries(
    entries.map(([name, list]) => [
      inspectionString(name, invalid, {
        allowEmpty: true,
        maximumBytes: 256,
      }),
      inspectionStringArray(list, budget, invalid),
    ]),
  )
}

function inspectionOptionalStringLists(
  record: Record<string, unknown>,
  fields: string[],
  budget: { remaining: number },
  invalid: () => never,
): Record<string, string[]> {
  return Object.fromEntries(
    fields.flatMap((field) =>
      hasInspectionField(record, field)
        ? [[field, inspectionStringArray(record[field], budget, invalid)]]
        : [],
    ),
  )
}

function inspectionStringArray(
  value: unknown,
  budget: { remaining: number },
  invalid: () => never,
  maximumBytes = 4096,
) {
  const values = inspectionArray(value, 4096, invalid)
  consumeWorkflowInspectionBudget(budget, values.length, invalid)
  return values.map((item) =>
    inspectionString(item, invalid, { allowEmpty: true, maximumBytes }),
  )
}

function consumeWorkflowInspectionBudget(
  budget: { remaining: number },
  amount: number,
  invalid: () => never,
) {
  budget.remaining -= amount
  if (budget.remaining < 0) {
    invalid()
  }
}

const workflowTriggerSimulationReasons =
  new Set<WorkflowTriggerSimulationReason>([
    "matched",
    "invalid_workflow",
    "trigger_absent",
    "schedule_index_required",
    "schedule_index_out_of_range",
    "invalid_scenario",
    "not_matched",
    "shadowed_by_command",
    "runtime_feedback_suppressed",
    "trigger_evaluation_failed",
    "review_incomplete",
  ])

function workflowTriggerInvocationScenarioBody(
  scenario: WorkflowTriggerInvocationScenario,
): WorkflowTriggerInvocationScenario {
  return {
    ...(scenario.inputs === undefined ? {} : { inputs: scenario.inputs }),
    ...(scenario.secrets === undefined ? {} : { secrets: scenario.secrets }),
    ...(scenario.session === undefined ? {} : { session: scenario.session }),
    ...(scenario.delivery === undefined ? {} : { delivery: scenario.delivery }),
  }
}

async function workflowTriggerSimulationResponseValue(
  response: Response,
  unavailableMessage: string,
  expectedStatus = 200,
): Promise<unknown> {
  let responseText: string
  try {
    responseText = await boundedWorkflowResponseText(response, 1 << 20)
  } catch {
    throw new WorkflowAPIError(unavailableMessage, response.status)
  }
  if (response.status !== expectedStatus) {
    const details = apiErrorDetails(
      responseText,
      response.status,
      response.statusText,
    )
    throw new WorkflowAPIError(
      details.message,
      response.status,
      details.candidateValidation,
    )
  }
  const contentType = response.headers.get("Content-Type")?.toLowerCase() ?? ""
  if (!contentType.startsWith("application/json")) {
    throw new WorkflowAPIError(
      "Workflow trigger service returned an invalid response.",
      502,
    )
  }
  try {
    return JSON.parse(responseText) as unknown
  } catch {
    throw new WorkflowAPIError(
      "Workflow trigger service returned an invalid response.",
      502,
    )
  }
}

function parseWorkflowTriggerSimulationResponse(
  value: unknown,
): WorkflowTriggerSimulationResponse {
  const invalid = (): never => {
    throw new WorkflowAPIError(
      "Workflow trigger simulation returned an invalid response.",
      502,
    )
  }
  const root = inspectionObject(
    value,
    ["simulation", "review", "review_token"],
    invalid,
  )
  const simulationValue = inspectionObject(
    root.simulation,
    [
      "selected_kind",
      "effective_kind",
      "schedule_index",
      "present",
      "matched",
      "executable",
      "reason",
      "passthrough",
      "context_summary",
    ],
    invalid,
  )
  const selectedKind = inspectionEnum(
    simulationValue.selected_kind,
    new Set(workflowTriggerKinds),
    invalid,
  )
  const present = inspectionBoolean(simulationValue.present, invalid)
  const matched = inspectionBoolean(simulationValue.matched, invalid)
  const executable = inspectionBoolean(simulationValue.executable, invalid)
  const reason = inspectionEnum(
    simulationValue.reason,
    workflowTriggerSimulationReasons,
    invalid,
  )
  const contextValue = inspectionObject(
    simulationValue.context_summary,
    ["input_count", "secret_count", "has_event", "has_session", "has_delivery"],
    invalid,
  )
  if (Object.keys(contextValue).length !== 5) {
    return invalid()
  }
  const simulation: WorkflowTriggerSimulation = {
    selected_kind: selectedKind,
    present,
    matched,
    executable,
    reason,
    context_summary: {
      input_count: inspectionInteger(contextValue.input_count, invalid),
      secret_count: inspectionInteger(contextValue.secret_count, invalid),
      has_event: inspectionBoolean(contextValue.has_event, invalid),
      has_session: inspectionBoolean(contextValue.has_session, invalid),
      has_delivery: inspectionBoolean(contextValue.has_delivery, invalid),
    },
  }
  if (hasInspectionField(simulationValue, "effective_kind")) {
    simulation.effective_kind = inspectionEnum(
      simulationValue.effective_kind,
      new Set(workflowTriggerKinds),
      invalid,
    )
  }
  if (hasInspectionField(simulationValue, "schedule_index")) {
    simulation.schedule_index = inspectionInteger(
      simulationValue.schedule_index,
      invalid,
    )
  }
  if (hasInspectionField(simulationValue, "passthrough")) {
    simulation.passthrough = inspectionBoolean(
      simulationValue.passthrough,
      invalid,
    )
  }

  const reviewValue = inspectionObject(
    root.review,
    [
      "job_count",
      "step_count",
      "targets",
      "effects",
      "complete",
      "validation",
      "limits",
    ],
    invalid,
  )
  let previousTarget: string | undefined
  const targets = inspectionArray(reviewValue.targets, 4096, invalid).map(
    (targetValue) => {
      const target = inspectionString(targetValue, invalid, {
        maximumBytes: 1024,
      })
      if (
        previousTarget != null &&
        compareInspectionUTF8(previousTarget, target) >= 0
      ) {
        return invalid()
      }
      previousTarget = target
      return target
    },
  )
  let previousEffectKey: string | undefined
  const effects = inspectionArray(reviewValue.effects, 4096, invalid).map(
    (effectValue) => {
      const effect = inspectionObject(
        effectValue,
        ["kind", "target", "occurrences"],
        invalid,
      )
      const parsed: WorkflowDefinitionInspectionEffect = {
        kind: inspectionEnum(
          effect.kind,
          workflowInspectionEffectKinds,
          invalid,
        ),
        occurrences: inspectionPositiveInteger(effect.occurrences, invalid),
      }
      if (hasInspectionField(effect, "target")) {
        parsed.target = inspectionString(effect.target, invalid, {
          maximumBytes: 1024,
        })
      }
      const key = `${parsed.kind}\u0000${parsed.target ?? ""}`
      if (
        previousEffectKey != null &&
        compareInspectionUTF8(previousEffectKey, key) >= 0
      ) {
        return invalid()
      }
      previousEffectKey = key
      return parsed
    },
  )
  const validationValue = inspectionObject(
    reviewValue.validation,
    ["valid", "issue_count", "issues", "truncated"],
    invalid,
  )
  const issues = inspectionArray(validationValue.issues, 128, invalid).map(
    (issueValue) => {
      const issue = inspectionObject(issueValue, ["code", "scope"], invalid)
      return {
        code: inspectionEnum(
          issue.code,
          workflowInspectionValidationCodes,
          invalid,
        ),
        scope: inspectionEnum(
          issue.scope,
          workflowInspectionValidationScopes,
          invalid,
        ),
      }
    },
  )
  const issueCount = inspectionInteger(validationValue.issue_count, invalid)
  const truncated = inspectionBoolean(validationValue.truncated, invalid)
  const valid = inspectionBoolean(validationValue.valid, invalid)
  if (
    issueCount < issues.length ||
    (!truncated && issueCount !== issues.length) ||
    valid !== (issueCount === 0)
  ) {
    return invalid()
  }
  let previousLimit: string | undefined
  const limits = inspectionArray(reviewValue.limits, 7, invalid).map(
    (limitValue) => {
      const limit = inspectionEnum(
        limitValue,
        workflowInspectionLimitKinds,
        invalid,
      )
      if (
        previousLimit != null &&
        compareInspectionUTF8(previousLimit, limit) >= 0
      ) {
        return invalid()
      }
      previousLimit = limit
      return limit
    },
  )
  const complete = inspectionBoolean(reviewValue.complete, invalid)
  if (
    complete !== (limits.length === 0) ||
    truncated !== limits.includes("validation_issues_truncated")
  ) {
    return invalid()
  }
  const review: WorkflowTriggerSimulationReview = {
    job_count: inspectionInteger(reviewValue.job_count, invalid),
    step_count: inspectionInteger(reviewValue.step_count, invalid),
    targets,
    effects,
    complete,
    validation: {
      valid,
      issue_count: issueCount,
      issues,
      truncated,
    },
    limits,
  }
  const hasToken = hasInspectionField(root, "review_token")
  const reviewToken = hasToken
    ? inspectionString(root.review_token, invalid, { maximumBytes: 4096 })
    : undefined
  if (
    hasToken !== executable ||
    executable !==
      (present && matched && reason === "matched" && complete && valid)
  ) {
    return invalid()
  }
  return {
    simulation,
    review,
    ...(reviewToken === undefined ? {} : { review_token: reviewToken }),
  }
}

function parseWorkflowTriggerExecutionResult(
  value: unknown,
  expectedSessionID: string,
): WorkflowDevelopmentTestResult {
  const invalid = (): never => {
    throw new WorkflowAPIError(
      "Workflow trigger execution returned an invalid response.",
      502,
    )
  }
  const root = inspectionObject(
    value,
    ["session", "result", "reconciliation"],
    invalid,
  )
  let parsedSession: WorkflowDevelopmentSession | undefined
  if (hasInspectionField(root, "session")) {
    const session = inspectionObject(
      root.session,
      [
        "id",
        "session_revision",
        "draft_revision",
        "base_target_revision",
        "reason",
        "status",
        "prompt",
        "source_workflow_ref",
        "target_workflow_ref",
        "target_picoclaw_version",
        "target_git_commit",
        "yaml",
        "validation",
        "last_test",
        "created_at",
        "updated_at",
      ],
      invalid,
    )
    const requiredSessionFields = [
      "id",
      "session_revision",
      "draft_revision",
      "base_target_revision",
      "reason",
      "status",
      "target_workflow_ref",
      "yaml",
      "created_at",
      "updated_at",
    ] as const
    if (
      requiredSessionFields.some((field) => !hasInspectionField(session, field))
    ) {
      return invalid()
    }
    parsedSession = parseWorkflowTriggerExecutionSession(
      session,
      expectedSessionID,
      invalid,
    )
  }
  const resultValue = inspectionObject(
    root.result,
    ["run_id", "status"],
    invalid,
  )
  if (
    !hasInspectionField(resultValue, "run_id") ||
    !hasInspectionField(resultValue, "status")
  ) {
    return invalid()
  }
  const runID = inspectionString(resultValue.run_id, invalid, {
    maximumBytes: maximumWorkflowRunIDBytes,
  })
  if (!validWorkflowRunID(runID) || resultValue.status !== "running") {
    return invalid()
  }
  const result: WorkflowRunResult = {
    run_id: runID,
    status: "running",
  }
  let reconciliation: WorkflowDevelopmentTestReconciliation | undefined
  if (hasInspectionField(root, "reconciliation")) {
    const value = inspectionObject(
      root.reconciliation,
      ["state", "reason", "run_id", "message"],
      invalid,
    )
    if (
      value.state !== "degraded" ||
      ![
        "draft_test_snapshot_not_recorded",
        "draft_test_run_unavailable",
        "draft_test_terminal_snapshot_not_recorded",
        "draft_test_response_truncated",
      ].includes(String(value.reason))
    ) {
      return invalid()
    }
    reconciliation = {
      state: "degraded",
      reason: value.reason as WorkflowDevelopmentTestReconciliation["reason"],
      run_id: inspectionString(value.run_id, invalid, {
        maximumBytes: maximumWorkflowRunIDBytes,
      }),
      message: inspectionString(value.message, invalid, {
        maximumBytes: 16 * 1024,
      }),
    }
    if (
      reconciliation.run_id !== runID ||
      !validWorkflowRunID(reconciliation.run_id)
    ) {
      return invalid()
    }
  }
  if (
    parsedSession == null &&
    reconciliation?.reason !== "draft_test_response_truncated"
  ) {
    return invalid()
  }
  return {
    ...(parsedSession === undefined ? {} : { session: parsedSession }),
    result,
    ...(reconciliation === undefined ? {} : { reconciliation }),
  }
}

function parseWorkflowTriggerExecutionSession(
  session: Record<string, unknown>,
  expectedSessionID: string,
  invalid: () => never,
): WorkflowDevelopmentSession {
  const id = inspectionString(session.id, invalid, { maximumBytes: 4096 })
  if (id !== expectedSessionID) {
    return invalid()
  }
  const parsed: WorkflowDevelopmentSession = {
    id,
    session_revision: inspectionString(session.session_revision, invalid, {
      maximumBytes: 4096,
    }),
    draft_revision: inspectionString(session.draft_revision, invalid, {
      maximumBytes: 4096,
    }),
    base_target_revision: inspectionString(
      session.base_target_revision,
      invalid,
      { maximumBytes: 4096 },
    ),
    reason: inspectionEnum(
      session.reason,
      new Set(["new", "edit", "version_revalidation"]),
      invalid,
    ),
    status: inspectionEnum(
      session.status,
      new Set([
        "planning",
        "editing",
        "validating",
        "testing",
        "ready_to_publish",
      ]),
      invalid,
    ),
    target_workflow_ref: inspectionString(
      session.target_workflow_ref,
      invalid,
      { maximumBytes: 4096 },
    ),
    yaml: workflowJobEditorString(session.yaml, invalid, {
      allowEmpty: true,
      maximumBytes: 1 << 20,
      allowFormattingControls: true,
    }),
    created_at: workflowTriggerExecutionTimestamp(session.created_at, invalid),
    updated_at: workflowTriggerExecutionTimestamp(session.updated_at, invalid),
  }
  if (hasInspectionField(session, "prompt")) {
    parsed.prompt = workflowJobEditorString(session.prompt, invalid, {
      allowEmpty: true,
      maximumBytes: 64 << 10,
      allowFormattingControls: true,
    })
  }
  if (hasInspectionField(session, "source_workflow_ref")) {
    parsed.source_workflow_ref = inspectionString(
      session.source_workflow_ref,
      invalid,
      { maximumBytes: 4096 },
    )
  }
  if (hasInspectionField(session, "target_picoclaw_version")) {
    parsed.target_picoclaw_version = inspectionString(
      session.target_picoclaw_version,
      invalid,
      { maximumBytes: 4096 },
    )
  }
  if (hasInspectionField(session, "target_git_commit")) {
    parsed.target_git_commit = inspectionString(
      session.target_git_commit,
      invalid,
      { maximumBytes: 4096 },
    )
  }
  if (hasInspectionField(session, "validation")) {
    const validation = parseWorkflowJobEditorValidation(
      session.validation,
      invalid,
    )
    validation.validated_at = workflowTriggerExecutionTimestamp(
      validation.validated_at,
      invalid,
    )
    parsed.validation = validation
  }
  if (hasInspectionField(session, "last_test")) {
    parsed.last_test = parseWorkflowTriggerExecutionLastTest(
      session.last_test,
      invalid,
    )
  }
  return parsed
}

function parseWorkflowTriggerExecutionLastTest(
  value: unknown,
  invalid: () => never,
): WorkflowDevelopmentTestSnapshot {
  const snapshot = inspectionObject(
    value,
    [
      "draft_key",
      "draft_revision",
      "target_workflow_ref",
      "run_id",
      "event_id",
      "status",
      "error",
      "tested_at",
    ],
    invalid,
  )
  const required = [
    "draft_key",
    "target_workflow_ref",
    "status",
    "tested_at",
  ] as const
  if (required.some((field) => !hasInspectionField(snapshot, field))) {
    return invalid()
  }
  const parsed: WorkflowDevelopmentTestSnapshot = {
    draft_key: workflowTriggerExecutionDraftKey(snapshot.draft_key, invalid),
    target_workflow_ref: inspectionString(
      snapshot.target_workflow_ref,
      invalid,
      { maximumBytes: 4096 },
    ),
    status: inspectionString(snapshot.status, invalid, { maximumBytes: 64 }),
    tested_at: workflowTriggerExecutionTimestamp(snapshot.tested_at, invalid),
  }
  if (hasInspectionField(snapshot, "draft_revision")) {
    parsed.draft_revision = inspectionString(snapshot.draft_revision, invalid, {
      maximumBytes: 4096,
    })
  }
  if (hasInspectionField(snapshot, "run_id")) {
    const runID = inspectionString(snapshot.run_id, invalid, {
      maximumBytes: maximumWorkflowRunIDBytes,
    })
    if (!validWorkflowRunID(runID)) {
      return invalid()
    }
    parsed.run_id = runID
  }
  if (hasInspectionField(snapshot, "event_id")) {
    parsed.event_id = inspectionString(snapshot.event_id, invalid, {
      maximumBytes: 4096,
    })
  }
  if (hasInspectionField(snapshot, "error")) {
    parsed.error = workflowJobEditorString(snapshot.error, invalid, {
      allowEmpty: true,
      maximumBytes: 16 << 10,
      allowFormattingControls: true,
    })
  }
  return parsed
}

function workflowTriggerExecutionDraftKey(
  value: unknown,
  invalid: () => never,
) {
  if (typeof value !== "string") {
    return invalid()
  }
  const separator = value.indexOf("\u0000")
  if (
    separator <= 0 ||
    separator !== value.lastIndexOf("\u0000") ||
    separator === value.length - 1 ||
    new TextEncoder().encode(value).byteLength > 1 << 20
  ) {
    return invalid()
  }
  inspectionString(value.slice(0, separator), invalid, { maximumBytes: 4096 })
  workflowJobEditorString(value.slice(separator + 1), invalid, {
    maximumBytes: 1 << 20,
    allowFormattingControls: true,
  })
  return value
}

function workflowTriggerExecutionTimestamp(
  value: unknown,
  invalid: () => never,
) {
  const timestamp = inspectionString(value, invalid, { maximumBytes: 128 })
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(
      timestamp,
    ) ||
    Number.isNaN(Date.parse(timestamp))
  ) {
    return invalid()
  }
  return timestamp
}

function inspectionObject(
  value: unknown,
  allowedFields: readonly string[] | undefined,
  invalid: () => never,
): Record<string, unknown> {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return invalid()
  }
  const record = value as Record<string, unknown>
  if (
    allowedFields != null &&
    Object.keys(record).some((field) => !allowedFields.includes(field))
  ) {
    return invalid()
  }
  return record
}

function hasInspectionField(record: Record<string, unknown>, field: string) {
  return Object.prototype.hasOwnProperty.call(record, field)
}

function inspectionArray(
  value: unknown,
  maximum: number,
  invalid: () => never,
): unknown[] {
  if (!Array.isArray(value) || value.length > maximum) {
    return invalid()
  }
  return value
}

function inspectionString(
  value: unknown,
  invalid: () => never,
  {
    allowEmpty = false,
    maximumBytes = 4096,
  }: { allowEmpty?: boolean; maximumBytes?: number } = {},
): string {
  if (
    typeof value !== "string" ||
    (!allowEmpty && value === "") ||
    new TextEncoder().encode(value).byteLength > maximumBytes ||
    /[\p{Cc}\p{Cf}]/u.test(value)
  ) {
    return invalid()
  }
  return value
}

function inspectionBoolean(value: unknown, invalid: () => never): boolean {
  return typeof value === "boolean" ? value : invalid()
}

function inspectionInteger(value: unknown, invalid: () => never): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0
    ? value
    : invalid()
}

function inspectionPositiveInteger(
  value: unknown,
  invalid: () => never,
): number {
  const integer = inspectionInteger(value, invalid)
  return integer > 0 ? integer : invalid()
}

function inspectionEnum<Value extends string>(
  value: unknown,
  allowed: ReadonlySet<Value>,
  invalid: () => never,
): Value {
  return typeof value === "string" && allowed.has(value as Value)
    ? (value as Value)
    : invalid()
}

function compareInspectionUTF8(left: string, right: string) {
  const encoder = new TextEncoder()
  const leftBytes = encoder.encode(left)
  const rightBytes = encoder.encode(right)
  const length = Math.min(leftBytes.length, rightBytes.length)
  for (let index = 0; index < length; index += 1) {
    const difference = leftBytes[index] - rightBytes[index]
    if (difference !== 0) {
      return difference
    }
  }
  return leftBytes.length - rightBytes.length
}

function normalizeWorkflowCompatibilitySummary(
  summary: WorkflowCompatibilitySummary,
): WorkflowCompatibilitySummary {
  return {
    ...summary,
    workflows: arrayOrEmpty(summary.workflows),
    counts: recordOrEmpty(summary.counts),
  }
}

function normalizeWorkflowReloadResult(
  result: WorkflowReloadResult,
): WorkflowReloadResult {
  return {
    ...result,
    workflows: arrayOrEmpty(result.workflows),
    errors: arrayOrEmpty(result.errors),
  }
}

function normalizeWorkflowRun(run: WorkflowRun): WorkflowRun {
  return {
    ...run,
    child_run_ids: arrayOrEmpty(run.child_run_ids),
    jobs: recordOrEmpty(run.jobs),
    steps: recordOrEmpty(run.steps),
  }
}

function normalizeWorkflowRunGraph(graph: WorkflowRunGraph): WorkflowRunGraph {
  return {
    ...graph,
    nodes: arrayOrEmpty(graph.nodes),
    edges: arrayOrEmpty(graph.edges),
  }
}
