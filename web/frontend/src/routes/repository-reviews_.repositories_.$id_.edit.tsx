import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRepositoryEditorPage } from "@/components/repository-reviews/repository-review-repositories-page"
import {
  repositoryReviewRepositoryDefaultQuery,
  repositoryReviewRepositoryViews,
} from "@/components/repository-reviews/repository-review-repositories-route-state"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/edit",
)({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewRepositoryDefaultQuery,
      supportedViews: repositoryReviewRepositoryViews,
    }),
  component: EditRepositoryReviewRepositoryRoute,
})

function EditRepositoryReviewRepositoryRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewRepositoryEditorPage
      automationID={id}
      onBack={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id",
          params: { id },
          search,
        })
      }
      onSaved={(savedID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id",
          params: { id: savedID },
          search,
          replace: true,
        })
      }
    />
  )
}
