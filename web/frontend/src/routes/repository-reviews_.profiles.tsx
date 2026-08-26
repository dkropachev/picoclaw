import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  repositoryReviewProfileDefaultQuery,
  repositoryReviewProfileViews,
} from "@/components/repository-reviews/repository-review-profile-route-state"
import { RepositoryReviewProfilesPage } from "@/components/repository-reviews/repository-review-profiles-page"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

function RepositoryReviewProfilesRoutePage() {
  const location = useLocation({
    select: ({ pathname, search }) => ({ pathname, search }),
  })
  const routeSearch = Route.useSearch()
  const navigate = Route.useNavigate()
  const search = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...routeSearch },
        {
          defaultQuery: repositoryReviewProfileDefaultQuery,
          supportedViews: repositoryReviewProfileViews,
        },
      ),
    [routeSearch],
  )
  const normalizedLocationSearch = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...location.search },
        {
          defaultQuery: repositoryReviewProfileDefaultQuery,
          supportedViews: repositoryReviewProfileViews,
        },
      ),
    [location.search],
  )
  useEffect(() => {
    if (location.pathname !== "/repository-reviews/profiles") return
    if (!collectionRouteSearchIsCanonical(normalizedLocationSearch, search)) {
      return
    }
    if (!collectionRouteSearchIsCanonical({ ...location.search }, search)) {
      void navigate({ search, replace: true })
    }
  }, [
    location.pathname,
    location.search,
    navigate,
    normalizedLocationSearch,
    search,
  ])
  const changeSearch = useCallback(
    (next: CollectionRouteSearch, replace = false) => {
      if (location.pathname !== "/repository-reviews/profiles") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )
  return (
    <RepositoryReviewProfilesPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() =>
        void navigate({ to: "/repository-reviews/profiles/new", search })
      }
      onOpen={(profile) =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID",
          params: { profileID: profile.id },
          search,
        })
      }
      onEdit={(profile) =>
        void navigate({
          to: "/repository-reviews/profiles/$profileID/edit",
          params: { profileID: profile.id },
          search,
        })
      }
    />
  )
}

export const Route = createFileRoute("/repository-reviews_/profiles")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewProfileDefaultQuery,
      supportedViews: repositoryReviewProfileViews,
    }),
  component: RepositoryReviewProfilesRoutePage,
})
