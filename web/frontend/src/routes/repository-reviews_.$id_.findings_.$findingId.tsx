import { createFileRoute, redirect } from "@tanstack/react-router"

import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { resolveRepositoryFindingRouteID } from "@/components/repository-reviews/repository-review-repository-route"
import { normalizeRepositoryReviewRouteSearch } from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId",
)({
  validateSearch: normalizeRepositoryReviewRouteSearch,
  beforeLoad: async ({ params, search }) => {
    if (search.scope !== "all") return
    const repositoryFindingID = await resolveRepositoryFindingRouteID(
      params.id,
      params.findingId,
    )
    if (!repositoryFindingID) {
      throw redirect({
        to: "/repository-reviews/$id/findings/$findingId",
        params: { id: params.id, findingId: params.findingId },
        search: { ...search, scope: "current" },
        replace: true,
      })
    }
    throw redirect({
      to: "/repository-reviews/repositories/$id/findings/$findingId",
      params: { id: params.id, findingId: repositoryFindingID },
      search: { ...search, scope: "all" },
      replace: true,
    })
  },
  component: RepositoryReviewFindingRoute,
})

function RepositoryReviewFindingRoute() {
  const { id, findingId } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
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
        })
      }
      onOpenIssue={(draftID) =>
        void navigate({
          to: "/repository-reviews/$id/issues/$draftId",
          params: { id, draftId: draftID },
          search,
        })
      }
      onLinkIssue={(targetFindingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId/link-issue",
          params: { id, findingId: targetFindingID || findingId },
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
        void navigate({
          to: "/repository-reviews/repositories/$id/findings/$findingId",
          params: { id, findingId: repositoryFindingID },
          search: { ...search, scope: "all" },
        })
      }
      onRepositoryFindingReplaced={(repositoryFindingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id, findingId: repositoryFindingID },
          search,
          replace: true,
        })
      }
    />
  )
}
