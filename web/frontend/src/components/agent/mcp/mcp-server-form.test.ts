import { describe, expect, it } from "vitest"

import type { MCPServer } from "@/api/mcp"

import {
  draftFromMCPServer,
  hasMCPServerOriginChanged,
  newKeyValueRow,
  serverInputFromDraft,
  validateMCPServerDraft,
} from "./mcp-server-form"

const server: MCPServer = {
  name: "context7",
  enabled: true,
  deferred: null,
  type: "http",
  url: "https://example.com/mcp",
  command: "",
  args: [],
  env_file: "",
  env_keys: [],
  header_keys: ["X-API-Key"],
  auth: {
    type: "headers",
    configured: true,
  },
}

describe("MCP server form", () => {
  it("rejects names that are unsafe for the credential store", () => {
    const draft = {
      ...draftFromMCPServer(server),
      name: "context 7",
    }

    expect(validateMCPServerDraft(draft, [], server)).toMatchObject({
      name: "invalid_name",
    })
  })

  it("sends explicit secret key lists while preserving blank saved values", () => {
    const draft = {
      ...draftFromMCPServer(server),
      headerRows: [newKeyValueRow("X-API-Key", "")],
    }

    expect(serverInputFromDraft(draft)).toMatchObject({
      env_keys: [],
      headers: { "X-API-Key": "" },
      header_keys: ["X-API-Key"],
    })
  })

  it("uses explicit empty key lists when switching transports", () => {
    const draft = {
      ...draftFromMCPServer(server),
      type: "stdio" as const,
      command: "npx",
    }

    expect(serverInputFromDraft(draft)).toMatchObject({
      header_keys: [],
      headers: {},
    })
  })

  it("retains OAuth as the selected authentication mode", () => {
    const draft = draftFromMCPServer({
      ...server,
      header_keys: [],
      auth: { type: "oauth", configured: true },
    })

    expect(draft.authMode).toBe("oauth")
    expect(serverInputFromDraft(draft)).toMatchObject({
      headers: {},
      header_keys: [],
    })
  })

  it("requires secret values to be re-entered when the endpoint changes", () => {
    const customDraft = {
      ...draftFromMCPServer(server),
      url: "https://new.example.com/mcp",
      headerRows: [newKeyValueRow("X-API-Key", "")],
    }
    expect(validateMCPServerDraft(customDraft, [], server)).toMatchObject({
      headers: "secret_reentry_required",
    })

    const bearerServer: MCPServer = {
      ...server,
      header_keys: [],
      auth: { type: "bearer", configured: true },
    }
    const bearerDraft = {
      ...draftFromMCPServer(bearerServer),
      url: "https://new.example.com/mcp",
    }
    expect(validateMCPServerDraft(bearerDraft, [], bearerServer)).toMatchObject(
      {
        token: "required",
      },
    )
  })

  it("only treats a scheme, host, or effective port change as a new origin", () => {
    expect(
      hasMCPServerOriginChanged(
        "https://example.com/mcp",
        "https://example.com/v2/mcp",
      ),
    ).toBe(false)
    expect(
      hasMCPServerOriginChanged(
        "https://example.com/mcp",
        "https://example.com:443/v2/mcp",
      ),
    ).toBe(false)
    expect(
      hasMCPServerOriginChanged(
        "https://example.com/mcp",
        "https://api.example.com/mcp",
      ),
    ).toBe(true)
  })

  it("requires HTTPS for bearer and OAuth credentials except on loopback", () => {
    const publicBearer = {
      ...draftFromMCPServer({
        ...server,
        header_keys: [],
        auth: { type: "none", configured: false },
      }),
      url: "http://mcp.example.com/api",
      authMode: "bearer" as const,
      token: "secret",
    }
    expect(validateMCPServerDraft(publicBearer, [], server)).toMatchObject({
      url: "secure_auth_url",
    })

    expect(
      validateMCPServerDraft(
        { ...publicBearer, url: "http://127.0.0.1:9123/mcp" },
        [],
        server,
      ).url,
    ).toBeUndefined()
    expect(
      validateMCPServerDraft(
        { ...publicBearer, url: "http://127.999.0.1:9123/mcp" },
        [],
        server,
      ).url,
    ).toBe("invalid")
  })

  it("rejects credentials embedded in a remote server URL", () => {
    const draft = {
      ...draftFromMCPServer(server),
      url: "https://user:secret@example.com/mcp",
    }
    expect(validateMCPServerDraft(draft, [], server)).toMatchObject({
      url: "invalid",
    })
  })

  it("allows retrying a name that was already persisted by this sheet", () => {
    const renamedDraft = {
      ...draftFromMCPServer(server),
      name: "renamed",
    }

    expect(
      validateMCPServerDraft(renamedDraft, ["renamed"], server, "renamed").name,
    ).toBeUndefined()
  })
})
