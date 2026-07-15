import {
  IconCode,
  IconGitPullRequest,
  IconSearch,
  IconTag,
  IconTrash,
} from "@tabler/icons-react"
import dayjs from "dayjs"
import type { ComponentType } from "react"

import type { ThreadSummary, ThreadType } from "@/api/threads"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const TYPE_ICONS: Record<ThreadType, ComponentType<{ className?: string }>> = {
  general: IconTag,
  coding: IconCode,
  reviewing: IconGitPullRequest,
  investigating: IconSearch,
}

function threadTypeLabel(type: ThreadType): string {
  switch (type) {
    case "coding":
      return "Coding"
    case "reviewing":
      return "Reviewing"
    case "investigating":
      return "Investigating"
    default:
      return "General"
  }
}

export function ThreadTile({
  thread,
  active = false,
  compact = false,
  onOpen,
  onDrop,
  dropLabel,
}: {
  thread: ThreadSummary
  active?: boolean
  compact?: boolean
  onOpen: (threadId: string) => void
  onDrop?: (thread: ThreadSummary) => void
  dropLabel?: string
}) {
  const Icon = TYPE_ICONS[thread.type] ?? IconTag
  const openSessionId = thread.ui_session_id || thread.id
  const resolvedDropLabel = dropLabel ?? "Drop thread"
  const contextEntries = Object.entries(thread.context ?? {}).filter(
    ([key, value]) => key && value,
  )

  return (
    <div
      className={cn(
        "border-border/50 bg-card/70 hover:bg-accent/70 relative flex rounded-lg border text-left shadow-none transition-colors",
        active && "border-primary/35 bg-accent text-accent-foreground",
      )}
    >
      <button
        type="button"
        className={cn(
          "flex min-w-0 flex-1 flex-col gap-2 p-3 text-left",
          compact && "p-2.5",
          onDrop && "pr-10",
        )}
        onClick={() => onOpen(openSessionId)}
      >
        <div className="flex min-w-0 items-start justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            <Icon className="text-muted-foreground size-4 shrink-0" />
            <span className="line-clamp-1 min-w-0 text-sm font-medium">
              {thread.title}
            </span>
          </div>
          <Badge
            variant="secondary"
            className="h-5 shrink-0 px-1.5 text-[10px]"
          >
            {threadTypeLabel(thread.type)}
          </Badge>
        </div>

        {!compact && (
          <p className="text-muted-foreground line-clamp-2 text-xs leading-relaxed whitespace-normal">
            {thread.preview}
          </p>
        )}

        {contextEntries.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {contextEntries.slice(0, compact ? 2 : 4).map(([key, value]) => (
              <span
                key={`${thread.id}-${key}`}
                className="bg-muted text-muted-foreground max-w-full truncate rounded px-1.5 py-0.5 text-[10px]"
              >
                {key}:{value}
              </span>
            ))}
          </div>
        )}

        <div className="text-muted-foreground/75 flex items-center gap-1.5 text-[11px]">
          <span>{thread.message_count} messages</span>
          <span>-</span>
          <span>{dayjs(thread.updated).fromNow()}</span>
        </div>
      </button>
      {onDrop ? (
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="text-muted-foreground hover:text-destructive absolute top-1.5 right-1.5 size-8 opacity-80"
          title={resolvedDropLabel}
          aria-label={resolvedDropLabel}
          onClick={(event) => {
            event.stopPropagation()
            onDrop(thread)
          }}
        >
          <IconTrash className="size-4" />
        </Button>
      ) : null}
    </div>
  )
}
