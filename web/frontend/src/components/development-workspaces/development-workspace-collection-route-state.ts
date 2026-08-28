import type { CollectionView } from "@/components/collection"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const developmentWorkspacesDefaultQuery = "ORDER BY updated DESC"
export const developmentWorkspaceCollectionViews = [
  "list",
  "table",
  "grid",
] as const satisfies readonly CollectionView[]

export interface DevelopmentWorkspacesRouteSearch {
  q?: string
  view?: CollectionView
}

export function normalizeDevelopmentWorkspacesSearch(
  raw: object,
): DevelopmentWorkspacesRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: developmentWorkspacesDefaultQuery,
    supportedViews: developmentWorkspaceCollectionViews,
  })
}
