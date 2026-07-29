import { createFileRoute } from "@tanstack/react-router"

import { EventSourcesPage } from "@/components/events/event-sources-page"

export const Route = createFileRoute("/event-sources")({
  component: EventSourcesPage,
})
