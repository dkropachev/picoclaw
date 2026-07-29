import type { WorkflowDependencyCheckResponse } from "@/api/workflows"

export type WorkflowDependencyFenceState =
  | "idle"
  | "loading"
  | "error"
  | "stale"
  | "current"

export type WorkflowDependencyFenceStatus =
  | "idle"
  | "loading"
  | "unavailable"
  | "stale"
  | "workflows-disabled"
  | "structural-blocked"
  | "runtime-blocked"
  | "blocked"
  | "ready"

export interface WorkflowDependencyFence {
  status: WorkflowDependencyFenceStatus
  revision?: string
}

export function workflowDependencyFence(
  workflowRef: string | null,
  state: WorkflowDependencyFenceState,
  report?: WorkflowDependencyCheckResponse,
): WorkflowDependencyFence {
  if (workflowRef == null || workflowRef.trim() === "" || state === "idle") {
    return { status: "idle" }
  }
  if (state === "loading") {
    return { status: "loading" }
  }
  if (state === "stale") {
    return { status: "stale" }
  }
  if (
    state === "error" ||
    report == null ||
    report.root_ref !== workflowRef ||
    report.revision.trim() === ""
  ) {
    return {
      status:
        report != null && report.root_ref !== workflowRef
          ? "stale"
          : "unavailable",
    }
  }
  if (!report.workflow_enabled) {
    return { status: "workflows-disabled" }
  }
  if (!report.structural_ready) {
    return { status: "structural-blocked" }
  }
  if (!report.runtime_ready) {
    return { status: "runtime-blocked" }
  }
  if (!report.ready) {
    return { status: "blocked" }
  }
  return { status: "ready", revision: report.revision }
}

export function workflowDependencyFenceMessage(
  action: "run" | "retry",
  fence: WorkflowDependencyFence,
) {
  const verb = action === "run" ? "running" : "retrying"
  switch (fence.status) {
    case "idle":
      return action === "run"
        ? "Select a published workflow to run."
        : "Select a published workflow run to retry."
    case "loading":
      return `Checking dependencies before ${verb}…`
    case "stale":
      return "Waiting for a fresh dependency readiness check."
    case "unavailable":
      return "Dependency readiness could not be checked. Retry the dependency check."
    case "workflows-disabled":
      return `Enable workflows in Settings before ${verb}.`
    case "structural-blocked":
      return `Resolve the structural dependency blockers before ${verb}.`
    case "runtime-blocked":
      return `Resolve the runtime dependency blockers before ${verb}.`
    case "blocked":
      return `Resolve the dependency blockers before ${verb}.`
    case "ready":
      return action === "run" ? "Ready to run." : "Ready to retry."
  }
}
