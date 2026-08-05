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

export type ReviewAttentionPolicyMode =
  | "inherit"
  | "overlay"
  | "replace"
  | "disable"

export interface ReviewAttentionGate {
  id: string
  kind: ReviewAttentionGateKind
  agent_id?: string
  criteria?: string
  when?: string
  title?: string
  questions?: ExactJSONValue
}

export interface ReviewAttentionRepositoryPolicy {
  mode: ReviewAttentionPolicyMode
  gates: ReviewAttentionGate[]
}

export interface ReviewAttentionPolicyCatalog {
  global: Record<string, ReviewAttentionGate[]>
  repositories: Record<string, Record<string, ReviewAttentionRepositoryPolicy>>
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
const maximumDecisionPoints = 128
const maximumRepositories = 1024
const maximumPolicies = 8192
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
const policyModes = new Set<ReviewAttentionPolicyMode>([
  "inherit",
  "overlay",
  "replace",
  "disable",
])

const snapshotKeys = new Set([
  "global",
  "repositories",
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
const repositoryPolicyKeys = new Set(["mode", "gates"])
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
      ["global", policyMapToExactJSON(catalog.global)],
      ["repositories", repositoriesToExactJSON(catalog.repositories)],
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
  const global = projectPolicyMap(root.global)
  const repositoriesValue = exactObject(root.repositories)
  if (Object.keys(repositoriesValue).length > maximumRepositories) invalid()

  let policyCount = Object.keys(global).length
  let gateCount = Object.values(global).reduce(
    (total, gates) => total + gates.length,
    0,
  )
  const repositories =
    emptyRecord<Record<string, ReviewAttentionRepositoryPolicy>>()
  for (const repository of Object.keys(repositoriesValue)) {
    const configured = exactObject(repositoriesValue[repository])
    if (Object.keys(configured).length > maximumDecisionPoints) invalid()
    const policies = emptyRecord<ReviewAttentionRepositoryPolicy>()
    for (const decisionPoint of Object.keys(configured)) {
      const policy = projectRepositoryPolicy(configured[decisionPoint])
      policies[decisionPoint] = policy
      policyCount += 1
      gateCount += policy.gates.length
    }
    repositories[repository] = policies
  }
  if (policyCount > maximumPolicies || gateCount > maximumGates) invalid()

  const effects = exactObject(root.effects)
  onlyKeys(effects, effectsKeys)
  const gatewayEffect = exactString(effects.gateway_effect)
  if (gatewayEffect !== "applied" && gatewayEffect !== "restart_required") {
    invalid()
  }

  const snapshot: ReviewAttentionPolicySnapshot = {
    global,
    repositories,
    catalog_revision: opaqueToken(root.catalog_revision),
    config_revision: configRevisionToken(root.config_revision),
    effects: { gateway_effect: gatewayEffect },
  }
  if (!isReviewAttentionPolicyCatalogSemanticallyValid(snapshot)) invalid()
  return snapshot
}

function projectPolicyMap(
  value: ExactJSONValue | undefined,
): Record<string, ReviewAttentionGate[]> {
  const configured = exactObject(value)
  if (Object.keys(configured).length > maximumDecisionPoints) invalid()
  const policies = emptyRecord<ReviewAttentionGate[]>()
  for (const decisionPoint of Object.keys(configured)) {
    policies[decisionPoint] = projectGateList(configured[decisionPoint])
  }
  return policies
}

function projectRepositoryPolicy(
  value: ExactJSONValue | undefined,
): ReviewAttentionRepositoryPolicy {
  const policy = exactObject(value)
  onlyKeys(policy, repositoryPolicyKeys)
  const mode = exactString(policy.mode)
  if (!policyModes.has(mode as ReviewAttentionPolicyMode)) invalid()
  const gates = policy.gates === undefined ? [] : projectGateList(policy.gates)
  if (
    ((mode === "inherit" || mode === "disable") && gates.length !== 0) ||
    ((mode === "overlay" || mode === "replace") && gates.length === 0)
  ) {
    invalid()
  }
  return { mode: mode as ReviewAttentionPolicyMode, gates }
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

function repositoriesToExactJSON(
  repositories: ReviewAttentionPolicyCatalog["repositories"],
): ExactJSONObject {
  return createExactJSONObject(
    Object.keys(repositories).map((repository) => {
      const policies = repositories[repository]
      return [
        repository,
        createExactJSONObject(
          Object.keys(policies).map((decisionPoint) => {
            const policy = policies[decisionPoint]
            return [
              decisionPoint,
              createExactJSONObject([
                ["mode", policy.mode],
                ["gates", policy.gates.map(gateToExactJSON)],
              ]),
            ] as const
          }),
        ),
      ] as const
    }),
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
