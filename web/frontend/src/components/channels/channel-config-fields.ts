import type { ChannelConfig } from "@/api/channels"

export const SECRET_FIELD_MAP = {
  token: "_token",
  app_secret: "_app_secret",
  client_secret: "_client_secret",
  corp_secret: "_corp_secret",
  channel_secret: "_channel_secret",
  channel_access_token: "_channel_access_token",
  access_token: "_access_token",
  bot_token: "_bot_token",
  app_token: "_app_token",
  encoding_aes_key: "_encoding_aes_key",
  encrypt_key: "_encrypt_key",
  verification_token: "_verification_token",
  secret: "_secret",
  username: "_username",
  password: "_password",
  nickserv_password: "_nickserv_password",
  sasl_password: "_sasl_password",
} as const

const CHANNEL_SECRET_FIELDS: Record<string, string[]> = {
  weixin: ["token"],
  telegram: ["token"],
  discord: ["token"],
  slack: ["bot_token", "app_token"],
  feishu: ["app_secret", "encrypt_key", "verification_token"],
  dingtalk: ["client_secret"],
  line: ["channel_secret", "channel_access_token"],
  qq: ["app_secret"],
  onebot: ["access_token"],
  wecom: ["secret"],
  pico: ["token"],
  matrix: ["access_token"],
  deltachat: ["password"],
  irc: ["password", "nickserv_password", "sasl_password"],
  mqtt: ["username", "password"],
}

const CHANNEL_DEFAULT_FIELDS: Record<string, ChannelConfig> = {
  deltachat: {
    allow_from: [],
    group_trigger: {
      mention_only: true,
      prefixes: [],
    },
    typing: {
      enabled: false,
    },
    placeholder: {
      enabled: false,
      text: [],
    },
    email: "",
    display_name: "",
    avatar_image: "",
    data_dir: "",
    rpc_server_path: "",
    invite_link: "",
    allow_crosspost: false,
    imap_server: "",
    imap_port: 0,
    smtp_server: "",
    smtp_port: 0,
  },
}

export function getRequiredFieldKeys(channelName: string): string[] {
  switch (channelName) {
    case "telegram":
      return ["token"]
    case "discord":
      return ["token"]
    case "slack":
      return ["bot_token"]
    case "feishu":
      return ["app_id", "app_secret"]
    case "dingtalk":
      return ["client_id", "client_secret"]
    case "line":
      return ["channel_secret", "channel_access_token"]
    case "qq":
      return ["app_id", "app_secret"]
    case "onebot":
      return ["ws_url"]
    case "wecom":
      return []
    case "whatsapp":
      return ["bridge_url"]
    case "pico":
      return ["token"]
    case "maixcam":
      return ["host"]
    case "matrix":
      return ["homeserver", "user_id", "access_token"]
    case "deltachat":
      return ["email"]
    case "irc":
      return ["server"]
    case "mqtt":
      return ["broker", "agent_id"]
    default:
      return []
  }
}

const SECRET_FIELD_SET = new Set(Object.keys(SECRET_FIELD_MAP))

function asString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

export function isSecretField(key: string): boolean {
  return SECRET_FIELD_SET.has(key)
}

export function buildEditConfig(
  channelName: string,
  config: ChannelConfig,
): ChannelConfig {
  const edit: ChannelConfig = {
    ...(CHANNEL_DEFAULT_FIELDS[channelName] ?? {}),
    ...config,
  }

  for (const key of CHANNEL_SECRET_FIELDS[channelName] ?? []) {
    if (!(key in edit)) {
      edit[key] = ""
    }
    const editKey = SECRET_FIELD_MAP[key as keyof typeof SECRET_FIELD_MAP]
    if (editKey) {
      edit[editKey] = ""
    }
  }

  return edit
}

export function hasConfiguredSecret(
  configuredSecrets: readonly string[],
  key: string,
): boolean {
  return configuredSecrets.includes(key)
}

export function getFieldValueForValidation(
  config: ChannelConfig,
  configuredSecrets: readonly string[],
  key: string,
): unknown {
  const editKey = SECRET_FIELD_MAP[key as keyof typeof SECRET_FIELD_MAP]
  if (editKey) {
    const incoming = asString(config[editKey]).trim()
    if (incoming !== "") {
      return incoming
    }
    if (hasConfiguredSecret(configuredSecrets, key)) {
      return true
    }
  }
  return config[key]
}

export function getSecretInputPlaceholder(
  configuredSecrets: readonly string[],
  key: string,
  configuredPlaceholder: string,
  fallback = "",
): string {
  return hasConfiguredSecret(configuredSecrets, key)
    ? configuredPlaceholder
    : fallback
}
