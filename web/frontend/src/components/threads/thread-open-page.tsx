import { IconMessages, IconPlus, IconTrash } from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useSetAtom } from "jotai"
import { useEffect } from "react"
import { useTranslation } from "react-i18next"

import {
  type ThreadSummary,
  createThread,
  dropThread,
  getThread,
} from "@/api/threads"
import { ChatPage } from "@/components/chat/chat-page"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { switchChatSession } from "@/features/chat/controller"
import { buildThreadSourceQuery } from "@/features/chat/thread-seed"
import { threadOpenSessionIdAtom } from "@/store/threads"

function EmptyThreadPage({ onCreateThread }: { onCreateThread: () => void }) {
  const { t } = useTranslation()

  return (
    <div className="bg-background/95 flex h-full flex-col">
      <PageHeader title={t("threads.threadTitle")}>
        <Button
          variant="secondary"
          size="sm"
          onClick={onCreateThread}
          className="h-9 gap-2"
        >
          <IconPlus className="size-4" />
          <span className="hidden sm:inline">{t("threads.newThread")}</span>
        </Button>
      </PageHeader>
      <div className="flex min-h-0 flex-1 items-center justify-center px-6 py-12 text-center">
        <div className="flex max-w-sm flex-col items-center">
          <div className="mb-5 flex h-14 w-14 items-center justify-center rounded-xl bg-sky-500/10 text-sky-600 dark:text-sky-400">
            <IconMessages className="h-7 w-7" />
          </div>
          <h3 className="text-foreground text-lg font-medium">
            {t("threads.empty")}
          </h3>
        </div>
      </div>
    </div>
  )
}

export function ThreadOpenPage({ threadId = "" }: { threadId?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const setThreadOpenSessionId = useSetAtom(threadOpenSessionIdAtom)

  useEffect(() => {
    let cancelled = false
    const normalizedID = threadId.trim()

    if (!normalizedID) {
      setThreadOpenSessionId("")
      return () => {
        cancelled = true
      }
    }

    void getThread(normalizedID)
      .then((thread) => {
        if (cancelled) {
          return
        }

        if (!thread || thread.discoverable === false) {
          setThreadOpenSessionId("")
          void navigate({ to: "/threads/open", replace: true })
          return
        }

        const threadSessionId = thread.ui_session_id || thread.id
        setThreadOpenSessionId(threadSessionId)
        void switchChatSession(threadSessionId)
      })
      .catch((error) => {
        console.error("Failed to load thread:", error)
        if (!cancelled) {
          setThreadOpenSessionId("")
          void navigate({ to: "/threads/open", replace: true })
        }
      })

    return () => {
      cancelled = true
    }
  }, [navigate, setThreadOpenSessionId, threadId])

  const handleCreateThread = async () => {
    try {
      const thread = await createThread({
        type: "general",
        title: t("threads.newThread"),
        source_query: buildThreadSourceQuery(),
      })
      const threadSessionId = thread.ui_session_id || thread.id
      setThreadOpenSessionId(threadSessionId)
      void switchChatSession(threadSessionId)
      void navigate({
        to: "/threads/open/$threadId",
        params: { threadId: threadSessionId },
      })
    } catch (error) {
      console.error("Failed to create thread:", error)
    }
  }

  const handleDropThread = async (thread: ThreadSummary) => {
    try {
      await dropThread(thread.id)
      setThreadOpenSessionId("")
      void navigate({ to: "/threads/open" })
    } catch (error) {
      console.error("Failed to drop thread:", error)
    }
  }

  if (!threadId.trim()) {
    return <EmptyThreadPage onCreateThread={() => void handleCreateThread()} />
  }

  return (
    <ChatPage
      fallbackTitle={t("threads.threadTitle")}
      newChatLabel={t("threads.newThread")}
      onNewChat={() => void handleCreateThread()}
      showSessionHistory={false}
      activeThreadActions={(thread) => (
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="text-muted-foreground hover:text-destructive h-9 w-9"
          title={t("threads.dropThread")}
          aria-label={t("threads.dropThread")}
          onClick={() => void handleDropThread(thread)}
        >
          <IconTrash className="size-4" />
        </Button>
      )}
    />
  )
}
