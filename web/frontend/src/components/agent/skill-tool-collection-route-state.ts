import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import {
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const administrativeCollectionViews = ["list", "table", "grid"] as const
export const skillsDefaultQuery = "ORDER BY name ASC"
export const toolsDefaultQuery = "ORDER BY category ASC, name ASC"

export function normalizeSkillsCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: skillsDefaultQuery,
    supportedViews: administrativeCollectionViews,
  })
}

export function normalizeToolsCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: toolsDefaultQuery,
    supportedViews: administrativeCollectionViews,
  })
}

export function skillToolCollectionSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}
