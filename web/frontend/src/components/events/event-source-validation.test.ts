import { describe, expect, it, vi } from "vitest"

import {
  type EventChannelDraft,
  type EventWebhookDraft,
  createWebhookSecret,
  normalizeGitHubRepositories,
  normalizeRedactFields,
  validateChannelDraft,
  validateSettingsDraft,
  validateWebhookDraft,
} from "./event-source-validation"

describe("event source validation", () => {
  it("normalizes repository and redaction lists case-insensitively", () => {
    expect(
      normalizeGitHubRepositories([
        " octo/picoclaw ",
        "OCTO/PICOCLAW",
        "",
        "octo/docs",
      ]),
    ).toEqual(["octo/picoclaw", "octo/docs"])
    expect(
      normalizeRedactFields([" token ", "TOKEN", "customer_number"]),
    ).toEqual(["token", "customer_number"])
  })

  it("requires valid identities, scopes, target users, and active secrets", () => {
    const errors = validateWebhookDraft(
      webhook({
        name: "9 bad",
        enabled: true,
        repositories: ["missing-owner"],
        target_user: "-bad",
        secret: "short",
      }),
    )

    expect(errors).toMatchObject({
      name: expect.stringContaining("1–64"),
      repositories: expect.stringContaining("owner/repo"),
      target_user: expect.stringContaining("GitHub login"),
      secret: expect.stringContaining("32–256"),
    })
  })

  it("allows poll-only GitHub but requires disable before explicit clear", () => {
    expect(
      validateWebhookDraft(
        webhook({
          enabled: true,
          poll_notifications: true,
          secret_update: "replace",
          secret: "",
        }),
      ).secret,
    ).toBeUndefined()
    expect(
      validateWebhookDraft(
        webhook({
          enabled: true,
          poll_notifications: true,
          secret_configured: true,
          secret_update: "clear",
          secret: "",
        }),
      ).secret,
    ).toContain("Disable")
  })

  it("blocks preserved credentials after webhook format changes", () => {
    expect(
      validateWebhookDraft(
        webhook({
          format: "standard",
          persisted_format: "github",
          secret_configured: true,
          secret_update: "preserve",
        }),
      ).secret,
    ).toContain("compatible replacement")
  })

  it("validates channel dependencies whenever the source is enabled", () => {
    const draft: EventChannelDraft = {
      kind: "channel",
      name: "mail",
      enabled: true,
      source: "email",
      mode: "mirror",
      allow_unverified_email: false,
      channel_enabled: false,
      channel_type: "deltachat",
    }
    expect(validateChannelDraft(draft)).toContain("Enable the referenced")
    expect(validateChannelDraft({ ...draft, enabled: false })).toBeUndefined()
  })

  it("accepts blank policy defaults and rejects unsafe numeric values", () => {
    expect(
      validateSettingsDraft({ retention_days: "", max_payload_bytes: "" }),
    ).toEqual({})
    expect(
      validateSettingsDraft({ retention_days: "0", max_payload_bytes: "1.5" }),
    ).toEqual({
      retention_days: expect.stringContaining("positive whole number"),
      max_payload_bytes: expect.stringContaining("positive whole number"),
    })
  })

  it("generates format-valid secrets from secure random bytes", () => {
    vi.spyOn(crypto, "getRandomValues").mockImplementation((array) => {
      ;(array as Uint8Array).fill(7)
      return array
    })
    expect(createWebhookSecret("github")).toMatch(/^[0-9a-f]{64}$/)
    expect(createWebhookSecret("standard")).toMatch(/^whsec_/)
  })
})

function webhook(
  overrides: Partial<EventWebhookDraft> = {},
): EventWebhookDraft {
  return {
    kind: "webhook",
    name: "github",
    enabled: false,
    format: "github",
    repositories: [],
    target_user: "",
    poll_notifications: false,
    secret_configured: false,
    secret_update: "replace",
    secret: "x".repeat(32),
    ...overrides,
  }
}
