import {
  Outlet,
  createFileRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import { isPRLifecycleWorkflowConfigurationID } from "@/api/pr-lifecycle-workflow-configurations"
import { PRLifecycleWorkflowConfigurationsPage } from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRWorkflowConfigurationsSearch {
  from?: string
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRWorkflowConfigurationsSearch(
  raw: Record<string, unknown>,
): PRWorkflowConfigurationsSearch {
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  return {
    ...(from ? { from } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestWorkflowConfigurationsRoutePage() {
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRWorkflowConfigurationsSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== "/pull-requests/workflow-configurations" ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/workflow-configurations",
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, search])

  if (pathname !== "/pull-requests/workflow-configurations") return <Outlet />
  if (!canonical) return null

  return (
    <PRLifecycleWorkflowConfigurationsPage
      page="configs"
      discardOpen={search.dialog === "discard"}
      onBack={() => {
        const fallback = () => {
          if (search.from) {
            void navigate({
              to: "/pull-requests/$workspaceID",
              params: { workspaceID: search.from },
              replace: true,
            })
            return
          }
          void navigate({ to: "/pull-requests", replace: true })
        }
        if (
          locationState.prWorkKey &&
          goToMarkedPRHistory(
            router.history,
            locationState,
            locationState.prWorkIndex,
            locationState.prWorkKey,
            fallback,
          )
        ) {
          return
        }
        fallback()
      }}
      onDiscardOpenChange={(open) =>
        navigate({
          to: "/pull-requests/workflow-configurations",
          search: {
            ...(search.from ? { from: search.from } : {}),
            ...(open ? { dialog: "discard" as const } : {}),
          },
          replace: true,
          state: (previous) =>
            updatePRNavigationState(previous, () => ({
              prOverlay: open ? "discard" : undefined,
            })),
        })
      }
      onConfigChange={(configID) => {
        if (!configID || !isPRLifecycleWorkflowConfigurationID(configID)) return
        void navigate({
          to: "/pull-requests/workflow-configurations/$configurationID",
          params: { configurationID: configID },
          search: {
            flow: "review",
            ...(search.from ? { from: search.from } : {}),
          },
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "workflow-configurations",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
            })),
        })
      }}
    />
  )
}

export const Route = createFileRoute("/pull-requests_/workflow-configurations")(
  {
    validateSearch: normalizePRWorkflowConfigurationsSearch,
    component: PullRequestWorkflowConfigurationsRoutePage,
  },
)
