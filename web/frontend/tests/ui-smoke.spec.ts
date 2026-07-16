import { type Page, type Route, expect, test } from "@playwright/test"

const smokeRoutes = [
  "/",
  "/models",
  "/logs",
  "/agent/tools",
  "/agent/skills",
  "/agent/hub",
] as const

const modelResponse = {
  models: [
    {
      index: 0,
      model_name: "gpt-4o-mini",
      provider: "openai",
      model: "gpt-4o-mini",
      api_key: "",
      enabled: true,
      available: true,
      status: "available",
      is_default: true,
      is_virtual: false,
      default_model_allowed: true,
    },
  ],
  total: 1,
  default_model: "gpt-4o-mini",
  provider_options: [
    {
      id: "openai",
      display_name: "OpenAI",
      default_api_base: "https://api.openai.com/v1",
      empty_api_key_allowed: false,
      create_allowed: true,
      default_model_allowed: true,
      supports_fetch: true,
    },
  ],
}

const toolsResponse = {
  tools: [
    {
      name: "web_search",
      description: "Search the web",
      category: "web",
      config_key: "tools.web_search",
      status: "enabled",
    },
    {
      name: "find_skills",
      description: "Find skills",
      category: "skills",
      config_key: "tools.find_skills",
      status: "enabled",
    },
    {
      name: "install_skill",
      description: "Install skills",
      category: "skills",
      config_key: "tools.install_skill",
      status: "enabled",
    },
  ],
}

const webSearchConfigResponse = {
  provider: "openai",
  current_service: "openai",
  prefer_native: true,
  providers: [
    {
      id: "openai",
      label: "OpenAI",
      configured: true,
      current: true,
      requires_auth: true,
    },
  ],
  settings: {
    openai: {
      enabled: true,
      max_results: 5,
      api_key_set: true,
    },
  },
}

const skillsResponse = {
  skills: [
    {
      name: "review-helper",
      path: "/workspace/skills/review-helper",
      source: "workspace",
      description: "Review code changes",
      origin_kind: "manual",
    },
  ],
}

const channelCatalogResponse = {
  channels: [
    {
      name: "telegram",
      display_name: "Telegram",
      config_key: "telegram",
    },
    {
      name: "discord",
      display_name: "Discord",
      config_key: "discord",
    },
  ],
}

async function mockLauncherApis(page: Page) {
  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const path = url.pathname
      const method = request.method()

      if (method !== "GET") {
        return json(route, { status: "ok" })
      }

      switch (path) {
        case "/api/auth/status":
          return json(route, { authenticated: true, initialized: true })
        case "/api/gateway/status":
          return json(route, {
            gateway_status: "stopped",
            gateway_start_allowed: true,
            gateway_restart_required: false,
            boot_default_model: "gpt-4o-mini",
            config_default_model: "gpt-4o-mini",
          })
        case "/api/gateway/logs":
          return json(route, { logs: [], log_total: 0, log_run_id: 1 })
        case "/api/channels/catalog":
          return json(route, channelCatalogResponse)
        case "/api/config":
          return json(route, {
            channels: {
              telegram: { enabled: true },
              discord: { enabled: false },
            },
          })
        case "/api/models":
          return json(route, modelResponse)
        case "/api/models/catalog":
          return json(route, { entries: [], total: 0 })
        case "/api/oauth/providers":
          return json(route, { providers: [] })
        case "/api/sessions":
          return json(route, [])
        case "/api/tools":
          return json(route, toolsResponse)
        case "/api/tools/web-search-config":
          return json(route, webSearchConfigResponse)
        case "/api/skills":
          return json(route, skillsResponse)
        case "/api/skills/search":
          return json(route, {
            results: [],
            limit: Number(url.searchParams.get("limit") ?? 20),
            offset: Number(url.searchParams.get("offset") ?? 0),
            has_more: false,
          })
        case "/api/system/autostart":
          return json(route, {
            enabled: false,
            supported: true,
            platform: "linux",
          })
        case "/api/system/launcher-config":
          return json(route, {
            port: 18800,
            public: false,
            allowed_cidrs: [],
            allow_localhost_bypass: true,
            trusted_proxy_cidrs: [],
          })
        case "/api/system/version":
          return json(route, {
            version: "test",
            git_commit: "test",
            build_time: "test",
            go_version: "go1.25",
          })
        default:
          return json(route, {})
      }
    },
  )
}

async function json(route: Route, body: unknown) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    body: JSON.stringify(body),
  })
}

for (const routePath of smokeRoutes) {
  test(`${routePath} renders without console errors or horizontal overflow`, async ({
    page,
  }) => {
    const errors: string[] = []
    page.on("console", (message) => {
      if (message.type() === "error") {
        errors.push(message.text())
      }
    })
    page.on("pageerror", (error) => {
      errors.push(error.message)
    })

    await mockLauncherApis(page)
    await page.goto(routePath)
    await expect(page.getByRole("banner")).toBeVisible()
    await expect(page.locator("main")).toBeVisible()
    await expect(page.getByRole("button").first()).toBeVisible()
    await page.waitForTimeout(500)

    const hasHorizontalOverflow = await page.evaluate(() => {
      const doc = document.documentElement
      const body = document.body
      const scrollWidth = Math.max(doc.scrollWidth, body.scrollWidth)
      const clientWidth = Math.max(doc.clientWidth, window.innerWidth)
      return scrollWidth > clientWidth + 1
    })

    expect(hasHorizontalOverflow).toBe(false)
    expect(errors).toEqual([])
  })
}
