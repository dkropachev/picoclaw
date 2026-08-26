import type { RepositoryReviewReportScope } from "@/api/repository-reviews"
import type { CollectionView } from "@/components/collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const repositoryReviewDefaultQuery = "ORDER BY repository ASC"
export const repositoryReviewViews = ["list", "table", "grid"] as const

export interface RepositoryReviewRouteSearch {
  q: string
  view?: CollectionView
  scope: RepositoryReviewReportScope
  offset: number
  generation_id?: string
}

export function normalizeRepositoryReviewRouteSearch(
  raw: Record<string, unknown>,
): RepositoryReviewRouteSearch {
  const collection = normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
  const numericOffset =
    typeof raw.offset === "number"
      ? raw.offset
      : typeof raw.offset === "string" && raw.offset.trim() !== ""
        ? Number(raw.offset)
        : 0
  const offset =
    Number.isSafeInteger(numericOffset) && numericOffset >= 0
      ? Math.min(numericOffset, 1_000_000_000)
      : 0
  const generationID =
    typeof raw.generation_id === "string" &&
    /^[A-Za-z0-9_-]{1,128}$/u.test(raw.generation_id)
      ? raw.generation_id
      : undefined
  return {
    ...collection,
    scope: raw.scope === "all" ? "all" : "current",
    offset,
    ...(generationID ? { generation_id: generationID } : {}),
  }
}

export function collectionSearchFromReviewSearch(
  search: RepositoryReviewRouteSearch,
): { q: string; view?: CollectionView } {
  return {
    q: search.q,
    ...(search.view ? { view: search.view } : {}),
  }
}
