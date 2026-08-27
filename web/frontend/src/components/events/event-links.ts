export function exactEventHref(eventID: string): string {
  return withSearch("/events", { event: eventID })
}

export function exactDispatchHref(dispatchID: string): string {
  return withSearch("/events", {
    view: "dispatches",
    dispatch: dispatchID,
  })
}

export function workflowOperateHref(
  _workflowRef: string,
  workflowID?: string,
): string | undefined {
  if (workflowID && /^[A-Za-z0-9_-]{43}$/.test(workflowID)) {
    return `/agent/workflows/${encodeURIComponent(workflowID)}`
  }
  return undefined
}

export function workflowRunHref(_workflowRef: string, runID: string): string {
  return `/agent/workflows/runs/${encodeURIComponent(runID)}`
}

function withSearch(path: string, values: Record<string, string>): string {
  const search = new URLSearchParams(values)
  return `${path}?${search.toString()}`
}
