import { createFileRoute, redirect } from "@tanstack/react-router"

import {
  collectionSearchFromReviewSearch,
  normalizeRepositoryReviewRouteSearch,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/results")({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  beforeLoad: ({ search }) => {
    throw redirect({
      to: "/repository-reviews",
      search: collectionSearchFromReviewSearch(search),
      replace: true,
    })
  },
})
