import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewIssueEditorPage } from "@/components/repository-reviews/repository-review-issue-editor-page"
import { normalizeRepositoryReviewIssuesSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/issues_/$draftId_/edit",
)({
  validateSearch: normalizeRepositoryReviewIssuesSearch,
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
