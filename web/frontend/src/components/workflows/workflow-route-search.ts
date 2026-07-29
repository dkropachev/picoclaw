export type WorkflowPageMode = "develop" | "operate"

export interface WorkflowsRouteSearch {
  mode?: WorkflowPageMode
  workflow?: string
  run?: string
  q?: string
}

const workflowRunIDPattern = /^wr_[A-Za-z0-9_-]+$/
const maximumWorkflowRefBytes = 1024
const maximumWorkflowRunIDLength = 1024
const maximumQueryCharacters = 256

export function normalizeWorkflowsSearch(
  raw: Record<string, unknown>,
): WorkflowsRouteSearch {
  const mode = raw.mode === "operate" ? "operate" : undefined
  const workflow = optionalByteText(raw.workflow, maximumWorkflowRefBytes)
  const run = isWorkflowRunID(raw.run) ? raw.run : undefined
  const q = optionalCharacterText(raw.q, maximumQueryCharacters)

  return {
    ...(mode ? { mode } : {}),
    ...(workflow ? { workflow } : {}),
    ...(run ? { run } : {}),
    ...(q ? { q } : {}),
  }
}

export function workflowsSearchIsCanonical(
  raw: Record<string, unknown>,
  normalized: WorkflowsRouteSearch,
): boolean {
  const rawKeys = Object.keys(raw)
  const normalizedKeys = Object.keys(normalized) as Array<
    keyof WorkflowsRouteSearch
  >
  return (
    rawKeys.length === normalizedKeys.length &&
    normalizedKeys.every((key) => raw[key] === normalized[key])
  )
}

export function isWorkflowRunID(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= maximumWorkflowRunIDLength &&
    workflowRunIDPattern.test(value)
  )
}

export function navigableWorkflowRef(value: unknown): string | undefined {
  const workflow = optionalByteText(value, maximumWorkflowRefBytes)
  return workflow?.startsWith("draft:") ? undefined : workflow
}

function optionalByteText(
  value: unknown,
  maximumBytes: number,
): string | undefined {
  if (typeof value !== "string") {
    return undefined
  }
  const normalized = value.trim()
  return normalized !== "" &&
    new TextEncoder().encode(normalized).byteLength <= maximumBytes
    ? normalized
    : undefined
}

function optionalCharacterText(
  value: unknown,
  maximumCharacters: number,
): string | undefined {
  if (typeof value !== "string") {
    return undefined
  }
  const normalized = value.trim()
  return normalized !== "" && Array.from(normalized).length <= maximumCharacters
    ? normalized
    : undefined
}
