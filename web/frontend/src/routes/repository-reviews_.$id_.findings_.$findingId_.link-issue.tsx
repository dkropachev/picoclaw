import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewLinkIssuePage } from "@/components/repository-reviews/repository-review-link-issue-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  component: RepositoryReviewLinkIssueRoute,
})

function RepositoryReviewLinkIssueRoute() {
  const { id, findingId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const findingPath = () =>
    navigate({
      to: "/repository-reviews/$id/findings/$findingId",
      params: { id, findingId },
      search,
    })
  return (
    <RepositoryReviewLinkIssuePage
      automationID={id}
      findingID={findingId}
      onBack={() => void findingPath()}
      onLinked={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search,
        })
      }
    />
  )
}
