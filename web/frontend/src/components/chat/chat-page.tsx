import { IconMessages, IconPlus } from "@tabler/icons-react"
import { useAtom } from "jotai"
import {
  type ChangeEvent,
  type ClipboardEvent,
  type DragEvent,
  type ReactNode,
  useEffect,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import { type ThreadSummary, getThread } from "@/api/threads"
import { AssistantMessage } from "@/components/chat/assistant-message"
import {
  ChatComposer,
  type ChatInputDisabledReason,
} from "@/components/chat/chat-composer"
import { ChatEmptyState } from "@/components/chat/chat-empty-state"
import { ModelSelector } from "@/components/chat/model-selector"
import { SessionHistoryMenu } from "@/components/chat/session-history-menu"
import { TypingIndicator } from "@/components/chat/typing-indicator"
import { UserMessage } from "@/components/chat/user-message"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  CHAT_IMAGE_ACCEPT,
  buildChatImageAttachments,
  getTransferredFiles,
  hasFileTransfer,
} from "@/features/chat/image-input"
import { useChatModels } from "@/hooks/use-chat-models"
import { useGateway } from "@/hooks/use-gateway"
import { usePicoChat } from "@/hooks/use-pico-chat"
import { useSessionHistory } from "@/hooks/use-session-history"
import type { AssistantDetailVisibility } from "@/store/chat"
import type { ConnectionState } from "@/store/chat"
import type { ChatAttachment } from "@/store/chat"
import {
  assistantDetailVisibilityAtom,
  shouldShowAssistantMessage,
} from "@/store/chat"
import type { GatewayState } from "@/store/gateway"

interface ChatPageProps {
  fallbackTitle?: string
  newChatLabel?: string
  onNewChat?: () => void
  headerActions?: ReactNode
  activeThreadActions?: (thread: ThreadSummary) => ReactNode
  showSessionHistory?: boolean
}

function resolveChatInputDisabledReason({
  hasDefaultModel,
  connectionState,
  gatewayState,
}: {
  hasDefaultModel: boolean
  connectionState: ConnectionState
  gatewayState: GatewayState
}): ChatInputDisabledReason | null {
  if (gatewayState === "unknown") {
    return "gatewayUnknown"
  }

  if (gatewayState === "starting") {
    return "gatewayStarting"
  }

  if (gatewayState === "restarting") {
    return "gatewayRestarting"
  }

  if (gatewayState === "stopping") {
    return "gatewayStopping"
  }

  if (gatewayState === "stopped") {
    return "gatewayStopped"
  }

  if (gatewayState === "error") {
    return "gatewayError"
  }

  if (connectionState === "connecting") {
    return "websocketConnecting"
  }

  if (connectionState === "error") {
    return "websocketError"
  }

  if (connectionState === "disconnected") {
    return "websocketDisconnected"
  }

  if (!hasDefaultModel) {
    return "noDefaultModel"
  }

  return null
}

function ThreadChatEmptyState({ thread }: { thread: ThreadSummary }) {
  const { t } = useTranslation()
  const contextEntries = Object.entries(thread.context ?? {}).filter(
    ([key, value]) => key && value,
  )

  return (
    <div className="mx-auto flex max-w-xl flex-col items-center justify-center py-20 text-center">
      <div className="bg-muted text-muted-foreground mb-6 flex h-12 w-12 items-center justify-center rounded-xl">
        <IconMessages className="h-7 w-7" />
      </div>
      <h3 className="mb-2 text-lg font-medium">
        {t("threads.chatEmptyTitle")}
      </h3>
      <p className="text-muted-foreground text-sm">
        {t("threads.chatEmptyDescription", { title: thread.title })}
      </p>
      {contextEntries.length > 0 && (
        <div className="mt-4 flex max-w-full flex-wrap justify-center gap-1.5">
          {contextEntries.slice(0, 6).map(([key, value]) => (
            <span
              key={`${thread.id}-${key}`}
              className="bg-muted text-muted-foreground max-w-full truncate rounded-lg px-2 py-1 text-xs"
            >
              {key}:{value}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

export function ChatPage({
  fallbackTitle,
  newChatLabel,
  onNewChat,
  headerActions,
  activeThreadActions,
  showSessionHistory = true,
}: ChatPageProps = {}) {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const dragDepthRef = useRef(0)
  const [isAtBottom, setIsAtBottom] = useState(true)
  const [hasScrolled, setHasScrolled] = useState(false)
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])
  const [isDragActive, setIsDragActive] = useState(false)
  const [activeThread, setActiveThread] = useState<ThreadSummary | null>(null)
  const [assistantDetailVisibility, setAssistantDetailVisibility] = useAtom(
    assistantDetailVisibilityAtom,
  )

  const assistantDetailVisibilityOptions: Array<{
    value: AssistantDetailVisibility
    label: string
  }> = [
    { value: "none", label: t("chat.assistantDetailVisibility.none") },
    { value: "thought", label: t("chat.assistantDetailVisibility.thought") },
    {
      value: "tool_calls",
      label: t("chat.assistantDetailVisibility.toolCalls"),
    },
    { value: "all", label: t("chat.assistantDetailVisibility.all") },
  ]

  const {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    contextUsage,
    sendMessage,
    switchSession,
    newChat,
  } = usePicoChat()

  useEffect(() => {
    let cancelled = false
    setActiveThread(null)

    if (!activeSessionId) {
      return () => {
        cancelled = true
      }
    }

    void getThread(activeSessionId)
      .then((thread) => {
        if (!cancelled) {
          setActiveThread(thread)
        }
      })
      .catch((error) => {
        console.error("Failed to load active thread metadata:", error)
        if (!cancelled) {
          setActiveThread(null)
        }
      })

    return () => {
      cancelled = true
    }
  }, [activeSessionId])

  const { state: gwState } = useGateway()
  const isGatewayRunning = gwState === "running"

  const {
    defaultModelName,
    hasAvailableModels,
    apiKeyModels,
    oauthModels,
    localModels,
    handleSetDefault,
  } = useChatModels({ isConnected: isGatewayRunning })
  const hasDefaultModel = Boolean(defaultModelName)
  const inputDisabledReason = resolveChatInputDisabledReason({
    hasDefaultModel,
    connectionState,
    gatewayState: gwState,
  })
  const canInput = inputDisabledReason === null

  const {
    sessions,
    hasMore,
    loadError,
    loadErrorMessage,
    observerRef,
    loadSessions,
    handleDeleteSession,
  } = useSessionHistory({
    activeSessionId,
    onDeletedActiveSession: newChat,
  })

  const syncScrollState = (element: HTMLDivElement) => {
    const { clientHeight, scrollHeight, scrollTop } = element
    setHasScrolled(scrollTop > 0)
    setIsAtBottom(scrollHeight - scrollTop <= clientHeight + 10)
  }

  const handleScroll = (e: React.UIEvent<HTMLDivElement>) => {
    syncScrollState(e.currentTarget)
  }

  useEffect(() => {
    if (scrollRef.current) {
      if (isAtBottom) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      }
      syncScrollState(scrollRef.current)
    }
  }, [messages, isTyping, isAtBottom])

  const handleSend = () => {
    if ((!input.trim() && attachments.length === 0) || !canInput) return
    if (
      sendMessage({
        content: input,
        attachments,
      })
    ) {
      setInput("")
      setAttachments([])
    }
  }

  const handleAddImages = () => {
    if (!canInput) return
    fileInputRef.current?.click()
  }

  const handleRemoveAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, itemIndex) => itemIndex !== index))
  }

  const appendImageFiles = async (files: readonly File[]) => {
    if (!canInput || files.length === 0) {
      return
    }

    const nextAttachments = await buildChatImageAttachments(files, t)
    if (nextAttachments.length === 0) {
      return
    }

    setAttachments((prev) => [...prev, ...nextAttachments])
  }

  const handleImageSelection = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ""

    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const resetDragState = () => {
    dragDepthRef.current = 0
    setIsDragActive(false)
  }

  const handleComposerPaste = async (
    event: ClipboardEvent<HTMLTextAreaElement>,
  ) => {
    const files = getTransferredFiles(event.clipboardData)
    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const handleComposerDragEnter = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      return
    }
    dragDepthRef.current += 1
    setIsDragActive(true)
  }

  const handleComposerDragLeave = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      resetDragState()
      return
    }
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) {
      setIsDragActive(false)
    }
  }

  const handleComposerDragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    event.dataTransfer.dropEffect = canInput ? "copy" : "none"
  }

  const handleComposerDrop = async (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    const files = getTransferredFiles(event.dataTransfer)
    resetDragState()

    if (!canInput || files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const canSubmit =
    canInput && (Boolean(input.trim()) || attachments.length > 0)
  const title = activeThread?.title || fallbackTitle || t("navigation.chat")
  const resolvedNewChatLabel = newChatLabel ?? t("chat.newChat")
  const handleNewChat = onNewChat ?? newChat
  const showThreadEmptyState =
    Boolean(activeThread) &&
    hasAvailableModels &&
    Boolean(defaultModelName) &&
    isGatewayRunning

  return (
    <div className="bg-background flex h-full flex-col">
      <PageHeader
        title={title}
        className={`transition-colors ${
          hasScrolled ? "border-border/70 bg-background/95 border-b" : ""
        }`}
        titleExtra={
          <>
            {activeThread && (
              <Badge variant="secondary" className="hidden md:inline-flex">
                {t(`threads.types.${activeThread.type}`)}
              </Badge>
            )}
            {hasAvailableModels && (
              <ModelSelector
                defaultModelName={defaultModelName}
                apiKeyModels={apiKeyModels}
                oauthModels={oauthModels}
                localModels={localModels}
                onValueChange={handleSetDefault}
              />
            )}
          </>
        }
      >
        <div className="bg-muted/70 hidden items-center gap-2 rounded-full px-3 py-1.5 sm:flex">
          <span className="text-muted-foreground text-sm">
            {t("chat.showAssistantDetails")}
          </span>
          <Select
            value={assistantDetailVisibility}
            onValueChange={(value) =>
              setAssistantDetailVisibility(value as AssistantDetailVisibility)
            }
          >
            <SelectTrigger
              size="sm"
              aria-label={t("chat.showAssistantDetails")}
              className="text-muted-foreground hover:text-foreground focus-visible:border-input h-8 min-w-[104px] border-transparent bg-transparent shadow-none focus-visible:ring-0"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent align="end">
              {assistantDetailVisibilityOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {activeThread && activeThreadActions?.(activeThread)}
        {headerActions}

        <Button
          variant="secondary"
          size="sm"
          onClick={handleNewChat}
          className="h-9 gap-2 rounded-full px-3"
          aria-label={t("chat.newChat")}
        >
          <IconPlus className="size-4" />
          <span className="hidden sm:inline">{resolvedNewChatLabel}</span>
        </Button>

        {showSessionHistory && (
          <SessionHistoryMenu
            sessions={sessions}
            activeSessionId={activeSessionId}
            hasMore={hasMore}
            loadError={loadError}
            loadErrorMessage={loadErrorMessage}
            observerRef={observerRef}
            onOpenChange={(open) => {
              if (open) {
                void loadSessions(true)
              }
            }}
            onSwitchSession={switchSession}
            onDeleteSession={handleDeleteSession}
          />
        )}
      </PageHeader>

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="min-h-0 flex-1 [scrollbar-gutter:stable] overflow-y-auto px-4 py-6 md:px-8 lg:px-16 xl:px-24"
      >
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-7 pb-8">
          {messages.length === 0 && !isTyping && (
            <>
              {showThreadEmptyState && activeThread ? (
                <ThreadChatEmptyState thread={activeThread} />
              ) : (
                <ChatEmptyState
                  hasAvailableModels={hasAvailableModels}
                  defaultModelName={defaultModelName}
                  isConnected={isGatewayRunning}
                />
              )}
            </>
          )}

          {messages.map((msg) => {
            if (
              !shouldShowAssistantMessage(assistantDetailVisibility, msg.kind)
            ) {
              return null
            }

            return (
              <div key={msg.id} className="flex w-full">
                {msg.role === "assistant" ? (
                  <AssistantMessage
                    content={msg.content}
                    attachments={msg.attachments}
                    kind={msg.kind}
                    modelName={msg.modelName}
                    toolCalls={msg.toolCalls}
                    timestamp={msg.timestamp}
                  />
                ) : (
                  <UserMessage
                    content={msg.content}
                    attachments={msg.attachments}
                    timestamp={msg.timestamp}
                  />
                )}
              </div>
            )
          })}

          {isTyping && <TypingIndicator />}
        </div>
      </div>

      <input
        ref={fileInputRef}
        type="file"
        accept={CHAT_IMAGE_ACCEPT}
        multiple
        className="hidden"
        onChange={handleImageSelection}
      />

      <ChatComposer
        input={input}
        attachments={attachments}
        onInputChange={setInput}
        onAddImages={handleAddImages}
        onPaste={handleComposerPaste}
        onDragEnter={handleComposerDragEnter}
        onDragLeave={handleComposerDragLeave}
        onDragOver={handleComposerDragOver}
        onDrop={handleComposerDrop}
        onRemoveAttachment={handleRemoveAttachment}
        onSend={handleSend}
        onContextDetail={() => {
          if (sendMessage({ content: "/context", attachments: [] })) {
            setInput("")
          }
        }}
        inputDisabledReason={inputDisabledReason}
        canSend={canSubmit}
        isDragActive={isDragActive}
        contextUsage={contextUsage}
      />
    </div>
  )
}
