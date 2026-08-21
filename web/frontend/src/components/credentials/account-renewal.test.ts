import { describe, expect, it } from "vitest"

import type { OAuthProviderStatus } from "@/api/oauth"

import { getAccountRenewalMethod } from "./account-renewal"

const openAIAccount: OAuthProviderStatus = {
  provider: "openai",
  credential_id: "openai:work",
  display_name: "OpenAI",
  methods: ["browser", "device_code", "token"],
  logged_in: true,
  status: "expired",
}

describe("getAccountRenewalMethod", () => {
  it("uses device login for Codex OAuth accounts", () => {
    expect(
      getAccountRenewalMethod({ ...openAIAccount, auth_method: "oauth" }),
    ).toBe("device_code")
  })

  it("preserves token renewal for OpenAI token accounts", () => {
    expect(
      getAccountRenewalMethod({ ...openAIAccount, auth_method: "token" }),
    ).toBe("token")
  })
})
