import {
  IconBraces,
  IconChevronDown,
  IconChevronUp,
  IconDownload,
} from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import { type DispatchView, type EventView, getEvent } from "@/api/events"
import { type WorkflowRun, getWorkflowRun } from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"

import { eventErrorMessage } from "./event-format"
import { EventPanel, EventScrollRegion } from "./event-ui"

const eventAttributeOrder = [
  "repository_full_name",
  "pull_request_number",
  "pull_request_url",
  "pull_request_author",
  "pull_request_base_ref",
  "pull_request_base_sha",
  "pull_request_head_ref",
  "pull_request_head_sha",
  "pull_request_draft",
  "notification_reason",
  "target_user",
  "target_reason",
  "targets_user",
  "source_authenticated",
  "provider_authenticated",
]

export function DispatchParameters({ dispatch }: { dispatch: DispatchView }) {
  const { t } = useTranslation()
  const [fullParametersRequested, setFullParametersRequested] = useState(false)
  const [showFullParameters, setShowFullParameters] = useState(false)
  const runLinked = dispatch.run_id !== "" && dispatch.linked_at != null

  const eventQuery = useQuery({
    queryKey: ["events", "detail", dispatch.event_id],
    queryFn: () => getEvent(dispatch.event_id),
    staleTime: 30_000,
  })
  const eventIdentityMismatch =
    eventQuery.data != null && eventQuery.data.id !== dispatch.event_id
  const event = eventIdentityMismatch ? undefined : eventQuery.data

  const runQuery = useQuery({
    queryKey: ["workflows", "runs", dispatch.run_id],
    queryFn: () => getWorkflowRun(dispatch.run_id),
    enabled: runLinked && fullParametersRequested && event != null,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 0,
  })
  const run = runQuery.data
  const verified =
    run != null && event != null && runMatchesDispatch(run, dispatch, event)
  const inputs = useMemo(
    () => (verified ? recordValue(run.inputs) : {}),
    [run, verified],
  )
  const fullParameters = useMemo(
    () =>
      showFullParameters && verified
        ? JSON.stringify(inputs, null, 2) || "{}"
        : "",
    [inputs, showFullParameters, verified],
  )

  const requestFullParameters = () => {
    setFullParametersRequested(true)
    setShowFullParameters(true)
  }

  return (
    <EventPanel
      title={t(
        "pages.events.dispatch_detail.parameters",
        "Workflow parameters",
      )}
      titleExtra={<IconBraces className="text-muted-foreground size-4" />}
      className="xl:col-span-2"
    >
      <div className="grid min-w-0 gap-4">
        <p className="text-muted-foreground text-xs">
          {t(
            "pages.events.dispatch_detail.parameters_description",
            "Dispatch inputs and payload-free event context for this workflow invocation.",
          )}
        </p>

        <InvocationSummary
          dispatch={dispatch}
          event={event}
          loading={eventQuery.isPending}
          error={eventQuery.error}
          identityMismatch={eventIdentityMismatch}
          onRetry={() => void eventQuery.refetch()}
        />

        <div className="border-border/70 grid min-w-0 gap-2 border-t pt-3">
          <p className="text-muted-foreground text-xs">
            {t(
              "pages.events.dispatch_detail.parameters_full_description",
              "Full persisted inputs may include nested event payload content.",
            )}
          </p>

          {!runLinked ? (
            <ParameterMessage>
              {t(
                "pages.events.dispatch_detail.parameters_not_linked",
                "Full parameters become available after the workflow run is linked.",
              )}
            </ParameterMessage>
          ) : !fullParametersRequested ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-fit"
              onClick={requestFullParameters}
            >
              <IconDownload className="size-4" />
              {t(
                "pages.events.dispatch_detail.parameters_load",
                "Load full parameters",
              )}
            </Button>
          ) : eventQuery.isPending ? (
            <ParameterMessage role="status">
              {t(
                "pages.events.dispatch_detail.parameters_waiting_for_summary",
                "Waiting for the invocation summary…",
              )}
            </ParameterMessage>
          ) : eventQuery.error || eventIdentityMismatch || event == null ? (
            <ParameterError
              message={t(
                "pages.events.dispatch_detail.parameters_summary_required",
                "Full workflow parameters require a verified invocation summary.",
              )}
              retryLabel={t(
                "pages.events.dispatch_detail.parameters_summary_retry",
                "Retry summary",
              )}
              onRetry={() => void eventQuery.refetch()}
            />
          ) : runQuery.isPending ? (
            <ParameterMessage role="status">
              {t(
                "pages.events.dispatch_detail.parameters_loading",
                "Loading full workflow parameters…",
              )}
            </ParameterMessage>
          ) : runQuery.error ? (
            <ParameterError
              message={eventErrorMessage(
                runQuery.error,
                t(
                  "pages.events.dispatch_detail.parameters_error",
                  "Failed to load workflow parameters.",
                ),
              )}
              retryLabel={t(
                "pages.events.dispatch_detail.parameters_retry",
                "Retry parameters",
              )}
              onRetry={() => void runQuery.refetch()}
            />
          ) : !verified ? (
            <ParameterError
              message={t(
                "pages.events.dispatch_detail.parameters_mismatch",
                "Workflow parameters could not be verified for this dispatch.",
              )}
              retryLabel={t(
                "pages.events.dispatch_detail.parameters_retry",
                "Retry parameters",
              )}
              onRetry={() => void runQuery.refetch()}
            />
          ) : (
            <Collapsible
              open={showFullParameters}
              onOpenChange={setShowFullParameters}
              className="grid min-w-0 gap-2"
            >
              <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
                <Badge variant="outline" className="font-mono">
                  {Object.keys(inputs).length}{" "}
                  {t(
                    "pages.events.dispatch_detail.parameters_input_count",
                    "inputs",
                  )}
                </Badge>
                <CollapsibleTrigger asChild>
                  <Button type="button" variant="outline" size="sm">
                    {showFullParameters ? (
                      <IconChevronUp className="size-4" />
                    ) : (
                      <IconChevronDown className="size-4" />
                    )}
                    {showFullParameters
                      ? t(
                          "pages.events.dispatch_detail.parameters_hide",
                          "Hide full parameters",
                        )
                      : t(
                          "pages.events.dispatch_detail.parameters_show",
                          "Show full parameters",
                        )}
                  </Button>
                </CollapsibleTrigger>
              </div>
              <CollapsibleContent>
                <EventScrollRegion
                  label={t(
                    "pages.events.dispatch_detail.parameters_json_region",
                    "Workflow parameters JSON",
                  )}
                  className="bg-muted/50 max-h-96 overflow-auto rounded-md p-3 font-mono text-xs"
                >
                  <pre className="m-0 min-w-max whitespace-pre">
                    {fullParameters}
                  </pre>
                </EventScrollRegion>
              </CollapsibleContent>
            </Collapsible>
          )}
        </div>
      </div>
    </EventPanel>
  )
}

function InvocationSummary({
  dispatch,
  event,
  loading,
  error,
  identityMismatch,
  onRetry,
}: {
  dispatch: DispatchView
  event?: EventView
  loading: boolean
  error: unknown
  identityMismatch: boolean
  onRetry: () => void
}) {
  const { t } = useTranslation()
  if (loading) {
    return (
      <ParameterMessage role="status">
        {t(
          "pages.events.dispatch_detail.parameters_summary_loading",
          "Loading invocation summary…",
        )}
      </ParameterMessage>
    )
  }
  if (error || event == null) {
    return (
      <ParameterError
        message={
          identityMismatch
            ? t(
                "pages.events.dispatch_detail.parameters_summary_mismatch",
                "Invocation summary could not be verified for this dispatch.",
              )
            : eventErrorMessage(
                error,
                t(
                  "pages.events.dispatch_detail.parameters_summary_error",
                  "Failed to load the invocation summary.",
                ),
              )
        }
        retryLabel={t(
          "pages.events.dispatch_detail.parameters_summary_retry",
          "Retry summary",
        )}
        onRetry={onRetry}
      />
    )
  }

  const inputs: Array<[string, string]> = [
    ["source", event.source],
    ["connector", event.connector],
    ["type", event.type],
    ["event_id", dispatch.event_id],
    ["dispatch_id", dispatch.id],
  ]
  const eventAttributes = orderedScalarEntries(
    recordValue(event.attributes),
    eventAttributeOrder,
  )

  return (
    <div className="grid min-w-0 gap-4">
      <ParameterList entries={inputs} />
      {eventAttributes.length > 0 ? (
        <section className="grid min-w-0 gap-2">
          <div className="flex min-w-0 items-center justify-between gap-2">
            <h4 className="text-sm font-medium">
              {t(
                "pages.events.dispatch_detail.event_parameters",
                "Event parameters",
              )}
            </h4>
            <Badge variant="outline" className="font-mono">
              {eventAttributes.length}
            </Badge>
          </div>
          <ParameterList entries={eventAttributes} />
        </section>
      ) : null}
    </div>
  )
}

function ParameterList({ entries }: { entries: Array<[string, string]> }) {
  return (
    <dl className="grid min-w-0 gap-2 sm:grid-cols-2 lg:grid-cols-3">
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

function ParameterMessage({
  children,
  role,
}: {
  children: React.ReactNode
  role?: "status"
}) {
  return (
    <p role={role} className="text-muted-foreground py-2 text-sm">
      {children}
    </p>
  )
}

function ParameterError({
  message,
  retryLabel,
  onRetry,
}: {
  message: string
  retryLabel: string
  onRetry: () => void
}) {
  return (
    <div role="alert" className="grid gap-2">
      <p className="text-destructive text-sm break-words">{message}</p>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={onRetry}
      >
        {retryLabel}
      </Button>
    </div>
  )
}

function runMatchesDispatch(
  run: WorkflowRun,
  dispatch: DispatchView,
  event: EventView,
): boolean {
  const inputs = recordValue(run.inputs)
  const inputEvent = recordValue(inputs.event)
  return (
    event.id === dispatch.event_id &&
    run.id === dispatch.run_id &&
    run.workflow_ref === dispatch.workflow_ref &&
    run.origin?.kind === "external_event" &&
    run.origin.event_id === dispatch.event_id &&
    run.origin.dispatch_id === dispatch.id &&
    run.origin.root_run_id === run.id &&
    inputs.source === event.source &&
    inputs.connector === event.connector &&
    inputs.type === event.type &&
    inputs.event_id === dispatch.event_id &&
    inputs.dispatch_id === dispatch.id &&
    inputEvent.id === dispatch.event_id
  )
}

function orderedScalarEntries(
  value: Record<string, unknown>,
  preferredOrder: string[],
): Array<[string, string]> {
  const order = new Map(
    preferredOrder.map((key, index) => [key, index] as const),
  )
  return Object.entries(value)
    .flatMap(([key, item]) => {
      const formatted = scalarValue(item)
      return formatted == null ? [] : [[key, formatted] as [string, string]]
    })
    .sort(([left], [right]) => {
      const leftOrder = order.get(left) ?? Number.MAX_SAFE_INTEGER
      const rightOrder = order.get(right) ?? Number.MAX_SAFE_INTEGER
      return leftOrder - rightOrder || left.localeCompare(right)
    })
}

function scalarValue(value: unknown): string | null {
  if (value === null) {
    return "null"
  }
  if (
    typeof value === "string" ||
    typeof value === "number" ||
    typeof value === "boolean"
  ) {
    return String(value)
  }
  return null
}

function recordValue(value: unknown): Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {}
}
