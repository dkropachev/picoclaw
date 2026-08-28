import {
  CollectionAPIError,
  type CollectionBulkDeleteFailure,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
  projectCollectionQuerySchema,
} from "@/api/collection"
import {
  DevelopmentWorkspaceAPIError as PRWorkspaceAPIError,
  requestDevelopmentJSON as requestPRWorkspaceJSON,
} from "@/api/development-workspaces"
import {
  type PRLifecycleFlowCatalog,
  projectPRLifecycleFlowCatalog,
} from "@/api/pr-lifecycle-flow"

export type PRLifecycleGateActionType =
  | "human"
  | "ai"
  | "deterministic"
  | "workflow"

export interface PRLifecycleGateAction {
  type: PRLifecycleGateActionType
  agentID?: string
  prompt?: string
  session?: "ephemeral" | "private" | "source"
  history?: "none" | "read_only" | "read_write"
  cache?: "none" | "session" | "agent"
  tools?: "none" | "inherit"
  fields?: Record<string, unknown>
  workflowRef?: string
}

export interface PRLifecycleGateBinding {
  workflowRef: string
  gateRef: string
  action?: PRLifecycleGateAction
}

export interface PRLifecycleWorkflowConfiguration {
  name: string
  bindings: PRLifecycleGateBinding[]
  deferredIssues: { mode: PRLifecycleDeferredIssueModeV3 }
  scopeDisposition?: PRLifecycleScopeDispositionConfig
}

export type PRLifecycleScopeDispositionMode = "strict" | "relaxed"

export interface PRLifecycleScopeDispositionRule {
  mode: PRLifecycleScopeDispositionMode
  prompt: string
}

export interface PRLifecycleScopeDispositionConfig {
  default: PRLifecycleScopeDispositionRule
  byType: Partial<
    Record<
      "fix" | "feature" | "refactor" | "documentation" | "test",
      PRLifecycleScopeDispositionRule
    >
  >
}

export type PRLifecycleGateCatalogField =
  | {
      id: string
      type: "short-text" | "long-text" | "boolean"
      label: string
      required: boolean
    }
  | {
      id: string
      type: "select"
      label: string
      required: boolean
      minSelections: number
      maxSelections: number
      options: Array<{ id: string; label: string }>
    }

export interface PRLifecycleGateCatalogEntry {
  workflowRef: string
  gateRef: string
  sourceAISupported: boolean
  prompt?: string
  fields?: PRLifecycleGateCatalogField[]
  workflowRevision?: string
  defaultAction?: PRLifecycleGateAction
  effectiveAction?: PRLifecycleGateAction
  actionSource?: "workflow-default" | "config-override"
}

export interface PRLifecycleNudgeConfigV3 {
  reviewMinimumAdditional: number
  reviewMaximumAdditional: number
  completionMinimumAdditional: number
  completionMaximumAdditional: number
}

export interface PRLifecycleSizeThresholdV3 {
  files: number
  semanticLines: number
  modules: number
}

export interface PRLifecycleScopeConfigV3 {
  xs: PRLifecycleSizeThresholdV3
  s: PRLifecycleSizeThresholdV3
  m: PRLifecycleSizeThresholdV3
}

export type PRLifecycleDeferredIssueModeV3 = "off" | "ask" | "automatic"

export interface PRLifecycleWorkflowConfigurationSnapshot {
  workflowConfigurations: Record<string, PRLifecycleWorkflowConfiguration>
  defaultWorkflowConfiguration: string
  nudge: PRLifecycleNudgeConfigV3
  scope: PRLifecycleScopeConfigV3
  gateCatalog: Record<string, PRLifecycleGateCatalogEntry>
  flow: PRLifecycleFlowCatalog
  flowRevision: string
  catalogRevision: string
  configRevision: string
  effects: {
    gatewayEffect: "applied" | "restart-required"
    deferredPolicyEffect: "applied" | "restart-required"
  }
}

export interface PutPRLifecycleWorkflowConfigurationsInput {
  expectedConfigRevision: string
  requestID: string
  workflowConfigurations: Record<string, PRLifecycleWorkflowConfiguration>
  defaultWorkflowConfiguration: string
  nudge: PRLifecycleNudgeConfigV3
  scope: PRLifecycleScopeConfigV3
}

export interface PRLifecycleWorkflowConfigurationIssue {
  path: string
  message: string
}

export interface PRLifecycleWorkflowConfigurationSummary {
  id: string
  name: string
  is_default: boolean
  bindings: number
  deferred_issues: PRLifecycleDeferredIssueModeV3
}

export interface PRLifecycleWorkflowConfigurationsCollectionResponse {
  workflow_configurations: PRLifecycleWorkflowConfigurationSummary[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
  config_revision: string
  effects: PRLifecycleWorkflowConfigurationEffects
}

export interface PRLifecycleWorkflowConfigurationItem extends PRLifecycleWorkflowConfiguration {
  id: string
  isDefault: boolean
}

export interface PRLifecycleWorkflowConfigurationDetailResponse {
  workflow_configuration: PRLifecycleWorkflowConfigurationItem
  gate_catalog: Record<string, PRLifecycleGateCatalogEntry>
  flow: PRLifecycleFlowCatalog
  flow_revision: string
  catalog_revision: string
  config_revision: string
  effects: PRLifecycleWorkflowConfigurationEffects
}

export interface PRLifecycleWorkflowConfigurationEffects {
  gateway_effect: "applied" | "restart_required"
  deferred_policy_effect: "applied" | "restart_required"
}

export interface PRLifecycleWorkflowConfigurationInput {
  id: string
  name: string
  bindings: PRLifecycleGateBinding[]
  deferredIssues: { mode: PRLifecycleDeferredIssueModeV3 }
  scopeDisposition?: PRLifecycleScopeDispositionConfig
}

export interface PRLifecycleWorkflowConfigurationDeleteResponse {
  deleted_ids: string[]
  failures: CollectionBulkDeleteFailure[]
  config_revision: string
  effects: PRLifecycleWorkflowConfigurationEffects
}

export const prLifecycleWorkflowConfigurationIDPattern =
  /^(?=.{1,64}$)[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/
const gateRefPattern = /^gates\.[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/

export function isPRLifecycleWorkflowConfigurationID(value: string): boolean {
  return prLifecycleWorkflowConfigurationIDPattern.test(value)
}

export function validatePRLifecycleWorkflowConfigurations(
  snapshot: Pick<
    PRLifecycleWorkflowConfigurationSnapshot,
    | "workflowConfigurations"
    | "defaultWorkflowConfiguration"
    | "nudge"
    | "scope"
  > &
    Partial<Pick<PRLifecycleWorkflowConfigurationSnapshot, "gateCatalog">>,
): PRLifecycleWorkflowConfigurationIssue[] {
  const issues: PRLifecycleWorkflowConfigurationIssue[] = []
  const configIDs = Object.keys(snapshot.workflowConfigurations)
  if (configIDs.length === 0) {
    issues.push({
      path: "workflow-configurations",
      message: "Add at least one Workflow configuration.",
    })
  }
  if (!snapshot.workflowConfigurations[snapshot.defaultWorkflowConfiguration]) {
    issues.push({
      path: "default-workflow-configuration",
      message: "The default Workflow configuration does not exist.",
    })
  }
  const names = new Map<string, string>()
  for (const [configID, config] of Object.entries(
    snapshot.workflowConfigurations,
  )) {
    const path = `workflow-configurations.${configID}`
    if (!isPRLifecycleWorkflowConfigurationID(configID)) {
      issues.push({ path, message: "Configuration ID must use kebab-case." })
    }
    if (
      !config.name ||
      config.name !== config.name.trim() ||
      new TextEncoder().encode(config.name).length > 128
    ) {
      issues.push({
        path: `${path}.name`,
        message:
          "Workflow configuration name must be trimmed, non-empty, and at most 128 bytes.",
      })
    }
    validateScopeDisposition(
      config.scopeDisposition,
      `${path}.scope-disposition`,
      issues,
    )
    const foldedName = config.name.toLowerCase()
    const previousNameOwner = names.get(foldedName)
    if (previousNameOwner !== undefined) {
      issues.push({
        path: `${path}.name`,
        message: `Workflow configuration name duplicates ${previousNameOwner} ignoring case.`,
      })
    } else {
      names.set(foldedName, configID)
    }
    if (
      config.deferredIssues.mode !== "off" &&
      config.deferredIssues.mode !== "ask" &&
      config.deferredIssues.mode !== "automatic"
    ) {
      issues.push({
        path: `${path}.deferred-issues.mode`,
        message: "Deferred issue mode must be off, ask, or automatic.",
      })
    }
    const bindingKeys = new Set<string>()
    for (const [index, binding] of config.bindings.entries()) {
      const bindingPath = `${path}.bindings.${index}`
      if (!isCanonicalWorkflowRef(binding.workflowRef)) {
        issues.push({
          path: `${bindingPath}.workflow-ref`,
          message:
            "Workflow reference must be an exact workflows/*.yml or *.yaml path.",
        })
      }
      if (
        !gateRefPattern.test(binding.gateRef) ||
        new TextEncoder().encode(binding.gateRef).byteLength > 128
      ) {
        issues.push({
          path: `${bindingPath}.gate-ref`,
          message: "Gate reference must be gates.<kebab-case-id>.",
        })
      }
      const key = `${binding.workflowRef}\u0000${binding.gateRef}`
      if (bindingKeys.has(key)) {
        issues.push({
          path: bindingPath,
          message: "This Gate already has an override.",
        })
      }
      bindingKeys.add(key)
      if (binding.action)
        validateAction(binding.action, `${bindingPath}.action`, issues)
      if (
        snapshot.gateCatalog !== undefined &&
        binding.action?.type === "ai" &&
        binding.action.session === "source"
      ) {
        const catalogEntry = Object.values(snapshot.gateCatalog ?? {}).find(
          (entry) =>
            entry.workflowRef === binding.workflowRef &&
            entry.gateRef === binding.gateRef,
        )
        if (catalogEntry?.sourceAISupported !== true) {
          issues.push({
            path: `${bindingPath}.action.session`,
            message:
              "Originating snapshots require a Gate that publishes a source-bearing finding.",
          })
        }
      }
    }
  }
  validateRange(
    snapshot.nudge.reviewMinimumAdditional,
    snapshot.nudge.reviewMaximumAdditional,
    "nudge.review",
    issues,
  )
  validateRange(
    snapshot.nudge.completionMinimumAdditional,
    snapshot.nudge.completionMaximumAdditional,
    "nudge.completion",
    issues,
  )
  for (const [grade, threshold] of Object.entries(snapshot.scope)) {
    for (const [name, value] of Object.entries(threshold)) {
      if (
        typeof value !== "number" ||
        !Number.isSafeInteger(value) ||
        value < 0
      ) {
        issues.push({
          path: `scope.${grade}.${name}`,
          message: "Scope thresholds must be non-negative integers.",
        })
      }
    }
  }
  return issues
}

function validateRange(
  minimum: number,
  maximum: number,
  path: string,
  issues: PRLifecycleWorkflowConfigurationIssue[],
) {
  if (
    !Number.isSafeInteger(minimum) ||
    !Number.isSafeInteger(maximum) ||
    minimum < 0 ||
    maximum < minimum
  ) {
    issues.push({
      path,
      message:
        "Minimum and maximum must be non-negative, with minimum no greater than maximum.",
    })
  }
}

function validateAction(
  action: PRLifecycleGateAction,
  path: string,
  issues: PRLifecycleWorkflowConfigurationIssue[],
) {
  if (action.type === "ai") {
    const sourceSession = action.session === "source"
    if (!sourceSession && !action.agentID?.trim()) {
      issues.push({
        path: `${path}.agent-id`,
        message: "AI actions require an agent ID.",
      })
    } else if (
      !sourceSession &&
      !/^(?=.{1,64}$)[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/u.test(action.agentID ?? "")
    ) {
      issues.push({
        path: `${path}.agent-id`,
        message: "AI action agent ID must use kebab-case.",
      })
    }
    if (!action.prompt?.trim()) {
      issues.push({
        path: `${path}.prompt`,
        message: "AI actions require a prompt.",
      })
    } else if (new TextEncoder().encode(action.prompt).byteLength > 32 << 10) {
      issues.push({
        path: `${path}.prompt`,
        message: "AI action prompts cannot exceed 32768 bytes.",
      })
    }
    const ephemeral = action.session === "ephemeral"
    const privateSession = action.session === "private"
    if (!ephemeral && !privateSession && !sourceSession) {
      issues.push({
        path: `${path}.session`,
        message: "AI session must be ephemeral, private, or source.",
      })
    } else if (
      (ephemeral &&
        (action.history !== "none" ||
          action.cache !== "none" ||
          action.tools !== "none")) ||
      (privateSession &&
        (action.history !== "read_only" ||
          (action.cache !== "none" && action.cache !== "session") ||
          action.tools !== "none")) ||
      (sourceSession &&
        (action.agentID !== undefined ||
          action.history !== undefined ||
          action.cache !== undefined ||
          action.tools !== undefined))
    ) {
      issues.push({
        path,
        message: ephemeral
          ? "Ephemeral AI requires history, cache, and tools set to none."
          : privateSession
            ? "Private AI requires an explicit agent, read-only history, none/session cache, and no tools."
            : "Source AI derives the originating agent, history, cache, and tools; those fields must be omitted.",
      })
    }
  }
  if (action.type === "deterministic" && !action.fields) {
    issues.push({
      path: `${path}.fields`,
      message: "Deterministic actions require field expressions.",
    })
  }
  if (action.type === "workflow" && !action.workflowRef?.trim()) {
    issues.push({
      path: `${path}.workflow-ref`,
      message: "Workflow actions require a workflow reference.",
    })
  }
  if (
    action.type === "workflow" &&
    action.workflowRef !== undefined &&
    (!isCanonicalWorkflowRef(action.workflowRef) ||
      new TextEncoder().encode(action.workflowRef).byteLength > 512)
  ) {
    issues.push({
      path: `${path}.workflow-ref`,
      message:
        "Workflow reference must be an exact workflows/*.yml or *.yaml path.",
    })
  }
  const hasAISettings =
    action.agentID !== undefined ||
    action.prompt !== undefined ||
    action.session !== undefined ||
    action.history !== undefined ||
    action.cache !== undefined ||
    action.tools !== undefined
  if (action.type !== "ai" && hasAISettings) {
    issues.push({
      path,
      message: "Only AI actions may contain AI execution settings.",
    })
  }
  if (action.type !== "deterministic" && action.fields !== undefined) {
    issues.push({
      path: `${path}.fields`,
      message: "Only deterministic actions may contain field expressions.",
    })
  }
  if (action.type !== "workflow" && action.workflowRef !== undefined) {
    issues.push({
      path: `${path}.workflow-ref`,
      message: "Only workflow actions may contain a workflow reference.",
    })
  }
}

function isCanonicalWorkflowRef(value: string): boolean {
  if (
    value !== value.trim() ||
    new TextEncoder().encode(value).byteLength > 1024 ||
    !value.startsWith("workflows/") ||
    (!value.endsWith(".yml") && !value.endsWith(".yaml")) ||
    value.includes("\\") ||
    value.includes("${{") ||
    value.startsWith("draft:") ||
    /[\0\r\n]/u.test(value)
  )
    return false
  return value
    .split("/")
    .every((part) => part !== "" && part !== "." && part !== "..")
}

export async function getPRLifecycleWorkflowConfigurations(
  signal?: AbortSignal,
): Promise<PRLifecycleWorkflowConfigurationSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/development/workflow-configurations",
      undefined,
      signal,
    ),
  )
}

const workflowConfigurationItemsPath =
  "/api/development/workflow-configurations/items"

export async function listPRLifecycleWorkflowConfigurations(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<PRLifecycleWorkflowConfigurationsCollectionResponse> {
  return projectWorkflowConfigurationCollection(
    await collectionRequest<unknown>(
      collectionListURL(workflowConfigurationItemsPath, options),
      undefined,
      signal,
    ),
  )
}

export async function getPRLifecycleWorkflowConfiguration(
  id: string,
  signal?: AbortSignal,
): Promise<PRLifecycleWorkflowConfigurationDetailResponse> {
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  const response = projectWorkflowConfigurationDetail(
    await collectionRequest<unknown>(
      `${workflowConfigurationItemsPath}/${encodeURIComponent(id)}`,
      undefined,
      signal,
    ),
  )
  if (response.workflow_configuration.id !== id) collectionMalformed()
  return response
}

export async function createPRLifecycleWorkflowConfiguration(
  configuration: PRLifecycleWorkflowConfigurationInput,
  expectedConfigRevision: string,
): Promise<PRLifecycleWorkflowConfigurationDetailResponse> {
  if (!isPRLifecycleWorkflowConfigurationID(configuration.id)) {
    collectionMalformed()
  }
  const response = projectWorkflowConfigurationDetail(
    await collectionRequest<unknown>(workflowConfigurationItemsPath, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        expected_config_revision: expectedConfigRevision,
        workflow_configuration: serializeCollectionConfig(configuration),
      }),
    }),
  )
  if (response.workflow_configuration.id !== configuration.id) {
    collectionMalformed()
  }
  return response
}

export async function updatePRLifecycleWorkflowConfiguration(
  id: string,
  configuration: PRLifecycleWorkflowConfigurationInput,
  expectedConfigRevision: string,
): Promise<PRLifecycleWorkflowConfigurationDetailResponse> {
  if (!isPRLifecycleWorkflowConfigurationID(id) || configuration.id !== id) {
    collectionMalformed()
  }
  const response = projectWorkflowConfigurationDetail(
    await collectionRequest<unknown>(
      `${workflowConfigurationItemsPath}/${encodeURIComponent(id)}`,
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
          workflow_configuration: serializeCollectionConfig(configuration),
        }),
      },
    ),
  )
  if (response.workflow_configuration.id !== id) collectionMalformed()
  return response
}

export async function makePRLifecycleWorkflowConfigurationDefault(
  id: string,
  expectedConfigRevision: string,
): Promise<PRLifecycleWorkflowConfigurationDetailResponse> {
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  const response = projectWorkflowConfigurationDetail(
    await collectionRequest<unknown>(
      `${workflowConfigurationItemsPath}/${encodeURIComponent(id)}/default`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
        }),
      },
    ),
  )
  if (
    response.workflow_configuration.id !== id ||
    !response.workflow_configuration.isDefault
  ) {
    collectionMalformed()
  }
  return response
}

export async function deletePRLifecycleWorkflowConfiguration(
  id: string,
  expectedConfigRevision: string,
): Promise<PRLifecycleWorkflowConfigurationDeleteResponse> {
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  const response = projectWorkflowConfigurationDeleteResponse(
    await collectionRequest<unknown>(
      `${workflowConfigurationItemsPath}/${encodeURIComponent(id)}`,
      {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_config_revision: expectedConfigRevision,
        }),
      },
    ),
  )
  if (
    response.deleted_ids.length !== 1 ||
    response.deleted_ids[0] !== id ||
    response.failures.length !== 0
  ) {
    collectionMalformed()
  }
  return response
}

function serializeCollectionConfig(
  configuration: PRLifecycleWorkflowConfigurationInput,
) {
  return {
    id: configuration.id,
    name: configuration.name,
    bindings: configuration.bindings.map((binding) => ({
      workflow_ref: binding.workflowRef,
      gate_ref: binding.gateRef,
      ...(binding.action
        ? { action: serializeCollectionAction(binding.action) }
        : {}),
    })),
    deferred_issues: configuration.deferredIssues,
    scope_disposition: {
      default: (configuration.scopeDisposition ?? defaultScopeDisposition())
        .default,
      by_type: (configuration.scopeDisposition ?? defaultScopeDisposition())
        .byType,
    },
  }
}

function serializeCollectionAction(action: PRLifecycleGateAction) {
  return {
    type: action.type,
    ...(action.agentID === undefined ? {} : { agent_id: action.agentID }),
    ...(action.prompt === undefined ? {} : { prompt: action.prompt }),
    ...(action.session === undefined ? {} : { session: action.session }),
    ...(action.history === undefined ? {} : { history: action.history }),
    ...(action.cache === undefined ? {} : { cache: action.cache }),
    ...(action.tools === undefined ? {} : { tools: action.tools }),
    ...(action.fields === undefined ? {} : { fields: action.fields }),
    ...(action.workflowRef === undefined
      ? {}
      : { workflow_ref: action.workflowRef }),
  }
}

function projectWorkflowConfigurationCollection(
  value: unknown,
): PRLifecycleWorkflowConfigurationsCollectionResponse {
  const root = collectionRecord(value)
  if (!Array.isArray(root.workflow_configurations)) collectionMalformed()
  const configurations = root.workflow_configurations.map(
    projectWorkflowConfigurationSummary,
  )
  rejectCollectionDuplicateIDs(
    configurations.map((configuration) => configuration.id),
  )
  return {
    workflow_configurations: configurations,
    ...projectCollectionMetadata(root),
    config_revision: collectionString(root.config_revision),
    effects: projectWorkflowCollectionEffects(root.effects),
  }
}

function projectWorkflowConfigurationSummary(
  value: unknown,
): PRLifecycleWorkflowConfigurationSummary {
  const source = collectionRecord(value)
  const id = collectionString(source.id)
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  return {
    id,
    name: collectionTrimmedUTF8String(source.name, 128),
    is_default: collectionBoolean(source.is_default),
    bindings: collectionInteger(source.bindings),
    deferred_issues: collectionDeferredIssueMode(source.deferred_issues),
  }
}

function projectWorkflowConfigurationDetail(
  value: unknown,
): PRLifecycleWorkflowConfigurationDetailResponse {
  const root = collectionRecord(value)
  const gateCatalog = collectionMap(
    root.gate_catalog,
    projectCollectionCatalogEntry,
  )
  const workflowConfiguration = projectWorkflowConfigurationItem(
    root.workflow_configuration,
  )
  const issues = validatePRLifecycleWorkflowConfigurations({
    workflowConfigurations: {
      [workflowConfiguration.id]: workflowConfiguration,
    },
    defaultWorkflowConfiguration: workflowConfiguration.id,
    nudge: {
      reviewMinimumAdditional: 0,
      reviewMaximumAdditional: 0,
      completionMinimumAdditional: 0,
      completionMaximumAdditional: 0,
    },
    scope: {
      xs: { files: 0, semanticLines: 0, modules: 0 },
      s: { files: 0, semanticLines: 0, modules: 0 },
      m: { files: 0, semanticLines: 0, modules: 0 },
    },
    gateCatalog,
  })
  if (issues.length > 0) collectionMalformed()
  return {
    workflow_configuration: workflowConfiguration,
    gate_catalog: gateCatalog,
    flow: projectPRLifecycleFlowCatalog(root.flow),
    flow_revision: collectionString(root.flow_revision),
    catalog_revision: collectionString(root.catalog_revision),
    config_revision: collectionString(root.config_revision),
    effects: projectWorkflowCollectionEffects(root.effects),
  }
}

function projectWorkflowConfigurationItem(
  value: unknown,
): PRLifecycleWorkflowConfigurationItem {
  const source = collectionRecord(value)
  const id = collectionString(source.id)
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  if (!Array.isArray(source.bindings) || source.bindings.length > 8192) {
    collectionMalformed()
  }
  return {
    id,
    isDefault: collectionBoolean(source.is_default),
    name: collectionTrimmedUTF8String(source.name, 128),
    bindings: source.bindings.map(projectCollectionBinding),
    deferredIssues: {
      mode: collectionDeferredIssueMode(source.deferred_issues),
    },
    scopeDisposition: projectCollectionScopeDisposition(
      source.scope_disposition,
    ),
  }
}

function projectCollectionBinding(value: unknown): PRLifecycleGateBinding {
  const source = collectionRecord(value)
  const workflowRef = collectionBoundedUTF8String(source.workflow_ref, 1024)
  const gateRef = collectionBoundedUTF8String(source.gate_ref, 128)
  if (!isCanonicalWorkflowRef(workflowRef) || !gateRefPattern.test(gateRef)) {
    collectionMalformed()
  }
  return {
    workflowRef,
    gateRef,
    ...(source.action === undefined
      ? {}
      : { action: projectCollectionAction(source.action) }),
  }
}

function projectCollectionAction(value: unknown): PRLifecycleGateAction {
  const source = collectionRecord(value)
  const type = collectionString(source.type)
  if (
    type !== "human" &&
    type !== "ai" &&
    type !== "deterministic" &&
    type !== "workflow"
  ) {
    collectionMalformed()
  }
  const action: PRLifecycleGateAction = {
    type,
    ...collectionOptionalBoundedString(source.agent_id, "agentID", 64),
    ...collectionOptionalBoundedString(source.prompt, "prompt", 32 << 10),
    ...collectionOptionalEnum(source.session, "session", [
      "ephemeral",
      "private",
      "source",
    ] as const),
    ...collectionOptionalEnum(source.history, "history", [
      "none",
      "read_only",
      "read_write",
    ] as const),
    ...collectionOptionalEnum(source.cache, "cache", [
      "none",
      "session",
      "agent",
    ] as const),
    ...collectionOptionalEnum(source.tools, "tools", [
      "none",
      "inherit",
    ] as const),
    ...(source.fields === undefined
      ? {}
      : { fields: collectionRecord(source.fields) }),
    ...collectionOptionalBoundedString(source.workflow_ref, "workflowRef", 512),
  }
  const issues: PRLifecycleWorkflowConfigurationIssue[] = []
  validateAction(action, "action", issues)
  if (issues.length > 0) collectionMalformed()
  return action
}

function projectCollectionScopeDisposition(
  value: unknown,
): PRLifecycleScopeDispositionConfig {
  if (value === undefined) return defaultScopeDisposition()
  const source = collectionRecord(value)
  const byType = collectionRecord(source.by_type)
  const supported = new Set([
    "fix",
    "feature",
    "refactor",
    "documentation",
    "test",
  ])
  if (Object.keys(byType).some((key) => !supported.has(key))) {
    collectionMalformed()
  }
  return {
    default: projectCollectionScopeRule(source.default),
    byType: Object.fromEntries(
      Object.entries(byType).map(([key, rule]) => [
        key,
        projectCollectionScopeRule(rule),
      ]),
    ) as PRLifecycleScopeDispositionConfig["byType"],
  }
}

function projectCollectionScopeRule(
  value: unknown,
): PRLifecycleScopeDispositionRule {
  const source = collectionRecord(value)
  const mode = collectionString(source.mode)
  if (mode !== "strict" && mode !== "relaxed") collectionMalformed()
  return {
    mode,
    prompt: collectionScopePrompt(source.prompt),
  }
}

function projectCollectionCatalogEntry(
  value: unknown,
): PRLifecycleGateCatalogEntry {
  const source = collectionRecord(value)
  const workflowRef = collectionBoundedUTF8String(source.workflow_ref, 1024)
  const gateRef = collectionBoundedUTF8String(source.gate_ref, 128)
  const prompt = collectionNonblankUTF8String(source.prompt, 16 << 10)
  if (!isCanonicalWorkflowRef(workflowRef) || !gateRefPattern.test(gateRef)) {
    collectionMalformed()
  }
  const actionSource = source.action_source
  if (
    actionSource !== undefined &&
    actionSource !== "workflow-default" &&
    actionSource !== "config-override"
  ) {
    collectionMalformed()
  }
  return {
    workflowRef,
    gateRef,
    sourceAISupported: collectionBoolean(source.source_ai_supported),
    prompt,
    ...(source.fields === undefined
      ? {}
      : { fields: projectCollectionCatalogFields(source.fields) }),
    ...(source.workflow_revision === undefined
      ? {}
      : { workflowRevision: collectionString(source.workflow_revision) }),
    ...(source.default_action === undefined
      ? {}
      : { defaultAction: projectCollectionAction(source.default_action) }),
    ...(source.effective_action === undefined
      ? {}
      : { effectiveAction: projectCollectionAction(source.effective_action) }),
    ...(actionSource === undefined ? {} : { actionSource }),
  }
}

function projectCollectionCatalogFields(
  value: unknown,
): PRLifecycleGateCatalogField[] {
  if (!Array.isArray(value) || value.length > 64) collectionMalformed()
  const ids = new Set<string>()
  return value.map((field) => {
    const source = collectionRecord(field)
    const id = collectionGateID(source.id)
    if (ids.has(id)) collectionMalformed()
    ids.add(id)
    const type = collectionString(source.type)
    const label = collectionNonblankUTF8String(source.label, 4 << 10)
    const required = collectionBoolean(source.required)
    if (type === "short-text" || type === "long-text" || type === "boolean") {
      return { id, type, label, required }
    }
    if (type !== "select" || !Array.isArray(source.options)) {
      collectionMalformed()
    }
    const minSelections = collectionOptionalInteger(source.min_selections)
    const maxSelections = collectionInteger(source.max_selections)
    const options = source.options.map((option) => {
      const entry = collectionRecord(option)
      return {
        id: collectionGateID(entry.id),
        label: collectionNonblankUTF8String(entry.label, 4 << 10),
      }
    })
    rejectCollectionDuplicateIDs(options.map((option) => option.id))
    if (
      options.length === 0 ||
      options.length > 128 ||
      maxSelections < 1 ||
      maxSelections > options.length ||
      minSelections > maxSelections
    ) {
      collectionMalformed()
    }
    return {
      id,
      type,
      label,
      required: required || minSelections > 0,
      minSelections,
      maxSelections,
      options,
    }
  })
}

function projectWorkflowCollectionEffects(
  value: unknown,
): PRLifecycleWorkflowConfigurationEffects {
  const source = collectionRecord(value)
  return {
    gateway_effect: collectionEffect(source.gateway_effect),
    deferred_policy_effect: collectionEffect(source.deferred_policy_effect),
  }
}

function projectWorkflowConfigurationDeleteResponse(
  value: unknown,
): PRLifecycleWorkflowConfigurationDeleteResponse {
  const root = collectionRecord(value)
  if (!Array.isArray(root.deleted_ids) || !Array.isArray(root.failures)) {
    collectionMalformed()
  }
  const deletedIDs = root.deleted_ids.map((id) => collectionConfigurationID(id))
  const failures = root.failures.map((failure): CollectionBulkDeleteFailure => {
    const source = collectionRecord(failure)
    const blockers = source.blockers
    if (blockers !== undefined && !Array.isArray(blockers)) {
      collectionMalformed()
    }
    return {
      id: collectionConfigurationID(source.id),
      code: collectionCode(source.code),
      ...(Array.isArray(blockers)
        ? { blockers: blockers.map((blocker) => collectionString(blocker)) }
        : {}),
    }
  })
  rejectCollectionDuplicateIDs(deletedIDs)
  rejectCollectionDuplicateIDs(failures.map((failure) => failure.id))
  if (failures.some((failure) => deletedIDs.includes(failure.id))) {
    collectionMalformed()
  }
  return {
    deleted_ids: deletedIDs,
    failures,
    config_revision: collectionString(root.config_revision),
    effects: projectWorkflowCollectionEffects(root.effects),
  }
}

function collectionDeferredIssueMode(
  value: unknown,
): PRLifecycleDeferredIssueModeV3 {
  const candidate =
    typeof value === "object" && value !== null && !Array.isArray(value)
      ? collectionRecord(value).mode
      : value
  if (candidate !== "off" && candidate !== "ask" && candidate !== "automatic") {
    collectionMalformed()
  }
  return candidate
}

function projectCollectionMetadata(root: Record<string, unknown>) {
  const schema = projectCollectionQuerySchema(root.query_schema, [
    { field: "name", direction: "ASC" },
  ])
  return {
    total: collectionInteger(root.total),
    canonical_query: collectionCanonicalQuery(root.canonical_query),
    query_schema: schema,
    ...(root.next_cursor === undefined || root.next_cursor === ""
      ? {}
      : { next_cursor: collectionString(root.next_cursor) }),
  }
}

function collectionMap<T>(
  value: unknown,
  project: (value: unknown) => T,
): Record<string, T> {
  return Object.fromEntries(
    Object.entries(collectionRecord(value)).map(([key, entry]) => [
      key,
      project(entry),
    ]),
  )
}

function collectionRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    collectionMalformed()
  }
  return value as Record<string, unknown>
}

function collectionString(value: unknown): string {
  return collectionBoundedUTF8String(value, 4096)
}

function collectionBoundedUTF8String(
  value: unknown,
  maximumBytes: number,
  allowEmpty = false,
): string {
  if (
    typeof value !== "string" ||
    (!allowEmpty && value.length === 0) ||
    hasUnpairedSurrogate(value) ||
    new TextEncoder().encode(value).byteLength > maximumBytes
  ) {
    collectionMalformed()
  }
  return value
}

function collectionTrimmedUTF8String(
  value: unknown,
  maximumBytes: number,
): string {
  const result = collectionBoundedUTF8String(value, maximumBytes)
  if (result !== result.trim()) collectionMalformed()
  return result
}

function collectionNonblankUTF8String(
  value: unknown,
  maximumBytes: number,
): string {
  const result = collectionBoundedUTF8String(value, maximumBytes)
  if (result.trim() === "") collectionMalformed()
  return result
}

function collectionScopePrompt(value: unknown): string {
  if (value === undefined) return ""
  const result = collectionBoundedUTF8String(value, 8 << 10, true)
  if (result !== result.trim()) collectionMalformed()
  return result
}

function collectionGateID(value: unknown): string {
  const result = collectionBoundedUTF8String(value, 128)
  if (!/^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/u.test(result)) {
    collectionMalformed()
  }
  return result
}

function collectionCanonicalQuery(value: unknown): string {
  const query = collectionBoundedUTF8String(value, 4096)
  if (/\p{Cc}/u.test(query)) collectionMalformed()
  return query
}

function collectionConfigurationID(value: unknown): string {
  const id = collectionString(value)
  if (!isPRLifecycleWorkflowConfigurationID(id)) collectionMalformed()
  return id
}

function collectionCode(value: unknown): string {
  const code = collectionString(value)
  if (!/^[a-z0-9_.-]{1,64}$/u.test(code)) collectionMalformed()
  return code
}

function collectionInteger(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0) {
    collectionMalformed()
  }
  return value as number
}

function collectionOptionalInteger(value: unknown): number {
  return value === undefined ? 0 : collectionInteger(value)
}

function collectionBoolean(value: unknown): boolean {
  if (typeof value !== "boolean") collectionMalformed()
  return value
}

function collectionEffect(
  value: unknown,
): PRLifecycleWorkflowConfigurationEffects["gateway_effect"] {
  if (value !== "applied" && value !== "restart_required") {
    collectionMalformed()
  }
  return value
}

function collectionOptionalBoundedString<K extends string>(
  value: unknown,
  key: K,
  maximumBytes: number,
): Partial<Record<K, string>> {
  return value === undefined
    ? {}
    : ({
        [key]: collectionBoundedUTF8String(value, maximumBytes, true),
      } as Partial<Record<K, string>>)
}

function collectionOptionalEnum<
  K extends string,
  const Values extends readonly string[],
>(value: unknown, key: K, values: Values): Partial<Record<K, Values[number]>> {
  if (value === undefined) return {}
  const candidate = collectionString(value)
  if (!(values as readonly string[]).includes(candidate)) collectionMalformed()
  return { [key]: candidate } as Partial<Record<K, Values[number]>>
}

function rejectCollectionDuplicateIDs(ids: string[]) {
  if (new Set(ids).size !== ids.length) collectionMalformed()
}

function hasUnpairedSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return true
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true
    }
  }
  return false
}

function collectionMalformed(): never {
  throw new CollectionAPIError(
    502,
    "The server returned an invalid response.",
    {
      code: "malformed_response",
    },
  )
}

export async function putPRLifecycleWorkflowConfigurations(
  input: PutPRLifecycleWorkflowConfigurationsInput,
  signal?: AbortSignal,
): Promise<PRLifecycleWorkflowConfigurationSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/development/workflow-configurations",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(serializeInput(input)),
      },
      signal,
    ),
  )
}

function serializeInput(input: PutPRLifecycleWorkflowConfigurationsInput) {
  return {
    "expected-config-revision": input.expectedConfigRevision,
    "request-id": input.requestID,
    "workflow-configurations": Object.fromEntries(
      Object.entries(input.workflowConfigurations).map(([id, config]) => [
        id,
        serializeConfig(config),
      ]),
    ),
    "default-workflow-configuration": input.defaultWorkflowConfiguration,
    nudge: {
      "review-minimum-additional": input.nudge.reviewMinimumAdditional,
      "review-maximum-additional": input.nudge.reviewMaximumAdditional,
      "completion-minimum-additional": input.nudge.completionMinimumAdditional,
      "completion-maximum-additional": input.nudge.completionMaximumAdditional,
    },
    scope: Object.fromEntries(
      Object.entries(input.scope).map(([grade, threshold]) => [
        grade,
        {
          files: threshold.files,
          "semantic-lines": threshold.semanticLines,
          modules: threshold.modules,
        },
      ]),
    ),
  }
}

function serializeConfig(config: PRLifecycleWorkflowConfiguration) {
  return {
    name: config.name,
    "deferred-issues": config.deferredIssues,
    "scope-disposition": {
      default: (config.scopeDisposition ?? defaultScopeDisposition()).default,
      "by-type": (config.scopeDisposition ?? defaultScopeDisposition()).byType,
    },
    bindings: config.bindings.map((binding) => ({
      "workflow-ref": binding.workflowRef,
      "gate-ref": binding.gateRef,
      ...(binding.action ? { action: serializeAction(binding.action) } : {}),
    })),
  }
}

function serializeAction(action: PRLifecycleGateAction) {
  return {
    type: action.type,
    ...(action.agentID === undefined ? {} : { "agent-id": action.agentID }),
    ...(action.prompt === undefined ? {} : { prompt: action.prompt }),
    ...(action.session === undefined ? {} : { session: action.session }),
    ...(action.history === undefined ? {} : { history: action.history }),
    ...(action.cache === undefined ? {} : { cache: action.cache }),
    ...(action.tools === undefined ? {} : { tools: action.tools }),
    ...(action.fields === undefined ? {} : { fields: action.fields }),
    ...(action.workflowRef === undefined
      ? {}
      : { "workflow-ref": action.workflowRef }),
  }
}

function projectSnapshot(
  value: unknown,
): PRLifecycleWorkflowConfigurationSnapshot {
  const root = asRecord(value)
  onlyKeys(root, [
    "workflow-configurations",
    "default-workflow-configuration",
    "nudge",
    "scope",
    "gate-catalog",
    "flow",
    "flow-revision",
    "catalog-revision",
    "config-revision",
    "effects",
  ])
  const snapshot: PRLifecycleWorkflowConfigurationSnapshot = {
    workflowConfigurations: projectMap(
      root["workflow-configurations"],
      projectConfig,
    ),
    defaultWorkflowConfiguration: stringValue(
      root["default-workflow-configuration"],
    ),
    nudge: projectNudge(root.nudge),
    scope: projectScope(root.scope),
    gateCatalog: projectMap(root["gate-catalog"], projectCatalogEntry),
    flow: projectPRLifecycleFlowCatalog(root.flow),
    flowRevision: stringValue(root["flow-revision"]),
    catalogRevision: stringValue(root["catalog-revision"]),
    configRevision: stringValue(root["config-revision"]),
    effects: projectEffects(root.effects),
  }
  if (
    validatePRLifecycleWorkflowConfigurations({
      workflowConfigurations: snapshot.workflowConfigurations,
      defaultWorkflowConfiguration: snapshot.defaultWorkflowConfiguration,
      nudge: snapshot.nudge,
      scope: snapshot.scope,
    }).length > 0
  )
    malformed()
  return snapshot
}

function projectConfig(value: unknown): PRLifecycleWorkflowConfiguration {
  const source = asRecord(value)
  onlyKeys(source, ["name", "bindings", "deferred-issues", "scope-disposition"])
  if (!Array.isArray(source.bindings)) malformed()
  return {
    name: stringValue(source.name),
    bindings: source.bindings.map(projectBinding),
    deferredIssues: projectDeferredIssues(source["deferred-issues"]),
    scopeDisposition: projectScopeDisposition(source["scope-disposition"]),
  }
}

function projectScopeDisposition(
  value: unknown,
): PRLifecycleScopeDispositionConfig {
  if (value === undefined) {
    return { default: { mode: "strict", prompt: "" }, byType: {} }
  }
  const source = asRecord(value)
  onlyKeys(source, ["default", "by-type"])
  const projectRule = (raw: unknown): PRLifecycleScopeDispositionRule => {
    const rule = asRecord(raw)
    onlyKeys(rule, ["mode", "prompt"])
    const mode = stringValue(rule.mode)
    if (mode !== "strict" && mode !== "relaxed") malformed()
    if (typeof rule.prompt !== "string") malformed()
    return { mode, prompt: rule.prompt }
  }
  const byTypeSource = asRecord(source["by-type"])
  onlyKeys(byTypeSource, [
    "fix",
    "feature",
    "refactor",
    "documentation",
    "test",
  ])
  return {
    default: projectRule(source.default),
    byType: Object.fromEntries(
      Object.entries(byTypeSource).map(([key, rule]) => [
        key,
        projectRule(rule),
      ]),
    ) as PRLifecycleScopeDispositionConfig["byType"],
  }
}

function validateScopeDisposition(
  value: PRLifecycleScopeDispositionConfig | undefined,
  path: string,
  issues: PRLifecycleWorkflowConfigurationIssue[],
) {
  value ??= defaultScopeDisposition()
  const rules = [
    ["default", value.default],
    ...Object.entries(value.byType),
  ] as Array<[string, PRLifecycleScopeDispositionRule]>
  for (const [kind, rule] of rules) {
    if (rule.mode !== "strict" && rule.mode !== "relaxed") {
      issues.push({
        path: `${path}.${kind}.mode`,
        message: "Mode must be strict or relaxed.",
      })
    }
    if (
      rule.prompt !== rule.prompt.trim() ||
      new TextEncoder().encode(rule.prompt).length > 8192
    ) {
      issues.push({
        path: `${path}.${kind}.prompt`,
        message: "Prompt must be trimmed and at most 8192 bytes.",
      })
    }
  }
}

export function defaultScopeDisposition(): PRLifecycleScopeDispositionConfig {
  return { default: { mode: "strict", prompt: "" }, byType: {} }
}

function projectBinding(value: unknown): PRLifecycleGateBinding {
  const source = asRecord(value)
  onlyKeys(source, ["workflow-ref", "gate-ref", "action"])
  return {
    workflowRef: stringValue(source["workflow-ref"]),
    gateRef: stringValue(source["gate-ref"]),
    ...(source.action === undefined
      ? {}
      : { action: projectAction(source.action) }),
  }
}

function projectAction(value: unknown): PRLifecycleGateAction {
  const source = asRecord(value)
  onlyKeys(source, [
    "type",
    "agent-id",
    "prompt",
    "session",
    "history",
    "cache",
    "tools",
    "fields",
    "workflow-ref",
  ])
  const type = stringValue(source.type)
  if (
    type !== "human" &&
    type !== "ai" &&
    type !== "deterministic" &&
    type !== "workflow"
  )
    malformed()
  const tools = source.tools
  if (tools !== undefined && typeof tools !== "string") malformed()
  const action: PRLifecycleGateAction = {
    type,
    ...optionalString(source, "agent-id", "agentID"),
    ...optionalString(source, "prompt", "prompt"),
    ...optionalEnumProperty(source, "session", "session", [
      "ephemeral",
      "private",
      "source",
    ] as const),
    ...optionalEnumProperty(source, "history", "history", [
      "none",
      "read_only",
      "read_write",
    ] as const),
    ...optionalEnumProperty(source, "cache", "cache", [
      "none",
      "session",
      "agent",
    ] as const),
    ...optionalEnumProperty(source, "tools", "tools", [
      "none",
      "inherit",
    ] as const),
    ...(source.fields === undefined ? {} : { fields: asRecord(source.fields) }),
    ...optionalString(source, "workflow-ref", "workflowRef"),
  }
  const issues: PRLifecycleWorkflowConfigurationIssue[] = []
  validateAction(action, "action", issues)
  if (issues.length > 0) malformed()
  return action
}

function projectCatalogEntry(value: unknown): PRLifecycleGateCatalogEntry {
  const source = asRecord(value)
  onlyKeys(source, [
    "workflow-ref",
    "gate-ref",
    "source-ai-supported",
    "prompt",
    "fields",
    "workflow-revision",
    "default-action",
    "effective-action",
    "action-source",
  ])
  const actionSource = source["action-source"]
  if (
    actionSource !== undefined &&
    actionSource !== "workflow-default" &&
    actionSource !== "config-override"
  )
    malformed()
  return {
    workflowRef: stringValue(source["workflow-ref"]),
    gateRef: stringValue(source["gate-ref"]),
    sourceAISupported: booleanValue(source["source-ai-supported"]),
    ...optionalString(source, "prompt", "prompt"),
    ...(source.fields === undefined
      ? {}
      : { fields: projectCatalogFields(source.fields) }),
    ...optionalString(source, "workflow-revision", "workflowRevision"),
    ...(source["default-action"] === undefined
      ? {}
      : { defaultAction: projectAction(source["default-action"]) }),
    ...(source["effective-action"] === undefined
      ? {}
      : { effectiveAction: projectAction(source["effective-action"]) }),
    ...(actionSource === undefined ? {} : { actionSource }),
  }
}

function projectCatalogFields(value: unknown): PRLifecycleGateCatalogField[] {
  if (!Array.isArray(value) || value.length > 64) malformed()
  const fieldIDs = new Set<string>()
  return value.map((entry) => {
    const source = asRecord(entry)
    onlyKeys(source, [
      "id",
      "type",
      "label",
      "required",
      "min-selections",
      "max-selections",
      "options",
    ])
    const id = stringValue(source.id)
    if (fieldIDs.has(id)) malformed()
    fieldIDs.add(id)
    const type = stringValue(source.type)
    const label = stringValue(source.label)
    const required =
      source.required === undefined ? false : booleanValue(source.required)
    if (type === "short-text" || type === "long-text" || type === "boolean") {
      if (
        source["min-selections"] !== undefined ||
        source["max-selections"] !== undefined ||
        source.options !== undefined
      ) {
        malformed()
      }
      return { id, type, label, required }
    }
    if (type !== "select" || !Array.isArray(source.options)) malformed()
    const minSelections = integerValue(source["min-selections"])
    const maxSelections = integerValue(source["max-selections"])
    if (
      maxSelections < 1 ||
      minSelections > maxSelections ||
      source.options.length === 0 ||
      source.options.length > 128 ||
      maxSelections > source.options.length
    ) {
      malformed()
    }
    const optionIDs = new Set<string>()
    const options = source.options.map((entry) => {
      const option = asRecord(entry)
      onlyKeys(option, ["id", "label"])
      const optionID = stringValue(option.id)
      if (optionIDs.has(optionID)) malformed()
      optionIDs.add(optionID)
      return { id: optionID, label: stringValue(option.label) }
    })
    return {
      id,
      type,
      label,
      required: required || minSelections > 0,
      minSelections,
      maxSelections,
      options,
    }
  })
}

function projectNudge(value: unknown): PRLifecycleNudgeConfigV3 {
  const source = asRecord(value)
  onlyKeys(source, [
    "review-minimum-additional",
    "review-maximum-additional",
    "completion-minimum-additional",
    "completion-maximum-additional",
  ])
  return {
    reviewMinimumAdditional: integerValue(source["review-minimum-additional"]),
    reviewMaximumAdditional: integerValue(source["review-maximum-additional"]),
    completionMinimumAdditional: integerValue(
      source["completion-minimum-additional"],
    ),
    completionMaximumAdditional: integerValue(
      source["completion-maximum-additional"],
    ),
  }
}

function projectScope(value: unknown): PRLifecycleScopeConfigV3 {
  const source = asRecord(value)
  onlyKeys(source, ["xs", "s", "m"])
  return {
    xs: projectThreshold(source.xs),
    s: projectThreshold(source.s),
    m: projectThreshold(source.m),
  }
}

function projectThreshold(value: unknown): PRLifecycleSizeThresholdV3 {
  const source = asRecord(value)
  onlyKeys(source, ["files", "semantic-lines", "modules"])
  return {
    files: integerValue(source.files),
    semanticLines: integerValue(source["semantic-lines"]),
    modules: integerValue(source.modules),
  }
}

function projectDeferredIssues(value: unknown): {
  mode: PRLifecycleDeferredIssueModeV3
} {
  const source = asRecord(value)
  onlyKeys(source, ["mode"])
  const mode = stringValue(source.mode)
  if (mode !== "off" && mode !== "ask" && mode !== "automatic") malformed()
  return { mode }
}

function projectEffects(
  value: unknown,
): PRLifecycleWorkflowConfigurationSnapshot["effects"] {
  const source = asRecord(value)
  onlyKeys(source, ["gateway-effect", "deferred-policy-effect"])
  const gatewayEffect = stringValue(source["gateway-effect"])
  const deferredPolicyEffect = stringValue(source["deferred-policy-effect"])
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart-required")
    malformed()
  if (
    deferredPolicyEffect !== "applied" &&
    deferredPolicyEffect !== "restart-required"
  )
    malformed()
  return { gatewayEffect, deferredPolicyEffect }
}

function projectMap<T>(
  value: unknown,
  project: (value: unknown) => T,
): Record<string, T> {
  const source = asRecord(value)
  return Object.fromEntries(
    Object.entries(source).map(([key, entry]) => [key, project(entry)]),
  )
}

function optionalString<K extends string>(
  source: Record<string, unknown>,
  wireKey: string,
  key: K,
): Partial<Record<K, string>> {
  return source[wireKey] === undefined
    ? {}
    : ({ [key]: stringValue(source[wireKey]) } as Partial<Record<K, string>>)
}

function optionalEnumProperty<
  K extends string,
  const Values extends readonly string[],
>(
  source: Record<string, unknown>,
  wireKey: string,
  key: K,
  values: Values,
): Partial<Record<K, Values[number]>> {
  if (source[wireKey] === undefined) return {}
  const value = stringValue(source[wireKey])
  if (!(values as readonly string[]).includes(value)) malformed()
  return { [key]: value } as Partial<Record<K, Values[number]>>
}

function asRecord(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    malformed()
  return value as Record<string, unknown>
}

function onlyKeys(source: Record<string, unknown>, keys: string[]) {
  const allowed = new Set(keys)
  if (Object.keys(source).some((key) => !allowed.has(key))) malformed()
}

function stringValue(value: unknown): string {
  if (typeof value !== "string" || value.length === 0) malformed()
  return value
}

function integerValue(value: unknown): number {
  if (!Number.isSafeInteger(value) || (value as number) < 0) malformed()
  return value as number
}

function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") malformed()
  return value
}

function malformed(): never {
  throw new PRWorkspaceAPIError("malformed_response", 502)
}
