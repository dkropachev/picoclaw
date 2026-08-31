import { createFileRoute, redirect, useLocation } from "@tanstack/react-router"

import { RepositoryReviewFindingsProcessingPage } from "@/components/repository-reviews/repository-review-findings-processing-page"
import {
  normalizeRepositoryReviewFindingsProcessingSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewParentSearchFromState,
  repositoryReviewSearchIsCanonical,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings-processing",
)({
  validateSearch: normalizeRepositoryReviewFindingsProcessingSearch,
  beforeLoad: ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    const canonical = normalizeRepositoryReviewFindingsProcessingSearch(raw)
    if (!repositoryReviewSearchIsCanonical(raw, canonical)) {
      throw redirect({
        to: "/repository-reviews/$id/findings-processing",
        params: { id: params.id },
        search: canonical,
        state: true,
        replace: true,
      })
    }
  },
  component: RepositoryReviewFindingsProcessingRoute,
})

function RepositoryReviewFindingsProcessingRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const navigationState = useLocation({ select: (current) => current.state })
  const parentSearch = repositoryReviewParentSearchFromState(
    navigationState,
    repositoryReviewDefaultQuery,
  )
  return (
    <RepositoryReviewFindingsProcessingPage
      automationID={id}
      search={search}
      onSearchChange={(next, replace) =>
        void navigate({ search: next, state: true, replace })
      }
      onBack={() =>
        void navigate({
          to: "/repository-reviews/$id",
          params: { id },
          search: parentSearch,
          state: true,
        })
      }
      onOpenSource={(sourceID) =>
        void navigate({
          to: "/repository-reviews/$id/findings-processing/$sourceId",
          params: { id, sourceId: sourceID },
          search,
          state: repositoryReviewParentNavigationState(
            parentSearch,
            repositoryReviewDefaultQuery,
          ),
        })
      }
    />
  )
}

function rawSearch(searchString: string): Record<string, unknown> {
  return Object.fromEntries(new URLSearchParams(searchString))
}
