import {
  type PRLifecycleFlowCatalog,
  projectPRLifecycleFlowCatalog,
} from "@/api/pr-lifecycle-flow"
import {
  PRWorkspaceAPIError,
  requestPRWorkspaceJSON,
} from "@/api/pr-workspaces"

export type PRLifecycleGateActionType =
  | "human"
  | "ai"
  | "deterministic"
  | "workflow"

export interface PRLifecycleGateAction {
  type: PRLifecycleGateActionType
  agentID?: string
  prompt?: string
  session?: "ephemeral" | "private"
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

export interface PRLifecycleGateConfig {
  name: string
  bindings: PRLifecycleGateBinding[]
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

export interface PRLifecycleGateConfigSnapshot {
  gateConfigs: Record<string, PRLifecycleGateConfig>
  defaultGateConfig: string
  repositoryAssignments: Record<string, string>
  nudge: PRLifecycleNudgeConfigV3
  scope: PRLifecycleScopeConfigV3
  deferredIssues: { mode: PRLifecycleDeferredIssueModeV3 }
  gateCatalog: Record<string, PRLifecycleGateCatalogEntry>
  flow: PRLifecycleFlowCatalog
  flowRevision: string
  catalogRevision: string
  configRevision: string
  effects: { gatewayEffect: "applied" | "restart-required" }
}

export interface PutPRLifecycleGateConfigsInput {
  expectedConfigRevision: string
  requestID: string
  gateConfigs: Record<string, PRLifecycleGateConfig>
  defaultGateConfig: string
  repositoryAssignments: Record<string, string>
  nudge: PRLifecycleNudgeConfigV3
  scope: PRLifecycleScopeConfigV3
  deferredIssues: { mode: PRLifecycleDeferredIssueModeV3 }
}

export interface PRLifecycleGateConfigIssue {
  path: string
  message: string
}

export const prLifecycleGateConfigIDPattern = /^[a-z][a-z0-9-]{0,63}$/
const gateRefPattern = /^gates\.[a-z][a-z0-9-]{0,63}$/

export function isPRLifecycleGateConfigID(value: string): boolean {
  return prLifecycleGateConfigIDPattern.test(value)
}

export function validatePRLifecycleGateConfigs(
  snapshot: Pick<
    PRLifecycleGateConfigSnapshot,
    | "gateConfigs"
    | "defaultGateConfig"
    | "repositoryAssignments"
    | "nudge"
    | "scope"
  >,
): PRLifecycleGateConfigIssue[] {
  const issues: PRLifecycleGateConfigIssue[] = []
  const configIDs = Object.keys(snapshot.gateConfigs)
  if (configIDs.length === 0) {
    issues.push({
      path: "gate-configs",
      message: "Add at least one Gate configuration.",
    })
  }
  if (!snapshot.gateConfigs[snapshot.defaultGateConfig]) {
    issues.push({
      path: "default-gate-config",
      message: "The default Gate configuration does not exist.",
    })
  }
  for (const [configID, config] of Object.entries(snapshot.gateConfigs)) {
    const path = `gate-configs.${configID}`
    if (!isPRLifecycleGateConfigID(configID)) {
      issues.push({ path, message: "Configuration ID must use kebab-case." })
    }
    if (!config.name.trim()) {
      issues.push({
        path: `${path}.name`,
        message: "Configuration name is required.",
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
      if (!gateRefPattern.test(binding.gateRef)) {
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
    }
  }
  for (const [repository, configID] of Object.entries(
    snapshot.repositoryAssignments,
  )) {
    if (!repository.trim()) {
      issues.push({
        path: "repository-assignments",
        message: "Repository assignment key is required.",
      })
    }
    if (!snapshot.gateConfigs[configID]) {
      issues.push({
        path: `repository-assignments.${repository}`,
        message: "Repository assignment references a missing configuration.",
      })
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
  issues: PRLifecycleGateConfigIssue[],
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
  issues: PRLifecycleGateConfigIssue[],
) {
  if (action.type === "ai") {
    if (!action.agentID?.trim()) {
      issues.push({
        path: `${path}.agent-id`,
        message: "AI actions require an agent ID.",
      })
    }
    if (!action.prompt?.trim()) {
      issues.push({
        path: `${path}.prompt`,
        message: "AI actions require a prompt.",
      })
    }
    const ephemeral = action.session === "ephemeral"
    const privateSession = action.session === "private"
    if (!ephemeral && !privateSession) {
      issues.push({
        path: `${path}.session`,
        message: "AI session must be ephemeral or private.",
      })
    } else if (
      action.tools !== "none" ||
      (ephemeral && (action.history !== "none" || action.cache !== "none")) ||
      (privateSession &&
        (action.history !== "read_only" ||
          (action.cache !== "none" && action.cache !== "session")))
    ) {
      issues.push({
        path,
        message: ephemeral
          ? "Ephemeral AI requires history, cache, and tools set to none."
          : "Private AI requires read-only history, none/session cache, and no tools.",
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
    !isCanonicalWorkflowRef(action.workflowRef)
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
    !value.startsWith("workflows/") ||
    (!value.endsWith(".yml") && !value.endsWith(".yaml")) ||
    value.includes("\\")
  )
    return false
  return value
    .split("/")
    .every((part) => part !== "" && part !== "." && part !== "..")
}

export async function getPRLifecycleGateConfigs(
  signal?: AbortSignal,
): Promise<PRLifecycleGateConfigSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/pr-lifecycle/gate-configs",
      undefined,
      signal,
    ),
  )
}

export async function putPRLifecycleGateConfigs(
  input: PutPRLifecycleGateConfigsInput,
  signal?: AbortSignal,
): Promise<PRLifecycleGateConfigSnapshot> {
  return projectSnapshot(
    await requestPRWorkspaceJSON<unknown>(
      "/api/pr-lifecycle/gate-configs",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(serializeInput(input)),
      },
      signal,
    ),
  )
}

function serializeInput(input: PutPRLifecycleGateConfigsInput) {
  return {
    "expected-config-revision": input.expectedConfigRevision,
    "request-id": input.requestID,
    "gate-configs": Object.fromEntries(
      Object.entries(input.gateConfigs).map(([id, config]) => [
        id,
        serializeConfig(config),
      ]),
    ),
    "default-gate-config": input.defaultGateConfig,
    "repository-assignments": input.repositoryAssignments,
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
    "deferred-issues": input.deferredIssues,
  }
}

function serializeConfig(config: PRLifecycleGateConfig) {
  return {
    name: config.name,
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

function projectSnapshot(value: unknown): PRLifecycleGateConfigSnapshot {
  const root = asRecord(value)
  onlyKeys(root, [
    "gate-configs",
    "default-gate-config",
    "repository-assignments",
    "nudge",
    "scope",
    "deferred-issues",
    "gate-catalog",
    "flow",
    "flow-revision",
    "catalog-revision",
    "config-revision",
    "effects",
  ])
  const snapshot: PRLifecycleGateConfigSnapshot = {
    gateConfigs: projectMap(root["gate-configs"], projectConfig),
    defaultGateConfig: stringValue(root["default-gate-config"]),
    repositoryAssignments: projectMap(
      root["repository-assignments"],
      stringValue,
    ),
    nudge: projectNudge(root.nudge),
    scope: projectScope(root.scope),
    deferredIssues: projectDeferredIssues(root["deferred-issues"]),
    gateCatalog: projectMap(root["gate-catalog"], projectCatalogEntry),
    flow: projectPRLifecycleFlowCatalog(root.flow),
    flowRevision: stringValue(root["flow-revision"]),
    catalogRevision: stringValue(root["catalog-revision"]),
    configRevision: stringValue(root["config-revision"]),
    effects: projectEffects(root.effects),
  }
  if (validatePRLifecycleGateConfigs(snapshot).length > 0) malformed()
  return snapshot
}

function projectConfig(value: unknown): PRLifecycleGateConfig {
  const source = asRecord(value)
  onlyKeys(source, ["name", "bindings"])
  if (!Array.isArray(source.bindings)) malformed()
  return {
    name: stringValue(source.name),
    bindings: source.bindings.map(projectBinding),
  }
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
  const issues: PRLifecycleGateConfigIssue[] = []
  validateAction(action, "action", issues)
  if (issues.length > 0) malformed()
  return action
}

function projectCatalogEntry(value: unknown): PRLifecycleGateCatalogEntry {
  const source = asRecord(value)
  onlyKeys(source, [
    "workflow-ref",
    "gate-ref",
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
): PRLifecycleGateConfigSnapshot["effects"] {
  const source = asRecord(value)
  onlyKeys(source, ["gateway-effect"])
  const gatewayEffect = stringValue(source["gateway-effect"])
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart-required")
    malformed()
  return { gatewayEffect }
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
