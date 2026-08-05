import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  PRDevelopmentAPIError,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
} from "@/api/pr-development"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const caseID = `pdc_${"1".repeat(32)}`
const summary = {
  id: caseID,
  repository: "octo/repo",
  pull_number: 42,
  pull_url: "https://github.com/octo/repo/pull/42",
  pull_author: "octocat",
  pull_state: "open",
  pull_draft: false,
  pull_merged: false,
  head_repository: "octocat/repo",
  head_ref: "fix/review-feedback",
  head_sha: "a".repeat(40),
  review_author: "reviewer",
  submitted_review_state: "changes_requested",
  current_review_state: "changes_requested",
  review_submitted_at: "2026-08-05T12:00:00Z",
  review_url: "https://github.com/octo/repo/pull/42#pullrequestreview-7",
  captured_at: "2026-08-05T12:00:01Z",
} as const
const developmentCase = {
  ...summary,
  base_repository: "octo/repo",
  base_ref: "main",
  base_sha: "b".repeat(40),
  review_commit_sha: "c".repeat(40),
  feedback: "Please cover the retry path.\0Keep the exact provider text.",
} as const

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("PR development API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("lists strict safe summaries with encoded filters", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ cases: [summary], next_cursor: "next+/=" }),
    )

    await expect(
      listPRDevelopmentCases({
        repository: "octo/repo with space",
        pull_number: 42,
        limit: 40,
        cursor: "cursor+/=",
      }),
    ).resolves.toEqual({ cases: [summary], next_cursor: "next+/=" })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/pr-development?repository=octo%2Frepo+with+space&pull_number=42&limit=40&cursor=cursor%2B%2F%3D",
      undefined,
    )
  })

  it("reads an encoded detail and preserves feedback exactly", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ case: developmentCase }),
    )

    await expect(getPRDevelopmentCase("pdc_/unsafe")).resolves.toEqual({
      case: developmentCase,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/pr-development/pdc_%2Funsafe",
      undefined,
    )
  })

  it("rejects internal fields and inconsistent safe projections", async () => {
    for (const invalid of [
      { ...summary, event_id: `ev_${"2".repeat(32)}` },
      { ...summary, id: `prc_${"1".repeat(32)}` },
      { ...summary, pull_url: "http://github.com/octo/repo/pull/42" },
      { ...summary, pull_number: Number.MAX_SAFE_INTEGER + 1 },
      { ...summary, pull_number: 2_147_483_648 },
      { ...summary, pull_merged: true, pull_state: "open" },
      { ...summary, current_review_state: "approved" },
      { ...summary, head_sha: "A".repeat(40) },
      { ...summary, captured_at: "not-a-time" },
      { ...summary, captured_at: "August 5, 2026 12:00:01 UTC" },
      { ...summary, repository: "octo/repo with space" },
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(
        jsonResponse({ cases: [invalid] }),
      )
      await expect(listPRDevelopmentCases()).rejects.toMatchObject({
        status: 502,
      })
    }
  })

  it("rejects unsafe detail additions, mismatched base, and oversized feedback", async () => {
    for (const invalid of [
      { ...developmentCase, review_id: "7" },
      { ...developmentCase, trigger_review_node_id: "private" },
      { ...developmentCase, base_repository: "another/repo" },
      { ...developmentCase, review_commit_sha: "z".repeat(40) },
      { ...developmentCase, feedback: "é".repeat((64 << 10) / 2 + 1) },
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(jsonResponse({ case: invalid }))
      await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
        status: 502,
      })
    }

    mockedLauncherFetch.mockResolvedValueOnce({
      ok: true,
      json: vi.fn().mockResolvedValue({
        case: {
          ...developmentCase,
          feedback: String.fromCharCode(0xd800),
        },
      }),
    } as unknown as Response)
    await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
      status: 502,
    })
  })

  it("bounds list shape and returns only a safe public error", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ cases: [], unknown: true }))
      .mockResolvedValueOnce(jsonResponse({ cases: [], next_cursor: "" }))
      .mockResolvedValueOnce(
        jsonResponse({ error: "development unavailable" }, 503),
      )
      .mockResolvedValueOnce(
        new Response("private upstream failure", { status: 500 }),
      )

    await expect(listPRDevelopmentCases()).rejects.toBeInstanceOf(
      PRDevelopmentAPIError,
    )
    await expect(listPRDevelopmentCases()).rejects.toMatchObject({
      status: 502,
    })
    await expect(listPRDevelopmentCases()).rejects.toMatchObject({
      message: "development unavailable",
      status: 503,
    })
    await expect(listPRDevelopmentCases()).rejects.toMatchObject({
      message: "PR development is unavailable.",
      status: 500,
    })
  })
})
