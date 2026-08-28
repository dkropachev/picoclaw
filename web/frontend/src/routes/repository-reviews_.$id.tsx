import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewDetailPage } from "@/components/repository-reviews/repository-review-detail-page"
import {
  collectionSearchFromReviewSearch,
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewViews,
} from "@/components/repository-reviews/repository-review-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/repository-reviews_/$id")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewDefaultQuery,
      supportedViews: repositoryReviewViews,
    }),
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
      onFindings={() =>
        void navigate({
          to: "/repository-reviews/$id/findings",
          params: { id },
          search: normalizeRepositoryReviewRunFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewDefaultQuery,
          ),
        })
      }
      onIssues={() =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search: normalizeRepositoryReviewIssuesSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewDefaultQuery,
          ),
        })
      }
    />
  )
}
