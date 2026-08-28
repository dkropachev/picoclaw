import { createFileRoute, redirect, useLocation } from "@tanstack/react-router"

import { RepositoryReviewIssuesPage } from "@/components/repository-reviews/repository-review-issues-page"
import {
  normalizeRepositoryReviewIssuesSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentSearchFromState,
  repositoryReviewSearchIsCanonical,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/issues")({
  validateSearch: normalizeRepositoryReviewIssuesSearch,
  beforeLoad: ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    const canonical = normalizeRepositoryReviewIssuesSearch(raw)
    if (!repositoryReviewSearchIsCanonical(raw, canonical)) {
      throw redirect({
        to: "/repository-reviews/$id/issues",
        params: { id: params.id },
        search: canonical,
        state: true,
        replace: true,
      })
    }
  },
  component: RepositoryReviewIssuesRoute,
})

function RepositoryReviewIssuesRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const navigationState = useLocation({ select: (current) => current.state })
  const parentSearch = repositoryReviewParentSearchFromState(
    navigationState,
    repositoryReviewDefaultQuery,
  )
  return (
    <RepositoryReviewIssuesPage
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
      onOpenIssue={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search,
          state: true,
        })
      }
    />
  )
}

function rawSearch(searchString: string): Record<string, unknown> {
  return Object.fromEntries(new URLSearchParams(searchString))
}
