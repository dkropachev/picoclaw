import { expect, test } from "@playwright/test"

const providersResponse = {
  providers: [
    {
      provider: "openai",
      credential_id: "openai",
      display_name: "OpenAI",
      methods: ["browser", "device_code", "token"],
      logged_in: true,
      status: "connected",
      auth_method: "browser",
      account_id: "acct-default",
      credentials: [
        {
          provider: "openai",
          credential_id: "openai",
          display_name: "OpenAI",
          methods: ["browser", "device_code", "token"],
          logged_in: true,
          status: "connected",
          auth_method: "browser",
          account_id: "acct-default",
        },
        {
          provider: "openai",
          credential_id: "openai:work",
          display_name: "OpenAI",
          methods: ["browser", "device_code", "token"],
          logged_in: true,
          status: "connected",
          auth_method: "token",
          account_id: "acct-work",
        },
      ],
    },
    {
      provider: "anthropic",
      credential_id: "anthropic",
      display_name: "Anthropic",
      methods: ["token"],
      logged_in: true,
      status: "connected",
      auth_method: "token",
      credentials: [
        {
          provider: "anthropic",
          credential_id: "anthropic",
          display_name: "Anthropic",
          methods: ["token"],
          logged_in: true,
          status: "connected",
          auth_method: "token",
        },
        {
          provider: "anthropic",
          credential_id: "anthropic:backup",
          display_name: "Anthropic",
          methods: ["token"],
          logged_in: true,
          status: "connected",
          auth_method: "token",
        },
      ],
    },
    {
      provider: "google-antigravity",
      credential_id: "google-antigravity",
      display_name: "Google Antigravity",
      methods: ["browser"],
      logged_in: false,
      status: "not_logged_in",
    },
  ],
}

test("credentials page shows and targets named credentials", async ({
  page,
}) => {
  const loginRequests: unknown[] = []
  const logoutRequests: unknown[] = []

  await page.addInitScript(() => {
    window.localStorage.setItem(
      "picoclaw-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
  })

  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url())
    if (!url.pathname.startsWith("/api/")) {
      await route.continue()
      return
    }

    switch (url.pathname) {
      case "/api/auth/status":
        await route.fulfill({
          json: { initialized: true, authenticated: true },
        })
        return
      case "/api/oauth/providers":
        await route.fulfill({ json: providersResponse })
        return
      case "/api/oauth/login":
        loginRequests.push(route.request().postDataJSON())
        await route.fulfill({
          json: {
            status: "ok",
            provider: "openai",
            credential_id: "openai:personal",
            method: "browser",
            flow_id: "flow-personal",
            auth_url: "https://example.test/oauth",
            expires_at: new Date(Date.now() + 60_000).toISOString(),
          },
        })
        return
      case "/api/oauth/logout":
        logoutRequests.push(route.request().postDataJSON())
        await route.fulfill({
          json: {
            status: "ok",
            provider: "openai",
            credential_id: "openai:work",
          },
        })
        return
      case "/api/gateway/status":
        await route.fulfill({
          json: {
            gateway_status: "stopped",
            can_start: false,
            start_reason: "test",
          },
        })
        return
      case "/api/channels":
        await route.fulfill({ json: { channels: [] } })
        return
      default:
        await route.fulfill({ json: {} })
    }
  })

  await page.goto("/credentials")

  await expect(page.getByRole("heading", { name: "OpenAI" })).toBeVisible()
  await expect(page.getByText("Default").first()).toBeVisible()
  await expect(page.getByText("openai:work")).toBeVisible()
  await expect(page.getByText("anthropic:backup")).toBeVisible()

  const openAICard = page
    .locator("section")
    .filter({ has: page.getByRole("heading", { name: "OpenAI" }) })
  const openAICredentialInput = openAICard.getByPlaceholder(
    /Optional named credential/,
  )

  const openAIWorkRow = openAICard.getByTestId("credential-row-openai-work")

  await openAIWorkRow.getByRole("button", { name: "Use" }).click()
  await expect(openAICredentialInput).toHaveValue("openai:work")

  await openAICredentialInput.fill("personal")
  const popupPromise = page.waitForEvent("popup")
  await openAICard.getByRole("button", { name: "Browser OAuth" }).click()
  const popup = await popupPromise
  await popup.close()

  expect(loginRequests).toContainEqual({
    provider: "openai",
    credential_id: "personal",
    method: "browser",
  })

  await openAIWorkRow.getByRole("button", { name: "Logout" }).click()
  await page.getByRole("button", { name: "Logout" }).last().click()

  expect(logoutRequests).toContainEqual({
    provider: "openai",
    credential_id: "openai:work",
  })
})
