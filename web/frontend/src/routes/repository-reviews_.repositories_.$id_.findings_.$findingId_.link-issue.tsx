import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewLinkIssuePage } from "@/components/repository-reviews/repository-review-link-issue-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: (raw: Record<string, unknown>) => ({
    ...normalizeRepositoryReviewRouteSearch(raw),
    scope: "all" as const,
  }),
  component: RepositoryReviewRepositoryLinkIssueRoute,
})

function RepositoryReviewRepositoryLinkIssueRoute() {
  const { id, findingId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const findingPath = () =>
    navigate({
      to: "/repository-reviews/repositories/$id/findings/$findingId",
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
