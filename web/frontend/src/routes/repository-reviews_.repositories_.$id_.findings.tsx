import { createFileRoute, redirect, useLocation } from "@tanstack/react-router"

import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import { RepositoryReviewRepositoryFindingsPage } from "@/components/repository-reviews/repository-review-repository-findings-page"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewParentSearchFromState,
  repositoryReviewSearchIsCanonical,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/findings",
)({
  validateSearch: normalizeRepositoryReviewRepositoryFindingsSearch,
  beforeLoad: ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    const canonical = normalizeRepositoryReviewRepositoryFindingsSearch(raw)
    if (!repositoryReviewSearchIsCanonical(raw, canonical)) {
      throw redirect({
        to: "/repository-reviews/repositories/$id/findings",
        params: { id: params.id },
        search: canonical,
        state: true,
        replace: true,
      })
    }
  },
  component: RepositoryReviewRepositoryFindingsRoute,
})

function RepositoryReviewRepositoryFindingsRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const navigationState = useLocation({ select: (current) => current.state })
  const parentSearch = repositoryReviewParentSearchFromState(
    navigationState,
    repositoryReviewRepositoryDefaultQuery,
  )
  return (
    <RepositoryReviewRepositoryFindingsPage
      automationID={id}
      search={search}
      onSearchChange={(next, replace) =>
        void navigate({ search: next, state: true, replace })
      }
      onBack={() =>
        void navigate({
          to: "/repository-reviews/repositories",
          search: parentSearch,
          state: true,
        })
      }
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search,
          state: true,
        })
      }
      onOpenIncompleteFindings={() =>
        void navigate({
          to: "/repository-reviews/$id/findings",
          params: { id },
          search: normalizeRepositoryReviewRunFindingsSearch({
            q: "run_status IN (pending, processing, failed) ORDER BY updated DESC",
          }),
          state: true,
        })
      }
      onGenerated={(generationID) =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search: {
            ...normalizeRepositoryReviewIssuesSearch({}),
            generation_id: generationID,
          },
          state: repositoryReviewParentNavigationState(
            {},
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
