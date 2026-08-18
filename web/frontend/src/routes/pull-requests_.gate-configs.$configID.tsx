import {
  createFileRoute,
  redirect,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import {
  type PRLifecycleDecisionPoint,
  isPRLifecycleDecisionPoint,
} from "@/api/pr-lifecycle-flow"
import { isPRLifecycleGateConfigID } from "@/api/pr-lifecycle-gate-configs"
import { PRLifecycleGateConfigsPage } from "@/components/pr-workspaces/pr-lifecycle-gate-configs-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRGateConfigEditorSearch {
  flow: "review" | "implementation"
  from?: string
  gate?: PRLifecycleDecisionPoint
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRGateConfigEditorSearch(
  raw: Record<string, unknown>,
): PRGateConfigEditorSearch {
  const flow: PRGateConfigEditorSearch["flow"] =
    raw.flow === "implementation" ? "implementation" : "review"
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  const base: Pick<PRGateConfigEditorSearch, "flow" | "from"> = {
    flow,
    ...(from ? { from } : {}),
  }
  return {
    ...base,
    ...(isPRLifecycleDecisionPoint(raw.gate) ? { gate: raw.gate } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestGateConfigEditorRoutePage() {
  const { configID } = Route.useParams()
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRGateConfigEditorSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== `/pull-requests/gate-configs/${configID}` ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/gate-configs/$configID",
      params: { configID },
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, configID, navigate, search])

  if (!canonical) return null
  const parentSearch = {
    flow: search.flow,
    ...(search.from ? { from: search.from } : {}),
  } as const
  const discardOwnerSearch = {
    ...parentSearch,
    ...(search.gate ? { gate: search.gate } : {}),
  } as const
  const returnToConfigs = () => {
    const fallback = () =>
      void navigate({
        to: "/pull-requests/gate-configs",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    if (
      locationState.prParent === "gate-configs" &&
      locationState.prParentKey &&
      goToMarkedPRHistory(
        router.history,
        locationState,
        locationState.prParentIndex,
        locationState.prParentKey,
        fallback,
      )
    ) {
      return
    }
    fallback()
  }
  return (
    <PRLifecycleGateConfigsPage
      activeFlowID={search.flow}
      discardOpen={search.dialog === "discard"}
      initialDecisionPoint={search.gate}
      initialConfigID={configID}
      page="config"
      onBack={returnToConfigs}
      onDecisionPointChange={(gate) => {
        if (gate) {
          void navigate({
            to: "/pull-requests/gate-configs/$configID",
            params: { configID },
            search: { ...parentSearch, gate },
            state: (previous) =>
              updatePRNavigationState(previous, () => ({
                prOverlay: "gate",
              })),
          })
          return
        }
        if (locationState.prOverlay === "gate" && router.history.canGoBack()) {
          router.history.back()
          return
        }
        void navigate({
          to: "/pull-requests/gate-configs/$configID",
          params: { configID },
          search: parentSearch,
          replace: true,
        })
      }}
      onDiscardOpenChange={(open) =>
        navigate({
          to: "/pull-requests/gate-configs/$configID",
          params: { configID },
          search: open
            ? { ...discardOwnerSearch, dialog: "discard" }
            : discardOwnerSearch,
          replace: true,
          state: (previous) =>
            updatePRNavigationState(previous, () => ({
              prOverlay: open ? "discard" : undefined,
            })),
        })
      }
      onFlowChange={(flow) =>
        void navigate({
          to: "/pull-requests/gate-configs/$configID",
          params: { configID },
          search: {
            flow,
            ...(search.from ? { from: search.from } : {}),
            ...(search.gate ? { gate: search.gate } : {}),
          },
          replace: Boolean(search.gate),
          state: (previous) =>
            updatePRNavigationState(previous, () => ({
              prOverlay: undefined,
            })),
        })
      }
      onConfigChange={(nextConfigID) => {
        if (!nextConfigID) {
          returnToConfigs()
          return
        }
        if (!isPRLifecycleGateConfigID(nextConfigID)) return
        void navigate({
          to: "/pull-requests/gate-configs/$configID",
          params: { configID: nextConfigID },
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

export const Route = createFileRoute("/pull-requests_/gate-configs/$configID")({
  validateSearch: normalizePRGateConfigEditorSearch,
  beforeLoad: ({ params, search }) => {
    if (!isPRLifecycleGateConfigID(params.configID)) {
      throw redirect({
        to: "/pull-requests/gate-configs",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    }
  },
  component: PullRequestGateConfigEditorRoutePage,
})
