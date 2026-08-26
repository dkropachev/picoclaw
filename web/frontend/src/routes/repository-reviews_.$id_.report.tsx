import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewReportPage } from "@/components/repository-reviews/repository-review-report-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute("/repository-reviews_/$id_/report")({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  component: RepositoryReviewReportRoute,
})

function RepositoryReviewReportRoute() {
  const { id } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewReportPage
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
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
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
      onOpenThread={(threadID) =>
        void navigate({
          to: "/threads/open/$threadId",
          params: { threadId: threadID },
        })
      }
    />
  )
}
