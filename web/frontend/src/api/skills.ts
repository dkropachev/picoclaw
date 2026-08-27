import {
  type CollectionBulkDeleteResponse,
  type CollectionQuerySchema,
  collectionListURL,
  collectionRequest,
} from "@/api/collection"
import { launcherFetch } from "@/api/http"

export interface SkillSupportItem {
  id: string
  name: string
  path: string
  source: "workspace" | "global" | "builtin" | string
  description: string
  origin: string
  origin_kind: "builtin" | "third_party" | "manual" | string
  registry?: string
  registry_name?: string
  registry_url?: string
  version?: string
  installed_version?: string
  installed_at?: number
  removable?: boolean
}

export interface SkillDetailResponse extends SkillSupportItem {
  content: string
}

export interface SkillRegistrySearchResult {
  score: number
  slug: string
  display_name: string
  summary: string
  version: string
  registry_name: string
  url?: string
  installed: boolean
  installed_name?: string
}

export interface SkillsCollectionResponse {
  skills: SkillSupportItem[]
  total: number
  next_cursor?: string
  canonical_query: string
  query_schema: CollectionQuerySchema
}

export interface SkillSearchResponse {
  results: SkillRegistrySearchResult[]
  limit: number
  offset: number
  next_offset?: number
  has_more: boolean
}

export type SkillActionResponse = Partial<SkillSupportItem> & {
  status?: string
}

export interface InstallSkillRequest {
  slug: string
  registry: string
  version?: string
  force?: boolean
}

export interface InstallSkillResponse {
  status: string
  slug: string
  registry: string
  version: string
  summary?: string
  is_suspicious?: boolean
  skill?: SkillSupportItem
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    throw new Error(await extractErrorMessage(res))
  }
  return res.json() as Promise<T>
}

export function listSkills(
  options: { query?: string; cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<SkillsCollectionResponse> {
  return collectionRequest<SkillsCollectionResponse>(
    collectionListURL("/api/skills", options),
    undefined,
    signal,
  )
}

/** @deprecated Collection UIs should use listSkills. */
export async function getSkills(): Promise<SkillsCollectionResponse> {
  return listSkills()
}

export function getSkill(
  id: string,
  signal?: AbortSignal,
): Promise<SkillDetailResponse> {
  return collectionRequest<SkillDetailResponse>(
    `/api/skills/${encodeURIComponent(id)}`,
    undefined,
    signal,
  )
}

export function bulkDeleteSkills(
  ids: string[],
): Promise<CollectionBulkDeleteResponse> {
  return collectionRequest<CollectionBulkDeleteResponse>(
    "/api/skills/bulk-delete",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ids }),
    },
  )
}

export async function searchSkills(
  query: string,
  limit = 20,
  offset = 0,
): Promise<SkillSearchResponse> {
  const params = new URLSearchParams({
    q: query,
    limit: String(limit),
    offset: String(offset),
  })
  return request<SkillSearchResponse>(`/api/skills/search?${params.toString()}`)
}

export async function installSkill(
  input: InstallSkillRequest,
): Promise<InstallSkillResponse> {
  return request<InstallSkillResponse>("/api/skills/install", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  })
}

export async function importSkill(file: File): Promise<SkillActionResponse> {
  const formData = new FormData()
  formData.set("file", file)

  const res = await launcherFetch("/api/skills/import", {
    method: "POST",
    body: formData,
  })
  if (!res.ok) {
    throw new Error(await extractErrorMessage(res))
  }
  return res.json() as Promise<SkillActionResponse>
}

export function deleteSkill(id: string): Promise<SkillActionResponse> {
  return collectionRequest<SkillActionResponse>(
    `/api/skills/${encodeURIComponent(id)}`,
    {
      method: "DELETE",
    },
  )
}

async function extractErrorMessage(res: Response): Promise<string> {
  try {
    const raw = await res.text()
    if (raw.trim() === "") {
      return `API error: ${res.status} ${res.statusText}`
    }
    try {
      const body = JSON.parse(raw) as {
        error?: string
        errors?: string[]
      }
      if (Array.isArray(body.errors) && body.errors.length > 0) {
        return body.errors.join("; ")
      }
      if (typeof body.error === "string" && body.error.trim() !== "") {
        return body.error
      }
    } catch {
      return raw.trim()
    }
  } catch {
    // ignore invalid body
  }
  return `API error: ${res.status} ${res.statusText}`
}
