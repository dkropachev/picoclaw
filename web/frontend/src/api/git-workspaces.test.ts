import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  cleanupGitWorkspace,
  dropGitWorkspace,
  getGitWorkspace,
  getGitWorkspaceSettings,
  listGitWorkspaceHistory,
  listGitWorkspaces,
  reconcileGitWorkspaces,
  updateGitWorkspaceSettings,
} from "@/api/git-workspaces"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))
const mockedFetch = vi.mocked(launcherFetch)
const workspaceID = "gw-0123456789ab"
const summary = {
  id: workspaceID,
  repository: "git@example.test:team/repo.git",
  branch: "main",
  status: "available",
  locked: false,
  dirty: false,
  size: 4096,
  ignored: 512,
  updated: "2026-08-01T00:00:00Z",
}
const workspaceSchema = querySchema(
  [
    queryField("id", "string", [workspaceID]),
    queryField("repository", "string", [summary.repository]),
    queryField("branch", "string", [summary.branch]),
    queryField("status", "enum", ["available", "locked", "dropped"]),
    queryField("locked", "boolean", ["true", "false"]),
    queryField("dirty", "boolean", ["true", "false"]),
    queryField("size", "number"),
    queryField("ignored", "number"),
    queryField("updated", "timestamp"),
  ],
  "updated",
)
const historySchema = querySchema(
  [
    queryField("action", "string", ["allocated"]),
    queryField("workspace", "string", [workspaceID]),
    queryField("repository", "string", [summary.repository]),
    queryField("agent", "string", ["main"]),
    queryField("time", "timestamp"),
  ],
  "time",
)

describe("git workspace API", () => {
  beforeEach(() => mockedFetch.mockReset())

  it("lists the server-paged collection and direct exact item", async () => {
    mockedFetch
      .mockResolvedValueOnce(
        jsonResponse({
          workspaces: [summary],
          total: 1,
          next_cursor: "next-cursor",
          canonical_query: "ORDER BY updated DESC",
          query_schema: workspaceSchema,
          max_total_size_bytes: 1024,
          total_size_bytes: 512,
          ignored_bytes: 64,
          repository_count: 1,
          workspace_count: 1,
          locked_workspace_count: 0,
          ignored_cleanup_delay_seconds: 3600,
          drop_delay_seconds: 86400,
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          workspace: {
            ...summary,
            repository_id: "gw-fedcba987654",
            remote_url: "https://example.test/team/repo.git",
            path: "/tmp/workspaces/repo",
            created: "2026-07-31T00:00:00Z",
          },
        }),
      )

    await expect(
      listGitWorkspaces({
        query: "status = available",
        cursor: "cursor",
        limit: 25,
      }),
    ).resolves.toMatchObject({ workspaces: [summary], total: 1 })
    expect(mockedFetch).toHaveBeenNthCalledWith(
      1,
      "/api/git-workspaces?query=status+%3D+available&cursor=cursor&limit=25",
      undefined,
    )
    await expect(getGitWorkspace(workspaceID)).resolves.toMatchObject({
      workspace: { id: workspaceID, path: "/tmp/workspaces/repo" },
    })
  })

  it("loads paged operational history", async () => {
    mockedFetch.mockResolvedValueOnce(
      jsonResponse({
        history: [
          {
            id: "abcdef012345",
            action: "allocated",
            workspace: workspaceID,
            repository: "git@example.test:team/repo.git",
            agent: "main",
            time: "2026-08-01T00:00:00Z",
          },
        ],
        total: 1,
        canonical_query: "ORDER BY time DESC",
        query_schema: historySchema,
      }),
    )
    await expect(listGitWorkspaceHistory()).resolves.toMatchObject({
      history: [{ id: "abcdef012345", action: "allocated" }],
    })
  })

  it("reads and revision-fences scoped settings", async () => {
    const response = {
      configured: {
        max_total_size_bytes: 1024,
        ignored_cleanup_delay_seconds: 3600,
        drop_delay_seconds: 86400,
      },
      effective: {
        max_total_size_bytes: 1024,
        ignored_cleanup_delay_seconds: 3600,
        drop_delay_seconds: 86400,
      },
      config_revision: "revision-1",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "restart_required",
      },
    }
    mockedFetch
      .mockResolvedValueOnce(jsonResponse(response))
      .mockResolvedValueOnce(jsonResponse(response))
    await expect(getGitWorkspaceSettings()).resolves.toEqual(response)
    await updateGitWorkspaceSettings(response.configured, "revision-1")
    expect(mockedFetch).toHaveBeenNthCalledWith(
      2,
      "/api/git-workspaces/settings",
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          expected_config_revision: "revision-1",
          settings: response.configured,
        }),
      }),
    )
  })

  it("sends maintenance, cleanup, and drop requests", async () => {
    mockedFetch
      .mockResolvedValueOnce(
        jsonResponse({
          cleaned: [],
          dropped: [],
          stats: {
            max_total_size_bytes: 1024,
            ignored_cleanup_delay_seconds: 3600,
            drop_delay_seconds: 86400,
            total_size_bytes: 512,
            ignored_bytes: 64,
            repository_count: 1,
            workspace_count: 1,
            locked_workspace_count: 0,
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          before_ignored_bytes: 64,
          after_ignored_bytes: 0,
          workspace: {
            ...summary,
            repository_id: "gw-fedcba987654",
            created: "2026-07-31T00:00:00Z",
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          workspace: {
            ...summary,
            repository_id: "gw-fedcba987654",
            created: "2026-07-31T00:00:00Z",
          },
        }),
      )

    await reconcileGitWorkspaces()
    await cleanupGitWorkspace(workspaceID)
    await dropGitWorkspace(workspaceID)
    expect(mockedFetch).toHaveBeenNthCalledWith(
      2,
      "/api/git-workspaces/cleanup",
      expect.objectContaining({
        body: JSON.stringify({ workspace_id: workspaceID }),
      }),
    )
    expect(mockedFetch).toHaveBeenNthCalledWith(
      3,
      `/api/git-workspaces/${workspaceID}`,
      { method: "DELETE" },
    )
  })

  it("rejects malformed and duplicate collection identities", async () => {
    mockedFetch.mockResolvedValueOnce(
      jsonResponse({
        workspaces: [summary, summary],
        total: 2,
        canonical_query: "ORDER BY updated DESC",
        query_schema: workspaceSchema,
        max_total_size_bytes: 1024,
        total_size_bytes: 512,
        ignored_bytes: 64,
        repository_count: 1,
        workspace_count: 2,
        locked_workspace_count: 0,
        ignored_cleanup_delay_seconds: 3600,
        drop_delay_seconds: 86400,
      }),
    )
    await expect(listGitWorkspaces()).rejects.toMatchObject({
      status: 502,
      code: "malformed_response",
    })
  })

  it.each([
    [
      "non-record field",
      (schema: MutableQuerySchema) => (schema.fields[0] = null),
    ],
    [
      "non-canonical field name",
      (schema: MutableQuerySchema) => setSchemaField(schema, 0, "name", "Not"),
    ],
    [
      "duplicate field name",
      (schema: MutableQuerySchema) =>
        schema.fields.push(structuredClone(schema.fields[0])),
    ],
    [
      "unsupported field type",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "type", "object"),
    ],
    [
      "invalid operator",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "operators", ["DROP"]),
    ],
    [
      "duplicate operator",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "operators", ["=", "="]),
    ],
    [
      "operator incompatible with field type",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 3, "operators", ["~"]),
    ],
    [
      "non-boolean sortable",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "sortable", "yes"),
    ],
    [
      "non-array suggestions",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "suggested_values", "value"),
    ],
    [
      "duplicate suggestions",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "suggested_values", ["alpha", "ALPHA"]),
    ],
    [
      "too many suggestions",
      (schema: MutableQuerySchema) =>
        setSchemaField(
          schema,
          0,
          "suggested_values",
          Array.from({ length: 101 }, (_, index) => `value-${index}`),
        ),
    ],
    [
      "oversized UTF-8 suggestion",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 0, "suggested_values", ["é".repeat(129)]),
    ],
    [
      "enum without suggestions",
      (schema: MutableQuerySchema) =>
        setSchemaField(schema, 3, "suggested_values", []),
    ],
    [
      "missing default order",
      (schema: MutableQuerySchema) => (schema.default_order = []),
    ],
    [
      "unknown default-order field",
      (schema: MutableQuerySchema) =>
        (schema.default_order = [{ field: "missing", direction: "DESC" }]),
    ],
    [
      "non-canonical default order",
      (schema: MutableQuerySchema) =>
        (schema.default_order = [{ field: "updated", direction: "ASC" }]),
    ],
  ])("rejects malformed query schema: %s", async (_name, mutate) => {
    const candidate = structuredClone(workspaceSchema) as MutableQuerySchema
    mutate(candidate)
    mockedFetch.mockResolvedValueOnce(
      jsonResponse(workspaceListResponse(candidate)),
    )
    await expect(listGitWorkspaces()).rejects.toMatchObject({
      status: 502,
      code: "malformed_response",
    })
  })
})

type QueryFieldType = "string" | "enum" | "boolean" | "number" | "timestamp"
type MutableQuerySchema = { fields: unknown[]; default_order: unknown[] }

function queryField(
  name: string,
  type: QueryFieldType,
  suggestedValues: string[] = [],
) {
  const operators =
    type === "string"
      ? ["=", "!=", "~", "!~", "IN", "NOT IN"]
      : type === "enum" || type === "boolean"
        ? ["=", "!=", "IN", "NOT IN"]
        : ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]
  return {
    name,
    type,
    operators,
    sortable: true,
    ...(suggestedValues.length > 0
      ? { suggested_values: suggestedValues }
      : {}),
  }
}

function querySchema(fields: ReturnType<typeof queryField>[], field: string) {
  return { fields, default_order: [{ field, direction: "DESC" as const }] }
}

function setSchemaField(
  schema: MutableQuerySchema,
  index: number,
  key: string,
  value: unknown,
) {
  ;(schema.fields[index] as Record<string, unknown>)[key] = value
}

function workspaceListResponse(querySchema: unknown) {
  return {
    workspaces: [summary],
    total: 1,
    canonical_query: "ORDER BY updated DESC",
    query_schema: querySchema,
    max_total_size_bytes: 1024,
    total_size_bytes: 512,
    ignored_bytes: 64,
    repository_count: 1,
    workspace_count: 1,
    locked_workspace_count: 0,
    ignored_cleanup_delay_seconds: 3600,
    drop_delay_seconds: 86400,
  }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
