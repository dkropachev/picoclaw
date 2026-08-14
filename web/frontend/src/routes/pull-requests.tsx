import { createFileRoute, useLocation } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo } from "react"

import {
  type PRLifecycleDecisionPoint,
  prLifecycleKnownDecisionPoints,
} from "@/api/pr-lifecycle-gate-profiles"
import { PRLifecycleGateProfilesPage } from "@/components/pr-workspaces/pr-lifecycle-gate-profiles-page"
import { PRWorkspacePage } from "@/components/pr-workspaces/pr-workspace-page"
import { PRWorkspacePortfolioPage } from "@/components/pr-workspaces/pr-workspace-portfolio-page"

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export interface PullRequestsRouteSearch {
  workspace?: string
  view?: "gate-profiles"
  gate?: PRLifecycleDecisionPoint
}

export function normalizePullRequestsSearch(
  raw: Record<string, unknown>,
): PullRequestsRouteSearch {
  if (Object.hasOwn(raw, "view")) {
    if (raw.view !== "gate-profiles") return {}
    const gate =
      typeof raw.gate === "string" &&
      (prLifecycleKnownDecisionPoints as readonly string[]).includes(raw.gate)
        ? (raw.gate as PRLifecycleDecisionPoint)
        : undefined
    return gate ? { view: "gate-profiles", gate } : { view: "gate-profiles" }
  }
  return typeof raw.workspace === "string" &&
    workspaceIDPattern.test(raw.workspace)
    ? { workspace: raw.workspace }
    : {}
}

export function pullRequestsSearchIsCanonical(
  raw: Record<string, unknown>,
  normalized: PullRequestsRouteSearch,
): boolean {
  const rawKeys = Object.keys(raw)
  const normalizedKeys = Object.keys(normalized) as Array<
    keyof PullRequestsRouteSearch
  >
  return (
    rawKeys.length === normalizedKeys.length &&
    normalizedKeys.every((key) => raw[key] === normalized[key])
  )
}

function PullRequestsRoutePage() {
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const navigate = Route.useNavigate()
  const search = useMemo(
    () => normalizePullRequestsSearch({ ...locationSearch }),
    [locationSearch],
  )

  useEffect(() => {
    if (
      locationHash !== "" ||
      !pullRequestsSearchIsCanonical({ ...locationSearch }, search)
    ) {
      void navigate({ search, hash: "", replace: true })
    }
  }, [locationHash, locationSearch, navigate, search])

  const changeSearch = useCallback(
    (next: PullRequestsRouteSearch) => {
      void navigate({ search: next, hash: "" })
    },
    [navigate],
  )

  if (search.view === "gate-profiles") {
    return (
      <PRLifecycleGateProfilesPage
        onBack={() => changeSearch({})}
        initialDecisionPoint={search.gate}
        onDecisionPointChange={(gate) =>
          changeSearch({ view: "gate-profiles", gate })
        }
      />
    )
  }
  if (search.workspace) {
    return (
      <PRWorkspacePage
        workspaceID={search.workspace}
        onBack={() => changeSearch({})}
        onOpenGateProfiles={() => changeSearch({ view: "gate-profiles" })}
      />
    )
  }
  return (
    <PRWorkspacePortfolioPage
      onOpenWorkspace={(workspace) => changeSearch({ workspace })}
      onOpenGateProfiles={() => changeSearch({ view: "gate-profiles" })}
    />
  )
}

export const Route = createFileRoute("/pull-requests")({
  validateSearch: normalizePullRequestsSearch,
  component: PullRequestsRoutePage,
})
