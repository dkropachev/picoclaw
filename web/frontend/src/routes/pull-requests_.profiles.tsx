import {
  Outlet,
  createFileRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import { isPRLifecycleGateProfileID } from "@/api/pr-lifecycle-gate-profiles"
import { PRLifecycleGateProfilesPage } from "@/components/pr-workspaces/pr-lifecycle-gate-profiles-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRProfilesSearch {
  from?: string
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRProfilesSearch(
  raw: Record<string, unknown>,
): PRProfilesSearch {
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  return {
    ...(from ? { from } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestProfilesRoutePage() {
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRProfilesSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== "/pull-requests/profiles" ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/profiles",
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, search])

  if (pathname !== "/pull-requests/profiles") return <Outlet />
  if (!canonical) return null

  return (
    <PRLifecycleGateProfilesPage
      page="profiles"
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
          to: "/pull-requests/profiles",
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
      onProfileChange={(profileID) => {
        if (!profileID || !isPRLifecycleGateProfileID(profileID)) return
        void navigate({
          to: "/pull-requests/profiles/$profileID",
          params: { profileID },
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

export const Route = createFileRoute("/pull-requests_/profiles")({
  validateSearch: normalizePRProfilesSearch,
  component: PullRequestProfilesRoutePage,
})
