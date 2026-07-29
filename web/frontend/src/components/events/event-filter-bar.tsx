import { IconFilter, IconRestore } from "@tabler/icons-react"
import { type FormEvent, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import type { EventRoutingStatus } from "@/api/events"
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

export interface EventFilterValues {
  source: string
  connector: string
  type: string
  routingStatus: EventRoutingStatus | ""
}

const routingStatuses: EventRoutingStatus[] = [
  "pending",
  "claimed",
  "succeeded",
  "dead",
]

export function EventFilterBar({
  filters,
  onApply,
  onReset,
}: {
  filters: EventFilterValues
  onApply: (filters: EventFilterValues) => void
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
    const source = draft.source.trim()
    const connector = draft.connector.trim()
    const type = draft.type.trim()
    const encoder = new TextEncoder()
    if (encoder.encode(source).byteLength > 128) {
      setValidationError(
        t(
          "pages.events.filters.invalid_source",
          "Source must not exceed 128 UTF-8 bytes.",
        ),
      )
      return
    }
    if (encoder.encode(connector).byteLength > 256) {
      setValidationError(
        t(
          "pages.events.filters.invalid_connector",
          "Connector must not exceed 256 UTF-8 bytes.",
        ),
      )
      return
    }
    if (encoder.encode(type).byteLength > 256) {
      setValidationError(
        t(
          "pages.events.filters.invalid_type",
          "Event type must not exceed 256 UTF-8 bytes.",
        ),
      )
      return
    }
    setValidationError("")
    onApply({
      source,
      connector,
      type,
      routingStatus: draft.routingStatus,
    })
  }

  const reset = () => {
    const empty: EventFilterValues = {
      source: "",
      connector: "",
      type: "",
      routingStatus: "",
    }
    setDraft(empty)
    setValidationError("")
    onReset()
  }

  return (
    <form
      onSubmit={submit}
      className="border-border grid shrink-0 gap-2 border-b p-3 sm:grid-cols-2 xl:grid-cols-4"
    >
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="events-source" className="text-xs">
          {t("pages.events.filters.source", "Source")}
        </Label>
        <Input
          id="events-source"
          value={draft.source}
          maxLength={128}
          aria-invalid={validationError !== "" || undefined}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              source: event.target.value,
            }))
          }
          className="h-8"
        />
      </div>
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="events-connector" className="text-xs">
          {t("pages.events.filters.connector", "Connector")}
        </Label>
        <Input
          id="events-connector"
          value={draft.connector}
          maxLength={256}
          aria-invalid={validationError !== "" || undefined}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              connector: event.target.value,
            }))
          }
          className="h-8"
        />
      </div>
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="events-type" className="text-xs">
          {t("pages.events.filters.type", "Type")}
        </Label>
        <Input
          id="events-type"
          value={draft.type}
          maxLength={256}
          aria-invalid={validationError !== "" || undefined}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              type: event.target.value,
            }))
          }
          className="h-8"
        />
      </div>
      <div className="min-w-0 space-y-1.5">
        <Label htmlFor="events-routing-status" className="text-xs">
          {t("pages.events.filters.routing_status", "Routing status")}
        </Label>
        <Select
          value={draft.routingStatus || "all"}
          onValueChange={(value) =>
            setDraft((current) => ({
              ...current,
              routingStatus:
                value === "all" ? "" : (value as EventRoutingStatus),
            }))
          }
        >
          <SelectTrigger id="events-routing-status" className="h-8 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t("pages.events.filters.all_statuses", "All statuses")}
            </SelectItem>
            {routingStatuses.map((status) => (
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
          className="text-destructive min-w-0 text-xs sm:col-span-2 xl:col-span-4"
        >
          {validationError}
        </p>
      ) : null}
      <div className="flex min-w-0 gap-2 sm:col-span-2 xl:col-span-4 xl:justify-end">
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
            {t("pages.events.filters.apply", "Apply filters")}
          </span>
        </Button>
      </div>
    </form>
  )
}
