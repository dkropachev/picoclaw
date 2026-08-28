import { createFileRoute, redirect } from "@tanstack/react-router"

import {
  developmentWorkspacesDefaultQuery,
  normalizeDevelopmentWorkspacesSearch,
} from "@/components/development-workspaces/development-workspace-collection-route-state"
import {
  type DevelopmentAttentionPanel,
  isDevelopmentAttentionPanel,
} from "@/components/development-workspaces/development-workspace-navigation"
import {
  DevelopmentWorkspacePage,
  type DevelopmentWorkspaceTab,
} from "@/components/development-workspaces/development-workspace-page"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

export interface DevelopmentWorkspaceSearch {
  q?: string
  view?: CollectionRouteSearch["view"]
  tab: DevelopmentWorkspaceTab
  path?: string
  revision?: string
  panel?: DevelopmentAttentionPanel
  entity?: string
}

const workspaceIDPattern = /^devw_[0-9a-f]{32}$/
const tabs = new Set<DevelopmentWorkspaceTab>([
  "overview",
  "changes",
  "files",
  "activity",
])
export function normalizeDevelopmentWorkspaceSearch(
  raw: Record<string, unknown>,
): DevelopmentWorkspaceSearch {
  const collection = normalizeDevelopmentWorkspacesSearch(raw)
  const tab =
    typeof raw.tab === "string" && tabs.has(raw.tab as DevelopmentWorkspaceTab)
      ? (raw.tab as DevelopmentWorkspaceTab)
      : "overview"
  const path = safeRepositoryPath(raw.path)
  const revision = safeRevision(raw.revision)
  const codeTab = tab === "changes" || tab === "files"
  const panel =
    tab === "overview" && isDevelopmentAttentionPanel(raw.panel)
      ? raw.panel
      : undefined
  const entity = panel ? safeEntityID(raw.entity) : undefined
  return {
    ...collection,
    tab,
    ...(codeTab && path ? { path } : {}),
    ...(codeTab && path && revision ? { revision } : {}),
    ...(panel ? { panel } : {}),
    ...(panel && entity ? { entity } : {}),
  }
}

function DevelopmentWorkspaceRoutePage() {
  const { workspaceID } = Route.useParams()
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <DevelopmentWorkspacePage
      workspaceID={workspaceID}
      tab={search.tab}
      selectedPath={search.path}
      selectedRevision={search.revision}
      attentionPanel={search.panel}
      attentionEntityID={search.entity}
      onBack={() =>
        void navigate({
          to: "/development",
          search: collectionSearch(search),
        })
      }
      onTabChange={(tab) =>
        void navigate({
          search: { ...collectionSearch(search), tab },
          replace: false,
        })
      }
      onPathChange={(path, revision) =>
        void navigate({
          search: {
            ...collectionSearch(search),
            tab: search.tab === "changes" ? "changes" : "files",
            ...(path ? { path } : {}),
            ...(path && revision ? { revision } : {}),
          },
          replace: true,
        })
      }
    />
  )
}

export const Route = createFileRoute("/development_/$workspaceID")({
  validateSearch: normalizeDevelopmentWorkspaceSearch,
  beforeLoad: ({ params }) => {
    if (!workspaceIDPattern.test(params.workspaceID)) {
      throw redirect({
        to: "/development",
        search: normalizeDevelopmentWorkspacesSearch({}),
        replace: true,
      })
    }
  },
  component: DevelopmentWorkspaceRoutePage,
})

function collectionSearch(search: {
  q?: string
  view?: CollectionRouteSearch["view"]
}): CollectionRouteSearch {
  return {
    q: search.q ?? developmentWorkspacesDefaultQuery,
    ...(search.view ? { view: search.view } : {}),
  }
}

function safeRepositoryPath(value: unknown): string | undefined {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > 4096 ||
    value.startsWith("/") ||
    value.includes("\\") ||
    value.includes("\0") ||
    value
      .split("/")
      .some((part) => part === "" || part === "." || part === "..")
  ) {
    return undefined
  }
  return value
}

function safeRevision(value: unknown): string | undefined {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > 512 ||
    [...value].some((character) => {
      const code = character.charCodeAt(0)
      return code <= 31 || code === 127
    })
  ) {
    return undefined
  }
  return value
}

function safeEntityID(value: unknown): string | undefined {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > 512 ||
    !/^[a-zA-Z0-9._:-]+$/.test(value)
  ) {
    return undefined
  }
  return value
}
