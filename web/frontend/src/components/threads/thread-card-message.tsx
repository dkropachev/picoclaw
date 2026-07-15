import { IconArrowRight, IconSearch } from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useSetAtom } from "jotai"
import { useCallback, useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import type { ThreadSummary } from "@/api/threads"
import { ThreadTile } from "@/components/threads/thread-tile"
import { switchChatSession } from "@/features/chat/controller"
import type { ThreadCardPayload } from "@/features/chat/thread-cards"
import {
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

export function ThreadCardMessage({ payload }: { payload: ThreadCardPayload }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const setThreadSearchQuery = useSetAtom(threadSearchQueryAtom)
  const setFocusNonce = useSetAtom(threadSearchFocusNonceAtom)
  const autoSwitchedThreadIdRef = useRef<string | null>(null)
  const navigateToThread = useCallback(
    (threadId: string) => {
      void navigate({
        to: "/threads/$threadId",
        params: { threadId },
      })
    },
    [navigate],
  )

  let searchQuery = ""
  let threads: ThreadSummary[] = []
  switch (payload.type) {
    case "picoclaw.thread_search.v1":
    case "picoclaw.thread_search.v2":
    case "picoclaw.thread_proposal.v1":
      searchQuery = payload.query ?? ""
      threads = payload.threads
      break
    case "picoclaw.thread_switch.v1":
    case "picoclaw.thread_switch.v2":
      searchQuery = payload.query ?? ""
      threads = [payload.thread]
      break
    case "picoclaw.thread_return.v1":
      break
  }

  useEffect(() => {
    if (
      (payload.type === "picoclaw.thread_switch.v1" ||
        payload.type === "picoclaw.thread_switch.v2") &&
      payload.auto_switch &&
      payload.thread.id
    ) {
      const targetSessionId =
        payload.target_session_id ||
        payload.thread.ui_session_id ||
        payload.thread.id
      if (autoSwitchedThreadIdRef.current === targetSessionId) {
        return
      }
      autoSwitchedThreadIdRef.current = targetSessionId
      void switchChatSession(targetSessionId)
      navigateToThread(targetSessionId)
    }
    if (
      payload.type === "picoclaw.thread_return.v1" &&
      payload.target_session_id
    ) {
      if (autoSwitchedThreadIdRef.current === payload.target_session_id) {
        return
      }
      autoSwitchedThreadIdRef.current = payload.target_session_id
      void switchChatSession(payload.target_session_id)
    }
  }, [navigateToThread, payload])

  const openThreadSearch = () => {
    setThreadSearchQuery(searchQuery)
    setFocusNonce((prev) => prev + 1)
    void navigate({ to: "/threads" })
  }

  const openThread = (threadId: string) => {
    void switchChatSession(threadId)
    navigateToThread(threadId)
  }

  return (
    <div className="border-border/60 bg-card text-card-foreground overflow-hidden rounded-xl border">
      <button
        type="button"
        className="hover:bg-muted/50 flex w-full items-center justify-between gap-3 px-4 py-3 text-left transition-colors"
        onClick={openThreadSearch}
      >
        <div className="flex min-w-0 items-center gap-2">
          <IconSearch className="text-muted-foreground size-4 shrink-0" />
          <div className="min-w-0">
            <div className="text-sm font-semibold">
              {payload.type === "picoclaw.thread_switch.v1" ||
              payload.type === "picoclaw.thread_switch.v2" ||
              payload.type === "picoclaw.thread_return.v1"
                ? t("threads.switching")
                : t("threads.searchResult")}
            </div>
            <div className="text-muted-foreground truncate text-xs">
              {searchQuery || t("threads.allThreads")}
            </div>
          </div>
        </div>
        <IconArrowRight className="text-muted-foreground size-4 shrink-0" />
      </button>

      <div className="border-border/40 space-y-2 border-t p-3">
        {payload.type === "picoclaw.thread_return.v1" ? (
          <div className="text-muted-foreground px-2 py-4 text-center text-xs">
            {t("threads.switching")}
          </div>
        ) : threads.length === 0 ? (
          <div className="text-muted-foreground px-2 py-4 text-center text-xs">
            {t("threads.emptySearch")}
          </div>
        ) : (
          threads.map((thread) => (
            <ThreadTile
              key={thread.id}
              thread={thread}
              compact
              onOpen={openThread}
            />
          ))
        )}
      </div>
    </div>
  )
}
