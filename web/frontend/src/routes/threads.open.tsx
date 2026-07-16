import {
  Outlet,
  createFileRoute,
  useRouterState,
} from "@tanstack/react-router"

import { ThreadOpenPage } from "@/components/threads/thread-open-page"

function ThreadsOpenRoutePage() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname === "/threads/open") {
    return <ThreadOpenPage />
  }

  return <Outlet />
}

export const Route = createFileRoute("/threads/open")({
  component: ThreadsOpenRoutePage,
})
