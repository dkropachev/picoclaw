import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewRawFindingPage } from "@/components/repository-reviews/repository-review-raw-finding-page"
import {
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/raw-findings_/$sourceId",
)({
  validateSearch: normalizeRepositoryReviewRawFindingsSearch,
  component: RepositoryReviewRawFindingRoute,
})

function RepositoryReviewRawFindingRoute() {
  const { id, sourceId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const findingsSearch = normalizeRepositoryReviewRunFindingsSearch({})
  return (
    <RepositoryReviewRawFindingPage
      automationID={id}
      sourceID={sourceId}
      onBack={() =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings",
          params: { id },
          search,
          state: true,
        })
      }
      onCanonicalSource={(canonicalSourceID) =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings/$sourceId",
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
          search: findingsSearch,
          state: true,
        })
      }
    />
  )
}
