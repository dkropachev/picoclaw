import {
  createFileRoute,
  useLocation,
  useNavigate,
  useRouter,
} from "@tanstack/react-router"
import { useEffect, useMemo } from "react"

import { PRLifecycleRepositoryAssignmentsPage } from "@/components/pr-workspaces/pr-lifecycle-repository-assignments-page"
import {
  asPRNavigationState,
  goToMarkedPRHistory,
  updatePRNavigationState,
} from "@/routes/-pr-navigation"

export interface PRRepositoryAssignmentsSearch {
  from?: string
  dialog?: "discard"
}

const workspaceIDPattern = /^prw_[0-9a-f]{32}$/

export function normalizePRRepositoryAssignmentsSearch(
  raw: Record<string, unknown>,
): PRRepositoryAssignmentsSearch {
  const from =
    typeof raw.from === "string" && workspaceIDPattern.test(raw.from)
      ? raw.from
      : undefined
  return {
    ...(from ? { from } : {}),
    ...(raw.dialog === "discard" ? { dialog: "discard" as const } : {}),
  }
}

function PullRequestRepositoryAssignmentsRoutePage() {
  const navigate = useNavigate()
  const router = useRouter()
  const locationState = useLocation({
    select: (location) => asPRNavigationState(location.state),
  })
  const pathname = useLocation({ select: (location) => location.pathname })
  const locationSearch = useLocation({ select: (location) => location.search })
  const locationHash = useLocation({ select: (location) => location.hash })
  const search = useMemo(
    () => normalizePRRepositoryAssignmentsSearch({ ...locationSearch }),
    [locationSearch],
  )
  const canonical =
    pathname !== "/pull-requests/repository-assignments" ||
    (locationHash === "" &&
      Object.keys(locationSearch).length === Object.keys(search).length &&
      Object.entries(search).every(
        ([key, value]) =>
          (locationSearch as Record<string, unknown>)[key] === value,
      ))

  useEffect(() => {
    if (canonical) return
    void navigate({
      to: "/pull-requests/repository-assignments",
      search,
      hash: "",
      replace: true,
    })
  }, [canonical, navigate, search])

  if (!canonical) return null

  return (
    <PRLifecycleRepositoryAssignmentsPage
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
          to: "/pull-requests/repository-assignments",
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
    />
  )
}

export const Route = createFileRoute("/pull-requests_/repository-assignments")({
  validateSearch: normalizePRRepositoryAssignmentsSearch,
  component: PullRequestRepositoryAssignmentsRoutePage,
})
