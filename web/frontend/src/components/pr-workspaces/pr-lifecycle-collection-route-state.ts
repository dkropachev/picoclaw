import type { CollectionView } from "@/components/collection"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

export const prLifecycleAdministrativeCollectionViews = [
  "list",
  "table",
  "grid",
] as const satisfies readonly CollectionView[]

export const repositoryAssignmentsDefaultQuery = "ORDER BY repository ASC"
export const workflowConfigurationsDefaultQuery = "ORDER BY name ASC"

export function normalizeRepositoryAssignmentsSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: repositoryAssignmentsDefaultQuery,
    supportedViews: prLifecycleAdministrativeCollectionViews,
  })
}

export function normalizeWorkflowConfigurationsSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: workflowConfigurationsDefaultQuery,
    supportedViews: prLifecycleAdministrativeCollectionViews,
  })
}

export function normalizeWorkflowConfigurationEditorSearch(
  raw: object,
): CollectionRouteSearch & {
  flow: "review" | "implementation"
  gate?: string
} {
  const values = raw as Record<string, unknown>
  const collection = normalizeWorkflowConfigurationsSearch(raw)
  const flow = values.flow === "implementation" ? "implementation" : "review"
  const gate =
    typeof values.gate === "string" &&
    values.gate.length <= 128 &&
    /^pr(?:\.[a-z][a-z0-9_-]*){2,7}$/u.test(values.gate)
      ? values.gate
      : undefined
  return { ...collection, flow, ...(gate ? { gate } : {}) }
}
