import { IconArrowRight, IconSearch } from "@tabler/icons-react"
import { useSetAtom } from "jotai"
import { useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import { ThreadTile } from "@/components/threads/thread-tile"
import { switchChatSession } from "@/features/chat/controller"
import type { ThreadCardPayload } from "@/features/chat/thread-cards"
import {
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

export function ThreadCardMessage({ payload }: { payload: ThreadCardPayload }) {
  const { t } = useTranslation()
  const setThreadSearchQuery = useSetAtom(threadSearchQueryAtom)
  const setFocusNonce = useSetAtom(threadSearchFocusNonceAtom)
  const autoSwitchedThreadIdRef = useRef<string | null>(null)

  const searchQuery =
    payload.type === "picoclaw.thread_search.v1"
      ? payload.query
      : (payload.query ?? "")
  const threads =
    payload.type === "picoclaw.thread_search.v1"
      ? payload.threads
      : [payload.thread]

  useEffect(() => {
    if (
      payload.type === "picoclaw.thread_switch.v1" &&
      payload.auto_switch &&
      payload.thread.id
    ) {
      if (autoSwitchedThreadIdRef.current === payload.thread.id) {
        return
      }
      autoSwitchedThreadIdRef.current = payload.thread.id
      void switchChatSession(payload.thread.id)
    }
  }, [payload])

  const openThreadSearch = () => {
    setThreadSearchQuery(searchQuery)
    setFocusNonce((prev) => prev + 1)
  }

  const openThread = (threadId: string) => {
    void switchChatSession(threadId)
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
              {payload.type === "picoclaw.thread_switch.v1"
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
        {threads.length === 0 ? (
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
