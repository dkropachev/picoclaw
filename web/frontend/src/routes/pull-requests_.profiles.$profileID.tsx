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
  isPRLifecycleGateProfileID,
} from "@/api/pr-lifecycle-gate-profiles"
import { PRLifecycleGateProfilesPage } from "@/components/pr-workspaces/pr-lifecycle-gate-profiles-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRProfileEditorSearch {
  flow: "review" | "implementation"
  from?: string
  gate?: PRLifecycleDecisionPoint
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRProfileEditorSearch(
  raw: Record<string, unknown>,
): PRProfileEditorSearch {
  const flow: PRProfileEditorSearch["flow"] =
    raw.flow === "implementation" ? "implementation" : "review"
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  const base: Pick<PRProfileEditorSearch, "flow" | "from"> = {
    flow,
    ...(from ? { from } : {}),
  }
  return {
    ...base,
    ...(isPRLifecycleDecisionPoint(raw.gate) ? { gate: raw.gate } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestProfileEditorRoutePage() {
  const { profileID } = Route.useParams()
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRProfileEditorSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== `/pull-requests/profiles/${profileID}` ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/profiles/$profileID",
      params: { profileID },
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, profileID, search])

  if (!canonical) return null
  const parentSearch = {
    flow: search.flow,
    ...(search.from ? { from: search.from } : {}),
  } as const
  const discardOwnerSearch = {
    ...parentSearch,
    ...(search.gate ? { gate: search.gate } : {}),
  } as const
  const returnToProfiles = () => {
    const fallback = () =>
      void navigate({
        to: "/pull-requests/profiles",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    if (
      locationState.prParent === "profiles" &&
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
    <PRLifecycleGateProfilesPage
      activeFlowID={search.flow}
      discardOpen={search.dialog === "discard"}
      initialDecisionPoint={search.gate}
      initialProfileID={profileID}
      page="profile"
      onBack={returnToProfiles}
      onDecisionPointChange={(gate) => {
        if (gate) {
          void navigate({
            to: "/pull-requests/profiles/$profileID",
            params: { profileID },
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
          to: "/pull-requests/profiles/$profileID",
          params: { profileID },
          search: parentSearch,
          replace: true,
        })
      }}
      onDiscardOpenChange={(open) =>
        navigate({
          to: "/pull-requests/profiles/$profileID",
          params: { profileID },
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
          to: "/pull-requests/profiles/$profileID",
          params: { profileID },
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
      onProfileChange={(nextProfileID) => {
        if (!nextProfileID) {
          returnToProfiles()
          return
        }
        if (!isPRLifecycleGateProfileID(nextProfileID)) return
        void navigate({
          to: "/pull-requests/profiles/$profileID",
          params: { profileID: nextProfileID },
          search: {
            flow: "review",
            ...(search.from ? { from: search.from } : {}),
          },
          state: (previous) =>
            updatePRNavigationState(previous, (current) => ({
              prParent: "profiles",
              prParentIndex: current.__TSR_index,
              prParentKey: current.__TSR_key,
            })),
        })
      }}
    />
  )
}

export const Route = createFileRoute("/pull-requests_/profiles/$profileID")({
  validateSearch: normalizePRProfileEditorSearch,
  beforeLoad: ({ params, search }) => {
    if (!isPRLifecycleGateProfileID(params.profileID)) {
      throw redirect({
        to: "/pull-requests/profiles",
        search: search.from ? { from: search.from } : {},
        replace: true,
      })
    }
  },
  component: PullRequestProfileEditorRoutePage,
})
