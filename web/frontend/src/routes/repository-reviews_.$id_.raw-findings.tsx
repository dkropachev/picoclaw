import { createFileRoute, redirect, useLocation } from "@tanstack/react-router"

import { RepositoryReviewRawFindingsPage } from "@/components/repository-reviews/repository-review-raw-findings-page"
import {
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewParentSearchFromState,
  repositoryReviewSearchIsCanonical,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/raw-findings")({
  validateSearch: normalizeRepositoryReviewRawFindingsSearch,
  beforeLoad: ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    const canonical = normalizeRepositoryReviewRawFindingsSearch(raw)
    if (!repositoryReviewSearchIsCanonical(raw, canonical)) {
      throw redirect({
        to: "/repository-reviews/$id/raw-findings",
        params: { id: params.id },
        search: canonical,
        state: true,
        replace: true,
      })
    }
  },
  component: RepositoryReviewRawFindingsRoute,
})

function RepositoryReviewRawFindingsRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const navigationState = useLocation({ select: (current) => current.state })
  const parentSearch = repositoryReviewParentSearchFromState(
    navigationState,
    repositoryReviewDefaultQuery,
  )
  const findingsSearch = normalizeRepositoryReviewRunFindingsSearch({})
  return (
    <RepositoryReviewRawFindingsPage
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
      onOpenRawFinding={(sourceID) =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings/$sourceId",
          params: { id, sourceId: sourceID },
          search,
          state: true,
        })
      }
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search: findingsSearch,
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
