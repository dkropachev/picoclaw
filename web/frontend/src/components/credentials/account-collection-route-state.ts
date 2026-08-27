import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import {
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const accountsDefaultQuery = "ORDER BY provider ASC, id ASC"
export const accountRoutersDefaultQuery = "ORDER BY name ASC"
export const accountCollectionViews = ["list", "table", "grid"] as const

export function normalizeAccountsCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: accountsDefaultQuery,
    supportedViews: accountCollectionViews,
  })
}

export function normalizeAccountRoutersCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: accountRoutersDefaultQuery,
    supportedViews: accountCollectionViews,
  })
}

export function accountCollectionSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}
