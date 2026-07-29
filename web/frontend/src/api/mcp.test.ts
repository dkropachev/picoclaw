import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  addMCPServer,
  getMCPOAuthFlow,
  setMCPServerCredential,
  startMCPServerOAuth,
  testMCPServer,
  updateMCPServer,
} from "@/api/mcp"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const server = {
  name: "context7",
  enabled: true,
  deferred: null,
  type: "http" as const,
  url: "https://mcp.context7.com/mcp",
}

describe("MCP API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
    mockedLauncherFetch.mockImplementation(async () =>
      jsonResponse({ status: "ok" }),
    )
  })

  it("adds and updates a server using dedicated endpoints", async () => {
    await addMCPServer(server)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith("/api/mcp/servers", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(server),
    })

    await updateMCPServer("old context", server)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/mcp/servers/old%20context",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(server),
      },
    )
  })

  it("tests unsaved server settings with the saved server name", async () => {
    await testMCPServer(server, "context7")

    expect(mockedLauncherFetch).toHaveBeenCalledWith("/api/mcp/servers/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "context7", server }),
    })
  })

  it("stores bearer credentials separately from server configuration", async () => {
    await setMCPServerCredential("context/7", "secret-token")

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/mcp/servers/context%2F7/credential",
      {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          token: "secret-token",
          auth_type: "bearer",
        }),
      },
    )
  })

  it("starts and checks a server-specific OAuth flow", async () => {
    await startMCPServerOAuth("remote server")
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/mcp/servers/remote%20server/oauth",
      {
        method: "POST",
      },
    )

    await getMCPOAuthFlow("flow/id")
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/mcp/oauth/flows/flow%2Fid",
      undefined,
    )
  })

  it("preserves a structured API error", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(JSON.stringify({ errors: ["URL is required"] }), {
        status: 400,
        statusText: "Bad Request",
      }),
    )

    await expect(addMCPServer(server)).rejects.toThrow("URL is required")
  })
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
