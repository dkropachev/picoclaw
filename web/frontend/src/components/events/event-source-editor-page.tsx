import {
  IconAlertTriangle,
  IconKey,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type EligibleEventChannelAdapter,
  type EventChannelSource,
  type EventSourceInput,
  type EventWebhookFormat,
  type EventWebhookSource,
  createEventSource,
  getEventSource,
  getEventSourceSettings,
  updateEventSource,
} from "@/api/event-sources"
import { CollectionDetailShell } from "@/components/collection"
import { Badge } from "@/components/ui/badge"
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

import {
  type EventChannelDraft,
  type EventWebhookDraft,
  type EventWebhookErrors,
  createWebhookSecret,
  normalizeGitHubRepositories,
  validateChannelDraft,
  validateWebhookDraft,
  webhookSecretState,
} from "./event-source-validation"

type EventSourceDraft = EventWebhookDraft | EventChannelDraft

export function EventSourceEditorPage({
  mode,
  id,
  onBack,
  onSaved,
}: {
  mode: "create" | "edit"
  id?: string
  onBack: () => void
  onSaved: (id: string) => void
}) {
  const { t } = useTranslation()
  const settingsQuery = useQuery({
    queryKey: ["event-source-settings"],
    queryFn: ({ signal }) => getEventSourceSettings(signal),
    enabled: mode === "create",
    retry: false,
  })
  const detailQuery = useQuery({
    queryKey: ["event-sources", "detail", id],
    queryFn: ({ signal }) => getEventSource(id ?? "", signal),
    enabled: mode === "edit" && Boolean(id),
    retry: false,
  })
  const [draft, setDraft] = useState<EventSourceDraft | null>(null)
  const [baseline, setBaseline] = useState("")
  const [webhookErrors, setWebhookErrors] = useState<EventWebhookErrors>({})
  const [channelError, setChannelError] = useState("")
  const appliedRef = useRef<unknown>(null)
  const loaded = mode === "create" ? settingsQuery.data : detailQuery.data

  useEffect(() => {
    if (!loaded || appliedRef.current != null) return
    appliedRef.current = loaded
    const initial =
      mode === "create"
        ? newWebhookDraft()
        : detailQuery.data
          ? detailDraft(detailQuery.data.event_source)
          : null
    if (!initial) return
    setDraft(initial)
    setBaseline(JSON.stringify(initial))
    setWebhookErrors({})
    setChannelError("")
  }, [detailQuery.data, loaded, mode])

  const isDirty = draft != null && JSON.stringify(draft) !== baseline
  const expectedRevision =
    mode === "create"
      ? settingsQuery.data?.config_revision
      : detailQuery.data?.config_revision
  const normalizedInput = useMemo(
    () => (draft ? draftInput(draft) : null),
    [draft],
  )
  const save = useMutation({
    mutationFn: async () => {
      if (!draft || !normalizedInput || !expectedRevision) {
        throw new Error("Event source details are unavailable")
      }
      if (mode === "edit") {
        if (!id) throw new Error("Event source identity is unavailable")
        return updateEventSource(id, normalizedInput, expectedRevision)
      }
      return createEventSource(normalizedInput, expectedRevision)
    },
    onSuccess: async (response) => {
      setDraft((current) =>
        current?.kind === "webhook"
          ? {
              ...current,
              secret: "",
              secret_configured:
                current.secret_update === "replace" ||
                (current.secret_update === "preserve" &&
                  current.secret_configured),
              secret_update: "preserve",
            }
          : current,
      )
      const gateway = await refreshGatewayState({ force: true }).catch(
        () => undefined,
      )
      showSaveSuccessOrRestartToast(
        t,
        `${response.event_source.name} was saved.`,
        response.event_source.name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      onSaved(response.event_source.id)
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "The event source could not be saved.",
      )
    },
  })

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!draft || save.isPending) return
    if (draft.kind === "webhook") {
      const errors = validateWebhookDraft(draft)
      setWebhookErrors(errors)
      setChannelError("")
      if (Object.keys(errors).length > 0) {
        toast.error("Fix the highlighted event source settings before saving.")
        return
      }
    } else {
      const error = validateChannelDraft(draft)
      setWebhookErrors({})
      setChannelError(error ?? "")
      if (error) {
        toast.error("Fix the highlighted event source settings before saving.")
        return
      }
    }
    save.mutate()
  }

  const reset = () => {
    if (!baseline) return
    setDraft(JSON.parse(baseline) as EventSourceDraft)
    setWebhookErrors({})
    setChannelError("")
    save.reset()
  }

  const loading =
    mode === "create" ? settingsQuery.isLoading : detailQuery.isLoading
  const queryError = mode === "create" ? settingsQuery.error : detailQuery.error
  const notFound = mode === "edit" && isNotFound(detailQuery.error)

  return (
    <CollectionDetailShell
      title={
        mode === "create"
          ? "Add event source"
          : draft
            ? `Edit ${draft.name}`
            : "Edit event source"
      }
      identity={
        mode === "edit" && id ? (
          <span className="font-mono text-xs">{id}</span>
        ) : undefined
      }
      status={
        draft ? (
          <Badge variant="outline">
            {draft.kind === "webhook" ? "Webhook" : "Channel adapter"}
          </Badge>
        ) : undefined
      }
      loading={loading}
      error={notFound ? undefined : errorMessage(queryError)}
      notFound={notFound}
      onRetry={() => {
        if (mode === "create") {
          void settingsQuery.refetch()
        } else {
          void detailQuery.refetch()
        }
      }}
      onBack={onBack}
      backLabel={mode === "create" ? "Cancel" : "Back to source"}
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
              form="event-source-editor"
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
        <form id="event-source-editor" onSubmit={submit} noValidate>
          <fieldset disabled={save.isPending} className="space-y-6">
            {mode === "create" && settingsQuery.data && (
              <CreationChoice
                draft={draft}
                adapters={settingsQuery.data.eligible_channel_adapters}
                onChange={setDraft}
              />
            )}
            {draft.kind === "webhook" ? (
              <WebhookEditor
                draft={draft}
                errors={webhookErrors}
                nameReadOnly={mode === "edit"}
                onChange={(patch) => {
                  setDraft((current) =>
                    current?.kind === "webhook"
                      ? { ...current, ...patch }
                      : current,
                  )
                  setWebhookErrors({})
                }}
              />
            ) : (
              <ChannelEditor
                draft={draft}
                error={channelError}
                onChange={(patch) => {
                  setDraft((current) =>
                    current?.kind === "channel"
                      ? { ...current, ...patch }
                      : current,
                  )
                  setChannelError("")
                }}
              />
            )}
          </fieldset>
        </form>
      )}
    </CollectionDetailShell>
  )
}

function CreationChoice({
  draft,
  adapters,
  onChange,
}: {
  draft: EventSourceDraft
  adapters: EligibleEventChannelAdapter[]
  onChange: (draft: EventSourceDraft) => void
}) {
  const value = draft.kind === "webhook" ? "webhook" : `channel:${draft.name}`
  return (
    <Card>
      <CardHeader>
        <CardTitle>Source type</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        <Label htmlFor="event-source-type">Create from</Label>
        <Select
          value={value}
          onValueChange={(next) => {
            if (next === "webhook") {
              onChange(newWebhookDraft())
              return
            }
            const name = next.slice("channel:".length)
            const adapter = adapters.find((item) => item.name === name)
            if (adapter) onChange(newChannelDraft(adapter))
          }}
        >
          <SelectTrigger id="event-source-type" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="webhook">Authenticated webhook</SelectItem>
            {adapters.map((adapter) => (
              <SelectItem key={adapter.name} value={`channel:${adapter.name}`}>
                Delta Chat adapter — {adapter.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          Existing unconfigured Delta Chat channels are creation choices. They
          do not appear in the collection until saved.
        </p>
      </CardContent>
    </Card>
  )
}

function WebhookEditor({
  draft,
  errors,
  nameReadOnly,
  onChange,
}: {
  draft: EventWebhookDraft
  errors: EventWebhookErrors
  nameReadOnly: boolean
  onChange: (patch: Partial<EventWebhookDraft>) => void
}) {
  const secretState = webhookSecretState(draft)
  const endpoint = `/webhooks/events/${draft.name.trim() || "{connector}"}`
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Webhook</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-5 md:grid-cols-[minmax(0,1fr)_minmax(190px,0.55fr)_auto]">
          <Field data-invalid={Boolean(errors.name)}>
            <FieldLabel htmlFor="event-webhook-name">Connector name</FieldLabel>
            <Input
              id="event-webhook-name"
              value={draft.name}
              readOnly={nameReadOnly}
              maxLength={64}
              placeholder="github"
              autoComplete="off"
              aria-invalid={Boolean(errors.name)}
              onChange={(event) => onChange({ name: event.target.value })}
            />
            <FieldDescription>
              {nameReadOnly
                ? "Endpoint names are stable. Create a replacement to change one."
                : "Starts with a letter; use letters, numbers, underscores, or hyphens."}
            </FieldDescription>
            <FieldError>{errors.name}</FieldError>
          </Field>
          <Field>
            <FieldLabel htmlFor="event-webhook-format">
              Webhook format
            </FieldLabel>
            <Select
              value={draft.format}
              onValueChange={(format: EventWebhookFormat) =>
                onChange({ format })
              }
            >
              <SelectTrigger id="event-webhook-format" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="standard">Standard Webhooks</SelectItem>
                <SelectItem value="github">GitHub</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <div className="space-y-2">
            <Label htmlFor="event-webhook-enabled">Enabled</Label>
            <Switch
              id="event-webhook-enabled"
              checked={draft.enabled}
              onCheckedChange={(enabled) => onChange({ enabled })}
              aria-label="Enable webhook"
            />
          </div>
        </CardContent>
      </Card>

      <div className="bg-muted/35 space-y-2 rounded-lg border p-3">
        <p className="text-xs font-medium">Endpoint</p>
        <code className="block overflow-x-auto text-xs">POST {endpoint}</code>
        <p className="text-muted-foreground text-xs">
          This token-free path uses the configured gateway listener after
          restart.
        </p>
      </div>

      {draft.format === "github" && (
        <Card>
          <CardHeader>
            <CardTitle>GitHub routing</CardTitle>
          </CardHeader>
          <CardContent className="space-y-5">
            <div className="bg-muted/35 flex flex-wrap items-center justify-between gap-4 rounded-lg border p-3">
              <div>
                <Label htmlFor="event-webhook-poll">
                  Poll GitHub notifications
                </Label>
                <p className="text-muted-foreground mt-1 text-xs">
                  Read mentions, assignments, issues, and review requests
                  through configured read-only GitHub MCP tools.
                </p>
              </div>
              <Switch
                id="event-webhook-poll"
                checked={draft.poll_notifications}
                onCheckedChange={(poll_notifications) =>
                  onChange({ poll_notifications })
                }
                aria-label="Poll GitHub notifications"
              />
            </div>
            <div className="grid gap-5 md:grid-cols-[minmax(190px,0.55fr)_minmax(0,1fr)]">
              <Field data-invalid={Boolean(errors.target_user)}>
                <FieldLabel htmlFor="event-webhook-target">
                  GitHub user to notify
                </FieldLabel>
                <Input
                  id="event-webhook-target"
                  value={draft.target_user}
                  maxLength={128}
                  placeholder="octocat"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={Boolean(errors.target_user)}
                  onChange={(event) =>
                    onChange({ target_user: event.target.value })
                  }
                />
                <FieldDescription>
                  Marks review requests, assignments, and mentions targeting
                  this user. This is routing metadata, not authentication.
                </FieldDescription>
                <FieldError>{errors.target_user}</FieldError>
              </Field>
              <Field data-invalid={Boolean(errors.repositories)}>
                <div className="flex items-center justify-between gap-2">
                  <FieldLabel htmlFor="event-webhook-repositories">
                    Watched repositories
                  </FieldLabel>
                  <Badge variant="outline">
                    {draft.repositories.filter(Boolean).length} repositories
                  </Badge>
                </div>
                <Textarea
                  id="event-webhook-repositories"
                  value={draft.repositories.join("\n")}
                  rows={8}
                  placeholder={"scylladb/scylla\nscylladb/gocql"}
                  spellCheck={false}
                  aria-invalid={Boolean(errors.repositories)}
                  onChange={(event) =>
                    onChange({
                      repositories: event.target.value.split(/\r?\n/),
                    })
                  }
                />
                <FieldDescription>
                  One owner/repo per line. Leave empty to accept every
                  repository visible to this source.
                </FieldDescription>
                <Label htmlFor="event-webhook-repository-file">
                  Import owner/repo text file
                </Label>
                <Input
                  id="event-webhook-repository-file"
                  type="file"
                  accept=".txt,text/plain"
                  onChange={(event) => {
                    const input = event.currentTarget
                    const file = input.files?.[0]
                    if (!file) return
                    void file.text().then((text) => {
                      onChange({ repositories: text.split(/\r?\n/) })
                      input.value = ""
                    })
                  }}
                />
                <FieldError>{errors.repositories}</FieldError>
              </Field>
            </div>
            <div
              className="flex gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs"
              role="note"
            >
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600" />
              <p>
                {draft.poll_notifications
                  ? "Public webhook ingress is optional while polling is enabled. Terminate trusted TLS before this gateway when accepting webhook deliveries."
                  : "GitHub webhook delivery requires a public HTTPS endpoint with trusted TLS termination."}
              </p>
            </div>
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <IconKey className="size-4" /> Signing secret
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Field data-invalid={Boolean(errors.secret)}>
            <div className="mb-2 flex justify-end">
              <Badge
                variant={secretState === "empty" ? "outline" : "secondary"}
              >
                {secretState === "configured"
                  ? "Configured"
                  : secretState === "replacement"
                    ? "New secret ready"
                    : secretState === "clear"
                      ? "Will be cleared"
                      : "Not set"}
              </Badge>
            </div>
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                aria-label="Signing secret"
                type="password"
                value={draft.secret_update === "replace" ? draft.secret : ""}
                placeholder={
                  draft.secret_configured && draft.secret_update === "preserve"
                    ? "Configured — type to replace"
                    : "Enter a signing secret"
                }
                autoComplete="new-password"
                spellCheck={false}
                aria-invalid={Boolean(errors.secret)}
                onChange={(event) =>
                  onChange({
                    secret: event.target.value,
                    secret_update:
                      event.target.value === "" ? "preserve" : "replace",
                  })
                }
              />
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  try {
                    onChange({
                      secret: createWebhookSecret(draft.format),
                      secret_update: "replace",
                    })
                  } catch {
                    toast.error(
                      "Secure random generation is unavailable in this browser.",
                    )
                  }
                }}
              >
                <IconRefresh /> Generate
              </Button>
              <Button
                type="button"
                variant="outline"
                disabled={
                  draft.enabled ||
                  (!draft.secret_configured &&
                    draft.secret_update !== "replace")
                }
                title={
                  draft.enabled
                    ? "Disable this webhook before clearing its signing secret."
                    : undefined
                }
                onClick={() => onChange({ secret: "", secret_update: "clear" })}
              >
                Clear
              </Button>
            </div>
            <FieldDescription>
              {draft.format === "standard"
                ? "Standard Webhooks secrets use whsec_ followed by canonical base64 that decodes to at least 32 bytes."
                : "GitHub secrets use 32–256 UTF-8 bytes without leading or trailing whitespace."}
            </FieldDescription>
            <FieldError>{errors.secret}</FieldError>
          </Field>
        </CardContent>
      </Card>
    </>
  )
}

function ChannelEditor({
  draft,
  error,
  onChange,
}: {
  draft: EventChannelDraft
  error: string
  onChange: (patch: Partial<EventChannelDraft>) => void
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Delta Chat email adapter</CardTitle>
      </CardHeader>
      <CardContent className="space-y-5">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="secondary">Delta Chat</Badge>
          <Badge variant={draft.channel_enabled ? "outline" : "destructive"}>
            Channel{" "}
            {draft.channel_enabled ? "enabled" : "disabled or unavailable"}
          </Badge>
        </div>
        <Field>
          <FieldLabel htmlFor="event-channel-name">Channel</FieldLabel>
          <Input id="event-channel-name" value={draft.name} readOnly />
          <FieldDescription>
            Adapter identity follows the existing channel and cannot be renamed.
          </FieldDescription>
        </Field>
        <div className="grid gap-5 md:grid-cols-[minmax(170px,0.7fr)_minmax(220px,1fr)_auto]">
          <Field>
            <FieldLabel htmlFor="event-channel-mode">Delivery mode</FieldLabel>
            <Select
              value={draft.mode}
              onValueChange={(mode: EventChannelDraft["mode"]) =>
                onChange({ mode })
              }
            >
              <SelectTrigger id="event-channel-mode" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="mirror">Mirror to event + chat</SelectItem>
                <SelectItem value="event_only">Event only</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <div className="flex items-center justify-between gap-4">
              <div>
                <FieldLabel htmlFor="event-channel-unverified">
                  Allow unverified email
                </FieldLabel>
                <FieldDescription>
                  Security opt-in: accept mail without a verified sender and
                  encrypted or signed transport.
                </FieldDescription>
              </div>
              <Switch
                id="event-channel-unverified"
                checked={draft.allow_unverified_email}
                onCheckedChange={(allow_unverified_email) =>
                  onChange({ allow_unverified_email })
                }
                aria-label="Allow unverified email"
              />
            </div>
          </Field>
          <div className="space-y-2">
            <Label htmlFor="event-channel-enabled">Enabled</Label>
            <Switch
              id="event-channel-enabled"
              checked={draft.enabled}
              onCheckedChange={(enabled) => onChange({ enabled })}
              aria-label="Enable event adapter"
            />
          </div>
        </div>
        {draft.allow_unverified_email && (
          <div
            className="flex gap-3 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs"
            role="note"
          >
            <IconAlertTriangle className="size-4 shrink-0 text-amber-600" />
            <p>
              Unverified email can be spoofed. Use deterministic workflow rules
              that limit what these events may trigger.
            </p>
          </div>
        )}
        {error && (
          <p className="text-destructive text-sm" role="alert">
            {error}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function newWebhookDraft(): EventWebhookDraft {
  return {
    kind: "webhook",
    name: "",
    enabled: false,
    format: "github",
    repositories: [],
    target_user: "",
    poll_notifications: false,
    secret_configured: false,
    secret_update: "preserve",
    secret: "",
  }
}

function newChannelDraft(
  adapter: EligibleEventChannelAdapter,
): EventChannelDraft {
  return {
    kind: "channel",
    name: adapter.name,
    enabled: false,
    source: "email",
    mode: "mirror",
    allow_unverified_email: false,
    channel_enabled: adapter.channel_enabled,
    channel_type: adapter.channel_type,
  }
}

function detailDraft(
  source: EventWebhookSource | EventChannelSource,
): EventSourceDraft {
  if (source.kind === "webhook") {
    return {
      kind: "webhook",
      name: source.name,
      enabled: source.enabled,
      format: source.format,
      repositories: [...source.repositories],
      target_user: source.target_user,
      poll_notifications: source.poll_notifications,
      secret_configured: source.secret_configured,
      secret_update: "preserve",
      secret: "",
      persisted_format: source.format,
    }
  }
  return {
    kind: "channel",
    name: source.name,
    enabled: source.enabled,
    source: "email",
    mode: source.mode,
    allow_unverified_email: source.allow_unverified_email,
    channel_enabled: source.channel_enabled,
    channel_type: source.channel_type,
  }
}

function draftInput(draft: EventSourceDraft): EventSourceInput {
  if (draft.kind === "webhook") {
    return {
      kind: "webhook",
      name: draft.name.trim(),
      enabled: draft.enabled,
      format: draft.format,
      repositories:
        draft.format === "github"
          ? normalizeGitHubRepositories(draft.repositories)
          : [],
      target_user: draft.format === "github" ? draft.target_user.trim() : "",
      poll_notifications: draft.format === "github" && draft.poll_notifications,
      secret_update: draft.secret_update,
      ...(draft.secret_update === "replace" && draft.secret !== ""
        ? { secret: draft.secret }
        : {}),
    }
  }
  return {
    kind: "channel",
    name: draft.name,
    enabled: draft.enabled,
    source: "email",
    mode: draft.mode,
    allow_unverified_email: draft.allow_unverified_email,
  }
}

function isNotFound(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "status" in error &&
    (error as { status?: unknown }).status === 404
  )
}

function errorMessage(error: unknown): string | undefined {
  return error instanceof Error
    ? error.message
    : error
      ? String(error)
      : undefined
}
