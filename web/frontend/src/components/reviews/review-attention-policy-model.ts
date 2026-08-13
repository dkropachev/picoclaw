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
  ReviewAttentionRuleSet,
} from "@/api/review-attention-policies"
import {
  reviewAttentionBuiltInRuleSetID,
  reviewAttentionBuiltInRuleSetName,
} from "@/api/review-attention-policies"

export const reviewAttentionGateKinds = [
  "ai_working_context",
  "ai_isolated_context",
  "deterministic",
  "zero",
] as const satisfies readonly ReviewAttentionGateKind[]

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

export interface ReviewAttentionRuleDraft {
  editorKey: string
  decisionPoint: string
  gates: ReviewAttentionGateDraft[]
}

export interface ReviewAttentionRuleSetDraft {
  editorKey: string
  id: string
  name: string
  rules: ReviewAttentionRuleDraft[]
}

export interface ReviewAttentionRepositoryAssignmentDraft {
  editorKey: string
  repository: string
  ruleSetID: string
}

export interface ReviewAttentionPolicyDraft {
  ruleSets: ReviewAttentionRuleSetDraft[]
  defaultRuleSetID: string
  repositoryAssignments: ReviewAttentionRepositoryAssignmentDraft[]
}

export interface ReviewAttentionPolicyIssue {
  path: string
  code: string
  message: string
}

export interface ReviewAttentionPolicyMetrics {
  ruleSets: number
  repositories: number
  rules: number
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

export type ReviewAttentionResolutionAction = "default" | "assigned"

export interface ReviewAttentionResolutionEntry {
  id: string
  action: ReviewAttentionResolutionAction
  effectivePosition: number
  gate: ReviewAttentionGate
}

export interface ReviewAttentionPolicyResolution {
  repository: string
  decisionPoint: string
  ruleSetID: string
  ruleSetName: string
  assigned: boolean
  entries: ReviewAttentionResolutionEntry[]
  effective: ReviewAttentionGate[]
  noop: boolean
}

export interface ReviewAttentionRuleSetResolution {
  repository: string
  ruleSetID: string
  ruleSetName: string
  assigned: boolean
  ruleSet: ReviewAttentionRuleSet
}

const limits = {
  ruleSets: 1025,
  decisionPoints: 128,
  repositories: 1024,
  rules: 8192,
  gates: 8192,
  gatesPerPolicy: 64,
  ruleSetIDBytes: 64,
  ruleSetNameBytes: 128,
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
const ruleSetIDPattern = /^[a-z][a-z0-9_-]{0,63}$/
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

export function createReviewAttentionRuleDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionRuleDraft {
  return { editorKey: nextKey("rule"), decisionPoint: "", gates: [] }
}

export function createReviewAttentionRuleSetDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
  id = "",
  name = "",
): ReviewAttentionRuleSetDraft {
  return { editorKey: nextKey("rule-set"), id, name, rules: [] }
}

export function createReviewAttentionRepositoryAssignmentDraft(
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionRepositoryAssignmentDraft {
  return {
    editorKey: nextKey("repository-assignment"),
    repository: "",
    ruleSetID: "",
  }
}

export function duplicateReviewAttentionRuleSetDraft(
  source: ReviewAttentionRuleSetDraft,
  id: string,
  name: string,
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionRuleSetDraft {
  return {
    editorKey: nextKey("rule-set"),
    id,
    name,
    rules: source.rules.map((rule) => ({
      editorKey: nextKey("rule"),
      decisionPoint: rule.decisionPoint,
      gates: rule.gates.map((gate) => cloneGateDraft(gate, nextKey)),
    })),
  }
}

// Go's strings.ToLower uses the simple lowercase mapping for U+0130 while
// JavaScript applies its multi-code-point special case. Normalize that one
// unconditional expansion before lowercasing so client/server name uniqueness
// decisions stay aligned.
export function foldReviewAttentionRuleSetName(value: string): string {
  return value.replaceAll("\u0130", "I").toLowerCase()
}

export function reviewAttentionPolicyDraftFromCatalog(
  catalog: ReviewAttentionPolicyCatalog,
  nextKey = createReviewAttentionEditorKeyFactory(),
): ReviewAttentionPolicyDraft {
  return {
    ruleSets: Object.keys(catalog.rule_sets)
      .sort(compareText)
      .map((id) => ({
        editorKey: nextKey("rule-set"),
        id,
        name: catalog.rule_sets[id].name,
        rules: Object.keys(catalog.rule_sets[id].rules)
          .sort(compareText)
          .map((decisionPoint) => ({
            editorKey: nextKey("rule"),
            decisionPoint,
            gates: catalog.rule_sets[id].rules[decisionPoint].map((gate) =>
              gateDraftFromValue(gate, nextKey),
            ),
          })),
      })),
    defaultRuleSetID: catalog.default_rule_set_id,
    repositoryAssignments: Object.keys(catalog.repository_assignments)
      .sort(compareRepositories)
      .map((repository) => ({
        editorKey: nextKey("repository-assignment"),
        repository,
        ruleSetID: catalog.repository_assignments[repository],
      })),
  }
}

function cloneGateDraft(
  gate: ReviewAttentionGateDraft,
  nextKey: ReviewAttentionEditorKeyFactory,
): ReviewAttentionGateDraft {
  return { ...gate, editorKey: nextKey("gate") }
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
  const ruleSets = nullRecord<ReviewAttentionRuleSet>()
  const repositoryAssignments = nullRecord<string>()
  const ruleSetIDs = new Set<string>()
  const foldedNames = new Map<string, string>()
  let ruleCount = 0
  let gateCount = 0

  if (draft.ruleSets.length > limits.ruleSets) {
    addIssue(
      issues,
      "ruleSets",
      "limit.rule_sets",
      `At most ${limits.ruleSets} rule sets are allowed.`,
    )
  }
  if (draft.repositoryAssignments.length > limits.repositories) {
    addIssue(
      issues,
      "repositoryAssignments",
      "limit.repositories",
      `At most ${limits.repositories} repositories are allowed.`,
    )
  }

  draft.ruleSets.forEach((ruleSet, ruleSetIndex) => {
    const path = `ruleSets[${ruleSetIndex}]`
    validateRuleSetID(ruleSet.id, `${path}.id`, issues)
    validateRuleSetName(ruleSet.name, `${path}.name`, issues)
    if (ruleSetIDs.has(ruleSet.id)) {
      addIssue(
        issues,
        `${path}.id`,
        "rule_set.id_duplicate",
        "Rule set IDs must be unique.",
      )
    }
    ruleSetIDs.add(ruleSet.id)

    const foldedName = foldReviewAttentionRuleSetName(ruleSet.name)
    const previousName = foldedNames.get(foldedName)
    if (previousName !== undefined) {
      addIssue(
        issues,
        `${path}.name`,
        "rule_set.name_duplicate",
        `Rule set names ${JSON.stringify(previousName)} and ${JSON.stringify(ruleSet.name)} differ only by case.`,
      )
    } else {
      foldedNames.set(foldedName, ruleSet.name)
    }
    if (
      ruleSet.id === reviewAttentionBuiltInRuleSetID &&
      ruleSet.name !== reviewAttentionBuiltInRuleSetName
    ) {
      addIssue(
        issues,
        `${path}.name`,
        "rule_set.default_name",
        `The built-in rule set must be named ${JSON.stringify(reviewAttentionBuiltInRuleSetName)}.`,
      )
    }
    if (ruleSet.rules.length > limits.decisionPoints) {
      addIssue(
        issues,
        `${path}.rules`,
        "limit.rule_set_decision_points",
        `At most ${limits.decisionPoints} workflow moments are allowed per rule set.`,
      )
    }

    ruleCount += ruleSet.rules.length
    const rules = nullRecord<ReviewAttentionGate[]>()
    const decisionPoints = new Set<string>()
    ruleSet.rules.forEach((rule, ruleIndex) => {
      const rulePath = `${path}.rules[${ruleIndex}]`
      validateDecisionPoint(
        rule.decisionPoint,
        `${rulePath}.decisionPoint`,
        issues,
      )
      if (decisionPoints.has(rule.decisionPoint)) {
        addIssue(
          issues,
          `${rulePath}.decisionPoint`,
          "decision_point.duplicate",
          "Workflow moments must be unique within a rule set.",
        )
      }
      decisionPoints.add(rule.decisionPoint)
      gateCount += rule.gates.length
      const gates = compileGateList(
        rule.gates,
        `${rulePath}.gates`,
        availableAgentIDs,
        issues,
      )
      if (!(rule.decisionPoint in rules)) rules[rule.decisionPoint] = gates
    })
    if (!(ruleSet.id in ruleSets)) {
      ruleSets[ruleSet.id] = { name: ruleSet.name, rules }
    }
  })

  if (!ruleSetIDs.has(reviewAttentionBuiltInRuleSetID)) {
    addIssue(
      issues,
      "ruleSets",
      "rule_set.default_missing",
      `The built-in ${JSON.stringify(reviewAttentionBuiltInRuleSetName)} rule set is required.`,
    )
  }

  validateRuleSetID(draft.defaultRuleSetID, "defaultRuleSetID", issues)
  if (!ruleSetIDs.has(draft.defaultRuleSetID)) {
    addIssue(
      issues,
      "defaultRuleSetID",
      "rule_set.default_reference",
      "The default rule set must reference an existing rule set.",
    )
  }

  const foldedRepositories = new Map<string, string>()
  draft.repositoryAssignments.forEach((assignment, assignmentIndex) => {
    const path = `repositoryAssignments[${assignmentIndex}]`
    validateRepository(assignment.repository, `${path}.repository`, issues)
    const folded = assignment.repository.toLowerCase()
    const previous = foldedRepositories.get(folded)
    if (previous !== undefined) {
      addIssue(
        issues,
        `${path}.repository`,
        "repository.case_collision",
        `Repository names ${JSON.stringify(previous)} and ${JSON.stringify(assignment.repository)} differ only by case.`,
      )
    } else {
      foldedRepositories.set(folded, assignment.repository)
    }
    validateRuleSetID(assignment.ruleSetID, `${path}.ruleSetID`, issues)
    if (!ruleSetIDs.has(assignment.ruleSetID)) {
      addIssue(
        issues,
        `${path}.ruleSetID`,
        "rule_set.assignment_reference",
        "Repository assignments must reference an existing rule set.",
      )
    }
    if (!(assignment.repository in repositoryAssignments)) {
      repositoryAssignments[assignment.repository] = assignment.ruleSetID
    }
  })

  if (ruleCount > limits.rules) {
    addIssue(
      issues,
      "catalog",
      "limit.rules",
      `At most ${limits.rules} attention rules are allowed.`,
    )
  }
  if (gateCount > limits.gates) {
    addIssue(
      issues,
      "catalog",
      "limit.gates",
      `At most ${limits.gates} configured checks are allowed.`,
    )
  }

  const candidate: ReviewAttentionPolicyCatalog = {
    rule_sets: ruleSets,
    default_rule_set_id: draft.defaultRuleSetID,
    repository_assignments: repositoryAssignments,
  }
  const measuredCanonicalBytes = safeEncodedBytes(
    configCatalogValue(candidate),
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
      ruleSets: draft.ruleSets.length,
      repositories: draft.repositoryAssignments.length,
      rules: ruleCount,
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
  for (const ruleSet of Object.values(catalog.rule_sets)) {
    for (const gates of Object.values(ruleSet.rules)) collect(gates)
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
      `An attention rule may contain at most ${limits.gatesPerPolicy} checks.`,
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
        "Check type is unsupported.",
      )
    }
    if (seen.has(draft.id)) {
      addIssue(
        issues,
        `${gatePath}.id`,
        "gate.id_duplicate",
        "Check IDs must be unique within this rule layer.",
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
            "Working-agent checks in one rule must use one agent.",
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
          "Fixed-condition questions must be a non-null JSON value.",
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

export function resolveReviewAttentionRuleSet(
  catalog: ReviewAttentionPolicyCatalog,
  repository: string,
): ReviewAttentionRuleSetResolution {
  const configuredRepository = Object.keys(catalog.repository_assignments).find(
    (candidate) => candidate.toLowerCase() === repository.toLowerCase(),
  )
  const assigned = configuredRepository !== undefined
  const ruleSetID = assigned
    ? catalog.repository_assignments[configuredRepository]
    : catalog.default_rule_set_id
  const ruleSet = catalog.rule_sets[ruleSetID]
  if (ruleSet === undefined) {
    throw new TypeError("attention rule set reference is unavailable")
  }
  return {
    repository,
    ruleSetID,
    ruleSetName: ruleSet.name,
    assigned,
    ruleSet: cloneRuleSet(ruleSet),
  }
}

export function resolveReviewAttentionPolicy(
  catalog: ReviewAttentionPolicyCatalog,
  repository: string,
  decisionPoint: string,
): ReviewAttentionPolicyResolution {
  const selected = resolveReviewAttentionRuleSet(catalog, repository)
  const action: ReviewAttentionResolutionAction = selected.assigned
    ? "assigned"
    : "default"
  const entries = (selected.ruleSet.rules[decisionPoint] ?? []).map(
    (gate, index) => ({
      id: gate.id,
      action,
      effectivePosition: index + 1,
      gate: cloneGate(gate),
    }),
  )
  const effective = entries.map((entry) => cloneGate(entry.gate))
  return {
    repository,
    decisionPoint,
    ruleSetID: selected.ruleSetID,
    ruleSetName: selected.ruleSetName,
    assigned: selected.assigned,
    entries,
    effective,
    noop: effective.every((gate) => gate.kind === "zero"),
  }
}

function cloneRuleSet(ruleSet: ReviewAttentionRuleSet): ReviewAttentionRuleSet {
  const rules = nullRecord<ReviewAttentionGate[]>()
  for (const [decisionPoint, gates] of Object.entries(ruleSet.rules)) {
    rules[decisionPoint] = gates.map(cloneGate)
  }
  return { name: ruleSet.name, rules }
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
      "Workflow moment must start with a lowercase letter and contain only lowercase letters, digits, dot, underscore, or hyphen (128 bytes maximum).",
    )
  }
}

function validateRuleSetID(
  value: string,
  path: string,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (
    !ruleSetIDPattern.test(value) ||
    encodedLength(value) > limits.ruleSetIDBytes
  ) {
    addIssue(
      issues,
      path,
      "rule_set.id_invalid",
      "Rule set ID must start with a lowercase letter and contain only lowercase letters, digits, underscore, or hyphen (64 bytes maximum).",
    )
  }
}

function validateRuleSetName(
  value: string,
  path: string,
  issues: ReviewAttentionPolicyIssue[],
) {
  if (
    trimGoSpace(value) === "" ||
    trimGoSpace(value) !== value ||
    !validUnicode(value) ||
    encodedLength(value) > limits.ruleSetNameBytes
  ) {
    addIssue(
      issues,
      path,
      "rule_set.name_invalid",
      `Rule set name must be trimmed, nonblank valid UTF-8, and at most ${limits.ruleSetNameBytes} bytes.`,
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
      "Check ID must start with a lowercase letter and contain only lowercase letters, digits, underscore, or hyphen (64 bytes maximum).",
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
      "AI checks require an exact configured agent ID.",
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
  const ruleSetEntries = Object.keys(catalog.rule_sets)
    .sort(compareText)
    .map((id) => {
      const ruleSet = catalog.rule_sets[id]
      return [
        id,
        createExactJSONObject([
          ["name", ruleSet.name],
          [
            "rules",
            createExactJSONObject(
              Object.keys(ruleSet.rules)
                .sort(compareText)
                .map(
                  (decisionPoint) =>
                    [
                      decisionPoint,
                      ruleSet.rules[decisionPoint].map(gateValue),
                    ] as const,
                ),
            ),
          ],
        ]),
      ] as const
    })
  const assignmentEntries = Object.keys(catalog.repository_assignments)
    .sort(compareRepositories)
    .map(
      (repository) =>
        [repository, catalog.repository_assignments[repository]] as const,
    )
  return createExactJSONObject([
    ["rule_sets", createExactJSONObject(ruleSetEntries)],
    ["default_rule_set_id", catalog.default_rule_set_id],
    ["repository_assignments", createExactJSONObject(assignmentEntries)],
  ])
}

function configCatalogValue(
  catalog: ReviewAttentionPolicyCatalog,
): ExactJSONValue {
  const value = catalogValue(catalog) as Record<string, ExactJSONValue>
  return createExactJSONObject([
    ["rule_sets", value.rule_sets],
    ["default_rule_set_id", value.default_rule_set_id],
    ...(Object.keys(catalog.repository_assignments).length === 0
      ? []
      : [["repository_assignments", value.repository_assignments] as const]),
  ])
}

function requestCatalogValue(
  catalog: ReviewAttentionPolicyCatalog,
  revision: string,
): ExactJSONValue {
  const value = catalogValue(catalog) as Record<string, ExactJSONValue>
  return createExactJSONObject([
    ["expected_config_revision", revision],
    ["rule_sets", value.rule_sets],
    ["default_rule_set_id", value.default_rule_set_id],
    ["repository_assignments", value.repository_assignments],
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
