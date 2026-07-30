import {
  IconAlertTriangle,
  IconChevronRight,
  IconRefresh,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { type ReactNode, useState } from "react"

import {
  type WorkflowDefinitionInspection,
  type WorkflowDefinitionInspectionSource,
  inspectPublishedWorkflowDefinition,
  inspectWorkflowTemplate,
  workflowTriggerKinds,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"

export type WorkflowDefinitionInspectorTarget =
  | { kind: "published"; ref: string }
  | { kind: "template"; name: string }

const workflowDefinitionInspectionQueryKey = [
  "workflows",
  "definition-inspections",
] as const

// Keyboard focus is required for these independently scrollable inspection regions.
/* eslint-disable jsx-a11y/no-noninteractive-tabindex */
function InspectionScrollRegion({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div
      role="region"
      tabIndex={0}
      aria-label={`${label} inspection details`}
      className="border-border/70 max-h-[min(32rem,50dvh)] overflow-y-auto border-t p-2.5"
    >
      {children}
    </div>
  )
}

function InspectionTriggerValue({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <pre
      tabIndex={0}
      aria-label={`${label} trigger details`}
      className="text-muted-foreground mt-1 max-h-28 overflow-auto font-mono text-[10px] leading-4 break-all whitespace-pre-wrap"
    >
      {children}
    </pre>
  )
}
/* eslint-enable jsx-a11y/no-noninteractive-tabindex */

export function WorkflowDefinitionInspector({
  target,
  defaultOpen = true,
  className,
}: {
  target: WorkflowDefinitionInspectorTarget
  defaultOpen?: boolean
  className?: string
}) {
  const [open, setOpen] = useState(defaultOpen)
  const [activated, setActivated] = useState(defaultOpen)
  const queryClient = useQueryClient()
  const identity = target.kind === "published" ? target.ref : target.name
  const queryKey = [
    ...workflowDefinitionInspectionQueryKey,
    target.kind,
    identity,
  ] as const
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      target.kind === "published"
        ? inspectPublishedWorkflowDefinition(target.ref, signal)
        : inspectWorkflowTemplate(target.name, signal),
    enabled: activated && open && identity.trim() !== "",
    retry: false,
  })
  const label =
    target.kind === "published" ? "Published definition" : "Built-in definition"
  const accessibleLabel = `${label}: ${identity}`
  const loading =
    query.isFetching || (query.isPending && query.data === undefined)

  return (
    <Collapsible
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen)
        if (nextOpen) {
          setActivated(true)
        } else {
          void queryClient.cancelQueries({ queryKey, exact: true })
        }
      }}
      className={cn(
        "group/definition-inspector border-border/70 min-w-0 rounded-md border",
        className,
      )}
    >
      <div className="flex min-w-0 items-center justify-between gap-2 px-2.5 py-2">
        <CollapsibleTrigger asChild>
          <button
            type="button"
            aria-label={accessibleLabel}
            className="focus-visible:ring-ring flex min-w-0 flex-1 items-center gap-2 rounded-sm text-left focus-visible:ring-2 focus-visible:outline-none"
          >
            <IconChevronRight className="text-muted-foreground size-3.5 shrink-0 transition-transform group-data-[state=open]/definition-inspector:rotate-90" />
            <span className="truncate text-xs font-medium">{label}</span>
          </button>
        </CollapsibleTrigger>
        <InspectionStatusBadge
          activated={activated}
          open={open}
          loading={loading}
          failed={query.isError}
          inspection={query.data}
        />
      </div>
      <CollapsibleContent>
        <InspectionScrollRegion label={accessibleLabel}>
          {loading ? (
            <InspectionLoading />
          ) : query.isError ? (
            <InspectionError
              message={errorMessage(query.error)}
              retrying={query.isFetching}
              onRetry={() => void query.refetch()}
            />
          ) : (
            <InspectionContent inspection={query.data} />
          )}
        </InspectionScrollRegion>
      </CollapsibleContent>
    </Collapsible>
  )
}

function InspectionStatusBadge({
  activated,
  open,
  loading,
  failed,
  inspection,
}: {
  activated: boolean
  open: boolean
  loading: boolean
  failed: boolean
  inspection?: WorkflowDefinitionInspection
}) {
  if (!activated || (!open && (loading || inspection == null))) {
    return <Badge variant="outline">Review</Badge>
  }
  if (loading) {
    return <Badge variant="outline">Loading</Badge>
  }
  if (failed) {
    return <Badge variant="destructive">Unavailable</Badge>
  }
  if (inspection == null) {
    return <Badge variant="outline">Empty</Badge>
  }
  if (!inspection.validation.valid) {
    return <Badge variant="destructive">Invalid</Badge>
  }
  if (!inspection.complete) {
    return <Badge variant="outline">Incomplete</Badge>
  }
  return <Badge variant="secondary">Inspected</Badge>
}

function InspectionLoading() {
  return (
    <div
      role="status"
      className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs"
    >
      Inspecting workflow structure…
    </div>
  )
}

function InspectionError({
  message,
  retrying,
  onRetry,
}: {
  message: string
  retrying: boolean
  onRetry: () => void
}) {
  return (
    <div
      role="alert"
      className="border-destructive/30 bg-destructive/5 grid gap-2 rounded-md border px-3 py-2.5 text-xs"
    >
      <div className="text-destructive break-words">{message}</div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        disabled={retrying}
        onClick={onRetry}
      >
        <IconRefresh className="size-3.5" />
        {retrying ? "Retrying" : "Retry inspection"}
      </Button>
    </div>
  )
}

function InspectionContent({
  inspection,
}: {
  inspection: WorkflowDefinitionInspection
}) {
  const presentTriggers = workflowTriggerKinds.filter(
    (kind) => inspection.triggers[kind].present,
  )
  const empty =
    presentTriggers.length === 0 &&
    inspection.jobs.length === 0 &&
    inspection.dependencies.length === 0 &&
    inspection.effects.length === 0

  return (
    <div className="grid min-w-0 gap-3">
      <InspectionMetadata
        source={inspection.source}
        revision={inspection.revision}
      />
      {!inspection.validation.valid ? (
        <ValidationWarning inspection={inspection} />
      ) : null}
      {!inspection.complete ? (
        <IncompleteWarning limits={inspection.limits} />
      ) : null}
      {empty ? (
        <div className="text-muted-foreground rounded-md border border-dashed px-3 py-3 text-center text-xs">
          No inspectable triggers, jobs, dependencies, or possible effects.
        </div>
      ) : (
        <div className="grid min-w-0 gap-3 md:grid-cols-2">
          <InspectionGroup title="Triggers" count={presentTriggers.length}>
            {presentTriggers.length === 0 ? (
              <InspectionEmpty label="No triggers declared" />
            ) : (
              presentTriggers.map((kind) => {
                const trigger = inspection.triggers[kind]
                return (
                  <div
                    key={kind}
                    className="border-border/60 min-w-0 rounded border px-2 py-1.5"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-mono text-[11px]">
                        {displayCode(kind)}
                      </span>
                      {!trigger.projected ? (
                        <Badge variant="outline">Not projected</Badge>
                      ) : null}
                    </div>
                    {trigger.projected && trigger.value !== undefined ? (
                      <InspectionTriggerValue label={displayCode(kind)}>
                        {formatTriggerValue(trigger.value)}
                      </InspectionTriggerValue>
                    ) : null}
                  </div>
                )
              })
            )}
          </InspectionGroup>

          <InspectionGroup title="Jobs" count={inspection.jobs.length}>
            {inspection.jobs.length === 0 ? (
              <InspectionEmpty label="No jobs declared" />
            ) : (
              inspection.jobs.map((job, jobIndex) => (
                <div
                  key={`${jobIndex}:${job.id}`}
                  className="border-border/60 min-w-0 rounded border px-2 py-1.5"
                >
                  <div className="flex min-w-0 items-center justify-between gap-2">
                    <span className="truncate font-mono text-[11px]">
                      {job.id}
                    </span>
                    <Badge variant="outline">{displayCode(job.kind)}</Badge>
                  </div>
                  {job.reusable_target ? (
                    <div className="text-muted-foreground mt-1 truncate font-mono text-[10px]">
                      {job.reusable_target}
                    </div>
                  ) : null}
                  {job.steps.length === 0 ? (
                    <InspectionEmpty label="No steps" />
                  ) : (
                    <ol className="mt-1 grid gap-1">
                      {job.steps.map((step) => (
                        <li
                          key={`${step.index}:${step.id ?? ""}`}
                          className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-[10px]"
                        >
                          <span className="shrink-0 font-mono">
                            {step.index + 1}.
                          </span>
                          <span className="shrink-0">
                            {displayCode(step.kind)}
                          </span>
                          {step.id ? (
                            <span
                              className="shrink-0 font-mono"
                              title={`Step ID: ${step.id}`}
                            >
                              #{step.id}
                            </span>
                          ) : null}
                          <span
                            className="truncate font-mono"
                            title={step.target ?? "target omitted"}
                          >
                            {step.target || "target omitted"}
                          </span>
                        </li>
                      ))}
                    </ol>
                  )}
                </div>
              ))
            )}
          </InspectionGroup>

          <InspectionGroup
            title="Dependencies"
            count={inspection.dependencies.length}
          >
            {inspection.dependencies.length === 0 ? (
              <InspectionEmpty label="No external dependencies detected" />
            ) : (
              <InspectionRows
                rows={inspection.dependencies.map((dependency) => ({
                  key: `${dependency.kind}:${dependency.target}`,
                  kind: dependency.kind,
                  target: dependency.target,
                  occurrences: dependency.occurrences,
                }))}
              />
            )}
          </InspectionGroup>

          <InspectionGroup
            title="Possible effects"
            count={inspection.effects.length}
            description="Conservative preview; a run may produce fewer effects."
          >
            {inspection.effects.length === 0 ? (
              <InspectionEmpty label="No possible effects detected" />
            ) : (
              <InspectionRows
                rows={inspection.effects.map((effect, index) => ({
                  key: `${effect.kind}:${effect.target ?? ""}:${index}`,
                  kind: effect.kind,
                  target: effect.target ?? "unspecified target",
                  occurrences: effect.occurrences,
                }))}
              />
            )}
          </InspectionGroup>
        </div>
      )}
    </div>
  )
}

function InspectionMetadata({
  source,
  revision,
}: {
  source: WorkflowDefinitionInspectionSource
  revision: string
}) {
  const identity =
    source.kind === "published" ? source.ref : source.template_name
  return (
    <div className="text-muted-foreground grid min-w-0 gap-1 text-[10px] sm:grid-cols-[minmax(0,1fr)_auto]">
      <span className="truncate font-mono" title={identity}>
        {identity}
      </span>
      <span className="truncate font-mono" title={revision}>
        revision {shortRevision(revision)}
      </span>
    </div>
  )
}

function ValidationWarning({
  inspection,
}: {
  inspection: WorkflowDefinitionInspection
}) {
  const validation = inspection.validation
  return (
    <div
      role="alert"
      className="border-destructive/30 bg-destructive/5 rounded-md border px-2.5 py-2 text-xs"
    >
      <div className="text-destructive flex items-center gap-1.5 font-medium">
        <IconAlertTriangle className="size-3.5 shrink-0" />
        Invalid workflow definition ({validation.issue_count}{" "}
        {validation.issue_count === 1 ? "issue" : "issues"})
      </div>
      {validation.issues.length > 0 ? (
        <ul className="text-muted-foreground mt-1.5 grid gap-1">
          {validation.issues.map((issue, index) => (
            <li key={`${issue.code}:${issue.scope}:${index}`}>
              <span className="font-mono">{displayCode(issue.code)}</span>
              {issue.scope ? ` · ${displayCode(issue.scope)}` : ""}
            </li>
          ))}
        </ul>
      ) : null}
      {validation.truncated ? (
        <div className="text-muted-foreground mt-1">
          Additional validation issues were omitted.
        </div>
      ) : null}
    </div>
  )
}

function IncompleteWarning({ limits }: { limits: string[] }) {
  return (
    <div
      role="status"
      className="rounded-md border border-amber-500/30 bg-amber-500/10 px-2.5 py-2 text-xs"
    >
      <div className="font-medium">Inspection is incomplete</div>
      <div className="text-muted-foreground mt-1">
        Safe inspection limits omitted part of this definition.
      </div>
      {limits.length > 0 ? (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {limits.map((limit) => (
            <Badge key={limit} variant="outline">
              {displayCode(limit)}
            </Badge>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function InspectionGroup({
  title,
  count,
  description,
  children,
}: {
  title: string
  count: number
  description?: string
  children: ReactNode
}) {
  return (
    <section className="min-w-0">
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <h4 className="text-xs font-medium">{title}</h4>
        <Badge variant="outline">{count}</Badge>
      </div>
      {description ? (
        <p className="text-muted-foreground mb-1.5 text-[10px]">
          {description}
        </p>
      ) : null}
      <div className="grid min-w-0 gap-1.5">{children}</div>
    </section>
  )
}

function InspectionRows({
  rows,
}: {
  rows: Array<{
    key: string
    kind: string
    target: string
    occurrences: number
  }>
}) {
  return rows.map((row) => (
    <div
      key={row.key}
      className="border-border/60 flex min-w-0 items-center gap-2 rounded border px-2 py-1.5 text-[10px]"
    >
      <Badge variant="outline">{displayCode(row.kind)}</Badge>
      <span className="min-w-0 flex-1 truncate font-mono" title={row.target}>
        {row.target}
      </span>
      <span
        className="text-muted-foreground shrink-0"
        aria-label={`${row.occurrences} occurrences`}
      >
        ×{row.occurrences}
      </span>
    </div>
  ))
}

function InspectionEmpty({ label }: { label: string }) {
  return (
    <div className="text-muted-foreground rounded border border-dashed px-2 py-1.5 text-[10px]">
      {label}
    </div>
  )
}

function formatTriggerValue(value: unknown) {
  return JSON.stringify(value, null, 2) ?? "null"
}

function displayCode(value: string) {
  return value.replaceAll("_", " ")
}

function shortRevision(revision: string) {
  return revision.length > 18 ? `${revision.slice(0, 18)}…` : revision
}

function errorMessage(error: unknown) {
  return error instanceof Error && error.message.trim() !== ""
    ? error.message
    : "Workflow definition inspection is unavailable."
}
