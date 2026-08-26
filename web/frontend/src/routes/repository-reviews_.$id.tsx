import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewDetailPage } from "@/components/repository-reviews/repository-review-detail-page"
import {
  collectionSearchFromReviewSearch,
  normalizeRepositoryReviewRouteSearch,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id")({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  component: RepositoryReviewDetailRoute,
})

function RepositoryReviewDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewDetailPage
      id={id}
      onBack={() =>
        void navigate({
          to: "/repository-reviews",
          search: collectionSearchFromReviewSearch(search),
        })
      }
      onReport={() =>
        void navigate({
          to: "/repository-reviews/$id/report",
          params: { id },
          search,
        })
      }
      onIssues={() =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search,
        })
      }
    />
  )
}
