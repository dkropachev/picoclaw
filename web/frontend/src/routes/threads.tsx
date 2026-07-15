import { createFileRoute, useParams } from "@tanstack/react-router"

import { ThreadsPage } from "@/components/threads/threads-page"

function ThreadsRoutePage() {
  const { threadId } = useParams({ strict: false }) as { threadId?: string }
  return <ThreadsPage threadId={threadId} />
}

export const Route = createFileRoute("/threads")({
  component: ThreadsRoutePage,
})
