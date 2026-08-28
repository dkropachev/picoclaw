import { createFileRoute, redirect } from "@tanstack/react-router"

import { RepositoryReviewLinkIssuePage } from "@/components/repository-reviews/repository-review-link-issue-page"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  repositoryReviewDefaultQuery,
  repositoryReviewParentNavigationState,
  repositoryReviewSearchHasLegacyPaging,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/findings_/$findingId_/link-issue",
)({
  validateSearch: normalizeRepositoryReviewRepositoryFindingsSearch,
  beforeLoad: ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    if (!repositoryReviewSearchHasLegacyPaging(raw)) return
    throw redirect({
      to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
      params: { id: params.id, findingId: params.findingId },
      search: normalizeRepositoryReviewRepositoryFindingsSearch(raw),
      state: true,
      replace: true,
    })
  },
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
      state: true,
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
          search: normalizeRepositoryReviewIssuesSearch({}),
          state: repositoryReviewParentNavigationState(
            {},
            repositoryReviewDefaultQuery,
          ),
        })
      }
    />
  )
}
