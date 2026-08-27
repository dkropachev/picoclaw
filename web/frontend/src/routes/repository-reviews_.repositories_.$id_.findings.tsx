import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRepositoryFindingsPage } from "@/components/repository-reviews/repository-review-repository-findings-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/findings",
)({
  validateSearch: (raw: Record<string, unknown>) => ({
    ...normalizeRepositoryReviewRouteSearch(raw),
    scope: "all" as const,
  }),
  component: RepositoryReviewRepositoryFindingsRoute,
})

function RepositoryReviewRepositoryFindingsRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewRepositoryFindingsPage
      automationID={id}
      search={search}
      onSearchChange={(next, replace) =>
        void navigate({ search: { ...next, scope: "all" }, replace })
      }
      onBack={() =>
        void navigate({
          to: "/repository-reviews/repositories",
          search: {
            q: search.q,
            ...(search.view ? { view: search.view } : {}),
          },
        })
      }
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search,
        })
      }
      onGenerated={(generationID) =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search: { ...search, generation_id: generationID },
        })
      }
    />
  )
}
