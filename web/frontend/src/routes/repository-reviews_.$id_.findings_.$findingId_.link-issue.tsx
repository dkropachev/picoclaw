import { createFileRoute, redirect } from "@tanstack/react-router"

import { getRepositoryReviewRawSource } from "@/api/repository-reviews"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import { resolveRepositoryFindingRouteID } from "@/components/repository-reviews/repository-review-repository-route"
import {
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
  repositoryReviewRunFindingsDefaultQuery,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  beforeLoad: async ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    if (params.findingId.startsWith("rfn_")) {
      let sourceID = params.findingId
      try {
        sourceID = (
          await getRepositoryReviewRawSource(params.id, params.findingId)
        ).source.id
      } catch {
        // Keep the alias on the canonical raw detail route for normal error UI.
      }
      throw redirect({
        to: "/repository-reviews/$id/raw-findings/$sourceId",
        params: { id: params.id, sourceId: sourceID },
        search: normalizeRepositoryReviewRawFindingsSearch(
          raw.q === repositoryReviewRunFindingsDefaultQuery ? {} : raw,
        ),
        state: true,
        replace: true,
      })
    }
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
