import { beforeEach, describe, expect, it, vi } from "vitest"

import { collectionRequest } from "@/api/collection"
import { requestDevelopmentJSON } from "@/api/development-workspaces"
import {
  bulkDeletePRLifecycleRepositoryAssignments,
  canonicalPRLifecycleRepositoryIdentity,
  createPRLifecycleRepositoryAssignment,
  deletePRLifecycleRepositoryAssignment,
  getPRLifecycleRepositoryAssignment,
  getPRLifecycleRepositoryAssignments,
  listPRLifecycleRepositoryAssignments,
  putPRLifecycleRepositoryAssignments,
  updatePRLifecycleRepositoryAssignment,
  validatePRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"

vi.mock("@/api/collection", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/collection")>()
  return { ...original, collectionRequest: vi.fn() }
})

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return { ...original, requestDevelopmentJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestDevelopmentJSON)
const mockedCollectionRequest = vi.mocked(collectionRequest)
const assignmentID = "Rjljc2epaibQOt_BhFLZFLSNQFrkJGFxU2BnbKKqal8"

const wireSnapshot = {
  repositories: {
    "https://github.com|100": {
      name: "owner/existing",
      "default-branch": "main",
    },
  },
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
  beforeEach(() => {
    mockedRequest.mockReset()
    mockedCollectionRequest.mockReset()
  })

  it("projects a self-contained policy snapshot", async () => {
    mockedRequest.mockResolvedValue(wireSnapshot)

    const snapshot = await getPRLifecycleRepositoryAssignments()

    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/development/repositories",
      undefined,
      undefined,
    )
    expect(snapshot.defaultWorkflowConfiguration).toBe("default")
    expect(snapshot.repositoryAssignments).toEqual({
      "https://github.com|100": "strict",
    })
    expect(snapshot.repositories).toEqual({
      "https://github.com|100": {
        name: "owner/existing",
        defaultBranch: "main",
      },
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
      repositories: snapshot.repositories,
    })

    const request = mockedRequest.mock.calls.at(-1)?.[1]
    const body = JSON.parse(String(request?.body))
    expect(body).toEqual({
      "expected-config-revision": "config-1",
      "request-id": "request-1",
      repositories: {
        "https://github.com|100": {
          name: "owner/existing",
          "default-branch": "main",
        },
      },
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

  it("projects the paged collection and direct safe identity", async () => {
    mockedCollectionRequest.mockResolvedValueOnce({
      repository_assignments: [
        {
          id: assignmentID,
          repository: "owner/repository",
          configuration: "strict",
          default_branch: "",
        },
      ],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: {
        fields: [
          {
            name: "repository",
            type: "string",
            operators: ["=", "~"],
            sortable: true,
          },
        ],
        default_order: [{ field: "repository", direction: "ASC" }],
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })

    const page = await listPRLifecycleRepositoryAssignments({
      query: "repository ~ repo",
      limit: 25,
    })

    expect(mockedCollectionRequest).toHaveBeenCalledWith(
      "/api/development/repository-assignments?query=repository+%7E+repo&limit=25",
      undefined,
      undefined,
    )
    expect(page.repository_assignments[0]).toMatchObject({
      id: assignmentID,
      repository: "owner/repository",
      default_branch: "",
    })
    expect(page.next_cursor).toBeUndefined()

    mockedCollectionRequest.mockResolvedValueOnce({
      repository_assignment: {
        ...page.repository_assignments[0],
        provider_origin: "https://github.com",
        repository_id: "100",
      },
      workflow_configurations: {
        strict: {
          name: "Strict",
          deferred_issues: { mode: "automatic" },
        },
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })

    const detail = await getPRLifecycleRepositoryAssignment(assignmentID)
    expect(detail.repository_assignment.repository_id).toBe("100")
    expect(detail.workflow_configurations.strict.name).toBe("Strict")
  })

  it("sends item and bulk mutations with one config revision fence", async () => {
    const detailResponse = {
      repository_assignment: {
        id: assignmentID,
        repository: "owner/repository",
        configuration: "strict",
        default_branch: "main",
        provider_origin: "https://github.com",
        repository_id: "100",
      },
      workflow_configurations: {
        strict: {
          name: "Strict",
          deferred_issues: { mode: "ask" },
        },
      },
      config_revision: "revision-2",
      effects: {
        gateway_effect: "restart_required",
        deferred_policy_effect: "applied",
      },
    }
    const input = {
      provider_origin: "https://github.com",
      repository_id: "100",
      repository: "owner/repository",
      configuration: "strict",
      default_branch: "main",
    }
    mockedCollectionRequest.mockResolvedValue(detailResponse)

    await createPRLifecycleRepositoryAssignment(input, "revision-1")
    await updatePRLifecycleRepositoryAssignment(
      assignmentID,
      input,
      "revision-2",
    )

    expect(
      JSON.parse(String(mockedCollectionRequest.mock.calls[0]?.[1]?.body)),
    ).toEqual({
      expected_config_revision: "revision-1",
      repository_assignment: input,
    })
    expect(mockedCollectionRequest.mock.calls[1]?.[0]).toBe(
      `/api/development/repository-assignments/${assignmentID}`,
    )

    mockedCollectionRequest.mockResolvedValueOnce({
      deleted_ids: [assignmentID],
      failures: [],
      config_revision: "revision-3",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await bulkDeletePRLifecycleRepositoryAssignments(
      [assignmentID],
      "revision-2",
    )
    expect(
      JSON.parse(String(mockedCollectionRequest.mock.calls[2]?.[1]?.body)),
    ).toEqual({
      expected_config_revision: "revision-2",
      ids: [assignmentID],
    })
  })

  it("fails closed on duplicate or unsafe collection identities", async () => {
    const summary = {
      id: "unsafe/id",
      repository: "owner/repository",
      configuration: "strict",
      default_branch: "main",
    }
    mockedCollectionRequest.mockResolvedValueOnce({
      repository_assignments: [summary],
      total: 1,
      next_cursor: "",
      canonical_query: "ORDER BY repository ASC",
      query_schema: {
        fields: [
          {
            name: "repository",
            type: "string",
            operators: ["="],
            sortable: true,
          },
        ],
        default_order: [{ field: "repository", direction: "ASC" }],
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })

    await expect(listPRLifecycleRepositoryAssignments()).rejects.toMatchObject({
      code: "malformed_response",
      status: 502,
    })
  })

  it("binds direct and mutation responses to requested assignment IDs", async () => {
    mockedCollectionRequest.mockResolvedValueOnce({
      repository_assignment: {
        id: "A".repeat(43),
        repository: "owner/repository",
        configuration: "strict",
        default_branch: "",
        provider_origin: "https://github.com",
        repository_id: "100",
      },
      workflow_configurations: {
        strict: {
          name: "Strict",
          deferred_issues: { mode: "ask" },
        },
      },
      config_revision: "revision-1",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await expect(
      getPRLifecycleRepositoryAssignment(assignmentID),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })

    mockedCollectionRequest.mockResolvedValueOnce({
      deleted_ids: [],
      failures: [{ id: assignmentID, code: "referenced" }],
      config_revision: "revision-2",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await expect(
      deletePRLifecycleRepositoryAssignment(assignmentID, "revision-1"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })

    mockedCollectionRequest.mockResolvedValueOnce({
      deleted_ids: ["B".repeat(43)],
      failures: [],
      config_revision: "revision-2",
      effects: {
        gateway_effect: "applied",
        deferred_policy_effect: "applied",
      },
    })
    await expect(
      bulkDeletePRLifecycleRepositoryAssignments([assignmentID], "revision-1"),
    ).rejects.toMatchObject({ code: "malformed_response", status: 502 })
  })
})
