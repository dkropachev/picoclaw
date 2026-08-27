import { createFileRoute } from "@tanstack/react-router"

import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/repositories_/$id_/findings_/$findingId",
)({
  validateSearch: (raw: Record<string, unknown>) => ({
    ...normalizeRepositoryReviewRouteSearch(raw),
    scope: "all" as const,
  }),
  component: RepositoryReviewRepositoryFindingRoute,
})

function RepositoryReviewRepositoryFindingRoute() {
  const { id, findingId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const openRepositoryFinding = (repositoryFindingID: string) =>
    navigate({
      to: "/repository-reviews/repositories/$id/findings/$findingId",
      params: { id, findingId: repositoryFindingID },
      search,
    })
  return (
    <RepositoryReviewFindingPage
      automationID={id}
      findingID={findingId}
      resourceKind="repository"
      onBack={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings",
          params: { id },
          search,
        })
      }
      onOpenIssue={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search,
        })
      }
      onLinkIssue={() =>
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId/link-issue",
          params: { id, findingId },
          search,
        })
      }
      onGenerated={(generationID) =>
        void navigate({
          to: "/repository-reviews/$id/issues",
          params: { id },
          search: { ...search, generation_id: generationID },
        })
      }
      onOpenThread={(threadID) =>
        void navigate({
          to: "/threads/open/$threadId",
          params: { threadId: threadID },
        })
      }
      onOpenRepositoryFinding={(repositoryFindingID) =>
        void openRepositoryFinding(repositoryFindingID)
      }
      onRepositoryFindingReplaced={(repositoryFindingID) =>
        void openRepositoryFinding(repositoryFindingID)
      }
    />
  )
}
