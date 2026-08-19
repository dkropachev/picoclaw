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
import { isPRLifecycleWorkflowConfigurationID } from "@/api/pr-lifecycle-workflow-configurations"
import { PRLifecycleWorkflowConfigurationsPage } from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRWorkflowConfigurationEditorSearch {
  flow: "review" | "implementation"
  from?: string
  gate?: PRLifecycleDecisionPoint
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRWorkflowConfigurationEditorSearch(
  raw: Record<string, unknown>,
): PRWorkflowConfigurationEditorSearch {
  const flow: PRWorkflowConfigurationEditorSearch["flow"] =
    raw.flow === "implementation" ? "implementation" : "review"
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  const base: Pick<PRWorkflowConfigurationEditorSearch, "flow" | "from"> = {
    flow,
    ...(from ? { from } : {}),
  }
  return {
    ...base,
    ...(isPRLifecycleDecisionPoint(raw.gate) ? { gate: raw.gate } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestWorkflowConfigurationEditorRoutePage() {
  const { configurationID } = Route.useParams()
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRWorkflowConfigurationEditorSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== `/pull-requests/workflow-configurations/${configurationID}` ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/workflow-configurations/$configurationID",
      params: { configurationID },
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, configurationID, navigate, search])

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
        to: "/pull-requests/workflow-configurations",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    if (
      locationState.prParent === "workflow-configurations" &&
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
    <PRLifecycleWorkflowConfigurationsPage
      activeFlowID={search.flow}
      discardOpen={search.dialog === "discard"}
      initialDecisionPoint={search.gate}
      initialConfigID={configurationID}
      page="config"
      onBack={returnToConfigs}
      onDecisionPointChange={(gate) => {
        if (gate) {
          void navigate({
            to: "/pull-requests/workflow-configurations/$configurationID",
            params: { configurationID },
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
          to: "/pull-requests/workflow-configurations/$configurationID",
          params: { configurationID },
          search: parentSearch,
          replace: true,
        })
      }}
      onDiscardOpenChange={(open) =>
        navigate({
          to: "/pull-requests/workflow-configurations/$configurationID",
          params: { configurationID },
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
          to: "/pull-requests/workflow-configurations/$configurationID",
          params: { configurationID },
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
        if (!isPRLifecycleWorkflowConfigurationID(nextConfigID)) return
        void navigate({
          to: "/pull-requests/workflow-configurations/$configurationID",
          params: { configurationID: nextConfigID },
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

export const Route = createFileRoute(
  "/pull-requests_/workflow-configurations/$configurationID",
)({
  validateSearch: normalizePRWorkflowConfigurationEditorSearch,
  beforeLoad: ({ params, search }) => {
    if (!isPRLifecycleWorkflowConfigurationID(params.configurationID)) {
      throw redirect({
        to: "/pull-requests/workflow-configurations",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    }
  },
  component: PullRequestWorkflowConfigurationEditorRoutePage,
})
