import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRepositoryDetailPage } from "@/components/repository-reviews/repository-review-repositories-page"
import {
  repositoryReviewRepositoryDefaultQuery,
  repositoryReviewRepositoryViews,
} from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewRepositoryFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/repository-reviews_/repositories_/$id")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewRepositoryDefaultQuery,
      supportedViews: repositoryReviewRepositoryViews,
    }),
  component: RepositoryReviewRepositoryDetailRoute,
})

function RepositoryReviewRepositoryDetailRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewRepositoryDetailPage
      automationID={id}
      onBack={() =>
        void navigate({ to: "/repository-reviews/repositories", search })
      }
      onEdit={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/edit",
          params: { id },
          search,
        })
      }
      onFindings={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings",
          params: { id },
          search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewRepositoryDefaultQuery,
          ),
        })
      }
      onDeleted={() =>
        void navigate({
          to: "/repository-reviews/repositories",
          search,
          replace: true,
        })
      }
    />
  )
}
