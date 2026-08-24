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

export const prLifecycleWorkflowConfigurationIDPattern =
  /^(?=.{1,64}$)[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/
const gateRefPattern = /^gates\.[a-z][a-z0-9-]{0,63}$/

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
    }
    if (!action.prompt?.trim()) {
      issues.push({
        path: `${path}.prompt`,
        message: "AI actions require a prompt.",
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
