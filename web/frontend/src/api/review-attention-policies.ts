import { launcherFetch } from "@/api/http"
import { isValidReviewAttentionConfigRevision } from "@/api/review-attention-agents"
import {
  type ExactJSONObject,
  type ExactJSONValue,
  cloneExactJSON,
  createExactJSONObject,
  isExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"
import { isReviewAttentionPolicyCatalogSemanticallyValid } from "@/components/reviews/review-attention-policy-model"

export type ReviewAttentionGateKind =
  | "ai_working_context"
  | "ai_isolated_context"
  | "deterministic"
  | "zero"

export interface ReviewAttentionGate {
  id: string
  kind: ReviewAttentionGateKind
  agent_id?: string
  criteria?: string
  when?: string
  title?: string
  questions?: ExactJSONValue
}

export const reviewAttentionBuiltInRuleSetID = "default"
export const reviewAttentionBuiltInRuleSetName = "Default"

export interface ReviewAttentionRuleSet {
  name: string
  rules: Record<string, ReviewAttentionGate[]>
}

export interface ReviewAttentionPolicyCatalog {
  rule_sets: Record<string, ReviewAttentionRuleSet>
  default_rule_set_id: string
  repository_assignments: Record<string, string>
}

export interface ReviewAttentionPolicySnapshot extends ReviewAttentionPolicyCatalog {
  catalog_revision: string
  config_revision: string
  effects: {
    gateway_effect: "applied" | "restart_required"
  }
}

export class ReviewAttentionPoliciesAPIError extends Error {
  readonly status: number
  readonly code: string

  constructor(code: string, status: number, message = code) {
    super(message)
    this.name = "ReviewAttentionPoliciesAPIError"
    this.status = status
    this.code = code
  }
}

const attentionPoliciesPath = "/api/reviews/attention-policies"
const attentionPolicyEnvelopeMaximumBytes = (1 << 20) + (64 << 10)
const attentionPolicyErrorMaximumBytes = 64 << 10
// One set per legacy repository override plus the built-in Default covers the
// largest representable named migration; oversized expansion stays on the
// legacy runtime and is rejected before this client receives a partial catalog.
const maximumRuleSets = 1025
const maximumDecisionPointsPerRuleSet = 128
const maximumRepositories = 1024
const maximumRules = 8192
const maximumGates = 8192
const maximumGatesPerPolicy = 64
const maximumQuestionBytes = 128 << 10
const maximumQuestionDepth = 64
const maximumQuestionNodes = 100_000

const gateKinds = new Set<ReviewAttentionGateKind>([
  "ai_working_context",
  "ai_isolated_context",
  "deterministic",
  "zero",
])
const snapshotKeys = new Set([
  "rule_sets",
  "default_rule_set_id",
  "repository_assignments",
  "catalog_revision",
  "config_revision",
  "effects",
])
const gateKeys = new Set([
  "id",
  "kind",
  "agent_id",
  "criteria",
  "when",
  "title",
  "questions",
])
const ruleSetKeys = new Set(["name", "rules"])
const effectsKeys = new Set(["gateway_effect"])

export async function getReviewAttentionPolicies(
  signal?: AbortSignal,
): Promise<ReviewAttentionPolicySnapshot> {
  return requestReviewAttentionPolicies(undefined, signal)
}

export async function putReviewAttentionPolicies(
  catalog: ReviewAttentionPolicyCatalog,
  expectedConfigRevision: string,
  signal?: AbortSignal,
): Promise<ReviewAttentionPolicySnapshot> {
  if (!isValidReviewAttentionConfigRevision(expectedConfigRevision)) {
    throw new ReviewAttentionPoliciesAPIError(
      "expected_config_revision_required",
      400,
    )
  }
  const body = stringifyExactJSON(
    createExactJSONObject([
      ["expected_config_revision", expectedConfigRevision],
      ["rule_sets", ruleSetsToExactJSON(catalog.rule_sets)],
      ["default_rule_set_id", catalog.default_rule_set_id],
      [
        "repository_assignments",
        stringMapToExactJSON(catalog.repository_assignments),
      ],
    ]),
    { maximumBytes: attentionPolicyEnvelopeMaximumBytes },
  )
  return requestReviewAttentionPolicies(
    {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body,
    },
    signal,
  )
}

async function requestReviewAttentionPolicies(
  init: RequestInit | undefined,
  signal: AbortSignal | undefined,
): Promise<ReviewAttentionPolicySnapshot> {
  const response = await launcherFetch(attentionPoliciesPath, {
    ...init,
    signal,
  })
  let text: string
  try {
    text = await boundedUTF8ResponseText(
      response,
      response.ok
        ? attentionPolicyEnvelopeMaximumBytes
        : attentionPolicyErrorMaximumBytes,
    )
  } catch {
    throw new ReviewAttentionPoliciesAPIError(
      response.ok
        ? "invalid_attention_policy_response"
        : "attention_policies_unavailable",
      response.ok ? 502 : response.status,
    )
  }
  if (!response.ok) {
    const code = reviewAttentionErrorCode(text)
    throw new ReviewAttentionPoliciesAPIError(
      code ?? "attention_policies_unavailable",
      response.status,
    )
  }
  if (!jsonContentType(response.headers.get("Content-Type"))) {
    throw new ReviewAttentionPoliciesAPIError(
      "invalid_attention_policy_response",
      502,
    )
  }
  try {
    return projectReviewAttentionPolicySnapshot(
      parseExactJSON(text, {
        maximumBytes: attentionPolicyEnvelopeMaximumBytes,
      }),
    )
  } catch (error) {
    if (error instanceof ReviewAttentionPoliciesAPIError) throw error
    throw new ReviewAttentionPoliciesAPIError(
      "invalid_attention_policy_response",
      502,
    )
  }
}

function projectReviewAttentionPolicySnapshot(
  value: ExactJSONValue,
): ReviewAttentionPolicySnapshot {
  const root = exactObject(value)
  onlyKeys(root, snapshotKeys)
  const ruleSets = projectRuleSets(root.rule_sets)
  const defaultRuleSetID = exactString(root.default_rule_set_id)
  const repositoryAssignments = projectStringMap(
    root.repository_assignments,
    maximumRepositories,
  )

  let ruleCount = 0
  let gateCount = 0
  for (const ruleSet of Object.values(ruleSets)) {
    ruleCount += Object.keys(ruleSet.rules).length
    gateCount += Object.values(ruleSet.rules).reduce(
      (total, gates) => total + gates.length,
      0,
    )
  }
  if (ruleCount > maximumRules || gateCount > maximumGates) invalid()

  const effects = exactObject(root.effects)
  onlyKeys(effects, effectsKeys)
  const gatewayEffect = exactString(effects.gateway_effect)
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart_required") {
    invalid()
  }

  const snapshot: ReviewAttentionPolicySnapshot = {
    rule_sets: ruleSets,
    default_rule_set_id: defaultRuleSetID,
    repository_assignments: repositoryAssignments,
    catalog_revision: opaqueToken(root.catalog_revision),
    config_revision: configRevisionToken(root.config_revision),
    effects: { gateway_effect: gatewayEffect },
  }
  if (!isReviewAttentionPolicyCatalogSemanticallyValid(snapshot)) invalid()
  return snapshot
}

function projectRuleSets(
  value: ExactJSONValue | undefined,
): Record<string, ReviewAttentionRuleSet> {
  const configured = exactObject(value)
  if (Object.keys(configured).length > maximumRuleSets) invalid()
  const ruleSets = emptyRecord<ReviewAttentionRuleSet>()
  for (const id of Object.keys(configured)) {
    const ruleSet = exactObject(configured[id])
    onlyKeys(ruleSet, ruleSetKeys)
    ruleSets[id] = {
      name: exactString(ruleSet.name),
      rules: projectPolicyMap(ruleSet.rules),
    }
  }
  return ruleSets
}

function projectPolicyMap(
  value: ExactJSONValue | undefined,
): Record<string, ReviewAttentionGate[]> {
  const configured = exactObject(value)
  if (Object.keys(configured).length > maximumDecisionPointsPerRuleSet)
    invalid()
  const policies = emptyRecord<ReviewAttentionGate[]>()
  for (const decisionPoint of Object.keys(configured)) {
    policies[decisionPoint] = projectGateList(configured[decisionPoint])
  }
  return policies
}

function projectStringMap(
  value: ExactJSONValue | undefined,
  maximumEntries: number,
): Record<string, string> {
  const configured = exactObject(value)
  if (Object.keys(configured).length > maximumEntries) invalid()
  const projected = emptyRecord<string>()
  for (const key of Object.keys(configured)) {
    projected[key] = exactString(configured[key])
  }
  return projected
}

function projectGateList(
  value: ExactJSONValue | undefined,
): ReviewAttentionGate[] {
  if (!Array.isArray(value) || value.length > maximumGatesPerPolicy) invalid()
  return value.map(projectGate)
}

function projectGate(value: ExactJSONValue): ReviewAttentionGate {
  const gate = exactObject(value)
  onlyKeys(gate, gateKeys)
  const kind = exactString(gate.kind)
  if (!gateKinds.has(kind as ReviewAttentionGateKind)) invalid()
  const projectedKind = kind as ReviewAttentionGateKind
  const id = exactString(gate.id)
  const agentID = optionalExactString(gate.agent_id)
  const criteria = optionalExactString(gate.criteria)
  const when = optionalExactString(gate.when)
  const title = optionalExactString(gate.title)
  const questions = gate.questions
  if (questions === null) invalid()

  switch (projectedKind) {
    case "ai_working_context":
    case "ai_isolated_context":
      if (
        agentID === undefined ||
        criteria === undefined ||
        title === undefined ||
        when !== undefined
      ) {
        invalid()
      }
      return {
        id,
        kind: projectedKind,
        agent_id: agentID,
        criteria,
        title,
        ...(questions === undefined
          ? {}
          : { questions: projectQuestions(questions) }),
      }
    case "deterministic":
      if (
        agentID !== undefined ||
        criteria !== undefined ||
        when === undefined ||
        title === undefined ||
        questions === undefined
      ) {
        invalid()
      }
      return {
        id,
        kind: projectedKind,
        when,
        title,
        questions: projectQuestions(questions),
      }
    case "zero":
      if (
        agentID !== undefined ||
        criteria !== undefined ||
        when !== undefined ||
        title !== undefined ||
        questions !== undefined
      ) {
        invalid()
      }
      return { id, kind: projectedKind }
  }
}

function projectQuestions(value: ExactJSONValue): ExactJSONValue {
  let source: string
  try {
    source = stringifyExactJSON(value, {
      maximumBytes: maximumQuestionBytes,
      maximumDepth: maximumQuestionDepth,
      maximumNodes: maximumQuestionNodes,
    })
  } catch {
    invalid()
  }
  if (goJSONEncodedBytes(source) > maximumQuestionBytes) invalid()
  return cloneExactJSON(value)
}

function goJSONEncodedBytes(source: string): number {
  let bytes = new TextEncoder().encode(source).byteLength
  for (const character of source) {
    switch (character) {
      case "<":
      case ">":
      case "&":
        // encoding/json escapes these one-byte characters as six-byte
        // Unicode sequences while HTML escaping is enabled by json.Marshal.
        bytes += 5
        break
      case "\u2028":
      case "\u2029":
        // encoding/json escapes these three-byte JSONP separators as six-byte
        // Unicode sequences even though JavaScript JSON.stringify does not.
        bytes += 3
        break
    }
  }
  return bytes
}

function policyMapToExactJSON(
  policies: Record<string, ReviewAttentionGate[]>,
): ExactJSONObject {
  return createExactJSONObject(
    Object.keys(policies).map(
      (decisionPoint) =>
        [decisionPoint, policies[decisionPoint].map(gateToExactJSON)] as const,
    ),
  )
}

function ruleSetsToExactJSON(
  ruleSets: ReviewAttentionPolicyCatalog["rule_sets"],
): ExactJSONObject {
  return createExactJSONObject(
    Object.keys(ruleSets).map(
      (id) =>
        [
          id,
          createExactJSONObject([
            ["name", ruleSets[id].name],
            ["rules", policyMapToExactJSON(ruleSets[id].rules)],
          ]),
        ] as const,
    ),
  )
}

function stringMapToExactJSON(values: Record<string, string>): ExactJSONObject {
  return createExactJSONObject(
    Object.keys(values).map((key) => [key, values[key]] as const),
  )
}

function gateToExactJSON(gate: ReviewAttentionGate): ExactJSONObject {
  const entries: Array<readonly [string, ExactJSONValue]> = [
    ["id", gate.id],
    ["kind", gate.kind],
  ]
  for (const field of ["agent_id", "criteria", "when", "title"] as const) {
    const value = gate[field]
    if (value !== undefined) entries.push([field, value])
  }
  if (gate.questions !== undefined) {
    entries.push(["questions", cloneExactJSON(gate.questions)])
  }
  return createExactJSONObject(entries)
}

function exactObject(value: ExactJSONValue | undefined): ExactJSONObject {
  if (!isExactJSONObject(value)) invalid()
  return value
}

function exactString(value: ExactJSONValue | undefined): string {
  if (typeof value !== "string") invalid()
  return value
}

function optionalExactString(
  value: ExactJSONValue | undefined,
): string | undefined {
  return value === undefined ? undefined : exactString(value)
}

function opaqueToken(value: ExactJSONValue | undefined): string {
  const token = exactString(value)
  if (!validOpaqueToken(token)) invalid()
  return token
}

function configRevisionToken(value: ExactJSONValue | undefined): string {
  const token = exactString(value)
  if (!isValidReviewAttentionConfigRevision(token)) invalid()
  return token
}

function validOpaqueToken(value: string): boolean {
  return (
    value !== "" &&
    value === trimGoSpace(value) &&
    new TextEncoder().encode(value).byteLength <= 4096
  )
}

function onlyKeys(value: ExactJSONObject, allowed: ReadonlySet<string>) {
  if (Object.keys(value).some((key) => !allowed.has(key))) invalid()
}

function emptyRecord<T>(): Record<string, T> {
  return Object.create(null) as Record<string, T>
}

function invalid(): never {
  throw new ReviewAttentionPoliciesAPIError(
    "invalid_attention_policy_response",
    502,
  )
}

function reviewAttentionErrorCode(text: string): string | undefined {
  try {
    const value = parseExactJSON(text, {
      maximumBytes: attentionPolicyErrorMaximumBytes,
      maximumDepth: 8,
      maximumNodes: 32,
    })
    const object = exactObject(value)
    if (
      Object.keys(object).length === 1 &&
      typeof object.error === "string" &&
      /^[a-z0-9_]+$/.test(object.error)
    ) {
      return object.error
    }
  } catch {
    // Use the fixed unavailable fallback for malformed errors.
  }
  return undefined
}

async function boundedUTF8ResponseText(
  response: Response,
  maximumBytes: number,
): Promise<string> {
  const length = response.headers.get("Content-Length")
  if (
    length !== null &&
    /^\d+$/.test(length) &&
    Number(length) > maximumBytes
  ) {
    throw new Error("response too large")
  }
  if (response.body === null) {
    const text = await response.text()
    if (new TextEncoder().encode(text).byteLength > maximumBytes) {
      throw new Error("response too large")
    }
    return text
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder("utf-8", { fatal: true })
  let total = 0
  let text = ""
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) return text + decoder.decode()
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

function jsonContentType(value: string | null): boolean {
  if (value === null) return false
  const [mediaType, ...parameters] = value.split(";")
  if (mediaType.trim().toLowerCase() !== "application/json") return false
  if (parameters.length > 1) return false
  return parameters.every((parameter) => {
    const separator = parameter.indexOf("=")
    if (separator < 0) return false
    const name = parameter.slice(0, separator).trim().toLowerCase()
    const configured = parameter
      .slice(separator + 1)
      .trim()
      .replace(/^"(.*)"$/, "$1")
      .toLowerCase()
    return name === "charset" && configured === "utf-8"
  })
}
