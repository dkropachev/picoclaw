import { launcherFetch } from "@/api/http"

export const maximumCollectionQueryBytes = 4096
export const maximumCollectionPageSize = 200
export const maximumCollectionBulkDeleteItems = 200

export type CollectionQueryFieldType =
  | "string"
  | "enum"
  | "boolean"
  | "number"
  | "timestamp"

export interface CollectionQueryField {
  name: string
  type: CollectionQueryFieldType
  operators: string[]
  sortable: boolean
  suggested_values?: string[]
}

export interface CollectionQuerySchema {
  fields: CollectionQueryField[]
}

export interface CollectionDefaultOrderField {
  field: string
  direction: "ASC" | "DESC"
}

export interface CollectionPageMetadata {
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
}

export interface CollectionPage<T> extends CollectionPageMetadata {
  items: T[]
}

export interface CollectionBulkDeleteFailure {
  id: string
  code: string
  blockers?: string[]
}

export interface CollectionMutationEffects {
  launcher_effect: "applied"
  catalog_effect: "applied"
  gateway_effect: "applied" | "restart_required"
}

export interface CollectionBulkDeleteResponse {
  deleted_ids: string[]
  failures: CollectionBulkDeleteFailure[]
}

export interface CollectionConfigBulkDeleteResponse extends CollectionBulkDeleteResponse {
  config_revision: string
  effects: CollectionMutationEffects
  cleanup_failures?: CollectionBulkDeleteFailure[]
}

export interface CollectionBulkDeleteRequest {
  ids: string[]
  revision?: string
  versions?: Record<string, number | string>
}

export class CollectionAPIError extends Error {
  readonly status: number
  readonly code?: string
  readonly position?: number

  constructor(
    status: number,
    message: string,
    options: { code?: string; position?: number } = {},
  ) {
    super(message)
    this.name = "CollectionAPIError"
    this.status = status
    this.code = options.code
    this.position = options.position
  }
}

export interface CollectionListRequest {
  query?: string
  cursor?: string
  limit?: number
}

export function collectionListURL(
  path: string,
  input: CollectionListRequest = {},
): string {
  const parameters = new URLSearchParams()
  const query = input.query?.trim()
  if (query) parameters.set("query", truncateCollectionQuery(query))
  if (input.cursor) parameters.set("cursor", input.cursor)
  if (input.limit != null) {
    const limit = Math.min(
      maximumCollectionPageSize,
      Math.max(1, Math.trunc(input.limit)),
    )
    parameters.set("limit", String(limit))
  }
  return parameters.size > 0 ? `${path}?${parameters.toString()}` : path
}

export async function collectionRequest<T>(
  path: string,
  options?: RequestInit,
  signal?: AbortSignal,
): Promise<T> {
  const response = await launcherFetch(
    path,
    signal ? { ...options, signal } : options,
  )
  if (!response.ok) {
    throw await collectionAPIErrorFromResponse(response)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export async function collectionAPIErrorFromResponse(
  response: Response,
): Promise<CollectionAPIError> {
  const detail = await response.text().catch(() => "")
  let message = detail.trim()
  let code: string | undefined
  let position: number | undefined

  if (detail) {
    try {
      const value = JSON.parse(detail) as Record<string, unknown>
      if (typeof value.message === "string" && value.message.trim()) {
        message = value.message.trim()
      } else if (typeof value.error === "string" && value.error.trim()) {
        message = value.error.trim()
      }
      if (
        typeof value.code === "string" &&
        /^[a-z0-9_.-]{1,64}$/.test(value.code)
      ) {
        code = value.code
      }
      if (
        typeof value.position === "number" &&
        Number.isSafeInteger(value.position) &&
        value.position >= 0 &&
        value.position <= maximumCollectionQueryBytes
      ) {
        position = value.position
      }
    } catch {
      // Preserve safe bounded plain-text API errors.
    }
  }

  return new CollectionAPIError(
    response.status,
    boundCollectionErrorMessage(
      message || `API error: ${response.status} ${response.statusText}`,
    ),
    { ...(code ? { code } : {}), ...(position != null ? { position } : {}) },
  )
}

const maximumCollectionSchemaFields = 128
const maximumCollectionFieldNameBytes = 64
const maximumCollectionSuggestedValues = 100
const maximumCollectionSuggestedValueBytes = 256
const maximumCollectionDefaultOrderFields = 3
const collectionFieldTypes = new Set<CollectionQueryFieldType>([
  "string",
  "enum",
  "boolean",
  "number",
  "timestamp",
])
const meaningfulCollectionOperators: Record<
  CollectionQueryFieldType,
  ReadonlySet<string>
> = {
  string: new Set(["=", "!=", "~", "!~", "IN", "NOT IN"]),
  enum: new Set(["=", "!=", "IN", "NOT IN"]),
  boolean: new Set(["=", "!=", "IN", "NOT IN"]),
  number: new Set(["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]),
  timestamp: new Set(["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]),
}

export function projectCollectionQuerySchema(
  value: unknown,
  expectedDefaultOrder: readonly CollectionDefaultOrderField[],
): CollectionQuerySchema {
  if (
    !collectionRecord(value) ||
    !Array.isArray(value.fields) ||
    value.fields.length === 0 ||
    value.fields.length > maximumCollectionSchemaFields ||
    !Array.isArray(value.default_order) ||
    value.default_order.length === 0 ||
    value.default_order.length > maximumCollectionDefaultOrderFields
  ) {
    malformedCollectionSchema()
  }
  const fields = value.fields.map(projectCollectionQueryField)
  rejectCollectionSchemaDuplicates(fields.map((field) => field.name))
  const fieldsByName = new Map(fields.map((field) => [field.name, field]))
  const defaultOrder = value.default_order.map((entry) => {
    if (
      !collectionRecord(entry) ||
      typeof entry.field !== "string" ||
      (entry.direction !== "ASC" && entry.direction !== "DESC") ||
      !fieldsByName.get(entry.field)?.sortable
    ) {
      malformedCollectionSchema()
    }
    return { field: entry.field, direction: entry.direction }
  })
  rejectCollectionSchemaDuplicates(defaultOrder.map((entry) => entry.field))
  if (
    defaultOrder.length !== expectedDefaultOrder.length ||
    defaultOrder.some(
      (entry, index) =>
        entry.field !== expectedDefaultOrder[index]?.field ||
        entry.direction !== expectedDefaultOrder[index]?.direction,
    )
  ) {
    malformedCollectionSchema()
  }
  return { fields }
}

function projectCollectionQueryField(value: unknown): CollectionQueryField {
  if (
    !collectionRecord(value) ||
    typeof value.name !== "string" ||
    value.name === "all" ||
    value.name === "not" ||
    !/^[a-z][a-z0-9_.-]*$/u.test(value.name) ||
    collectionQueryByteLength(value.name) > maximumCollectionFieldNameBytes ||
    collectionHasUnpairedSurrogate(value.name) ||
    typeof value.type !== "string" ||
    !collectionFieldTypes.has(value.type as CollectionQueryFieldType) ||
    !Array.isArray(value.operators) ||
    value.operators.length === 0 ||
    value.operators.length > 10 ||
    typeof value.sortable !== "boolean"
  ) {
    malformedCollectionSchema()
  }
  const type = value.type as CollectionQueryFieldType
  const meaningfulOperators = meaningfulCollectionOperators[type]
  const operators = value.operators.map((operator) => {
    if (typeof operator !== "string" || !meaningfulOperators.has(operator)) {
      malformedCollectionSchema()
    }
    return operator
  })
  rejectCollectionSchemaDuplicates(operators)
  const rawSuggestions = value.suggested_values ?? []
  if (
    !Array.isArray(rawSuggestions) ||
    rawSuggestions.length > maximumCollectionSuggestedValues
  ) {
    malformedCollectionSchema()
  }
  const suggestedValues = rawSuggestions.map((suggestion) => {
    if (
      typeof suggestion !== "string" ||
      suggestion === "" ||
      suggestion.trim() !== suggestion ||
      collectionQueryByteLength(suggestion) >
        maximumCollectionSuggestedValueBytes ||
      collectionHasUnpairedSurrogate(suggestion) ||
      /\p{Cc}/u.test(suggestion) ||
      (type === "enum" && suggestion.toLowerCase() !== suggestion)
    ) {
      malformedCollectionSchema()
    }
    return suggestion
  })
  if (
    new Set(suggestedValues.map((suggestion) => suggestion.toLowerCase()))
      .size !== suggestedValues.length ||
    (type === "enum" && suggestedValues.length === 0)
  ) {
    malformedCollectionSchema()
  }
  return {
    name: value.name,
    type,
    operators,
    sortable: value.sortable,
    ...(suggestedValues.length > 0
      ? { suggested_values: suggestedValues }
      : {}),
  }
}

function collectionRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function collectionHasUnpairedSurrogate(value: string): boolean {
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

function rejectCollectionSchemaDuplicates(values: string[]) {
  if (new Set(values).size !== values.length) malformedCollectionSchema()
}

function malformedCollectionSchema(): never {
  throw new CollectionAPIError(
    502,
    "The server returned an invalid collection query schema.",
    { code: "malformed_response" },
  )
}

export function collectionQueryByteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength
}

export function truncateCollectionQuery(value: string): string {
  if (collectionQueryByteLength(value) <= maximumCollectionQueryBytes) {
    return value
  }
  let bytes = 0
  let result = ""
  for (const character of value) {
    const characterBytes = collectionQueryByteLength(character)
    if (bytes + characterBytes > maximumCollectionQueryBytes) break
    bytes += characterBytes
    result += character
  }
  return result
}

export function collectionUTF8BytePositionToUTF16Offset(
  value: string,
  bytePosition: number,
): number {
  const boundedPosition = Math.max(0, Math.trunc(bytePosition))
  let bytes = 0
  let offset = 0
  for (const character of value) {
    const characterBytes = collectionQueryByteLength(character)
    if (bytes + characterBytes > boundedPosition) break
    bytes += characterBytes
    offset += character.length
  }
  return offset
}

function boundCollectionErrorMessage(value: string): string {
  const normalized = value.replace(/[\r\n\t]+/g, " ").trim()
  return truncateUTF8(normalized, 1024)
}

function truncateUTF8(value: string, maximumBytes: number): string {
  let bytes = 0
  let result = ""
  for (const character of value) {
    const characterBytes = collectionQueryByteLength(character)
    if (bytes + characterBytes > maximumBytes) break
    bytes += characterBytes
    result += character
  }
  return result
}
