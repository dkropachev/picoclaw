import { createFileRoute, redirect, useLocation } from "@tanstack/react-router"

import { RepositoryReviewFindingsPage } from "@/components/repository-reviews/repository-review-findings-page"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewParentSearchFromState,
  repositoryReviewSearchIsCanonical,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/findings")({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  beforeLoad: ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    if (raw.scope === "all") {
      throw redirect({
        to: "/repository-reviews/repositories/$id/findings",
        params: { id: params.id },
        search: normalizeRepositoryReviewRepositoryFindingsSearch(raw),
        state: repositoryReviewParentNavigationState(
          {},
          repositoryReviewRepositoryDefaultQuery,
        ),
        replace: true,
      })
    }
    const canonical = normalizeRepositoryReviewRunFindingsSearch(raw)
    if (!repositoryReviewSearchIsCanonical(raw, canonical)) {
      throw redirect({
        to: "/repository-reviews/$id/findings",
        params: { id: params.id },
        search: canonical,
        state: true,
        replace: true,
      })
    }
  },
  component: RepositoryReviewFindingsRoute,
})

function RepositoryReviewFindingsRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const navigationState = useLocation({ select: (current) => current.state })
  const parentSearch = repositoryReviewParentSearchFromState(
    navigationState,
    repositoryReviewDefaultQuery,
  )
  const repositorySearch = normalizeRepositoryReviewRepositoryFindingsSearch({})
  return (
    <RepositoryReviewFindingsPage
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
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search,
          state: true,
        })
      }
      onOpenRawFindings={() =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings",
          params: { id },
          search: normalizeRepositoryReviewRawFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            parentSearch,
            repositoryReviewDefaultQuery,
          ),
        })
      }
      onOpenRepositoryFindings={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings",
          params: { id },
          search: repositorySearch,
          state: repositoryReviewParentNavigationState(
            {},
            repositoryReviewRepositoryDefaultQuery,
          ),
        })
      }
      onOpenRepositoryFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search: repositorySearch,
          state: repositoryReviewParentNavigationState(
            {},
            repositoryReviewRepositoryDefaultQuery,
          ),
        })
      }
      onOpenThread={(threadID) =>
        void navigate({
          to: "/threads/open/$threadId",
          params: { threadId: threadID },
        })
      }
    />
  )
}

function rawSearch(searchString: string): Record<string, unknown> {
  return Object.fromEntries(new URLSearchParams(searchString))
}
