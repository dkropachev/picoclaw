import { createFileRoute } from "@tanstack/react-router"

import {
  repositoryReviewProfileDefaultQuery,
  repositoryReviewProfileViews,
} from "@/components/repository-reviews/repository-review-profile-route-state"
import { RepositoryReviewProfileEditorPage } from "@/components/repository-reviews/repository-review-profiles-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

function RepositoryReviewProfileEditRoutePage() {
  const { profileID } = Route.useParams()
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <RepositoryReviewProfileEditorPage
      profileID={profileID}
      onBack={() =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID",
          params: { profileID },
          search,
        })
      }
      onSaved={() =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID",
          params: { profileID },
          search,
        })
      }
    />
  )
}

export const Route = createFileRoute(
  "/repository-reviews_/profiles_/$profileID_/edit",
)({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewProfileDefaultQuery,
      supportedViews: repositoryReviewProfileViews,
    }),
  component: RepositoryReviewProfileEditRoutePage,
})
