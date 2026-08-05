import { launcherFetch } from "@/api/http"
import {
  type ExactJSONObject,
  type ExactJSONValue,
  isExactJSONObject,
  parseExactJSON,
  trimGoSpace,
} from "@/api/review-attention-json"

export interface ReviewAttentionAgent {
  id: string
  name: string
}

/** One revision-fenced page from the identity-only attention-agent catalog. */
export interface ReviewAttentionAgentCatalog {
  agents: ReviewAttentionAgent[]
  default_agent_id: string
  config_revision: string
  next_cursor?: string
}

export interface ReviewAttentionAgentsRequest {
  expectedConfigRevision: string
  cursor?: string
  signal?: AbortSignal
}

export type ReviewAttentionAgentsAPIErrorCode =
  | "attention_agents_unavailable"
  | "config_revision_mismatch"
  | "invalid_attention_agents_request"
  | "invalid_attention_agents_response"

export class ReviewAttentionAgentsAPIError extends Error {
  readonly status: number
  readonly code: ReviewAttentionAgentsAPIErrorCode

  constructor(code: ReviewAttentionAgentsAPIErrorCode, status: number) {
    super(code)
    this.name = "ReviewAttentionAgentsAPIError"
    this.status = status
    this.code = code
  }
}

const attentionAgentsPath = "/api/reviews/attention-agents"
const attentionAgentPageSize = 256
const attentionAgentResponseMaximumBytes = 512 << 10
const attentionAgentErrorMaximumBytes = 64 << 10
const attentionAgentResponseMaximumDepth = 8
const attentionAgentResponseMaximumNodes = 1024
const maximumAgentNameBytes = 256
const maximumIfMatchBytes = 4 << 10
const maximumUint32 = 0xffff_ffff
const canonicalAgentIDPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

const responseKeys = new Set([
  "agents",
  "default_agent_id",
  "config_revision",
  "next_cursor",
])
const agentKeys = new Set(["id", "name"])

/**
 * Loads a bounded page for the policy editor. Every page is pinned to the
 * policy snapshot's config revision, and the supplied signal cancels its fetch.
 */
export async function getReviewAttentionAgents(
  request: ReviewAttentionAgentsRequest,
): Promise<ReviewAttentionAgentCatalog> {
  if (
    request === null ||
    typeof request !== "object" ||
    !isValidReviewAttentionConfigRevision(request.expectedConfigRevision)
  ) {
    invalidRequest()
  }
  const offset =
    request.cursor === undefined ? 0 : requestCursor(request.cursor)
  const path =
    request.cursor === undefined
      ? attentionAgentsPath
      : `${attentionAgentsPath}?cursor=${request.cursor}`
  const response = await launcherFetch(path, {
    headers: {
      "If-Match": `"${request.expectedConfigRevision}"`,
    },
    signal: request.signal,
  })

  let text: string
  try {
    text = await boundedUTF8ResponseText(
      response,
      response.ok
        ? attentionAgentResponseMaximumBytes
        : attentionAgentErrorMaximumBytes,
    )
  } catch {
    throw new ReviewAttentionAgentsAPIError(
      response.ok
        ? "invalid_attention_agents_response"
        : "attention_agents_unavailable",
      response.ok ? 502 : response.status,
    )
  }

  if (!response.ok) {
    throw new ReviewAttentionAgentsAPIError(
      reviewAttentionAgentsErrorCode(text, response.status) ??
        "attention_agents_unavailable",
      response.status,
    )
  }
  if (!jsonContentType(response.headers.get("Content-Type"))) invalidResponse()

  try {
    return projectCatalogPage(
      parseExactJSON(text, {
        maximumBytes: attentionAgentResponseMaximumBytes,
        maximumDepth: attentionAgentResponseMaximumDepth,
        maximumNodes: attentionAgentResponseMaximumNodes,
      }),
      request.expectedConfigRevision,
      offset,
    )
  } catch (error) {
    if (error instanceof ReviewAttentionAgentsAPIError) throw error
    invalidResponse()
  }
}

function projectCatalogPage(
  value: ExactJSONValue,
  expectedConfigRevision: string,
  offset: number,
): ReviewAttentionAgentCatalog {
  const root = exactObject(value)
  onlyKeys(root, responseKeys)
  const configured = exactArray(root.agents)
  if (
    configured.length === 0 ||
    configured.length > attentionAgentPageSize ||
    offset + configured.length > maximumUint32
  ) {
    invalidResponse()
  }

  let previousID: string | undefined
  const agents = configured.map((candidate) => {
    const agent = exactObject(candidate)
    onlyKeys(agent, agentKeys)
    const id = canonicalAgentID(agent.id)
    if (previousID !== undefined && id <= previousID) invalidResponse()
    previousID = id
    return { id, name: normalizedAgentName(agent.name) }
  })

  const defaultAgentID = canonicalAgentID(root.default_agent_id)
  const configRevision = exactString(root.config_revision)
  if (
    !isValidReviewAttentionConfigRevision(configRevision) ||
    configRevision !== expectedConfigRevision
  ) {
    invalidResponse()
  }

  const nextCursor = optionalResponseCursor(root.next_cursor)
  if (
    nextCursor !== undefined &&
    (configured.length !== attentionAgentPageSize ||
      Number(nextCursor) !== offset + attentionAgentPageSize)
  ) {
    invalidResponse()
  }

  return {
    agents,
    default_agent_id: defaultAgentID,
    config_revision: configRevision,
    ...(nextCursor === undefined ? {} : { next_cursor: nextCursor }),
  }
}

function normalizedAgentName(value: ExactJSONValue | undefined): string {
  const name = exactString(value)
  if (
    name !== trimGoSpace(name) ||
    encodedLength(name) > maximumAgentNameBytes ||
    /\p{Cc}/u.test(name)
  ) {
    invalidResponse()
  }
  return name
}

function canonicalAgentID(value: ExactJSONValue | undefined): string {
  const id = exactString(value)
  if (!canonicalAgentIDPattern.test(id)) invalidResponse()
  return id
}

function requestCursor(value: unknown): number {
  if (typeof value !== "string") invalidRequest()
  const cursor = canonicalCursor(value)
  if (cursor === undefined) invalidRequest()
  return cursor
}

function optionalResponseCursor(
  value: ExactJSONValue | undefined,
): string | undefined {
  if (value === undefined) return undefined
  const cursor = exactString(value)
  if (canonicalCursor(cursor) === undefined) invalidResponse()
  return cursor
}

function canonicalCursor(value: string): number | undefined {
  if (!/^[1-9]\d*$/.test(value) || value.length > 10) return undefined
  const parsed = Number(value)
  if (
    !Number.isSafeInteger(parsed) ||
    parsed > maximumUint32 ||
    parsed % attentionAgentPageSize !== 0 ||
    String(parsed) !== value
  ) {
    return undefined
  }
  return parsed
}

export function isValidReviewAttentionConfigRevision(
  value: unknown,
): value is string {
  if (typeof value !== "string") return false
  const bytes = new TextEncoder().encode(value)
  if (bytes.length === 0 || bytes.length + 2 > maximumIfMatchBytes) return false
  return bytes.every(
    (byte) => byte >= 0x21 && byte !== 0x22 && byte !== 0x2c && byte !== 0x7f,
  )
}

function reviewAttentionAgentsErrorCode(
  text: string,
  status: number,
): ReviewAttentionAgentsAPIErrorCode | undefined {
  try {
    const value = parseExactJSON(text, {
      maximumBytes: attentionAgentErrorMaximumBytes,
      maximumDepth: 4,
      maximumNodes: 8,
    })
    const object = exactObject(value)
    if (Object.keys(object).length !== 1) return undefined
    if (status === 409 && object.error === "config_revision_mismatch") {
      return "config_revision_mismatch"
    }
    if (status === 400 && object.error === "invalid_attention_agents_request") {
      return "invalid_attention_agents_request"
    }
    if (status >= 500 && object.error === "attention_agents_unavailable") {
      return "attention_agents_unavailable"
    }
  } catch {
    // Malformed and unknown server details collapse to the fixed fallback.
  }
  return undefined
}

function exactObject(value: ExactJSONValue | undefined): ExactJSONObject {
  if (!isExactJSONObject(value)) invalidResponse()
  return value
}

function exactArray(value: ExactJSONValue | undefined): ExactJSONValue[] {
  if (!Array.isArray(value)) invalidResponse()
  return value
}

function exactString(value: ExactJSONValue | undefined): string {
  if (typeof value !== "string") invalidResponse()
  return value
}

function onlyKeys(value: ExactJSONObject, allowed: ReadonlySet<string>) {
  if (Object.keys(value).some((key) => !allowed.has(key))) invalidResponse()
}

function encodedLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

function invalidRequest(): never {
  throw new ReviewAttentionAgentsAPIError(
    "invalid_attention_agents_request",
    400,
  )
}

function invalidResponse(): never {
  throw new ReviewAttentionAgentsAPIError(
    "invalid_attention_agents_response",
    502,
  )
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
    if (encodedLength(text) > maximumBytes) {
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
