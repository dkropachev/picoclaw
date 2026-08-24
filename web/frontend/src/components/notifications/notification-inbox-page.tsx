import {
  IconAdjustmentsHorizontal,
  IconArchive,
  IconBell,
  IconBellOff,
  IconCheck,
  IconChevronLeft,
  IconChevronRight,
  IconClock,
  IconDeviceMobile,
  IconFilter,
  IconRefresh,
  IconSearch,
  IconSettings,
  IconSortDescending,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import dayjs from "dayjs"
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import {
  type DevelopmentNotification,
  type DevelopmentNotificationBulkAction,
  type DevelopmentNotificationPriority,
  NotificationAPIError,
  type NotificationSavedViewDraft,
  type NotificationSavedViewsDocument,
  createNotificationRequestID,
  getDevelopmentNotification,
  getDevelopmentNotificationNeighbors,
  getNotificationSavedViews,
  listDevelopmentNotifications,
  mutateDevelopmentNotifications,
  openNotificationEventStream,
  updateNotificationSavedViews,
} from "@/api/notifications"
import {
  type NotificationSimpleFilters,
  type NotificationSort,
  buildNotificationSimpleQuery,
  defaultNotificationQuery,
  insertNotificationQuerySuggestion,
  maximumNotificationQueryLength,
  notificationBuiltInViews,
  notificationQueryByteLength,
  notificationQuerySuggestions,
  truncateNotificationQuery,
  withNotificationSort,
} from "@/components/notifications/notification-query"
import { PushNotificationSettings } from "@/components/notifications/push-notification-settings"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { refreshPicoClawAppBadge } from "@/lib/pwa-notifications"
import { cn } from "@/lib/utils"

const notificationListKey = (query: string) => ["notifications", query] as const
const notificationScrollPositions = new Map<string, number>()
const maximumRememberedNotificationQueries = 20

export interface NotificationInboxPageProps {
  initialQuery?: string
  selectedNotificationID?: string
  onQueryChange?: (query: string) => void
  onNotificationChange?: (notificationID?: string) => void
  onOpenWorkspace?: (
    workspaceID: string,
    target: DevelopmentNotification["target"],
  ) => void
}

export function NotificationInboxPage({
  initialQuery,
  selectedNotificationID,
  onQueryChange,
  onNotificationChange,
  onOpenWorkspace,
}: NotificationInboxPageProps) {
  const queryClient = useQueryClient()
  const [query, setQuery] = useState(
    () => initialQuery?.trim() || defaultNotificationQuery,
  )
  const [queryDraft, setQueryDraft] = useState(query)
  const [internalSelectedID, setInternalSelectedID] = useState(
    selectedNotificationID,
  )
  const [selectedIDs, setSelectedIDs] = useState<Set<string>>(() => new Set())
  const [filterOpen, setFilterOpen] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [viewsOpen, setViewsOpen] = useState(false)
  const [pushOpen, setPushOpen] = useState(false)
  const [actionError, setActionError] = useState("")
  const listScrollRef = useRef<HTMLDivElement>(null)
  const listScrollPosition = useRef(0)
  const defaultViewApplied = useRef(Boolean(initialQuery?.trim()))
  const effectiveSelectedID = selectedNotificationID ?? internalSelectedID

  useEffect(() => {
    if (initialQuery?.trim() && initialQuery.trim() !== query) {
      setQuery(initialQuery.trim())
      setQueryDraft(initialQuery.trim())
    }
  }, [initialQuery, query])

  useEffect(() => {
    setInternalSelectedID(selectedNotificationID)
  }, [selectedNotificationID])

  const notificationsQuery = useInfiniteQuery({
    queryKey: notificationListKey(query),
    queryFn: ({ pageParam, signal }) =>
      listDevelopmentNotifications(
        { query, cursor: pageParam, limit: 50 },
        signal,
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor,
    refetchInterval: 30_000,
  })
  const viewsQuery = useQuery({
    queryKey: ["notification-views"],
    queryFn: ({ signal }) => getNotificationSavedViews(signal),
  })
  const detailQuery = useQuery({
    queryKey: ["notification", effectiveSelectedID],
    queryFn: ({ signal }) =>
      getDevelopmentNotification(effectiveSelectedID!, signal),
    enabled: Boolean(effectiveSelectedID),
  })
  const neighborsQuery = useQuery({
    queryKey: ["notification-neighbors", effectiveSelectedID, query],
    queryFn: ({ signal }) =>
      getDevelopmentNotificationNeighbors(effectiveSelectedID!, query, signal),
    enabled: Boolean(effectiveSelectedID),
  })

  const notifications = useMemo(
    () =>
      notificationsQuery.data?.pages.flatMap((page) => page.notifications) ??
      [],
    [notificationsQuery.data?.pages],
  )
  const notificationByID = useMemo(
    () => new Map(notifications.map((item) => [item.id, item])),
    [notifications],
  )
  const selectedNotifications = [...selectedIDs].flatMap((id) => {
    const notification = notificationByID.get(id)
    return notification ? [notification] : []
  })
  const counts = notificationsQuery.data?.pages[0]?.counts
  const currentDetail = detailQuery.data

  useEffect(() => {
    void refreshPicoClawAppBadge(counts?.open ?? 0)
  }, [counts?.open])

  useEffect(() => {
    const stream = openNotificationEventStream()
    if (!stream) return
    const refresh = () => {
      void queryClient.invalidateQueries({ queryKey: ["notifications"] })
      if (effectiveSelectedID) {
        void queryClient.invalidateQueries({
          queryKey: ["notification", effectiveSelectedID],
        })
      }
    }
    stream.addEventListener("notification", refresh)
    stream.addEventListener("attention", refresh)
    stream.onmessage = refresh
    return () => stream.close()
  }, [effectiveSelectedID, queryClient])

  useEffect(() => {
    setSelectedIDs(new Set())
  }, [query])

  useEffect(() => {
    if (effectiveSelectedID) return
    const top = notificationScrollPositions.get(query) ?? 0
    const frame = requestAnimationFrame(() => {
      if (listScrollRef.current) listScrollRef.current.scrollTop = top
    })
    return () => cancelAnimationFrame(frame)
  }, [effectiveSelectedID, query])

  const refreshNotifications = async () => {
    await queryClient.invalidateQueries({ queryKey: ["notifications"] })
    if (effectiveSelectedID) {
      await queryClient.invalidateQueries({
        queryKey: ["notification", effectiveSelectedID],
      })
    }
  }

  const bulkMutation = useMutation({
    mutationFn: ({
      action,
      values,
      snoozedUntil,
    }: {
      action: DevelopmentNotificationBulkAction
      values: DevelopmentNotification[]
      snoozedUntil?: string
    }) =>
      mutateDevelopmentNotifications({
        action,
        items: values.map((item) => ({
          id: item.id,
          expected_version: item.version,
        })),
        request_id: createNotificationRequestID(),
        ...(snoozedUntil ? { snoozed_until: snoozedUntil } : {}),
      }),
    onSuccess: async () => {
      setActionError("")
      setSelectedIDs(new Set())
      await refreshNotifications()
    },
    onError: (error) => {
      setActionError(notificationErrorMessage(error))
      void refreshNotifications()
    },
  })

  const viewsMutation = useMutation({
    mutationFn: (drafts: NotificationSavedViewDraft[]) =>
      updateNotificationSavedViews({
        views: drafts,
        expected_version: viewsQuery.data?.version ?? 0,
        request_id: createNotificationRequestID(),
      }),
    onSuccess: (document) => {
      queryClient.setQueryData(["notification-views"], document)
      setActionError("")
      setViewsOpen(false)
    },
    onError: (error) => {
      setActionError(notificationErrorMessage(error))
      void viewsQuery.refetch()
    },
  })

  const applyQuery = useCallback(
    (value: string, options?: { keepAdvancedOpen?: boolean }) => {
      const next =
        truncateNotificationQuery(value.trim()) || defaultNotificationQuery
      setQuery(next)
      setQueryDraft(next)
      setActionError("")
      setFilterOpen(false)
      if (!options?.keepAdvancedOpen) setAdvancedOpen(false)
      setInternalSelectedID(undefined)
      onNotificationChange?.(undefined)
      onQueryChange?.(next)
    },
    [onNotificationChange, onQueryChange],
  )

  useEffect(() => {
    if (defaultViewApplied.current || !viewsQuery.data) return
    defaultViewApplied.current = true
    const defaultView = viewsQuery.data.views.find((view) => view.default)
    if (defaultView) applyQuery(defaultView.query)
  }, [applyQuery, viewsQuery.data])

  const selectNotification = (notificationID?: string) => {
    if (notificationID) {
      listScrollPosition.current = listScrollRef.current?.scrollTop ?? 0
      rememberNotificationScroll(query, listScrollPosition.current)
    }
    setInternalSelectedID(notificationID)
    onNotificationChange?.(notificationID)
    if (!notificationID) {
      requestAnimationFrame(() => {
        if (listScrollRef.current) {
          listScrollRef.current.scrollTop = listScrollPosition.current
        }
      })
    }
  }

  const mutateOne = (
    item: DevelopmentNotification,
    action: DevelopmentNotificationBulkAction,
    snoozedUntil?: string,
  ) => bulkMutation.mutate({ action, values: [item], snoozedUntil })

  const queryError =
    notificationsQuery.error instanceof NotificationAPIError
      ? notificationsQuery.error
      : undefined
  const total = notificationsQuery.data?.pages[0]?.total

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="notification-inbox"
    >
      <PageHeader
        title="Notifications"
        titleExtra={
          typeof counts?.open === "number" && counts.open > 0 ? (
            <Badge variant="secondary">{counts.open} open</Badge>
          ) : undefined
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Mobile notification settings"
          title="Mobile notification settings"
          onClick={() => setPushOpen(true)}
        >
          <IconDeviceMobile />
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Refresh notifications"
          title="Refresh notifications"
          disabled={notificationsQuery.isFetching}
          onClick={() => void refreshNotifications()}
        >
          <IconRefresh
            className={cn(
              "size-4",
              notificationsQuery.isFetching && "animate-spin",
            )}
          />
        </Button>
      </PageHeader>

      <div className="border-border flex min-h-0 flex-1 flex-col border-t">
        <SavedViewBar
          activeQuery={query}
          document={viewsQuery.data}
          onApply={applyQuery}
          onManage={() => setViewsOpen(true)}
        />

        <div className="border-border flex flex-wrap items-center gap-2 border-y px-3 py-2 md:px-4">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => setFilterOpen(true)}
          >
            <IconFilter />
            Filter
          </Button>
          <Select
            value={detectSort(query)}
            onValueChange={(value) =>
              applyQuery(withNotificationSort(query, value as NotificationSort))
            }
          >
            <SelectTrigger size="sm" aria-label="Sort notifications">
              <IconSortDescending />
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="priority">Priority</SelectItem>
              <SelectItem value="updated">Recently updated</SelectItem>
              <SelectItem value="created">Newest</SelectItem>
              <SelectItem value="repository">Repository</SelectItem>
            </SelectContent>
          </Select>
          <Button
            type="button"
            size="sm"
            variant={advancedOpen ? "secondary" : "ghost"}
            onClick={() => setAdvancedOpen((open) => !open)}
          >
            <IconAdjustmentsHorizontal />
            Advanced
          </Button>
          <span className="text-muted-foreground ml-auto text-xs">
            {typeof total === "number"
              ? `${total} result${total === 1 ? "" : "s"}`
              : `${notifications.length} loaded`}
          </span>
        </div>

        {advancedOpen && (
          <AdvancedQueryEditor
            value={queryDraft}
            error={queryError}
            onChange={setQueryDraft}
            onApply={(value) => applyQuery(value, { keepAdvancedOpen: true })}
          />
        )}

        {actionError && (
          <p
            role="alert"
            className="border-border bg-destructive/5 text-destructive border-b px-4 py-2 text-sm"
          >
            {actionError}
          </p>
        )}

        {selectedIDs.size > 0 && (
          <BulkActionBar
            selected={selectedNotifications}
            pending={bulkMutation.isPending}
            onClear={() => setSelectedIDs(new Set())}
            onAction={(action, snoozedUntil) =>
              bulkMutation.mutate({
                action,
                values: selectedNotifications,
                snoozedUntil,
              })
            }
          />
        )}

        <div className="grid min-h-0 flex-1 md:grid-cols-[minmax(20rem,0.8fr)_minmax(24rem,1.2fr)]">
          <section
            className={cn(
              "border-border min-h-0 flex-col border-r",
              effectiveSelectedID ? "hidden md:flex" : "flex",
            )}
            aria-label="Notification list"
          >
            {notifications.length > 0 && (
              <div className="border-border flex h-10 shrink-0 items-center justify-between gap-3 border-b px-3 text-xs">
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    aria-label="Select all loaded notifications"
                    checked={notifications.every((item) =>
                      selectedIDs.has(item.id),
                    )}
                    className="accent-primary size-4"
                    onChange={(event) =>
                      setSelectedIDs(
                        event.target.checked
                          ? new Set(notifications.map((item) => item.id))
                          : new Set(),
                      )
                    }
                  />
                  Select loaded
                </label>
                <span className="text-muted-foreground">
                  {notifications.length} loaded
                </span>
              </div>
            )}
            <div
              ref={listScrollRef}
              className="min-h-0 flex-1 overflow-auto"
              onScroll={(event) =>
                rememberNotificationScroll(query, event.currentTarget.scrollTop)
              }
            >
              {notificationsQuery.isPending ? (
                <InboxMessage
                  icon={<IconBell />}
                  text="Loading notifications…"
                />
              ) : notificationsQuery.isError ? (
                <InboxMessage
                  icon={<IconBellOff />}
                  text={
                    queryError
                      ? `Query error${
                          typeof queryError.position === "number"
                            ? ` at position ${queryError.position}`
                            : ""
                        }: ${queryError.message}`
                      : "Could not load notifications."
                  }
                  destructive
                />
              ) : notifications.length === 0 ? (
                <InboxMessage
                  icon={<IconCheck />}
                  text="No notifications match this view."
                />
              ) : (
                <ul className="divide-border divide-y">
                  {notifications.map((notification) => (
                    <NotificationListItem
                      key={notification.id}
                      notification={notification}
                      checked={selectedIDs.has(notification.id)}
                      active={notification.id === effectiveSelectedID}
                      onCheck={(checked) => {
                        setSelectedIDs((current) => {
                          const next = new Set(current)
                          if (checked) next.add(notification.id)
                          else next.delete(notification.id)
                          return next
                        })
                      }}
                      onOpen={() => selectNotification(notification.id)}
                    />
                  ))}
                </ul>
              )}
              {notificationsQuery.hasNextPage && (
                <div className="flex justify-center p-3">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={notificationsQuery.isFetchingNextPage}
                    onClick={() => void notificationsQuery.fetchNextPage()}
                  >
                    {notificationsQuery.isFetchingNextPage
                      ? "Loading…"
                      : "Load more"}
                  </Button>
                </div>
              )}
            </div>
          </section>

          <section
            className={cn(
              "min-h-0 flex-col",
              effectiveSelectedID ? "flex" : "hidden md:flex",
            )}
            aria-label="Notification detail"
          >
            {!effectiveSelectedID ? (
              <InboxMessage
                icon={<IconBell />}
                text="Select a notification to see its details."
              />
            ) : detailQuery.isPending ? (
              <InboxMessage icon={<IconBell />} text="Loading details…" />
            ) : detailQuery.isError || !currentDetail ? (
              <InboxMessage
                icon={<IconBellOff />}
                text="Could not load this notification."
                destructive
              />
            ) : (
              <NotificationDetail
                notification={currentDetail}
                previousID={neighborsQuery.data?.previous_id}
                nextID={neighborsQuery.data?.next_id}
                pending={bulkMutation.isPending}
                onBack={() => selectNotification(undefined)}
                onPrevious={(id) => selectNotification(id)}
                onNext={(id) => selectNotification(id)}
                onAction={mutateOne}
                onOpenWorkspace={onOpenWorkspace}
              />
            )}
          </section>
        </div>
      </div>

      <SimpleFiltersSheet
        open={filterOpen}
        currentQuery={query}
        onOpenChange={setFilterOpen}
        onApply={applyQuery}
      />
      <SavedViewsDialog
        open={viewsOpen}
        document={viewsQuery.data}
        currentQuery={query}
        pending={viewsMutation.isPending}
        onOpenChange={setViewsOpen}
        onSave={(views) => viewsMutation.mutate(views)}
      />
      <PushNotificationSettings open={pushOpen} onOpenChange={setPushOpen} />
    </div>
  )
}

function NotificationListItem({
  notification,
  active,
  checked,
  onCheck,
  onOpen,
}: {
  notification: DevelopmentNotification
  active: boolean
  checked: boolean
  onCheck: (checked: boolean) => void
  onOpen: () => void
}) {
  return (
    <li
      className={cn(
        "hover:bg-muted/40 relative flex min-w-0 items-start gap-2 p-3",
        active && "bg-muted/60",
        notification.status !== "open" && "opacity-75",
      )}
    >
      <label className="mt-1 flex size-7 shrink-0 items-center justify-center">
        <span className="sr-only">Select {notification.title}</span>
        <input
          type="checkbox"
          checked={checked}
          onChange={(event) => onCheck(event.target.checked)}
          className="border-input accent-primary size-4 rounded"
        />
      </label>
      <button
        type="button"
        onClick={onOpen}
        className="focus-visible:ring-ring min-w-0 flex-1 rounded-md text-left focus-visible:ring-2 focus-visible:outline-none"
        aria-label={`Open ${notification.title}`}
      >
        <span className="flex min-w-0 items-start gap-2">
          {!notification.read && (
            <span
              className="bg-primary mt-1.5 size-2 shrink-0 rounded-full"
              aria-label="Unread"
            />
          )}
          <span className="min-w-0 flex-1">
            <span className="line-clamp-2 text-sm font-medium">
              {notification.title}
            </span>
            <span className="text-muted-foreground mt-1 block truncate text-xs">
              {notification.repository}
            </span>
          </span>
          <span className="text-muted-foreground shrink-0 text-xs">
            {dayjs(notification.updated_at).fromNow()}
          </span>
        </span>
        <span className="mt-2 flex flex-wrap items-center gap-1.5">
          <PriorityBadge priority={notification.priority} />
          <Badge variant="outline">{reasonLabel(notification.reason)}</Badge>
          {isSnoozed(notification) && (
            <Badge variant="secondary">
              <IconClock /> Snoozed
            </Badge>
          )}
        </span>
      </button>
    </li>
  )
}

function NotificationDetail({
  notification,
  previousID,
  nextID,
  pending,
  onBack,
  onPrevious,
  onNext,
  onAction,
  onOpenWorkspace,
}: {
  notification: DevelopmentNotification
  previousID?: string
  nextID?: string
  pending: boolean
  onBack: () => void
  onPrevious: (notificationID: string) => void
  onNext: (notificationID: string) => void
  onAction: (
    notification: DevelopmentNotification,
    action: DevelopmentNotificationBulkAction,
    snoozedUntil?: string,
  ) => void
  onOpenWorkspace?: NotificationInboxPageProps["onOpenWorkspace"]
}) {
  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="border-border bg-background/95 sticky top-0 z-10 flex items-center gap-2 border-b px-3 py-2 backdrop-blur md:px-4">
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          className="md:hidden"
          aria-label="Back to notifications"
          onClick={onBack}
        >
          <IconChevronLeft />
        </Button>
        <Button
          type="button"
          size="icon-sm"
          variant="outline"
          aria-label="Previous notification"
          disabled={!previousID}
          onClick={() => previousID && onPrevious(previousID)}
        >
          <IconChevronLeft />
        </Button>
        <Button
          type="button"
          size="icon-sm"
          variant="outline"
          aria-label="Next notification"
          disabled={!nextID}
          onClick={() => nextID && onNext(nextID)}
        >
          <IconChevronRight />
        </Button>
        <span className="text-muted-foreground ml-auto text-xs">
          Updated {dayjs(notification.updated_at).fromNow()}
        </span>
      </div>

      <article className="mx-auto flex w-full max-w-3xl flex-col gap-5 p-4 md:p-6">
        <header className="space-y-3">
          <div className="flex flex-wrap gap-1.5">
            <PriorityBadge priority={notification.priority} />
            <Badge
              variant={notification.status === "open" ? "default" : "outline"}
            >
              {notification.status}
            </Badge>
            <Badge variant="outline">{reasonLabel(notification.reason)}</Badge>
          </div>
          <div>
            <h2 className="text-xl font-semibold tracking-tight">
              {notification.title}
            </h2>
            <p className="text-muted-foreground mt-1 text-sm">
              {notification.repository} · {intentLabel(notification.intent)}
            </p>
          </div>
        </header>

        <p className="text-sm leading-6 whitespace-pre-wrap">
          {notification.summary}
        </p>

        <dl className="border-border grid gap-x-5 gap-y-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
          <Metadata label="Reason" value={reasonLabel(notification.reason)} />
          <Metadata
            label="Source"
            value={sourceLabel(notification.source_kind)}
          />
          <Metadata label="Phase" value={notification.phase} />
          <Metadata
            label="Created"
            value={dayjs(notification.created_at).format("LLL")}
          />
          <Metadata
            label="State"
            value={
              isSnoozed(notification)
                ? `Snoozed until ${dayjs(notification.snoozed_until).format("LLL")}`
                : notification.read
                  ? "Read"
                  : "Unread"
            }
          />
        </dl>

        <div className="flex flex-wrap gap-2">
          {notification.status === "open" && onOpenWorkspace && (
            <Button
              type="button"
              onClick={() =>
                onOpenWorkspace(notification.workspace_id, notification.target)
              }
            >
              Open required action
              <IconChevronRight />
            </Button>
          )}
          <Button
            type="button"
            variant="outline"
            disabled={pending}
            onClick={() =>
              onAction(
                notification,
                notification.read ? "mark_unread" : "mark_read",
              )
            }
          >
            {notification.read ? <IconBell /> : <IconCheck />}
            {notification.read ? "Mark unread" : "Mark read"}
          </Button>
          {notification.status === "open" && (
            <SnoozeMenu
              disabled={pending}
              snoozed={isSnoozed(notification)}
              onClear={() => onAction(notification, "clear_snooze")}
              onSnooze={(until) => onAction(notification, "snooze", until)}
            />
          )}
          {notification.status === "resolved" && (
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onAction(notification, "archive")}
            >
              <IconArchive />
              Archive
            </Button>
          )}
        </div>
      </article>
    </div>
  )
}

function Metadata({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd className="mt-0.5 break-words">{value}</dd>
    </div>
  )
}

function BulkActionBar({
  selected,
  pending,
  onClear,
  onAction,
}: {
  selected: DevelopmentNotification[]
  pending: boolean
  onClear: () => void
  onAction: (
    action: DevelopmentNotificationBulkAction,
    snoozedUntil?: string,
  ) => void
}) {
  const resolvedOnly = selected.every((item) => item.status === "resolved")
  return (
    <div className="border-border bg-muted/30 flex flex-wrap items-center gap-2 border-b px-3 py-2 md:px-4">
      <Badge variant="secondary">{selected.length} selected</Badge>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        disabled={pending}
        onClick={() => onAction("mark_read")}
      >
        <IconCheck /> Read
      </Button>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        disabled={pending}
        onClick={() => onAction("mark_unread")}
      >
        <IconBell /> Unread
      </Button>
      <SnoozeMenu
        disabled={pending}
        snoozed={false}
        onClear={() => onAction("clear_snooze")}
        onSnooze={(until) => onAction("snooze", until)}
      />
      <Button
        type="button"
        size="sm"
        variant="ghost"
        disabled={pending || !resolvedOnly}
        title={
          resolvedOnly
            ? "Archive resolved notifications"
            : "Only resolved notifications can be archived"
        }
        onClick={() => onAction("archive")}
      >
        <IconArchive /> Archive
      </Button>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="ml-auto"
        onClick={onClear}
      >
        Clear
      </Button>
    </div>
  )
}

function SnoozeMenu({
  disabled,
  snoozed,
  onSnooze,
  onClear,
}: {
  disabled: boolean
  snoozed: boolean
  onSnooze: (until: string) => void
  onClear: () => void
}) {
  const now = dayjs()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" size="sm" variant="outline" disabled={disabled}>
          <IconClock /> {snoozed ? "Snoozed" : "Snooze"}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuItem
          onSelect={() => onSnooze(now.add(1, "hour").toISOString())}
        >
          For 1 hour
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={() =>
            onSnooze(
              now.add(1, "day").startOf("day").add(9, "hour").toISOString(),
            )
          }
        >
          Until tomorrow
        </DropdownMenuItem>
        <DropdownMenuItem
          onSelect={() => onSnooze(now.add(1, "week").toISOString())}
        >
          For 1 week
        </DropdownMenuItem>
        {snoozed && (
          <DropdownMenuItem onSelect={onClear}>Clear snooze</DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function AdvancedQueryEditor({
  value,
  error,
  onChange,
  onApply,
}: {
  value: string
  error?: NotificationAPIError
  onChange: (value: string) => void
  onApply: (value: string) => void
}) {
  const submit = (event: FormEvent) => {
    event.preventDefault()
    onApply(value)
  }
  return (
    <form
      onSubmit={submit}
      className="border-border bg-muted/20 space-y-2 border-b px-3 py-3 md:px-4"
    >
      <div className="flex items-center justify-between gap-3">
        <Label htmlFor="notification-query">Advanced query</Label>
        <span className="text-muted-foreground text-xs">
          {notificationQueryByteLength(value)}/{maximumNotificationQueryLength}{" "}
          bytes
        </span>
      </div>
      <Textarea
        id="notification-query"
        value={value}
        maxLength={maximumNotificationQueryLength}
        rows={2}
        spellCheck={false}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? "notification-query-error" : undefined}
        className="font-mono text-xs"
        onChange={(event) =>
          onChange(truncateNotificationQuery(event.target.value))
        }
      />
      {error && (
        <p
          id="notification-query-error"
          role="alert"
          className="text-destructive text-xs"
        >
          {typeof error.position === "number"
            ? `Position ${error.position}: `
            : ""}
          {error.message}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-muted-foreground mr-1 text-xs">Insert:</span>
        {notificationQuerySuggestions.map((suggestion) => (
          <button
            key={suggestion}
            type="button"
            className="border-border bg-background hover:bg-muted rounded-md border px-2 py-1 font-mono text-xs"
            onClick={() =>
              onChange(
                truncateNotificationQuery(
                  insertNotificationQuerySuggestion(value, suggestion),
                ),
              )
            }
          >
            {suggestion}
          </button>
        ))}
        <Button type="submit" size="sm" className="ml-auto">
          <IconSearch /> Run query
        </Button>
      </div>
    </form>
  )
}

function SimpleFiltersSheet({
  open,
  currentQuery,
  onOpenChange,
  onApply,
}: {
  open: boolean
  currentQuery: string
  onOpenChange: (open: boolean) => void
  onApply: (query: string) => void
}) {
  const [filters, setFilters] = useState<NotificationSimpleFilters>(() => ({
    statuses: ["open"],
    priorities: [],
    repository: "",
    text: "",
    unreadOnly: false,
    excludeSnoozed: true,
    sort: detectSort(currentQuery),
  }))
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="bottom"
        className="max-h-[85dvh] overflow-auto sm:right-0 sm:left-auto sm:h-full sm:max-h-none sm:w-[24rem] sm:border-t-0 sm:border-l"
      >
        <SheetHeader>
          <SheetTitle>Filter notifications</SheetTitle>
          <SheetDescription>
            Simple filters replace the current advanced query.
          </SheetDescription>
        </SheetHeader>
        <div className="space-y-5 px-4">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="notification-filter-text">Text</Label>
              <Input
                id="notification-filter-text"
                value={filters.text}
                placeholder="Title or summary"
                onChange={(event) =>
                  setFilters((current) => ({
                    ...current,
                    text: event.target.value,
                  }))
                }
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="notification-filter-repository">Repository</Label>
              <Input
                id="notification-filter-repository"
                value={filters.repository}
                placeholder="owner/repository"
                onChange={(event) =>
                  setFilters((current) => ({
                    ...current,
                    repository: event.target.value,
                  }))
                }
              />
            </div>
          </div>
          <FilterCheckGroup
            label="Status"
            values={["open", "resolved", "archived"] as const}
            selected={filters.statuses}
            onChange={(statuses) =>
              setFilters((current) => ({ ...current, statuses }))
            }
          />
          <FilterCheckGroup
            label="Priority"
            values={["critical", "high", "medium", "low"] as const}
            selected={filters.priorities}
            onChange={(priorities) =>
              setFilters((current) => ({ ...current, priorities }))
            }
          />
          <SwitchRow
            label="Unread only"
            checked={filters.unreadOnly}
            onCheckedChange={(unreadOnly) =>
              setFilters((current) => ({ ...current, unreadOnly }))
            }
          />
          <SwitchRow
            label="Hide snoozed"
            checked={filters.excludeSnoozed}
            onCheckedChange={(excludeSnoozed) =>
              setFilters((current) => ({ ...current, excludeSnoozed }))
            }
          />
          <div className="space-y-2">
            <Label htmlFor="notification-filter-sort">Sort</Label>
            <Select
              value={filters.sort}
              onValueChange={(value) =>
                setFilters((current) => ({
                  ...current,
                  sort: value as NotificationSort,
                }))
              }
            >
              <SelectTrigger id="notification-filter-sort" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="priority">Priority</SelectItem>
                <SelectItem value="updated">Recently updated</SelectItem>
                <SelectItem value="created">Newest</SelectItem>
                <SelectItem value="repository">Repository</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <SheetFooter>
          <Button
            type="button"
            onClick={() => onApply(buildNotificationSimpleQuery(filters))}
          >
            Apply filters
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function FilterCheckGroup<T extends string>({
  label,
  values,
  selected,
  onChange,
}: {
  label: string
  values: readonly T[]
  selected: T[]
  onChange: (value: T[]) => void
}) {
  return (
    <fieldset className="space-y-2">
      <legend className="text-sm font-medium">{label}</legend>
      <div className="grid grid-cols-2 gap-2">
        {values.map((value) => (
          <label
            key={value}
            className="border-border flex items-center gap-2 rounded-md border p-2 text-sm capitalize"
          >
            <input
              type="checkbox"
              checked={selected.includes(value)}
              className="accent-primary size-4"
              onChange={(event) =>
                onChange(
                  event.target.checked
                    ? [...selected, value]
                    : selected.filter((candidate) => candidate !== value),
                )
              }
            />
            {value}
          </label>
        ))}
      </div>
    </fieldset>
  )
}

function SwitchRow({
  label,
  checked,
  onCheckedChange,
}: {
  label: string
  checked: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <Label>{label}</Label>
      <Switch
        checked={checked}
        onCheckedChange={onCheckedChange}
        aria-label={label}
      />
    </div>
  )
}

function SavedViewBar({
  activeQuery,
  document,
  onApply,
  onManage,
}: {
  activeQuery: string
  document?: NotificationSavedViewsDocument
  onApply: (query: string) => void
  onManage: () => void
}) {
  const saved = [...(document?.views ?? [])]
    .filter((view) => view.pinned)
    .sort(
      (left, right) =>
        left.position - right.position || left.name.localeCompare(right.name),
    )
  return (
    <nav
      className="flex min-h-11 items-center gap-1 overflow-x-auto px-3 py-2 md:px-4"
      aria-label="Notification views"
    >
      {[...notificationBuiltInViews, ...saved].map((view) => (
        <Button
          key={view.id}
          type="button"
          size="sm"
          variant={activeQuery === view.query ? "secondary" : "ghost"}
          className="shrink-0"
          onClick={() => onApply(view.query)}
        >
          {view.name}
        </Button>
      ))}
      <Button
        type="button"
        size="icon-sm"
        variant="ghost"
        className="ml-auto shrink-0"
        aria-label="Manage saved views"
        title="Manage saved views"
        onClick={onManage}
      >
        <IconSettings />
      </Button>
    </nav>
  )
}

function SavedViewsDialog({
  open,
  document,
  currentQuery,
  pending,
  onOpenChange,
  onSave,
}: {
  open: boolean
  document?: NotificationSavedViewsDocument
  currentQuery: string
  pending: boolean
  onOpenChange: (open: boolean) => void
  onSave: (views: NotificationSavedViewDraft[]) => void
}) {
  const [drafts, setDrafts] = useState<NotificationSavedViewDraft[]>([])
  useEffect(() => {
    if (!open) return
    setDrafts(
      (document?.views ?? []).map((view) => ({
        id: view.id,
        name: view.name,
        query: view.query,
        pinned: view.pinned,
        default: view.default,
        position: view.position,
      })),
    )
  }, [document?.views, open])

  const addCurrent = () => {
    setDrafts((current) => [
      ...current,
      {
        name: uniqueViewName(current, "Current view"),
        query: currentQuery,
        pinned: true,
        default: current.length === 0,
        position: current.length,
      },
    ])
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85dvh] overflow-hidden sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Saved notification views</DialogTitle>
          <DialogDescription>
            Pin views to the inbox and choose one default query.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 space-y-3 overflow-auto">
          {drafts.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-sm">
              No saved views.
            </p>
          ) : (
            drafts.map((view, index) => (
              <div
                key={view.id ?? `new-${index}`}
                className="border-border grid gap-2 rounded-lg border p-3 sm:grid-cols-[minmax(9rem,0.6fr)_minmax(14rem,1.4fr)_auto]"
              >
                <Input
                  aria-label={`View ${index + 1} name`}
                  value={view.name}
                  maxLength={80}
                  onChange={(event) =>
                    setDrafts((current) =>
                      replaceAt(current, index, {
                        ...view,
                        name: event.target.value,
                      }),
                    )
                  }
                />
                <Input
                  aria-label={`View ${index + 1} query`}
                  value={view.query}
                  maxLength={maximumNotificationQueryLength}
                  className="font-mono text-xs"
                  onChange={(event) =>
                    setDrafts((current) =>
                      replaceAt(current, index, {
                        ...view,
                        query: truncateNotificationQuery(event.target.value),
                      }),
                    )
                  }
                />
                <div className="flex items-center justify-end gap-1">
                  <Button
                    type="button"
                    size="icon-sm"
                    variant={view.pinned ? "secondary" : "ghost"}
                    aria-label={`${view.pinned ? "Unpin" : "Pin"} ${view.name}`}
                    title={view.pinned ? "Unpin" : "Pin"}
                    onClick={() =>
                      setDrafts((current) =>
                        replaceAt(current, index, {
                          ...view,
                          pinned: !view.pinned,
                        }),
                      )
                    }
                  >
                    <IconBell />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant={view.default ? "secondary" : "ghost"}
                    aria-label={`Set ${view.name} as default`}
                    title="Set default"
                    onClick={() =>
                      setDrafts((current) =>
                        current.map((candidate, candidateIndex) => ({
                          ...candidate,
                          default: candidateIndex === index,
                        })),
                      )
                    }
                  >
                    <IconCheck />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Duplicate ${view.name}`}
                    title="Duplicate"
                    onClick={() =>
                      setDrafts((current) => [
                        ...current,
                        {
                          name: uniqueViewName(current, `${view.name} copy`),
                          query: view.query,
                          pinned: view.pinned,
                          default: false,
                          position: current.length,
                        },
                      ])
                    }
                  >
                    <IconRefresh />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={`Delete ${view.name}`}
                    title="Delete"
                    onClick={() =>
                      setDrafts((current) =>
                        current.filter(
                          (_, candidateIndex) => candidateIndex !== index,
                        ),
                      )
                    }
                  >
                    <IconArchive />
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={addCurrent}>
            Save current query
          </Button>
          <Button
            type="button"
            disabled={
              pending ||
              drafts.some((view) => !view.name.trim() || !view.query.trim())
            }
            onClick={() =>
              onSave(
                drafts.map((view, index) => ({
                  ...view,
                  name: view.name.trim(),
                  query: view.query.trim(),
                  position: index,
                })),
              )
            }
          >
            Save views
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function InboxMessage({
  icon,
  text,
  destructive = false,
}: {
  icon: ReactNode
  text: string
  destructive?: boolean
}) {
  return (
    <div
      className={cn(
        "text-muted-foreground flex min-h-48 flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-sm",
        destructive && "text-destructive",
      )}
    >
      <span className="[&_svg]:size-7">{icon}</span>
      <p>{text}</p>
    </div>
  )
}

function PriorityBadge({
  priority,
}: {
  priority: DevelopmentNotificationPriority
}) {
  return (
    <Badge
      variant={
        priority === "critical"
          ? "destructive"
          : priority === "high"
            ? "default"
            : priority === "medium"
              ? "secondary"
              : "outline"
      }
      className="capitalize"
    >
      {priority}
    </Badge>
  )
}

function reasonLabel(reason: DevelopmentNotification["reason"]): string {
  return {
    charter_ambiguity: "Charter ambiguity",
    scope_exception: "Scope exception",
    steering_scope_change: "Scope change",
    implementation_blocked: "Implementation blocked",
    provider_outcome_unknown: "Provider outcome unknown",
    publication_approval: "Publication approval",
  }[reason]
}

function intentLabel(intent: DevelopmentNotification["intent"]): string {
  return intent === "implement_feature" ? "Feature implementation" : "PR pickup"
}

function sourceLabel(source: DevelopmentNotification["source_kind"]): string {
  return source === "pull_request"
    ? "Pull request"
    : source === "issue"
      ? "Issue"
      : "Brief"
}

function isSnoozed(notification: DevelopmentNotification): boolean {
  return Boolean(
    notification.snoozed_until &&
    dayjs(notification.snoozed_until).isAfter(dayjs()),
  )
}

function detectSort(query: string): NotificationSort {
  const order =
    query
      .toUpperCase()
      .split(/\bORDER\s+BY\b/)
      .at(-1) ?? ""
  if (order.trim().startsWith("PRIORITY")) return "priority"
  if (order.trim().startsWith("CREATED")) return "created"
  if (order.trim().startsWith("REPOSITORY")) return "repository"
  return "updated"
}

function notificationErrorMessage(error: unknown): string {
  if (error instanceof NotificationAPIError) return error.message
  return error instanceof Error ? error.message : "Notification action failed."
}

function replaceAt<T>(values: T[], index: number, value: T): T[] {
  return values.map((candidate, candidateIndex) =>
    candidateIndex === index ? value : candidate,
  )
}

function uniqueViewName(
  views: NotificationSavedViewDraft[],
  preferred: string,
): string {
  const names = new Set(views.map((view) => view.name.toLowerCase()))
  if (!names.has(preferred.toLowerCase())) return preferred
  let suffix = 2
  while (names.has(`${preferred} ${suffix}`.toLowerCase())) suffix += 1
  return `${preferred} ${suffix}`
}

function rememberNotificationScroll(query: string, top: number): void {
  notificationScrollPositions.delete(query)
  notificationScrollPositions.set(query, top)
  while (
    notificationScrollPositions.size > maximumRememberedNotificationQueries
  ) {
    const oldest = notificationScrollPositions.keys().next().value as
      | string
      | undefined
    if (!oldest) break
    notificationScrollPositions.delete(oldest)
  }
}
