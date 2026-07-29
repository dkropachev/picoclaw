import type { MCPServer, MCPServerInput, MCPTransport } from "@/api/mcp"

export type MCPAuthMode = "none" | "oauth" | "bearer" | "custom"
export type MCPDiscoveryMode = "inherit" | "deferred" | "eager"

export interface MCPKeyValueRow {
  id: string
  key: string
  value: string
}

export interface MCPServerDraft {
  name: string
  enabled: boolean
  discoveryMode: MCPDiscoveryMode
  type: MCPTransport
  url: string
  command: string
  argsText: string
  envFile: string
  envRows: MCPKeyValueRow[]
  headerRows: MCPKeyValueRow[]
  authMode: MCPAuthMode
  token: string
}

export type MCPServerFieldErrors = Partial<
  Record<"name" | "url" | "command" | "env" | "headers" | "token", string>
>

const MCP_SERVER_NAME_PATTERN = /^[A-Za-z0-9._-]+$/

let nextRowID = 0

export function newKeyValueRow(key = "", value = ""): MCPKeyValueRow {
  nextRowID += 1
  return { id: `mcp-pair-${nextRowID}`, key, value }
}

export function hasMCPServerOriginChanged(
  originalURL: string,
  nextURL: string,
): boolean {
  try {
    return new URL(originalURL.trim()).origin !== new URL(nextURL.trim()).origin
  } catch {
    return false
  }
}

export function isMCPAuthURLSecure(rawURL: string): boolean {
  try {
    const url = new URL(rawURL.trim())
    if (url.username || url.password) return false
    if (url.protocol === "https:") return true
    if (url.protocol !== "http:") return false
    const hostname = url.hostname.toLocaleLowerCase()
    const octets = hostname.split(".")
    return (
      hostname === "localhost" ||
      hostname === "::1" ||
      hostname === "[::1]" ||
      (octets.length === 4 &&
        octets[0] === "127" &&
        octets.every(
          (octet) => /^\d{1,3}$/.test(octet) && Number(octet) <= 255,
        ))
    )
  } catch {
    return false
  }
}

export function emptyMCPServerDraft(): MCPServerDraft {
  return {
    name: "",
    enabled: true,
    discoveryMode: "inherit",
    type: "http",
    url: "",
    command: "",
    argsText: "",
    envFile: "",
    envRows: [],
    headerRows: [],
    authMode: "none",
    token: "",
  }
}

export function draftFromMCPServer(server: MCPServer): MCPServerDraft {
  const authType = server.auth.type.trim().toLowerCase()
  const authMode: MCPAuthMode =
    authType === "oauth"
      ? "oauth"
      : authType === "bearer"
        ? "bearer"
        : server.header_keys.length > 0
          ? "custom"
          : "none"

  return {
    name: server.name,
    enabled: server.enabled,
    discoveryMode:
      server.deferred === null
        ? "inherit"
        : server.deferred
          ? "deferred"
          : "eager",
    type: server.type,
    url: server.url,
    command: server.command,
    argsText: server.args.join("\n"),
    envFile: server.env_file,
    envRows: server.env_keys.map((key) => newKeyValueRow(key)),
    headerRows: server.header_keys.map((key) => newKeyValueRow(key)),
    authMode,
    token: "",
  }
}

export function serverInputFromDraft(draft: MCPServerDraft): MCPServerInput {
  const base: MCPServerInput = {
    name: draft.name.trim(),
    enabled: draft.enabled,
    deferred:
      draft.discoveryMode === "inherit"
        ? null
        : draft.discoveryMode === "deferred",
    type: draft.type,
    auth_mode: draft.type === "stdio" ? "none" : draft.authMode,
  }

  if (draft.type === "stdio") {
    return {
      ...base,
      command: draft.command.trim(),
      args: parseLines(draft.argsText),
      env_file: draft.envFile.trim(),
      env: rowsToRecord(draft.envRows),
      env_keys: rowKeys(draft.envRows),
      headers: {},
      header_keys: [],
    }
  }

  return {
    ...base,
    url: draft.url.trim(),
    env: {},
    env_keys: [],
    headers: draft.authMode === "custom" ? rowsToRecord(draft.headerRows) : {},
    header_keys: draft.authMode === "custom" ? rowKeys(draft.headerRows) : [],
  }
}

export function serverInputFromServer(server: MCPServer): MCPServerInput {
  const authType = server.auth.type.trim().toLocaleLowerCase()
  const authMode: MCPAuthMode =
    authType === "oauth"
      ? "oauth"
      : authType === "bearer"
        ? "bearer"
        : server.header_keys.length > 0
          ? "custom"
          : "none"
  return {
    name: server.name,
    enabled: server.enabled,
    deferred: server.deferred,
    type: server.type,
    auth_mode: server.type === "stdio" ? "none" : authMode,
    ...(server.type === "stdio"
      ? {
          command: server.command,
          args: server.args,
          env_file: server.env_file,
          env: Object.fromEntries(server.env_keys.map((key) => [key, ""])),
          env_keys: server.env_keys,
        }
      : {
          url: server.url,
          headers: Object.fromEntries(
            server.header_keys.map((key) => [key, ""]),
          ),
          header_keys: server.header_keys,
        }),
  }
}

export function validateMCPServerDraft(
  draft: MCPServerDraft,
  existingNames: string[],
  original?: MCPServer | null,
  persistedName = "",
): MCPServerFieldErrors {
  const errors: MCPServerFieldErrors = {}
  const name = draft.name.trim()

  if (!name) {
    errors.name = "required"
  } else if (!MCP_SERVER_NAME_PATTERN.test(name)) {
    errors.name = "invalid_name"
  } else if (
    existingNames.some(
      (candidate) =>
        candidate.toLocaleLowerCase() === name.toLocaleLowerCase() &&
        candidate.toLocaleLowerCase() !==
          (persistedName || original?.name || "").toLocaleLowerCase(),
    )
  ) {
    errors.name = "duplicate"
  }

  if (draft.type === "stdio") {
    if (!draft.command.trim()) {
      errors.command = "required"
    }
    const envError = validateRows(
      draft.envRows,
      new Set(original?.env_keys ?? []),
      false,
    )
    if (envError) errors.env = envError
  } else {
    const originChanged = Boolean(
      original &&
      original.type !== "stdio" &&
      hasMCPServerOriginChanged(original.url, draft.url),
    )

    try {
      const url = new URL(draft.url.trim())
      if (url.protocol !== "http:" && url.protocol !== "https:") {
        errors.url = "invalid"
      } else if (url.username || url.password) {
        errors.url = "invalid"
      } else if (draft.authMode !== "none" && !isMCPAuthURLSecure(draft.url)) {
        errors.url = "secure_auth_url"
      }
    } catch {
      errors.url = draft.url.trim() ? "invalid" : "required"
    }

    if (draft.authMode === "custom") {
      const headerError = validateRows(
        draft.headerRows,
        originChanged
          ? new Set()
          : new Set(
              original?.header_keys.map((key) => key.toLocaleLowerCase()) ?? [],
            ),
        true,
      )
      if (headerError) {
        errors.headers =
          originChanged && headerError === "missing_value"
            ? "secret_reentry_required"
            : headerError
      }
    }

    const savedBearer =
      original?.auth.configured === true &&
      original.auth.type.trim().toLocaleLowerCase() === "bearer" &&
      !originChanged
    if (draft.authMode === "bearer" && !savedBearer && !draft.token.trim()) {
      errors.token = "required"
    }
  }

  return errors
}

function validateRows(
  rows: MCPKeyValueRow[],
  persistedKeys: Set<string>,
  caseInsensitive: boolean,
): string | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (!key && !row.value) continue
    if (!key) return "missing_key"
    const comparable = caseInsensitive ? key.toLocaleLowerCase() : key
    if (seen.has(comparable)) return "duplicate_key"
    seen.add(comparable)
    if (!row.value && !persistedKeys.has(comparable)) {
      return "missing_value"
    }
  }
  return null
}

function rowsToRecord(rows: MCPKeyValueRow[]): Record<string, string> {
  return Object.fromEntries(
    rows
      .map((row) => [row.key.trim(), row.value] as const)
      .filter(([key]) => key !== ""),
  )
}

function rowKeys(rows: MCPKeyValueRow[]): string[] {
  return rows.map((row) => row.key.trim()).filter(Boolean)
}

function parseLines(value: string): string[] {
  return value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean)
}
