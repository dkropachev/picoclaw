import { createFileRoute } from "@tanstack/react-router"

import {
  repositoryReviewProfileDefaultQuery,
  repositoryReviewProfileViews,
} from "@/components/repository-reviews/repository-review-profile-route-state"
import { RepositoryReviewProfileDetailPage } from "@/components/repository-reviews/repository-review-profiles-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

function RepositoryReviewProfileDetailRoutePage() {
  const { profileID } = Route.useParams()
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <RepositoryReviewProfileDetailPage
      profileID={profileID}
      onBack={() =>
        void navigate({ to: "/repository-reviews/profiles", search })
      }
      onEdit={() =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID/edit",
          params: { profileID },
          search,
        })
      }
      onDeleted={() =>
        void navigate({ to: "/repository-reviews/profiles", search })
      }
    />
  )
}

export const Route = createFileRoute(
  "/repository-reviews_/profiles_/$profileID",
)({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewProfileDefaultQuery,
      supportedViews: repositoryReviewProfileViews,
    }),
  component: RepositoryReviewProfileDetailRoutePage,
})
