import {
  createFileRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import {
  PRLifecycleGateProfilesPage,
  type PRLifecycleSettingsTab,
} from "@/components/pr-workspaces/pr-lifecycle-gate-profiles-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRLifecycleSettingsSearch {
  tab: PRLifecycleSettingsTab
  from?: string
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRLifecycleSettingsSearch(
  raw: Record<string, unknown>,
): PRLifecycleSettingsSearch {
  const tab: PRLifecycleSettingsTab =
    raw.tab === "scope" || raw.tab === "deferred" ? raw.tab : "nudging"
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  return {
    tab,
    ...(from ? { from } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestLifecycleSettingsRoutePage() {
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRLifecycleSettingsSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== "/pull-requests/settings" ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/settings",
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, search])

  if (!canonical) return null

  const parentSearch = {
    tab: search.tab,
    ...(search.from ? { from: search.from } : {}),
  } as const
  return (
    <PRLifecycleGateProfilesPage
      discardOpen={search.dialog === "discard"}
      page="settings"
      settingsTab={search.tab}
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
          to: "/pull-requests/settings",
          search: open ? { ...parentSearch, dialog: "discard" } : parentSearch,
          replace: true,
          state: (previous) =>
            updatePRNavigationState(previous, () => ({
              prOverlay: open ? "discard" : undefined,
            })),
        })
      }
      onSettingsTabChange={(tab) =>
        void navigate({
          to: "/pull-requests/settings",
          search: { tab, ...(search.from ? { from: search.from } : {}) },
          replace: false,
          state: (previous) =>
            updatePRNavigationState(previous, () => ({
              prOverlay: undefined,
            })),
        })
      }
    />
  )
}

export const Route = createFileRoute("/pull-requests_/settings")({
  validateSearch: normalizePRLifecycleSettingsSearch,
  component: PullRequestLifecycleSettingsRoutePage,
})
