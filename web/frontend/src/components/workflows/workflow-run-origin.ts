import type { WorkflowRunOrigin } from "@/api/workflows"

const eventIDPattern = /^ev_[0-9a-f]{32}$/
const dispatchIDPattern = /^dsp_[0-9a-f]{32}$/
const workflowRunIDPattern = /^wr_[A-Za-z0-9_-]+$/
const maximumWorkflowRunIDBytes = 1024

export function trustedWorkflowRunOrigin(
  value: unknown,
): WorkflowRunOrigin | undefined {
  if (value == null || typeof value !== "object" || Array.isArray(value)) {
    return undefined
  }
  const origin = value as Record<string, unknown>
  if (
    (origin.kind !== "external_event" &&
      origin.kind !== "external_event_draft_test") ||
    typeof origin.event_id !== "string" ||
    !eventIDPattern.test(origin.event_id) ||
    typeof origin.root_run_id !== "string" ||
    new TextEncoder().encode(origin.root_run_id).byteLength >
      maximumWorkflowRunIDBytes ||
    !workflowRunIDPattern.test(origin.root_run_id)
  ) {
    return undefined
  }
  if (
    origin.dispatch_id !== undefined &&
    (typeof origin.dispatch_id !== "string" ||
      !dispatchIDPattern.test(origin.dispatch_id))
  ) {
    return undefined
  }
  if (
    (origin.kind === "external_event" &&
      typeof origin.dispatch_id !== "string") ||
    (origin.kind === "external_event_draft_test" &&
      origin.dispatch_id !== undefined)
  ) {
    return undefined
  }
  return {
    kind: origin.kind,
    event_id: origin.event_id,
    ...(origin.dispatch_id ? { dispatch_id: origin.dispatch_id } : {}),
    root_run_id: origin.root_run_id,
  }
}
