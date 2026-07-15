import { IconTrash } from "@tabler/icons-react"
import { useNavigate } from "@tanstack/react-router"
import { useSetAtom } from "jotai"
import { useTranslation } from "react-i18next"

import {
  createThread,
  dropThread,
  type ThreadSummary,
  type ThreadType,
} from "@/api/threads"
import { ChatPage } from "@/components/chat/chat-page"
import { ThreadSidebar } from "@/components/threads/thread-sidebar"
import { Button } from "@/components/ui/button"
import { switchChatSession } from "@/features/chat/controller"
import { buildThreadSourceQuery } from "@/features/chat/thread-seed"
import { threadOpenSessionIdAtom } from "@/store/threads"

export function ThreadsPage({
  initialQuery = "",
  initialType = "all",
}: {
  initialQuery?: string
  initialType?: ThreadType | "all"
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const setThreadOpenSessionId = useSetAtom(threadOpenSessionIdAtom)

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

  return (
    <div className="bg-background flex h-full min-h-0 flex-col lg:flex-row">
      <aside className="border-border/60 min-h-64 shrink-0 border-b lg:h-full lg:w-96 lg:border-r lg:border-b-0 xl:w-[28rem]">
        <ThreadSidebar
          layout="pane"
          title={t("threads.searchTitle")}
          initialQuery={initialQuery}
          initialType={initialType}
          syncURL
        />
      </aside>
      <main className="min-h-0 min-w-0 flex-1">
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
      </main>
    </div>
  )
}
