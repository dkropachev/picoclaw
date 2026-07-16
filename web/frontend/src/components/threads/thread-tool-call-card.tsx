import { IconSearch } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { getThread, getThreads, type ThreadSummary } from "@/api/threads"
import { ThreadCardMessage } from "@/components/threads/thread-card-message"
import type { ThreadCardPayload } from "@/features/chat/thread-cards"
import type { ThreadToolCardRequest } from "@/features/chat/thread-tool-calls"

type CardState =
  | { status: "loading" }
  | { status: "loaded"; payload: ThreadCardPayload }
  | { status: "error" }

function switchCardPayload(
  request: ThreadToolCardRequest,
  thread: ThreadSummary,
): ThreadCardPayload {
  return {
    type: "picoclaw.thread_switch.v2",
    query: request.query,
    auto_switch: false,
    thread,
    target_session_id: thread.ui_session_id || thread.id,
  }
}

function listCardPayload(
  request: ThreadToolCardRequest,
  threads: ThreadSummary[],
): ThreadCardPayload {
  if (request.mode === "switch" && threads.length === 1) {
    return switchCardPayload(request, threads[0])
  }
  if (request.mode === "proposal" || request.mode === "switch") {
    return {
      type: "picoclaw.thread_proposal.v1",
      query: request.query,
      reason: "historical thread tool call",
      threads,
      total: threads.length,
    }
  }
  return {
    type: "picoclaw.thread_search.v2",
    query: request.query,
    threads,
    total: threads.length,
  }
}

export function ThreadToolCallCard({
  request,
}: {
  request: ThreadToolCardRequest
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<CardState>({ status: "loading" })

  useEffect(() => {
    let cancelled = false
    setState({ status: "loading" })

    const loadPayload = async (): Promise<ThreadCardPayload> => {
      if (request.mode === "switch" && request.id) {
        const thread = await getThread(request.id)
        if (thread) {
          return switchCardPayload(request, thread)
        }
      }

      const threads = await getThreads({
        query: request.query,
        type: request.type,
        context: request.context,
        limit: request.limit,
      })
      return listCardPayload(request, threads)
    }

    void loadPayload()
      .then((payload) => {
        if (!cancelled) {
          setState({ status: "loaded", payload })
        }
      })
      .catch((error: unknown) => {
        console.error("Failed to load thread tool call card:", error)
        if (!cancelled) {
          setState({ status: "error" })
        }
      })

    return () => {
      cancelled = true
    }
  }, [request])

  if (state.status === "loaded") {
    return <ThreadCardMessage payload={state.payload} />
  }

  return (
    <div className="border-border/60 bg-card text-card-foreground overflow-hidden rounded-xl border">
      <div className="flex w-full items-center justify-between gap-3 px-4 py-3 text-left">
        <div className="flex min-w-0 items-center gap-2">
          <IconSearch className="text-muted-foreground size-4 shrink-0" />
          <div className="min-w-0">
            <div className="text-sm font-semibold">
              {request.mode === "switch"
                ? t("threads.switching")
                : t("threads.searchResult")}
            </div>
            <div className="text-muted-foreground truncate text-xs">
              {request.query || request.id || t("threads.allThreads")}
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
