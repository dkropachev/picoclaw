import { createFileRoute, redirect } from "@tanstack/react-router"

import { getRepositoryReviewRawSource } from "@/api/repository-reviews"
import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { repositoryReviewRepositoryDefaultQuery } from "@/components/repository-reviews/repository-review-repositories-route-state"
import { resolveRepositoryFindingRouteID } from "@/components/repository-reviews/repository-review-repository-route"
import {
  normalizeRepositoryReviewIssuesSearch,
  normalizeRepositoryReviewRawFindingsSearch,
  normalizeRepositoryReviewRepositoryFindingsSearch,
  normalizeRepositoryReviewRunFindingsSearch,
  repositoryReviewParentNavigationState,
  repositoryReviewRunFindingsDefaultQuery,
  repositoryReviewSearchHasLegacyPaging,
} from "@/components/repository-reviews/repository-review-route-state"

export const Route = createFileRoute(
  "/repository-reviews_/$id_/findings_/$findingId",
)({
  validateSearch: normalizeRepositoryReviewRunFindingsSearch,
  beforeLoad: async ({ params, location }) => {
    const raw = rawSearch(location.searchStr)
    if (params.findingId.startsWith("rfn_")) {
      let sourceID = params.findingId
      try {
        sourceID = (
          await getRepositoryReviewRawSource(params.id, params.findingId)
        ).source.id
      } catch {
        // Preserve the legacy alias on the canonical raw route so its detail
        // surface can report a normal not-found or recovery error.
      }
      throw redirect({
        to: "/repository-reviews/$id/raw-findings/$sourceId",
        params: { id: params.id, sourceId: sourceID },
        search: normalizeRepositoryReviewRawFindingsSearch(
          raw.q === repositoryReviewRunFindingsDefaultQuery ? {} : raw,
        ),
        state: true,
        replace: true,
      })
    }
    if (raw.scope === "all") {
      const repositoryFindingID = await resolveRepositoryFindingRouteID(
        params.id,
        params.findingId,
      )
      if (!repositoryFindingID) {
        throw redirect({
          to: "/repository-reviews/$id/findings/$findingId",
          params: { id: params.id, findingId: params.findingId },
          search: normalizeRepositoryReviewRunFindingsSearch(raw),
          state: true,
          replace: true,
        })
      }
      throw redirect({
        to: "/repository-reviews/repositories/$id/findings/$findingId",
        params: { id: params.id, findingId: repositoryFindingID },
        search: normalizeRepositoryReviewRepositoryFindingsSearch(raw),
        state: repositoryReviewParentNavigationState(
          {},
          repositoryReviewRepositoryDefaultQuery,
        ),
        replace: true,
      })
    }
    if (repositoryReviewSearchHasLegacyPaging(raw)) {
      throw redirect({
        to: "/repository-reviews/$id/findings/$findingId",
        params: { id: params.id, findingId: params.findingId },
        search: normalizeRepositoryReviewRunFindingsSearch(raw),
        state: true,
        replace: true,
      })
    }
  },
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
      onLinkIssue={(targetFindingID) =>
        void navigate({
          to: "/repository-reviews/$id/findings/$findingId/link-issue",
          params: { id, findingId: targetFindingID || findingId },
          search,
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

function rawSearch(searchString: string): Record<string, unknown> {
  return Object.fromEntries(new URLSearchParams(searchString))
}
