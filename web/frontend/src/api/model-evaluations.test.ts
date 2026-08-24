import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"

import {
  createModelEvaluation,
  deleteModelEvaluation,
  getModelEvaluation,
  getModelEvaluationCorpus,
  getModelEvaluationOptions,
  listModelEvaluations,
  runModelEvaluation,
  runModelEvaluationAction,
  updateModelEvaluation,
} from "./model-evaluations"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedFetch = vi.mocked(launcherFetch)

function response(body: unknown, status = 200): Response {
  return new Response(status === 204 ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("model evaluation API", () => {
  beforeEach(() => mockedFetch.mockReset())

  it("reads list, detail, corpus, and options", async () => {
    mockedFetch
      .mockResolvedValueOnce(response({ evaluations: [{ id: "rme_a" }] }))
      .mockResolvedValueOnce(response({ evaluation: { id: "rme_a" } }))
      .mockResolvedValueOnce(response({ files: [], total: 0 }))
      .mockResolvedValueOnce(
        response({
          models: [],
          max_files_per_language: 20,
          default_files_per_language: 20,
          max_candidate_models: 8,
        }),
      )

    expect(await listModelEvaluations()).toHaveLength(1)
    expect(await getModelEvaluation("rme_a")).toMatchObject({ id: "rme_a" })
    expect(await getModelEvaluationCorpus("rme_a", 2, 5)).toEqual({
      files: [],
      total: 0,
      offset: 0,
      language_counts: {},
    })
    expect(await getModelEvaluationOptions()).toMatchObject({
      max_files_per_language: 20,
    })
    expect(mockedFetch.mock.calls[2]?.[0]).toContain("offset=2&limit=5")
  })

  it("starts one-shot probes and preserves legacy version-fenced actions", async () => {
    const evaluation = { id: "rme_a", version: 2 }
    mockedFetch
      .mockResolvedValueOnce(response({ evaluation }, 201))
      .mockResolvedValueOnce(response({ evaluation }, 202))
      .mockResolvedValueOnce(response({ evaluation }))
    for (let index = 0; index < 6; index += 1) {
      mockedFetch.mockResolvedValueOnce(response({ evaluation }, 202))
    }
    mockedFetch.mockResolvedValueOnce(response(undefined, 204))
    const input = {
      repository: "owner/repo",
      candidate_models: ["code", "fast"],
      selector_model_alias: "review",
      judge_model_alias: "review",
    }
    expect(await createModelEvaluation(input)).toMatchObject(evaluation)
    expect(await runModelEvaluation(input)).toMatchObject(evaluation)
    expect(
      await updateModelEvaluation("rme_a", {
        ...input,
        expected_version: 1,
      }),
    ).toMatchObject(evaluation)
    for (const action of [
      "preflight",
      "start",
      "run",
      "cancel",
      "resume",
      "restart",
    ] as const) {
      await expect(
        runModelEvaluationAction("rme_a", action, 2),
      ).resolves.toMatchObject(evaluation)
    }
    await deleteModelEvaluation("rme_a", 2)
    expect(mockedFetch.mock.calls[1]?.[0]).toBe("/api/model-evaluations/run")
    expect(JSON.parse(String(mockedFetch.mock.calls[1]?.[1]?.body))).toEqual(
      input,
    )
    expect(JSON.parse(String(mockedFetch.mock.calls[3]?.[1]?.body))).toEqual({
      expected_version: 2,
    })
    expect(mockedFetch.mock.calls.slice(3, 9).map(([path]) => path)).toEqual([
      "/api/model-evaluations/rme_a/preflight",
      "/api/model-evaluations/rme_a/start",
      "/api/model-evaluations/rme_a/run",
      "/api/model-evaluations/rme_a/cancel",
      "/api/model-evaluations/rme_a/resume",
      "/api/model-evaluations/rme_a/restart",
    ])
  })

  it("surfaces structured bounded API failures", async () => {
    mockedFetch.mockImplementation(() =>
      Promise.resolve(
        response(
          {
            code: "stale_repository_model_evaluation",
            message: "stale evaluation",
          },
          409,
        ),
      ),
    )
    await expect(getModelEvaluation("rme_stale")).rejects.toThrow(
      "stale evaluation",
    )
    await expect(getModelEvaluation("rme_stale")).rejects.toMatchObject({
      status: 409,
      code: "stale_repository_model_evaluation",
    })
  })

  it("normalizes omitted bounded collections and option defaults", async () => {
    mockedFetch
      .mockResolvedValueOnce(
        response({
          evaluations: [
            {
              id: "rme_a",
              candidate_models: null,
              progress: null,
              usage: null,
              warnings: null,
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        response({
          evaluation: {
            id: "rme_a",
            focus: null,
            candidate_models: null,
            progress: { percent: 150 },
            usage: null,
            comparisons: [
              {
                model_alias: "code",
                concrete_models: null,
                scores: null,
                languages: null,
                regions: null,
                usage: null,
              },
            ],
            warnings: null,
            run_ids: null,
          },
        }),
      )
      .mockResolvedValueOnce(response({ models: null }))

    await expect(listModelEvaluations()).resolves.toMatchObject([
      {
        candidate_models: [],
        progress: { stage: "idle", percent: 0, languages: {} },
        usage: { input_tokens: 0, output_tokens: 0 },
        warnings: [],
      },
    ])
    await expect(getModelEvaluation("rme_a")).resolves.toMatchObject({
      candidate_models: [],
      focus: {
        code_types: [],
        include_folders: [],
        exclude_folders: [],
      },
      progress: { percent: 100 },
      comparisons: [
        {
          concrete_models: {},
          scores: {},
          languages: [],
          regions: [],
          usage: { requests: 0 },
        },
      ],
      warnings: [],
      run_ids: [],
    })
    await expect(getModelEvaluationOptions()).resolves.toMatchObject({
      models: [],
      repositories: [],
      code_types: ["hotpath-code", "code", "test", "bench-test"],
      max_files_per_language: 20,
      default_files_per_language: 20,
      max_candidate_models: 8,
    })
  })
})
