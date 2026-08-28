import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import {
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const gitWorkspacesDefaultQuery = "ORDER BY updated DESC"
export const gitWorkspaceHistoryDefaultQuery = "ORDER BY time DESC"
export const gitWorkspaceViews = ["list", "table", "grid"] as const
export const gitWorkspaceHistoryViews = ["list", "table"] as const

export function normalizeGitWorkspacesSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: gitWorkspacesDefaultQuery,
    supportedViews: gitWorkspaceViews,
  })
}

export function normalizeGitWorkspaceHistorySearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: gitWorkspaceHistoryDefaultQuery,
    supportedViews: gitWorkspaceHistoryViews,
  })
}

export function gitWorkspaceSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}
