import { useTranslation } from "react-i18next"

import { ChatPage } from "@/components/chat/chat-page"
import { ThreadSidebar } from "@/components/threads/thread-sidebar"
import type { ThreadType } from "@/api/threads"

export function ThreadsPage({
  initialQuery = "",
  initialType = "all",
}: {
  initialQuery?: string
  initialType?: ThreadType | "all"
}) {
  const { t } = useTranslation()

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
        <ChatPage fallbackTitle={t("threads.threadTitle")} />
      </main>
    </div>
  )
}
