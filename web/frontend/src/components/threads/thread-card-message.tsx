import { IconArrowRight, IconSearch } from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useAtomValue, useSetAtom } from "jotai"
import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { dropThread, type ThreadSummary } from "@/api/threads"
import { ThreadTile } from "@/components/threads/thread-tile"
import {
  switchChatSession,
  switchChatSessionAndSend,
} from "@/features/chat/controller"
import { buildThreadInitialPromptFromCandidates } from "@/features/chat/thread-seed"
import type { ThreadCardPayload } from "@/features/chat/thread-cards"
import { chatAtom } from "@/store/chat"
import {
  threadOpenSessionIdAtom,
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

export function ThreadCardMessage({ payload }: { payload: ThreadCardPayload }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { activeSessionId } = useAtomValue(chatAtom)
  const setThreadOpenSessionId = useSetAtom(threadOpenSessionIdAtom)
  const setThreadSearchQuery = useSetAtom(threadSearchQueryAtom)
  const setFocusNonce = useSetAtom(threadSearchFocusNonceAtom)
  const autoSwitchedThreadIdRef = useRef<string | null>(null)
  const [droppedThreadIds, setDroppedThreadIds] = useState<Set<string>>(
    () => new Set(),
  )
  const navigateToThread = useCallback(
    (threadId: string) => {
      setThreadOpenSessionId(threadId)
      void navigate({
        to: "/threads/open/$threadId",
        params: { threadId },
      })
    },
    [navigate, setThreadOpenSessionId],
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
      const seedMessage =
        payload.thread.message_count === 0
          ? buildThreadInitialPromptFromCandidates(
              payload.query,
              payload.thread.source_query,
              payload.thread.preview,
              payload.thread.title,
            )
          : ""
      if (seedMessage) {
        void switchChatSessionAndSend(targetSessionId, {
          content: seedMessage,
        })
      } else {
        void switchChatSession(targetSessionId)
      }
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
    void navigate({
      to: "/threads/search",
      search: searchQuery ? { query: searchQuery } : {},
    })
  }

  const openThread = (threadId: string) => {
    void switchChatSession(threadId)
    navigateToThread(threadId)
  }

  const handleDropThread = async (thread: ThreadSummary) => {
    try {
      await dropThread(thread.id)
      const threadSessionId = thread.ui_session_id || thread.id
      setThreadOpenSessionId((current) =>
        current === thread.id || current === threadSessionId ? "" : current,
      )
      setDroppedThreadIds((prev) => {
        const next = new Set(prev)
        next.add(thread.id)
        return next
      })
      if (threadSessionId === activeSessionId) {
        void navigate({ to: "/threads/open" })
      }
    } catch (error) {
      console.error("Failed to drop thread:", error)
    }
  }

  const visibleThreads = threads.filter(
    (thread) => !droppedThreadIds.has(thread.id),
  )

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
        ) : visibleThreads.length === 0 ? (
          <div className="text-muted-foreground px-2 py-4 text-center text-xs">
            {t("threads.emptySearch")}
          </div>
        ) : (
          visibleThreads.map((thread) => (
            <ThreadTile
              key={thread.id}
              thread={thread}
              compact
              onOpen={openThread}
              onDrop={(item) => void handleDropThread(item)}
              dropLabel={t("threads.dropThread")}
            />
          ))
        )}
      </div>
    </div>
  )
}
