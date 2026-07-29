import { useEffect, useRef, useState } from "react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

import {
  workflowCancelReason,
  workflowCancelReasonMaximumBytes,
} from "./workflow-cancel-reason"

export interface WorkflowCancelTarget {
  id: string
  workflowRef: string
}

export function WorkflowCancelDialog({
  target,
  pending,
  requestError,
  onDismiss,
  onConfirm,
}: {
  target: WorkflowCancelTarget | null
  pending: boolean
  requestError?: string
  onDismiss: () => void
  onConfirm: (reason: string) => void
}) {
  const [reasonInput, setReasonInput] = useState("")
  const [attempted, setAttempted] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const validation = workflowCancelReason(reasonInput)
  const errorID = "workflow-cancel-reason-error"
  const helpID = "workflow-cancel-reason-help"

  useEffect(() => {
    setReasonInput("")
    setAttempted(false)
  }, [target?.id])

  return (
    <AlertDialog
      open={target != null}
      onOpenChange={(open) => {
        if (!open && !pending) {
          onDismiss()
        }
      }}
    >
      <AlertDialogContent
        className="max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] max-w-lg overflow-y-auto"
        onOpenAutoFocus={(event) => {
          event.preventDefault()
          textareaRef.current?.focus()
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Cancel workflow run?</AlertDialogTitle>
          <AlertDialogDescription>
            Stop{" "}
            <span className="font-mono break-all">
              {target?.id ?? "this workflow run"}
            </span>{" "}
            from{" "}
            <span className="font-mono break-all">
              {target?.workflowRef ?? "the selected workflow"}
            </span>
            . The reason is stored with the run for operators.
          </AlertDialogDescription>
        </AlertDialogHeader>

        <div className="grid min-w-0 gap-2">
          <Label htmlFor="workflow-cancel-reason">Cancel reason</Label>
          <Textarea
            ref={textareaRef}
            id="workflow-cancel-reason"
            value={reasonInput}
            onChange={(event) => {
              setReasonInput(event.target.value)
              setAttempted(true)
            }}
            disabled={pending}
            aria-invalid={validation.error != null}
            aria-describedby={`${helpID}${
              attempted && validation.error != null ? ` ${errorID}` : ""
            }`}
            className="min-h-24 resize-y"
            placeholder="Why should this run stop?"
          />
          <div
            id={helpID}
            className={cn(
              "text-xs",
              validation.bytes > workflowCancelReasonMaximumBytes
                ? "text-destructive"
                : "text-muted-foreground",
            )}
          >
            {validation.bytes} / {workflowCancelReasonMaximumBytes} UTF-8 bytes
          </div>
          {attempted && validation.error != null ? (
            <div id={errorID} role="alert" className="text-destructive text-xs">
              {validation.error}
            </div>
          ) : null}
          {requestError ? (
            <div role="alert" className="text-destructive text-sm">
              {requestError}
            </div>
          ) : null}
        </div>

        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Keep running</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={validation.error != null || pending || target == null}
            onClick={(event) => {
              event.preventDefault()
              setAttempted(true)
              if (validation.error == null && target != null && !pending) {
                onConfirm(validation.reason)
              }
            }}
          >
            {pending ? "Canceling…" : "Cancel run"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
