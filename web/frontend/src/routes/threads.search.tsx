import { createFileRoute } from "@tanstack/react-router"

import type { ThreadType } from "@/api/threads"
import { ThreadsPage } from "@/components/threads/threads-page"

type ThreadSearchRouteSearch = {
  query?: string
  type?: ThreadType | "all"
}

const THREAD_TYPES = new Set([
  "all",
  "coding",
  "reviewing",
  "investigating",
  "general",
])

function normalizeSearch(
  raw: Record<string, unknown>,
): ThreadSearchRouteSearch {
  const query = typeof raw.query === "string" ? raw.query.trim() : ""
  const type = typeof raw.type === "string" ? raw.type.trim() : ""

  return {
    ...(query ? { query } : {}),
    ...(THREAD_TYPES.has(type) ? { type: type as ThreadType | "all" } : {}),
  }
}

function ThreadsSearchRoutePage() {
  const search = Route.useSearch()
  return (
    <ThreadsPage
      initialQuery={search.query ?? ""}
      initialType={search.type ?? "all"}
    />
  )
}

export const Route = createFileRoute("/threads/search")({
  validateSearch: normalizeSearch,
  component: ThreadsSearchRoutePage,
})
