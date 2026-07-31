import { beforeEach, describe, expect, it, vi } from "vitest"

import { getAppConfig, patchAppConfig } from "@/api/channels"
import {
  EVENT_SECRET_PLACEHOLDER,
  buildEventSourcesPatch,
  loadEventSources,
  parseEventSourcesConfig,
  saveEventSources,
} from "@/api/event-sources"

vi.mock("@/api/channels", () => ({
  getAppConfig: vi.fn(),
  patchAppConfig: vi.fn(),
}))

const mockedGetAppConfig = vi.mocked(getAppConfig)
const mockedPatchAppConfig = vi.mocked(patchAppConfig)

const appConfig = {
  gateway: { host: "127.0.0.1", port: 18790 },
  events: {
    ingress: {
      enabled: true,
      database_path: "eventing/custom.db",
      retention_days: 14,
      max_payload_bytes: 2097152,
      redact_fields: ["customer_number"],
      webhooks: {
        primary: {
          enabled: true,
          format: "github",
          repositories: ["scylladb/gocql", "scylladb/scylla"],
          target_user: "review-user",
          poll_notifications: true,
          secret: EVENT_SECRET_PLACEHOLDER,
        },
        generic: {
          enabled: false,
          secret: EVENT_SECRET_PLACEHOLDER,
        },
      },
      channels: {
        mail: {
          enabled: true,
          source: "email",
          mode: "event_only",
          allow_unverified_email: true,
        },
        removed: {
          enabled: false,
          source: "email",
          mode: "mirror",
        },
      },
    },
  },
  channel_list: {
    mail: { type: "deltachat", enabled: true },
    disabled_mail: { type: "deltachat", enabled: false },
    slack: { type: "slack", enabled: true },
  },
}

describe("event sources API", () => {
  beforeEach(() => {
    mockedGetAppConfig.mockReset()
    mockedPatchAppConfig.mockReset()
  })

  it("projects event policy, webhook secret presence, and Delta Chat instances", () => {
    const loaded = parseEventSourcesConfig(appConfig)

    expect(loaded.settings).toMatchObject({
      enabled: true,
      databasePath: "eventing/custom.db",
      retentionDays: "14",
      maxPayloadBytes: "2097152",
      redactFields: ["customer_number"],
      gatewayHost: "127.0.0.1",
      gatewayPort: 18790,
    })
    expect(loaded.settings.webhooks).toEqual([
      expect.objectContaining({
        name: "generic",
        format: "standard",
        persistedFormat: "standard",
        secretConfigured: true,
        secretUpdate: "preserve",
        secret: "",
      }),
      expect.objectContaining({
        name: "primary",
        format: "github",
        persistedFormat: "github",
        repositories: ["scylladb/gocql", "scylladb/scylla"],
        targetUser: "review-user",
        pollNotifications: true,
        persistedPollNotifications: true,
        secretConfigured: true,
        secretUpdate: "preserve",
        secret: "",
      }),
    ])
    expect(loaded.settings.channels).toEqual([
      expect.objectContaining({
        name: "disabled_mail",
        configured: false,
        available: true,
        channelEnabled: false,
      }),
      expect.objectContaining({
        name: "mail",
        enabled: true,
        configured: true,
        available: true,
        channelEnabled: true,
        mode: "event_only",
        allowUnverifiedEmail: true,
      }),
      expect.objectContaining({
        name: "removed",
        configured: true,
        available: false,
        channelEnabled: false,
      }),
    ])
    expect(loaded.persisted).toEqual({
      webhookNames: ["primary", "generic"],
      channelNames: ["mail", "removed"],
    })
  })

  it("builds a merge patch that rotates, preserves, clears, and removes secrets safely", () => {
    const loaded = parseEventSourcesConfig(appConfig)
    const primary = loaded.settings.webhooks.find(
      (source) => source.name === "primary",
    )
    const generic = loaded.settings.webhooks.find(
      (source) => source.name === "generic",
    )
    expect(primary).toBeDefined()
    expect(generic).toBeDefined()

    const patch = buildEventSourcesPatch(
      {
        ...loaded.settings,
        databasePath: "",
        retentionDays: "",
        maxPayloadBytes: "1048576",
        redactFields: [],
        webhooks: [
          {
            ...primary!,
            secretUpdate: "replace",
            secret: "01234567890123456789012345678901",
          },
          {
            id: "new",
            name: "deploy",
            enabled: false,
            format: "standard",
            repositories: [],
            targetUser: "",
            pollNotifications: false,
            secretConfigured: false,
            secretUpdate: "clear",
            secret: "",
          },
        ],
        channels: loaded.settings.channels.filter(
          (source) => source.name !== "removed",
        ),
      },
      loaded.persisted,
    )

    expect(patch).toEqual({
      events: {
        ingress: {
          enabled: true,
          database_path: null,
          retention_days: null,
          max_payload_bytes: 1048576,
          redact_fields: null,
          webhooks: {
            primary: {
              enabled: true,
              format: "github",
              repositories: ["scylladb/gocql", "scylladb/scylla"],
              target_user: "review-user",
              poll_notifications: true,
              secret: "01234567890123456789012345678901",
            },
            deploy: {
              enabled: false,
              format: "standard",
              repositories: null,
              target_user: null,
              secret: "",
            },
            generic: null,
          },
          channels: {
            mail: {
              enabled: true,
              source: "email",
              mode: "event_only",
              allow_unverified_email: true,
            },
            removed: null,
          },
        },
      },
    })
  })

  it("does not persist untouched disabled channel choices", () => {
    const loaded = parseEventSourcesConfig(appConfig)
    const patch = buildEventSourcesPatch(
      {
        ...loaded.settings,
        channels: loaded.settings.channels.filter(
          (source) => source.name !== "mail" && source.name !== "removed",
        ),
      },
      { webhookNames: [], channelNames: [] },
    )

    expect(patch).toEqual({
      events: {
        ingress: {
          enabled: true,
          database_path: "eventing/custom.db",
          retention_days: 14,
          max_payload_bytes: 2097152,
          redact_fields: ["customer_number"],
          webhooks: {
            primary: {
              enabled: true,
              format: "github",
              repositories: ["scylladb/gocql", "scylladb/scylla"],
              target_user: "review-user",
              poll_notifications: true,
            },
            generic: {
              enabled: false,
              format: "standard",
              repositories: null,
              target_user: null,
            },
          },
        },
      },
    })
  })

  it("never treats an erased replacement as an implicit secret clear", () => {
    const loaded = parseEventSourcesConfig(appConfig)
    const primary = loaded.settings.webhooks.find(
      (source) => source.name === "primary",
    )
    expect(primary).toBeDefined()

    const patch = buildEventSourcesPatch(
      {
        ...loaded.settings,
        webhooks: [
          {
            ...primary!,
            secretUpdate: "replace",
            secret: "",
          },
        ],
      },
      loaded.persisted,
    )

    expect(patch).toMatchObject({
      events: {
        ingress: {
          webhooks: {
            primary: {
              enabled: true,
              format: "github",
            },
          },
        },
      },
    })
    expect(
      (
        (
          (patch.events as Record<string, unknown>).ingress as Record<
            string,
            unknown
          >
        ).webhooks as Record<string, Record<string, unknown>>
      ).primary,
    ).not.toHaveProperty("secret")
  })

  it("clears a previously enabled notification poll without emitting default noise", () => {
    const loaded = parseEventSourcesConfig(appConfig)
    const primary = loaded.settings.webhooks.find(
      (source) => source.name === "primary",
    )
    const generic = loaded.settings.webhooks.find(
      (source) => source.name === "generic",
    )
    expect(primary).toBeDefined()
    expect(generic).toBeDefined()

    const patch = buildEventSourcesPatch(
      {
        ...loaded.settings,
        webhooks: [{ ...primary!, pollNotifications: false }, generic!],
      },
      loaded.persisted,
    )

    expect(patch).toMatchObject({
      events: {
        ingress: {
          webhooks: {
            primary: { poll_notifications: null },
            generic: {
              enabled: false,
              format: "standard",
            },
          },
        },
      },
    })
    expect(
      (
        patch.events as {
          ingress: { webhooks: Record<string, Record<string, unknown>> }
        }
      ).ingress.webhooks.generic,
    ).not.toHaveProperty("poll_notifications")
  })

  it("rejects renaming a persisted connector because its secret identity cannot move", () => {
    const loaded = parseEventSourcesConfig(appConfig)
    const primary = loaded.settings.webhooks.find(
      (source) => source.name === "primary",
    )
    expect(primary).toBeDefined()

    expect(() =>
      buildEventSourcesPatch(
        {
          ...loaded.settings,
          webhooks: [{ ...primary!, name: "renamed" }],
        },
        loaded.persisted,
      ),
    ).toThrow(/connector names cannot be changed/)
  })

  it("preserves prototype-like Delta instance names in merge patches", () => {
    const loaded = parseEventSourcesConfig(
      JSON.parse(`{
        "channel_list": {
          "__proto__": {"type": "deltachat", "enabled": true}
        },
        "events": {
          "ingress": {
            "channels": {
              "__proto__": {
                "enabled": true,
                "source": "email",
                "mode": "event_only"
              }
            }
          }
        }
      }`),
    )

    const patch = buildEventSourcesPatch(loaded.settings, {
      webhookNames: [],
      channelNames: ["__proto__", "removed"],
    })
    const channels = (
      (patch.events as Record<string, unknown>).ingress as Record<
        string,
        unknown
      >
    ).channels as Record<string, unknown>
    expect(Object.hasOwn(channels, "__proto__")).toBe(true)
    expect(channels.__proto__).toEqual({
      enabled: true,
      source: "email",
      mode: "event_only",
      allow_unverified_email: false,
    })
    expect(channels.removed).toBeNull()
    const serialized = JSON.stringify(patch)
    expect(serialized).toContain('"__proto__"')
    expect(
      JSON.parse(serialized).events.ingress.channels["__proto__"],
    ).toMatchObject({
      enabled: true,
      mode: "event_only",
    })
  })

  it("loads and saves through the shared authenticated config API", async () => {
    mockedGetAppConfig.mockResolvedValue(appConfig)
    mockedPatchAppConfig.mockResolvedValue({ status: "ok" })

    const loaded = await loadEventSources()
    await saveEventSources(loaded.settings, loaded.persisted)

    expect(mockedPatchAppConfig).toHaveBeenCalledOnce()
    expect(mockedPatchAppConfig).toHaveBeenCalledWith(
      buildEventSourcesPatch(loaded.settings, loaded.persisted),
    )
  })
})
