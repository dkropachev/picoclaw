import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import {
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const eventSourcesDefaultQuery = "ORDER BY name ASC"
export const eventSourceCollectionViews = ["list", "table", "grid"] as const

export function normalizeEventSourcesCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: eventSourcesDefaultQuery,
    supportedViews: eventSourceCollectionViews,
  })
}

export function eventSourceCollectionSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}
