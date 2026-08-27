import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  bulkDeleteAccountRouters,
  createAccountRouter,
  getAccount,
  getAccountRouter,
  listAccountRouters,
  listAccounts,
  setDefaultAccountRouter,
  updateAccountRouter,
} from "@/api/accounts"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

function jsonResponse(value: unknown, status = 200) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("accounts collection API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("passes canonical collection paging parameters and abort signals", async () => {
    mockedLauncherFetch.mockImplementation(async () =>
      jsonResponse({
        accounts: [],
        total: 0,
        canonical_query: "provider = openai ORDER BY id ASC",
        query_schema: { fields: [] },
      }),
    )
    const controller = new AbortController()

    await listAccounts(
      {
        query: "provider = openai ORDER BY id ASC",
        cursor: "opaque+/cursor",
        limit: 50,
      },
      controller.signal,
    )

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/accounts?query=provider+%3D+openai+ORDER+BY+id+ASC&cursor=opaque%2B%2Fcursor&limit=50",
      { signal: controller.signal },
    )
  })

  it("treats account and router identities as backend-provided path values", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          account: {
            id: "opaque-account-id",
            provider: "openai",
            account: "openai:work",
            status: "connected",
            auth_method: "device_code",
            expires_at: "",
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          account_router: { id: "team-router" },
          config_revision: "r1",
        }),
      )

    await getAccount("opaque-account-id")
    await getAccountRouter("team-router")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/accounts/opaque-account-id",
      undefined,
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/account-routers/team-router",
      undefined,
    )
  })

  it("uses one revision fence for router create, update, and bulk deletion", async () => {
    const router = {
      name: "team-router",
      enabled: true,
      entry: "primary",
      refresh_interval_seconds: 60,
      blocks: [
        {
          id: "primary",
          type: "account" as const,
          account: "credential:openai:work",
        },
      ],
    }
    mockedLauncherFetch.mockImplementation(async () =>
      jsonResponse({
        account_router: { id: router.name, ...router },
        config_revision: "revision-2",
        effects: {
          launcher_effect: "applied",
          catalog_effect: "applied",
          gateway_effect: "applied",
        },
        deleted_ids: [],
        failures: [],
      }),
    )

    await createAccountRouter(router, "revision-1")
    await updateAccountRouter("team-router", router, "revision-2")
    await bulkDeleteAccountRouters(["team-router"], "revision-3")
    await setDefaultAccountRouter("team-router", "revision-4")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/account-routers",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_config_revision: "revision-1",
          account_router: router,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/account-routers/team-router",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          expected_config_revision: "revision-2",
          account_router: router,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/account-routers/bulk-delete",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          ids: ["team-router"],
          config_revision: "revision-3",
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      4,
      "/api/account-routers/team-router/default",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ expected_config_revision: "revision-4" }),
      }),
    )
  })

  it("uses the router resource key in collection envelopes", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        account_routers: [],
        total: 0,
        canonical_query: "ORDER BY name ASC",
        query_schema: { fields: [] },
        config_revision: "revision-1",
      }),
    )

    const response = await listAccountRouters({ limit: 25 })

    expect(response.account_routers).toEqual([])
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/account-routers?limit=25",
      undefined,
    )
  })
})
