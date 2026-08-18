import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  getPRLifecycleGateConfigs,
  putPRLifecycleGateConfigs,
  validatePRLifecycleGateConfigs,
} from "@/api/pr-lifecycle-gate-configs"
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
  "gate-configs": {
    default: {
      name: "Default",
      bindings: [],
    },
    automated: {
      name: "Automated",
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
  "default-gate-config": "default",
  "repository-assignments": {
    "https://github.com|100": "automated",
  },
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
  "deferred-issues": { mode: "ask" },
  "gate-catalog": {
    "pr.charter.reconfirm": {
      "workflow-ref": "workflows/pr-lifecycle.yml",
      "gate-ref": "gates.charter-reconfirm",
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
  effects: { "gateway-effect": "applied" },
}

describe("PR lifecycle Gate configurations", () => {
  beforeEach(() => mockedRequest.mockReset())

  it("projects the kebab-case wire contract into idiomatic client fields", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)

    const snapshot = await getPRLifecycleGateConfigs()

    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/pr-lifecycle/gate-configs",
      undefined,
      undefined,
    )
    expect(snapshot.defaultGateConfig).toBe("default")
    expect(snapshot.nudge.reviewMinimumAdditional).toBe(1)
    expect(snapshot.scope.xs.semanticLines).toBe(20)
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
    expect(snapshot.gateConfigs.automated.bindings[0]).toEqual({
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
    const snapshot = await getPRLifecycleGateConfigs()
    mockedRequest.mockResolvedValueOnce(wireSnapshot)

    await putPRLifecycleGateConfigs({
      expectedConfigRevision: snapshot.configRevision,
      requestID: "request-1",
      gateConfigs: snapshot.gateConfigs,
      defaultGateConfig: snapshot.defaultGateConfig,
      repositoryAssignments: snapshot.repositoryAssignments,
      nudge: snapshot.nudge,
      scope: snapshot.scope,
      deferredIssues: snapshot.deferredIssues,
    })

    const request = mockedRequest.mock.calls.find(
      ([, init]) => init?.method === "PUT",
    )?.[1]
    const body = JSON.parse(String(request?.body)) as Record<string, unknown>
    expect(body).toMatchObject({
      "expected-config-revision": "config-1",
      "request-id": "request-1",
      "default-gate-config": "default",
    })
    expect(JSON.stringify(body)).not.toContain("_")
  })

  it("rejects invalid local references and incomplete atomic overrides", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)
    const snapshot = await getPRLifecycleGateConfigs()
    snapshot.gateConfigs.automated.bindings = [
      {
        workflowRef: "workflows/pr-lifecycle",
        gateRef: "charter-decision",
        action: { type: "workflow", workflowRef: "" },
      },
    ]

    expect(validatePRLifecycleGateConfigs(snapshot)).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ message: expect.stringContaining("gates.") }),
        expect.objectContaining({
          message: expect.stringContaining("workflow reference"),
        }),
      ]),
    )
  })

  it("rejects AI policy values outside the closed runtime vocabulary", async () => {
    const invalid = structuredClone(wireSnapshot)
    invalid["gate-configs"].automated.bindings[0].action.session = "ambient"
    mockedRequest.mockResolvedValue(invalid)

    await expect(getPRLifecycleGateConfigs()).rejects.toMatchObject({
      code: "malformed_response",
    })
  })
})
