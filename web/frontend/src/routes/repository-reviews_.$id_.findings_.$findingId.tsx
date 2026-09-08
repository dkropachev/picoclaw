import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId",
)({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  component: RepositoryReviewFindingRoute,
})

function RepositoryReviewFindingRoute() {
  const { id, findingId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const repositorySearch = normalizeRepositoryReviewRepositoryFindingsSearch({})
  const issuesSearch = normalizeRepositoryReviewIssuesSearch({})
  return (
    <RepositoryReviewFindingPage
      automationID={id}
      findingID={findingId}
      resourceKind="run"
      onBack={() =>
        void navigate({
          to: "/repository-reviews/$id/findings",
          params: { id },
          search,
          state: true,
        })
      }
      onOpenIssue={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search: issuesSearch,
          state: true,
        })
      }
      onGenerated={(generationID) =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search: { ...issuesSearch, generation_id: generationID },
          state: true,
        })
      }
      onOpenThread={(threadID) =>
        void navigate({
          to: "/threads/open/$threadId",
          params: { threadId: threadID },
        })
      }
      onOpenRepositoryFinding={(repositoryFindingID) =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: repositoryFindingID },
          search: repositorySearch,
          state: repositoryReviewParentNavigationState(
            {},
            repositoryReviewRepositoryDefaultQuery,
          ),
        })
      }
      onOpenRawFinding={(sourceID) =>
        void navigate({
          to: "/repository-reviews/$id/raw-findings/$sourceId",
          params: { id, sourceId: sourceID },
          search: normalizeRepositoryReviewRawFindingsSearch({}),
          state: true,
        })
      }
      onRepositoryFindingReplaced={(repositoryFindingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: repositoryFindingID },
          search,
          state: true,
          replace: true,
        })
      }
    />
  )
}
