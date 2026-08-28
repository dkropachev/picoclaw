import { beforeEach, describe, expect, it, vi } from "vitest"

import { collectionRequest } from "@/api/collection"
import { requestDevelopmentJSON } from "@/api/development-workspaces"
import {
  createPRLifecycleWorkflowConfiguration,
  deletePRLifecycleWorkflowConfiguration,
  getPRLifecycleWorkflowConfiguration,
  getPRLifecycleWorkflowConfigurations,
  listPRLifecycleWorkflowConfigurations,
  makePRLifecycleWorkflowConfigurationDefault,
  putPRLifecycleWorkflowConfigurations,
  updatePRLifecycleWorkflowConfiguration,
  validatePRLifecycleWorkflowConfigurations,
} from "@/api/pr-lifecycle-workflow-configurations"

vi.mock("@/api/collection", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/collection")>()
  return { ...original, collectionRequest: vi.fn() }
})

vi.mock("@/api/pr-lifecycle-flow", () => ({
  projectPRLifecycleFlowCatalog: vi.fn(() => ({
    schema: "pr-lifecycle-flow/v1",
    flows: [],
  })),
}))

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return { ...original, requestDevelopmentJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestDevelopmentJSON)
const mockedCollectionRequest = vi.mocked(collectionRequest)

const wireSnapshot = {
  "workflow-configurations": {
    default: {
      name: "Default",
      bindings: [],
      "deferred-issues": { mode: "ask" },
    },
    automated: {
      name: "Automated",
      "deferred-issues": { mode: "automatic" },
      bindings: [
        {
          "workflow-ref": "workflows/pr-lifecycle.yml",
          "gate-ref": "gates.charter-confirm",
          action: {
            type: "ai",
            "agent-id": "main",
            prompt: "Complete every required Gate field.",
            session: "ephemeral",
            history: "none",
            cache: "none",
            tools: "none",
          },
        },
      ],
    },
  },
  "default-workflow-configuration": "default",
  nudge: {
    "review-minimum-additional": 1,
    "review-maximum-additional": 3,
    "completion-minimum-additional": 1,
    "completion-maximum-additional": 2,
  },
  scope: {
    xs: { files: 1, "semantic-lines": 20, modules: 1 },
    s: { files: 3, "semantic-lines": 100, modules: 1 },
    m: { files: 10, "semantic-lines": 500, modules: 3 },
  },
  "gate-catalog": {
    "pr.charter.reconfirm": {
      "workflow-ref": "workflows/pr-lifecycle.yml",
      "gate-ref": "gates.charter-reconfirm",
      "source-ai-supported": false,
      prompt: "Review the revised charter.",
      fields: [
        {
          id: "action",
          type: "select",
          label: "What should happen?",
          "min-selections": 1,
          "max-selections": 1,
          options: [
            { id: "approve", label: "Approve" },
            { id: "revise", label: "Request revision" },
          ],
        },
      ],
      "workflow-revision": "revision-1",
      "default-action": { type: "human" },
      "effective-action": {
        type: "ai",
        "agent-id": "main",
        prompt: "Complete every required Gate field.",
        session: "ephemeral",
        history: "none",
        cache: "none",
        tools: "none",
      },
      "action-source": "config-override",
    },
  },
  flow: {},
  "flow-revision": "flow-1",
  "catalog-revision": "catalog-1",
  "config-revision": "config-1",
  effects: {
    "gateway-effect": "applied",
    "deferred-policy-effect": "applied",
  },
}

describe("PR lifecycle Workflow configurations", () => {
  beforeEach(() => {
    mockedRequest.mockReset()
    mockedCollectionRequest.mockReset()
  })

  it("projects the kebab-case wire contract into idiomatic client fields", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)

    const snapshot = await getPRLifecycleWorkflowConfigurations()

    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/development/workflow-configurations",
      undefined,
      undefined,
    )
    expect(snapshot.defaultWorkflowConfiguration).toBe("default")
    expect(snapshot.nudge.reviewMinimumAdditional).toBe(1)
    expect(snapshot.scope.xs.semanticLines).toBe(20)
    expect(snapshot.workflowConfigurations.automated.deferredIssues.mode).toBe(
      "automatic",
    )
    expect(snapshot.effects).toEqual({
      gatewayEffect: "applied",
      deferredPolicyEffect: "applied",
    })
    expect(snapshot.gateCatalog["pr.charter.reconfirm"].sourceAISupported).toBe(
      false,
    )
    expect(snapshot.gateCatalog["pr.charter.reconfirm"].fields).toEqual([
      {
        id: "action",
        type: "select",
        label: "What should happen?",
        required: true,
        minSelections: 1,
        maxSelections: 1,
        options: [
          { id: "approve", label: "Approve" },
          { id: "revise", label: "Request revision" },
        ],
      },
    ])
    expect(snapshot.workflowConfigurations.automated.bindings[0]).toEqual({
      workflowRef: "workflows/pr-lifecycle.yml",
      gateRef: "gates.charter-confirm",
      action: {
        type: "ai",
        agentID: "main",
        prompt: "Complete every required Gate field.",
        session: "ephemeral",
        history: "none",
        cache: "none",
        tools: "none",
      },
    })
  })

  it("writes only kebab-case configuration fields", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)
    const snapshot = await getPRLifecycleWorkflowConfigurations()
    mockedRequest.mockResolvedValueOnce(wireSnapshot)

    await putPRLifecycleWorkflowConfigurations({
      expectedConfigRevision: snapshot.configRevision,
      requestID: "request-1",
      workflowConfigurations: snapshot.workflowConfigurations,
      defaultWorkflowConfiguration: snapshot.defaultWorkflowConfiguration,
      nudge: snapshot.nudge,
      scope: snapshot.scope,
    })

    const request = mockedRequest.mock.calls.find(
      ([, init]) => init?.method === "PUT",
    )?.[1]
    const body = JSON.parse(String(request?.body)) as Record<string, unknown>
    expect(body).toMatchObject({
      "expected-config-revision": "config-1",
      "request-id": "request-1",
      "default-workflow-configuration": "default",
    })
    const serializedConfigs = body["workflow-configurations"] as Record<
      string,
      Record<string, unknown>
    >
    expect(serializedConfigs.automated["deferred-issues"]).toEqual({
      mode: "automatic",
    })
    expect(body).not.toHaveProperty("deferred-issues")
    expect(body).not.toHaveProperty("repository-assignments")
    expect(JSON.stringify(body)).not.toContain("_")
  })

  it("rejects invalid local references and incomplete atomic overrides", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)
    const snapshot = await getPRLifecycleWorkflowConfigurations()
    snapshot.workflowConfigurations.automated.bindings = [
      {
        workflowRef: "workflows/pr-lifecycle",
        gateRef: "charter-decision",
        action: { type: "workflow", workflowRef: "" },
      },
    ]

    expect(validatePRLifecycleWorkflowConfigurations(snapshot)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ message: expect.stringContaining("gates.") }),
        expect.objectContaining({
          message: expect.stringContaining("workflow reference"),
        }),
      ]),
    )
  })

  it("matches backend workflow configuration identity and name validation", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)
    const snapshot = await getPRLifecycleWorkflowConfigurations()
    snapshot.workflowConfigurations["bad--id"] = {
      name: " Automated ",
      deferredIssues: { mode: "ask" },
      bindings: [],
    }

    expect(validatePRLifecycleWorkflowConfigurations(snapshot)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          path: "workflow-configurations.bad--id",
          message: expect.stringContaining("kebab-case"),
        }),
        expect.objectContaining({
          path: "workflow-configurations.bad--id.name",
          message: expect.stringContaining("trimmed"),
        }),
      ]),
    )

    snapshot.workflowConfigurations["bad--id"].name = "AUTOMATED"
    expect(validatePRLifecycleWorkflowConfigurations(snapshot)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          path: "workflow-configurations.bad--id.name",
          message: expect.stringContaining("duplicates"),
        }),
      ]),
    )
  })

  it("rejects AI policy values outside the closed runtime vocabulary", async () => {
    const invalid = structuredClone(wireSnapshot)
    invalid["workflow-configurations"].automated.bindings[0].action.session =
      "ambient"
    mockedRequest.mockResolvedValue(invalid)

    await expect(getPRLifecycleWorkflowConfigurations()).rejects.toMatchObject({
      code: "malformed_response",
    })
  })

  it("round-trips the minimal originating-session AI profile", async () => {
    const source = structuredClone(wireSnapshot)
    const sourceAction = source["workflow-configurations"].automated.bindings[0]
      .action as unknown as Record<string, unknown>
    for (const key of ["agent-id", "history", "cache", "tools"])
      delete sourceAction[key]
    Object.assign(sourceAction, {
      type: "ai",
      prompt: "Recheck the originating finding.",
      session: "source",
    })
    mockedRequest.mockResolvedValue(source)

    const snapshot = await getPRLifecycleWorkflowConfigurations()
    expect(
      snapshot.workflowConfigurations.automated.bindings[0].action,
    ).toEqual({
      type: "ai",
      prompt: "Recheck the originating finding.",
      session: "source",
    })

    mockedRequest.mockResolvedValueOnce(source)
    await putPRLifecycleWorkflowConfigurations({
      expectedConfigRevision: snapshot.configRevision,
      requestID: "request-source",
      workflowConfigurations: snapshot.workflowConfigurations,
      defaultWorkflowConfiguration: snapshot.defaultWorkflowConfiguration,
      nudge: snapshot.nudge,
      scope: snapshot.scope,
    })
    const request = mockedRequest.mock.calls.at(-1)?.[1]
    const body = JSON.parse(String(request?.body))
    expect(
      body["workflow-configurations"].automated.bindings[0].action,
    ).toEqual(source["workflow-configurations"].automated.bindings[0].action)
  })

  it("rejects partial or mismatched originating-session profiles", async () => {
    const invalid = structuredClone(wireSnapshot)
    const invalidAction = invalid["workflow-configurations"].automated
      .bindings[0].action as unknown as Record<string, unknown>
    for (const key of ["agent-id", "history", "cache"])
      delete invalidAction[key]
    Object.assign(invalidAction, {
      type: "ai",
      prompt: "Recheck.",
      session: "source",
      tools: "none",
    })
    mockedRequest.mockResolvedValue(invalid)

    await expect(getPRLifecycleWorkflowConfigurations()).rejects.toMatchObject({
      code: "malformed_response",
    })
  })

  it("projects collection summaries and full routed editor context", async () => {
    mockedCollectionRequest.mockResolvedValueOnce({
      workflow_configurations: [
        {
          id: "automated",
          name: "Automated",
          is_default: false,
          bindings: 1,
          deferred_issues: "automatic",
        },
      ],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY name ASC",
      query_schema: {
        fields: [
          {
            name: "name",
            type: "string",
            operators: ["=", "~"],
            sortable: true,
          },
        ],
        default_order: [{ field: "name", direction: "ASC" }],
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })

    const page = await listPRLifecycleWorkflowConfigurations({ limit: 25 })
    expect(mockedCollectionRequest).toHaveBeenCalledWith(
      "/api/development/workflow-configurations/items?limit=25",
      undefined,
      undefined,
    )
    expect(page.workflow_configurations[0]).toMatchObject({
      id: "automated",
      bindings: 1,
      deferred_issues: "automatic",
    })
    expect(page.next_cursor).toBeUndefined()

    mockedCollectionRequest.mockResolvedValueOnce(collectionDetailResponse())
    const detail = await getPRLifecycleWorkflowConfiguration("automated")
    expect(detail.workflow_configuration).toMatchObject({
      id: "automated",
      name: "Automated",
      isDefault: false,
      deferredIssues: { mode: "automatic" },
    })
    expect(detail.workflow_configuration.bindings[0]).toMatchObject({
      workflowRef: "workflows/pr-lifecycle.yml",
      gateRef: "gates.charter-confirm",
    })
  })

  it("uses item-scoped revision-fenced mutation bodies", async () => {
    mockedCollectionRequest.mockResolvedValue(collectionDetailResponse())
    const input = {
      id: "automated",
      name: "Automated",
      bindings: [
        {
          workflowRef: "workflows/pr-lifecycle.yml",
          gateRef: "gates.charter-confirm",
          action: { type: "human" as const },
        },
      ],
      deferredIssues: { mode: "automatic" as const },
      scopeDisposition: {
        default: { mode: "strict" as const, prompt: "" },
        byType: {},
      },
    }

    await createPRLifecycleWorkflowConfiguration(input, "revision-1")
    await updatePRLifecycleWorkflowConfiguration(
      "automated",
      input,
      "revision-2",
    )
    const defaultResponse = collectionDetailResponse()
    defaultResponse.workflow_configuration.is_default = true
    mockedCollectionRequest.mockResolvedValueOnce(defaultResponse)
    await makePRLifecycleWorkflowConfigurationDefault("automated", "revision-3")

    const createBody = JSON.parse(
      String(mockedCollectionRequest.mock.calls[0]?.[1]?.body),
    )
    expect(createBody).toEqual({
      expected_config_revision: "revision-1",
      workflow_configuration: {
        id: "automated",
        name: "Automated",
        bindings: [
          {
            workflow_ref: "workflows/pr-lifecycle.yml",
            gate_ref: "gates.charter-confirm",
            action: { type: "human" },
          },
        ],
        deferred_issues: { mode: "automatic" },
        scope_disposition: {
          default: { mode: "strict", prompt: "" },
          by_type: {},
        },
      },
    })
    expect(mockedCollectionRequest.mock.calls[1]?.[0]).toBe(
      "/api/development/workflow-configurations/items/automated",
    )
    expect(mockedCollectionRequest.mock.calls[2]?.[0]).toBe(
      "/api/development/workflow-configurations/items/automated/default",
    )

    mockedCollectionRequest.mockResolvedValueOnce({
      deleted_ids: ["automated"],
      failures: [],
      config_revision: "revision-5",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await deletePRLifecycleWorkflowConfiguration("automated", "revision-4")
    expect(
      JSON.parse(String(mockedCollectionRequest.mock.calls[3]?.[1]?.body)),
    ).toEqual({ expected_config_revision: "revision-4" })
  })

  it("fails closed on duplicate workflow configuration IDs", async () => {
    const summary = {
      id: "automated",
      name: "Automated",
      is_default: false,
      bindings: 0,
      deferred_issues: "ask",
    }
    mockedCollectionRequest.mockResolvedValueOnce({
      workflow_configurations: [summary, summary],
      total: 2,
      next_cursor: "",
      canonical_query: "ORDER BY name ASC",
      query_schema: {
        fields: [
          {
            name: "name",
            type: "string",
            operators: ["="],
            sortable: true,
          },
        ],
        default_order: [{ field: "name", direction: "ASC" }],
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })

    await expect(listPRLifecycleWorkflowConfigurations()).rejects.toMatchObject(
      { code: "malformed_response", status: 502 },
    )
  })

  it("accepts backend-boundary prompts, labels, and multiline scope policy", async () => {
    const response = collectionDetailResponse()
    Object.assign(response.workflow_configuration, {
      name: "Automated\nreview",
      bindings: [
        {
          workflow_ref: "workflows/pr-lifecycle.yml",
          gate_ref: "gates.charter-confirm",
          action: {
            type: "ai",
            agent_id: "main",
            prompt: "x".repeat(32 << 10),
            session: "ephemeral",
            history: "none",
            cache: "none",
            tools: "none",
          },
        },
      ],
      scope_disposition: {
        default: {
          mode: "strict",
          prompt: `line one\n${"x".repeat((8 << 10) - 9)}`,
        },
        by_type: {},
      },
    })
    Object.assign(response, {
      gate_catalog: {
        "pr.charter.confirm": {
          workflow_ref: "workflows/pr-lifecycle.yml",
          gate_ref: "gates.charter-confirm",
          source_ai_supported: false,
          prompt: "p".repeat(16 << 10),
          fields: [
            {
              id: "decision",
              type: "select",
              label: "l".repeat(4 << 10),
              required: false,
              max_selections: 1,
              options: [
                {
                  id: "approve",
                  label: "o".repeat(4 << 10),
                },
              ],
            },
          ],
          default_action: { type: "human" },
          effective_action: {
            type: "ai",
            agent_id: "main",
            prompt: "x".repeat(32 << 10),
            session: "ephemeral",
            history: "none",
            cache: "none",
            tools: "none",
          },
          action_source: "config-override",
        },
      },
    })
    mockedCollectionRequest.mockResolvedValueOnce(response)

    const detail = await getPRLifecycleWorkflowConfiguration("automated")

    expect(detail.workflow_configuration.name).toBe("Automated\nreview")
    expect(
      detail.workflow_configuration.scopeDisposition?.default.prompt,
    ).toContain("line one\n")
    expect(
      detail.workflow_configuration.bindings[0]?.action?.prompt,
    ).toHaveLength(32 << 10)
    expect(detail.gate_catalog["pr.charter.confirm"]?.prompt).toHaveLength(
      16 << 10,
    )
    expect(
      detail.gate_catalog["pr.charter.confirm"]?.fields?.[0]?.label,
    ).toHaveLength(4 << 10)
  })

  it("rejects values one byte beyond backend workflow bounds", async () => {
    const response = collectionDetailResponse()
    Object.assign(response.workflow_configuration, {
      bindings: [
        {
          workflow_ref: "workflows/pr-lifecycle.yml",
          gate_ref: "gates.charter-confirm",
          action: {
            type: "ai",
            agent_id: "main",
            prompt: "x".repeat((32 << 10) + 1),
            session: "ephemeral",
            history: "none",
            cache: "none",
            tools: "none",
          },
        },
      ],
    })
    mockedCollectionRequest.mockResolvedValueOnce(response)

    await expect(
      getPRLifecycleWorkflowConfiguration("automated"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })
  })

  it("binds direct and default responses to the requested configuration", async () => {
    const mismatched = collectionDetailResponse()
    mismatched.workflow_configuration.id = "other"
    mockedCollectionRequest.mockResolvedValueOnce(mismatched)
    await expect(
      getPRLifecycleWorkflowConfiguration("automated"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })

    const notDefault = collectionDetailResponse()
    mockedCollectionRequest.mockResolvedValueOnce(notDefault)
    await expect(
      makePRLifecycleWorkflowConfigurationDefault("automated", "revision-1"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })

    mockedCollectionRequest.mockResolvedValueOnce({
      deleted_ids: ["other"],
      failures: [],
      config_revision: "revision-2",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await expect(
      deletePRLifecycleWorkflowConfiguration("automated", "revision-1"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })
  })
})

function collectionDetailResponse() {
  return {
    workflow_configuration: {
      id: "automated",
      name: "Automated",
      is_default: false,
      bindings: [
        {
          workflow_ref: "workflows/pr-lifecycle.yml",
          gate_ref: "gates.charter-confirm",
          action: { type: "human" },
        },
      ],
      deferred_issues: { mode: "automatic" },
      scope_disposition: {
        default: { mode: "strict", prompt: "" },
        by_type: {},
      },
    },
    gate_catalog: {},
    flow: {},
    flow_revision: "flow-1",
    catalog_revision: "catalog-1",
    config_revision: "revision-2",
    effects: {
      gateway_effect: "applied",
      deferred_policy_effect: "applied",
    },
  }
}
