import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  ReviewAttentionAPIError,
  getReviewAttention,
  respondToReviewAttention,
} from "@/api/review-attention"
import { stringifyExactJSON } from "@/api/review-attention-json"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const caseID = `prc_${"1".repeat(32)}`
const responseToken = `sha256:${"a".repeat(64)}`

function rawResponse(source: string, status = 200): Response {
  return new Response(source, {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function waitingProjection(questions = '{"priority":9007199254740993}') {
  return `{"case_version":7,"status":"waiting","can_respond":true,"turns":[{"status":"waiting","title":"Choose a safe contract","questions":${questions},"response_token":"${responseToken}"}]}`
}

describe("review attention API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("strictly reads a case-owned projection without rounding question numbers", async () => {
    mockedLauncherFetch.mockResolvedValue(rawResponse(waitingProjection()))

    const projection = await getReviewAttention(caseID)

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/reviews/${caseID}/attention`,
      {},
    )
    expect(projection).toMatchObject({
      case_version: 7,
      status: "waiting",
      can_respond: true,
      turns: [{ title: "Choose a safe contract" }],
    })
    expect(stringifyExactJSON(projection.turns[0].questions)).toBe(
      '{"priority":9007199254740993}',
    )
  })

  it("sends one trimmed fenced response and parses the next state", async () => {
    mockedLauncherFetch.mockResolvedValue(
      rawResponse(
        '{"case_version":7,"status":"completed","can_respond":false,"turns":[{"status":"answered","title":"Choose a safe contract","questions":["Keep v1?"],"response":"Keep v1"}]}',
      ),
    )

    await expect(
      respondToReviewAttention(caseID, 7, responseToken, "\u0085Keep v1\u0085"),
    ).resolves.toMatchObject({ status: "completed", can_respond: false })

    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/reviews/${caseID}/attention/respond`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_case_version: 7,
          response_token: responseToken,
          response: "Keep v1",
        }),
      },
    )
  })

  it("rejects unknown private fields, malformed tokens, and inconsistent authority", async () => {
    for (const source of [
      waitingProjection().replace(
        '"can_respond":true',
        '"run_id":"private","can_respond":true',
      ),
      waitingProjection().replace(
        '"title":"Choose a safe contract"',
        '"task_id":"private","title":"Choose a safe contract"',
      ),
      waitingProjection().replace(responseToken, "sha256:ABC"),
      waitingProjection().replace('"can_respond":true', '"can_respond":false'),
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(rawResponse(source))
      await expect(getReviewAttention(caseID)).rejects.toMatchObject({
        status: 502,
        code: "invalid_attention_response",
      })
    }
  })

  it("accepts read-only runtime-disabled and canceled failure projections", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        rawResponse(
          '{"case_version":7,"status":"waiting","can_respond":false,"turns":[{"status":"waiting","title":"Choose a safe contract","questions":[]}]}',
        ),
      )
      .mockResolvedValueOnce(
        rawResponse(
          '{"case_version":7,"status":"failed","can_respond":false,"turns":[{"status":"canceled","title":"Choose a safe contract","questions":[]}]}',
        ),
      )

    await expect(getReviewAttention(caseID)).resolves.toMatchObject({
      status: "waiting",
      can_respond: false,
    })
    await expect(getReviewAttention(caseID)).resolves.toMatchObject({
      status: "failed",
      turns: [{ status: "canceled" }],
    })
  })

  it("rejects tokens outside actionable turns and canceled turns outside failures", async () => {
    for (const source of [
      `{"case_version":7,"status":"continuing","can_respond":false,"turns":[{"status":"continuing","title":"Choose a safe contract","questions":[],"response":"Keep v1","response_token":"${responseToken}"}]}`,
      '{"case_version":7,"status":"completed","can_respond":false,"turns":[{"status":"canceled","title":"Choose a safe contract","questions":[]}]}',
      waitingProjection().replace(
        '"status":"waiting"',
        '"status":"processing"',
      ),
      '{"case_version":7,"status":"queued","can_respond":false,"turns":[{"status":"answered","title":"Done","questions":[],"response":"yes"}]}',
      '{"case_version":7,"status":"completed","can_respond":false,"turns":[{"status":"continuing","title":"Continue","questions":[],"response":"yes"}]}',
      '{"case_version":7,"status":"failed","can_respond":false,"turns":[{"status":"waiting","title":"Waiting","questions":[]}]}',
      '{"case_version":7,"status":"waiting","can_respond":false,"turns":[{"status":"canceled","title":"Canceled","questions":[]},{"status":"waiting","title":"Waiting","questions":[]}]}',
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(rawResponse(source))
      await expect(getReviewAttention(caseID)).rejects.toMatchObject({
        code: "invalid_attention_response",
      })
    }
  })

  it("rejects excessive turns, title, questions, and stored responses", async () => {
    const turn =
      '{"status":"answered","title":"Done","questions":[],"response":"yes"}'
    const sources = [
      `{"case_version":7,"status":"completed","can_respond":false,"turns":[${Array.from({ length: 65 }, () => turn).join(",")}]}`,
      waitingProjection().replace(
        "Choose a safe contract",
        "x".repeat((4 << 10) + 1),
      ),
      waitingProjection(JSON.stringify("x".repeat(256 << 10))),
      `{"case_version":7,"status":"completed","can_respond":false,"turns":[{"status":"answered","title":"Done","questions":[],"response":${JSON.stringify("x".repeat((32 << 10) + 1))}}]}`,
    ]
    for (const source of sources) {
      mockedLauncherFetch.mockResolvedValueOnce(rawResponse(source))
      await expect(getReviewAttention(caseID)).rejects.toBeInstanceOf(
        ReviewAttentionAPIError,
      )
    }
  })

  it("validates response fences and maps only a safe public error", async () => {
    await expect(
      respondToReviewAttention(caseID, 0, responseToken, "answer"),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      respondToReviewAttention(caseID, 7, "private-task", "answer"),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      respondToReviewAttention(caseID, 7, responseToken, "   "),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      respondToReviewAttention(caseID, 7, responseToken, "\u0085"),
    ).rejects.toMatchObject({ status: 400 })
    expect(mockedLauncherFetch).not.toHaveBeenCalled()

    mockedLauncherFetch.mockResolvedValue(
      rawResponse('{"error":"review_attention_conflict"}', 409),
    )
    await expect(getReviewAttention(caseID)).rejects.toMatchObject({
      status: 409,
      code: "review_attention_conflict",
    })

    mockedLauncherFetch.mockResolvedValue(
      rawResponse('{"error":"safe","private":"diagnostic"}', 500),
    )
    await expect(getReviewAttention(caseID)).rejects.toMatchObject({
      status: 500,
      code: "attention_unavailable",
    })
  })
})
