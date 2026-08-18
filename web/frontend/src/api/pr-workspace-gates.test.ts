import { beforeEach, describe, expect, it, vi } from "vitest"

import { respondPRWorkspaceGate } from "@/api/pr-workspace-gates"
import { requestPRWorkspaceAggregate } from "@/api/pr-workspaces"

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return { ...original, requestPRWorkspaceAggregate: vi.fn() }
})

const mockedRequest = vi.mocked(requestPRWorkspaceAggregate)

describe("PR workspace Gate response", () => {
  beforeEach(() => mockedRequest.mockReset())

  it("submits only the mutation fence and kebab-case field-values", async () => {
    mockedRequest.mockResolvedValue({} as never)

    await respondPRWorkspaceGate("prw-example", "gate-execution", {
      expected_version: 7,
      request_id: "request-1",
      fieldValues: {
        action: "revise",
        "affected-areas": ["implementation", "tests"],
      },
    })

    const [path, init] = mockedRequest.mock.calls[0]
    expect(path).toBe(
      "/api/pr-workspaces/prw-example/gates/gate-execution/respond",
    )
    expect(JSON.parse(String(init?.body))).toEqual({
      expected_version: 7,
      request_id: "request-1",
      "field-values": {
        action: "revise",
        "affected-areas": ["implementation", "tests"],
      },
    })
  })
})
