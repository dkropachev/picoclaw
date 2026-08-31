import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewFindingProcessingPage } from "@/components/repository-reviews/repository-review-finding-processing-page"
import {
  normalizeRepositoryReviewFindingsProcessingSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings-processing_/$sourceId",
)({
  validateSearch: normalizeRepositoryReviewFindingsProcessingSearch,
  component: RepositoryReviewFindingProcessingRoute,
})

function RepositoryReviewFindingProcessingRoute() {
  const { id, sourceId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <RepositoryReviewFindingProcessingPage
      automationID={id}
      sourceID={sourceId}
      onBack={() =>
        void navigate({
          to: "/repository-reviews/$id/findings-processing",
          params: { id },
          search,
          state: true,
        })
      }
      onCanonicalSource={(canonicalSourceID) =>
        void navigate({
          to: "/repository-reviews/$id/findings-processing/$sourceId",
          params: { id, sourceId: canonicalSourceID },
          search,
          state: true,
          replace: true,
        })
      }
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search: normalizeRepositoryReviewRunFindingsSearch({}),
          state: true,
        })
      }
      onOpenRepositoryFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
          state: true,
        })
      }
    />
  )
}
