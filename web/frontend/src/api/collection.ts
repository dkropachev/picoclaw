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
