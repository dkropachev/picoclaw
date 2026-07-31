import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  ReviewAPIError,
  chatAboutReview,
  dropReviewFinding,
  getReview,
  listReviews,
  reconcileReview,
  rephraseReviewFinding,
  restoreReviewFinding,
  submitReview,
  updateReviewFinding,
} from "@/api/reviews"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

const ids = {
  case: `prc_${"1".repeat(32)}`,
  finding: `prf_${"2".repeat(32)}`,
  message: `prm_${"3".repeat(32)}`,
  submission: `prs_${"4".repeat(32)}`,
}

const reviewCase = {
  id: ids.case,
  event_id: `ev_${"5".repeat(32)}`,
  dispatch_id: `dsp_${"6".repeat(32)}`,
  run_id: "wr_review",
  workflow_ref: "builtin://github-pr-review",
  workflow_revision: `sha256:${"a".repeat(64)}`,
  connector: "primary",
  repository: "octo/repo",
  pull_number: 42,
  pull_url: "https://github.com/octo/repo/pull/42",
  base_sha: "a".repeat(40),
  head_sha: "b".repeat(40),
  summary: "One correctness issue was found.",
  tests: ["go test ./..."],
  residual_risks: [],
  status: "open",
  version: 3,
  active_findings: 1,
  total_findings: 1,
  created_at: "2026-07-30T12:00:00Z",
  updated_at: "2026-07-30T12:01:00Z",
} as const

const finding = {
  id: ids.finding,
  case_id: ids.case,
  ordinal: 0,
  state: "active",
  severity: "high",
  title: "Lost update",
  file: "pkg/store.go",
  line: 72,
  message: "This write can replace newer state.",
  evidence: "The version predicate is missing.",
  impact: "Concurrent edits can be lost.",
  recommendation: "Include the expected version in the update.",
  validation: "Add a concurrent update test.",
  revision: 1,
  created_at: "2026-07-30T12:00:00Z",
  updated_at: "2026-07-30T12:00:00Z",
} as const

const detail = {
  case: reviewCase,
  findings: [finding],
  messages: [],
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("reviews API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("lists strict case projections with encoded filters", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ cases: [reviewCase], next_cursor: "next+/=" }),
    )

    await expect(
      listReviews({
        status: "submission_unknown",
        repository: "octo/repo with space",
        limit: 40,
        cursor: "cursor+/=",
      }),
    ).resolves.toEqual({
      cases: [reviewCase],
      next_cursor: "next+/=",
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/reviews?status=submission_unknown&repository=octo%2Frepo+with+space&limit=40&cursor=cursor%2B%2F%3D",
      undefined,
    )
  })

  it("normalizes omitted case lists and reads an encoded case path", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ cases: [{ ...reviewCase }] }))
      .mockResolvedValueOnce(jsonResponse(detail))

    await expect(listReviews()).resolves.toMatchObject({
      cases: [{ tests: ["go test ./..."], residual_risks: [] }],
    })
    await expect(getReview("prc_/unsafe")).resolves.toEqual(detail)

    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      "/api/reviews/prc_%2Funsafe",
      undefined,
    )
  })

  it("normalizes safe nil Go slices without accepting omitted aggregates", async () => {
    const emptyCase = {
      ...reviewCase,
      status: "all_dropped",
      active_findings: 0,
      total_findings: 0,
    }
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          case: emptyCase,
          findings: null,
          messages: null,
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          case: emptyCase,
          messages: [],
        }),
      )

    await expect(getReview(ids.case)).resolves.toEqual({
      case: emptyCase,
      findings: [],
      messages: [],
    })
    await expect(getReview(ids.case)).rejects.toMatchObject({ status: 502 })
  })

  it("rejects a transcript beyond the durable per-case message bound", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...detail,
        messages: Array.from({ length: 257 }, (_, ordinal) => ({
          id: `prm_${ordinal.toString(16).padStart(32, "0")}`,
          case_id: ids.case,
          ordinal,
          kind: "chat",
          role: "assistant",
          content: "Bounded message.",
          created_at: "2026-07-30T12:03:00Z",
        })),
      }),
    )

    await expect(getReview(ids.case)).rejects.toMatchObject({ status: 502 })
  })

  it("rejects a transcript beyond the durable aggregate byte bound", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...detail,
        messages: Array.from({ length: 65 }, (_, ordinal) => ({
          id: `prm_${ordinal.toString(16).padStart(32, "0")}`,
          case_id: ids.case,
          ordinal,
          kind: "chat",
          role: "assistant",
          content: "x".repeat(64 << 10),
          created_at: "2026-07-30T12:03:00Z",
        })),
      }),
    )

    await expect(getReview(ids.case)).rejects.toMatchObject({ status: 502 })
  })

  it("sends exact optimistic mutation bodies", async () => {
    mockedLauncherFetch.mockImplementation(async () => jsonResponse(detail))
    const draft = {
      severity: "medium" as const,
      title: "Clarified title",
      file: "pkg/store.go",
      line: 72,
      message: "Clarified comment.",
      evidence: "Evidence.",
      impact: "Impact.",
      recommendation: "Recommendation.",
      validation: "Validation.",
    }

    await updateReviewFinding(ids.case, ids.finding, 3, draft)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/findings/${ids.finding}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ expected_version: 3, finding: draft }),
      },
    )

    await dropReviewFinding(ids.case, ids.finding, 3, " duplicate ")
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/findings/${ids.finding}/drop`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_version: 3,
          reason: "duplicate",
        }),
      }),
    )

    await restoreReviewFinding(ids.case, ids.finding, 4)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/findings/${ids.finding}/restore`,
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 4 }),
      }),
    )

    await chatAboutReview(ids.case, 4, "Why?", ids.finding)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/chat`,
      expect.objectContaining({
        body: JSON.stringify({
          expected_version: 4,
          finding_id: ids.finding,
          content: "Why?",
        }),
      }),
    )

    await submitReview(ids.case, 5)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/submit`,
      expect.objectContaining({
        body: JSON.stringify({ expected_version: 5 }),
      }),
    )

    await reconcileReview(ids.case, 6, "absent")
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/reviews/${ids.case}/reconcile`,
      expect.objectContaining({
        body: JSON.stringify({
          expected_version: 6,
          resolution: "absent",
        }),
      }),
    )
  })

  it("accepts a rephrase preview only with a safe updated detail", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        detail,
        suggestion: {
          title: "Concise title",
          message: "Concise, constructive comment.",
        },
      }),
    )

    await expect(
      rephraseReviewFinding(ids.case, ids.finding, 3, "Be concise"),
    ).resolves.toMatchObject({
      detail,
      suggestion: {
        title: "Concise title",
        message: "Concise, constructive comment.",
      },
    })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/reviews/${ids.case}/findings/${ids.finding}/rephrase`,
      expect.objectContaining({
        body: JSON.stringify({
          expected_version: 3,
          instruction: "Be concise",
        }),
      }),
    )
  })

  it("rejects internal submission fields from the safe projection", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...detail,
        case: {
          ...reviewCase,
          status: "submitting",
        },
        submission: {
          id: ids.submission,
          case_id: ids.case,
          draft_version: 3,
          status: "pending",
          attempts: 0,
          marker: "private marker",
          lease_token: "private lease",
          request: { comments: [] },
          internal_error: "private error",
          created_at: "2026-07-30T12:02:00Z",
          updated_at: "2026-07-30T12:02:00Z",
        },
      }),
    )

    await expect(getReview(ids.case)).rejects.toMatchObject({
      message: "The review service returned a malformed response.",
      status: 502,
    })
  })

  it("rejects inconsistent aggregate relationships and malformed identifiers", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({
          ...detail,
          case: { ...reviewCase, active_findings: 0 },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...detail,
          findings: [{ ...finding, case_id: `prc_${"9".repeat(32)}` }],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...detail,
          messages: [
            {
              id: ids.message,
              case_id: ids.case,
              ordinal: 0,
              finding_id: `prf_${"9".repeat(32)}`,
              kind: "chat",
              role: "assistant",
              content: "Not linked to this case.",
              created_at: "2026-07-30T12:03:00Z",
            },
          ],
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ...detail,
          case: { ...reviewCase, id: `prc_${"A".repeat(32)}` },
        }),
      )

    for (let index = 0; index < 4; index += 1) {
      await expect(getReview(ids.case)).rejects.toBeInstanceOf(ReviewAPIError)
    }
  })

  it("surfaces a validated latest detail on public conflicts without retrying", async () => {
    const latest = {
      ...detail,
      case: { ...reviewCase, version: 4 },
    }
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ error: "review case changed", detail: latest }, 409),
    )

    await expect(submitReview(ids.case, 3)).rejects.toMatchObject({
      message: "review case changed",
      status: 409,
      detail: latest,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledOnce()
  })
})
