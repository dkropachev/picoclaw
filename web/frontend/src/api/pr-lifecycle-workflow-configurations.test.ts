import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  getPRLifecycleWorkflowConfigurations,
  putPRLifecycleWorkflowConfigurations,
  validatePRLifecycleWorkflowConfigurations,
} from "@/api/pr-lifecycle-workflow-configurations"
import { requestPRWorkspaceJSON } from "@/api/pr-workspaces"

vi.mock("@/api/pr-lifecycle-flow", () => ({
  projectPRLifecycleFlowCatalog: vi.fn(() => ({
    schema: "pr-lifecycle-flow/v1",
    flows: [],
  })),
}))

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return { ...original, requestPRWorkspaceJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestPRWorkspaceJSON)

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
  beforeEach(() => mockedRequest.mockReset())

  it("projects the kebab-case wire contract into idiomatic client fields", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)

    const snapshot = await getPRLifecycleWorkflowConfigurations()

    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/pr-lifecycle/workflow-configurations",
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
})
