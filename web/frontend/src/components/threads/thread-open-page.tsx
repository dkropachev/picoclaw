import { useEffect } from "react"
import { useTranslation } from "react-i18next"

import { ChatPage } from "@/components/chat/chat-page"
import { switchChatSession } from "@/features/chat/controller"

export function ThreadOpenPage({ threadId }: { threadId: string }) {
  const { t } = useTranslation()

  useEffect(() => {
    const normalizedID = threadId.trim()
    if (normalizedID) {
      void switchChatSession(normalizedID)
    }
  }, [threadId])

  return <ChatPage fallbackTitle={t("threads.threadTitle")} />
}
