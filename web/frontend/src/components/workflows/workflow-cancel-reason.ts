import { WORKFLOW_CANCEL_REASON_MAX_BYTES } from "@/api/workflows"

export function workflowCancelReason(value: string) {
  const reason = value.trim()
  const bytes = new TextEncoder().encode(reason).byteLength
  return {
    reason,
    bytes,
    error:
      reason === ""
        ? "A cancel reason is required."
        : bytes > WORKFLOW_CANCEL_REASON_MAX_BYTES
          ? "Cancel reason must not exceed 1024 UTF-8 bytes."
          : null,
  }
}

export const workflowCancelReasonMaximumBytes = WORKFLOW_CANCEL_REASON_MAX_BYTES
