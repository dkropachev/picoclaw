import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  type WorkflowAuthoringCapabilities,
  getWorkflowAuthoringCapabilities,
} from "@/api/workflows"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("workflow authoring capabilities API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("accepts only the bounded structured capability projection", async () => {
    const signal = new AbortController().signal
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(capabilities()))

    await expect(getWorkflowAuthoringCapabilities(signal)).resolves.toEqual(
      capabilities(),
    )
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/authoring/capabilities",
      { signal },
    )
  })

  it("rejects unknown fields, identity drift, hidden controls, and unsorted duplicates", async () => {
    const unknownField = capabilities()
    Object.assign(unknownField.tools[0], { description: "must not cross" })
    const targetDrift = capabilities()
    targetDrift.agents[0].target = "agent/other"
    const hiddenIdentity = capabilities()
    hiddenIdentity.tools[0].name = "message\u202e"
    hiddenIdentity.tools[0].target = "tool/message\u202e"
    const nestedMCPTool = capabilities()
    nestedMCPTool.mcp_tools[0].tool = "issues/create"
    nestedMCPTool.mcp_tools[0].target = "mcp/github/issues/create"
    const duplicate = capabilities()
    duplicate.functions[1] = { ...duplicate.functions[0] }

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(unknownField))
      .mockResolvedValueOnce(jsonResponse(targetDrift))
      .mockResolvedValueOnce(jsonResponse(hiddenIdentity))
      .mockResolvedValueOnce(jsonResponse(nestedMCPTool))
      .mockResolvedValueOnce(jsonResponse(duplicate))

    for (let index = 0; index < 5; index += 1) {
      await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("rejects agent IDs that workflow execution would normalize", async () => {
    const invalidIDs = [
      "ReviewAgent",
      "review/agent",
      "review agent",
      "réviseur",
      `a${"b".repeat(64)}`,
    ]
    for (const id of invalidIDs) {
      const catalog = capabilities()
      catalog.agents[1].id = id
      catalog.agents[1].target = `agent/${id}`
      mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(catalog))
    }

    for (let index = 0; index < invalidIDs.length; index += 1) {
      await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("enforces complete, MCP, default-agent, shape-omission, and limit invariants", async () => {
    const noDefault = capabilities()
    noDefault.agents[0].is_default = false
    const falseComplete = capabilities()
    falseComplete.complete = false
    const unavailableComplete = capabilities()
    unavailableComplete.mcp_status = "unavailable"
    unavailableComplete.mcp_tools = []
    const disabledWithEntries = capabilities()
    disabledWithEntries.mcp_status = "disabled"
    const omittedWithoutLimit = capabilities()
    omittedWithoutLimit.tools[0].parameter_shape_projected = false
    delete omittedWithoutLimit.tools[0].parameter_shape
    const validPartial = capabilities()
    validPartial.complete = false
    validPartial.tools[0].parameter_shape_projected = false
    delete validPartial.tools[0].parameter_shape
    validPartial.limits = ["parameter_shapes_omitted"]

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(noDefault))
      .mockResolvedValueOnce(jsonResponse(falseComplete))
      .mockResolvedValueOnce(jsonResponse(unavailableComplete))
      .mockResolvedValueOnce(jsonResponse(disabledWithEntries))
      .mockResolvedValueOnce(jsonResponse(omittedWithoutLimit))
      .mockResolvedValueOnce(jsonResponse(validPartial))

    for (let index = 0; index < 5; index += 1) {
      await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
        "invalid response",
      )
    }
    await expect(getWorkflowAuthoringCapabilities()).resolves.toMatchObject({
      complete: false,
      limits: ["parameter_shapes_omitted"],
      tools: [{ parameter_shape_projected: false }],
    })
  })

  it("enforces typed recursive parameter shapes and browser-safe scalar values", async () => {
    const nullShape = capabilities()
    nullShape.tools[0].parameter_shape = null as never
    const missingRequiredFlag = capabilities()
    const property =
      missingRequiredFlag.tools[0].parameter_shape?.properties?.[0]
    if (property != null) {
      delete (property as Partial<typeof property>).required
    }
    const ambiguousAdditionalProperties = capabilities()
    const shape = ambiguousAdditionalProperties.tools[0].parameter_shape
    if (shape != null) {
      shape.additional_properties = {
        allowed: true,
        shape: {},
      } as never
    }
    const leakyNestedShape = capabilities()
    const nestedShape =
      leakyNestedShape.tools[0].parameter_shape?.properties?.[0]?.shape
    if (nestedShape != null) {
      Object.assign(nestedShape, { default: "must-not-cross" })
    }
    const unsafeInteger = capabilities()
    const unsafeShape = unsafeInteger.tools[0].parameter_shape
    if (unsafeShape != null) {
      unsafeShape.enum = [Number.MAX_SAFE_INTEGER + 1]
    }
    const tooDeep = capabilities()
    let nested = tooDeep.tools[0].parameter_shape
    for (let depth = 0; depth < 6 && nested != null; depth += 1) {
      nested.items = {}
      nested = nested.items
    }

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(nullShape))
      .mockResolvedValueOnce(jsonResponse(missingRequiredFlag))
      .mockResolvedValueOnce(jsonResponse(leakyNestedShape))
      .mockResolvedValueOnce(jsonResponse(ambiguousAdditionalProperties))
      .mockResolvedValueOnce(jsonResponse(unsafeInteger))
      .mockResolvedValueOnce(jsonResponse(tooDeep))

    for (let index = 0; index < 6; index += 1) {
      await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("rejects unsorted and duplicate omission codes", async () => {
    const unsorted = capabilities()
    unsorted.complete = false
    unsorted.limits = ["functions_truncated", "agents_truncated"]
    const duplicate = capabilities()
    duplicate.complete = false
    duplicate.limits = ["unsafe_fields_omitted", "unsafe_fields_omitted"]
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(unsorted))
      .mockResolvedValueOnce(jsonResponse(duplicate))

    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "invalid response",
    )
    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "invalid response",
    )
  })

  it("caps the streamed response at four MiB and maps failures without raw details", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        new Response("{}", {
          headers: { "Content-Length": String((4 << 20) + 1) },
        }),
      )
      .mockResolvedValueOnce(chunkedResponseWithoutLength(5, 1 << 20))
      .mockResolvedValueOnce(
        new Response(JSON.stringify(capabilities()), {
          headers: { "Content-Type": "text/plain" },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          { error: "workflow_authoring_capabilities_unavailable" },
          503,
        ),
      )
      .mockResolvedValueOnce(
        new Response("open /private/config.json: permission denied", {
          status: 500,
        }),
      )

    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "invalid response",
    )
    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "invalid response",
    )
    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "invalid response",
    )
    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "temporarily unavailable",
    )
    await expect(getWorkflowAuthoringCapabilities()).rejects.toThrow(
      "Workflow capabilities are unavailable.",
    )
  })
})

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function chunkedResponseWithoutLength(chunkCount: number, chunkBytes: number) {
  let remaining = chunkCount
  return new Response(
    new ReadableStream<Uint8Array>({
      pull(controller) {
        if (remaining === 0) {
          controller.close()
          return
        }
        remaining -= 1
        controller.enqueue(new Uint8Array(chunkBytes))
      },
    }),
    {
      headers: { "Content-Type": "application/json" },
    },
  )
}

function capabilities(): WorkflowAuthoringCapabilities {
  return {
    complete: true,
    mcp_status: "ready" as const,
    agents: [
      {
        id: "main",
        target: "agent/main",
        is_default: true,
        readiness: "ready" as const,
      },
      {
        id: "reviewer",
        target: "agent/reviewer",
        is_default: false,
        readiness: "not_configured" as const,
      },
    ],
    tools: [
      {
        name: "message",
        target: "tool/message",
        readiness: "ready" as const,
        parameter_shape_projected: true,
        parameter_shape: {
          type: "object" as const,
          properties: [
            {
              name: "channel",
              required: false,
              shape: { type: "string" as const },
            },
            {
              name: "text",
              required: true,
              shape: {
                type: "string" as const,
                enum: ["brief", "full"],
              },
            },
          ],
          additional_properties: { allowed: false },
        },
      },
    ],
    mcp_tools: [
      {
        server: "github",
        tool: "create_issue",
        target: "mcp/github/create_issue",
        readiness: "ready" as const,
        parameter_shape_projected: true,
        parameter_shape: {
          type: "object" as const,
          additional_properties: {
            shape: { type: "string" as const },
          },
        },
      },
    ],
    functions: [
      {
        name: "git.filter",
        target: "function/git.filter",
        readiness: "ready" as const,
      },
      {
        name: "git.inventory",
        target: "function/git.inventory",
        readiness: "ready" as const,
      },
      {
        name: "workflow.artifact",
        target: "function/workflow.artifact",
        readiness: "ready" as const,
      },
      {
        name: "workflow.state",
        target: "function/workflow.state",
        readiness: "ready" as const,
      },
    ],
    limits: [],
  }
}
