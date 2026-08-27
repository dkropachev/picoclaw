const workflowRunIDPattern = /^wr_[A-Za-z0-9_-]+$/
const maximumWorkflowRefBytes = 1024
const maximumWorkflowRunIDLength = 1024

export function isWorkflowRunID(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= maximumWorkflowRunIDLength &&
    workflowRunIDPattern.test(value)
  )
}

export function navigableWorkflowRef(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined
  const workflow = value.trim()
  return workflow !== "" &&
    new TextEncoder().encode(workflow).byteLength <= maximumWorkflowRefBytes &&
    !workflow.startsWith("draft:")
    ? workflow
    : undefined
}
