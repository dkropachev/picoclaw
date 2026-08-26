import { createFileRoute } from "@tanstack/react-router"

import {
  repositoryReviewProfileDefaultQuery,
  repositoryReviewProfileViews,
} from "@/components/repository-reviews/repository-review-profile-route-state"
import { RepositoryReviewProfileEditorPage } from "@/components/repository-reviews/repository-review-profiles-page"
import { normalizeCollectionRouteSearch } from "@/hooks/use-collection-route-state"

function RepositoryReviewProfileNewRoutePage() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <RepositoryReviewProfileEditorPage
      onBack={() =>
        void navigate({ to: "/repository-reviews/profiles", search })
      }
      onSaved={(profile) =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID",
          params: { profileID: profile.id },
          search,
        })
      }
    />
  )
}

export const Route = createFileRoute("/repository-reviews_/profiles_/new")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewProfileDefaultQuery,
      supportedViews: repositoryReviewProfileViews,
    }),
  component: RepositoryReviewProfileNewRoutePage,
})
