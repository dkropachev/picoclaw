import { IconSearch } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { getThreads, type ThreadSummary } from "@/api/threads"
import { ThreadCardMessage } from "@/components/threads/thread-card-message"
import type { ThreadToolSearchRequest } from "@/features/chat/thread-tool-calls"

type SearchState =
  | { status: "loading" }
  | { status: "loaded"; threads: ThreadSummary[] }
  | { status: "error" }

export function ThreadToolCallSearchCard({
  request,
}: {
  request: ThreadToolSearchRequest
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<SearchState>({ status: "loading" })

  useEffect(() => {
    let cancelled = false
    setState({ status: "loading" })

    void getThreads({
      query: request.query,
      type: request.type,
      context: request.context,
      limit: request.limit,
    })
      .then((threads) => {
        if (!cancelled) {
          setState({ status: "loaded", threads })
        }
      })
      .catch((error: unknown) => {
        console.error("Failed to load thread search tool call:", error)
        if (!cancelled) {
          setState({ status: "error" })
        }
      })

    return () => {
      cancelled = true
    }
  }, [request])

  if (state.status === "loaded") {
    return (
      <ThreadCardMessage
        payload={{
          type: "picoclaw.thread_search.v2",
          query: request.query,
          threads: state.threads,
          total: state.threads.length,
        }}
      />
    )
  }

  return (
    <div className="border-border/60 bg-card text-card-foreground overflow-hidden rounded-xl border">
      <div className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left">
        <div className="flex min-w-0 items-center gap-2">
          <IconSearch className="text-muted-foreground size-4 shrink-0" />
          <div className="min-w-0">
            <div className="text-sm font-semibold">
              {t("threads.searchResult")}
            </div>
            <div className="text-muted-foreground truncate text-xs">
              {request.query || t("threads.allThreads")}
            </div>
          </div>
        </div>
      </div>
      <div className="border-border/40 border-t p-3">
        <div className="text-muted-foreground px-2 py-4 text-center text-xs">
          {state.status === "loading"
            ? t("threads.loading")
            : t("threads.loadFailed")}
        </div>
      </div>
    </div>
  )
}
