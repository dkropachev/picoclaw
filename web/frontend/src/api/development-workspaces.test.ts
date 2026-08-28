import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type CreateDevelopmentWorkspaceRequest,
  confirmDevelopmentCharter,
  createDevelopmentWorkspace,
  getDevelopmentCodeDiff,
  getDevelopmentConversation,
  getDevelopmentWorkspace,
  listDevelopmentRepositories,
  listDevelopmentWorkspaces,
  reconcileDevelopmentPublication,
  respondDevelopmentGate,
  saveDevelopmentCharter,
  sendDevelopmentMessage,
} from "@/api/development-workspaces"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const workspaceID = `devw_${"1".repeat(32)}`
const summary = {
  id: workspaceID,
  intent: "implement_feature",
  source_kind: "issue",
  repository: "octo/repo",
  title: "Improve retry feedback",
  phase: "implementation",
  execution_state: "running",
  version: 3,
  created_at: "2026-08-24T10:00:00Z",
  updated_at: "2026-08-24T10:05:00Z",
} as const
const collectionSummary = {
  id: workspaceID,
  intent: "implement_feature",
  source: "issue",
  repository: "octo/repo",
  title: "Improve retry feedback",
  phase: "implementation",
  execution_state: "running",
  created: "2026-08-24T10:00:00Z",
  updated: "2026-08-24T10:05:00Z",
} as const

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function developmentWorkspaceQuerySchema() {
  const stringOperators = ["=", "!=", "~", "!~", "IN", "NOT IN"]
  const enumOperators = ["=", "!=", "IN", "NOT IN"]
  const timestampOperators = ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]
  return {
    fields: [
      {
        name: "id",
        type: "string",
        operators: stringOperators,
        sortable: true,
        suggested_values: [workspaceID],
      },
      {
        name: "intent",
        type: "enum",
        operators: enumOperators,
        sortable: true,
        suggested_values: ["implement_feature", "pickup_pr"],
      },
      {
        name: "source",
        type: "enum",
        operators: enumOperators,
        sortable: true,
        suggested_values: ["issue", "brief", "pull_request"],
      },
      {
        name: "repository",
        type: "string",
        operators: stringOperators,
        sortable: true,
        suggested_values: ["octo/repo"],
      },
      {
        name: "title",
        type: "string",
        operators: stringOperators,
        sortable: true,
        suggested_values: ["Improve retry feedback"],
      },
      {
        name: "phase",
        type: "enum",
        operators: enumOperators,
        sortable: true,
        suggested_values: [
          "intake",
          "charter",
          "planning",
          "review",
          "triage",
          "implementation",
          "validation",
          "completion_audit",
          "publication",
          "complete",
        ],
      },
      {
        name: "execution_state",
        type: "enum",
        operators: enumOperators,
        sortable: true,
        suggested_values: [
          "queued",
          "running",
          "waiting_gate",
          "waiting_user",
          "succeeded",
          "failed",
          "blocked",
          "canceled",
          "stale",
          "unknown",
        ],
      },
      {
        name: "created",
        type: "timestamp",
        operators: timestampOperators,
        sortable: true,
      },
      {
        name: "updated",
        type: "timestamp",
        operators: timestampOperators,
        sortable: true,
      },
    ],
    default_order: [{ field: "updated", direction: "DESC" }],
  }
}

describe("development workspace API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("lists workspaces and configured implementation repositories", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          workspaces: [collectionSummary],
          total: 1,
          next_cursor: "next+/=",
          canonical_query: "ALL ORDER BY updated DESC",
          query_schema: developmentWorkspaceQuerySchema(),
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          repositories: [
            {
              identity: "https://github.com|100",
              name: "octo/repo",
              default_branch: "main",
              can_implement: true,
            },
          ],
        }),
      )

    await expect(
      listDevelopmentWorkspaces({
        query: "phase = implementation ORDER BY updated DESC",
        limit: 50,
        cursor: "cursor+/=",
      }),
    ).resolves.toEqual({
      workspaces: [collectionSummary],
      total: 1,
      next_cursor: "next+/=",
      canonical_query: "ALL ORDER BY updated DESC",
      query_schema: { fields: developmentWorkspaceQuerySchema().fields },
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/development-workspaces?query=phase+%3D+implementation+ORDER+BY+updated+DESC&cursor=cursor%2B%2F%3D&limit=50",
      { signal: undefined },
    )
    await expect(listDevelopmentRepositories()).resolves.toEqual({
      repositories: [
        {
          identity: "https://github.com|100",
          name: "octo/repo",
          default_branch: "main",
          can_implement: true,
        },
      ],
    })
  })

  it("rejects unsafe collection identities and noncanonical query schemas", async () => {
    const invalidID = {
      workspaces: [{ ...collectionSummary, id: "devw_unsafe" }],
      total: 1,
      canonical_query: "ALL ORDER BY updated DESC",
      query_schema: developmentWorkspaceQuerySchema(),
    }
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(invalidID))
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: "malformed_response",
      status: 502,
    })

    const invalidSchema = developmentWorkspaceQuerySchema()
    invalidSchema.default_order[0] = {
      field: "updated",
      direction: "ASC",
    }
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        workspaces: [collectionSummary],
        total: 1,
        canonical_query: "ALL ORDER BY updated DESC",
        query_schema: invalidSchema,
      }),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: "malformed_response",
      status: 502,
    })
  })

  it("preserves bounded UTF-8 collection query error positions", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          code: "invalid_query",
          message: "Unexpected operator",
          position: 17,
        },
        400,
      ),
    )
    await expect(
      listDevelopmentWorkspaces({ query: "title !! retry" }),
    ).rejects.toMatchObject({
      code: "invalid_query",
      status: 400,
      position: 17,
      message: "Unexpected operator",
    })
  })

  it("normalizes and bounds structured error data", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          code: "invalid_query",
          message: `Unexpected\noperator\t${"💡".repeat(400)}`,
          position: 7,
        },
        400,
      ),
    )
    try {
      await listDevelopmentWorkspaces({ query: "title !! retry" })
      throw new Error("expected request failure")
    } catch (error) {
      expect(error).toMatchObject({
        code: "invalid_query",
        status: 400,
        position: 7,
      })
      expect((error as Error).message).not.toMatch(/[\r\n\t]/u)
      expect(
        new TextEncoder().encode((error as Error).message).byteLength,
      ).toBeLessThanOrEqual(1024)
    }
  })

  it("drops invalid error codes and query positions", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        {
          code: "UNSAFE code",
          message: "Safe message",
          position: 4097,
        },
        422,
      ),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: undefined,
      status: 422,
      position: undefined,
      message: "Safe message",
    })
  })

  it("uses safe bounded fallbacks for malformed and oversized error bodies", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response("<html>private upstream failure</html>", {
        status: 502,
        headers: { "Content-Type": "text/html" },
      }),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: undefined,
      status: 502,
      message: "Request failed with status 502.",
    })

    mockedLauncherFetch.mockResolvedValueOnce(
      new Response("x".repeat((1 << 20) + 1), {
        status: 503,
        headers: { "Content-Type": "text/plain" },
      }),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: undefined,
      status: 503,
      message: "Request failed with status 503.",
    })

    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          code: "invalid_query",
          message: "private non-JSON response metadata",
        }),
        {
          status: 502,
          headers: { "Content-Type": "text/plain" },
        },
      ),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: undefined,
      status: 502,
      message: "Request failed with status 502.",
    })
  })

  it("rejects successful JSON with a non-JSON content type", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          workspaces: [],
          total: 0,
          canonical_query: "ALL ORDER BY updated DESC",
          query_schema: developmentWorkspaceQuerySchema(),
        }),
        {
          status: 200,
          headers: { "Content-Type": "text/plain" },
        },
      ),
    )
    await expect(listDevelopmentWorkspaces()).rejects.toMatchObject({
      code: "malformed_response",
      status: 502,
    })
  })

  it("binds direct detail responses to the requested workspace ID", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        ...summary,
        id: `devw_${"2".repeat(32)}`,
        source: { kind: "issue", url: "" },
      }),
    )
    await expect(getDevelopmentWorkspace(workspaceID)).rejects.toMatchObject({
      code: "malformed_response",
      status: 502,
    })
  })

  it("derives display data and candidate evidence from the backend aggregate", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        workspace: {
          id: workspaceID,
          intent: "implement_feature",
          source_kind: "issue",
          source_number: 7,
          repository: "octo/repo",
          phase: "validation",
          execution_state: "waiting_gate",
          version: 4,
          created_at: "2026-08-24T10:00:00Z",
          updated_at: "2026-08-24T10:06:00Z",
        },
        provider_snapshot: {
          intent: "implement_feature",
          source_kind: "issue",
          source_url: "https://github.com/octo/repo/issues/7",
          source_number: 7,
          title: "Improve retry feedback",
          body: "Show the user when another attempt is running.",
          base_sha: "base:1",
          head_sha: "head:1",
          provider_revision: "provider:4",
        },
        repair_attempts: [
          {
            candidate_sha: "candidate:2",
            changed_files: ["src/retry.ts"],
          },
        ],
        charters: [
          {
            id: `pcr_${"7".repeat(32)}`,
            revision: 1,
            type: "feature",
            goal: "Improve retry feedback",
            acceptance_criteria: ["Show retry state"],
            included_areas: ["web"],
            excluded_areas: [],
            non_goals: [],
            clarification_needed: true,
            clarification_question: "Which retry states?",
            confirmed: false,
          },
        ],
        validation_runs: [
          {
            candidate_sha: "candidate:2",
            checks: [{ id: "test", name: "Tests", status: "passed" }],
          },
        ],
        stage_runs: [{ summary: "Candidate ready for validation." }],
        gates: [
          {
            id: `pgr_${"8".repeat(32)}`,
            decision_point: "development.scope",
            state: "waiting_user",
            turns: [
              {
                stage_id: "stage-1",
                kind: "human",
                title: "Choose scope",
                status: "waiting_user",
                "gate-form": {
                  "gate-ref": "scope",
                  prompt: "Choose what belongs in this PR.",
                  fields: [
                    {
                      id: "decision",
                      type: "select",
                      label: "Decision",
                      required: true,
                      "min-selections": 1,
                      "max-selections": 1,
                      options: [{ id: "include", label: "Include" }],
                    },
                  ],
                },
              },
            ],
            created_at: "2026-08-24T10:05:00Z",
          },
        ],
        publications: [
          {
            id: `ppu_${"9".repeat(32)}`,
            kind: "branch_push",
            state: "unknown",
            updated_at: "2026-08-24T10:06:00Z",
          },
        ],
        activity: [],
      }),
    )

    const { getDevelopmentWorkspace } =
      await import("@/api/development-workspaces")
    await expect(getDevelopmentWorkspace(workspaceID)).resolves.toMatchObject({
      title: "Improve retry feedback",
      execution_state: "waiting_gate",
      source: {
        kind: "issue",
        url: "https://github.com/octo/repo/issues/7",
        number: 7,
      },
      charter: {
        goal: "Improve retry feedback",
        clarification_needed: true,
        clarification_question: "Which retry states?",
      },
      base_revision: "base:1",
      candidate_revision: "candidate:2",
      head_revision: "provider:4",
      changed_files: ["src/retry.ts"],
      validation_checks: [{ name: "Tests", status: "passed" }],
      summary: "Candidate ready for validation.",
      gates: [
        {
          decision_point: "development.scope",
          turns: [
            {
              gate_form: {
                gate_ref: "scope",
                fields: [
                  {
                    id: "decision",
                    min_selections: 1,
                    max_selections: 1,
                  },
                ],
              },
            },
          ],
        },
      ],
      publications: [{ kind: "branch_push", state: "unknown" }],
    })
  })

  it("sends each mutually exclusive intake variant without leaked fields", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...summary,
        source: {
          kind: "issue",
          url: "https://github.com/octo/repo/issues/7",
        },
      }),
    )
    await createDevelopmentWorkspace({
      intent: "implement_feature",
      source: {
        kind: "issue",
        issue_url: "https://github.com/octo/repo/issues/7",
      },
      request_id: `devq_${"2".repeat(32)}`,
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/development-workspaces",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          intent: "implement_feature",
          source: {
            kind: "issue",
            issue_url: "https://github.com/octo/repo/issues/7",
          },
          request_id: `devq_${"2".repeat(32)}`,
        }),
      }),
    )

    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...summary,
        source_kind: "brief",
        source: { kind: "brief", content: "Add retry feedback." },
      }),
    )
    await createDevelopmentWorkspace({
      intent: "implement_feature",
      source: {
        kind: "brief",
        repository_identity: "https://github.com|100",
        content: "Add retry feedback.",
      },
      request_id: `devq_${"3".repeat(32)}`,
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/development-workspaces",
      expect.objectContaining({
        body: JSON.stringify({
          intent: "implement_feature",
          source: {
            kind: "brief",
            repository_identity: "https://github.com|100",
            content: "Add retry feedback.",
          },
          request_id: `devq_${"3".repeat(32)}`,
        }),
      }),
    )

    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...summary,
        intent: "pickup_pr",
        source_kind: "pull_request",
        source: {
          kind: "pull_request",
          url: "https://github.com/octo/repo/pull/9",
        },
      }),
    )
    const unsafeMixed = {
      intent: "pickup_pr",
      pull_request_url: "https://github.com/octo/repo/pull/9",
      source: {
        kind: "issue",
        issue_url: "https://github.com/octo/repo/issues/7",
      },
      request_id: `devq_${"4".repeat(32)}`,
    } as unknown as CreateDevelopmentWorkspaceRequest
    await createDevelopmentWorkspace(unsafeMixed)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/development-workspaces",
      expect.objectContaining({
        body: JSON.stringify({
          intent: "pickup_pr",
          pull_request_url: "https://github.com/octo/repo/pull/9",
          request_id: `devq_${"4".repeat(32)}`,
        }),
      }),
    )
  })

  it("revision-fences chat steering and exact code reads", async () => {
    const conversation = {
      revision: 4,
      messages: [
        {
          id: "msg_1",
          role: "user",
          mode: "steer",
          status: "queued",
          content: "Keep the error copy concise.",
          created_at: "2026-08-24T10:06:00Z",
        },
      ],
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(conversation))
      .mockResolvedValueOnce(jsonResponse(conversation))
      .mockResolvedValueOnce(
        jsonResponse({
          base_revision: "base:1",
          candidate_revision: "candidate:2",
          path: "src/retry.ts",
          base_content: "old\n",
          candidate_content: "new\n",
          diff: "@@ -1 +1 @@\n-old\n+new",
        }),
      )

    await getDevelopmentConversation(workspaceID)
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      `/api/development-workspaces/${workspaceID}/conversation/messages`,
      { signal: undefined },
    )
    await sendDevelopmentMessage(workspaceID, {
      mode: "steer",
      content: "Keep the error copy concise.",
      expected_revision: 3,
      request_id: `devq_${"5".repeat(32)}`,
      candidate_revision: "candidate:2",
    })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      `/api/development-workspaces/${workspaceID}/conversation/messages`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          mode: "steer",
          content: "Keep the error copy concise.",
          expected_revision: 3,
          request_id: `devq_${"5".repeat(32)}`,
          candidate_revision: "candidate:2",
        }),
      }),
    )
    await expect(
      getDevelopmentCodeDiff(workspaceID, {
        revision: "candidate:2",
        path: "src/retry copy.ts",
      }),
    ).resolves.toMatchObject({ original: "old\n", modified: "new\n" })
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      `/api/development-workspaces/${workspaceID}/code/diff?revision=candidate%3A2&path=src%2Fretry+copy.ts`,
      { signal: undefined },
    )
  })

  it("accepts bounded tree and unified-diff projections from the runtime", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          revision: "candidate:2",
          entries: [{ path: "src/retry.ts", type: "file" }],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          revision: "evidence:3",
          path: "src/retry.ts",
          diff: "@@ -1 +1 @@\n-old\n+new",
        }),
      )
    const { getDevelopmentCodeTree } =
      await import("@/api/development-workspaces")
    await expect(
      getDevelopmentCodeTree(workspaceID, { revision: "candidate:2" }),
    ).resolves.toMatchObject({
      entries: [{ name: "src/retry.ts", path: "src/retry.ts", type: "file" }],
    })
    await expect(
      getDevelopmentCodeDiff(workspaceID, {
        revision: "candidate:2",
        path: "src/retry.ts",
      }),
    ).resolves.toEqual({
      base_revision: "evidence:3",
      candidate_revision: "evidence:3",
      path: "src/retry.ts",
      unified_diff: "@@ -1 +1 @@\n-old\n+new",
    })
  })

  it("responds to gates and reconciles publication through fenced mutations", async () => {
    const aggregate = {
      ...summary,
      source: {
        kind: "issue",
        url: "https://github.com/octo/repo/issues/7",
      },
      gates: [],
      publications: [],
    }
    mockedLauncherFetch.mockImplementation(async () => jsonResponse(aggregate))
    await saveDevelopmentCharter(workspaceID, {
      expected_version: 3,
      expected_head_revision: "provider:3",
      request_id: `devq_${"5".repeat(32)}`,
      charter: {
        type: "feature",
        goal: "Clarified goal",
        acceptance_criteria: ["One criterion"],
        included_areas: [],
        excluded_areas: [],
        non_goals: [],
      },
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/development-workspaces/${workspaceID}/charter`,
      expect.objectContaining({
        method: "PUT",
        body: JSON.stringify({
          expected_version: 3,
          expected_head_revision: "provider:3",
          request_id: `devq_${"5".repeat(32)}`,
          pr_type: "feature",
          goal: "Clarified goal",
          acceptance_criteria: ["One criterion"],
          included_areas: [],
          exclusions: [],
          non_goals: [],
        }),
      }),
    )
    await confirmDevelopmentCharter(workspaceID, {
      expected_version: 3,
      expected_charter_revision: 1,
      request_id: `devq_${"5".repeat(32)}`,
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/development-workspaces/${workspaceID}/charter/confirm`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_version: 3,
          expected_charter_revision: 1,
          request_id: `devq_${"5".repeat(32)}`,
        }),
      }),
    )
    await respondDevelopmentGate(workspaceID, "pgr/unsafe", {
      expected_version: 3,
      request_id: `devq_${"6".repeat(32)}`,
      field_values: { decision: "approve" },
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/development-workspaces/${workspaceID}/gates/pgr%2Funsafe/respond`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_version: 3,
          request_id: `devq_${"6".repeat(32)}`,
          "field-values": { decision: "approve" },
        }),
      }),
    )

    await reconcileDevelopmentPublication(workspaceID, "ppu/unsafe", {
      expected_version: 3,
      expected_head_revision: "provider:3",
      request_id: `devq_${"7".repeat(32)}`,
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/development-workspaces/${workspaceID}/publications/ppu%2Funsafe/reconcile`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_version: 3,
          expected_head_revision: "provider:3",
          request_id: `devq_${"7".repeat(32)}`,
        }),
      }),
    )
  })
})
