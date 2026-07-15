import { createFileRoute } from "@tanstack/react-router"

import { ThreadOpenPage } from "@/components/threads/thread-open-page"

function ThreadOpenRoutePage() {
  const { threadId } = Route.useParams()
  return <ThreadOpenPage threadId={threadId} />
}

export const Route = createFileRoute("/threads/open/$threadId")({
  component: ThreadOpenRoutePage,
})
