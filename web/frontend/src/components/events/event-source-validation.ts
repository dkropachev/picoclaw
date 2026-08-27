import type {
  EventChannelMode,
  EventSecretUpdate,
  EventWebhookFormat,
} from "@/api/event-sources"

const CONNECTOR_NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_-]{0,63}$/
const GITHUB_REPOSITORY_PATTERN = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/
const GITHUB_TARGET_USER_PATTERN =
  /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,126}[A-Za-z0-9])?$/
const MAX_GITHUB_REPOSITORIES = 4096

export interface EventWebhookDraft {
  kind: "webhook"
  name: string
  enabled: boolean
  format: EventWebhookFormat
  repositories: string[]
  target_user: string
  poll_notifications: boolean
  secret_configured: boolean
  secret_update: EventSecretUpdate
  secret: string
  persisted_format?: EventWebhookFormat
}

export interface EventChannelDraft {
  kind: "channel"
  name: string
  enabled: boolean
  source: "email"
  mode: EventChannelMode
  allow_unverified_email: boolean
  channel_enabled: boolean
  channel_type: string
}

export interface EventWebhookErrors {
  name?: string
  secret?: string
  repositories?: string
  target_user?: string
}

export interface EventSourceSettingsErrors {
  retention_days?: string
  max_payload_bytes?: string
}

export function normalizeGitHubRepositories(
  repositories: readonly string[],
): string[] {
  const seen = new Set<string>()
  const normalized: string[] = []
  for (const repository of repositories) {
    const value = repository.trim()
    const key = value.toLowerCase()
    if (!value || seen.has(key)) continue
    seen.add(key)
    normalized.push(value)
  }
  return normalized
}

export function parseRedactFields(value: string): string[] {
  return normalizeRedactFields(value.split(/[,\n]/))
}

export function normalizeRedactFields(fields: readonly string[]): string[] {
  const seen = new Set<string>()
  const normalized: string[] = []
  for (const field of fields) {
    const value = field.trim()
    const key = value.toLowerCase()
    if (!value || seen.has(key)) continue
    seen.add(key)
    normalized.push(value)
  }
  return normalized
}

export function validateWebhookDraft(
  source: EventWebhookDraft,
): EventWebhookErrors {
  const errors: EventWebhookErrors = {}
  if (!CONNECTOR_NAME_PATTERN.test(source.name)) {
    errors.name =
      "Use 1–64 characters: start with a letter, then letters, numbers, underscores, or hyphens."
  }

  const effectiveSecretPresent =
    source.secret_update === "preserve"
      ? source.secret_configured
      : source.secret_update === "replace"
        ? source.secret !== ""
        : false

  if (
    source.persisted_format != null &&
    source.format !== source.persisted_format &&
    source.secret_configured &&
    source.secret_update === "preserve"
  ) {
    errors.secret =
      "Changing webhook format requires a compatible replacement signing secret."
  } else if (
    source.secret_update === "replace" &&
    source.secret !== "" &&
    !isValidWebhookSecret(source.format, source.secret)
  ) {
    errors.secret = webhookSecretValidationMessage(source.format)
  } else if (
    source.enabled &&
    !effectiveSecretPresent &&
    !(source.format === "github" && source.poll_notifications)
  ) {
    errors.secret = "An enabled webhook requires a signing secret."
  } else if (source.secret_update === "clear" && source.enabled) {
    errors.secret = "Disable this webhook before clearing its signing secret."
  }

  if (source.format === "github") {
    if (source.repositories.length > MAX_GITHUB_REPOSITORIES) {
      errors.repositories = `Use at most ${MAX_GITHUB_REPOSITORIES} watched repositories.`
    } else {
      const seen = new Set<string>()
      for (const repository of source.repositories) {
        const value = repository.trim()
        if (!value) continue
        const key = value.toLowerCase()
        if (
          repository !== value ||
          new TextEncoder().encode(value).byteLength > 256 ||
          !GITHUB_REPOSITORY_PATTERN.test(value)
        ) {
          errors.repositories =
            "Each repository must be one trimmed owner/repo name of at most 256 bytes."
          break
        }
        if (seen.has(key)) {
          errors.repositories =
            "Watched repositories must be unique, including differences in letter case."
          break
        }
        seen.add(key)
      }
    }
    if (
      source.target_user !== "" &&
      (source.target_user !== source.target_user.trim() ||
        new TextEncoder().encode(source.target_user).byteLength > 128 ||
        !GITHUB_TARGET_USER_PATTERN.test(source.target_user))
    ) {
      errors.target_user =
        "Use a trimmed GitHub login of at most 128 letters, numbers, or internal hyphens."
    }
  }
  return errors
}

export function validateChannelDraft(
  source: EventChannelDraft,
): string | undefined {
  if (!source.enabled) return undefined
  if (source.channel_type !== "deltachat") {
    return "This adapter must reference an existing Delta Chat channel."
  }
  if (!source.channel_enabled) {
    return "Enable the referenced Delta Chat channel before enabling this adapter."
  }
  return undefined
}

export function validateSettingsDraft(input: {
  retention_days: string
  max_payload_bytes: string
}): EventSourceSettingsErrors {
  const errors: EventSourceSettingsErrors = {}
  if (!isOptionalPositiveInteger(input.retention_days)) {
    errors.retention_days =
      "Retention days must be a positive whole number or blank."
  }
  if (!isOptionalPositiveInteger(input.max_payload_bytes)) {
    errors.max_payload_bytes =
      "Maximum payload bytes must be a positive whole number or blank."
  }
  return errors
}

export function webhookSecretState(
  source: Pick<
    EventWebhookDraft,
    "secret_update" | "secret" | "secret_configured"
  >,
): "configured" | "replacement" | "clear" | "empty" {
  if (source.secret_update === "clear") return "clear"
  if (source.secret_update === "replace" && source.secret !== "") {
    return "replacement"
  }
  return source.secret_configured ? "configured" : "empty"
}

export function webhookSecretValidationMessage(
  format: EventWebhookFormat,
): string {
  return format === "github"
    ? "GitHub secrets must be 32–256 UTF-8 bytes with no leading or trailing whitespace."
    : "Standard Webhooks secrets must use whsec_ plus canonical base64 that decodes to at least 32 bytes."
}

export function createWebhookSecret(format: EventWebhookFormat): string {
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

function isOptionalPositiveInteger(value: string): boolean {
  const normalized = value.trim()
  return (
    normalized === "" ||
    (/^\d+$/.test(normalized) &&
      Number.isSafeInteger(Number(normalized)) &&
      Number(normalized) > 0)
  )
}

function isValidWebhookSecret(
  format: EventWebhookFormat,
  secret: string,
): boolean {
  if (format === "github") {
    const bytes = new TextEncoder().encode(secret).byteLength
    return secret === secret.trim() && bytes >= 32 && bytes <= 256
  }
  if (!secret.startsWith("whsec_")) return false
  const encoded = secret.slice("whsec_".length)
  try {
    const decoded = atob(encoded)
    return btoa(decoded) === encoded && decoded.length >= 32
  } catch {
    return false
  }
}
