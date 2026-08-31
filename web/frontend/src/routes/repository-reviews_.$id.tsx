import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewDetailPage } from "@/components/repository-reviews/repository-review-detail-page"
import {
  collectionSearchFromReviewSearch,
  normalizeRepositoryReviewFindingsProcessingSearch,
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
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
      onRepositoryFindings={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings",
          params: { id },
          search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewDefaultQuery,
          ),
        })
      }
      onFindingsProcessing={() =>
        void navigate({
          to: "/repository-reviews/$id/findings-processing",
          params: { id },
          search: normalizeRepositoryReviewFindingsProcessingSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewDefaultQuery,
          ),
        })
      }
      onRawFindings={() =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings",
          params: { id },
          search: normalizeRepositoryReviewRawFindingsSearch({}),
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
