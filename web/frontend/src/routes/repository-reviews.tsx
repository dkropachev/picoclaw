import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  normalizeRepositoryReviewRawFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewViews,
} from "@/components/repository-reviews/repository-review-route-state"
import { RepositoryReviewRunsPage } from "@/components/repository-reviews/repository-review-runs-page"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

function RepositoryReviewsRoutePage() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const locationSearch = location.search
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...routeSearch },
        {
          defaultQuery: repositoryReviewDefaultQuery,
          supportedViews: repositoryReviewViews,
        },
      ),
    [routeSearch],
  )
  const normalizedLocationSearch = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...locationSearch },
        {
          defaultQuery: repositoryReviewDefaultQuery,
          supportedViews: repositoryReviewViews,
        },
      ),
    [locationSearch],
  )
  useEffect(() => {
    if (location.pathname !== "/repository-reviews") return
    if (!collectionRouteSearchIsCanonical(normalizedLocationSearch, search)) {
      return
    }
    if (!collectionRouteSearchIsCanonical({ ...locationSearch }, search)) {
      void navigate({ search, replace: true })
    }
  }, [
    location.pathname,
    locationSearch,
    navigate,
    normalizedLocationSearch,
    search,
  ])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname !== "/repository-reviews") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )

  return (
    <RepositoryReviewRunsPage
      search={search}
      onSearchChange={changeSearch}
      onOpen={(review) =>
        void navigate({
          to: "/repository-reviews/$id",
          params: { id: review.id },
          search,
        })
      }
      onOpenRawFindings={(review) =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings",
          params: { id: review.id },
          search: normalizeRepositoryReviewRawFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewDefaultQuery,
          ),
        })
      }
    />
  )
}

export const Route = createFileRoute("/repository-reviews")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewDefaultQuery,
      supportedViews: repositoryReviewViews,
    }),
  component: RepositoryReviewsRoutePage,
})
