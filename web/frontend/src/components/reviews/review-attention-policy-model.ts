import {
  type ExactJSONValue,
  cloneExactJSON,
  createExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"
import type {
  ReviewAttentionGate,
  ReviewAttentionGateKind,
  ReviewAttentionPolicyCatalog,
  ReviewAttentionPolicyMode,
  ReviewAttentionRepositoryPolicy,
} from "@/api/review-attention-policies"

export const reviewAttentionGateKinds = [
  "ai_working_context",
  "ai_isolated_context",
  "deterministic",
  "zero",
] as const satisfies readonly ReviewAttentionGateKind[]

export const reviewAttentionPolicyModes = [
  "inherit",
  "overlay",
  "replace",
  "disable",
] as const satisfies readonly ReviewAttentionPolicyMode[]

export type ReviewAttentionEditorKeyFactory = (prefix: string) => string

interface ReviewAttentionGateDraftBase {
  editorKey: string
  id: string
}

export type ReviewAttentionGateDraft =
  | (ReviewAttentionGateDraftBase & {
      kind: "ai_working_context" | "ai_isolated_context"
      agentID: string
      criteria: string
      title: string
      questionsSource: string | null
    })
  | (ReviewAttentionGateDraftBase & {
      kind: "deterministic"
      when: string
      title: string
      questionsSource: string
    })
  | (ReviewAttentionGateDraftBase & {
      kind: "zero"
    })

export interface ReviewAttentionGlobalPolicyDraft {
  editorKey: string
  decisionPoint: string
  gates: ReviewAttentionGateDraft[]
}

export interface ReviewAttentionRepositoryPolicyDraft {
  editorKey: string
  decisionPoint: string
  mode: ReviewAttentionPolicyMode
  gates: ReviewAttentionGateDraft[]
}

export interface ReviewAttentionRepositoryDraft {
  editorKey: string
  repository: string
  policies: ReviewAttentionRepositoryPolicyDraft[]
}

export interface ReviewAttentionPolicyDraft {
  global: ReviewAttentionGlobalPolicyDraft[]
  repositories: ReviewAttentionRepositoryDraft[]
}

export interface ReviewAttentionPolicyIssue {
  path: string
  code: string
  message: string
}

export interface ReviewAttentionPolicyMetrics {
  repositories: number
  policies: number
  gates: number
  canonicalBytes: number
  requestBytes: number
}

export interface ReviewAttentionPolicyValidation {
  valid: boolean
  issues: ReviewAttentionPolicyIssue[]
  metrics: ReviewAttentionPolicyMetrics
  catalog?: ReviewAttentionPolicyCatalog
}

export type ReviewAttentionResolutionAction =
  | "inherited"
  | "replaced"
  | "tombstoned"
  | "appended"
  | "selected"

export interface ReviewAttentionResolutionEntry {
  id: string
  action: ReviewAttentionResolutionAction
  globalPosition?: number
  repositoryPosition?: number
  effectivePosition: number
  gate: ReviewAttentionGate
}

export interface ReviewAttentionPolicyResolution {
  repository: string
  decisionPoint: string
  overrideConfigured: boolean
  mode: ReviewAttentionPolicyMode
  entries: ReviewAttentionResolutionEntry[]
  effective: ReviewAttentionGate[]
  noop: boolean
}

const limits = {
  decisionPoints: 128,
  repositories: 1024,
  policies: 8192,
  gates: 8192,
  gatesPerPolicy: 64,
  decisionPointBytes: 128,
  repositoryBytes: 256,
  gateIDBytes: 64,
  criteriaBytes: 16 << 10,
  titleBytes: 4 << 10,
  conditionBytes: 4 << 10,
  questionBytes: 128 << 10,
  questionDepth: 64,
  questionNodes: 100_000,
  catalogBytes: 1 << 20,
  requestBytes: (1 << 20) + (64 << 10),
} as const

const decisionPointPattern = /^[a-z][a-z0-9._-]{0,127}$/
const repositoryPattern = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const gateIDPattern = /^[a-z][a-z0-9_-]{0,63}$/
const agentIDPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/
const expressionPathPattern = /^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*$/
const encoder = new TextEncoder()

export function createReviewAttentionEditorKeyFactory(
  start = 0,
): ReviewAttentionEditorKeyFactory {
  let sequence = start
  return (prefix: string) => `${prefix}-${++sequence}`
}

export function createReviewAttentionGateDraft(
  kind: ReviewAttentionGateKind,
  defaultAgentID: string,
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionGateDraft {
  const base = { editorKey: nextKey("gate"), id: "" }
  switch (kind) {
    case "ai_working_context":
    case "ai_isolated_context":
      return {
        ...base,
        kind,
        agentID: defaultAgentID,
        criteria: "",
        title: "",
        questionsSource: null,
      }
    case "deterministic":
      return {
        ...base,
        kind,
        when: "true",
        title: "",
        questionsSource: "[]",
      }
    case "zero":
      return { ...base, kind }
  }
}

export function createReviewAttentionGlobalPolicyDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionGlobalPolicyDraft {
  return { editorKey: nextKey("global-policy"), decisionPoint: "", gates: [] }
}

export function createReviewAttentionRepositoryDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionRepositoryDraft {
  return { editorKey: nextKey("repository"), repository: "", policies: [] }
}

export function createReviewAttentionRepositoryPolicyDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionRepositoryPolicyDraft {
  return {
    editorKey: nextKey("repository-policy"),
    decisionPoint: "",
    mode: "inherit",
    gates: [],
  }
}

export function reviewAttentionPolicyDraftFromCatalog(
  catalog: ReviewAttentionPolicyCatalog,
  nextKey = createReviewAttentionEditorKeyFactory(),
): ReviewAttentionPolicyDraft {
  return {
    global: Object.keys(catalog.global)
      .sort(compareText)
      .map((decisionPoint) => ({
        editorKey: nextKey("global-policy"),
        decisionPoint,
        gates: catalog.global[decisionPoint].map((gate) =>
          gateDraftFromValue(gate, nextKey),
        ),
      })),
    repositories: Object.keys(catalog.repositories)
      .sort(compareRepositories)
      .map((repository) => ({
        editorKey: nextKey("repository"),
        repository,
        policies: Object.keys(catalog.repositories[repository])
          .sort(compareText)
          .map((decisionPoint) => {
            const policy = catalog.repositories[repository][decisionPoint]
            return {
              editorKey: nextKey("repository-policy"),
              decisionPoint,
              mode: policy.mode,
              gates: policy.gates.map((gate) =>
                gateDraftFromValue(gate, nextKey),
              ),
            }
          }),
      })),
  }
}

function gateDraftFromValue(
  gate: ReviewAttentionGate,
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionGateDraft {
  const base = { editorKey: nextKey("gate"), id: gate.id }
  switch (gate.kind) {
    case "ai_working_context":
    case "ai_isolated_context":
      return {
        ...base,
        kind: gate.kind,
        agentID: gate.agent_id ?? "",
        criteria: gate.criteria ?? "",
        title: gate.title ?? "",
        questionsSource:
          gate.questions === undefined
            ? null
            : stringifyExactJSON(gate.questions, questionJSONLimits),
      }
    case "deterministic":
      return {
        ...base,
        kind: gate.kind,
        when: gate.when ?? "",
        title: gate.title ?? "",
        questionsSource:
          gate.questions === undefined
            ? ""
            : stringifyExactJSON(gate.questions, questionJSONLimits),
      }
    case "zero":
      return { ...base, kind: gate.kind }
  }
}

export function convertReviewAttentionGateKind(
  gate: ReviewAttentionGateDraft,
  nextKind: ReviewAttentionGateKind,
  defaultAgentID: string,
): ReviewAttentionGateDraft {
  if (gate.kind === nextKind) return gate
  const base = { editorKey: gate.editorKey, id: gate.id }
  if (nextKind === "zero") return { ...base, kind: "zero" }
  if (nextKind === "deterministic") {
    return {
      ...base,
      kind: "deterministic",
      when: "true",
      title: gate.kind === "zero" ? "" : gate.title,
      questionsSource:
        gate.kind === "zero" ? "[]" : (gate.questionsSource ?? "[]"),
    }
  }
  return {
    ...base,
    kind: nextKind,
    agentID:
      gate.kind === "ai_working_context" || gate.kind === "ai_isolated_context"
        ? gate.agentID
        : defaultAgentID,
    criteria:
      gate.kind === "ai_working_context" || gate.kind === "ai_isolated_context"
        ? gate.criteria
        : "",
    title: gate.kind === "zero" ? "" : gate.title,
    questionsSource: gate.kind === "zero" ? null : gate.questionsSource || null,
  }
}

export function reorderReviewAttentionGates(
  gates: readonly ReviewAttentionGateDraft[],
  from: number,
  to: number,
): ReviewAttentionGateDraft[] {
  if (
    !Number.isInteger(from) ||
    !Number.isInteger(to) ||
    from < 0 ||
    to < 0 ||
    from >= gates.length ||
    to >= gates.length ||
    from === to
  ) {
    return [...gates]
  }
  const reordered = [...gates]
  const [moved] = reordered.splice(from, 1)
  reordered.splice(to, 0, moved)
  return reordered
}

export function validateReviewAttentionPolicyDraft(
  draft: ReviewAttentionPolicyDraft,
  availableAgentIDs: ReadonlySet<string>,
): ReviewAttentionPolicyValidation {
  const issues: ReviewAttentionPolicyIssue[] = []
  const global = nullRecord<ReviewAttentionGate[]>()
  const repositories =
    nullRecord<Record<string, ReviewAttentionRepositoryPolicy>>()
  const globalNames = new Set<string>()
  let policyCount = draft.global.length
  let gateCount = 0

  if (draft.global.length > limits.decisionPoints) {
    addIssue(
      issues,
      "global",
      "limit.global_decision_points",
      `At most ${limits.decisionPoints} global decision points are allowed.`,
    )
  }
  if (draft.repositories.length > limits.repositories) {
    addIssue(
      issues,
      "repositories",
      "limit.repositories",
      `At most ${limits.repositories} repositories are allowed.`,
    )
  }

  draft.global.forEach((policy, policyIndex) => {
    const path = `global[${policyIndex}]`
    validateDecisionPoint(policy.decisionPoint, `${path}.decisionPoint`, issues)
    if (globalNames.has(policy.decisionPoint)) {
      addIssue(
        issues,
        `${path}.decisionPoint`,
        "decision_point.duplicate",
        "Decision points must be unique in the global catalog.",
      )
    }
    globalNames.add(policy.decisionPoint)
    gateCount += policy.gates.length
    const gates = compileGateList(
      policy.gates,
      `${path}.gates`,
      availableAgentIDs,
      issues,
    )
    if (!(policy.decisionPoint in global)) global[policy.decisionPoint] = gates
  })

  const foldedRepositories = new Map<string, string>()
  draft.repositories.forEach((repository, repositoryIndex) => {
    const path = `repositories[${repositoryIndex}]`
    validateRepository(repository.repository, `${path}.repository`, issues)
    const folded = repository.repository.toLowerCase()
    const previous = foldedRepositories.get(folded)
    if (previous !== undefined) {
      addIssue(
        issues,
        `${path}.repository`,
        "repository.case_collision",
        `Repository names ${JSON.stringify(previous)} and ${JSON.stringify(repository.repository)} differ only by case.`,
      )
    } else {
      foldedRepositories.set(folded, repository.repository)
    }
    if (repository.policies.length > limits.decisionPoints) {
      addIssue(
        issues,
        `${path}.policies`,
        "limit.repository_decision_points",
        `At most ${limits.decisionPoints} decision points are allowed per repository.`,
      )
    }
    policyCount += repository.policies.length
    const policies = nullRecord<ReviewAttentionRepositoryPolicy>()
    const names = new Set<string>()
    repository.policies.forEach((policy, policyIndex) => {
      const policyPath = `${path}.policies[${policyIndex}]`
      validateDecisionPoint(
        policy.decisionPoint,
        `${policyPath}.decisionPoint`,
        issues,
      )
      if (
        !(reviewAttentionPolicyModes as readonly string[]).includes(policy.mode)
      ) {
        addIssue(
          issues,
          `${policyPath}.mode`,
          "mode.invalid",
          "Repository policy mode is unsupported.",
        )
      }
      if (names.has(policy.decisionPoint)) {
        addIssue(
          issues,
          `${policyPath}.decisionPoint`,
          "decision_point.duplicate",
          "Decision points must be unique within a repository.",
        )
      }
      names.add(policy.decisionPoint)
      gateCount += policy.gates.length
      if (
        (policy.mode === "inherit" || policy.mode === "disable") &&
        policy.gates.length !== 0
      ) {
        addIssue(
          issues,
          `${policyPath}.gates`,
          "mode.gates_forbidden",
          `${policy.mode} policies cannot configure gates.`,
        )
      }
      if (
        (policy.mode === "overlay" || policy.mode === "replace") &&
        policy.gates.length === 0
      ) {
        addIssue(
          issues,
          `${policyPath}.gates`,
          "mode.gates_required",
          `${policy.mode} policies require at least one gate.`,
        )
      }
      const gates = compileGateList(
        policy.gates,
        `${policyPath}.gates`,
        availableAgentIDs,
        issues,
      )
      if (!(policy.decisionPoint in policies)) {
        policies[policy.decisionPoint] = { mode: policy.mode, gates }
      }
    })
    if (!(repository.repository in repositories))
      repositories[repository.repository] = policies
  })

  if (policyCount > limits.policies) {
    addIssue(
      issues,
      "catalog",
      "limit.policies",
      `At most ${limits.policies} policies are allowed.`,
    )
  }
  if (gateCount > limits.gates) {
    addIssue(
      issues,
      "catalog",
      "limit.gates",
      `At most ${limits.gates} configured gates are allowed.`,
    )
  }

  const candidate: ReviewAttentionPolicyCatalog = { global, repositories }
  validateEffectivePolicies(candidate, issues)
  const measuredCanonicalBytes = safeEncodedBytes(
    canonicalCatalogValue(candidate),
    true,
  )
  const measuredRequestBytes = safeEncodedBytes(
    requestCatalogValue(candidate, "sha256:" + "0".repeat(64)),
    false,
  )
  const canonicalBytes = measuredCanonicalBytes ?? 0
  const requestBytes = measuredRequestBytes ?? 0
  if (measuredCanonicalBytes !== null && canonicalBytes > limits.catalogBytes) {
    addIssue(
      issues,
      "catalog",
      "limit.catalog_bytes",
      `The canonical catalog exceeds ${limits.catalogBytes} encoded bytes.`,
    )
  }
  if (measuredRequestBytes !== null && requestBytes > limits.requestBytes) {
    addIssue(
      issues,
      "catalog",
      "limit.request_bytes",
      `The policy request exceeds ${limits.requestBytes} encoded bytes.`,
    )
  }

  issues.sort(
    (left, right) =>
      compareText(left.path, right.path) || compareText(left.code, right.code),
  )
  const result: ReviewAttentionPolicyValidation = {
    valid: issues.length === 0,
    issues,
    metrics: {
      repositories: draft.repositories.length,
      policies: policyCount,
      gates: gateCount,
      canonicalBytes,
      requestBytes,
    },
  }
  if (issues.length === 0) result.catalog = candidate
  return result
}

/**
 * Validates a server projection before the browser turns it into editable
 * state. Agent existence is checked against the separately revision-matched
 * agent catalog; this pass treats every declared canonical agent as available
 * while enforcing all other catalog, gate, text, condition, composition, and
 * encoded-size semantics.
 */
export function isReviewAttentionPolicyCatalogSemanticallyValid(
  catalog: ReviewAttentionPolicyCatalog,
): boolean {
  const declaredAgents = new Set<string>()
  const collect = (gates: readonly ReviewAttentionGate[]) => {
    for (const gate of gates) {
      if (gate.agent_id !== undefined) declaredAgents.add(gate.agent_id)
    }
  }
  for (const gates of Object.values(catalog.global)) collect(gates)
  for (const policies of Object.values(catalog.repositories)) {
    for (const policy of Object.values(policies)) collect(policy.gates)
  }
  try {
    return validateReviewAttentionPolicyDraft(
      reviewAttentionPolicyDraftFromCatalog(catalog),
      declaredAgents,
    ).valid
  } catch {
    return false
  }
}

function compileGateList(
  drafts: readonly ReviewAttentionGateDraft[],
  path: string,
  availableAgentIDs: ReadonlySet<string>,
  issues: ReviewAttentionPolicyIssue[],
): ReviewAttentionGate[] {
  if (drafts.length > limits.gatesPerPolicy) {
    addIssue(
      issues,
      path,
      "limit.policy_gates",
      `A policy may contain at most ${limits.gatesPerPolicy} gates.`,
    )
  }
  const seen = new Set<string>()
  let workingAgent = ""
  return drafts.map((draft, index) => {
    const gatePath = `${path}[${index}]`
    validateGateID(draft.id, `${gatePath}.id`, issues)
    if (!(reviewAttentionGateKinds as readonly string[]).includes(draft.kind)) {
      addIssue(
        issues,
        `${gatePath}.kind`,
        "gate.kind_invalid",
        "Gate kind is unsupported.",
      )
    }
    if (seen.has(draft.id)) {
      addIssue(
        issues,
        `${gatePath}.id`,
        "gate.id_duplicate",
        "Gate IDs must be unique within a policy layer.",
      )
    }
    seen.add(draft.id)
    if (
      draft.kind === "ai_working_context" ||
      draft.kind === "ai_isolated_context"
    ) {
      validateAgentID(
        draft.agentID,
        `${gatePath}.agentID`,
        availableAgentIDs,
        issues,
      )
      validateRequiredText(
        draft.criteria,
        `${gatePath}.criteria`,
        limits.criteriaBytes,
        issues,
      )
      validateRequiredText(
        draft.title,
        `${gatePath}.title`,
        limits.titleBytes,
        issues,
      )
      if (draft.kind === "ai_working_context") {
        if (workingAgent !== "" && workingAgent !== draft.agentID) {
          addIssue(
            issues,
            `${gatePath}.agentID`,
            "gate.working_agent_conflict",
            "Working-context gates in one policy must use one agent.",
          )
        }
        workingAgent = draft.agentID
      }
      const questions =
        draft.questionsSource === null
          ? undefined
          : parseQuestions(
              draft.questionsSource,
              `${gatePath}.questionsSource`,
              false,
              issues,
            )
      return {
        id: draft.id,
        kind: draft.kind,
        agent_id: draft.agentID,
        criteria: draft.criteria,
        title: draft.title,
        ...(questions === undefined ? {} : { questions }),
      }
    }
    if (draft.kind === "deterministic") {
      validateRequiredText(
        draft.title,
        `${gatePath}.title`,
        limits.titleBytes,
        issues,
      )
      const conditionError = reviewAttentionConditionError(draft.when)
      if (conditionError !== null)
        addIssue(
          issues,
          `${gatePath}.when`,
          "gate.when_invalid",
          conditionError,
        )
      const questions = parseQuestions(
        draft.questionsSource,
        `${gatePath}.questionsSource`,
        true,
        issues,
      )
      return {
        id: draft.id,
        kind: draft.kind,
        when: draft.when,
        title: draft.title,
        ...(questions === undefined ? {} : { questions }),
      }
    }
    return { id: draft.id, kind: "zero" }
  })
}

const questionJSONLimits = {
  maximumBytes: limits.questionBytes,
  maximumDepth: limits.questionDepth,
  maximumNodes: limits.questionNodes,
} as const

function parseQuestions(
  source: string,
  path: string,
  required: boolean,
  issues: ReviewAttentionPolicyIssue[],
): ExactJSONValue | undefined {
  try {
    const value = parseExactJSON(source, {
      ...questionJSONLimits,
      maximumBytes: limits.requestBytes,
    })
    if (value === null) {
      if (required)
        addIssue(
          issues,
          path,
          "gate.questions_required",
          "Deterministic questions must be a non-null JSON value.",
        )
      else
        addIssue(
          issues,
          path,
          "gate.questions_null",
          "Use no questions instead of an explicit null value.",
        )
      return undefined
    }
    if (encodedBytes(value, true) > limits.questionBytes) {
      throw new TypeError("questions exceed the encoded byte limit")
    }
    return value
  } catch {
    addIssue(
      issues,
      path,
      "gate.questions_invalid",
      "Questions must be one exact JSON value within the depth, node, and byte limits.",
    )
    return undefined
  }
}

function validateEffectivePolicies(
  catalog: ReviewAttentionPolicyCatalog,
  issues: ReviewAttentionPolicyIssue[],
) {
  Object.keys(catalog.repositories)
    .sort(compareRepositories)
    .forEach((repository) => {
      const policies = catalog.repositories[repository]
      Object.keys(policies)
        .sort(compareText)
        .forEach((decisionPoint) => {
          const policy = policies[decisionPoint]
          if (policy.mode !== "overlay") return
          const resolution = resolveReviewAttentionPolicy(
            catalog,
            repository,
            decisionPoint,
          )
          if (resolution.effective.length > limits.gatesPerPolicy) {
            addIssue(
              issues,
              `repositories[${JSON.stringify(repository)}].${decisionPoint}`,
              "effective.gate_limit",
              `The effective policy exceeds ${limits.gatesPerPolicy} gates.`,
            )
          }
          let workingAgent = ""
          resolution.effective.forEach((gate) => {
            if (gate.kind !== "ai_working_context") return
            const agent = gate.agent_id ?? ""
            if (workingAgent !== "" && workingAgent !== agent) {
              addIssue(
                issues,
                `repositories[${JSON.stringify(repository)}].${decisionPoint}`,
                "effective.working_agent_conflict",
                "The effective working-context gates must use one agent.",
              )
            }
            workingAgent = agent
          })
        })
    })
}

export function resolveReviewAttentionPolicy(
  catalog: ReviewAttentionPolicyCatalog,
  repository: string,
  decisionPoint: string,
): ReviewAttentionPolicyResolution {
  const global = (catalog.global[decisionPoint] ?? []).map(cloneGate)
  const configuredRepository = Object.keys(catalog.repositories).find(
    (candidate) => candidate.toLowerCase() === repository.toLowerCase(),
  )
  const policy =
    configuredRepository === undefined
      ? undefined
      : catalog.repositories[configuredRepository][decisionPoint]
  if (policy === undefined || policy.mode === "inherit") {
    const entries = global.map((gate, index) => ({
      id: gate.id,
      action: "inherited" as const,
      globalPosition: index + 1,
      effectivePosition: index + 1,
      gate,
    }))
    return resolution(
      repository,
      decisionPoint,
      policy !== undefined,
      "inherit",
      entries,
    )
  }
  if (policy.mode === "disable") {
    return resolution(repository, decisionPoint, true, "disable", [])
  }
  if (policy.mode === "replace") {
    const entries = policy.gates.map((source, index) => {
      const gate = cloneGate(source)
      return {
        id: gate.id,
        action: "selected" as const,
        repositoryPosition: index + 1,
        effectivePosition: index + 1,
        gate,
      }
    })
    return resolution(repository, decisionPoint, true, "replace", entries)
  }
  const entries: ReviewAttentionResolutionEntry[] = global.map(
    (gate, index) => ({
      id: gate.id,
      action: "inherited",
      globalPosition: index + 1,
      effectivePosition: index + 1,
      gate,
    }),
  )
  const positions = new Map(entries.map((entry, index) => [entry.id, index]))
  policy.gates.forEach((source, repositoryIndex) => {
    const gate = cloneGate(source)
    const effectiveIndex = positions.get(gate.id)
    if (effectiveIndex !== undefined) {
      const previous = entries[effectiveIndex]
      entries[effectiveIndex] = {
        id: gate.id,
        action:
          previous.gate.kind !== "zero" && gate.kind === "zero"
            ? "tombstoned"
            : "replaced",
        globalPosition: effectiveIndex + 1,
        repositoryPosition: repositoryIndex + 1,
        effectivePosition: effectiveIndex + 1,
        gate,
      }
      return
    }
    positions.set(gate.id, entries.length)
    entries.push({
      id: gate.id,
      action: "appended",
      repositoryPosition: repositoryIndex + 1,
      effectivePosition: entries.length + 1,
      gate,
    })
  })
  return resolution(repository, decisionPoint, true, "overlay", entries)
}

function resolution(
  repository: string,
  decisionPoint: string,
  overrideConfigured: boolean,
  mode: ReviewAttentionPolicyMode,
  entries: ReviewAttentionResolutionEntry[],
): ReviewAttentionPolicyResolution {
  const effective = entries.map((entry) => cloneGate(entry.gate))
  return {
    repository,
    decisionPoint,
    overrideConfigured,
    mode,
    entries,
    effective,
    noop: effective.every((gate) => gate.kind === "zero"),
  }
}

function cloneGate(gate: ReviewAttentionGate): ReviewAttentionGate {
  return {
    ...gate,
    ...(gate.questions === undefined
      ? {}
      : { questions: cloneExactJSON(gate.questions) }),
  }
}

export function reviewAttentionConditionError(value: string): string | null {
  if (
    !validUnicode(value) ||
    encodedLength(value) > limits.conditionBytes ||
    trimGoSpace(value) === ""
  ) {
    return `Condition must be nonblank valid UTF-8 and at most ${limits.conditionBytes} bytes.`
  }
  let condition = trimGoSpace(value)
  if (condition.startsWith("${{") || condition.endsWith("}}")) {
    if (!(condition.startsWith("${{") && condition.endsWith("}}"))) {
      return "Expression delimiters are incomplete."
    }
    condition = trimGoSpace(condition.slice(3, -2))
  }
  return conditionOperandError(condition)
}

function conditionOperandError(expression: string): string | null {
  const trimmed = trimGoSpace(expression)
  for (const operator of [" == ", " != ", " >= ", " <= ", " > ", " < "]) {
    const index = trimmed.indexOf(operator)
    if (index >= 0) {
      return (
        conditionOperandError(trimmed.slice(0, index)) ??
        conditionOperandError(trimmed.slice(index + operator.length))
      )
    }
  }
  if (trimmed.startsWith("not ")) return conditionOperandError(trimmed.slice(4))
  if (
    trimmed.length >= 2 &&
    ((trimmed.startsWith("'") && trimmed.endsWith("'")) ||
      (trimmed.startsWith('"') && trimmed.endsWith('"')))
  )
    return null
  if (
    trimmed === "true" ||
    trimmed === "false" ||
    trimmed === "null" ||
    isGoFloat(trimmed)
  )
    return null
  if (!expressionPathPattern.test(trimmed))
    return `Unsupported expression syntax ${JSON.stringify(trimmed)}.`
  if (trimmed.split(".", 1)[0] !== "inputs")
    return `Deterministic condition paths must use the inputs root.`
  return null
}

function isGoFloat(value: string) {
  if (/^nan$/i.test(value) || /^[+-]?inf(?:inity)?$/i.test(value)) return true

  const decimal =
    /^[+-]?(?:\d(?:_?\d)*(?:\.(?:\d(?:_?\d)*)?)?|\.\d(?:_?\d)*)(?:[eE][+-]?\d(?:_?\d)*)?$/
  if (decimal.test(value))
    return Number.isFinite(Number(value.replaceAll("_", "")))

  const hex = value.match(
    /^[+-]?0[xX](_?[0-9a-fA-F](?:_?[0-9a-fA-F])*(?:\.(?:[0-9a-fA-F](?:_?[0-9a-fA-F])*)?)?|\.[0-9a-fA-F](?:_?[0-9a-fA-F])*)[pP]([+-]?\d(?:_?\d)*)$/,
  )
  if (hex === null) return false

  const mantissa = hex[1].replaceAll("_", "")
  const [whole, fraction = ""] = mantissa.split(".")
  const digits = `${whole}${fraction}`.replace(/^0+/, "")
  if (digits === "") return true

  const significantBits = [...digits]
    .map((digit) => Number.parseInt(digit, 16).toString(2).padStart(4, "0"))
    .join("")
    .replace(/^0+/, "")
  const exponent = Number(hex[2].replaceAll("_", ""))
  const valueBits = significantBits.length + exponent - fraction.length * 4
  if (valueBits < 1024) return true
  if (valueBits > 1024) return false

  // ParseFloat reports ErrRange at the halfway point between MaxFloat64 and
  // infinity: (2^54 - 1) * 2^970, whose leading 54 bits are all ones.
  return significantBits.padEnd(54, "0").slice(0, 54) !== "1".repeat(54)
}

function validateDecisionPoint(
  value: string,
  path: string,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (
    !decisionPointPattern.test(value) ||
    encodedLength(value) > limits.decisionPointBytes
  ) {
    addIssue(
      issues,
      path,
      "decision_point.invalid",
      "Decision point must start with a lowercase letter and contain only lowercase letters, digits, dot, underscore, or hyphen (128 bytes maximum).",
    )
  }
}

function validateRepository(
  value: string,
  path: string,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (
    !repositoryPattern.test(value) ||
    encodedLength(value) > limits.repositoryBytes
  ) {
    addIssue(
      issues,
      path,
      "repository.invalid",
      "Repository must be a trimmed owner/repository name using letters, digits, dot, underscore, or hyphen (256 bytes maximum).",
    )
  }
}

function validateGateID(
  value: string,
  path: string,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (!gateIDPattern.test(value) || encodedLength(value) > limits.gateIDBytes) {
    addIssue(
      issues,
      path,
      "gate.id_invalid",
      "Gate ID must start with a lowercase letter and contain only lowercase letters, digits, underscore, or hyphen (64 bytes maximum).",
    )
  }
}

function validateAgentID(
  value: string,
  path: string,
  agents: ReadonlySet<string>,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (!agentIDPattern.test(value)) {
    addIssue(
      issues,
      path,
      "gate.agent_invalid",
      "AI gates require an exact canonical agent ID.",
    )
  } else if (!agents.has(value)) {
    addIssue(
      issues,
      path,
      "gate.agent_unavailable",
      `Agent ${JSON.stringify(value)} is not configured.`,
    )
  }
}

function validateRequiredText(
  value: string,
  path: string,
  maximum: number,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (
    trimGoSpace(value) === "" ||
    !validUnicode(value) ||
    encodedLength(value) > maximum
  ) {
    addIssue(
      issues,
      path,
      "gate.text_invalid",
      `Value must be nonblank valid UTF-8 and at most ${maximum} bytes.`,
    )
  }
}

function validUnicode(value: string) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1)
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false
      index += 1
    } else if (code >= 0xdc00 && code <= 0xdfff) return false
  }
  return true
}

function encodedLength(value: string) {
  return encoder.encode(value).byteLength
}

function addIssue(
  issues: ReviewAttentionPolicyIssue[],
  path: string,
  code: string,
  message: string,
) {
  issues.push({ path, code, message })
}

function nullRecord<Value>(): Record<string, Value> {
  return Object.create(null) as Record<string, Value>
}

function compareText(left: string, right: string) {
  return left < right ? -1 : left > right ? 1 : 0
}

function compareRepositories(left: string, right: string) {
  return (
    compareText(left.toLowerCase(), right.toLowerCase()) ||
    compareText(left, right)
  )
}

function gateValue(gate: ReviewAttentionGate): ExactJSONValue {
  const entries: [string, ExactJSONValue][] = [
    ["id", gate.id],
    ["kind", gate.kind],
  ]
  if (gate.agent_id) entries.push(["agent_id", gate.agent_id])
  if (gate.criteria) entries.push(["criteria", gate.criteria])
  if (gate.when) entries.push(["when", gate.when])
  if (gate.title) entries.push(["title", gate.title])
  if (gate.questions !== undefined) entries.push(["questions", gate.questions])
  return createExactJSONObject(entries)
}

function catalogValue(catalog: ReviewAttentionPolicyCatalog): ExactJSONValue {
  const globalEntries = Object.keys(catalog.global)
    .sort(compareText)
    .map(
      (decisionPoint) =>
        [decisionPoint, catalog.global[decisionPoint].map(gateValue)] as const,
    )
  const repositoryEntries = Object.keys(catalog.repositories)
    .sort(compareRepositories)
    .map(
      (repository) =>
        [
          repository,
          createExactJSONObject(
            Object.keys(catalog.repositories[repository])
              .sort(compareText)
              .map((decisionPoint) => {
                const policy = catalog.repositories[repository][decisionPoint]
                return [
                  decisionPoint,
                  createExactJSONObject([
                    ["mode", policy.mode],
                    ["gates", policy.gates.map(gateValue)],
                  ]),
                ] as const
              }),
          ),
        ] as const,
    )
  return createExactJSONObject([
    ["global", createExactJSONObject(globalEntries)],
    ["repositories", createExactJSONObject(repositoryEntries)],
  ])
}

function canonicalCatalogValue(
  catalog: ReviewAttentionPolicyCatalog,
): ExactJSONValue {
  const global = Object.keys(catalog.global)
    .sort(compareText)
    .map((decisionPoint) =>
      createExactJSONObject([
        ["decision_point", decisionPoint],
        ["gates", catalog.global[decisionPoint].map(gateValue)],
      ]),
    )
  const repositories = Object.keys(catalog.repositories)
    .sort(compareRepositories)
    .map((repository) =>
      createExactJSONObject([
        ["repository", repository.toLowerCase()],
        [
          "policies",
          Object.keys(catalog.repositories[repository])
            .sort(compareText)
            .map((decisionPoint) => {
              const policy = catalog.repositories[repository][decisionPoint]
              return createExactJSONObject([
                ["decision_point", decisionPoint],
                [
                  "policy",
                  createExactJSONObject([
                    ["mode", policy.mode],
                    ...(policy.gates.length === 0
                      ? []
                      : [["gates", policy.gates.map(gateValue)] as const]),
                  ]),
                ],
              ])
            }),
        ],
      ]),
    )
  return createExactJSONObject([
    ["format", "review-attention-catalog/v1"],
    ["global", global],
    ["repositories", repositories],
  ])
}

function requestCatalogValue(
  catalog: ReviewAttentionPolicyCatalog,
  revision: string,
): ExactJSONValue {
  const value = catalogValue(catalog) as Record<string, ExactJSONValue>
  return createExactJSONObject([
    ["expected_config_revision", revision],
    ["global", value.global],
    ["repositories", value.repositories],
  ])
}

function encodedBytes(value: ExactJSONValue, goCompatible: boolean) {
  let source = stringifyExactJSON(value, {
    maximumBytes: Number.MAX_SAFE_INTEGER,
    maximumDepth: 256,
    maximumNodes: Number.MAX_SAFE_INTEGER,
  })
  if (goCompatible) {
    source = source
      .replaceAll("<", "\\u003c")
      .replaceAll(">", "\\u003e")
      .replaceAll("&", "\\u0026")
      .replaceAll("\u2028", "\\u2028")
      .replaceAll("\u2029", "\\u2029")
  }
  return encodedLength(source)
}

function safeEncodedBytes(value: ExactJSONValue, goCompatible: boolean) {
  try {
    return encodedBytes(value, goCompatible)
  } catch {
    return null
  }
}
