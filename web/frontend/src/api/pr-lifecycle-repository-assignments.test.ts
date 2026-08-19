import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  canonicalPRLifecycleRepositoryIdentity,
  getPRLifecycleRepositoryAssignments,
  putPRLifecycleRepositoryAssignments,
  validatePRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"
import { requestPRWorkspaceJSON } from "@/api/pr-workspaces"

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return { ...original, requestPRWorkspaceJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestPRWorkspaceJSON)

const wireSnapshot = {
  "workflow-configurations": {
    default: {
      name: "Default",
      "deferred-issues": { mode: "ask" },
    },
    strict: {
      name: "Strict",
      "deferred-issues": { mode: "automatic" },
    },
  },
  "default-workflow-configuration": "default",
  "repository-assignments": {
    "https://github.com|100": "strict",
  },
  "config-revision": "config-1",
  effects: {
    "gateway-effect": "applied",
    "deferred-policy-effect": "applied",
  },
}

describe("PR lifecycle repository assignments", () => {
  beforeEach(() => mockedRequest.mockReset())

  it("projects a self-contained policy snapshot", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)

    const snapshot = await getPRLifecycleRepositoryAssignments()

    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/pr-lifecycle/repository-assignments",
      undefined,
      undefined,
    )
    expect(snapshot.defaultWorkflowConfiguration).toBe("default")
    expect(snapshot.repositoryAssignments).toEqual({
      "https://github.com|100": "strict",
    })
    expect(snapshot.workflowConfigurations.strict).toEqual({
      name: "Strict",
      deferredIssues: { mode: "automatic" },
    })
  })

  it("writes only the assignment-owned revision-fenced fields", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)
    const snapshot = await getPRLifecycleRepositoryAssignments()
    mockedRequest.mockResolvedValueOnce(wireSnapshot)

    await putPRLifecycleRepositoryAssignments({
      expectedConfigRevision: snapshot.configRevision,
      requestID: "request-1",
      repositoryAssignments: snapshot.repositoryAssignments,
    })

    const request = mockedRequest.mock.calls.at(-1)?.[1]
    const body = JSON.parse(String(request?.body))
    expect(body).toEqual({
      "expected-config-revision": "config-1",
      "request-id": "request-1",
      "repository-assignments": {
        "https://github.com|100": "strict",
      },
    })
    expect(JSON.stringify(body)).not.toContain("_")
    expect(body).not.toHaveProperty("workflow-configurations")
    expect(body).not.toHaveProperty("default-workflow-configuration")
  })

  it("fails closed for unknown assignments and non-summary configuration data", async () => {
    const unknown = structuredClone(wireSnapshot)
    unknown["repository-assignments"]["https://github.com|100"] = "missing"
    mockedRequest.mockResolvedValueOnce(unknown)
    await expect(getPRLifecycleRepositoryAssignments()).rejects.toMatchObject({
      code: "malformed_response",
    })

    const expanded = structuredClone(wireSnapshot)
    Object.assign(
      expanded["workflow-configurations"].strict as unknown as Record<
        string,
        unknown
      >,
      { bindings: [] },
    )
    mockedRequest.mockResolvedValueOnce(expanded)
    await expect(getPRLifecycleRepositoryAssignments()).rejects.toMatchObject({
      code: "malformed_response",
    })
  })

  it("matches runtime identity normalization and reports invalid collisions", () => {
    expect(
      canonicalPRLifecycleRepositoryIdentity("HTTPS://GitHub.com///|Repo-1"),
    ).toBe("https://github.com|repo-1")
    expect(
      canonicalPRLifecycleRepositoryIdentity(
        " https://github.com|repository-id",
      ),
    ).toBeUndefined()
    expect(
      canonicalPRLifecycleRepositoryIdentity(
        `https://github.com|${"x".repeat(1025)}`,
      ),
    ).toBeUndefined()

    expect(
      validatePRLifecycleRepositoryAssignments({
        workflowConfigurations: {
          default: {
            name: "Default",
            deferredIssues: { mode: "ask" },
          },
        },
        repositoryAssignments: {
          "https://github.com/|Repo-1": "default",
          "HTTPS://GITHUB.COM|repo-1": "default",
          "http://github.com|repo-2": "default",
        },
      }),
    ).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          message: expect.stringContaining("collides"),
        }),
        expect.objectContaining({
          message: expect.stringContaining("https://"),
        }),
      ]),
    )
  })
})
