import {
  IconActivity,
  IconInbox,
  IconRefresh,
  IconSettings,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type DispatchListParams,
  type DispatchPage,
  type DispatchStatus,
  type DispatchView,
  type EventListParams,
  type EventPage,
  type EventRoutingStatus,
  type EventView,
  listEventDispatches,
  listEvents,
  replayEvent,
} from "@/api/events"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import { DispatchDetail } from "./dispatch-detail"
import {
  DispatchFilterBar,
  type DispatchFilterValues,
} from "./dispatch-filter-bar"
import { DispatchList } from "./dispatch-list"
import { EventDetail } from "./event-detail"
import { EventFilterBar, type EventFilterValues } from "./event-filter-bar"
import { eventErrorMessage } from "./event-format"
import { EventList } from "./event-list"
import { ReplayEventDialog } from "./replay-event-dialog"

const EVENT_PAGE_SIZE = 40
const DISPATCH_PAGE_SIZE = 40

export type EventsView = "events" | "dispatches"

export interface EventsRouteSearch {
  view?: "dispatches"
  source?: string
  connector?: string
  type?: string
  routing_status?: EventRoutingStatus
  event?: string
  dispatch_event?: string
  workflow?: string
  dispatch_status?: DispatchStatus
  dispatch?: string
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
  const [refreshing, setRefreshing] = useState(false)
  const activeView: EventsView =
    search.view === "dispatches" ? "dispatches" : "events"

  const setView = (view: EventsView) => {
    const next = { ...search }
    if (view === "dispatches") {
      next.view = "dispatches"
    } else {
      delete next.view
    }
    onSearchChange(next)
  }

  const refresh = async () => {
    if (refreshing) {
      return
    }
    setRefreshing(true)
    try {
      await queryClient.invalidateQueries({ queryKey: ["events"] })
    } finally {
      setRefreshing(false)
    }
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader
        title={t("pages.events.title", "Events")}
        titleExtra={
          <Badge variant="secondary" className="font-mono text-[11px]">
            {activeView === "events"
              ? t("pages.events.views.events", "Events")
              : t("pages.events.views.dispatches", "Dispatches")}
          </Badge>
        }
      >
        <Button type="button" variant="outline" asChild>
          <Link to="/event-sources">
            <IconSettings className="size-4" />
            {t("pages.events.event_sources", "Event sources")}
          </Link>
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          disabled={refreshing}
          onClick={() => void refresh()}
          title={t("pages.events.refresh", "Refresh events")}
          aria-label={t("pages.events.refresh", "Refresh events")}
        >
          <IconRefresh className="size-4" />
        </Button>
      </PageHeader>

      <div
        role="tablist"
        aria-label={t("pages.events.views.label", "Operational view")}
        className="border-border flex shrink-0 gap-1 border-b px-3 pt-2"
      >
        <Button
          id="events-view-tab"
          type="button"
          role="tab"
          aria-selected={activeView === "events"}
          aria-controls="events-view-panel"
          variant={activeView === "events" ? "secondary" : "ghost"}
          size="sm"
          className="rounded-b-none"
          onClick={() => setView("events")}
        >
          <IconInbox className="size-4" />
          {t("pages.events.views.events", "Events")}
        </Button>
        <Button
          id="dispatches-view-tab"
          type="button"
          role="tab"
          aria-selected={activeView === "dispatches"}
          aria-controls="dispatches-view-panel"
          variant={activeView === "dispatches" ? "secondary" : "ghost"}
          size="sm"
          className="rounded-b-none"
          onClick={() => setView("dispatches")}
        >
          <IconActivity className="size-4" />
          {t("pages.events.views.dispatches", "Dispatches")}
        </Button>
      </div>

      {activeView === "events" ? (
        <div
          id="events-view-panel"
          role="tabpanel"
          aria-labelledby="events-view-tab"
          className="flex min-h-0 flex-1 flex-col"
        >
          <EventInboxView search={search} onSearchChange={onSearchChange} />
        </div>
      ) : (
        <div
          id="dispatches-view-panel"
          role="tabpanel"
          aria-labelledby="dispatches-view-tab"
          className="flex min-h-0 flex-1 flex-col"
        >
          <DispatchInboxView search={search} onSearchChange={onSearchChange} />
        </div>
      )}
    </div>
  )
}

function EventInboxView({
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
        queryKey: ["events", "dispatches"],
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
          ...withoutEventViewState(search),
          ...(next.source ? { source: next.source } : {}),
          ...(next.connector ? { connector: next.connector } : {}),
          ...(next.type ? { type: next.type } : {}),
          ...(next.routingStatus ? { routing_status: next.routingStatus } : {}),
        },
        true,
      )
    },
    [onSearchChange, search],
  )

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
    <>
      <EventFilterBar
        filters={filters}
        onApply={applyFilters}
        onReset={() => onSearchChange(withoutEventViewState(search), true)}
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
    </>
  )
}

function DispatchInboxView({
  search,
  onSearchChange,
}: {
  search: EventsRouteSearch
  onSearchChange: (search: EventsRouteSearch, replace?: boolean) => void
}) {
  const filters = useMemo<DispatchFilterValues>(
    () => ({
      eventID: search.dispatch_event ?? "",
      workflowRef: search.workflow ?? "",
      status: search.dispatch_status ?? "",
    }),
    [search.dispatch_event, search.dispatch_status, search.workflow],
  )

  const queryFilters = useMemo<DispatchListParams>(
    () => ({
      ...(filters.eventID ? { eventID: filters.eventID } : {}),
      ...(filters.workflowRef ? { workflowRef: filters.workflowRef } : {}),
      ...(filters.status ? { status: filters.status } : {}),
      limit: DISPATCH_PAGE_SIZE,
    }),
    [filters],
  )

  const dispatchQuery = useInfiniteQuery({
    queryKey: ["events", "dispatches", "list", queryFilters],
    initialPageParam: "",
    queryFn: ({ pageParam }) =>
      listEventDispatches({
        ...queryFilters,
        cursor: pageParam || undefined,
      }),
    getNextPageParam: (lastPage: DispatchPage) =>
      lastPage.next_cursor || undefined,
  })

  const dispatches = useMemo(
    () =>
      deduplicateDispatches(
        dispatchQuery.data?.pages.flatMap((page) => page.dispatches) ?? [],
      ),
    [dispatchQuery.data?.pages],
  )

  useEffect(() => {
    if (!search.dispatch && dispatches.length > 0) {
      onSearchChange({ ...search, dispatch: dispatches[0].id }, true)
    }
  }, [dispatches, onSearchChange, search])

  const applyFilters = useCallback(
    (next: DispatchFilterValues) => {
      onSearchChange(
        {
          ...withoutDispatchViewState(search),
          view: "dispatches",
          ...(next.eventID ? { dispatch_event: next.eventID } : {}),
          ...(next.workflowRef ? { workflow: next.workflowRef } : {}),
          ...(next.status ? { dispatch_status: next.status } : {}),
        },
        true,
      )
    },
    [onSearchChange, search],
  )

  const retryDispatches = () => {
    if (dispatchQuery.isFetchNextPageError) {
      void dispatchQuery.fetchNextPage()
    } else {
      void dispatchQuery.refetch()
    }
  }

  return (
    <>
      <DispatchFilterBar
        filters={filters}
        onApply={applyFilters}
        onReset={() =>
          onSearchChange(
            {
              ...withoutDispatchViewState(search),
              view: "dispatches",
            },
            true,
          )
        }
      />

      <div className="min-h-0 flex-1 overflow-auto p-3 lg:overflow-hidden lg:p-4">
        <div className="flex min-h-full min-w-0 flex-col gap-3 lg:grid lg:h-full lg:min-h-0 lg:grid-cols-[minmax(300px,0.85fr)_minmax(0,1.35fr)]">
          <DispatchList
            dispatches={dispatches}
            selectedDispatchID={search.dispatch}
            loading={dispatchQuery.isPending}
            error={dispatchQuery.error}
            hasMore={Boolean(dispatchQuery.hasNextPage)}
            loadingMore={dispatchQuery.isFetchingNextPage}
            onSelect={(dispatchID) =>
              onSearchChange({ ...search, dispatch: dispatchID })
            }
            onRetry={retryDispatches}
            onLoadMore={() => void dispatchQuery.fetchNextPage()}
          />
          <DispatchDetail dispatchID={search.dispatch} />
        </div>
      </div>
    </>
  )
}

function withoutEventViewState(search: EventsRouteSearch): EventsRouteSearch {
  const hiddenState = { ...search }
  delete hiddenState.source
  delete hiddenState.connector
  delete hiddenState.type
  delete hiddenState.routing_status
  delete hiddenState.event
  return hiddenState
}

function withoutDispatchViewState(
  search: EventsRouteSearch,
): EventsRouteSearch {
  const hiddenState = { ...search }
  delete hiddenState.dispatch_event
  delete hiddenState.workflow
  delete hiddenState.dispatch_status
  delete hiddenState.dispatch
  return hiddenState
}

function deduplicateEvents(events: EventView[]): EventView[] {
  return Array.from(new Map(events.map((event) => [event.id, event])).values())
}

function deduplicateDispatches(dispatches: DispatchView[]): DispatchView[] {
  return Array.from(
    new Map(dispatches.map((dispatch) => [dispatch.id, dispatch])).values(),
  )
}
