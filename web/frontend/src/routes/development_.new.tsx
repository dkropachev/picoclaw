import { createFileRoute } from "@tanstack/react-router"

import { DevelopmentIntakePage } from "@/components/development-workspaces/development-intake-page"
import {
  developmentWorkspacesDefaultQuery,
  normalizeDevelopmentWorkspacesSearch,
} from "@/components/development-workspaces/development-workspace-collection-route-state"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

function NewDevelopmentRoutePage() {
  const navigate = Route.useNavigate()
  const search = Route.useSearch()
  return (
    <DevelopmentIntakePage
      initialIssueURL={search.issue}
      onBack={() =>
        void navigate({
          to: "/development",
          search: collectionSearch(search),
        })
      }
      onCreated={(workspaceID) =>
        void navigate({
          to: "/development/$workspaceID",
          params: { workspaceID },
          search: { ...collectionSearch(search), tab: "overview" },
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/new")({
  validateSearch: normalizeNewDevelopmentSearch,
  component: NewDevelopmentRoutePage,
})

export function normalizeNewDevelopmentSearch(raw: Record<string, unknown>): {
  q?: string
  view?: CollectionRouteSearch["view"]
  issue?: string
} {
  const collection = normalizeDevelopmentWorkspacesSearch(raw)
  if (typeof raw.issue !== "string" || raw.issue.length > 4096) {
    return collection
  }
  const issue = raw.issue.trim()
  try {
    const parsed = new URL(issue)
    if (
      parsed.protocol !== "https:" ||
      parsed.username !== "" ||
      parsed.password !== "" ||
      !/\/issues\/[1-9][0-9]*\/?$/.test(parsed.pathname)
    ) {
      return collection
    }
    return { ...collection, issue }
  } catch {
    return collection
  }
}

function collectionSearch(search: {
  q?: string
  view?: CollectionRouteSearch["view"]
}): CollectionRouteSearch {
  return {
    q: search.q ?? developmentWorkspacesDefaultQuery,
    ...(search.view ? { view: search.view } : {}),
  }
}
