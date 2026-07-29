import { IconRefresh } from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type EventListParams,
  type EventPage,
  type EventRoutingStatus,
  type EventView,
  listEvents,
  replayEvent,
} from "@/api/events"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import { EventDetail } from "./event-detail"
import { EventFilterBar, type EventFilterValues } from "./event-filter-bar"
import { eventErrorMessage } from "./event-format"
import { EventList } from "./event-list"
import { ReplayEventDialog } from "./replay-event-dialog"

const EVENT_PAGE_SIZE = 40

export interface EventsRouteSearch {
  source?: string
  connector?: string
  type?: string
  routing_status?: EventRoutingStatus
  event?: string
}

export function EventsPage({
  search,
  onSearchChange,
}: {
  search: EventsRouteSearch
  onSearchChange: (search: EventsRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const replayInFlightRef = useRef(false)
  const [replayTarget, setReplayTarget] = useState<EventView | null>(null)

  const filters = useMemo<EventFilterValues>(
    () => ({
      source: search.source ?? "",
      connector: search.connector ?? "",
      type: search.type ?? "",
      routingStatus: search.routing_status ?? "",
    }),
    [search.connector, search.routing_status, search.source, search.type],
  )

  const queryFilters = useMemo<EventListParams>(
    () => ({
      ...(filters.source ? { source: filters.source } : {}),
      ...(filters.connector ? { connector: filters.connector } : {}),
      ...(filters.type ? { type: filters.type } : {}),
      ...(filters.routingStatus
        ? { routingStatus: filters.routingStatus }
        : {}),
      limit: EVENT_PAGE_SIZE,
    }),
    [filters],
  )

  const eventQuery = useInfiniteQuery({
    queryKey: ["events", "list", queryFilters],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listEvents({
        ...queryFilters,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (lastPage: EventPage) =>
      lastPage.next_cursor || undefined,
  })

  const events = useMemo(
    () =>
      deduplicateEvents(
        eventQuery.data?.pages.flatMap((page) => page.events) ?? [],
      ),
    [eventQuery.data?.pages],
  )

  useEffect(() => {
    if (!search.event && events.length > 0) {
      onSearchChange({ ...search, event: events[0].id }, true)
    }
  }, [events, onSearchChange, search])

  const replayMutation = useMutation({
    mutationFn: (eventID: string) => replayEvent(eventID),
    retry: false,
    onSuccess: (result) => {
      queryClient.setQueryData(
        ["events", "detail", result.event.id],
        result.event,
      )
      setReplayTarget(null)
      onSearchChange({ ...search, event: result.event.id })
      void queryClient.invalidateQueries({
        queryKey: ["events", "list"],
      })
      void queryClient.invalidateQueries({
        queryKey: ["events", "dispatches", result.event.id],
      })
    },
    onSettled: () => {
      replayInFlightRef.current = false
    },
  })

  const selectEvent = useCallback(
    (eventID: string) => {
      onSearchChange({ ...search, event: eventID })
    },
    [onSearchChange, search],
  )

  const applyFilters = useCallback(
    (next: EventFilterValues) => {
      onSearchChange(
        {
          ...(next.source ? { source: next.source } : {}),
          ...(next.connector ? { connector: next.connector } : {}),
          ...(next.type ? { type: next.type } : {}),
          ...(next.routingStatus ? { routing_status: next.routingStatus } : {}),
        },
        true,
      )
    },
    [onSearchChange],
  )

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["events", "list"] })
    void queryClient.invalidateQueries({ queryKey: ["events", "detail"] })
    void queryClient.invalidateQueries({
      queryKey: ["events", "dispatches"],
    })
  }

  const retryEvents = () => {
    if (eventQuery.isFetchNextPageError) {
      void eventQuery.fetchNextPage()
    } else {
      void eventQuery.refetch()
    }
  }

  const confirmReplay = () => {
    if (
      !replayTarget ||
      replayInFlightRef.current ||
      replayMutation.isPending
    ) {
      return
    }
    replayInFlightRef.current = true
    replayMutation.mutate(replayTarget.id)
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.events.title", "Events")}
        titleExtra={
          <Badge variant="secondary" className="font-mono text-[11px]">
            {t("pages.events.loaded_count", "{{count}} loaded", {
              count: events.length,
            })}
          </Badge>
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon"
          disabled={eventQuery.isFetching}
          onClick={refresh}
          title={t("pages.events.refresh", "Refresh events")}
          aria-label={t("pages.events.refresh", "Refresh events")}
        >
          <IconRefresh className="size-4" />
        </Button>
      </PageHeader>

      <EventFilterBar
        filters={filters}
        onApply={applyFilters}
        onReset={() => onSearchChange({}, true)}
      />

      <div className="min-h-0 flex-1 overflow-auto p-3 lg:overflow-hidden lg:p-4">
        <div className="flex min-h-full min-w-0 flex-col gap-3 lg:grid lg:h-full lg:min-h-0 lg:grid-cols-[minmax(300px,0.85fr)_minmax(0,1.35fr)]">
          <EventList
            events={events}
            selectedEventID={search.event}
            loading={eventQuery.isPending}
            error={eventQuery.error}
            hasMore={Boolean(eventQuery.hasNextPage)}
            loadingMore={eventQuery.isFetchingNextPage}
            onSelect={selectEvent}
            onRetry={retryEvents}
            onLoadMore={() => void eventQuery.fetchNextPage()}
          />
          <EventDetail
            eventID={search.event}
            onReplay={(event) => {
              replayMutation.reset()
              setReplayTarget(event)
            }}
          />
        </div>
      </div>

      <ReplayEventDialog
        target={replayTarget}
        pending={replayMutation.isPending}
        error={
          replayMutation.error
            ? eventErrorMessage(
                replayMutation.error,
                t(
                  "pages.events.replay.error",
                  "The event could not be replayed.",
                ),
              )
            : undefined
        }
        onOpenChange={(open) => {
          if (!open) {
            setReplayTarget(null)
            replayMutation.reset()
          }
        }}
        onConfirm={confirmReplay}
      />
    </div>
  )
}

function deduplicateEvents(events: EventView[]): EventView[] {
  return Array.from(new Map(events.map((event) => [event.id, event])).values())
}
