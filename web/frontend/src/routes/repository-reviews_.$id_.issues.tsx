import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewIssuesPage } from "@/components/repository-reviews/repository-review-issues-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/issues")({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  component: RepositoryReviewIssuesRoute,
})

function RepositoryReviewIssuesRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewIssuesPage
      automationID={id}
      search={search}
      onSearchChange={(next, replace) =>
        void navigate({ search: next, replace })
      }
      onBack={() =>
        void navigate({
          to: "/repository-reviews/$id",
          params: { id },
          search,
        })
      }
      onOpenIssue={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search,
        })
      }
    />
  )
}
