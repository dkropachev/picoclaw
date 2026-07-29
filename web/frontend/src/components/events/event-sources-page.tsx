import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDatabase,
  IconKey,
  IconLoader2,
  IconMail,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconWebhook,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type EventChannelSource,
  type EventSourcesSettings,
  type EventWebhookFormat,
  type EventWebhookSource,
  type LoadedEventSources,
  loadEventSources,
  newEventWebhookSource,
  saveEventSources,
} from "@/api/event-sources"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

const EVENT_SOURCES_QUERY_KEY = ["event-sources", "settings"] as const
const CONNECTOR_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/
const DEFAULT_DATABASE_PATH = "<workspace>/eventing/events.db"
const DEFAULT_RETENTION_DAYS = 30
const DEFAULT_MAX_PAYLOAD_BYTES = 1_048_576

interface WebhookErrors {
  name?: string
  secret?: string
}

interface EventSourcesErrors {
  retentionDays?: string
  maxPayloadBytes?: string
  webhooks: Record<string, WebhookErrors>
  channels: Record<string, string>
}

const EMPTY_ERRORS: EventSourcesErrors = {
  webhooks: {},
  channels: {},
}

export function EventSourcesPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [settings, setSettings] = useState<EventSourcesSettings | null>(null)
  const [persisted, setPersisted] = useState<
    LoadedEventSources["persisted"] | null
  >(null)
  const [baseline, setBaseline] = useState("")
  const [redactFieldsInput, setRedactFieldsInput] = useState("")
  const [errors, setErrors] = useState<EventSourcesErrors>(EMPTY_ERRORS)
  const lastAppliedSourcesRef = useRef<LoadedEventSources | null>(null)

  const sourcesQuery = useQuery({
    queryKey: EVENT_SOURCES_QUERY_KEY,
    queryFn: loadEventSources,
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  })

  const isDirty = useMemo(
    () => settings != null && JSON.stringify(settings) !== baseline,
    [baseline, settings],
  )

  useEffect(() => {
    if (
      !sourcesQuery.data ||
      sourcesQuery.data === lastAppliedSourcesRef.current ||
      isDirty
    ) {
      return
    }
    lastAppliedSourcesRef.current = sourcesQuery.data
    applyLoadedSources(
      sourcesQuery.data,
      setSettings,
      setPersisted,
      setBaseline,
      setRedactFieldsInput,
    )
    setErrors(EMPTY_ERRORS)
  }, [isDirty, sourcesQuery.data])

  const saveMutation = useMutation({
    mutationFn: async ({
      nextSettings,
      currentPersisted,
    }: {
      nextSettings: EventSourcesSettings
      currentPersisted: LoadedEventSources["persisted"]
    }) => {
      await saveEventSources(nextSettings, currentPersisted)
      // PATCH has committed at this point. Drop submitted credential bytes
      // before any fallible masked reload so a reload failure cannot retain a
      // now-persisted secret in browser state.
      setSettings((current) =>
        markCommittedWebhookSecrets(current, nextSettings),
      )
      await queryClient.invalidateQueries({
        queryKey: EVENT_SOURCES_QUERY_KEY,
      })
      const refreshed = await queryClient.fetchQuery({
        queryKey: EVENT_SOURCES_QUERY_KEY,
        queryFn: loadEventSources,
      })
      const gateway = await refreshGatewayState({ force: true })
      return { refreshed, restartRequired: gateway.restartRequired }
    },
    onSuccess: ({ refreshed, restartRequired }) => {
      lastAppliedSourcesRef.current = refreshed
      applyLoadedSources(
        refreshed,
        setSettings,
        setPersisted,
        setBaseline,
        setRedactFieldsInput,
      )
      setErrors(EMPTY_ERRORS)
      showSaveSuccessOrRestartToast(
        t,
        t("pages.event_sources.save_success", "Event source settings saved."),
        t("pages.event_sources.title", "Event sources"),
        restartRequired,
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t(
              "pages.event_sources.save_error",
              "Event source settings could not be saved.",
            ),
      )
    },
  })

  const updateSettings = <K extends keyof EventSourcesSettings>(
    key: K,
    value: EventSourcesSettings[K],
  ) => {
    setSettings((current) =>
      current == null ? current : { ...current, [key]: value },
    )
  }

  const updateWebhook = (
    id: string,
    patch:
      | Partial<EventWebhookSource>
      | ((source: EventWebhookSource) => Partial<EventWebhookSource>),
  ) => {
    setSettings((current) =>
      current == null
        ? current
        : {
            ...current,
            webhooks: current.webhooks.map((source) => {
              if (source.id !== id) {
                return source
              }
              const nextPatch =
                typeof patch === "function" ? patch(source) : patch
              return { ...source, ...nextPatch }
            }),
          },
    )
    setErrors((current) => ({
      ...current,
      webhooks: Object.fromEntries(
        Object.entries(current.webhooks).filter(
          ([sourceID]) => sourceID !== id,
        ),
      ),
    }))
  }

  const updateChannel = (id: string, patch: Partial<EventChannelSource>) => {
    setSettings((current) =>
      current == null
        ? current
        : {
            ...current,
            channels: current.channels.map((source) =>
              source.id === id ? { ...source, ...patch } : source,
            ),
          },
    )
    setErrors((current) => ({
      ...current,
      channels: Object.fromEntries(
        Object.entries(current.channels).filter(
          ([sourceID]) => sourceID !== id,
        ),
      ),
    }))
  }

  const handleRedactFieldsChange = (value: string) => {
    setRedactFieldsInput(value)
    updateSettings("redactFields", parseRedactFields(value))
  }

  const generateWebhookSecret = (source: EventWebhookSource): string | null => {
    try {
      const secret = createWebhookSecret(source.format)
      updateWebhook(source.id, {
        secret,
        secretUpdate: "replace",
      })
      return secret
    } catch {
      toast.error(
        t(
          "pages.event_sources.webhooks.secure_random_error",
          "Secure random generation is unavailable in this browser.",
        ),
      )
      return null
    }
  }

  const reset = () => {
    if (!sourcesQuery.data) {
      return
    }
    lastAppliedSourcesRef.current = sourcesQuery.data
    applyLoadedSources(
      sourcesQuery.data,
      setSettings,
      setPersisted,
      setBaseline,
      setRedactFieldsInput,
    )
    setErrors(EMPTY_ERRORS)
    saveMutation.reset()
  }

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!settings || !persisted || saveMutation.isPending) {
      return
    }

    const normalizedSettings = {
      ...settings,
      redactFields: normalizeRedactFields(settings.redactFields),
    }
    const nextErrors = validateEventSources(normalizedSettings)
    setErrors(nextErrors)
    if (hasValidationErrors(nextErrors)) {
      toast.error(
        t(
          "pages.event_sources.validation.fix_errors",
          "Fix the highlighted event source settings before saving.",
        ),
      )
      return
    }

    setSettings(normalizedSettings)
    setRedactFieldsInput(normalizedSettings.redactFields.join(", "))
    saveMutation.mutate({
      nextSettings: normalizedSettings,
      currentPersisted: persisted,
    })
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader title={t("pages.event_sources.title", "Event sources")}>
        <Button variant="outline" asChild>
          <Link to="/events">
            <IconArrowLeft className="size-4" />
            {t("pages.event_sources.back_to_events", "Back to events")}
          </Link>
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-10 sm:px-6">
        {sourcesQuery.isPending && !settings ? (
          <div
            className="text-muted-foreground flex items-center justify-center gap-2 py-20 text-sm"
            role="status"
          >
            <IconLoader2 className="size-5 animate-spin" />
            {t("pages.event_sources.loading", "Loading event source settings…")}
          </div>
        ) : sourcesQuery.error && !settings ? (
          <div className="mx-auto max-w-5xl space-y-4 py-12">
            <p className="text-destructive text-sm" role="alert">
              {sourcesQuery.error instanceof Error
                ? sourcesQuery.error.message
                : t(
                    "pages.event_sources.load_error",
                    "Event source settings could not be loaded.",
                  )}
            </p>
            <Button
              type="button"
              variant="outline"
              onClick={() => void sourcesQuery.refetch()}
            >
              <IconRefresh className="size-4" />
              {t("common.retry", "Retry")}
            </Button>
          </div>
        ) : settings ? (
          <form
            className="mx-auto w-full max-w-5xl space-y-6 pt-5"
            onSubmit={submit}
            noValidate
          >
            <fieldset
              className="contents"
              disabled={saveMutation.isPending}
              aria-busy={saveMutation.isPending}
            >
              <MasterIngressCard
                enabled={settings.enabled}
                onChange={(enabled) => updateSettings("enabled", enabled)}
              />

              <StorageCard
                settings={settings}
                redactFieldsInput={redactFieldsInput}
                errors={errors}
                onChange={updateSettings}
                onRedactFieldsChange={handleRedactFieldsChange}
              />

              <WebhookSourcesCard
                settings={settings}
                errors={errors.webhooks}
                onAdd={() =>
                  updateSettings("webhooks", [
                    ...settings.webhooks,
                    newEventWebhookSource(),
                  ])
                }
                onChange={updateWebhook}
                onGenerateSecret={generateWebhookSecret}
                onRemove={(id) =>
                  updateSettings(
                    "webhooks",
                    settings.webhooks.filter((source) => source.id !== id),
                  )
                }
              />

              <DeltaChatSourcesCard
                channels={settings.channels}
                errors={errors.channels}
                onChange={updateChannel}
                onRemove={(id) =>
                  updateSettings(
                    "channels",
                    settings.channels.filter((source) => source.id !== id),
                  )
                }
              />

              {isDirty && (
                <ConfigChangeNotice
                  kind="save"
                  title={t("common.saveChangesTitle", "Save your changes")}
                  description={t(
                    "pages.event_sources.save_prompt",
                    "Event ingress changes take effect after the gateway is restarted.",
                  )}
                />
              )}

              <div className="border-border/60 flex justify-end gap-2 border-t py-4">
                <Button
                  type="button"
                  variant="outline"
                  disabled={!isDirty || saveMutation.isPending}
                  onClick={reset}
                >
                  {t("common.reset", "Reset")}
                </Button>
                <Button
                  type="submit"
                  disabled={!isDirty || saveMutation.isPending}
                >
                  {saveMutation.isPending && (
                    <IconLoader2 className="size-4 animate-spin" />
                  )}
                  {saveMutation.isPending
                    ? t("common.saving", "Saving…")
                    : t("common.save", "Save")}
                </Button>
              </div>
            </fieldset>
          </form>
        ) : null}
      </div>
    </div>
  )
}

function MasterIngressCard({
  enabled,
  onChange,
}: {
  enabled: boolean
  onChange: (enabled: boolean) => void
}) {
  const { t } = useTranslation()
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>
          {t("pages.event_sources.master.title", "Durable event ingestion")}
        </CardTitle>
        <CardDescription>
          {t(
            "pages.event_sources.master.description",
            "Accept authenticated events and channel notifications for workflow routing.",
          )}
        </CardDescription>
        <CardAction className="flex items-center gap-3">
          <Label htmlFor="event-ingress-enabled">
            {enabled
              ? t("common.enabled", "Enabled")
              : t("common.disabled", "Disabled")}
          </Label>
          <Switch
            id="event-ingress-enabled"
            checked={enabled}
            onCheckedChange={onChange}
            aria-label={t(
              "pages.event_sources.master.toggle",
              "Enable durable event ingestion",
            )}
          />
        </CardAction>
      </CardHeader>
      {!enabled && (
        <CardContent>
          <p className="text-muted-foreground text-xs">
            {t(
              "pages.event_sources.master.disabled_hint",
              "Sources remain configured while ingestion is disabled; no event database or listener routes are activated.",
            )}
          </p>
        </CardContent>
      )}
    </Card>
  )
}

function StorageCard({
  settings,
  redactFieldsInput,
  errors,
  onChange,
  onRedactFieldsChange,
}: {
  settings: EventSourcesSettings
  redactFieldsInput: string
  errors: EventSourcesErrors
  onChange: <K extends keyof EventSourcesSettings>(
    key: K,
    value: EventSourcesSettings[K],
  ) => void
  onRedactFieldsChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <IconDatabase className="text-muted-foreground size-4" />
          {t("pages.event_sources.storage.title", "Storage and payload policy")}
        </CardTitle>
        <CardDescription>
          {t(
            "pages.event_sources.storage.description",
            "Control the durable SQLite inbox, retention policy, payload limit, and additional recursive redaction.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-5 md:grid-cols-2">
        <Field className="md:col-span-2">
          <FieldLabel htmlFor="event-database-path">
            {t(
              "pages.event_sources.storage.database_path",
              "SQLite database path",
            )}
          </FieldLabel>
          <Input
            id="event-database-path"
            value={settings.databasePath}
            placeholder={DEFAULT_DATABASE_PATH}
            onChange={(event) => onChange("databasePath", event.target.value)}
          />
          <FieldDescription>
            {t(
              "pages.event_sources.storage.database_path_hint",
              "Leave blank for {{path}}. Relative paths resolve inside the workspace.",
              { path: DEFAULT_DATABASE_PATH },
            )}
          </FieldDescription>
        </Field>

        <Field data-invalid={Boolean(errors.retentionDays)}>
          <FieldLabel htmlFor="event-retention-days">
            {t("pages.event_sources.storage.retention_days", "Retention days")}
          </FieldLabel>
          <Input
            id="event-retention-days"
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            value={settings.retentionDays}
            placeholder={String(DEFAULT_RETENTION_DAYS)}
            aria-invalid={Boolean(errors.retentionDays)}
            aria-describedby={
              errors.retentionDays
                ? "event-retention-days-error"
                : "event-retention-days-hint"
            }
            onChange={(event) => onChange("retentionDays", event.target.value)}
          />
          <FieldDescription id="event-retention-days-hint">
            {t(
              "pages.event_sources.storage.retention_days_hint",
              "Leave blank for {{days}} days.",
              { days: DEFAULT_RETENTION_DAYS },
            )}
          </FieldDescription>
          <FieldError id="event-retention-days-error">
            {errors.retentionDays}
          </FieldError>
        </Field>

        <Field data-invalid={Boolean(errors.maxPayloadBytes)}>
          <FieldLabel htmlFor="event-max-payload-bytes">
            {t(
              "pages.event_sources.storage.max_payload_bytes",
              "Maximum payload bytes",
            )}
          </FieldLabel>
          <Input
            id="event-max-payload-bytes"
            type="number"
            min={1}
            step={1}
            inputMode="numeric"
            value={settings.maxPayloadBytes}
            placeholder={String(DEFAULT_MAX_PAYLOAD_BYTES)}
            aria-invalid={Boolean(errors.maxPayloadBytes)}
            aria-describedby={
              errors.maxPayloadBytes
                ? "event-max-payload-bytes-error"
                : "event-max-payload-bytes-hint"
            }
            onChange={(event) =>
              onChange("maxPayloadBytes", event.target.value)
            }
          />
          <FieldDescription id="event-max-payload-bytes-hint">
            {t(
              "pages.event_sources.storage.max_payload_bytes_hint",
              "Leave blank for 1 MiB. Oversized requests are rejected before persistence.",
            )}
          </FieldDescription>
          <FieldError id="event-max-payload-bytes-error">
            {errors.maxPayloadBytes}
          </FieldError>
        </Field>

        <Field className="md:col-span-2">
          <FieldLabel htmlFor="event-redact-fields">
            {t(
              "pages.event_sources.storage.redact_fields",
              "Additional redacted fields",
            )}
          </FieldLabel>
          <Textarea
            id="event-redact-fields"
            value={redactFieldsInput}
            placeholder="customer_number, internal_note"
            onChange={(event) => onRedactFieldsChange(event.target.value)}
          />
          <FieldDescription>
            {t(
              "pages.event_sources.storage.redact_fields_hint",
              "Comma- or line-separated JSON field names. Built-in credentials and authentication fields are always redacted.",
            )}
          </FieldDescription>
        </Field>
      </CardContent>
    </Card>
  )
}

function WebhookSourcesCard({
  settings,
  errors,
  onAdd,
  onChange,
  onGenerateSecret,
  onRemove,
}: {
  settings: EventSourcesSettings
  errors: Record<string, WebhookErrors>
  onAdd: () => void
  onChange: (
    id: string,
    patch:
      | Partial<EventWebhookSource>
      | ((source: EventWebhookSource) => Partial<EventWebhookSource>),
  ) => void
  onGenerateSecret: (source: EventWebhookSource) => string | null
  onRemove: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <IconWebhook className="text-muted-foreground size-4" />
          {t("pages.event_sources.webhooks.title", "Webhook sources")}
        </CardTitle>
        <CardDescription>
          {t(
            "pages.event_sources.webhooks.description",
            "Create authenticated Standard Webhooks or native GitHub event endpoints on the existing gateway listener.",
          )}
        </CardDescription>
        <CardAction>
          <Button type="button" variant="outline" size="sm" onClick={onAdd}>
            <IconPlus className="size-4" />
            {t("pages.event_sources.webhooks.add", "Add webhook")}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4">
        {settings.webhooks.length === 0 ? (
          <p className="text-muted-foreground rounded-lg border border-dashed p-5 text-center text-sm">
            {t(
              "pages.event_sources.webhooks.empty",
              "No webhook sources are configured.",
            )}
          </p>
        ) : (
          settings.webhooks.map((source, index) => (
            <WebhookSourceEditor
              key={source.id}
              source={source}
              index={index}
              gatewayHost={settings.gatewayHost}
              gatewayPort={settings.gatewayPort}
              error={errors[source.id]}
              onChange={(patch) => onChange(source.id, patch)}
              onGenerateSecret={() => onGenerateSecret(source)}
              onRemove={() => onRemove(source.id)}
            />
          ))
        )}
      </CardContent>
    </Card>
  )
}

function WebhookSourceEditor({
  source,
  index,
  gatewayHost,
  gatewayPort,
  error,
  onChange,
  onGenerateSecret,
  onRemove,
}: {
  source: EventWebhookSource
  index: number
  gatewayHost: string
  gatewayPort: number
  error?: WebhookErrors
  onChange: (
    patch:
      | Partial<EventWebhookSource>
      | ((source: EventWebhookSource) => Partial<EventWebhookSource>),
  ) => void
  onGenerateSecret: () => string | null
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const prefix = `event-webhook-${source.id}`
  const displayName =
    source.name.trim() ||
    t("pages.event_sources.webhooks.unnamed", "New webhook {{number}}", {
      number: index + 1,
    })
  const endpointPath = `/webhooks/events/${source.name.trim() || "{connector}"}`
  const listener =
    gatewayHost && gatewayPort > 0
      ? `${gatewayHost}:${gatewayPort}`
      : t(
          "pages.event_sources.webhooks.configured_listener",
          "the configured gateway listener",
        )
  const secretState = webhookSecretState(source)

  return (
    <fieldset className="border-border/70 space-y-5 rounded-lg border p-4">
      <legend className="px-1 text-sm font-medium">{displayName}</legend>

      <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_minmax(190px,0.55fr)_auto]">
        <Field data-invalid={Boolean(error?.name)}>
          <FieldLabel htmlFor={`${prefix}-name`}>
            {t("pages.event_sources.webhooks.name", "Connector name")}
          </FieldLabel>
          <Input
            id={`${prefix}-name`}
            value={source.name}
            readOnly={source.persistedName != null}
            maxLength={64}
            placeholder="github"
            autoComplete="off"
            aria-invalid={Boolean(error?.name)}
            aria-describedby={
              error?.name ? `${prefix}-name-error` : `${prefix}-name-hint`
            }
            onChange={(event) => onChange({ name: event.target.value })}
          />
          <FieldDescription id={`${prefix}-name-hint`}>
            {source.persistedName != null
              ? t(
                  "pages.event_sources.webhooks.name_stable_hint",
                  "Endpoint names are stable. Add a new webhook and remove this one to change it.",
                )
              : t(
                  "pages.event_sources.webhooks.name_hint",
                  "Starts with a letter; use letters, numbers, underscores, or hyphens.",
                )}
          </FieldDescription>
          <FieldError id={`${prefix}-name-error`}>{error?.name}</FieldError>
        </Field>

        <Field>
          <FieldLabel htmlFor={`${prefix}-format`}>
            {t("pages.event_sources.webhooks.format", "Webhook format")}
          </FieldLabel>
          <Select
            value={source.format}
            onValueChange={(format: EventWebhookFormat) => onChange({ format })}
          >
            <SelectTrigger
              id={`${prefix}-format`}
              className="w-full"
              aria-label={t(
                "pages.event_sources.webhooks.format_for",
                "Webhook format for {{name}}",
                { name: displayName },
              )}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="standard">
                {t(
                  "pages.event_sources.webhooks.format_standard",
                  "Standard Webhooks",
                )}
              </SelectItem>
              <SelectItem value="github">
                {t("pages.event_sources.webhooks.format_github", "GitHub")}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        <div className="flex items-start justify-between gap-4 md:flex-col">
          <div className="space-y-2">
            <Label htmlFor={`${prefix}-enabled`}>
              {t("common.enabled", "Enabled")}
            </Label>
            <Switch
              id={`${prefix}-enabled`}
              checked={source.enabled}
              onCheckedChange={(enabled) => onChange({ enabled })}
              aria-label={t(
                "pages.event_sources.webhooks.enable_for",
                "Enable webhook {{name}}",
                { name: displayName },
              )}
            />
          </div>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            onClick={onRemove}
            aria-label={t(
              "pages.event_sources.webhooks.remove_for",
              "Remove webhook {{name}}",
              { name: displayName },
            )}
          >
            <IconTrash className="size-4" />
            {t("common.remove", "Remove")}
          </Button>
        </div>
      </div>

      <div className="bg-muted/35 space-y-2 rounded-lg border p-3">
        <p className="text-xs font-medium">
          {t("pages.event_sources.webhooks.endpoint", "Endpoint")}
        </p>
        <code className="block overflow-x-auto text-xs">
          POST {endpointPath}
        </code>
        <p className="text-muted-foreground text-xs">
          {t(
            "pages.event_sources.webhooks.endpoint_hint",
            "This path is registered on {{listener}} after the gateway restarts.",
            { listener },
          )}
        </p>
      </div>

      {source.format === "github" && (
        <div
          className="text-foreground flex gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs"
          role="note"
        >
          <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <div className="space-y-1">
            <p className="font-medium">
              {t(
                "pages.event_sources.webhooks.github_https_title",
                "GitHub requires a public HTTPS endpoint",
              )}
            </p>
            <p className="text-muted-foreground">
              {t(
                "pages.event_sources.webhooks.github_https_warning",
                "Terminate trusted TLS before this gateway and configure the same webhook secret in GitHub. GitHub authenticates the body with X-Hub-Signature-256, but its event and delivery headers rely on HTTPS for transport authenticity.",
              )}
            </p>
          </div>
        </div>
      )}

      <Field data-invalid={Boolean(error?.secret)}>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <FieldLabel htmlFor={`${prefix}-secret`}>
            <IconKey className="size-4" />
            {t("pages.event_sources.webhooks.secret", "Signing secret")}
          </FieldLabel>
          <Badge
            variant={
              secretState === "configured" || secretState === "replacement"
                ? "secondary"
                : "outline"
            }
          >
            {secretState === "configured"
              ? t(
                  "pages.event_sources.webhooks.secret_configured",
                  "Configured",
                )
              : secretState === "replacement"
                ? t(
                    "pages.event_sources.webhooks.secret_replacement",
                    "New secret ready",
                  )
                : secretState === "clear"
                  ? t(
                      "pages.event_sources.webhooks.secret_clear_pending",
                      "Will be cleared",
                    )
                  : t("pages.event_sources.webhooks.secret_not_set", "Not set")}
          </Badge>
        </div>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id={`${prefix}-secret`}
            type="password"
            value={source.secretUpdate === "replace" ? source.secret : ""}
            placeholder={
              source.secretConfigured && source.secretUpdate === "preserve"
                ? t(
                    "pages.event_sources.webhooks.secret_preserve_placeholder",
                    "Configured — type to replace",
                  )
                : t(
                    "pages.event_sources.webhooks.secret_placeholder",
                    "Enter a signing secret",
                  )
            }
            autoComplete="new-password"
            spellCheck={false}
            aria-invalid={Boolean(error?.secret)}
            aria-describedby={
              error?.secret ? `${prefix}-secret-error` : `${prefix}-secret-hint`
            }
            onChange={(event) =>
              onChange({
                secret: event.target.value,
                secretUpdate:
                  event.target.value === "" && source.secretConfigured
                    ? "preserve"
                    : "replace",
              })
            }
          />
          <Button type="button" variant="outline" onClick={onGenerateSecret}>
            <IconRefresh className="size-4" />
            {t("pages.event_sources.webhooks.generate_secret", "Generate")}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={
              source.enabled ||
              (!source.secretConfigured && source.secretUpdate !== "replace")
            }
            title={
              source.enabled
                ? t(
                    "pages.event_sources.webhooks.clear_secret_disabled",
                    "Disable this webhook before clearing its signing secret.",
                  )
                : undefined
            }
            onClick={() =>
              onChange({
                secret: "",
                secretUpdate: "clear",
              })
            }
          >
            {t("pages.event_sources.webhooks.clear_secret", "Clear")}
          </Button>
        </div>
        <FieldDescription id={`${prefix}-secret-hint`}>
          {source.format === "standard"
            ? t(
                "pages.event_sources.webhooks.standard_secret_hint",
                "Standard Webhooks secrets use whsec_ followed by canonical base64 that decodes to at least 32 bytes.",
              )
            : t(
                "pages.event_sources.webhooks.github_secret_hint",
                "GitHub secrets must be 32–256 UTF-8 bytes with no leading or trailing whitespace.",
              )}
        </FieldDescription>
        <FieldError id={`${prefix}-secret-error`}>{error?.secret}</FieldError>
      </Field>
    </fieldset>
  )
}

function DeltaChatSourcesCard({
  channels,
  errors,
  onChange,
  onRemove,
}: {
  channels: EventChannelSource[]
  errors: Record<string, string>
  onChange: (id: string, patch: Partial<EventChannelSource>) => void
  onRemove: (id: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <IconMail className="text-muted-foreground size-4" />
          {t(
            "pages.event_sources.deltachat.title",
            "Delta Chat email adapters",
          )}
        </CardTitle>
        <CardDescription>
          {t(
            "pages.event_sources.deltachat.description",
            "Turn messages from existing Delta Chat channel instances into durable email events.",
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {channels.length === 0 ? (
          <div className="rounded-lg border border-dashed p-5 text-center">
            <p className="text-muted-foreground text-sm">
              {t(
                "pages.event_sources.deltachat.empty",
                "No Delta Chat channel instances are available. Configure a Delta Chat channel first.",
              )}
            </p>
            <Button className="mt-3" type="button" variant="outline" asChild>
              <Link to="/channels/$name" params={{ name: "deltachat" }}>
                {t(
                  "pages.event_sources.deltachat.configure",
                  "Configure Delta Chat",
                )}
              </Link>
            </Button>
          </div>
        ) : (
          channels.map((source) => (
            <DeltaChatSourceEditor
              key={source.id}
              source={source}
              error={errors[source.id]}
              onChange={(patch) => onChange(source.id, patch)}
              onRemove={() => onRemove(source.id)}
            />
          ))
        )}
      </CardContent>
    </Card>
  )
}

function DeltaChatSourceEditor({
  source,
  error,
  onChange,
  onRemove,
}: {
  source: EventChannelSource
  error?: string
  onChange: (patch: Partial<EventChannelSource>) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const prefix = `event-channel-${source.id}`
  return (
    <fieldset className="border-border/70 space-y-5 rounded-lg border p-4">
      <legend className="px-1 text-sm font-medium">{source.name}</legend>

      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="secondary">Delta Chat</Badge>
        {!source.available && (
          <Badge variant="destructive">
            {t(
              "pages.event_sources.deltachat.channel_missing",
              "Channel missing",
            )}
          </Badge>
        )}
        {source.available && !source.channelEnabled && (
          <Badge variant="outline">
            {t(
              "pages.event_sources.deltachat.channel_disabled",
              "Channel disabled",
            )}
          </Badge>
        )}
        {source.configured && (
          <Badge variant="outline">
            {t("common.configured", "Configured")}
          </Badge>
        )}
      </div>

      {(!source.available || !source.channelEnabled) && (
        <p className="text-muted-foreground text-xs">
          {!source.available
            ? t(
                "pages.event_sources.deltachat.missing_hint",
                "This saved adapter no longer references an existing Delta Chat channel. Disable or remove it before enabling event ingress.",
              )
            : t(
                "pages.event_sources.deltachat.disabled_hint",
                "Enable this Delta Chat channel under Channels before enabling its event adapter.",
              )}
        </p>
      )}

      <div className="grid gap-5 md:grid-cols-[minmax(170px,0.7fr)_minmax(220px,1fr)_auto]">
        <Field>
          <FieldLabel htmlFor={`${prefix}-mode`}>
            {t("pages.event_sources.deltachat.mode", "Delivery mode")}
          </FieldLabel>
          <Select
            value={source.mode}
            disabled={!source.configured && !source.enabled}
            onValueChange={(mode: EventChannelSource["mode"]) =>
              onChange({ mode })
            }
          >
            <SelectTrigger
              id={`${prefix}-mode`}
              className="w-full"
              aria-label={t(
                "pages.event_sources.deltachat.mode_for",
                "Delivery mode for {{name}}",
                { name: source.name },
              )}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="mirror">
                {t(
                  "pages.event_sources.deltachat.mode_mirror",
                  "Mirror to event + chat",
                )}
              </SelectItem>
              <SelectItem value="event_only">
                {t(
                  "pages.event_sources.deltachat.mode_event_only",
                  "Event only",
                )}
              </SelectItem>
            </SelectContent>
          </Select>
          <FieldDescription>
            {source.mode === "mirror"
              ? t(
                  "pages.event_sources.deltachat.mode_mirror_hint",
                  "Persist the event and continue the ordinary agent turn.",
                )
              : t(
                  "pages.event_sources.deltachat.mode_event_only_hint",
                  "Persist the event without creating an ordinary agent turn.",
                )}
          </FieldDescription>
        </Field>

        <Field>
          <div className="flex items-center justify-between gap-4">
            <div>
              <FieldLabel htmlFor={`${prefix}-allow-unverified`}>
                {t(
                  "pages.event_sources.deltachat.allow_unverified",
                  "Allow unverified email",
                )}
              </FieldLabel>
              <FieldDescription className="mt-1">
                {t(
                  "pages.event_sources.deltachat.allow_unverified_hint",
                  "Security opt-in: accept mail without a verified sender and encrypted/signed transport.",
                )}
              </FieldDescription>
            </div>
            <Switch
              id={`${prefix}-allow-unverified`}
              checked={source.allowUnverifiedEmail}
              disabled={!source.configured && !source.enabled}
              onCheckedChange={(allowUnverifiedEmail) =>
                onChange({ allowUnverifiedEmail })
              }
              aria-label={t(
                "pages.event_sources.deltachat.allow_unverified_for",
                "Allow unverified email for {{name}}",
                { name: source.name },
              )}
            />
          </div>
        </Field>

        <div className="flex items-start justify-between gap-4 md:flex-col">
          <div className="space-y-2">
            <Label htmlFor={`${prefix}-enabled`}>
              {t("common.enabled", "Enabled")}
            </Label>
            <Switch
              id={`${prefix}-enabled`}
              checked={source.enabled}
              onCheckedChange={(enabled) =>
                onChange(
                  enabled || source.configured
                    ? { enabled }
                    : {
                        enabled,
                        mode: "mirror",
                        allowUnverifiedEmail: false,
                      },
                )
              }
              aria-label={t(
                "pages.event_sources.deltachat.enable_for",
                "Enable event adapter for {{name}}",
                { name: source.name },
              )}
            />
          </div>
          {source.configured && (
            <Button
              type="button"
              variant="destructive"
              size="sm"
              onClick={onRemove}
              aria-label={t(
                "pages.event_sources.deltachat.remove_for",
                "Remove event adapter {{name}}",
                { name: source.name },
              )}
            >
              <IconTrash className="size-4" />
              {t("common.remove", "Remove")}
            </Button>
          )}
        </div>
      </div>

      {source.allowUnverifiedEmail && (
        <div
          className="flex gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs"
          role="note"
        >
          <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <p>
            {t(
              "pages.event_sources.deltachat.unverified_warning",
              "Unverified email can be spoofed. Use deterministic workflow rules that limit what these events may trigger.",
            )}
          </p>
        </div>
      )}

      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
    </fieldset>
  )
}

function applyLoadedSources(
  loaded: LoadedEventSources,
  setSettings: (settings: EventSourcesSettings) => void,
  setPersisted: (persisted: LoadedEventSources["persisted"]) => void,
  setBaseline: (baseline: string) => void,
  setRedactFieldsInput: (value: string) => void,
) {
  setSettings(loaded.settings)
  setPersisted(loaded.persisted)
  setBaseline(JSON.stringify(loaded.settings))
  setRedactFieldsInput(loaded.settings.redactFields.join(", "))
}

function markCommittedWebhookSecrets(
  current: EventSourcesSettings | null,
  submitted: EventSourcesSettings,
): EventSourcesSettings | null {
  if (current == null) {
    return current
  }
  const submittedByID = new Map(
    submitted.webhooks.map((source) => [source.id, source]),
  )
  return {
    ...current,
    webhooks: current.webhooks.map((source) => {
      const committed = submittedByID.get(source.id)
      if (committed == null || committed.name !== source.name) {
        return source
      }

      const next: EventWebhookSource = {
        ...source,
        persistedName: committed.name,
        persistedFormat: committed.format,
      }
      if (
        committed.secretUpdate === "replace" &&
        committed.secret !== "" &&
        source.secretUpdate === "replace" &&
        source.secret === committed.secret
      ) {
        next.secret = ""
        next.secretConfigured = true
        next.secretUpdate = "preserve"
      } else if (
        committed.secretUpdate === "clear" &&
        source.secretUpdate === "clear"
      ) {
        next.secret = ""
        next.secretConfigured = false
        next.secretUpdate = "preserve"
      }
      return next
    }),
  }
}

function parseRedactFields(value: string): string[] {
  return value
    .split(/[,\n]/)
    .map((field) => field.trim())
    .filter(Boolean)
}

function normalizeRedactFields(fields: string[]): string[] {
  const seen = new Set<string>()
  const normalizedFields: string[] = []
  for (const field of fields) {
    const normalized = field.trim()
    const key = normalized.toLowerCase()
    if (!normalized || seen.has(key)) {
      continue
    }
    seen.add(key)
    normalizedFields.push(normalized)
  }
  return normalizedFields
}

function validateEventSources(
  settings: EventSourcesSettings,
): EventSourcesErrors {
  const result: EventSourcesErrors = { webhooks: {}, channels: {} }

  if (!isOptionalPositiveInteger(settings.retentionDays)) {
    result.retentionDays =
      "Retention days must be a positive whole number or blank."
  }
  if (!isOptionalPositiveInteger(settings.maxPayloadBytes)) {
    result.maxPayloadBytes =
      "Maximum payload bytes must be a positive whole number or blank."
  }

  const names = new Map<string, EventWebhookSource[]>()
  for (const source of settings.webhooks) {
    const key = source.name.toLowerCase()
    names.set(key, [...(names.get(key) ?? []), source])
  }

  for (const source of settings.webhooks) {
    const sourceErrors: WebhookErrors = {}
    if (source.persistedName != null && source.name !== source.persistedName) {
      sourceErrors.name =
        "Persisted connector names are stable; add a new webhook and remove the old one instead."
    } else if (!CONNECTOR_NAME_PATTERN.test(source.name)) {
      sourceErrors.name =
        "Use 1–64 characters: start with a letter, then letters, numbers, underscores, or hyphens."
    } else if ((names.get(source.name.toLowerCase())?.length ?? 0) > 1) {
      sourceErrors.name =
        "Connector names must be unique, including differences in letter case."
    }

    const effectiveSecretPresent =
      source.secretUpdate === "preserve"
        ? source.secretConfigured
        : source.secretUpdate === "replace"
          ? source.secret !== ""
          : false

    if (
      source.persistedFormat != null &&
      source.format !== source.persistedFormat &&
      source.secretConfigured &&
      source.secretUpdate === "preserve"
    ) {
      sourceErrors.secret =
        "Changing webhook format requires a compatible replacement signing secret."
    } else if (
      source.secretUpdate === "replace" &&
      source.secret !== "" &&
      !isValidWebhookSecret(source.format, source.secret)
    ) {
      sourceErrors.secret = webhookSecretValidationMessage(source.format)
    } else if (settings.enabled && source.enabled && !effectiveSecretPresent) {
      sourceErrors.secret = "An enabled webhook requires a signing secret."
    }

    if (Object.keys(sourceErrors).length > 0) {
      result.webhooks[source.id] = sourceErrors
    }
  }

  for (const source of settings.channels) {
    if (!settings.enabled || !source.enabled) {
      continue
    }
    if (!source.available) {
      result.channels[source.id] =
        "This adapter must reference an existing Delta Chat channel."
    } else if (!source.channelEnabled) {
      result.channels[source.id] =
        "Enable the referenced Delta Chat channel before enabling this adapter."
    }
  }

  return result
}

function isOptionalPositiveInteger(value: string): boolean {
  const normalized = value.trim()
  if (normalized === "") {
    return true
  }
  return (
    /^\d+$/.test(normalized) &&
    Number.isSafeInteger(Number(normalized)) &&
    Number(normalized) > 0
  )
}

function hasValidationErrors(errors: EventSourcesErrors): boolean {
  return Boolean(
    errors.retentionDays ||
    errors.maxPayloadBytes ||
    Object.keys(errors.webhooks).length > 0 ||
    Object.keys(errors.channels).length > 0,
  )
}

function webhookSecretState(
  source: EventWebhookSource,
): "configured" | "replacement" | "clear" | "empty" {
  if (source.secretUpdate === "clear") {
    return "clear"
  }
  if (source.secretUpdate === "replace" && source.secret !== "") {
    return "replacement"
  }
  return source.secretConfigured ? "configured" : "empty"
}

function isValidWebhookSecret(
  format: EventWebhookFormat,
  secret: string,
): boolean {
  if (format === "github") {
    const bytes = new TextEncoder().encode(secret).byteLength
    return secret === secret.trim() && bytes >= 32 && bytes <= 256
  }

  if (!secret.startsWith("whsec_")) {
    return false
  }
  const encoded = secret.slice("whsec_".length)
  try {
    const decoded = atob(encoded)
    return btoa(decoded) === encoded && decoded.length >= 32
  } catch {
    return false
  }
}

function webhookSecretValidationMessage(format: EventWebhookFormat): string {
  return format === "github"
    ? "GitHub secrets must be 32–256 UTF-8 bytes with no leading or trailing whitespace."
    : "Standard Webhooks secrets must use whsec_ plus canonical base64 that decodes to at least 32 bytes."
}

function createWebhookSecret(format: EventWebhookFormat): string {
  if (
    typeof crypto === "undefined" ||
    typeof crypto.getRandomValues !== "function"
  ) {
    throw new Error("secure random generation unavailable")
  }
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  if (format === "github") {
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(
      "",
    )
  }
  const binary = Array.from(bytes, (byte) => String.fromCharCode(byte)).join("")
  return `whsec_${btoa(binary)}`
}
