import { createFileRoute, redirect } from "@tanstack/react-router"

import { RepositoryReviewIssueEditorPage } from "@/components/repository-reviews/repository-review-issue-editor-page"
import {
  normalizeRepositoryReviewIssuesSearch,
  repositoryReviewSearchHasLegacyPaging,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/issues_/$draftId_/edit",
)({
  validateSearch: normalizeRepositoryReviewIssuesSearch,
  beforeLoad: ({ params, location }) => {
    const raw = Object.fromEntries(new URLSearchParams(location.searchStr))
    if (!repositoryReviewSearchHasLegacyPaging(raw)) return
    throw redirect({
      to: "/repository-reviews/$id/issues/$draftId/edit",
      params: { id: params.id, draftId: params.draftId },
      search: normalizeRepositoryReviewIssuesSearch(raw),
      state: true,
      replace: true,
    })
  },
  component: RepositoryReviewIssueEditorRoute,
})

function RepositoryReviewIssueEditorRoute() {
  const { id, draftId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const detailPath = (replace = false) =>
    navigate({
      to: "/repository-reviews/$id/issues/$draftId",
      params: { id, draftId },
      search,
      state: true,
      replace,
    })
  return (
    <RepositoryReviewIssueEditorPage
      automationID={id}
      draftID={draftId}
      onBack={() => void detailPath()}
      onSaved={() => void detailPath(true)}
    />
  )
}
