import { createFileRoute, redirect } from "@tanstack/react-router"

import { RepositoryReviewIssuePage } from "@/components/repository-reviews/repository-review-issue-page"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
  repositoryReviewSearchHasLegacyPaging,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/issues_/$draftId",
)({
  validateSearch: normalizeRepositoryReviewIssuesSearch,
  beforeLoad: ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    if (!repositoryReviewSearchHasLegacyPaging(raw)) return
    throw redirect({
      to: "/repository-reviews/$id/issues/$draftId",
      params: { id: params.id, draftId: params.draftId },
      search: normalizeRepositoryReviewIssuesSearch(raw),
      state: true,
      replace: true,
    })
  },
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
  const manageLink = (findingID: string) => {
    if (findingID.startsWith("rrf_")) {
      return navigate({
        to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
        params: { id, findingId: findingID },
        search: normalizeRepositoryReviewRepositoryFindingsSearch({}),
        state: repositoryReviewParentNavigationState(
          {},
          repositoryReviewRepositoryDefaultQuery,
        ),
      })
    }
    return navigate({
      to: "/repository-reviews/$id/findings/$findingId/link-issue",
      params: { id, findingId: findingID },
      search: normalizeRepositoryReviewRunFindingsSearch({}),
      state: true,
    })
  }
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
