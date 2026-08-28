import { createFileRoute, redirect } from "@tanstack/react-router"

import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import { resolveRepositoryFindingRouteID } from "@/components/repository-reviews/repository-review-repository-route"
import {
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  beforeLoad: async ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    if (raw.scope === "all") {
      const repositoryFindingID = await resolveRepositoryFindingRouteID(
        params.id,
        params.findingId,
      )
      if (repositoryFindingID) {
        throw redirect({
          to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
          params: { id: params.id, findingId: repositoryFindingID },
          search: normalizeRepositoryReviewRepositoryFindingsSearch(raw),
          state: repositoryReviewParentNavigationState(
            {},
            repositoryReviewRepositoryDefaultQuery,
          ),
          replace: true,
        })
      }
    }
    throw redirect({
      to: "/repository-reviews/$id/findings/$findingId",
      params: { id: params.id, findingId: params.findingId },
      search: normalizeRepositoryReviewRunFindingsSearch(raw),
      state: true,
      replace: true,
    })
  },
})
