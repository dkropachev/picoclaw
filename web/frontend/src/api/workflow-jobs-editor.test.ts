import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  type WorkflowJobEditorOperation,
  WorkflowJobsEditorAPIError,
  type WorkflowJobsInspection,
  inspectWorkflowJobs,
  renderWorkflowJobs,
} from "@/api/workflows"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const yaml = "name: Review\njobs:\n  review:\n    steps: []\n"

describe("workflow jobs editor API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("strictly inspects ordered jobs and preserves absent, false, and empty values", async () => {
    const signal = new AbortController().signal
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(inspection()))

    const result = await inspectWorkflowJobs(yaml, signal)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/development/jobs/inspect",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ yaml }),
        signal,
      },
    )
    expect(result.jobs[0].fields.continue_on_error).toEqual({
      present: true,
      value: false,
    })
    expect(result.jobs[0].steps[0].fields.name).toEqual({
      present: true,
      value: "",
    })
    expect(result.jobs[0].fields.uses).toEqual({
      present: false,
      value: null,
    })
  })

  it("sends the exact revision and tri-state operation envelope", async () => {
    const operation: WorkflowJobEditorOperation = {
      type: "step.patch",
      job_id: "review",
      step_index: 0,
      fields: {
        name: { mode: "set", value: "" },
        continue_on_error: { mode: "set", value: false },
        if: { mode: "remove" },
      },
    }
    const rendered = {
      ...inspection(),
      yaml: "name: Review\njobs: {}\n",
      revision: "opaque:jobs:rendered",
      validation: {
        valid: false,
        errors: [{ path: "jobs.review.steps", message: "add a step" }],
        validated_at: "2026-07-30T00:00:01Z",
      },
    }
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(rendered))

    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation,
      }),
    ).resolves.toEqual(rendered)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/workflows/development/jobs/render",
      expect.objectContaining({
        body: JSON.stringify({
          yaml,
          revision: "opaque:jobs:base",
          operation,
        }),
      }),
    )
  })

  it("rejects unknown fields, missing field projections, index drift, and absent-value drift", async () => {
    const unknown = inspection()
    Object.assign(unknown.jobs[0], { raw_yaml: "must not cross" })
    const missing = inspection()
    delete (
      missing.jobs[0].fields as Partial<
        WorkflowJobsInspection["jobs"][number]["fields"]
      >
    ).context
    const indexDrift = inspection()
    indexDrift.jobs[0].steps[0].index = 1
    const absentValue = inspection()
    absentValue.jobs[0].fields.uses.value = "agent/main"

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(unknown))
      .mockResolvedValueOnce(jsonResponse(missing))
      .mockResolvedValueOnce(jsonResponse(indexDrift))
      .mockResolvedValueOnce(jsonResponse(absentValue))

    for (let index = 0; index < 4; index += 1) {
      await expect(inspectWorkflowJobs(yaml)).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("rejects unsafe JSON values, duplicate job IDs, and inconsistent completeness", async () => {
    const unsafeNumber = inspection()
    unsafeNumber.jobs[0].steps[0].fields.with.value = {
      retries: Number.MAX_SAFE_INTEGER + 1,
    }
    const duplicate = inspection()
    duplicate.jobs.push({
      ...structuredClone(duplicate.jobs[0]),
      index: 1,
    })
    const inconsistent = inspection()
    inconsistent.complete = false

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(unsafeNumber))
      .mockResolvedValueOnce(jsonResponse(duplicate))
      .mockResolvedValueOnce(jsonResponse(inconsistent))

    for (let index = 0; index < 3; index += 1) {
      await expect(inspectWorkflowJobs(yaml)).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("enforces single-line 256-byte step IDs and dependency references", async () => {
    const oversizedStepID = inspection()
    oversizedStepID.jobs[0].steps[0].fields.id = field("x".repeat(257))
    const multilineDependency = inspection()
    multilineDependency.jobs[0].fields.needs = field(["prepare\npublish"])
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(oversizedStepID))
      .mockResolvedValueOnce(jsonResponse(multilineDependency))

    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")
    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")

    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "step.patch",
          job_id: "x".repeat(257),
          step_index: 0,
          fields: {},
        },
      }),
    ).rejects.toThrow("single-line")
    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "job.patch",
          job_id: " review ",
          fields: {},
        },
      }),
    ).rejects.toThrow("single-line")
    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "job.patch",
          job_id: "review",
          fields: {
            needs: { mode: "set", value: ["prepare\npublish"] },
          },
        },
      }),
    ).rejects.toThrow("single-line")
    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "job.patch",
          job_id: "review",
          fields: {
            needs: { mode: "set", value: ["   "] },
          },
        },
      }),
    ).rejects.toThrow("single-line")
    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: {
          type: "job.patch",
          job_id: "review",
          fields: {
            needs: { mode: "set", value: [" prepare "] },
          },
        },
      }),
    ).rejects.toThrow("single-line")
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(2)
  })

  it("rejects server-invalid mutation strings, targets, and structured values before fetch", async () => {
    const oversized = "x".repeat((16 << 10) + 1)
    const htmlSensitiveValue = "<".repeat(16 << 10)
    const operations: WorkflowJobEditorOperation[] = [
      stepPatch({ name: { mode: "set", value: oversized } }),
      stepPatch({ name: { mode: "set", value: "unsafe\u0001value" } }),
      stepPatch({ name: { mode: "set", value: "unsafe\u200evalue" } }),
      stepPatch({
        uses: { mode: "set", value: "unknown/target" },
      }),
      {
        type: "job.patch",
        job_id: "review",
        fields: {
          uses: { mode: "set", value: "./workflows/child.yml" },
        },
      },
      stepPatch({
        with: { mode: "set", value: { message: "unsafe\u0001value" } },
      }),
      stepPatch({
        with: { mode: "set", value: { ["unsafe\u0001key"]: true } },
      }),
      {
        type: "job.patch",
        job_id: "review",
        fields: {
          outputs: {
            mode: "set",
            value: { ["unsafe\u200ekey"]: "result" },
          },
        },
      },
      {
        type: "job.patch",
        job_id: "review",
        fields: {
          context: {
            mode: "set",
            value: { session: "unsafe\u200evalue" },
          },
        },
      },
      stepPatch({
        with: {
          mode: "set",
          value: {
            messages: [
              htmlSensitiveValue,
              htmlSensitiveValue,
              htmlSensitiveValue,
            ],
          },
        },
      }),
      stepPatch({
        with: {
          mode: "set",
          value: { items: Array<null>(4095).fill(null) },
        },
      }),
    ]

    for (const operation of operations) {
      await expect(
        renderWorkflowJobs({
          yaml,
          revision: "opaque:jobs:base",
          operation,
        }),
      ).rejects.toThrow("cannot be rendered safely")
    }
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("accepts formatting whitespace and canonical targets at the API boundary", async () => {
    const formattingWhitespace = "line one\n\tline two\r\n"
    const operation: WorkflowJobEditorOperation = {
      type: "job.patch",
      job_id: "review",
      fields: {
        name: { mode: "set", value: formattingWhitespace },
        uses: { mode: "set", value: "workflows/child.YAML" },
        with: {
          mode: "set",
          value: { message: formattingWhitespace },
        },
        outputs: {
          mode: "set",
          value: { result: formattingWhitespace },
        },
        context: {
          mode: "set",
          value: { session: formattingWhitespace },
        },
      },
    }
    const rendered = {
      ...inspection(),
      yaml,
      revision: "opaque:jobs:rendered",
    }
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(rendered))

    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation,
      }),
    ).resolves.toEqual(rendered)
    expect(mockedLauncherFetch).toHaveBeenCalledTimes(1)
  })

  it("rejects C1 controls in validation text while allowing formatting whitespace", async () => {
    const nextLine = inspection()
    nextLine.validation = {
      valid: false,
      errors: [{ message: "unsafe\u0085message" }],
      validated_at: "2026-07-30T00:00:00Z",
    }
    const applicationCommand = inspection()
    applicationCommand.validation = {
      valid: false,
      errors: [{ message: "unsafe\u009Fmessage" }],
      validated_at: "2026-07-30T00:00:00Z",
    }
    const formattingWhitespace = inspection()
    formattingWhitespace.validation = {
      valid: false,
      errors: [{ message: "line one\n\tline two\r\n" }],
      validated_at: "2026-07-30T00:00:00Z",
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(nextLine))
      .mockResolvedValueOnce(jsonResponse(applicationCommand))
      .mockResolvedValueOnce(jsonResponse(formattingWhitespace))

    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")
    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")
    await expect(inspectWorkflowJobs(yaml)).resolves.toMatchObject({
      validation: {
        errors: [{ message: "line one\n\tline two\r\n" }],
      },
    })
  })

  it("accepts bounded validation truncation as an incomplete projection", async () => {
    const partial = inspection()
    partial.complete = false
    partial.limits = ["validation_truncated"]
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(partial))

    await expect(inspectWorkflowJobs(yaml)).resolves.toMatchObject({
      complete: false,
      limits: ["validation_truncated"],
      editable: true,
    })
  })

  it("enforces dynamic JSON nodes per value, including root, without sharing budgets", async () => {
    const atLimit = inspection()
    const firstValue = { items: Array<null>(4094).fill(null) }
    const secondValue = { items: Array<null>(4094).fill(null) }
    atLimit.jobs[0].fields.with = field(firstValue)
    atLimit.jobs[0].steps[0].fields.with = field(secondValue)
    const overLimit = inspection()
    overLimit.jobs[0].fields.with = field({
      items: Array<null>(4095).fill(null),
    })
    const emptyKey = inspection()
    emptyKey.jobs[0].fields.with = field({ "": true })

    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse(atLimit))
      .mockResolvedValueOnce(jsonResponse(overLimit))
      .mockResolvedValueOnce(jsonResponse(emptyKey))

    await expect(inspectWorkflowJobs(yaml)).resolves.toMatchObject({
      jobs: [
        {
          fields: { with: { present: true } },
          steps: [{ fields: { with: { present: true } } }],
        },
      ],
    })
    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")
    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow("invalid response")
  })

  it("rejects over-limit source before issuing a request", async () => {
    await expect(
      inspectWorkflowJobs("x".repeat((1 << 20) + 1)),
    ).rejects.toThrow("too large")
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("bounds the exact encoded request body including escapes and operation overhead", async () => {
    const escapedSource = '"'.repeat(600_000)
    expect(new TextEncoder().encode(escapedSource).byteLength).toBeLessThan(
      1 << 20,
    )
    await expect(inspectWorkflowJobs(escapedSource)).rejects.toThrow(
      "too large",
    )

    const nearLimitSource = "x".repeat((1 << 20) - 100)
    await expect(
      renderWorkflowJobs({
        yaml: nearLimitSource,
        revision: "opaque:jobs:base",
        operation: {
          type: "step.patch",
          job_id: "review",
          step_index: 0,
          fields: {
            name: { mode: "set", value: "operation overhead" },
          },
        },
      }),
    ).rejects.toThrow("too large")
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("bounds and content-type checks every success response", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        new Response("{}", {
          headers: {
            "Content-Type": "application/json",
            "Content-Length": String((8 << 20) + 1),
          },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify(inspection()), {
          headers: { "Content-Type": "text/plain" },
        }),
      )
      .mockResolvedValueOnce(chunkedResponseWithoutLength(9, 1 << 20))

    for (let index = 0; index < 3; index += 1) {
      await expect(inspectWorkflowJobs(yaml)).rejects.toThrow(
        "invalid response",
      )
    }
  })

  it("maps stale and raw-only failures without exposing raw server details", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "workflow_jobs_revision_mismatch",
            inspection: inspection(),
          },
          409,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse({ error: "workflow_jobs_raw_only" }, 422),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "open /private/workflows/review.yml: permission denied",
          },
          500,
        ),
      )

    const stale = await renderWorkflowJobs({
      yaml,
      revision: "stale",
      operation: { type: "job.delete", job_id: "review" },
    }).catch((error: unknown) => error)
    expect(stale).toBeInstanceOf(WorkflowJobsEditorAPIError)
    expect(stale).toMatchObject({
      status: 409,
      inspection: expect.objectContaining({ revision: "opaque:jobs:base" }),
    })
    expect((stale as Error).message).toContain("revision is stale")

    await expect(
      renderWorkflowJobs({
        yaml,
        revision: "opaque:jobs:base",
        operation: { type: "job.delete", job_id: "review" },
      }),
    ).rejects.toThrow("must be edited in Workflow YAML")
    await expect(inspectWorkflowJobs(yaml)).rejects.toThrow(
      "jobs and actions editor is unavailable",
    )
  })
})

function stepPatch(
  fields: Extract<WorkflowJobEditorOperation, { type: "step.patch" }>["fields"],
): WorkflowJobEditorOperation {
  return {
    type: "step.patch",
    job_id: "review",
    step_index: 0,
    fields,
  }
}

function inspection(): WorkflowJobsInspection {
  return {
    revision: "opaque:jobs:base",
    editable: true,
    complete: true,
    limits: [],
    jobs: [
      {
        id: "review",
        index: 0,
        editable: true,
        advanced_fields_present: false,
        steps_present: true,
        fields: {
          name: field("Review"),
          runs_on: field("picoclaw"),
          needs: field(["prepare"]),
          uses: absent(),
          if: absent(),
          continue_on_error: field(false),
          with: absent(),
          secrets: absent(),
          outputs: absent(),
          context: absent(),
        },
        steps: [
          {
            index: 0,
            editable: true,
            advanced_fields_present: false,
            fields: {
              id: field("summarize"),
              name: field(""),
              uses: field("agent/main"),
              if: absent(),
              continue_on_error: field(false),
              with: field({
                prompt: "Summarize",
                include_closed: false,
              }),
              context: absent(),
            },
          },
        ],
      },
    ],
    validation: {
      valid: true,
      warnings: [],
      validated_at: "2026-07-30T00:00:00Z",
    },
  }
}

function field<Value>(value: Value) {
  return { present: true, value }
}

function absent() {
  return { present: false, value: null }
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
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
