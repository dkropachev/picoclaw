import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  MAXIMUM_PR_DEVELOPMENT_CHAT_BYTES,
  MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES,
  PRDevelopmentAPIError,
  chatAboutPRDevelopmentCase,
  createPRDevelopmentRepairRequestID,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
  normalizePRDevelopmentChatContent,
  startPRDevelopmentRepair,
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
  repair_available: true,
  repair_revision: 0,
} as const
const repairRequestID = `prq_${"6".repeat(32)}`
const repairInstruction = "Apply the reviewer feedback locally."
const repairDetail = {
  ...detail,
  repair_revision: 1,
  repair_session: {
    id: `pds_${"7".repeat(32)}`,
    revision: 1,
    agent_id: "main",
    head_repository: "octocat/repo",
    head_ref: "fix/review-feedback",
    head_sha: "d".repeat(40),
    attempts: [
      {
        id: `pdr_${"8".repeat(32)}`,
        ordinal: 0,
        status: "queued",
        conversation_version: 2,
        instruction: repairInstruction,
        created_at: "2026-08-05T12:03:00Z",
        updated_at: "2026-08-05T12:03:00Z",
      },
    ],
  },
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

  it("starts one version-fenced local repair and accepts only 202", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(repairDetail, 202))

    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: `  ${repairInstruction}  `,
      }),
    ).resolves.toEqual(repairDetail)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/pr-development/${caseID}/repair`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_conversation_version: 2,
          expected_repair_revision: 0,
          request_id: repairRequestID,
          instruction: repairInstruction,
        }),
      },
    )

    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(repairDetail))
    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).rejects.toMatchObject({ status: 502 })
  })

  it("binds an idempotent repair replay to its exact original ordinal", async () => {
    const replayedDetail = {
      ...repairDetail,
      repair_revision: 2,
      repair_session: {
        ...repairDetail.repair_session,
        revision: 2,
        head_repository: "octocat/repo",
        head_ref: "fix/review-feedback",
        head_sha: "d".repeat(40),
        attempts: [
          {
            ...repairDetail.repair_session.attempts[0],
            status: "completed",
            summary: "The original repair completed locally.",
            updated_at: "2026-08-05T12:04:00Z",
          },
          {
            id: `pdr_${"9".repeat(32)}`,
            ordinal: 1,
            status: "queued",
            conversation_version: 2,
            instruction: "Apply a later follow-up locally.",
            created_at: "2026-08-05T12:05:00Z",
            updated_at: "2026-08-05T12:05:00Z",
          },
        ],
      },
    } as const
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(replayedDetail, 202))

    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).resolves.toEqual(replayedDetail)

    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(replayedDetail, 202))
    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 1,
        expectedAttemptOrdinal: 1,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).rejects.toMatchObject({ status: 502 })
  })

  it("sends a full-history suffix fence and preserves authoritative capacity detail", async () => {
    const capacityDetail = {
      ...repairDetail,
      repair_revision: 64,
      repair_session: {
        ...repairDetail.repair_session,
        revision: 64,
        attempts: Array.from({ length: 64 }, (_, ordinal) => ({
          ...repairDetail.repair_session.attempts[0],
          id: `pdr_${ordinal.toString(16).padStart(32, "0")}`,
          ordinal,
          status: "completed" as const,
          instruction: `Completed local repair ${ordinal + 1}.`,
          summary: `Local repair ${ordinal + 1} completed.`,
          updated_at: "2026-08-05T12:04:00Z",
        })),
      },
    }
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(
        { error: "repair attempt capacity reached", detail: capacityDetail },
        409,
      ),
    )

    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 64,
        expectedAttemptOrdinal: 64,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).rejects.toMatchObject({
      status: 409,
      message: "repair attempt capacity reached",
      detail: capacityDetail,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/pr-development/${caseID}/repair`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          expected_conversation_version: 2,
          expected_repair_revision: 64,
          request_id: repairRequestID,
          instruction: repairInstruction,
        }),
      },
    )
  })

  it("generates opaque repair request IDs with secure browser randomness", () => {
    const first = createPRDevelopmentRepairRequestID()
    const second = createPRDevelopmentRepairRequestID()
    expect(first).toMatch(/^prq_[0-9a-f]{32}$/)
    expect(second).toMatch(/^prq_[0-9a-f]{32}$/)
    expect(second).not.toBe(first)
  })

  it("parses bounded repair history, terminal outcomes, and a pinned head", async () => {
    const historyDetail = {
      ...repairDetail,
      repair_revision: 7,
      repair_session: {
        ...repairDetail.repair_session,
        revision: 7,
        attempts: [
          {
            ...repairDetail.repair_session.attempts[0],
            status: "completed",
            summary: '<script>alert("summary")</script> stayed plain.',
            updated_at: "2026-08-05T12:04:00Z",
          },
          {
            id: `pdr_${"9".repeat(32)}`,
            ordinal: 1,
            status: "failed",
            conversation_version: 2,
            instruction: "Try the narrowed repair.",
            summary: "Partial local edits may remain.",
            error_code: "repair_failed",
            created_at: "2026-08-05T12:05:00Z",
            updated_at: "2026-08-05T12:06:00Z",
          },
        ],
      },
    } as const
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(historyDetail))

    await expect(getPRDevelopmentCase(caseID)).resolves.toEqual(historyDetail)
  })

  it("accepts repair instruction and summary at the exact 4 KiB boundary", async () => {
    const boundaryText = "x".repeat(4 << 10)
    const boundaryDetail = {
      ...repairDetail,
      repair_session: {
        ...repairDetail.repair_session,
        attempts: [
          {
            ...repairDetail.repair_session.attempts[0],
            status: "completed",
            instruction: boundaryText,
            summary: boundaryText,
            updated_at: "2026-08-05T12:04:00Z",
          },
        ],
      },
    } as const
    mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(boundaryDetail))

    await expect(getPRDevelopmentCase(caseID)).resolves.toEqual(boundaryDetail)
  })

  it("requires a complete public pin for recovery-required repairs", async () => {
    const unpinnedRecoveryDetail = {
      ...repairDetail,
      repair_session: {
        ...repairDetail.repair_session,
        head_repository: undefined,
        head_ref: undefined,
        head_sha: undefined,
        attempts: [
          {
            ...repairDetail.repair_session.attempts[0],
            status: "recovery_required",
            summary: "Partial local edits may exist.",
            error_code: "recovery_required",
          },
        ],
      },
    } as const
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse(unpinnedRecoveryDetail),
    )

    await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
      status: 502,
    })
  })

  it("rejects unsafe or inconsistent repair projections", async () => {
    const completedAttempt = {
      ...repairDetail.repair_session.attempts[0],
      status: "completed",
      summary: "Local edits are ready for review.",
      updated_at: "2026-08-05T12:04:00Z",
    } as const
    const failedAttempt = {
      ...completedAttempt,
      status: "failed",
      error_code: "repair_failed",
    } as const
    const invalidDetails: unknown[] = [
      { ...detail, repair_revision: 1 },
      { ...detail, repair_revision: 1025 },
      {
        ...detail,
        repair_available: true,
        repair_unavailable_reason: "runtime_unavailable",
      },
      { ...detail, repair_available: false },
      {
        ...repairDetail,
        repair_session: { ...repairDetail.repair_session, revision: 2 },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          checkout_path: "/private/worktree",
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          agent_id: "Main Agent",
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          head_ref: undefined,
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          head_repository: undefined,
          head_ref: undefined,
          head_sha: undefined,
          attempts: [
            { ...repairDetail.repair_session.attempts[0], status: "running" },
          ],
        },
      },
      {
        ...repairDetail,
        conversation_version: 2,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...completedAttempt,
              conversation_version: 2,
            },
            {
              ...completedAttempt,
              id: `pdr_${"b".repeat(32)}`,
              ordinal: 1,
              conversation_version: 1,
              created_at: "2026-08-05T12:05:00Z",
              updated_at: "2026-08-05T12:06:00Z",
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [{ ...completedAttempt, summary: undefined }],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...completedAttempt,
              summary: "x".repeat((4 << 10) + 1),
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...repairDetail.repair_session.attempts[0],
              instruction: "x".repeat((4 << 10) + 1),
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [{ ...failedAttempt, summary: undefined }],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...failedAttempt,
              status: "recovery_required",
              error_code: "repair_failed",
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...repairDetail.repair_session.attempts[0],
              updated_at: "2026-08-05T12:02:59Z",
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            {
              ...repairDetail.repair_session.attempts[0],
              conversation_version: 3,
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [
            repairDetail.repair_session.attempts[0],
            {
              ...completedAttempt,
              id: `pdr_${"a".repeat(32)}`,
              ordinal: 1,
            },
          ],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: [{ ...completedAttempt, private_tool_args: "secret" }],
        },
      },
      {
        ...repairDetail,
        repair_session: {
          ...repairDetail.repair_session,
          attempts: Array.from({ length: 65 }, (_, ordinal) => ({
            ...completedAttempt,
            id: `pdr_${ordinal.toString(16).padStart(32, "0")}`,
            ordinal,
          })),
        },
      },
    ]

    for (const invalid of invalidDetails) {
      mockedLauncherFetch.mockResolvedValueOnce(jsonResponse(invalid))
      await expect(getPRDevelopmentCase(caseID)).rejects.toMatchObject({
        status: 502,
      })
    }
  })

  it("binds repair success and authoritative error detail to both versions", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse(
          {
            ...repairDetail,
            repair_revision: 0,
            repair_session: undefined,
          },
          202,
        ),
      )
      .mockResolvedValueOnce(
        jsonResponse(
          {
            error: "repair changed; reload before retrying",
            detail: repairDetail,
          },
          409,
        ),
      )

    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).rejects.toMatchObject({ status: 502 })
    await expect(
      startPRDevelopmentRepair(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      }),
    ).rejects.toMatchObject({
      status: 409,
      message: "repair changed; reload before retrying",
      detail: repairDetail,
    })
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

  it("rejects invalid local repair input before making a request", async () => {
    for (const input of [
      {
        expectedConversationVersion: -1,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      },
      {
        expectedConversationVersion: 2,
        expectedRepairRevision: 1025,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: repairInstruction,
      },
      {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 65,
        requestID: repairRequestID,
        instruction: repairInstruction,
      },
      {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: "prq_not-random",
        instruction: repairInstruction,
      },
      {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: "   ",
      },
      {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: repairRequestID,
        instruction: "x".repeat(
          MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES + 1,
        ),
      },
    ]) {
      await expect(
        startPRDevelopmentRepair(caseID, input),
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
      repair_available: false,
      repair_unavailable_reason: "runtime_unavailable",
      repair_revision: 0,
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
