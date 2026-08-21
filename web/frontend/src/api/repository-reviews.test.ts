import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  createRepositoryReviewAutomation,
  createRepositoryReviewIssueDraft,
  deleteRepositoryReviewAutomation,
  getRepositoryReview,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewAutomations,
  listRepositoryReviews,
  pauseRepositoryReviewAutomation,
  publishRepositoryReviewIssueDraft,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
  updateRepositoryReviewAutomation,
  updateRepositoryReviewFinding,
  updateRepositoryReviewIssueDraft,
} from "@/api/repository-reviews"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("repository review API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("lists and loads repository review state", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ repositories: [] }))
      .mockResolvedValueOnce(
        jsonResponse({ id: "rrp_repo", repository: "owner/repo" }),
      )

    await expect(listRepositoryReviews()).resolves.toEqual({ repositories: [] })
    await expect(getRepositoryReview("rrp_repo/slash")).resolves.toMatchObject({
      repository: "owner/repo",
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/repository-reviews",
      { signal: undefined },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/repository-reviews/rrp_repo%2Fslash",
      { signal: undefined },
    )
  })

  it("requests a bounded finding page", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        id: "rrp_repo",
        repository: "owner/repo",
        finding_offset: 50,
        finding_total: 75,
      }),
    )

    await getRepositoryReview("rrp_repo", undefined, {
      offset: 50,
      limit: 25,
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/repository-reviews/rrp_repo?offset=50&limit=25",
      { signal: undefined },
    )
  })

  it("sends version-fenced finding and issue-draft mutations", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          repository: { id: "rrp_repo", review_version: 1 },
          finding: {
            id: "rfn_1",
            context_ids: [],
            models: [],
            validation: { status: "confirmed", summary: "confirmed" },
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          repository: { id: "rrp_repo" },
          draft: { id: "rid_1" },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          repository: { id: "rrp_repo" },
          draft: { id: "rid_1" },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          repository: { id: "rrp_repo" },
          draft: { id: "rid_1" },
        }),
      )

    await updateRepositoryReviewFinding("rrp_repo", "rfn/1", {
      status: "dismissed",
      expected_version: 4,
    })
    await createRepositoryReviewIssueDraft("rrp_repo", {
      finding_ids: ["rfn_1", "rfn_2"],
      expected_version: 5,
    })
    await updateRepositoryReviewIssueDraft("rrp_repo", "rid/1", {
      title: "Lost update",
      body: "The write needs a version fence.",
      labels: ["bug", "concurrency"],
      expected_version: 2,
    })
    await publishRepositoryReviewIssueDraft("rrp_repo", "rid/1", {
      expected_version: 3,
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/repository-reviews/rrp_repo/findings/rfn%2F1",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({
          status: "dismissed",
          expected_version: 4,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/repository-reviews/rrp_repo/issue-drafts",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          finding_ids: ["rfn_1", "rfn_2"],
          expected_version: 5,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/repository-reviews/rrp_repo/issue-drafts/rid%2F1",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({
          title: "Lost update",
          body: "The write needs a version fence.",
          labels: ["bug", "concurrency"],
          expected_version: 2,
        }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      4,
      "/api/repository-reviews/rrp_repo/issue-drafts/rid%2F1/publish",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ expected_version: 3 }),
      }),
    )
  })

  it("surfaces structured API errors", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        { code: "repository_review_conflict", message: "Review changed." },
        409,
      ),
    )

    await expect(getRepositoryReview("rrp_repo")).rejects.toMatchObject({
      status: 409,
      code: "repository_review_conflict",
      message: "Review changed.",
    })
  })

  it("normalizes nullable stored collections", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        id: "rrp_repo",
        repository: "owner/repo",
        files: null,
        findings: null,
        contexts: null,
        runs: null,
        issue_drafts: null,
      }),
    )

    await expect(getRepositoryReview("rrp_repo")).resolves.toMatchObject({
      files: {},
      findings: [],
      contexts: [],
      runs: [],
      issue_drafts: [],
    })
  })

  it("loads and normalizes automation options and model-stat maps", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          models: [
            {
              alias: "fast",
              resolved_model: "provider/fast",
              provider: "provider",
              available: true,
              price_known: true,
              input_price_per_1m: 0.2,
              output_price_per_1m: 0.8,
            },
            {
              alias: "blocked",
              resolved_model: "agentic-cli/model",
              provider: "agentic-cli",
              available: false,
              blocked_reason: "Agentic CLI models cannot run as reviewers.",
              price_known: false,
            },
          ],
          limits_error: "account telemetry offline",
          accounts: [
            {
              id: "acct",
              entries: [
                {
                  name: "Weekly",
                  window: "weekly",
                  remaining_percent: 75,
                  refreshes_at: "2026-08-21T12:00:00Z",
                },
              ],
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          automations: [
            {
              id: "auto_1",
              reviewer_models: null,
              run_ids: null,
              model_prices: null,
              budget: null,
              usage: null,
              progress: null,
              model_stats: {
                fast: {
                  tokens: {
                    prompt_tokens: 75,
                    completion_tokens: 25,
                    total_tokens: 100,
                  },
                  estimated_cost_usd: 0.01,
                  latency_millis: 250,
                },
              },
              account_limits: [
                {
                  account_id: "acct",
                  name: "Premium",
                  window: "weekly",
                  remaining_percent: 75,
                  resets_at: "0001-01-01T00:00:00Z",
                  checked_at: "2026-08-20T12:00:00Z",
                },
              ],
              next_check_at: "0001-01-01T00:00:00Z",
            },
          ],
        }),
      )

    await expect(getRepositoryReviewAutomationOptions()).resolves.toMatchObject(
      {
        models: [
          { alias: "fast", available: true },
          {
            alias: "blocked",
            available: false,
            blocked_reason: "Agentic CLI models cannot run as reviewers.",
          },
        ],
        limits_error: "account telemetry offline",
        accounts: [
          {
            id: "acct",
            entries: [
              {
                label: "Weekly",
                reset_at: "2026-08-21T12:00:00Z",
              },
            ],
          },
        ],
      },
    )
    await expect(listRepositoryReviewAutomations()).resolves.toMatchObject({
      automations: [
        {
          id: "auto_1",
          reviewer_models: [],
          usage: { total_tokens: 0 },
          progress: { stage: "waiting" },
          model_stats: [{ model: "fast", total_tokens: 100, latency_ms: 250 }],
          account_limits: [
            {
              id: "acct",
              entries: [
                {
                  window: "weekly",
                  label: "Premium",
                  remaining_percent: 75,
                  reset_at: undefined,
                },
              ],
            },
          ],
          next_check_at: undefined,
        },
      ],
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/repository-reviews/automation-options",
      { signal: undefined },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/repository-reviews/automations",
      { signal: undefined },
    )
  })

  it("sends version-fenced automation CRUD and lifecycle mutations", async () => {
    const automation = {
      id: "auto/slash",
      version: 4,
      reviewer_models: [],
      run_ids: [],
      model_prices: {},
      budget: {},
      usage: {},
      progress: {},
      model_stats: [],
      account_limits: [],
    }
    for (let index = 0; index < 7; index += 1) {
      mockedLauncherFetch.mockResolvedValueOnce(
        index === 2
          ? new Response(null, { status: 204 })
          : jsonResponse({ automation }),
      )
    }
    const config = {
      name: "Core review",
      repository: "owner/repo",
      ref: "HEAD",
      target: "all",
      review_focus: "bugs",
      reviewer_models: ["fast"],
      compare_models: false,
      force: false,
      max_files_per_run: 20,
      max_content_bytes: 524288,
      max_parallel_children: 2,
      estimated_output_tokens: 4096,
      auto_continue: true,
      model_prices: {
        fast: { input_price_per_1m: 0.2, output_price_per_1m: 0.8 },
      },
      budget: {
        max_total_tokens: 100000,
        max_estimated_cost_usd: 10,
        account_ids: ["acct"],
        min_remaining_percent: 10,
        min_remaining_percent_by_window: { daily: 20, weekly: 15 },
        auto_resume: true,
        pause_on_unknown: true,
        check_interval_seconds: 900,
      },
    }

    await createRepositoryReviewAutomation(config)
    await updateRepositoryReviewAutomation("auto/slash", {
      ...config,
      expected_version: 4,
    })
    await deleteRepositoryReviewAutomation("auto/slash", {
      expected_version: 5,
    })
    await startRepositoryReviewAutomation("auto/slash", {
      expected_version: 6,
    })
    await pauseRepositoryReviewAutomation("auto/slash", {
      expected_version: 7,
    })
    await resumeRepositoryReviewAutomation("auto/slash", {
      expected_version: 8,
      reset_budget: true,
    })
    await restartRepositoryReviewAutomation("auto/slash", {
      expected_version: 9,
    })

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/repository-reviews/automations",
      expect.objectContaining({ method: "POST", body: JSON.stringify(config) }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/repository-reviews/automations/auto%2Fslash",
      expect.objectContaining({
        method: "PATCH",
        body: JSON.stringify({ ...config, expected_version: 4 }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/repository-reviews/automations/auto%2Fslash",
      expect.objectContaining({
        method: "DELETE",
        body: JSON.stringify({ expected_version: 5 }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      4,
      "/api/repository-reviews/automations/auto%2Fslash/start",
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 6 }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      5,
      "/api/repository-reviews/automations/auto%2Fslash/pause",
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 7 }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      6,
      "/api/repository-reviews/automations/auto%2Fslash/resume",
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 8, reset_budget: true }),
      }),
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      7,
      "/api/repository-reviews/automations/auto%2Fslash/restart",
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 9 }),
      }),
    )
  })
})

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}
