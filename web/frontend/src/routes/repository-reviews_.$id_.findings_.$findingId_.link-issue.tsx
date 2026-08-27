import { createFileRoute, redirect } from "@tanstack/react-router"

import { resolveRepositoryFindingRouteID } from "@/components/repository-reviews/repository-review-repository-route"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  beforeLoad: async ({ params, search }) => {
    if (search.scope === "all") {
      const repositoryFindingID = await resolveRepositoryFindingRouteID(
        params.id,
        params.findingId,
      )
      if (repositoryFindingID) {
        throw redirect({
          to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
          params: { id: params.id, findingId: repositoryFindingID },
          search: { ...search, scope: "all" },
          replace: true,
        })
      }
    }
    throw redirect({
      to: "/repository-reviews/$id/findings/$findingId",
      params: { id: params.id, findingId: params.findingId },
      search: { ...search, scope: "current" },
      replace: true,
    })
  },
})
