import {
  IconBolt,
  IconRotateClockwise,
  IconRoute,
  IconUser,
} from "@tabler/icons-react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { useMemo } from "react"
import { useTranslation } from "react-i18next"

import {
  type DispatchPage,
  type DispatchView,
  type EventActor,
  type EventSubject,
  type EventView,
  getEvent,
  listEventDispatches,
} from "@/api/events"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import {
  eventErrorMessage,
  formatEventBytes,
  formatEventDate,
} from "./event-format"
import { EventPayloadPanel } from "./event-payload-panel"
import {
  EventMeta,
  EventPanel,
  EventScrollRegion,
  EventStatusBadge,
} from "./event-ui"

const DISPATCH_PAGE_SIZE = 40

export function EventDetail({
  eventID,
  onReplay,
}: {
  eventID?: string
  onReplay: (event: EventView) => void
}) {
  const { t } = useTranslation()

  const detailQuery = useQuery({
    queryKey: ["events", "detail", eventID],
    queryFn: () => getEvent(eventID!),
    enabled: Boolean(eventID),
    staleTime: 1000,
  })

  const dispatchQuery = useInfiniteQuery({
    queryKey: ["events", "dispatches", eventID],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listEventDispatches({
        eventID,
        limit: DISPATCH_PAGE_SIZE,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (lastPage: DispatchPage) =>
      lastPage.next_cursor || undefined,
    enabled: Boolean(eventID),
  })

  const dispatches = useMemo(
    () =>
      deduplicateDispatches(
        dispatchQuery.data?.pages.flatMap((page) => page.dispatches) ?? [],
      ),
    [dispatchQuery.data?.pages],
  )

  const detail = detailQuery.data

  return (
    <section className="border-border bg-card/40 flex min-h-[32rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:min-h-0">
      <div className="border-border flex min-h-12 shrink-0 items-center justify-between gap-3 border-b px-3 py-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="min-w-0 truncate text-sm font-medium">
              {detail?.type ?? t("pages.events.detail.title", "Event detail")}
            </h2>
            {detail ? (
              <EventStatusBadge status={detail.routing.status} />
            ) : null}
          </div>
          <p className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">
            {eventID ??
              t("pages.events.detail.select_prompt", "Select an event")}
          </p>
        </div>
        {detail ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="min-w-0 shrink-0"
            onClick={() => onReplay(detail)}
          >
            <IconRotateClockwise className="size-4" />
            <span className="truncate">
              {t("pages.events.replay.action", "Replay")}
            </span>
          </Button>
        ) : null}
      </div>

      <EventScrollRegion
        label={t("pages.events.detail.region", "Event detail")}
        className="min-h-0 flex-1 overflow-auto p-3"
      >
        {!eventID ? (
          <DetailMessage>
            {t(
              "pages.events.detail.select_prompt",
              "Select an event to inspect it.",
            )}
          </DetailMessage>
        ) : detailQuery.isPending ? (
          <DetailMessage>
            {t("pages.events.detail.loading", "Loading event detail…")}
          </DetailMessage>
        ) : detailQuery.error || !detail ? (
          <DetailError
            message={eventErrorMessage(
              detailQuery.error,
              t("pages.events.detail.error", "Failed to load event detail."),
            )}
            retryLabel={t("pages.events.detail.retry", "Retry")}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : (
          <div className="grid min-w-0 gap-3 xl:grid-cols-2">
            <EventSummary event={detail} />
            <RoutingSummary event={detail} />
            {detail.actor ? <ActorPanel actor={detail.actor} /> : null}
            {detail.subject ? <SubjectPanel subject={detail.subject} /> : null}
            {detail.attributes && Object.keys(detail.attributes).length > 0 ? (
              <AttributesPanel attributes={detail.attributes} />
            ) : null}
            <DispatchPanel
              dispatches={dispatches}
              loading={dispatchQuery.isPending}
              error={dispatchQuery.error}
              hasMore={Boolean(dispatchQuery.hasNextPage)}
              loadingMore={dispatchQuery.isFetchingNextPage}
              onRetry={() => {
                if (dispatchQuery.isFetchNextPageError) {
                  void dispatchQuery.fetchNextPage()
                } else {
                  void dispatchQuery.refetch()
                }
              }}
              onLoadMore={() => void dispatchQuery.fetchNextPage()}
            />
            <EventPayloadPanel
              eventID={detail.id}
              payloadBytes={detail.payload_bytes}
            />
          </div>
        )}
      </EventScrollRegion>
    </section>
  )
}

function EventSummary({ event }: { event: EventView }) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.detail.summary", "Summary")}
      className="xl:col-span-2"
    >
      <dl className="grid min-w-0 grid-cols-2 gap-3 lg:grid-cols-4">
        <EventMeta
          label={t("pages.events.detail.event_id", "Event ID")}
          value={event.id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.detail.source", "Source")}
          value={event.source}
          mono
        />
        <EventMeta
          label={t("pages.events.detail.connector", "Connector")}
          value={event.connector}
          mono
        />
        <EventMeta
          label={t("pages.events.detail.type", "Type")}
          value={event.type}
          mono
        />
        <EventMeta
          label={t("pages.events.detail.received", "Received")}
          value={formatEventDate(event.received_at)}
        />
        <EventMeta
          label={t("pages.events.detail.occurred", "Occurred")}
          value={formatEventDate(event.occurred_at)}
        />
        <EventMeta
          label={t("pages.events.detail.payload_size", "Payload size")}
          value={formatEventBytes(event.payload_bytes)}
        />
        <EventMeta
          label={t("pages.events.detail.replay_of", "Replay of")}
          value={event.replay_of}
          mono
          breakAll
        />
      </dl>
    </EventPanel>
  )
}

function RoutingSummary({ event }: { event: EventView }) {
  const { t } = useTranslation()
  const routing = event.routing
  return (
    <EventPanel
      title={t("pages.events.detail.routing", "Routing")}
      titleExtra={<IconRoute className="text-muted-foreground size-4" />}
    >
      <dl className="grid grid-cols-2 gap-3">
        <div className="min-w-0">
          <dt className="text-muted-foreground text-xs">
            {t("pages.events.detail.status", "Status")}
          </dt>
          <dd className="mt-1">
            <EventStatusBadge status={routing.status} />
          </dd>
        </div>
        <EventMeta
          label={t("pages.events.detail.attempts", "Attempts")}
          value={routing.attempts}
        />
        <EventMeta
          label={t("pages.events.detail.available_at", "Available")}
          value={formatEventDate(routing.available_at)}
        />
        <EventMeta
          label={t("pages.events.detail.updated_at", "Updated")}
          value={formatEventDate(routing.updated_at)}
        />
        <EventMeta
          label={t("pages.events.detail.lease_until", "Lease until")}
          value={formatEventDate(routing.lease_until)}
        />
      </dl>
      {routing.last_error ? (
        <div className="bg-destructive/10 text-destructive mt-3 rounded-md px-3 py-2 text-sm break-words">
          <div className="mb-1 text-xs font-medium">
            {t("pages.events.detail.last_error", "Last error")}
          </div>
          {routing.last_error}
        </div>
      ) : null}
    </EventPanel>
  )
}

function ActorPanel({ actor }: { actor: EventActor }) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.detail.actor", "Actor")}
      titleExtra={<IconUser className="text-muted-foreground size-4" />}
    >
      <dl className="grid grid-cols-2 gap-3">
        <EventMeta
          label={t("pages.events.detail.actor_id", "Actor ID")}
          value={actor.id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.detail.actor_type", "Actor type")}
          value={actor.type}
        />
        <EventMeta
          label={t("pages.events.detail.actor_name", "Display name")}
          value={actor.display_name}
        />
      </dl>
      {actor.attributes && Object.keys(actor.attributes).length > 0 ? (
        <AttributeList attributes={actor.attributes} className="mt-3" />
      ) : null}
    </EventPanel>
  )
}

function SubjectPanel({ subject }: { subject: EventSubject }) {
  const { t } = useTranslation()
  return (
    <EventPanel title={t("pages.events.detail.subject", "Subject")}>
      <dl className="grid grid-cols-2 gap-3">
        <EventMeta
          label={t("pages.events.detail.subject_id", "Subject ID")}
          value={subject.id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.detail.subject_type", "Subject type")}
          value={subject.type}
        />
        <EventMeta
          label={t("pages.events.detail.subject_name", "Name")}
          value={subject.name}
        />
        <EventMeta
          label={t("pages.events.detail.subject_url", "URL")}
          value={subject.url}
          mono
          breakAll
        />
      </dl>
      {subject.attributes && Object.keys(subject.attributes).length > 0 ? (
        <AttributeList attributes={subject.attributes} className="mt-3" />
      ) : null}
    </EventPanel>
  )
}

function AttributesPanel({
  attributes,
}: {
  attributes: Record<string, string>
}) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.detail.attributes", "Attributes")}
      className="xl:col-span-2"
    >
      <AttributeList attributes={attributes} />
    </EventPanel>
  )
}

function AttributeList({
  attributes,
  className,
}: {
  attributes: Record<string, string>
  className?: string
}) {
  const entries = Object.entries(attributes).sort(([left], [right]) =>
    left.localeCompare(right),
  )
  return (
    <dl className={`grid min-w-0 gap-2 sm:grid-cols-2 ${className ?? ""}`}>
      {entries.map(([key, value]) => (
        <div
          key={key}
          className="border-border/70 min-w-0 rounded-md border px-2.5 py-2"
        >
          <dt className="text-muted-foreground font-mono text-[11px] break-all">
            {key}
          </dt>
          <dd className="mt-0.5 text-sm break-all">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function DispatchPanel({
  dispatches,
  loading,
  error,
  hasMore,
  loadingMore,
  onRetry,
  onLoadMore,
}: {
  dispatches: DispatchView[]
  loading: boolean
  error: unknown
  hasMore: boolean
  loadingMore: boolean
  onRetry: () => void
  onLoadMore: () => void
}) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.dispatches.title", "Workflow dispatches")}
      titleExtra={
        <Badge variant="outline" className="font-mono">
          {dispatches.length}
        </Badge>
      }
      className="xl:col-span-2"
    >
      {loading ? (
        <DetailMessage compact>
          {t("pages.events.dispatches.loading", "Loading dispatches…")}
        </DetailMessage>
      ) : error && dispatches.length === 0 ? (
        <DetailError
          compact
          message={eventErrorMessage(
            error,
            t(
              "pages.events.dispatches.error",
              "Failed to load workflow dispatches.",
            ),
          )}
          retryLabel={t("pages.events.dispatches.retry", "Retry")}
          onRetry={onRetry}
        />
      ) : dispatches.length === 0 ? (
        <DetailMessage compact>
          {t(
            "pages.events.dispatches.empty",
            "No workflow dispatches for this event.",
          )}
        </DetailMessage>
      ) : (
        <EventScrollRegion
          label={t(
            "pages.events.dispatches.region",
            "Workflow dispatch history",
          )}
          className="max-h-96 overflow-auto"
        >
          <div className="grid min-w-0 gap-2 pr-1">
            {dispatches.map((dispatch) => (
              <DispatchRow key={dispatch.id} dispatch={dispatch} />
            ))}
            {error ? (
              <DetailError
                compact
                message={eventErrorMessage(
                  error,
                  t(
                    "pages.events.dispatches.error",
                    "Failed to load workflow dispatches.",
                  ),
                )}
                retryLabel={t("pages.events.dispatches.retry", "Retry")}
                onRetry={onRetry}
              />
            ) : null}
            {hasMore ? (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={loadingMore}
                onClick={onLoadMore}
                className="w-full"
              >
                {loadingMore
                  ? t("pages.events.dispatches.loading_more", "Loading more…")
                  : t("pages.events.dispatches.load_more", "Load more")}
              </Button>
            ) : null}
          </div>
        </EventScrollRegion>
      )}
    </EventPanel>
  )
}

function DispatchRow({ dispatch }: { dispatch: DispatchView }) {
  const { t } = useTranslation()
  return (
    <article className="border-border/70 grid min-w-0 gap-2 rounded-md border p-3">
      <div className="flex min-w-0 items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <IconBolt className="text-muted-foreground size-4 shrink-0" />
          <span className="min-w-0 truncate font-mono text-xs">
            {dispatch.workflow_ref}
          </span>
        </div>
        <EventStatusBadge status={dispatch.status} />
      </div>
      <dl className="grid min-w-0 grid-cols-2 gap-2 md:grid-cols-4">
        <EventMeta
          label={t("pages.events.dispatches.workflow", "Workflow")}
          value={dispatch.workflow_ref}
          mono
        />
        <EventMeta
          label={t("pages.events.dispatches.run", "Run")}
          value={dispatch.run_id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.dispatches.attempts", "Attempts")}
          value={dispatch.attempts}
        />
        <EventMeta
          label={t("pages.events.dispatches.created", "Created")}
          value={formatEventDate(dispatch.created_at)}
        />
        <EventMeta
          label={t("pages.events.dispatches.updated", "Updated")}
          value={formatEventDate(dispatch.updated_at)}
        />
        <EventMeta
          label={t("pages.events.dispatches.available", "Available")}
          value={formatEventDate(dispatch.available_at)}
        />
        <EventMeta
          label={t("pages.events.dispatches.finished", "Finished")}
          value={formatEventDate(dispatch.finished_at)}
        />
      </dl>
      {dispatch.last_error ? (
        <div className="bg-destructive/10 text-destructive rounded-md px-3 py-2 text-sm break-words">
          <div className="mb-1 text-xs font-medium">
            {t("pages.events.dispatches.last_error", "Last error")}
          </div>
          {dispatch.last_error}
        </div>
      ) : null}
    </article>
  )
}

function DetailMessage({
  children,
  compact,
}: {
  children: React.ReactNode
  compact?: boolean
}) {
  return (
    <div
      className={`text-muted-foreground flex items-center justify-center px-4 text-center text-sm ${
        compact ? "min-h-20" : "min-h-48"
      }`}
    >
      {children}
    </div>
  )
}

function DetailError({
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
      className={`flex flex-col items-center justify-center gap-2 px-4 text-center ${
        compact ? "min-h-20 py-2" : "min-h-48"
      }`}
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

function deduplicateDispatches(dispatches: DispatchView[]): DispatchView[] {
  return Array.from(
    new Map(dispatches.map((dispatch) => [dispatch.id, dispatch])).values(),
  )
}
