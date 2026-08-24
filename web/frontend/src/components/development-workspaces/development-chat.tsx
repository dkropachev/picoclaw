import {
  IconAlertTriangle,
  IconHelpCircle,
  IconLoader2,
  IconRoute,
  IconSend,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, type ReactNode, useState } from "react"

import {
  type DevelopmentConversationMessage,
  type DevelopmentConversationPage,
  type DevelopmentMessageMode,
  DevelopmentWorkspaceAPIError,
  createDevelopmentRequestID,
  getDevelopmentConversation,
  sendDevelopmentMessage,
} from "@/api/development-workspaces"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

export function DevelopmentChat({
  workspaceID,
  candidateRevision,
}: {
  workspaceID: string
  candidateRevision?: string
}) {
  const queryClient = useQueryClient()
  const queryKey = ["development-workspace", workspaceID, "conversation"]
  const [mode, setMode] = useState<DevelopmentMessageMode>("steer")
  const [content, setContent] = useState("")
  const [error, setError] = useState("")
  const conversationQuery = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      getDevelopmentConversation(workspaceID, {}, signal),
    refetchInterval: 3_000,
  })
  const messageMutation = useMutation({
    mutationFn: ({
      content: message,
      mode: messageMode,
      revision,
    }: {
      content: string
      mode: DevelopmentMessageMode
      revision: number
    }) =>
      sendDevelopmentMessage(workspaceID, {
        mode: messageMode,
        content: message,
        expected_revision: revision,
        request_id: createDevelopmentRequestID(),
        ...(candidateRevision ? { candidate_revision: candidateRevision } : {}),
      }),
    onSuccess: (conversation) => {
      queryClient.setQueryData<DevelopmentConversationPage>(
        queryKey,
        conversation,
      )
      setContent("")
      setError("")
      if (mode === "steer") {
        void queryClient.invalidateQueries({
          queryKey: ["development-workspace", workspaceID],
          exact: true,
        })
      }
    },
    onError: (cause) => {
      setError(
        cause instanceof DevelopmentWorkspaceAPIError
          ? cause.message
          : "Message could not be sent.",
      )
      void conversationQuery.refetch()
    },
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const message = content.trim()
    if (!message || !conversationQuery.data) return
    setError("")
    messageMutation.mutate({
      content: message,
      mode,
      revision: conversationQuery.data.revision,
    })
  }

  return (
    <section
      aria-label="Development chat"
      className="border-border bg-card flex min-h-0 flex-col rounded-lg border"
      data-testid="development-chat"
    >
      <div className="border-border flex items-center justify-between border-b px-3 py-2">
        <h2 className="text-sm font-medium">Development chat</h2>
        {conversationQuery.isFetching && !conversationQuery.isPending && (
          <IconLoader2
            className="text-muted-foreground size-4 animate-spin"
            aria-label="Refreshing conversation"
          />
        )}
      </div>

      <div
        className="flex min-h-48 flex-1 flex-col gap-3 overflow-auto p-3 lg:min-h-0"
        aria-live="polite"
      >
        {conversationQuery.isPending ? (
          <p className="text-muted-foreground my-auto text-center text-sm">
            Loading conversation…
          </p>
        ) : conversationQuery.isError ? (
          <p
            role="alert"
            className="text-destructive my-auto text-center text-sm"
          >
            Conversation could not be loaded.
          </p>
        ) : conversationQuery.data.messages.length === 0 ? (
          <p className="text-muted-foreground my-auto text-center text-sm">
            Ask about the code or steer the next implementation step.
          </p>
        ) : (
          conversationQuery.data.messages.map((message) => (
            <ChatMessage key={message.id} message={message} />
          ))
        )}
      </div>

      <form onSubmit={submit} className="border-border space-y-2 border-t p-3">
        <div className="bg-muted grid grid-cols-2 gap-1 rounded-lg p-1">
          <ModeButton
            active={mode === "ask"}
            icon={<IconHelpCircle />}
            label="Ask"
            onClick={() => setMode("ask")}
          />
          <ModeButton
            active={mode === "steer"}
            icon={<IconRoute />}
            label="Steer"
            onClick={() => setMode("steer")}
          />
        </div>
        <p className="text-muted-foreground text-xs">
          {mode === "ask"
            ? candidateRevision
              ? "Read-only answer fenced to the exact current candidate."
              : "Read-only answer from current workspace evidence."
            : "Queued at the next safe boundary; scope expansion will require approval."}
        </p>
        <Textarea
          value={content}
          onChange={(event) => setContent(event.target.value)}
          rows={3}
          aria-label={
            mode === "ask" ? "Ask development AI" : "Steer development AI"
          }
          placeholder={
            mode === "ask"
              ? "Ask about a decision or code change…"
              : "Describe what should change next…"
          }
        />
        {error && (
          <p
            role="alert"
            className="text-destructive flex items-center gap-1 text-xs"
          >
            <IconAlertTriangle className="size-3.5" />
            {error}
          </p>
        )}
        <div className="flex justify-end">
          <Button
            type="submit"
            size="sm"
            disabled={
              !content.trim() ||
              !conversationQuery.data ||
              messageMutation.isPending
            }
          >
            {messageMutation.isPending ? (
              <IconLoader2 className="animate-spin" />
            ) : (
              <IconSend />
            )}
            Send {mode === "ask" ? "question" : "steering"}
          </Button>
        </div>
      </form>
    </section>
  )
}

function ModeButton({
  active,
  icon,
  label,
  onClick,
}: {
  active: boolean
  icon: ReactNode
  label: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "focus-visible:ring-ring flex h-8 items-center justify-center gap-1.5 rounded-md text-sm font-medium focus-visible:ring-2 focus-visible:outline-none [&>svg]:size-4",
        active
          ? "bg-background text-foreground shadow-sm"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {icon}
      {label}
    </button>
  )
}

function ChatMessage({ message }: { message: DevelopmentConversationMessage }) {
  const fromUser = message.role === "user"
  const content =
    message.role === "system" &&
    message.mode === "steer" &&
    message.status === "applied" &&
    message.content.startsWith("applied:")
      ? "Steering applied to the implementation candidate."
      : message.content
  return (
    <article
      className={cn(
        "max-w-[92%] space-y-1 rounded-lg px-3 py-2 text-sm",
        fromUser
          ? "bg-primary text-primary-foreground ml-auto"
          : "bg-muted text-foreground mr-auto",
      )}
    >
      <div className="flex flex-wrap items-center gap-1">
        <span className="text-xs font-medium">
          {fromUser ? "You" : message.role === "assistant" ? "AI" : "System"}
        </span>
        {message.mode && (
          <Badge
            variant={fromUser ? "secondary" : "outline"}
            className="h-4 px-1.5 text-[0.65rem]"
          >
            {message.mode === "ask" ? "Ask" : "Steer"}
          </Badge>
        )}
        <span
          className={cn(
            "text-[0.65rem]",
            fromUser ? "text-primary-foreground/75" : "text-muted-foreground",
          )}
        >
          {message.status.replaceAll("_", " ")}
        </span>
      </div>
      <p className="break-words whitespace-pre-wrap">{content}</p>
    </article>
  )
}
