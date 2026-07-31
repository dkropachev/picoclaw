import { getAppConfig, patchAppConfig } from "@/api/channels"

export const EVENT_SECRET_PLACEHOLDER = "[NOT_HERE]"

export type EventWebhookFormat = "standard" | "github"
export type EventChannelMode = "mirror" | "event_only"
export type EventSecretUpdate = "preserve" | "replace" | "clear"

export interface EventWebhookSource {
  id: string
  name: string
  persistedName?: string
  enabled: boolean
  format: EventWebhookFormat
  persistedFormat?: EventWebhookFormat
  repositories: string[]
  targetUser: string
  pollNotifications: boolean
  persistedPollNotifications?: boolean
  secretConfigured: boolean
  secretUpdate: EventSecretUpdate
  secret: string
}

export interface EventChannelSource {
  id: string
  name: string
  enabled: boolean
  source: "email"
  mode: EventChannelMode
  allowUnverifiedEmail: boolean
  configured: boolean
  available: boolean
  channelEnabled: boolean
  channelType: string
}

export interface EventSourcesSettings {
  enabled: boolean
  databasePath: string
  retentionDays: string
  maxPayloadBytes: string
  redactFields: string[]
  webhooks: EventWebhookSource[]
  channels: EventChannelSource[]
  gatewayHost: string
  gatewayPort: number
}

interface PersistedEventSources {
  webhookNames: string[]
  channelNames: string[]
}

export interface LoadedEventSources {
  settings: EventSourcesSettings
  persisted: PersistedEventSources
}

type JsonRecord = Record<string, unknown>

function asRecord(value: unknown): JsonRecord {
  if (typeof value === "object" && value !== null && !Array.isArray(value)) {
    return value as JsonRecord
  }
  return {}
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function asBoolean(value: unknown): boolean {
  return value === true
}

function asNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0
}

function asNumberInput(value: unknown): string {
  const number = asNumber(value)
  return number > 0 ? String(number) : ""
}

function asStringList(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return []
  }
  return value.filter(
    (item): item is string => typeof item === "string" && item.trim() !== "",
  )
}

function webhookFormat(value: unknown): EventWebhookFormat {
  return value === "github" ? "github" : "standard"
}

function channelMode(value: unknown): EventChannelMode {
  return value === "event_only" ? "event_only" : "mirror"
}

function stableID(prefix: string, name: string): string {
  const random =
    typeof crypto !== "undefined" && typeof crypto.randomUUID === "function"
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2)
  return `${prefix}-${encodeURIComponent(name)}-${random}`
}

export function parseEventSourcesConfig(value: unknown): LoadedEventSources {
  const root = asRecord(value)
  const events = asRecord(root.events)
  const ingress = asRecord(events.ingress)
  const webhooks = asRecord(ingress.webhooks)
  const configuredChannels = asRecord(ingress.channels)
  const channelList = asRecord(root.channel_list)
  const gateway = asRecord(root.gateway)

  const webhookSources = Object.entries(webhooks)
    .map(([name, raw]) => {
      const item = asRecord(raw)
      const secret = asString(item.secret)
      return {
        id: stableID("webhook", name),
        name,
        persistedName: name,
        enabled: asBoolean(item.enabled),
        format: webhookFormat(item.format),
        persistedFormat: webhookFormat(item.format),
        repositories: asStringList(item.repositories),
        targetUser: asString(item.target_user),
        pollNotifications: asBoolean(item.poll_notifications),
        persistedPollNotifications: asBoolean(item.poll_notifications),
        secretConfigured:
          secret === EVENT_SECRET_PLACEHOLDER || secret.trim() !== "",
        secretUpdate: "preserve" as const,
        secret: "",
      }
    })
    .sort((left, right) => left.name.localeCompare(right.name))

  const availableChannels = new Map<
    string,
    { type: string; enabled: boolean }
  >()
  for (const [name, raw] of Object.entries(channelList)) {
    const item = asRecord(raw)
    const type = asString(item.type) || name
    if (type === "deltachat") {
      availableChannels.set(name, {
        type,
        enabled: asBoolean(item.enabled),
      })
    }
  }

  const channelNames = new Set([
    ...availableChannels.keys(),
    ...Object.keys(configuredChannels),
  ])
  const channelSources = Array.from(channelNames)
    .map((name) => {
      const item = asRecord(configuredChannels[name])
      const available = availableChannels.get(name)
      return {
        id: stableID("channel", name),
        name,
        enabled: asBoolean(item.enabled),
        source: "email" as const,
        mode: channelMode(item.mode),
        allowUnverifiedEmail: asBoolean(item.allow_unverified_email),
        configured: Object.hasOwn(configuredChannels, name),
        available: available != null,
        channelEnabled: available?.enabled === true,
        channelType: available?.type ?? "",
      }
    })
    .sort((left, right) => left.name.localeCompare(right.name))

  return {
    settings: {
      enabled: asBoolean(ingress.enabled),
      databasePath: asString(ingress.database_path),
      retentionDays: asNumberInput(ingress.retention_days),
      maxPayloadBytes: asNumberInput(ingress.max_payload_bytes),
      redactFields: asStringList(ingress.redact_fields),
      webhooks: webhookSources,
      channels: channelSources,
      gatewayHost: asString(gateway.host),
      gatewayPort: asNumber(gateway.port),
    },
    persisted: {
      webhookNames: Object.keys(webhooks),
      channelNames: Object.keys(configuredChannels),
    },
  }
}

export async function loadEventSources(): Promise<LoadedEventSources> {
  return parseEventSourcesConfig(await getAppConfig())
}

function optionalPositiveInteger(value: string): number | null {
  const normalized = value.trim()
  if (normalized === "") {
    return null
  }
  const parsed = Number(normalized)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null
}

function webhookPatch(source: EventWebhookSource): JsonRecord {
  const patch: JsonRecord = {
    enabled: source.enabled,
    format: source.format,
  }
  if (source.format === "github") {
    patch.repositories =
      (source.repositories ?? []).length > 0 ? source.repositories : null
    patch.target_user = (source.targetUser ?? "").trim() || null
    if (source.pollNotifications) {
      patch.poll_notifications = true
    } else if (source.persistedPollNotifications) {
      patch.poll_notifications = null
    }
  } else {
    patch.repositories = null
    patch.target_user = null
    if (source.persistedPollNotifications) {
      patch.poll_notifications = null
    }
  }
  if (source.secretUpdate === "replace" && source.secret !== "") {
    patch.secret = source.secret
  } else if (source.secretUpdate === "clear") {
    patch.secret = ""
  }
  return patch
}

function channelPatch(source: EventChannelSource): JsonRecord {
  return {
    enabled: source.enabled,
    source: "email",
    mode: source.mode,
    allow_unverified_email: source.allowUnverifiedEmail,
  }
}

export function buildEventSourcesPatch(
  settings: EventSourcesSettings,
  persisted: PersistedEventSources,
): JsonRecord {
  const webhooks: JsonRecord = Object.create(null) as JsonRecord
  const currentWebhookNames = new Set<string>()
  for (const source of settings.webhooks) {
    if (source.persistedName != null && source.name !== source.persistedName) {
      throw new Error(
        "Persisted webhook connector names cannot be changed; add a new connector and remove the old one instead.",
      )
    }
    currentWebhookNames.add(source.name)
    webhooks[source.name] = webhookPatch(source)
  }
  for (const name of persisted.webhookNames) {
    if (!currentWebhookNames.has(name)) {
      webhooks[name] = null
    }
  }

  const channels: JsonRecord = Object.create(null) as JsonRecord
  const currentChannelNames = new Set<string>()
  for (const source of settings.channels) {
    if (!source.configured && !source.enabled) {
      continue
    }
    currentChannelNames.add(source.name)
    channels[source.name] = channelPatch(source)
  }
  for (const name of persisted.channelNames) {
    if (!currentChannelNames.has(name)) {
      channels[name] = null
    }
  }

  const ingress: JsonRecord = {
    enabled: settings.enabled,
    database_path: settings.databasePath.trim() || null,
    retention_days: optionalPositiveInteger(settings.retentionDays),
    max_payload_bytes: optionalPositiveInteger(settings.maxPayloadBytes),
    redact_fields:
      settings.redactFields.length > 0 ? settings.redactFields : null,
  }
  if (Object.keys(webhooks).length > 0) {
    ingress.webhooks = webhooks
  }
  if (Object.keys(channels).length > 0) {
    ingress.channels = channels
  }

  return { events: { ingress } }
}

export async function saveEventSources(
  settings: EventSourcesSettings,
  persisted: PersistedEventSources,
): Promise<void> {
  await patchAppConfig(buildEventSourcesPatch(settings, persisted))
}

export function newEventWebhookSource(): EventWebhookSource {
  return {
    id: stableID("webhook", "new"),
    name: "",
    enabled: false,
    format: "github",
    repositories: [],
    targetUser: "",
    pollNotifications: false,
    secretConfigured: false,
    secretUpdate: "replace",
    secret: "",
  }
}
