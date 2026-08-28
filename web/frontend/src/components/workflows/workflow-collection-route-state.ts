import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import {
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const workflowDefinitionsDefaultQuery = "ORDER BY ref ASC"
export const workflowRunsDefaultQuery = "ORDER BY created DESC"
export const workflowDefinitionViews = ["list", "table", "grid"] as const
export const workflowRunViews = ["list", "table"] as const

export function normalizeWorkflowDefinitionsSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: workflowDefinitionsDefaultQuery,
    supportedViews: workflowDefinitionViews,
  })
}

export function normalizeWorkflowRunsSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: workflowRunsDefaultQuery,
    supportedViews: workflowRunViews,
  })
}

export function workflowCollectionSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}
