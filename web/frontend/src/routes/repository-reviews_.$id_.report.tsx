import { createFileRoute, redirect } from "@tanstack/react-router"

import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/report")({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  beforeLoad: ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    const repositoryScope = raw.scope === "all"
    throw redirect({
      to: repositoryScope
        ? "/repository-reviews/repositories/$id/findings"
        : "/repository-reviews/$id/findings",
      params: { id: params.id },
      search: repositoryScope
        ? normalizeRepositoryReviewRepositoryFindingsSearch(raw)
        : normalizeRepositoryReviewRunFindingsSearch(raw),
      state: repositoryScope
        ? repositoryReviewParentNavigationState(
            {},
            repositoryReviewRepositoryDefaultQuery,
          )
        : true,
      replace: true,
    })
  },
})
