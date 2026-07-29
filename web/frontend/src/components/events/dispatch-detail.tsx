import { IconActivity, IconExternalLink, IconRoute } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useTranslation } from "react-i18next"

import { type DispatchView, getEventDispatch } from "@/api/events"
import { Button } from "@/components/ui/button"

import { eventErrorMessage, formatEventDate } from "./event-format"
import {
  exactDispatchHref,
  exactEventHref,
  workflowOperateHref,
  workflowRunHref,
} from "./event-links"
import {
  EventMeta,
  EventPanel,
  EventScrollRegion,
  EventStatusBadge,
} from "./event-ui"

export function DispatchDetail({ dispatchID }: { dispatchID?: string }) {
  const { t } = useTranslation()
  const detailQuery = useQuery({
    queryKey: ["events", "dispatches", "detail", dispatchID],
    queryFn: () => getEventDispatch(dispatchID!),
    enabled: Boolean(dispatchID),
    staleTime: 1000,
  })
  const detail = detailQuery.data

  return (
    <section className="border-border bg-card/40 flex min-h-[32rem] min-w-0 flex-col overflow-hidden rounded-lg border lg:min-h-0">
      <div className="border-border flex min-h-12 shrink-0 items-center justify-between gap-3 border-b px-3 py-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <h2 className="min-w-0 truncate text-sm font-medium">
              {detail?.workflow_ref ??
                t("pages.events.dispatch_detail.title", "Dispatch detail")}
            </h2>
            {detail ? <EventStatusBadge status={detail.status} /> : null}
          </div>
          <p className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">
            {dispatchID ??
              t(
                "pages.events.dispatch_detail.select_prompt",
                "Select a dispatch",
              )}
          </p>
        </div>
      </div>

      <EventScrollRegion
        label={t("pages.events.dispatch_detail.region", "Dispatch detail")}
        className="min-h-0 flex-1 overflow-auto p-3"
      >
        {!dispatchID ? (
          <DetailMessage>
            {t(
              "pages.events.dispatch_detail.select_prompt",
              "Select a dispatch to inspect it.",
            )}
          </DetailMessage>
        ) : detailQuery.isPending ? (
          <DetailMessage>
            {t(
              "pages.events.dispatch_detail.loading",
              "Loading dispatch detail…",
            )}
          </DetailMessage>
        ) : detailQuery.error || !detail ? (
          <DetailError
            message={eventErrorMessage(
              detailQuery.error,
              t(
                "pages.events.dispatch_detail.error",
                "Failed to load dispatch detail.",
              ),
            )}
            retryLabel={t("pages.events.dispatch_detail.retry", "Retry")}
            onRetry={() => void detailQuery.refetch()}
          />
        ) : (
          <div className="grid min-w-0 gap-3 xl:grid-cols-2">
            <DispatchSummary dispatch={detail} />
            <DispatchRelationships dispatch={detail} />
            <DispatchLifecycle dispatch={detail} />
            {detail.last_error ? (
              <EventPanel
                title={t(
                  "pages.events.dispatch_detail.last_error",
                  "Last error",
                )}
                className="xl:col-span-2"
              >
                <p className="bg-destructive/10 text-destructive rounded-md px-3 py-2 text-sm break-words">
                  {detail.last_error}
                </p>
              </EventPanel>
            ) : null}
          </div>
        )}
      </EventScrollRegion>
    </section>
  )
}

function DispatchSummary({ dispatch }: { dispatch: DispatchView }) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.dispatch_detail.summary", "Summary")}
      titleExtra={<IconActivity className="text-muted-foreground size-4" />}
      className="xl:col-span-2"
    >
      <dl className="grid min-w-0 grid-cols-2 gap-3 lg:grid-cols-4">
        <EventMeta
          label={t("pages.events.dispatch_detail.dispatch_id", "Dispatch ID")}
          value={dispatch.id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.event_id", "Event ID")}
          value={dispatch.event_id}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.workflow", "Workflow")}
          value={dispatch.workflow_ref}
          mono
          breakAll
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.run", "Run")}
          value={dispatch.run_id}
          mono
          breakAll
        />
        <div className="min-w-0">
          <dt className="text-muted-foreground text-xs">
            {t("pages.events.dispatch_detail.status", "Status")}
          </dt>
          <dd className="mt-1">
            <EventStatusBadge status={dispatch.status} />
          </dd>
        </div>
        <EventMeta
          label={t("pages.events.dispatch_detail.attempts", "Attempts")}
          value={dispatch.attempts}
        />
        <EventMeta
          label={t(
            "pages.events.dispatch_detail.revision",
            "Workflow revision",
          )}
          value={dispatch.workflow_revision}
          mono
          breakAll
        />
      </dl>
    </EventPanel>
  )
}

function DispatchRelationships({ dispatch }: { dispatch: DispatchView }) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.dispatch_detail.relationships", "Relationships")}
      titleExtra={<IconRoute className="text-muted-foreground size-4" />}
      className="xl:col-span-2"
    >
      <div className="flex min-w-0 flex-wrap gap-2">
        <RelationshipLink href={exactEventHref(dispatch.event_id)}>
          {t("pages.events.dispatch_detail.open_event", "Open event")}
        </RelationshipLink>
        <RelationshipLink href={workflowOperateHref(dispatch.workflow_ref)}>
          {t("pages.events.dispatch_detail.open_workflow", "Open workflow")}
        </RelationshipLink>
        {dispatch.run_id ? (
          <RelationshipLink
            href={workflowRunHref(dispatch.workflow_ref, dispatch.run_id)}
          >
            {t("pages.events.dispatch_detail.open_run", "Open run")}
          </RelationshipLink>
        ) : null}
        <RelationshipLink href={exactDispatchHref(dispatch.id)}>
          {t("pages.events.dispatch_detail.permalink", "Dispatch permalink")}
        </RelationshipLink>
      </div>
    </EventPanel>
  )
}

function RelationshipLink({
  href,
  children,
}: {
  href: string
  children: React.ReactNode
}) {
  return (
    <Button type="button" variant="outline" size="sm" asChild>
      <a href={href}>
        <IconExternalLink className="size-4" />
        {children}
      </a>
    </Button>
  )
}

function DispatchLifecycle({ dispatch }: { dispatch: DispatchView }) {
  const { t } = useTranslation()
  return (
    <EventPanel
      title={t("pages.events.dispatch_detail.lifecycle", "Lifecycle")}
      className="xl:col-span-2"
    >
      <dl className="grid min-w-0 grid-cols-2 gap-3 lg:grid-cols-4">
        <EventMeta
          label={t("pages.events.dispatch_detail.created", "Created")}
          value={formatEventDate(dispatch.created_at)}
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.available", "Available")}
          value={formatEventDate(dispatch.available_at)}
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.linked", "Run linked")}
          value={formatEventDate(dispatch.linked_at)}
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.updated", "Updated")}
          value={formatEventDate(dispatch.updated_at)}
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.lease_until", "Lease until")}
          value={formatEventDate(dispatch.lease_until)}
        />
        <EventMeta
          label={t("pages.events.dispatch_detail.finished", "Finished")}
          value={formatEventDate(dispatch.finished_at)}
        />
      </dl>
    </EventPanel>
  )
}

function DetailMessage({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-muted-foreground flex min-h-48 items-center justify-center px-4 text-center text-sm">
      {children}
    </div>
  )
}

function DetailError({
  message,
  retryLabel,
  onRetry,
}: {
  message: string
  retryLabel: string
  onRetry: () => void
}) {
  return (
    <div
      role="alert"
      className="flex min-h-48 flex-col items-center justify-center gap-2 px-4 text-center"
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
