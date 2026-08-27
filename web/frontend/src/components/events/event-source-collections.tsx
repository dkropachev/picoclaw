import {
  IconEdit,
  IconPlus,
  IconPower,
  IconSettings,
  IconTrash,
} from "@tabler/icons-react"
import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type EventChannelSource,
  type EventSource,
  type EventSourceInput,
  type EventSourceSummary,
  type EventWebhookSource,
  bulkDeleteEventSources,
  deleteEventSource,
  getEventSource,
  listEventSources,
  updateEventSource,
} from "@/api/event-sources"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import {
  eventSourceCollectionViews,
  eventSourcesDefaultQuery,
  normalizeEventSourcesCollectionSearch,
} from "./event-source-collection-route-state"

interface EventSourcesCollectionNavigation {
  onAdd: () => void
  onOpen: (source: EventSourceSummary) => void
  onEdit: (source: EventSourceSummary) => void
  onSettings: () => void
}

export function EventSourcesCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: EventSourcesCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const activeQuery = normalizeEventSourcesCollectionSearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["event-sources", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listEventSources(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = useMemo(
    () => [
      ...new Map(
        (query.data?.pages.flatMap((page) => page.event_sources) ?? []).map(
          (source) => [source.id, source],
        ),
      ).values(),
    ],
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const toggle = useMutation({
    mutationFn: async (summary: EventSourceSummary) => {
      const detail = await getEventSource(summary.id)
      return updateEventSource(
        summary.id,
        sourceInput(detail.event_source, !detail.event_source.enabled),
        detail.config_revision,
      )
    },
    onSuccess: async (response) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${response.event_source.name} was ${response.event_source.enabled ? "enabled" : "disabled"}.`,
        response.event_source.name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      await query.refetch()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "The event source was not updated.",
      )
    },
  })
  const mutateToggle = toggle.mutate
  const togglePending = toggle.isPending
  const definition = useMemo<CollectionDefinition<EventSourceSummary>>(
    () => ({
      key: "event-sources",
      title: "Event sources",
      defaultQuery: eventSourcesDefaultQuery,
      supportedViews: eventSourceCollectionViews,
      defaultView: "list",
      getItemID: (source) => source.id,
      getItemLabel: (source) => source.name,
      getItemIdentity: (source) => ({
        title: source.name,
        description:
          source.kind === "webhook"
            ? `POST /webhooks/events/${source.name}`
            : "Delta Chat email adapter",
        metadata: sourceSummaryMetadata(source),
      }),
      columns: [
        {
          id: "kind",
          header: "Kind",
          cell: (source) => formatKind(source.kind),
        },
        {
          id: "status",
          header: "Status",
          cell: (source) => formatStatus(source.status),
        },
        {
          id: "format",
          header: "Format",
          cell: (source) => formatSourceFormat(source.format),
        },
        {
          id: "repositories",
          header: "Repositories",
          cell: repositoryScope,
          className: "w-32 tabular-nums",
        },
        {
          id: "polling",
          header: "Polling",
          cell: (source) => (source.poll_notifications ? "Enabled" : "—"),
        },
      ],
      gridFacts: [
        {
          id: "kind",
          label: "Kind",
          value: (source) => formatKind(source.kind),
        },
        {
          id: "format",
          label: "Format",
          value: (source) => formatSourceFormat(source.format),
        },
        { id: "repositories", label: "Repositories", value: repositoryScope },
        {
          id: "polling",
          label: "Notification polling",
          value: (source) => (source.poll_notifications ? "Enabled" : "Off"),
        },
      ],
      badges: [
        {
          id: "status",
          label: (source) => formatStatus(source.status),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit source",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
        {
          id: "toggle",
          label: (source) =>
            source.enabled ? "Disable source" : "Enable source",
          icon: <IconPower />,
          disabled: () => togglePending,
          onSelect: mutateToggle,
        },
      ],
    }),
    [mutateToggle, navigation.onEdit, togglePending],
  )

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={items}
      total={first?.total}
      schema={first?.query_schema}
      canonicalQuery={first?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={query.error}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={navigation.onOpen}
      addAction={
        <div className="flex items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-label="Event source settings"
            onClick={navigation.onSettings}
          >
            <IconSettings /> <span className="hidden sm:inline">Settings</span>
          </Button>
          <Button type="button" size="sm" onClick={navigation.onAdd}>
            <IconPlus /> Add source
          </Button>
        </div>
      }
      onBulkDelete={async (ids) => {
        if (!first?.config_revision) {
          throw new Error("Configuration revision is unavailable")
        }
        return bulkDeleteEventSources(ids, first.config_revision)
      }}
      afterBulkDelete={async () => {
        await Promise.all([
          query.refetch(),
          refreshGatewayState({ force: true }),
        ])
      }}
      bulkDeleteConfirmation={{
        title: (count) =>
          `Delete ${count} selected event source${count === 1 ? "" : "s"}?`,
        description:
          "Only explicitly selected configured sources will be deleted. Sources that changed or disappeared remain selected with their failure.",
      }}
      emptyTitle="No configured event sources"
      emptyDescription="Add an authenticated webhook or connect an eligible channel adapter."
    />
  )
}

export function EventSourceDetailPage({
  id,
  onBack,
  onEdit,
  onRemoved,
}: {
  id: string
  onBack: () => void
  onEdit: () => void
  onRemoved: () => void
}) {
  const { t } = useTranslation()
  const [deleteOpen, setDeleteOpen] = useState(false)
  const query = useQuery({
    queryKey: ["event-sources", "detail", id],
    queryFn: ({ signal }) => getEventSource(id, signal),
    retry: false,
  })
  const source = query.data?.event_source
  const toggle = useMutation({
    mutationFn: () => {
      if (!source || !query.data?.config_revision) {
        throw new Error("Event source details are unavailable")
      }
      return updateEventSource(
        id,
        sourceInput(source, !source.enabled),
        query.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${response.event_source.name} was ${response.event_source.enabled ? "enabled" : "disabled"}.`,
        response.event_source.name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      await query.refetch()
    },
    onError: mutationError("The event source was not updated."),
  })
  const remove = useMutation({
    mutationFn: () => {
      if (!query.data?.config_revision) {
        throw new Error("Configuration revision is unavailable")
      }
      return deleteEventSource(id, query.data.config_revision)
    },
    onSuccess: async () => {
      setDeleteOpen(false)
      await refreshGatewayState({ force: true })
      toast.success("Event source deleted.")
      onRemoved()
    },
    onError: mutationError("The event source was not deleted."),
  })

  return (
    <>
      <CollectionDetailShell
        title={source?.name ?? "Event source"}
        identity={<span className="font-mono text-xs">{id}</span>}
        status={
          source ? (
            <>
              <Badge variant="outline">{formatStatus(source.status)}</Badge>
              <Badge variant={source.enabled ? "secondary" : "outline"}>
                {source.enabled ? "Enabled" : "Disabled"}
              </Badge>
            </>
          ) : undefined
        }
        loading={query.isLoading}
        error={isNotFound(query.error) ? undefined : errorMessage(query.error)}
        notFound={isNotFound(query.error)}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="All event sources"
        actions={
          source ? (
            <>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={toggle.isPending}
                onClick={() => toggle.mutate()}
              >
                <IconPower /> {source.enabled ? "Disable" : "Enable"}
              </Button>
              <Button type="button" size="sm" onClick={onEdit}>
                <IconEdit /> Edit
              </Button>
              <Button
                type="button"
                size="sm"
                variant="destructive"
                aria-label="Delete source"
                onClick={() => setDeleteOpen(true)}
              >
                <IconTrash /> <span className="hidden sm:inline">Delete</span>
              </Button>
            </>
          ) : undefined
        }
      >
        {source && <EventSourceDetails source={source} />}
      </CollectionDetailShell>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {source?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              This removes the configured event source. Existing durable events
              remain available.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={remove.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault()
                remove.mutate()
              }}
            >
              Delete source
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function EventSourceDetails({ source }: { source: EventSource }) {
  return source.kind === "webhook" ? (
    <WebhookDetails source={source} />
  ) : (
    <ChannelDetails source={source} />
  )
}

function WebhookDetails({ source }: { source: EventWebhookSource }) {
  const endpoint = source.endpoint || `/webhooks/events/${source.name}`
  return (
    <div className="space-y-6">
      <DetailCard
        title="Webhook configuration"
        values={[
          ["Kind", "Webhook"],
          ["Format", formatSourceFormat(source.format)],
          ["Endpoint", `POST ${endpoint}`],
          [
            "Signing secret",
            source.secret_configured ? "Configured" : "Not set",
          ],
          [
            "Notification polling",
            source.poll_notifications ? "Enabled" : "Off",
          ],
          ["GitHub target user", source.target_user || "—"],
        ]}
      />
      {source.format === "github" && (
        <Card>
          <CardHeader>
            <CardTitle>Watched repositories</CardTitle>
          </CardHeader>
          <CardContent>
            {source.repositories.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                All repositories visible to this source
              </p>
            ) : (
              <ul className="space-y-1 font-mono text-sm">
                {source.repositories.map((repository) => (
                  <li key={repository}>{repository}</li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  )
}

function ChannelDetails({ source }: { source: EventChannelSource }) {
  return (
    <div className="space-y-4">
      <DetailCard
        title="Channel adapter"
        values={[
          ["Kind", "Channel adapter"],
          ["Channel type", source.channel_type || "Delta Chat"],
          ["Channel", source.name],
          [
            "Channel state",
            source.channel_enabled ? "Enabled" : "Disabled or unavailable",
          ],
          [
            "Delivery mode",
            source.mode === "event_only"
              ? "Event only"
              : "Mirror to event + chat",
          ],
          [
            "Allow unverified email",
            source.allow_unverified_email ? "Yes" : "No",
          ],
        ]}
      />
      {source.allow_unverified_email && (
        <div
          role="note"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm"
        >
          Unverified email can be spoofed. Use deterministic workflow rules that
          limit what these events may trigger.
        </div>
      )}
    </div>
  )
}

function DetailCard({
  title,
  values,
}: {
  title: string
  values: Array<[string, string]>
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-x-6 gap-y-4 sm:grid-cols-2">
          {values.map(([label, value]) => (
            <div key={label} className="min-w-0">
              <dt className="text-muted-foreground text-xs font-medium uppercase">
                {label}
              </dt>
              <dd className="mt-1 text-sm break-words">{value}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}

function sourceInput(
  source: EventSource,
  enabled = source.enabled,
): EventSourceInput {
  if (source.kind === "webhook") {
    return {
      kind: "webhook",
      name: source.name,
      enabled,
      format: source.format,
      repositories: source.repositories,
      target_user: source.target_user,
      poll_notifications: source.poll_notifications,
      secret_update: "preserve",
    }
  }
  return {
    kind: "channel",
    name: source.name,
    enabled,
    source: "email",
    mode: source.mode,
    allow_unverified_email: source.allow_unverified_email,
  }
}

function sourceSummaryMetadata(source: EventSourceSummary): string {
  if (source.kind === "channel") return "Existing Delta Chat channel"
  if (source.format !== "github") return "Authenticated webhook"
  const scope =
    source.repositories === 0
      ? "All repositories"
      : `${source.repositories} repositories`
  return source.poll_notifications ? `${scope} · Polling notifications` : scope
}

function repositoryScope(source: EventSourceSummary): string | number {
  if (source.kind !== "webhook" || source.format !== "github") return "—"
  return source.repositories === 0 ? "All" : source.repositories
}

function formatKind(kind: EventSourceSummary["kind"]): string {
  return kind === "channel" ? "Channel adapter" : "Webhook"
}

function formatSourceFormat(format: EventSourceSummary["format"]): string {
  if (format === "github") return "GitHub"
  if (format === "deltachat") return "Delta Chat"
  return "Standard Webhooks"
}

function formatStatus(status: string): string {
  return status
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ")
}

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    (error as { status?: unknown }).status === 404
  )
}

function errorMessage(error: unknown): string | undefined {
  return error instanceof Error
    ? error.message
    : error
      ? String(error)
      : undefined
}

function mutationError(fallback: string) {
  return (error: unknown) => {
    toast.error(error instanceof Error ? error.message : fallback)
  }
}
