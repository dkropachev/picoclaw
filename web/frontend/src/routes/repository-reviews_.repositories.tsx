import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import { RepositoryReviewRepositoriesPage } from "@/components/repository-reviews/repository-review-repositories-page"
import {
  repositoryReviewRepositoryDefaultQuery,
  repositoryReviewRepositoryViews,
} from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewRepositoryFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

function RepositoryReviewRepositoriesRoutePage() {
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
          defaultQuery: repositoryReviewRepositoryDefaultQuery,
          supportedViews: repositoryReviewRepositoryViews,
        },
      ),
    [routeSearch],
  )
  const normalizedLocationSearch = useMemo(
    () =>
      normalizeCollectionRouteSearch(
        { ...location.search },
        {
          defaultQuery: repositoryReviewRepositoryDefaultQuery,
          supportedViews: repositoryReviewRepositoryViews,
        },
      ),
    [location.search],
  )

  useEffect(() => {
    if (location.pathname !== "/repository-reviews/repositories") return
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
      if (location.pathname !== "/repository-reviews/repositories") return
      void navigate({ search: next, replace })
    },
    [location.pathname, navigate],
  )

  return (
    <RepositoryReviewRepositoriesPage
      search={search}
      onSearchChange={changeSearch}
      onAdd={() =>
        void navigate({
          to: "/repository-reviews/repositories/new",
          search,
        })
      }
      onOpen={(repository) =>
        void navigate({
          to: "/repository-reviews/repositories/$id",
          params: { id: repository.id },
          search,
        })
      }
      onEdit={(repository) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/edit",
          params: { id: repository.id },
          search,
        })
      }
      onOpenFindings={(repository) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings",
          params: { id: repository.id },
          search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
          state: repositoryReviewParentNavigationState(
            search,
            repositoryReviewRepositoryDefaultQuery,
          ),
        })
      }
    />
  )
}

export const Route = createFileRoute("/repository-reviews_/repositories")({
  validateSearch: (raw: Record<string, unknown>) =>
    normalizeCollectionRouteSearch(raw, {
      defaultQuery: repositoryReviewRepositoryDefaultQuery,
      supportedViews: repositoryReviewRepositoryViews,
    }),
  component: RepositoryReviewRepositoriesRoutePage,
})
