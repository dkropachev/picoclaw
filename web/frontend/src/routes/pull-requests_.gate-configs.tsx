import {
  Outlet,
  createFileRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import { isPRLifecycleGateConfigID } from "@/api/pr-lifecycle-gate-configs"
import { PRLifecycleGateConfigsPage } from "@/components/pr-workspaces/pr-lifecycle-gate-configs-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRGateConfigsSearch {
  from?: string
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRGateConfigsSearch(
  raw: Record<string, unknown>,
): PRGateConfigsSearch {
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  return {
    ...(from ? { from } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestGateConfigsRoutePage() {
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRGateConfigsSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== "/pull-requests/gate-configs" ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/gate-configs",
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, search])

  if (pathname !== "/pull-requests/gate-configs") return <Outlet />
  if (!canonical) return null

  return (
    <PRLifecycleGateConfigsPage
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
          to: "/pull-requests/gate-configs",
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
        if (!configID || !isPRLifecycleGateConfigID(configID)) return
        void navigate({
          to: "/pull-requests/gate-configs/$configID",
          params: { configID },
          search: {
            flow: "review",
            ...(search.from ? { from: search.from } : {}),
          },
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "gate-configs",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
            })),
        })
      }}
    />
  )
}

export const Route = createFileRoute("/pull-requests_/gate-configs")({
  validateSearch: normalizePRGateConfigsSearch,
  component: PullRequestGateConfigsRoutePage,
})
