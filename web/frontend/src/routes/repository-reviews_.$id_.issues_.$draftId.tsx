import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewIssuePage } from "@/components/repository-reviews/repository-review-issue-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/issues_/$draftId",
)({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  component: RepositoryReviewIssueRoute,
})

function RepositoryReviewIssueRoute() {
  const { id, draftId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const issuesPath = () =>
    navigate({
      to: "/repository-reviews/$id/issues",
      params: { id },
      search,
    })
  const manageLink = (findingID: string) => {
    if (findingID.startsWith("rrf_")) {
      return navigate({
        to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
        params: { id, findingId: findingID },
        search: { ...search, scope: "all" },
      })
    }
    return navigate({
      to: "/repository-reviews/$id/findings/$findingId/link-issue",
      params: { id, findingId: findingID },
      search: { ...search, scope: "current" },
    })
  }
  return (
    <RepositoryReviewIssuePage
      automationID={id}
      draftID={draftId}
      onBack={() => void issuesPath()}
      onDeleted={() => void issuesPath()}
      onOpenFinding={(findingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: findingID },
          search: { ...search, scope: "current" },
        })
      }
      onManageLink={(findingID) => void manageLink(findingID)}
    />
  )
}
