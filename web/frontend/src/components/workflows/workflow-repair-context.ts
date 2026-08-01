import type { WorkflowRun, WorkflowRunEvent } from "@/api/workflows"

export interface WorkflowDraftTestRepairResult {
  runID?: string
  eventID?: string
  status: string
  error?: string
}

const promptBlockLimit = 4000
const diagnosticLimit = 600
const identityLimit = 240
const eventPathLimit = 64
const eventPathDepthLimit = 6

export function workflowDraftTestRepairPrompt(
  prompt: string,
  result: WorkflowDraftTestRepairResult | null,
  stale: boolean,
  run?: WorkflowRun,
  events?: WorkflowRunEvent[],
) {
  const base = prompt.trim()
  const lines = [
    base === "" ? "Fix the workflow draft so its draft test passes." : base,
  ]
  if (result != null && canFixDraftTestWithAI(result, stale)) {
    const eventBacked = Boolean(result.eventID)
    lines.push("")
    lines.push(
      "Last draft test failed. Update the workflow YAML so the next draft test passes.",
    )
    lines.push(`Test status: ${boundedText(result.status, identityLimit)}`)
    if (result.runID) {
      lines.push(`Run ID: ${boundedText(result.runID, identityLimit)}`)
    }
    if (result.error && !eventBacked) {
      lines.push(`Error: ${boundedText(result.error, diagnosticLimit)}`)
    }
    appendPromptJSONBlock(
      lines,
      "Failed run context",
      draftTestRunContext(run, result, eventBacked),
    )
    appendPromptJSONBlock(
      lines,
      "Recent failed run events",
      draftTestEventContext(events, result),
    )
  }
  return lines.join("\n")
}

function canFixDraftTestWithAI(
  result: WorkflowDraftTestRepairResult,
  stale: boolean,
) {
  return (
    !stale &&
    result.status !== "running" &&
    result.status !== "waiting" &&
    result.status !== "succeeded" &&
    result.status !== "skipped"
  )
}

function draftTestRunContext(
  run: WorkflowRun | undefined,
  result: WorkflowDraftTestRepairResult | null,
  eventBacked: boolean,
) {
  if (run == null || result?.runID == null || run.id !== result.runID) {
    return null
  }
  return {
    run_id: boundedText(run.id, identityLimit),
    workflow_ref: boundedText(run.workflow_ref, identityLimit),
    status: boundedText(run.status, identityLimit),
    error: eventBacked
      ? undefined
      : boundedOptionalText(run.error, diagnosticLimit),
    trigger_event: eventShapeContext(run.event),
    jobs: compactExecutions(run.jobs, !eventBacked),
    steps: compactExecutions(run.steps, !eventBacked),
  }
}

function eventShapeContext(event?: Record<string, unknown>) {
  if (event == null) {
    return undefined
  }
  const actor = recordValue(event.actor)
  const subject = recordValue(event.subject)
  const attributes = recordValue(event.attributes)
  return {
    id: boundedUnknownText(event.id, identityLimit),
    source: boundedUnknownText(event.source, identityLimit),
    connector: boundedUnknownText(event.connector, identityLimit),
    type: boundedUnknownText(event.type, identityLimit),
    actor_type: boundedUnknownText(actor?.type, identityLimit),
    subject_type: boundedUnknownText(subject?.type, identityLimit),
    attribute_keys: attributes
      ? Object.keys(attributes).sort().slice(0, eventPathLimit)
      : undefined,
    payload_paths: eventPayloadPaths(event.payload),
  }
}

function eventPayloadPaths(payload: unknown) {
  const paths: string[] = []
  collectEventPayloadPaths(payload, "$", 0, paths)
  return paths.length > 0 ? paths.sort().slice(0, eventPathLimit) : undefined
}

function collectEventPayloadPaths(
  value: unknown,
  prefix: string,
  depth: number,
  paths: string[],
) {
  if (
    depth >= eventPathDepthLimit ||
    paths.length >= eventPathLimit ||
    value == null
  ) {
    return
  }
  if (Array.isArray(value)) {
    const path = `${prefix}[]`
    paths.push(path)
    for (const item of value.slice(0, 4)) {
      collectEventPayloadPaths(item, path, depth + 1, paths)
    }
    return
  }
  const record = recordValue(value)
  if (record == null) {
    return
  }
  for (const key of Object.keys(record).sort()) {
    if (paths.length >= eventPathLimit) {
      return
    }
    const path = `${prefix}[${JSON.stringify(boundedText(key, identityLimit))}]`
    paths.push(path)
    collectEventPayloadPaths(record[key], path, depth + 1, paths)
  }
}

function draftTestEventContext(
  events: WorkflowRunEvent[] | undefined,
  result: WorkflowDraftTestRepairResult | null,
) {
  if (result?.runID == null) {
    return []
  }
  return (events ?? [])
    .filter((event) => event.run_id === result.runID)
    .slice(-8)
    .map((event) => ({
      time: boundedText(event.time, identityLimit),
      kind: boundedText(event.kind, identityLimit),
      job_id: boundedOptionalText(event.job_id, identityLimit),
      step_id: boundedOptionalText(event.step_id, identityLimit),
    }))
}

function compactExecutions(
  executions:
    | Record<
        string,
        {
          status: string
          error?: string
        }
      >
    | undefined,
  includeErrors: boolean,
) {
  if (executions == null) {
    return undefined
  }
  return Object.fromEntries(
    Object.entries(executions).map(([id, execution]) => [
      boundedText(id, identityLimit),
      {
        status: boundedText(execution.status, identityLimit),
        error: includeErrors
          ? boundedOptionalText(execution.error, diagnosticLimit)
          : undefined,
      },
    ]),
  )
}

function appendPromptJSONBlock(lines: string[], label: string, value: unknown) {
  const text = JSON.stringify(value, null, 2)
  if (!text || text === "{}" || text === "[]" || text === "null") {
    return
  }
  lines.push("")
  lines.push(`${label}:`)
  lines.push(
    text.length > promptBlockLimit
      ? `${text.slice(0, promptBlockLimit)}\n... truncated`
      : text,
  )
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

function boundedUnknownText(value: unknown, limit: number) {
  return typeof value === "string" ? boundedText(value, limit) : undefined
}

function boundedOptionalText(value: string | undefined, limit: number) {
  return value == null ? undefined : boundedText(value, limit)
}

function boundedText(value: string, limit: number) {
  const normalized = value
    .replaceAll("\u0000", "")
    .replaceAll("\r", " ")
    .replaceAll("\n", " ")
    .trim()
  return normalized.length > limit
    ? `${normalized.slice(0, Math.max(0, limit - 3))}...`
    : normalized
}
