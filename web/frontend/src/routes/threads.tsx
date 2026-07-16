import {
  Navigate,
  Outlet,
  createFileRoute,
  useRouterState,
} from "@tanstack/react-router"

function ThreadsRoutePage() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/threads") {
    return <Navigate to="/threads/search" />
  }

  return <Outlet />
}

export const Route = createFileRoute("/threads")({
  component: ThreadsRoutePage,
})
