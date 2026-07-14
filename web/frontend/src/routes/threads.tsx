import { createFileRoute } from "@tanstack/react-router"

import { ThreadSidebar } from "@/components/threads/thread-sidebar"

export const Route = createFileRoute("/threads")({
  component: ThreadsRoute,
})

function ThreadsRoute() {
  return <ThreadSidebar />
}
