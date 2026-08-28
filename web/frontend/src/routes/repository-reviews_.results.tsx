import { createFileRoute, redirect } from "@tanstack/react-router"

import {
  collectionSearchFromReviewSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewViews,
} from "@/components/repository-reviews/repository-review-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/repository-reviews_/results")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewDefaultQuery,
      supportedViews: repositoryReviewViews,
    }),
  beforeLoad: ({ search }) => {
    throw redirect({
      to: "/repository-reviews",
      search: collectionSearchFromReviewSearch(search),
      replace: true,
    })
  },
})
