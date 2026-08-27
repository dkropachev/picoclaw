import { IconDatabase, IconLoader2 } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type EventSourceSettings,
  getEventSourceSettings,
  updateEventSourceSettings,
} from "@/api/event-sources"
import { CollectionDetailShell } from "@/components/collection"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import {
  type EventSourceSettingsErrors,
  normalizeRedactFields,
  parseRedactFields,
  validateSettingsDraft,
} from "./event-source-validation"

const DEFAULT_DATABASE_PATH = "<workspace>/eventing/events.db"
const DEFAULT_RETENTION_DAYS = 30
const DEFAULT_MAX_PAYLOAD_BYTES = 1_048_576

interface EventSourceSettingsDraft {
  enabled: boolean
  database_path: string
  retention_days: string
  max_payload_bytes: string
  redact_fields: string[]
}

export function EventSourceSettingsPage({ onBack }: { onBack: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["event-source-settings"],
    queryFn: ({ signal }) => getEventSourceSettings(signal),
    retry: false,
  })
  const [draft, setDraft] = useState<EventSourceSettingsDraft | null>(null)
  const [baseline, setBaseline] = useState("")
  const [redactFieldsInput, setRedactFieldsInput] = useState("")
  const [errors, setErrors] = useState<EventSourceSettingsErrors>({})
  const appliedRef = useRef(false)

  useEffect(() => {
    if (!query.data || appliedRef.current) return
    appliedRef.current = true
    applySettings(
      settingsDraft(query.data.event_source_settings),
      setDraft,
      setBaseline,
      setRedactFieldsInput,
    )
  }, [query.data])

  const isDirty = draft != null && JSON.stringify(draft) !== baseline
  const save = useMutation({
    mutationFn: () => {
      if (!draft || !query.data?.config_revision) {
        throw new Error("Event source settings are unavailable")
      }
      return updateEventSourceSettings(
        settingsInput(draft),
        query.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      queryClient.setQueryData(["event-source-settings"], response)
      const next = settingsDraft(response.event_source_settings)
      applySettings(next, setDraft, setBaseline, setRedactFieldsInput)
      setErrors({})
      const gateway = await refreshGatewayState({ force: true }).catch(
        () => undefined,
      )
      showSaveSuccessOrRestartToast(
        t,
        "Event source settings saved.",
        "Event source settings",
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Event source settings could not be saved.",
      )
    },
  })

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!draft || save.isPending) return
    const nextErrors = validateSettingsDraft(draft)
    setErrors(nextErrors)
    if (Object.keys(nextErrors).length > 0) {
      toast.error("Fix the highlighted event source settings before saving.")
      return
    }
    setDraft((current) =>
      current
        ? {
            ...current,
            redact_fields: normalizeRedactFields(current.redact_fields),
          }
        : current,
    )
    save.mutate()
  }

  const reset = () => {
    if (!baseline) return
    const next = JSON.parse(baseline) as EventSourceSettingsDraft
    setDraft(next)
    setRedactFieldsInput(next.redact_fields.join(", "))
    setErrors({})
    save.reset()
  }

  return (
    <CollectionDetailShell
      title="Event source settings"
      onBack={onBack}
      backLabel="All event sources"
      loading={query.isLoading}
      error={errorMessage(query.error)}
      onRetry={() => void query.refetch()}
      actions={
        draft ? (
          <>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={!isDirty || save.isPending}
              onClick={reset}
            >
              Reset
            </Button>
            <Button
              type="submit"
              size="sm"
              form="event-source-settings"
              disabled={!isDirty || save.isPending}
            >
              {save.isPending && <IconLoader2 className="animate-spin" />}
              Save
            </Button>
          </>
        ) : undefined
      }
    >
      {draft && (
        <form id="event-source-settings" onSubmit={submit} noValidate>
          <fieldset disabled={save.isPending} className="space-y-6">
            <Card size="sm">
              <CardHeader>
                <CardTitle>Durable event ingestion</CardTitle>
                <div className="flex items-center gap-3">
                  <Label htmlFor="event-ingress-enabled">
                    {draft.enabled ? "Enabled" : "Disabled"}
                  </Label>
                  <Switch
                    id="event-ingress-enabled"
                    checked={draft.enabled}
                    onCheckedChange={(enabled) =>
                      setDraft((current) =>
                        current ? { ...current, enabled } : current,
                      )
                    }
                    aria-label="Enable durable event ingestion"
                  />
                </div>
              </CardHeader>
              {!draft.enabled && (
                <CardContent>
                  <p className="text-muted-foreground text-xs">
                    Sources remain configured while ingestion is disabled. No
                    event database or listener route is activated.
                  </p>
                </CardContent>
              )}
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <IconDatabase className="text-muted-foreground size-4" />
                  Storage and payload policy
                </CardTitle>
              </CardHeader>
              <CardContent className="grid gap-5 md:grid-cols-2">
                <Field className="md:col-span-2">
                  <FieldLabel htmlFor="event-database-path">
                    SQLite database path
                  </FieldLabel>
                  <Input
                    id="event-database-path"
                    value={draft.database_path}
                    placeholder={DEFAULT_DATABASE_PATH}
                    onChange={(event) =>
                      setDraft((current) =>
                        current
                          ? { ...current, database_path: event.target.value }
                          : current,
                      )
                    }
                  />
                  <FieldDescription>
                    Leave blank for {DEFAULT_DATABASE_PATH}. Relative paths
                    resolve inside the workspace.
                  </FieldDescription>
                </Field>
                <Field data-invalid={Boolean(errors.retention_days)}>
                  <FieldLabel htmlFor="event-retention-days">
                    Retention days
                  </FieldLabel>
                  <Input
                    id="event-retention-days"
                    type="number"
                    min={1}
                    step={1}
                    inputMode="numeric"
                    value={draft.retention_days}
                    placeholder={String(DEFAULT_RETENTION_DAYS)}
                    aria-invalid={Boolean(errors.retention_days)}
                    onChange={(event) =>
                      setDraft((current) =>
                        current
                          ? { ...current, retention_days: event.target.value }
                          : current,
                      )
                    }
                  />
                  <FieldDescription>
                    Leave blank for {DEFAULT_RETENTION_DAYS} days.
                  </FieldDescription>
                  <FieldError>{errors.retention_days}</FieldError>
                </Field>
                <Field data-invalid={Boolean(errors.max_payload_bytes)}>
                  <FieldLabel htmlFor="event-max-payload-bytes">
                    Maximum payload bytes
                  </FieldLabel>
                  <Input
                    id="event-max-payload-bytes"
                    type="number"
                    min={1}
                    step={1}
                    inputMode="numeric"
                    value={draft.max_payload_bytes}
                    placeholder={String(DEFAULT_MAX_PAYLOAD_BYTES)}
                    aria-invalid={Boolean(errors.max_payload_bytes)}
                    onChange={(event) =>
                      setDraft((current) =>
                        current
                          ? {
                              ...current,
                              max_payload_bytes: event.target.value,
                            }
                          : current,
                      )
                    }
                  />
                  <FieldDescription>
                    Leave blank for 1 MiB. Oversized requests are rejected
                    before persistence.
                  </FieldDescription>
                  <FieldError>{errors.max_payload_bytes}</FieldError>
                </Field>
                <Field className="md:col-span-2">
                  <FieldLabel htmlFor="event-redact-fields">
                    Additional redacted fields
                  </FieldLabel>
                  <Textarea
                    id="event-redact-fields"
                    value={redactFieldsInput}
                    placeholder="customer_number, internal_note"
                    onChange={(event) => {
                      setRedactFieldsInput(event.target.value)
                      const redact_fields = parseRedactFields(
                        event.target.value,
                      )
                      setDraft((current) =>
                        current ? { ...current, redact_fields } : current,
                      )
                    }}
                  />
                  <FieldDescription>
                    Comma- or line-separated JSON field names. Built-in
                    authentication fields are always redacted.
                  </FieldDescription>
                </Field>
              </CardContent>
            </Card>

            {isDirty && (
              <ConfigChangeNotice
                kind="save"
                title="Save your changes"
                description="Active event ingress changes may require a gateway restart."
              />
            )}
          </fieldset>
        </form>
      )}
    </CollectionDetailShell>
  )
}

function settingsDraft(
  settings: EventSourceSettings,
): EventSourceSettingsDraft {
  return {
    enabled: settings.enabled,
    database_path: settings.database_path,
    retention_days:
      settings.retention_days > 0 ? String(settings.retention_days) : "",
    max_payload_bytes:
      settings.max_payload_bytes > 0 ? String(settings.max_payload_bytes) : "",
    redact_fields: [...settings.redact_fields],
  }
}

function settingsInput(draft: EventSourceSettingsDraft): EventSourceSettings {
  return {
    enabled: draft.enabled,
    database_path: draft.database_path.trim(),
    retention_days: draft.retention_days.trim()
      ? Number(draft.retention_days)
      : 0,
    max_payload_bytes: draft.max_payload_bytes.trim()
      ? Number(draft.max_payload_bytes)
      : 0,
    redact_fields: normalizeRedactFields(draft.redact_fields),
  }
}

function applySettings(
  settings: EventSourceSettingsDraft,
  setDraft: (settings: EventSourceSettingsDraft) => void,
  setBaseline: (baseline: string) => void,
  setRedactFieldsInput: (value: string) => void,
) {
  setDraft(settings)
  setBaseline(JSON.stringify(settings))
  setRedactFieldsInput(settings.redact_fields.join(", "))
}

function errorMessage(error: unknown): string | undefined {
  return error instanceof Error
    ? error.message
    : error
      ? String(error)
      : undefined
}
