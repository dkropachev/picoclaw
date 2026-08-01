import type { ReactNode } from "react"

import type {
  WorkflowDependencyCheckResponse,
  WorkflowDependencyIssue,
  WorkflowDependencyReadiness,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export type WorkflowDependencyCheckState =
  | "idle"
  | "loading"
  | "error"
  | "stale"
  | "current"

export function WorkflowPublishReadinessPanel({
  targetReady,
  yamlReady,
  validationStatus,
  testStatus,
  dependencyState,
  dependencyReport,
  readinessMessage,
}: {
  targetReady: boolean
  yamlReady: boolean
  validationStatus: string
  testStatus: string
  dependencyState: WorkflowDependencyCheckState
  dependencyReport?: WorkflowDependencyCheckResponse
  readinessMessage: string
}) {
  return (
    <section
      aria-label="Publish readiness"
      className="border-border bg-background/60 rounded-lg border p-4"
    >
      <h3 className="mb-3 text-sm font-medium">Publish readiness</h3>
      <div className="grid gap-2">
        <ReadinessRow
          label="Target"
          status={targetReady ? "ready" : "missing"}
        />
        <ReadinessRow label="YAML" status={yamlReady ? "ready" : "missing"} />
        <ReadinessRow label="Validation" status={validationStatus} />
        <ReadinessRow label="Latest test" status={testStatus} />
        <ReadinessRow
          label="Dependencies"
          status={dependencyStatus(dependencyState, dependencyReport)}
        />
      </div>

      <DependencyDetails state={dependencyState} report={dependencyReport} />

      <div className="text-muted-foreground border-border/70 mt-3 border-t pt-2 text-xs">
        {readinessMessage}
      </div>
    </section>
  )
}

export function WorkflowDependencyReadinessPanel({
  workflowRef,
  dependencyState,
  dependencyReport,
  onRetry,
  ariaLabel = "Published workflow dependency readiness",
  heading = "Dependency readiness",
  idleMessage = "Select a published workflow to inspect its dependencies.",
  loadingMessage = "Checking dependencies for the selected published workflow…",
  staleMessage = "The dependency result is stale. Waiting for a fresh published workflow check…",
  unavailableMessage = "Published workflow dependency readiness is unavailable.",
}: {
  workflowRef: string
  dependencyState: WorkflowDependencyCheckState
  dependencyReport?: WorkflowDependencyCheckResponse
  onRetry: () => void
  ariaLabel?: string
  heading?: string
  idleMessage?: string
  loadingMessage?: string
  staleMessage?: string
  unavailableMessage?: string
}) {
  return (
    <section aria-label={ariaLabel} className="grid min-w-0 gap-3 p-3">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="text-sm font-medium">{heading}</h3>
          <p className="text-muted-foreground mt-0.5 truncate font-mono text-xs">
            {workflowRef}
          </p>
        </div>
        <ReadinessBadge
          status={dependencyStatus(dependencyState, dependencyReport)}
        />
      </div>

      <DependencyDetails
        state={dependencyState}
        report={dependencyReport}
        context="published"
        idleMessage={idleMessage}
        loadingMessage={loadingMessage}
        staleMessage={staleMessage}
        unavailableMessage={unavailableMessage}
      />

      {dependencyState === "error" ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="justify-self-start"
          onClick={onRetry}
        >
          Retry dependency check
        </Button>
      ) : null}
    </section>
  )
}

function DependencyDetails({
  state,
  report,
  context = "draft",
  idleMessage = "Complete the target and YAML to check dependencies.",
  loadingMessage,
  staleMessage,
  unavailableMessage = "Workflow dependency readiness is unavailable. Edit the draft or refresh to try again.",
}: {
  state: WorkflowDependencyCheckState
  report?: WorkflowDependencyCheckResponse
  context?: "draft" | "published"
  idleMessage?: string
  loadingMessage?: string
  staleMessage?: string
  unavailableMessage?: string
}) {
  if (state === "idle") {
    return <ReadinessNotice>{idleMessage}</ReadinessNotice>
  }
  if (state === "loading") {
    return (
      <ReadinessNotice role="status">
        {loadingMessage ??
          (context === "published"
            ? "Checking dependencies for the selected published workflow…"
            : "Checking dependencies for the exact current draft…")}
      </ReadinessNotice>
    )
  }
  if (state === "stale") {
    return (
      <ReadinessNotice role="status" warning>
        {staleMessage ??
          (context === "published"
            ? "The dependency result is stale. Waiting for a fresh published workflow check…"
            : "The dependency result is stale. Waiting for the current draft check…")}
      </ReadinessNotice>
    )
  }
  if (state === "error" || report == null) {
    return (
      <ReadinessNotice role="alert" destructive>
        {unavailableMessage}
      </ReadinessNotice>
    )
  }

  return (
    <div className="mt-3 grid min-w-0 gap-3">
      {!report.workflow_enabled ? (
        <ReadinessNotice role="alert" destructive flush>
          Workflows are disabled. Enable workflows in Settings
          {context === "draft" ? " before publishing" : ""}.
        </ReadinessNotice>
      ) : null}

      {report.structural_issues.length > 0 ? (
        <div className="grid min-w-0 gap-2">
          <h4 className="text-xs font-medium">
            Structural blockers ({report.structural_issues.length})
          </h4>
          <ul className="grid min-w-0 gap-2">
            {report.structural_issues.map((issue, index) => (
              <StructuralIssue
                key={`${issue.workflow_ref}-${issue.path}-${issue.code}-${index}`}
                issue={issue}
              />
            ))}
          </ul>
        </div>
      ) : !report.structural_ready ? (
        <ReadinessNotice role="alert" destructive flush>
          Structural dependency checks did not pass.{" "}
          {context === "published"
            ? "Retry the published workflow dependency check."
            : "Refresh the draft check before publishing."}
        </ReadinessNotice>
      ) : null}

      <div className="grid min-w-0 gap-2">
        <h4 className="text-xs font-medium">
          Runtime dependencies ({report.dependencies.length})
        </h4>
        {report.dependencies.length === 0 ? (
          <div className="text-muted-foreground rounded-md border border-dashed px-3 py-3 text-xs">
            No declared runtime dependencies.
          </div>
        ) : (
          <ul className="grid min-w-0 gap-2">
            {report.dependencies.map((item, index) => (
              <RuntimeReadiness
                key={`${item.dependency.workflow_ref}-${item.dependency.path}-${item.dependency.kind}-${item.dependency.name}-${index}`}
                item={item}
              />
            ))}
          </ul>
        )}
      </div>

      {!report.runtime_ready && report.dependencies.length === 0 ? (
        <ReadinessNotice role="alert" destructive flush>
          Runtime dependency checks did not pass.{" "}
          {context === "published"
            ? "Retry the published workflow dependency check."
            : "Refresh the draft check before publishing."}
        </ReadinessNotice>
      ) : null}
    </div>
  )
}

function StructuralIssue({ issue }: { issue: WorkflowDependencyIssue }) {
  return (
    <li className="border-destructive/30 bg-destructive/5 min-w-0 rounded-md border px-3 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <ReadinessBadge status="blocked" />
        <span className="font-mono text-xs">
          {structuralCodeLabel(issue.code)}
        </span>
        {issue.dependency_name ? (
          <span className="text-muted-foreground min-w-0 text-xs break-all">
            {issue.dependency_kind
              ? `${issue.dependency_kind}/${issue.dependency_name}`
              : issue.dependency_name}
          </span>
        ) : null}
      </div>
      <div className="text-muted-foreground mt-1 font-mono text-[11px] break-all">
        {formatLocation(issue.workflow_ref, issue.path)}
      </div>
      <p className="mt-1 text-xs">{structuralGuidance(issue.code)}</p>
    </li>
  )
}

function RuntimeReadiness({ item }: { item: WorkflowDependencyReadiness }) {
  const { dependency } = item
  const code = safeRuntimeCode(item.code)
  return (
    <li className="border-border/70 min-w-0 rounded-md border px-3 py-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <ReadinessBadge
          status={item.ready && code === "ready" ? "ready" : code}
        />
        <span className="min-w-0 font-mono text-xs break-all">
          {dependency.kind}/{dependency.name}
        </span>
      </div>
      <div className="text-muted-foreground mt-1 font-mono text-[11px] break-all">
        {formatLocation(dependency.workflow_ref, dependency.path)}
      </div>
      <p className="mt-1 text-xs">{runtimeGuidance(code)}</p>
    </li>
  )
}

function ReadinessRow({ label, status }: { label: string; status: string }) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 text-sm">
      <span className="text-muted-foreground min-w-0 truncate">{label}</span>
      <ReadinessBadge status={status} />
    </div>
  )
}

function ReadinessBadge({ status }: { status: string }) {
  const destructive = [
    "blocked",
    "disabled",
    "error",
    "failed",
    "invalid",
    "invalid_configuration",
    "missing",
    "name_collision",
    "not_allowed",
    "not_configured",
    "not_connected",
    "not_found",
    "unavailable",
    "validation_failed",
  ].includes(status)
  const ready = [
    "ready",
    "succeeded",
    "valid",
    "runnable",
    "needs_review",
  ].includes(status)
  return (
    <Badge
      variant={destructive ? "default" : ready ? "secondary" : "outline"}
      className={cn(
        "shrink-0 capitalize",
        destructive && "bg-destructive dark:text-background text-white",
      )}
    >
      {formatCode(status)}
    </Badge>
  )
}

function ReadinessNotice({
  children,
  role,
  destructive,
  warning,
  flush,
}: {
  children: ReactNode
  role?: "alert" | "status"
  destructive?: boolean
  warning?: boolean
  flush?: boolean
}) {
  return (
    <div
      role={role}
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        !flush && "mt-3",
        destructive
          ? "border-destructive/40 bg-destructive/10 text-destructive"
          : warning
            ? "border-amber-500/40 bg-amber-500/10 text-amber-800 dark:text-amber-200"
            : "text-muted-foreground border-dashed",
      )}
    >
      {children}
    </div>
  )
}

function dependencyStatus(
  state: WorkflowDependencyCheckState,
  report?: WorkflowDependencyCheckResponse,
) {
  switch (state) {
    case "current":
      return report?.ready ? "ready" : "blocked"
    case "loading":
      return "checking"
    case "error":
      return "unavailable"
    default:
      return state === "idle" ? "pending" : state
  }
}

function structuralGuidance(code: string) {
  switch (code) {
    case "invalid_reusable_ref":
      return "Use a canonical workspace-local workflows/*.yml reference."
    case "reusable_unavailable":
      return "Install or restore the referenced reusable workflow."
    case "reusable_invalid":
      return "Fix validation errors in the referenced reusable workflow."
    case "reusable_cycle":
      return "Remove the cycle between reusable workflows."
    case "call_depth_exceeded":
      return "Reduce reusable workflow nesting to the configured call-depth limit."
    case "missing_required_input":
      return "Map the required input or add a default in the reusable workflow."
    case "input_type_mismatch":
      return "Change the mapped input to match the reusable workflow input type."
    case "invalid_secrets":
      return 'Use "inherit" or a named secret mapping.'
    case "missing_required_secret":
      return "Map or inherit the required reusable workflow secret."
    case "human_task_reusable_unsupported":
      return "Keep human/task and reusable workflow calls in separate workflow closures."
    case "analysis_limit_exceeded":
      return "Reduce the reusable workflow closure or split it into smaller workflows."
    default:
      return "Resolve this structural dependency blocker."
  }
}

function structuralCodeLabel(code: string) {
  switch (code) {
    case "invalid_reusable_ref":
    case "reusable_unavailable":
    case "reusable_invalid":
    case "reusable_cycle":
    case "call_depth_exceeded":
    case "missing_required_input":
    case "input_type_mismatch":
    case "invalid_secrets":
    case "missing_required_secret":
    case "human_task_reusable_unsupported":
    case "analysis_limit_exceeded":
      return formatCode(code)
    default:
      return "structural blocker"
  }
}

function safeRuntimeCode(code: string) {
  switch (code) {
    case "ready":
    case "unchecked":
    case "not_configured":
    case "disabled":
    case "not_allowed":
    case "not_connected":
    case "not_found":
    case "invalid_configuration":
    case "name_collision":
    case "unavailable":
      return code
    default:
      return "unavailable"
  }
}

function runtimeGuidance(code: string) {
  switch (code) {
    case "ready":
      return "Ready now."
    case "unchecked":
      return "Readiness was not checked. Refresh after the runtime is available."
    case "not_configured":
      return "Configure this capability before publishing."
    case "disabled":
      return "Enable this capability or its owning subsystem."
    case "not_allowed":
      return "Allow this capability in runtime policy."
    case "not_connected":
      return "Connect or start the required runtime server."
    case "not_found":
      return "Install the capability or correct its declared name."
    case "invalid_configuration":
      return "Fix this capability's runtime configuration."
    case "name_collision":
      return "Resolve duplicate capabilities with the same canonical name."
    case "unavailable":
      return "Check the runtime and retry when this capability is available."
    default:
      return "Resolve this runtime dependency blocker."
  }
}

function formatLocation(workflowRef: string, path: string) {
  return path.trim() === "" ? workflowRef : `${workflowRef} · ${path}`
}

function formatCode(code: string) {
  return code.replaceAll("_", " ")
}
