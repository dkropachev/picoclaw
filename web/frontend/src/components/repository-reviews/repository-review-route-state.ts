import type { HistoryState } from "@tanstack/react-router"

import type { CollectionView } from "@/components/collection"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const repositoryReviewDefaultQuery = "ORDER BY repository ASC"
export const repositoryReviewRunFindingsDefaultQuery =
  "ALL ORDER BY severity DESC, updated DESC"
export const repositoryReviewRawFindingsDefaultQuery =
  "ALL ORDER BY created DESC"
export const repositoryReviewFindingsProcessingDefaultQuery =
  "ALL ORDER BY updated DESC"
export const repositoryReviewRepositoryFindingsDefaultQuery =
  "ALL ORDER BY severity DESC, updated DESC"
export const repositoryReviewIssuesDefaultQuery = "ALL ORDER BY updated DESC"
export const repositoryReviewViews = ["list", "table", "grid"] as const

export type RepositoryReviewCollectionSearch = CollectionRouteSearch

export interface RepositoryReviewIssueRouteSearch extends RepositoryReviewCollectionSearch {
  generation_id?: string
}

interface RepositoryReviewParentNavigationState extends HistoryState {
  repositoryReviewParentSearch?: CollectionRouteSearch
}

export function normalizeRepositoryReviewRunFindingsSearch(
  raw: Record<string, unknown>,
): RepositoryReviewCollectionSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewRunFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
}

export function normalizeRepositoryReviewRawFindingsSearch(
  raw: Record<string, unknown>,
): RepositoryReviewCollectionSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewRawFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
}

export function normalizeRepositoryReviewFindingsProcessingSearch(
  raw: Record<string, unknown>,
): RepositoryReviewCollectionSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewFindingsProcessingDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
}

export function normalizeRepositoryReviewRepositoryFindingsSearch(
  raw: Record<string, unknown>,
): RepositoryReviewCollectionSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewRepositoryFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
}

export function normalizeRepositoryReviewIssuesSearch(
  raw: Record<string, unknown>,
): RepositoryReviewIssueRouteSearch {
  const collection = normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryReviewIssuesDefaultQuery,
    supportedViews: repositoryReviewViews,
  })
  const generationID = normalizeGenerationID(raw.generation_id)
  return {
    ...collection,
    ...(generationID ? { generation_id: generationID } : {}),
  }
}

export function repositoryReviewParentNavigationState(
  search: { q?: string; view?: CollectionView },
  defaultQuery: string,
): RepositoryReviewParentNavigationState {
  return {
    repositoryReviewParentSearch: normalizeCollectionRouteSearch(search, {
      defaultQuery,
      supportedViews: repositoryReviewViews,
    }),
  }
}

export function repositoryReviewParentSearchFromState(
  state: unknown,
  defaultQuery: string,
): CollectionRouteSearch {
  const candidate =
    state && typeof state === "object"
      ? (state as RepositoryReviewParentNavigationState)
          .repositoryReviewParentSearch
      : undefined
  return normalizeCollectionRouteSearch(candidate ?? {}, {
    defaultQuery,
    supportedViews: repositoryReviewViews,
  })
}

export function collectionSearchFromReviewSearch(search: {
  q?: string
  view?: CollectionView
}): CollectionRouteSearch {
  return {
    q: search.q ?? repositoryReviewDefaultQuery,
    ...(search.view ? { view: search.view } : {}),
  }
}

export function repositoryReviewSearchHasLegacyPaging(
  raw: Record<string, unknown>,
): boolean {
  return raw.scope !== undefined || raw.offset !== undefined
}

export function repositoryReviewSearchIsCanonical(
  raw: Record<string, unknown>,
  normalized:
    | RepositoryReviewCollectionSearch
    | RepositoryReviewIssueRouteSearch,
): boolean {
  const rawKeys = Object.keys(raw).filter((key) => raw[key] !== undefined)
  const normalizedRecord = normalized as unknown as Record<string, unknown>
  const normalizedKeys = Object.keys(normalizedRecord)
  return (
    rawKeys.length === normalizedKeys.length &&
    rawKeys.every(
      (key) =>
        normalizedKeys.includes(key) && raw[key] === normalizedRecord[key],
    )
  )
}

function normalizeGenerationID(value: unknown): string | undefined {
  return typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/u.test(value)
    ? value
    : undefined
}
