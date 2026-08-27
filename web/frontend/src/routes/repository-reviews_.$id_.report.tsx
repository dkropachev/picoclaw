import { createFileRoute, redirect } from "@tanstack/react-router"

import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/report")({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to:
        search.scope === "all"
          ? "/repository-reviews/repositories/$id/findings"
          : "/repository-reviews/$id/findings",
      params: { id: params.id },
      search,
      replace: true,
    })
  },
})
