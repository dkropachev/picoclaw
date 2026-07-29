import { IconFilter, IconRestore } from "@tabler/icons-react"
import { type FormEvent, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import type { DispatchStatus } from "@/api/events"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

export interface DispatchFilterValues {
  eventID: string
  workflowRef: string
  status: DispatchStatus | ""
}

const dispatchStatuses: DispatchStatus[] = [
  "pending",
  "claimed",
  "running",
  "succeeded",
  "failed",
  "dead",
]

const eventIDPattern = /^ev_[0-9a-f]{32}$/

export function DispatchFilterBar({
  filters,
  onApply,
  onReset,
}: {
  filters: DispatchFilterValues
  onApply: (filters: DispatchFilterValues) => void
  onReset: () => void
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(filters)
  const [validationError, setValidationError] = useState("")

  useEffect(() => {
    setDraft(filters)
    setValidationError("")
  }, [filters])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const eventID = draft.eventID.trim()
    const workflowRef = draft.workflowRef.trim()
    if (eventID !== "" && !eventIDPattern.test(eventID)) {
      setValidationError(
        t(
          "pages.events.dispatch_filters.invalid_event",
          "Event ID must use the ev_ prefix followed by 32 lowercase hexadecimal characters.",
        ),
      )
      return
    }
    if (new TextEncoder().encode(workflowRef).byteLength > 1024) {
      setValidationError(
        t(
          "pages.events.dispatch_filters.invalid_workflow",
          "Workflow reference must not exceed 1024 UTF-8 bytes.",
        ),
      )
      return
    }
    setValidationError("")
    onApply({
      eventID,
      workflowRef,
      status: draft.status,
    })
  }

  const reset = () => {
    const empty: DispatchFilterValues = {
      eventID: "",
      workflowRef: "",
      status: "",
    }
    setDraft(empty)
    setValidationError("")
    onReset()
  }

  return (
    <form
      onSubmit={submit}
      className="border-border grid shrink-0 gap-2 border-b p-3 sm:grid-cols-2 xl:grid-cols-3"
    >
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="dispatch-event-id" className="text-xs">
          {t("pages.events.dispatch_filters.event", "Event ID")}
        </Label>
        <Input
          id="dispatch-event-id"
          value={draft.eventID}
          maxLength={35}
          aria-invalid={validationError !== "" || undefined}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              eventID: event.target.value,
            }))
          }
          className="h-8 font-mono text-xs"
          placeholder="ev_…"
        />
      </div>
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="dispatch-workflow" className="text-xs">
          {t("pages.events.dispatch_filters.workflow", "Workflow")}
        </Label>
        <Input
          id="dispatch-workflow"
          value={draft.workflowRef}
          maxLength={1024}
          aria-invalid={validationError !== "" || undefined}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              workflowRef: event.target.value,
            }))
          }
          className="h-8 font-mono text-xs"
        />
      </div>
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="dispatch-status" className="text-xs">
          {t("pages.events.dispatch_filters.status", "Dispatch status")}
        </Label>
        <Select
          value={draft.status || "all"}
          onValueChange={(value) =>
            setDraft((current) => ({
              ...current,
              status: value === "all" ? "" : (value as DispatchStatus),
            }))
          }
        >
          <SelectTrigger id="dispatch-status" className="h-8 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t("pages.events.dispatch_filters.all_statuses", "All statuses")}
            </SelectItem>
            {dispatchStatuses.map((status) => (
              <SelectItem key={status} value={status}>
                {t(`pages.events.statuses.${status}`, status)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {validationError ? (
        <p
          role="alert"
          className="text-destructive min-w-0 text-xs sm:col-span-2 xl:col-span-3"
        >
          {validationError}
        </p>
      ) : null}
      <div className="flex min-w-0 gap-2 sm:col-span-2 xl:col-span-3 xl:justify-end">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="min-w-0"
          onClick={reset}
        >
          <IconRestore className="size-4" />
          <span className="truncate">{t("common.reset", "Reset")}</span>
        </Button>
        <Button type="submit" variant="outline" size="sm" className="min-w-0">
          <IconFilter className="size-4" />
          <span className="truncate">
            {t("pages.events.dispatch_filters.apply", "Apply filters")}
          </span>
        </Button>
      </div>
    </form>
  )
}
