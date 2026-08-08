import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES,
  PRDevelopmentAPIError,
  chatAboutPRDevelopmentCase,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
  normalizePRDevelopmentChatContent,
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
const detail = {
  case: developmentCase,
  conversation_version: 2,
  messages: [
    {
      id: `pdm_${"2".repeat(32)}`,
      ordinal: 0,
      role: "user",
      content: "How should I approach this?",
      created_at: "2026-08-05T12:01:00Z",
    },
    {
      id: `pdm_${"3".repeat(32)}`,
      ordinal: 1,
      role: "assistant",
      content: 'Treat <script>alert("x")</script> as plain text.',
      created_at: "2026-08-05T12:01:01Z",
    },
  ],
} as const
const submittedChatContent = "Explain the retry path."
const chatDetail = {
  ...detail,
  conversation_version: 4,
  messages: [
    ...detail.messages,
    {
      id: `pdm_${"4".repeat(32)}`,
      ordinal: 2,
      role: "user",
      content: submittedChatContent,
      created_at: "2026-08-05T12:02:00Z",
    },
    {
      id: `pdm_${"5".repeat(32)}`,
      ordinal: 3,
      role: "assistant",
      content: "Start by covering the version-conflict path.",
      created_at: "2026-08-05T12:02:01Z",
    },
  ],
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

  it("reads a case-bound detail and preserves feedback exactly", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(detail))

    await expect(getPRDevelopmentCase(caseID)).resolves.toEqual(detail)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/pr-development/${caseID}`,
      undefined,
    )
  })

  it("posts one locally validated advisory chat message", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(chatDetail))

    await expect(
      chatAboutPRDevelopmentCase(
        caseID,
        2,
        `\u0085  ${submittedChatContent}  \u0085`,
      ),
    ).resolves.toEqual(chatDetail)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/pr-development/${caseID}/chat`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_version: 2,
          content: submittedChatContent,
        }),
      },
    )
  })

  it("matches Go whitespace canonicalization without stripping a BOM", () => {
    expect(
      normalizePRDevelopmentChatContent("\u0085  explain this \u0085"),
    ).toBe("explain this")
    expect(normalizePRDevelopmentChatContent("\ufeffkeep BOM\ufeff")).toBe(
      "\ufeffkeep BOM\ufeff",
    )
  })

  it("accepts canonical stored message content bounded by BOM code points", async () => {
    const detailWithBOM = {
      ...detail,
      messages: detail.messages.map((message, index) =>
        index === 1
          ? { ...message, content: `\ufeff${message.content}\ufeff` }
          : message,
      ),
    }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(detailWithBOM))

    await expect(getPRDevelopmentCase(caseID)).resolves.toEqual(detailWithBOM)
  })

  it("rejects invalid chat input before making a request", async () => {
    await expect(getPRDevelopmentCase("pdc_/unsafe")).rejects.toMatchObject({
      status: 400,
    })
    for (const [id, version, content] of [
      ["not-a-case", 2, "Explain this."],
      [caseID, -1, "Explain this."],
      [caseID, 257, "Explain this."],
      [caseID, 2, "   "],
      [caseID, 2, "unsafe\0message"],
      [caseID, 2, "x".repeat(MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES + 1)],
    ] as const) {
      await expect(
        chatAboutPRDevelopmentCase(id, version, content),
      ).rejects.toMatchObject({ status: 400 })
    }
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it("rejects cross-case, stale, or unbound successful chat details", async () => {
    const anotherCaseID = `pdc_${"9".repeat(32)}`
    for (const invalid of [
      {
        ...chatDetail,
        case: { ...developmentCase, id: anotherCaseID },
      },
      detail,
      {
        ...chatDetail,
        messages: chatDetail.messages.map((message, index) =>
          index === 2 ? { ...message, content: "another message" } : message,
        ),
      },
      {
        ...chatDetail,
        messages: chatDetail.messages.map((message, index) =>
          index === 3 ? { ...message, role: "user" } : message,
        ),
      },
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(invalid))
      await expect(
        chatAboutPRDevelopmentCase(caseID, 2, submittedChatContent),
      ).rejects.toMatchObject({ status: 502 })
    }
  })

  it("rejects a structurally valid detail bound to another case", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        ...detail,
        case: { ...developmentCase, id: `pdc_${"9".repeat(32)}` },
      }),
    )

    await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
      status: 502,
    })
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
      { ...summary, captured_at: "2026-02-30T12:00:01Z" },
      { ...summary, captured_at: "2026-04-31T12:00:01Z" },
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
      mockedLauncherFetch.mockResolvedValueOnce(
        jsonResponse({ ...detail, case: invalid }),
      )
      await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
        status: 502,
      })
    }

    mockedLauncherFetch.mockResolvedValueOnce({
      ok: true,
      json: vi.fn().mockResolvedValue({
        ...detail,
        case: { ...developmentCase, feedback: String.fromCharCode(0xd800) },
      }),
    } as unknown as Response)
    await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
      status: 502,
    })
  })

  it("strictly validates the bounded contiguous conversation", async () => {
    const firstMessage = detail.messages[0]
    for (const invalid of [
      { ...detail, conversation_version: 1 },
      { ...detail, conversation_version: -1 },
      {
        ...detail,
        messages: [{ ...firstMessage, id: `prm_${"2".repeat(32)}` }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [{ ...firstMessage, ordinal: 1 }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [{ ...firstMessage, role: "system" }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [{ ...firstMessage, content: " padded " }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [{ ...firstMessage, content: "x".repeat((64 << 10) + 1) }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [{ ...firstMessage, created_at: "yesterday" }],
        conversation_version: 1,
      },
      {
        ...detail,
        messages: [firstMessage, { ...firstMessage, ordinal: 1 }],
      },
      {
        ...detail,
        messages: [{ ...firstMessage, private_agent_id: "main" }],
        conversation_version: 1,
      },
    ]) {
      mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(invalid))
      await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
        status: 502,
      })
    }
  })

  it("accepts only safe error detail for optimistic recovery", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "conversation changed; reload before retrying",
            detail,
          },
          409,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "private failure",
            detail: {
              ...detail,
              case: { ...developmentCase, session: "private-session" },
            },
          },
          503,
        ),
      )

    await expect(
      chatAboutPRDevelopmentCase(caseID, 2, "Explain this."),
    ).rejects.toMatchObject({
      status: 409,
      message: "conversation changed; reload before retrying",
      detail,
    })
    await expect(
      chatAboutPRDevelopmentCase(caseID, 2, "Explain this."),
    ).rejects.toMatchObject({
      status: 503,
      message: "PR development is unavailable.",
      detail: undefined,
    })
  })

  it("accepts authoritative error detail larger than 64 KiB", async () => {
    const largeContent = "x".repeat(64 << 10)
    const largeDetail = {
      case: developmentCase,
      conversation_version: 1,
      messages: [
        {
          id: `pdm_${"4".repeat(32)}`,
          ordinal: 0,
          role: "user",
          content: largeContent,
          created_at: "2026-08-05T12:04:00Z",
        },
      ],
    } as const
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        { error: "development AI unavailable", detail: largeDetail },
        503,
      ),
    )

    await expect(
      chatAboutPRDevelopmentCase(caseID, 0, "Explain this."),
    ).rejects.toMatchObject({
      status: 503,
      detail: largeDetail,
    })
  })

  it("drops cross-case or older error details", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "development AI unavailable",
            detail: {
              ...detail,
              case: {
                ...developmentCase,
                id: `pdc_${"9".repeat(32)}`,
              },
            },
          },
          503,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "development AI unavailable",
            detail: {
              ...detail,
              conversation_version: 1,
              messages: [detail.messages[0]],
            },
          },
          503,
        ),
      )

    for (let index = 0; index < 2; index += 1) {
      await expect(
        chatAboutPRDevelopmentCase(caseID, 2, "Explain this."),
      ).rejects.toMatchObject({
        status: 503,
        message: "PR development is unavailable.",
        detail: undefined,
      })
    }
  })

  it("never admits safe detail from list or detail-read errors", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ error: "list unavailable", detail }, 503),
      )
      .mockResolvedValueOnce(
        jsonResponse({ error: "detail unavailable", detail }, 503),
      )

    await expect(listPRDevelopmentCases()).rejects.toMatchObject({
      status: 503,
      message: "PR development is unavailable.",
      detail: undefined,
    })
    await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
      status: 503,
      message: "PR development is unavailable.",
      detail: undefined,
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
