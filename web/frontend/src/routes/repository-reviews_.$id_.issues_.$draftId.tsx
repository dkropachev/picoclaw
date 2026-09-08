import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewIssuePage } from "@/components/repository-reviews/repository-review-issue-page"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/issues_/$draftId",
)({
  validateSearch: normalizeRepositoryReviewIssuesSearch,
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
      state: true,
    })
  const manageLink = (repositoryFindingID: string) =>
    navigate({
      to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
      params: { id, findingId: repositoryFindingID },
      search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
      state: repositoryReviewParentNavigationState(
        {},
        repositoryReviewRepositoryDefaultQuery,
      ),
    })
  return (
    <RepositoryReviewIssuePage
      automationID={id}
      draftID={draftId}
      onBack={() => void issuesPath()}
      onDeleted={() => void issuesPath()}
      onEdit={() =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId/edit",
          params: { id, draftId },
          search,
          state: true,
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
      onManageLink={(findingID) => void manageLink(findingID)}
    />
  )
}
