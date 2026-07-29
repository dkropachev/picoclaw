export function exactEventHref(eventID: string): string {
  return withSearch("/events", { event: eventID })
}

export function exactDispatchHref(dispatchID: string): string {
  return withSearch("/events", {
    view: "dispatches",
    dispatch: dispatchID,
  })
}

export function workflowOperateHref(workflowRef: string): string {
  return withSearch("/agent/workflows", {
    mode: "operate",
    workflow: workflowRef,
  })
}

export function workflowRunHref(workflowRef: string, runID: string): string {
  return withSearch("/agent/workflows", {
    mode: "operate",
    workflow: workflowRef,
    run: runID,
  })
}

function withSearch(path: string, values: Record<string, string>): string {
  const search = new URLSearchParams(values)
  return `${path}?${search.toString()}`
}
