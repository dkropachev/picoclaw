import { IconChevronRight, IconInbox } from "@tabler/icons-react"
import { type UIEvent, useEffect, useRef } from "react"
import { useTranslation } from "react-i18next"

import type { EventView } from "@/api/events"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

import { eventErrorMessage, formatEventDate } from "./event-format"
import { EventScrollRegion, EventStatusBadge } from "./event-ui"

export function EventList({
  events,
  selectedEventID,
  loading,
  error,
  hasMore,
  loadingMore,
  onSelect,
  onRetry,
  onLoadMore,
}: {
  events: EventView[]
  selectedEventID?: string
  loading: boolean
  error: unknown
  hasMore: boolean
  loadingMore: boolean
  onSelect: (eventID: string) => void
  onRetry: () => void
  onLoadMore: () => void
}) {
  const { t } = useTranslation()
  const loadLockedRef = useRef(false)

  useEffect(() => {
    if (!loadingMore) {
      loadLockedRef.current = false
    }
  }, [loadingMore])

  const loadMore = () => {
    if (!hasMore || loadingMore || loadLockedRef.current) {
      return
    }
    loadLockedRef.current = true
    onLoadMore()
  }

  const handleScroll = (event: UIEvent<HTMLDivElement>) => {
    const node = event.currentTarget
    const remaining = node.scrollHeight - node.scrollTop - node.clientHeight
    if (remaining <= 180) {
      loadMore()
    }
  }

  return (
    <section className="border-border bg-card/40 flex min-h-[24rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:min-h-0">
      <div className="border-border flex h-12 shrink-0 items-center justify-between gap-2 border-b px-3">
        <div className="flex min-w-0 items-center gap-2">
          <IconInbox className="text-muted-foreground size-4 shrink-0" />
          <h2 className="truncate text-sm font-medium">
            {t("pages.events.list.title", "Event inbox")}
          </h2>
        </div>
        <Badge variant="outline" className="font-mono">
          {events.length}
        </Badge>
      </div>

      <EventScrollRegion
        label={t("pages.events.list.region", "Events")}
        className="min-h-0 flex-1 overflow-auto p-2"
        onScroll={handleScroll}
      >
        {loading ? (
          <ListMessage>
            {t("pages.events.list.loading", "Loading events…")}
          </ListMessage>
        ) : error && events.length === 0 ? (
          <ListError
            message={eventErrorMessage(
              error,
              t("pages.events.list.error", "Failed to load events."),
            )}
            retryLabel={t("pages.events.list.retry", "Retry")}
            onRetry={onRetry}
          />
        ) : events.length === 0 ? (
          <ListMessage>
            {t("pages.events.list.empty", "No events match these filters.")}
          </ListMessage>
        ) : (
          <div className="flex min-w-0 flex-col gap-1.5">
            {events.map((event) => {
              const selected = selectedEventID === event.id
              return (
                <button
                  key={event.id}
                  type="button"
                  aria-current={selected ? "true" : undefined}
                  onClick={() => onSelect(event.id)}
                  className={cn(
                    "border-border/70 hover:bg-muted/60 focus-visible:border-ring focus-visible:ring-ring/30 grid min-w-0 gap-1.5 rounded-md border px-3 py-2 text-left outline-none focus-visible:ring-2",
                    selected && "bg-accent/70 text-accent-foreground",
                  )}
                >
                  <div className="flex min-w-0 items-center justify-between gap-2">
                    <span className="min-w-0 truncate text-sm font-medium">
                      {event.type}
                    </span>
                    <EventStatusBadge status={event.routing.status} />
                  </div>
                  <div className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs">
                    <span className="min-w-0 truncate font-mono">
                      {event.source}/{event.connector}
                    </span>
                    <span aria-hidden="true">·</span>
                    <span className="shrink-0">
                      {formatEventDate(event.received_at)}
                    </span>
                  </div>
                  <div className="text-muted-foreground flex min-w-0 items-center gap-2 font-mono text-[11px]">
                    <span className="min-w-0 flex-1 truncate">{event.id}</span>
                    <IconChevronRight
                      aria-hidden="true"
                      className="size-3.5 shrink-0"
                    />
                  </div>
                </button>
              )
            })}

            {error ? (
              <ListError
                compact
                message={eventErrorMessage(
                  error,
                  t("pages.events.list.error", "Failed to load events."),
                )}
                retryLabel={t("pages.events.list.retry", "Retry")}
                onRetry={onRetry}
              />
            ) : null}

            {hasMore ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={loadingMore}
                onClick={loadMore}
                className="mt-1 w-full"
              >
                {loadingMore
                  ? t("pages.events.list.loading_more", "Loading more…")
                  : t("pages.events.list.load_more", "Load more")}
              </Button>
            ) : null}
          </div>
        )}
      </EventScrollRegion>
    </section>
  )
}

function ListMessage({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground flex min-h-40 items-center justify-center px-4 text-center text-sm">
      {children}
    </div>
  )
}

function ListError({
  message,
  retryLabel,
  onRetry,
  compact,
}: {
  message: string
  retryLabel: string
  onRetry: () => void
  compact?: boolean
}) {
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-center justify-center gap-2 px-4 text-center",
        compact ? "py-3" : "min-h-40",
      )}
    >
      <p className="text-destructive max-w-full text-sm break-words">
        {message}
      </p>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>
        {retryLabel}
      </Button>
    </div>
  )
}
