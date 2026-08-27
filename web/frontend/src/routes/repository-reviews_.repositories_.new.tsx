import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRepositoryEditorPage } from "@/components/repository-reviews/repository-review-repositories-page"
import {
  repositoryReviewRepositoryDefaultQuery,
  repositoryReviewRepositoryViews,
} from "@/components/repository-reviews/repository-review-repositories-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute("/repository-reviews_/repositories_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewRepositoryDefaultQuery,
      supportedViews: repositoryReviewRepositoryViews,
    }),
  component: NewRepositoryReviewRepositoryRoute,
})

function NewRepositoryReviewRepositoryRoute() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewRepositoryEditorPage
      onBack={() =>
        void navigate({ to: "/repository-reviews/repositories", search })
      }
      onSaved={(id) =>
        void navigate({
          to: "/repository-reviews/repositories/$id",
          params: { id },
          search,
          replace: true,
        })
      }
    />
  )
}
