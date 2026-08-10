import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES,
  PRDevelopmentAPIError,
  type PRDevelopmentCase,
  type PRDevelopmentCaseDetail,
  chatAboutPRDevelopmentCase,
  createPRDevelopmentRepairRequestID,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
  startPRDevelopmentRepair,
} from "@/api/pr-development"
import {
  PRDevelopmentAttentionAPIError,
  type PRDevelopmentAttentionProjection,
  getPRDevelopmentAttention,
  respondToPRDevelopmentAttention,
} from "@/api/pr-development-attention"
import { parseExactJSON } from "@/api/review-attention-json"
import { PRDevelopmentPage } from "@/components/reviews/pr-development-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/pr-development", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/pr-development")>()
  return {
    ...actual,
    chatAboutPRDevelopmentCase: vi.fn(),
    createPRDevelopmentRepairRequestID: vi.fn(),
    getPRDevelopmentCase: vi.fn(),
    listPRDevelopmentCases: vi.fn(),
    startPRDevelopmentRepair: vi.fn(),
  }
})

vi.mock("@/api/pr-development-attention", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/api/pr-development-attention")>()
  return {
    ...actual,
    getPRDevelopmentAttention: vi.fn(),
    respondToPRDevelopmentAttention: vi.fn(),
  }
})

const mockedList = vi.mocked(listPRDevelopmentCases)
const mockedGet = vi.mocked(getPRDevelopmentCase)
const mockedChat = vi.mocked(chatAboutPRDevelopmentCase)
const mockedRepairRequestID = vi.mocked(createPRDevelopmentRepairRequestID)
const mockedStartRepair = vi.mocked(startPRDevelopmentRepair)
const mockedGetAttention = vi.mocked(getPRDevelopmentAttention)
const mockedRespondToAttention = vi.mocked(respondToPRDevelopmentAttention)
const caseID = `pdc_${"1".repeat(32)}`
const replayCaseID = `pdc_${"2".repeat(32)}`
const baseSummary = {
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
const summary = {
  ...baseSummary,
  attention_required: false,
} as const
const feedback =
  'Fix this <a href="https://evil.test">steal</a>\n<script>alert(1)</script>'
const developmentCase = {
  ...baseSummary,
  base_repository: "octo/repo",
  base_ref: "main",
  base_sha: "b".repeat(40),
  review_commit_sha: "c".repeat(40),
  feedback,
} satisfies PRDevelopmentCase
const detail = {
  case: developmentCase,
  conversation_version: 2,
  messages: [
    {
      id: `pdm_${"3".repeat(32)}`,
      ordinal: 0,
      role: "user",
      content: "What is the reviewer asking for?",
      created_at: "2026-08-05T12:01:00Z",
    },
    {
      id: `pdm_${"4".repeat(32)}`,
      ordinal: 1,
      role: "assistant",
      content: '<img src=x onerror="alert(1)"> Keep this plain.',
      created_at: "2026-08-05T12:01:01Z",
    },
  ],
  repair_available: true,
  repair_revision: 0,
} satisfies PRDevelopmentCaseDetail
const repairInstruction =
  'Apply <script>alert("instruction")</script> safely in the local copy.'
const queuedRepairDetail = {
  ...detail,
  repair_revision: 1,
  repair_session: {
    id: `pds_${"6".repeat(32)}`,
    revision: 1,
    agent_id: "main",
    attempts: [
      {
        id: `pdr_${"7".repeat(32)}`,
        ordinal: 0,
        status: "queued",
        conversation_version: 2,
        instruction: repairInstruction,
        created_at: "2026-08-05T12:03:00Z",
        updated_at: "2026-08-05T12:03:00Z",
      },
    ],
  },
  local_development: {
    attempt_id: `pdr_${"7".repeat(32)}`,
    attempt_ordinal: 0,
    attempt_status: "queued",
    no_changes: false,
    review_status: "not_started",
    review_finding_count: 0,
    local_ready: false,
    updated_at: "2026-08-05T12:03:00Z",
  },
} satisfies PRDevelopmentCaseDetail
const runningRepairDetail = {
  ...queuedRepairDetail,
  repair_revision: 3,
  repair_session: {
    ...queuedRepairDetail.repair_session,
    revision: 3,
    head_repository: "octocat/repo",
    head_ref: "fix/review-feedback",
    head_sha: "d".repeat(40),
    attempts: [
      {
        ...queuedRepairDetail.repair_session.attempts[0],
        status: "running",
        updated_at: "2026-08-05T12:04:00Z",
      },
    ],
  },
  local_development: {
    ...queuedRepairDetail.local_development,
    attempt_status: "running",
    updated_at: "2026-08-05T12:04:00Z",
  },
} satisfies PRDevelopmentCaseDetail
const completedRepairDetail = {
  ...runningRepairDetail,
  repair_revision: 4,
  repair_session: {
    ...runningRepairDetail.repair_session,
    revision: 4,
    attempts: [
      {
        ...runningRepairDetail.repair_session.attempts[0],
        status: "completed",
        summary: '<img src=x onerror="alert(1)"> Local edits are ready.',
        updated_at: "2026-08-05T12:05:00Z",
      },
    ],
  },
  local_development: {
    ...runningRepairDetail.local_development,
    attempt_status: "completed",
    summary: '<img src=x onerror="alert(1)"> Local edits are ready.',
    commit_sha: "e".repeat(40),
    no_changes: false,
    ci_status: "passed",
    ci_plan_digest: "a".repeat(64),
    ci_result_digest: "b".repeat(64),
    review_status: "pending",
    updated_at: "2026-08-05T12:05:00Z",
  },
} satisfies PRDevelopmentCaseDetail
const reviewedRepairDetail = {
  ...completedRepairDetail,
  local_development: {
    ...completedRepairDetail.local_development,
    review_status: "completed",
    review_outcome: "passed",
    review_summary:
      '<script>alert("review")</script> The exact local candidate passed review.',
    review_finding_count: 0,
    local_ready: true,
    // Equal timestamps exercise the evidence-stage tie-breaker used when a
    // durable store clock does not advance between attempt and review writes.
    updated_at: completedRepairDetail.local_development.updated_at,
  },
} satisfies PRDevelopmentCaseDetail
const recoveryRequiredRepairDetail = {
  ...completedRepairDetail,
  repair_revision: 6,
  repair_session: {
    ...completedRepairDetail.repair_session,
    revision: 6,
    attempts: [
      completedRepairDetail.repair_session.attempts[0],
      {
        id: `pdr_${"8".repeat(32)}`,
        ordinal: 1,
        status: "recovery_required",
        conversation_version: 2,
        instruction: "Continue after inspecting the partial edits.",
        summary: "The process stopped after writing one local file.",
        error_code: "recovery_required",
        created_at: "2026-08-05T12:06:00Z",
        updated_at: "2026-08-05T12:07:00Z",
      },
    ],
  },
  local_development: {
    attempt_id: `pdr_${"8".repeat(32)}`,
    attempt_ordinal: 1,
    attempt_status: "recovery_required",
    summary: "The process stopped after writing one local file.",
    no_changes: false,
    review_status: "not_started",
    review_finding_count: 0,
    local_ready: false,
    updated_at: "2026-08-05T12:07:00Z",
  },
} satisfies PRDevelopmentCaseDetail
const replaySummary = { ...summary, id: replayCaseID } as const
const attentionResponseToken = `sha256:${"e".repeat(64)}`

function waitingAttention(): PRDevelopmentAttentionProjection {
  return {
    case_version: 2,
    status: "waiting",
    can_respond: true,
    turns: [
      {
        status: "waiting",
        title: "Choose how to address the review",
        questions: parseExactJSON(
          '{"gate_id":"owner_input","reason":"The repair direction is ambiguous.","questions":["Preserve compatibility?"]}',
        ),
        response_token: attentionResponseToken,
      },
    ],
  }
}

describe("PR development page", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: query === "(min-width: 1024px)",
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    })
    Object.defineProperty(window, "requestAnimationFrame", {
      configurable: true,
      value: (callback: FrameRequestCallback) => {
        callback(0)
        return 1
      },
    })
    Object.defineProperty(window, "cancelAnimationFrame", {
      configurable: true,
      value: vi.fn(),
    })
  })

  beforeEach(() => {
    setWideLayoutForTest(true)
    mockedList.mockReset()
    mockedGet.mockReset()
    mockedChat.mockReset()
    mockedRepairRequestID.mockReset()
    mockedStartRepair.mockReset()
    mockedGetAttention.mockReset()
    mockedRespondToAttention.mockReset()
    mockedRepairRequestID.mockReturnValue(`prq_${"5".repeat(32)}`)
    mockedList.mockResolvedValue({
      cases: [summary, summary, replaySummary],
    })
    mockedGet.mockResolvedValue(detail)
    mockedGetAttention.mockResolvedValue({
      case_version: 2,
      status: "none",
      can_respond: false,
      turns: [],
    })
  })

  it("opens a case-owned attention handoff in chat and sends one fenced reply", async () => {
    mockedGetAttention.mockResolvedValue(waitingAttention())
    mockedRespondToAttention.mockResolvedValue({
      case_version: 2,
      status: "completed",
      can_respond: false,
      turns: [
        {
          status: "answered",
          title: "Choose how to address the review",
          questions: parseExactJSON('["Preserve compatibility?"]'),
          response: "Preserve it.",
        },
      ],
    })
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    await waitFor(() => expect(response).toHaveFocus())
    expect(screen.getByText("Gate owner_input")).toBeVisible()
    expect(document.body).not.toHaveTextContent(attentionResponseToken)

    await user.type(response, " Preserve it. ")
    await user.click(
      screen.getByRole("button", { name: "Send attention reply" }),
    )

    await waitFor(() => {
      expect(mockedRespondToAttention).toHaveBeenCalledWith(
        caseID,
        2,
        attentionResponseToken,
        "Preserve it.",
      )
    })
    expect(
      await screen.findByText("The attention conversation is complete."),
    ).toBeVisible()
  })

  it("reloads an attention conflict without losing the reply draft", async () => {
    mockedGetAttention.mockResolvedValue(waitingAttention())
    mockedRespondToAttention.mockRejectedValue(
      new PRDevelopmentAttentionAPIError(
        "pr_development_attention_conflict",
        409,
      ),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    await user.type(response, "Keep my direction")
    await user.click(
      screen.getByRole("button", { name: "Send attention reply" }),
    )

    expect(await screen.findByText(/attention request changed/i)).toBeVisible()
    expect(response).toHaveValue("Keep my direction")
    await waitFor(() => expect(mockedGetAttention).toHaveBeenCalledTimes(2))
  })

  it("renders captured feedback and advisory conversation as plain text", async () => {
    renderPage({ view: "development", case: caseID })

    const feedbackRegion = await screen.findByTestId("pr-development-feedback")
    expect(feedbackRegion.textContent).toBe(feedback)
    expect(feedbackRegion.querySelector("a")).toBeNull()
    expect(feedbackRegion.querySelector("script")).toBeNull()
    expect(
      screen.queryByRole("link", { name: "steal" }),
    ).not.toBeInTheDocument()
    const assistantMessage = screen.getByTestId("pr-development-message-1")
    expect(assistantMessage.textContent).toBe(
      '<img src=x onerror="alert(1)"> Keep this plain.',
    )
    expect(assistantMessage.querySelector("img")).toBeNull()
    const conversationLog = screen.getByRole("log", {
      name: "Development conversation",
    })
    expect(conversationLog).toHaveAttribute("aria-live", "polite")
    expect(conversationLog).toHaveAttribute("aria-relevant", "additions text")

    expect(screen.getByRole("link", { name: /Open PR/ })).toHaveAttribute(
      "href",
      summary.pull_url,
    )
    expect(screen.getByRole("link", { name: /Open review/ })).toHaveAttribute(
      "href",
      summary.review_url,
    )
    expect(screen.getByText(/This PR uses a fork/)).toBeVisible()
    expect(
      screen.getAllByRole("button", { name: /octo\/repo #42/ }),
    ).toHaveLength(2)
    expect(screen.getAllByText("At capture")).not.toHaveLength(0)
    expect(screen.getAllByText(/Head at capture:/)).toHaveLength(2)
    expect(screen.getByText(/cannot inspect a checkout/i)).toBeVisible()
    expect(screen.queryByRole("button", { name: /checkout|push/i })).toBeNull()
  })

  it("confirms and starts one explicit local repair with a fenced request", async () => {
    mockedStartRepair.mockResolvedValue(queuedRepairDetail)
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, `  ${repairInstruction}  `)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))

    expect(mockedStartRepair).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      /run discovered local checks, record a commit when files changed, and review the result/i,
    )
    await user.click(screen.getByRole("button", { name: "Start local repair" }))

    await waitFor(() =>
      expect(mockedStartRepair).toHaveBeenCalledWith(caseID, {
        expectedConversationVersion: 2,
        expectedRepairRevision: 0,
        expectedAttemptOrdinal: 0,
        requestID: `prq_${"5".repeat(32)}`,
        instruction: repairInstruction,
      }),
    )
    expect(mockedRepairRequestID).toHaveBeenCalledTimes(1)
    expect((await screen.findAllByText("Queued"))[0]).toBeVisible()
    const renderedInstruction = screen.getByText(repairInstruction)
    expect(renderedInstruction.querySelector("script")).toBeNull()
    expect(instruction).toHaveValue("")
    expect(instruction).toBeDisabled()
  })

  it("blocks a local repair instruction larger than 4 KiB", async () => {
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    fireEvent.change(instruction, {
      target: {
        value: "x".repeat(MAXIMUM_PR_DEVELOPMENT_REPAIR_INSTRUCTION_BYTES + 1),
      },
    })

    expect(
      screen.getByText("The instruction must be at most 4 KiB."),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Start local repair" }),
    ).toBeDisabled()
  })

  it("reuses the same opaque request ID when an ambiguous start is retried", async () => {
    mockedStartRepair
      .mockRejectedValueOnce(new Error("repair response was interrupted"))
      .mockResolvedValueOnce(queuedRepairDetail)
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    expect(
      await screen.findByText("repair response was interrupted"),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Retry start" }))

    await waitFor(() => expect(mockedStartRepair).toHaveBeenCalledTimes(2))
    expect(mockedStartRepair.mock.calls[1]).toEqual(
      mockedStartRepair.mock.calls[0],
    )
    expect(mockedRepairRequestID).toHaveBeenCalledTimes(1)
  })

  it("preserves the draft and retry identity at full repair-history capacity", async () => {
    const capacityDetail = {
      ...completedRepairDetail,
      repair_revision: 64,
      repair_session: {
        ...completedRepairDetail.repair_session,
        revision: 64,
        attempts: Array.from({ length: 64 }, (_, ordinal) => ({
          ...completedRepairDetail.repair_session.attempts[0],
          id: `pdr_${ordinal.toString(16).padStart(32, "0")}`,
          ordinal,
          status: "completed" as const,
          instruction: `Completed local repair ${ordinal + 1}.`,
          summary: `Local repair ${ordinal + 1} completed.`,
        })),
      },
    } satisfies PRDevelopmentCaseDetail
    mockedGet.mockResolvedValue(capacityDetail)
    mockedStartRepair.mockRejectedValue(
      new PRDevelopmentAPIError(
        "repair attempt capacity reached",
        409,
        capacityDetail,
      ),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))

    expect(
      await screen.findByText("repair attempt capacity reached"),
    ).toBeVisible()
    expect(instruction).toHaveValue(repairInstruction)
    expect(mockedStartRepair).toHaveBeenCalledWith(caseID, {
      expectedConversationVersion: 2,
      expectedRepairRevision: 64,
      expectedAttemptOrdinal: 64,
      requestID: `prq_${"5".repeat(32)}`,
      instruction: repairInstruction,
    })

    await user.click(screen.getByRole("button", { name: "Retry start" }))
    await waitFor(() => expect(mockedStartRepair).toHaveBeenCalledTimes(2))
    expect(mockedStartRepair.mock.calls[1]).toEqual(
      mockedStartRepair.mock.calls[0],
    )
    expect(mockedRepairRequestID).toHaveBeenCalledTimes(1)
  })

  it("preserves a newer ambiguous intent when older identical work precedes another-tab work", async () => {
    const otherTabDetail: PRDevelopmentCaseDetail = {
      ...completedRepairDetail,
      repair_revision: 6,
      repair_session: {
        ...completedRepairDetail.repair_session,
        revision: 6,
        attempts: [
          completedRepairDetail.repair_session.attempts[0],
          {
            id: `pdr_${"9".repeat(32)}`,
            ordinal: 1,
            status: "completed",
            conversation_version: 2,
            instruction: "An unrelated repair from another tab.",
            summary: "The unrelated local repair completed.",
            created_at: "2026-08-05T12:06:00Z",
            updated_at: "2026-08-05T12:07:00Z",
          },
        ],
      },
    }
    mockedGet.mockResolvedValue(completedRepairDetail)
    mockedStartRepair
      .mockRejectedValueOnce(new Error("repair response was interrupted"))
      .mockRejectedValueOnce(new Error("repair remains conflicted"))
    const client = newTestQueryClient()
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    expect(
      await screen.findByText("repair response was interrupted"),
    ).toBeVisible()
    expect(mockedStartRepair.mock.calls[0]?.[1]).toMatchObject({
      expectedRepairRevision: 4,
      expectedAttemptOrdinal: 1,
    })

    client.setQueryData(
      ["pr-development", "detail", caseID] as const,
      otherTabDetail,
    )
    expect(
      await screen.findByText(/Repair history changed in another tab/i),
    ).toBeVisible()
    expect(instruction).toHaveValue(repairInstruction)
    await user.click(screen.getByRole("button", { name: "Retry start" }))

    await waitFor(() => expect(mockedStartRepair).toHaveBeenCalledTimes(2))
    expect(mockedStartRepair.mock.calls[1]).toEqual(
      mockedStartRepair.mock.calls[0],
    )
    expect(mockedRepairRequestID).toHaveBeenCalledTimes(1)
  })

  it("reconciles an ambiguous start only when the exact expected ordinal appears", async () => {
    const exactCommittedDetail: PRDevelopmentCaseDetail = {
      ...completedRepairDetail,
      repair_revision: 5,
      repair_session: {
        ...completedRepairDetail.repair_session,
        revision: 5,
        attempts: [
          completedRepairDetail.repair_session.attempts[0],
          {
            id: `pdr_${"9".repeat(32)}`,
            ordinal: 1,
            status: "queued",
            conversation_version: 2,
            instruction: repairInstruction,
            created_at: "2026-08-05T12:06:00Z",
            updated_at: "2026-08-05T12:06:00Z",
          },
        ],
      },
    }
    mockedGet.mockResolvedValue(completedRepairDetail)
    mockedStartRepair.mockRejectedValueOnce(
      new Error("repair response was interrupted"),
    )
    const client = newTestQueryClient()
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    expect(
      await screen.findByText("repair response was interrupted"),
    ).toBeVisible()

    client.setQueryData(
      ["pr-development", "detail", caseID] as const,
      exactCommittedDetail,
    )

    await waitFor(() => expect(instruction).toHaveValue(""))
    expect(screen.queryByRole("button", { name: "Retry start" })).toBeNull()
    expect(mockedStartRepair).toHaveBeenCalledTimes(1)
    expect(mockedRepairRequestID).toHaveBeenCalledTimes(1)
  })

  it("lets mutation detail disable repair and blocks an unavailable retry", async () => {
    const unavailableDetail: PRDevelopmentCaseDetail = {
      ...detail,
      repair_available: false,
      repair_unavailable_reason: "runtime_unavailable",
    }
    mockedStartRepair.mockRejectedValue(
      new PRDevelopmentAPIError(
        "development workbench unavailable",
        503,
        unavailableDetail,
      ),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))

    expect(
      await screen.findByText("development workbench unavailable"),
    ).toBeVisible()
    expect(
      screen.getByText(/required repair dependencies are unavailable/i),
    ).toBeVisible()
    expect(instruction).toBeDisabled()
    expect(screen.getByRole("button", { name: "Retry start" })).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Start local repair" }),
    ).toBeDisabled()
  })

  it("preserves independent local repair drafts for each selected case", async () => {
    const replayDevelopmentCase: PRDevelopmentCase = {
      ...developmentCase,
      id: replayCaseID,
    }
    const replayDetail: PRDevelopmentCaseDetail = {
      ...detail,
      case: replayDevelopmentCase,
    }
    mockedGet.mockImplementation(async (id) =>
      id === replayCaseID ? replayDetail : detail,
    )
    const client = newTestQueryClient()
    function RepairDraftHarness() {
      const [selectedCase, setSelectedCase] = useState(caseID)
      return (
        <>
          <button type="button" onClick={() => setSelectedCase(caseID)}>
            Select first repair
          </button>
          <button type="button" onClick={() => setSelectedCase(replayCaseID)}>
            Select replay repair
          </button>
          <PRDevelopmentPage
            search={{ view: "development", case: selectedCase }}
            onSearchChange={(next) => {
              if (next.case) setSelectedCase(next.case)
            }}
          />
        </>
      )
    }
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <SidebarProvider>
          <RepairDraftHarness />
        </SidebarProvider>
      </QueryClientProvider>,
    )

    let instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, "First-case local repair draft.")
    await user.click(
      screen.getByRole("button", { name: "Select replay repair" }),
    )
    instruction = await screen.findByLabelText("Local repair instruction")
    expect(instruction).toHaveValue("")
    await user.type(instruction, "Replay-case local repair draft.")

    await user.click(
      screen.getByRole("button", { name: "Select first repair" }),
    )
    expect(
      await screen.findByLabelText("Local repair instruction"),
    ).toHaveValue("First-case local repair draft.")
    await user.click(
      screen.getByRole("button", { name: "Select replay repair" }),
    )
    expect(
      await screen.findByLabelText("Local repair instruction"),
    ).toHaveValue("Replay-case local repair draft.")
  })

  it("polls through local repair and pending AI review without losing evidence", async () => {
    mockedGet
      .mockResolvedValueOnce(runningRepairDetail)
      .mockResolvedValueOnce(completedRepairDetail)
      .mockResolvedValueOnce(reviewedRepairDetail)
    renderPage({ view: "development", case: caseID })

    const status = await screen.findByRole("status")
    expect(status).toHaveAttribute("aria-live", "polite")
    expect(status).toHaveTextContent(/AI is editing the pinned local copy/i)
    expect(screen.getByLabelText("Local repair instruction")).toBeDisabled()

    await waitFor(() => expect(status).toHaveTextContent(/Ready locally/i), {
      timeout: 6000,
    })
    expect(mockedGet).toHaveBeenCalledTimes(3)
    const summary = screen.getByText(
      '<img src=x onerror="alert(1)"> Local edits are ready.',
    )
    expect(summary.querySelector("img")).toBeNull()
    expect(screen.getByText("Outcome summary")).toBeVisible()
  })

  it("does not poll a legacy completed attempt without bound review evidence", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"],
    })
    try {
      const legacyCompletedDetail: PRDevelopmentCaseDetail = {
        ...completedRepairDetail,
        local_development: {
          attempt_id: `pdr_${"7".repeat(32)}`,
          attempt_ordinal: 0,
          attempt_status: "completed",
          summary: '<img src=x onerror="alert(1)"> Local edits are ready.',
          no_changes: false,
          review_status: "not_started",
          review_finding_count: 0,
          local_ready: false,
          updated_at: "2026-08-05T12:05:00Z",
        },
      }
      mockedGet.mockResolvedValue(legacyCompletedDetail)

      renderPage({ view: "development", case: caseID })
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1)
      })

      expect(mockedGet).toHaveBeenCalledTimes(1)
      const status = screen.getByTestId("local-development-status")
      expect(status).toHaveTextContent(/Local commit\s*Evidence unavailable/)
      expect(status).toHaveTextContent(/Local CI\s*Evidence unavailable/)
      expect(status).toHaveTextContent(/AI review\s*Not started/)
      expect(status).toHaveTextContent(
        /bound commit, CI, and AI-review evidence is not available/i,
      )

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6_000)
      })
      expect(mockedGet).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it("shows exact local commit, CI, and AI-review evidence without implying push", async () => {
    mockedGet.mockResolvedValue(reviewedRepairDetail)
    renderPage({ view: "development", case: caseID })

    const status = await screen.findByTestId("local-development-status")
    expect(status).toHaveTextContent("Local development status")
    expect(status).toHaveTextContent("Ready locally")
    expect(status).toHaveTextContent("Attempt 1")
    expect(status).toHaveTextContent(/Recorded eeeeeeee/)
    expect(status).toHaveTextContent(/Local CI\s*Passed/)
    expect(status).toHaveTextContent(/AI review\s*Passed/)
    expect(status).toHaveTextContent(/plan aaaaaaaa · result bbbbbbbb/)
    expect(status).toHaveTextContent(/does not mean changes were pushed/i)
    const reviewSummary = screen.getByText(
      '<script>alert("review")</script> The exact local candidate passed review.',
    )
    expect(reviewSummary.querySelector("script")).toBeNull()
  })

  it("shows no-change and attention outcomes truthfully", async () => {
    mockedGet.mockResolvedValue({
      ...reviewedRepairDetail,
      local_development: {
        ...reviewedRepairDetail.local_development,
        no_changes: true,
        review_outcome: "attention_required",
        review_summary: "Please choose whether compatibility should be kept.",
        local_ready: false,
      },
    })
    renderPage({ view: "development", case: caseID })

    const status = await screen.findByTestId("local-development-status")
    expect(status).toHaveTextContent(/No file changes · retained eeeeeeee/)
    expect(status).toHaveTextContent("Needs your attention")
    expect(status).toHaveTextContent(/needs your direction in the PR chat/i)
  })

  it("describes preparing without claiming that the workspace is pinned", async () => {
    const preparingDetail: PRDevelopmentCaseDetail = {
      ...queuedRepairDetail,
      repair_revision: 2,
      repair_session: {
        ...queuedRepairDetail.repair_session,
        revision: 2,
        attempts: [
          {
            ...queuedRepairDetail.repair_session.attempts[0],
            status: "preparing",
            updated_at: "2026-08-05T12:03:30Z",
          },
        ],
      },
      local_development: {
        ...queuedRepairDetail.local_development,
        attempt_status: "preparing",
        updated_at: "2026-08-05T12:03:30Z",
      },
    }
    mockedGet.mockResolvedValue(preparingDetail)
    renderPage({ view: "development", case: caseID })

    expect(await screen.findByRole("status")).toHaveTextContent(
      /Current PR state is being verified before local editing starts/i,
    )
    expect(
      screen.queryByText(/pinned local copy is being prepared/i),
    ).toBeNull()
  })

  it("keeps unavailable repair history visible in newest-first order", async () => {
    const unavailableHistory: PRDevelopmentCaseDetail = {
      ...recoveryRequiredRepairDetail,
      repair_available: false,
      repair_unavailable_reason: "runtime_unavailable",
    }
    mockedGet.mockResolvedValue(unavailableHistory)
    renderPage({ view: "development", case: caseID })

    expect(
      await screen.findByText(/required repair dependencies are unavailable/i),
    ).toBeVisible()
    expect(screen.getByLabelText("Local repair instruction")).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Continue local repair" }),
    ).toBeDisabled()
    const attempts = within(
      screen.getByRole("list", { name: "Repair history" }),
    ).getAllByText(/Attempt [12]/)
    expect(attempts.map((node) => node.textContent)).toEqual([
      "Attempt 2",
      "Attempt 1",
    ])
    expect(screen.getByRole("status")).toHaveTextContent(
      /Partial local edits may already exist/i,
    )
    expect(
      screen.getByText(/inspect and preserve them before continuing/i),
    ).toBeVisible()
  })

  it("warns how to preserve partial edits before continuing recovery", async () => {
    mockedGet.mockResolvedValue(recoveryRequiredRepairDetail)
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    expect(instruction).toBeEnabled()
    expect(
      screen.getByText(/Tell AI to inspect and preserve them/i),
    ).toBeVisible()
    await user.type(
      instruction,
      "Inspect and preserve the partial edits, then finish the repair.",
    )
    await user.click(
      screen.getByRole("button", { name: "Continue local repair" }),
    )

    expect(mockedStartRepair).not.toHaveBeenCalled()
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      /Partial edits may exist in the same pinned local workspace/i,
    )
    expect(screen.getByRole("alertdialog")).toHaveTextContent(
      /inspect and preserve them before continuing/i,
    )
  })

  it("merges conversation and repair revisions without regressing either", async () => {
    const client = newTestQueryClient()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)
    await screen.findByLabelText("Local repair instruction")
    const queryKey = ["pr-development", "detail", caseID] as const

    client.setQueryData(queryKey, queuedRepairDetail)
    const newerConversation: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 3,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"9".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Keep this newer conversation turn.",
          created_at: "2026-08-05T12:08:00Z",
        },
      ],
    }
    client.setQueryData(queryKey, newerConversation)
    client.setQueryData(queryKey, {
      ...runningRepairDetail,
      conversation_version: 2,
      messages: detail.messages,
    })

    await waitFor(() => {
      const merged = client.getQueryData<PRDevelopmentCaseDetail>(queryKey)
      expect(merged?.conversation_version).toBe(3)
      expect(merged?.messages.at(-1)?.content).toBe(
        "Keep this newer conversation turn.",
      )
      expect(merged?.repair_revision).toBe(3)
      expect(merged?.repair_session?.attempts.at(-1)?.status).toBe("running")
    })
  })

  it("does not regress completed local review evidence with a later timestamp", async () => {
    const client = newTestQueryClient()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)
    await screen.findByLabelText("Local repair instruction")
    const queryKey = ["pr-development", "detail", caseID] as const

    client.setQueryData(queryKey, reviewedRepairDetail)
    client.setQueryData(queryKey, {
      ...completedRepairDetail,
      local_development: {
        ...completedRepairDetail.local_development,
        updated_at: "2026-08-05T12:06:00Z",
      },
    })

    await waitFor(() => {
      const cached = client.getQueryData<PRDevelopmentCaseDetail>(queryKey)
      expect(cached?.local_development).toEqual(
        reviewedRepairDetail.local_development,
      )
    })
    expect(screen.getByTestId("local-development-status")).toHaveTextContent(
      "Ready locally",
    )
  })

  it("takes repair capability from an authoritative equal-version reload", async () => {
    const unavailableDetail: PRDevelopmentCaseDetail = {
      ...detail,
      repair_available: false,
      repair_unavailable_reason: "runtime_unavailable",
    }
    mockedGet
      .mockResolvedValueOnce(detail)
      .mockResolvedValueOnce(unavailableDetail)
      .mockResolvedValueOnce(detail)
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    expect(instruction).toBeEnabled()
    await user.click(screen.getByRole("button", { name: "Reload status" }))
    expect(
      await screen.findByText(/required repair dependencies are unavailable/i),
    ).toBeVisible()
    expect(instruction).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Reload status" }))
    await waitFor(() =>
      expect(
        screen.queryByText(/required repair dependencies are unavailable/i),
      ).toBeNull(),
    )
    expect(instruction).toBeEnabled()
  })

  it("does not let a stale chat response restore repair capability", async () => {
    const unavailableDetail: PRDevelopmentCaseDetail = {
      ...detail,
      repair_available: false,
      repair_unavailable_reason: "runtime_unavailable",
    }
    const chatDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"5".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Keep the fresh capability.",
          created_at: "2026-08-05T12:08:00Z",
        },
        {
          id: `pdm_${"6".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "Capability stayed authoritative.",
          created_at: "2026-08-05T12:08:01Z",
        },
      ],
      repair_available: true,
    }
    let resolveChat: ((value: PRDevelopmentCaseDetail) => void) | undefined
    mockedChat.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveChat = resolve
        }),
    )
    const client = newTestQueryClient()
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Keep the fresh capability.")
    await user.click(screen.getByRole("button", { name: "Send" }))
    await waitFor(() => expect(mockedChat).toHaveBeenCalledTimes(1))

    const queryKey = ["pr-development", "detail", caseID] as const
    client.setQueryData(queryKey, unavailableDetail)
    resolveChat?.(chatDetail)

    await waitFor(() => {
      const cached = client.getQueryData<PRDevelopmentCaseDetail>(queryKey)
      expect(cached?.conversation_version).toBe(4)
      expect(cached?.repair_available).toBe(false)
      expect(cached?.repair_unavailable_reason).toBe("runtime_unavailable")
    })
    expect(
      await screen.findByText(/required repair dependencies are unavailable/i),
    ).toBeVisible()
  })

  it("sends one version-fenced message and updates the conversation cache", async () => {
    const updatedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"5".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Suggest a safe plan.",
          created_at: "2026-08-05T12:02:00Z",
        },
        {
          id: `pdm_${"6".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "Start by reproducing the retry failure locally.",
          created_at: "2026-08-05T12:02:01Z",
        },
      ],
      repair_available: true,
      repair_revision: 0,
    }
    mockedChat.mockResolvedValue(updatedDetail)
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "  Suggest a safe plan.  ")
    await user.click(screen.getByRole("button", { name: "Send" }))

    await waitFor(() =>
      expect(mockedChat).toHaveBeenCalledWith(
        caseID,
        2,
        "Suggest a safe plan.",
      ),
    )
    expect(
      await screen.findByText(
        "Start by reproducing the retry failure locally.",
      ),
    ).toBeVisible()
    expect(composer).toHaveValue("")
  })

  it("does not let a delayed reload overwrite a newer chat result", async () => {
    const updatedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"5".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Keep the newer turn.",
          created_at: "2026-08-05T12:02:00Z",
        },
        {
          id: `pdm_${"6".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "Newer answer survives.",
          created_at: "2026-08-05T12:02:01Z",
        },
      ],
    }
    let resolveReload: ((value: PRDevelopmentCaseDetail) => void) | undefined
    mockedGet.mockResolvedValueOnce(detail).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveReload = resolve
        }),
    )
    mockedChat.mockResolvedValue(updatedDetail)
    const client = newTestQueryClient()
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID }, vi.fn(), client)

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.click(
      screen.getByRole("button", { name: "Reload captured feedback" }),
    )
    await waitFor(() => expect(mockedGet).toHaveBeenCalledTimes(2))
    await user.type(composer, "Keep the newer turn.")
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(await screen.findByText("Newer answer survives.")).toBeVisible()

    resolveReload?.(detail)
    await waitFor(() =>
      expect(client.getQueryData(["pr-development", "detail", caseID])).toEqual(
        updatedDetail,
      ),
    )
    expect(screen.getByText("Newer answer survives.")).toBeVisible()
  })

  it("locks both chat and repair while the advisory response is pending", async () => {
    let resolveChat: ((detail: PRDevelopmentCaseDetail) => void) | undefined
    mockedChat.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveChat = resolve
        }),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    const instruction = screen.getByLabelText("Local repair instruction")
    await user.type(instruction, "Keep this repair draft while chat runs.")
    await user.type(composer, "Explain this.")
    expect(
      screen.getByRole("button", { name: "Start local repair" }),
    ).toBeEnabled()
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled()
    expect(composer).toBeDisabled()
    expect(instruction).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Start local repair" }),
    ).toBeDisabled()
    expect(mockedChat).toHaveBeenCalledTimes(1)

    resolveChat?.(detail)
    await waitFor(() => expect(composer).toBeEnabled())
    expect(instruction).toBeEnabled()
    expect(
      screen.getByRole("button", { name: "Start local repair" }),
    ).toBeEnabled()
    expect(screen.queryByRole("button", { name: "Sending…" })).toBeNull()
    expect(mockedChat).toHaveBeenCalledTimes(1)
  })

  it("locks both repair and chat while a local repair start is pending", async () => {
    let resolveRepair: ((detail: PRDevelopmentCaseDetail) => void) | undefined
    mockedStartRepair.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRepair = resolve
        }),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const instruction = await screen.findByLabelText("Local repair instruction")
    const composer = screen.getByLabelText("Message AI about this feedback")
    await user.type(instruction, repairInstruction)
    await user.type(composer, "Keep this chat draft while repair starts.")
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))

    await waitFor(() => expect(mockedStartRepair).toHaveBeenCalledTimes(1))
    expect(instruction).toBeDisabled()
    expect(composer).toBeDisabled()
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled()

    resolveRepair?.(queuedRepairDetail)
    await waitFor(() => expect(composer).toBeEnabled())
    expect(composer).toHaveValue("Keep this chat draft while repair starts.")
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled()
  })

  it("keeps a same-case local repair locked when navigation remounts it", async () => {
    const replayDevelopmentCase: PRDevelopmentCase = {
      ...developmentCase,
      id: replayCaseID,
    }
    const replayDetail: PRDevelopmentCaseDetail = {
      case: replayDevelopmentCase,
      conversation_version: 0,
      messages: [],
      repair_available: true,
      repair_revision: 0,
    }
    mockedGet.mockImplementation(async (id) =>
      id === replayCaseID ? replayDetail : detail,
    )
    let resolveRepair: ((value: PRDevelopmentCaseDetail) => void) | undefined
    mockedStartRepair.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveRepair = resolve
        }),
    )
    const client = newTestQueryClient()
    const page = (selectedCase: string) => (
      <QueryClientProvider client={client}>
        <SidebarProvider>
          <PRDevelopmentPage
            search={{ view: "development", case: selectedCase }}
            onSearchChange={vi.fn()}
          />
        </SidebarProvider>
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const rendered = render(page(caseID))

    const instruction = await screen.findByLabelText("Local repair instruction")
    await user.type(instruction, repairInstruction)
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await user.click(screen.getByRole("button", { name: "Start local repair" }))
    await waitFor(() => expect(mockedStartRepair).toHaveBeenCalledTimes(1))

    rendered.rerender(page(replayCaseID))
    await screen.findByLabelText("Local repair instruction")
    rendered.rerender(page(caseID))

    const remountedInstruction = await screen.findByLabelText(
      "Local repair instruction",
    )
    expect(remountedInstruction).toHaveValue(repairInstruction)
    expect(remountedInstruction).toBeDisabled()
    expect(screen.getByRole("button", { name: "Starting…" })).toBeDisabled()
    expect(mockedStartRepair).toHaveBeenCalledTimes(1)

    resolveRepair?.(queuedRepairDetail)
    await waitFor(() =>
      expect(screen.getAllByText("Queued").length).toBeGreaterThan(0),
    )
    expect(mockedStartRepair).toHaveBeenCalledTimes(1)
  })

  it("keeps a same-case advisory chat locked when navigation remounts it", async () => {
    const replayDevelopmentCase: PRDevelopmentCase = {
      ...developmentCase,
      id: replayCaseID,
    }
    const replayDetail: PRDevelopmentCaseDetail = {
      case: replayDevelopmentCase,
      conversation_version: 0,
      messages: [],
      repair_available: true,
      repair_revision: 0,
    }
    const updatedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"a".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Keep this request singular.",
          created_at: "2026-08-05T12:02:00Z",
        },
        {
          id: `pdm_${"b".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "Only one advisory request ran.",
          created_at: "2026-08-05T12:02:01Z",
        },
      ],
    }
    mockedGet.mockImplementation(async (id) =>
      id === replayCaseID ? replayDetail : detail,
    )
    let resolveChat: ((value: PRDevelopmentCaseDetail) => void) | undefined
    mockedChat.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveChat = resolve
        }),
    )
    const client = newTestQueryClient()
    const page = (selectedCase: string) => (
      <QueryClientProvider client={client}>
        <SidebarProvider>
          <PRDevelopmentPage
            search={{ view: "development", case: selectedCase }}
            onSearchChange={vi.fn()}
          />
        </SidebarProvider>
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const rendered = render(page(caseID))

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Keep this request singular.")
    await user.click(screen.getByRole("button", { name: "Send" }))
    await waitFor(() => expect(mockedChat).toHaveBeenCalledTimes(1))

    rendered.rerender(page(replayCaseID))
    await screen.findByLabelText("Message AI about this feedback")
    rendered.rerender(page(caseID))

    const remountedComposer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    expect(remountedComposer).toBeDisabled()
    expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled()
    expect(
      screen.getByRole("log", { name: "Development conversation" }),
    ).toHaveAttribute("aria-busy", "true")
    expect(mockedChat).toHaveBeenCalledTimes(1)

    resolveChat?.(updatedDetail)
    expect(
      await screen.findByText("Only one advisory request ran."),
    ).toBeVisible()
    await waitFor(() => expect(remountedComposer).toBeEnabled())
    expect(mockedChat).toHaveBeenCalledTimes(1)
  })

  it("isolates drafts and pending mutation results when the selected case changes", async () => {
    const replayDevelopmentCase: PRDevelopmentCase = {
      ...developmentCase,
      id: replayCaseID,
    }
    const replayDetail: PRDevelopmentCaseDetail = {
      case: replayDevelopmentCase,
      conversation_version: 0,
      messages: [],
      repair_available: true,
      repair_revision: 0,
    }
    const firstUpdatedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"a".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Draft for the first case.",
          created_at: "2026-08-05T12:02:00Z",
        },
        {
          id: `pdm_${"b".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "First-case answer.",
          created_at: "2026-08-05T12:02:01Z",
        },
      ],
    }
    const replayUpdatedDetail: PRDevelopmentCaseDetail = {
      ...replayDetail,
      conversation_version: 2,
      messages: [
        {
          id: `pdm_${"c".repeat(32)}`,
          ordinal: 0,
          role: "user",
          content: "Draft for the replay case.",
          created_at: "2026-08-05T12:03:00Z",
        },
        {
          id: `pdm_${"d".repeat(32)}`,
          ordinal: 1,
          role: "assistant",
          content: "Replay-case answer.",
          created_at: "2026-08-05T12:03:01Z",
        },
      ],
    }
    mockedGet.mockImplementation(async (id) =>
      id === replayCaseID ? replayDetail : detail,
    )
    let resolveFirst: ((value: PRDevelopmentCaseDetail) => void) | undefined
    mockedChat
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValueOnce(replayUpdatedDetail)
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    const page = (selectedCase: string) => (
      <QueryClientProvider client={client}>
        <SidebarProvider>
          <PRDevelopmentPage
            search={{ view: "development", case: selectedCase }}
            onSearchChange={vi.fn()}
          />
        </SidebarProvider>
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const rendered = render(page(caseID))

    const firstComposer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(firstComposer, "Draft for the first case.")
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled()

    rendered.rerender(page(replayCaseID))
    const replayComposer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    expect(replayComposer).toHaveValue("")
    expect(replayComposer).toBeEnabled()
    await user.type(replayComposer, "Draft for the replay case.")

    resolveFirst?.(firstUpdatedDetail)
    await waitFor(() =>
      expect(client.getQueryData(["pr-development", "detail", caseID])).toEqual(
        firstUpdatedDetail,
      ),
    )
    expect(replayComposer).toHaveValue("Draft for the replay case.")
    expect(screen.queryByText("First-case answer.")).toBeNull()

    await user.click(screen.getByRole("button", { name: "Send" }))
    await waitFor(() =>
      expect(mockedChat).toHaveBeenLastCalledWith(
        replayCaseID,
        0,
        "Draft for the replay case.",
      ),
    )
    expect(await screen.findByText("Replay-case answer.")).toBeVisible()
  })

  it("keeps a dirty draft mounted when a background detail reload fails", async () => {
    mockedGet
      .mockResolvedValueOnce(detail)
      .mockRejectedValueOnce(new Error("Conversation reload failed."))
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Keep this draft during reload.")
    await user.click(
      screen.getByRole("button", { name: "Reload captured feedback" }),
    )

    expect(await screen.findByText("Conversation reload failed.")).toBeVisible()
    expect(composer).toBeVisible()
    expect(composer).toHaveValue("Keep this draft during reload.")
    expect(screen.getByTestId("pr-development-feedback")).toBeVisible()
  })

  it("moves mobile focus into detail and restores it to the selected case", async () => {
    setWideLayoutForTest(false)
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    function MobileHarness() {
      const [selectedCase, setSelectedCase] = useState<string>()
      return (
        <PRDevelopmentPage
          search={{
            view: "development",
            ...(selectedCase ? { case: selectedCase } : {}),
          }}
          onSearchChange={(next) => setSelectedCase(next.case)}
        />
      )
    }
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={client}>
        <SidebarProvider>
          <MobileHarness />
        </SidebarProvider>
      </QueryClientProvider>,
    )

    const selectedButton = (
      await screen.findAllByRole("button", { name: /octo\/repo #42/ })
    )[0]
    await user.click(selectedButton)
    const backButton = await screen.findByRole("button", {
      name: "Back to PR feedback",
    })
    await waitFor(() => expect(backButton).toHaveFocus())

    await user.click(backButton)
    await waitFor(() => expect(selectedButton).toHaveFocus())
  })

  it("adopts a committed human message after model failure without offering a duplicate retry", async () => {
    const committedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 3,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"8".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Explain the model failure.",
          created_at: "2026-08-05T12:02:30Z",
        },
      ],
    }
    mockedChat.mockRejectedValue(
      new PRDevelopmentAPIError(
        "development AI unavailable",
        503,
        committedDetail,
      ),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Explain the model failure.")
    await user.click(screen.getByRole("button", { name: "Send" }))

    expect(await screen.findByText("Explain the model failure.")).toBeVisible()
    expect(composer).toHaveValue("")
    expect(
      screen.getByText(/Your message was saved, but the AI response/i),
    ).toBeVisible()
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
    expect(
      screen.getByRole("button", { name: "Reload conversation" }),
    ).toBeEnabled()
  })

  it("recognizes an ambiguously failed message after an authoritative reload", async () => {
    const committedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 3,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"9".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Was my message saved?",
          created_at: "2026-08-05T12:02:45Z",
        },
      ],
    }
    mockedGet
      .mockResolvedValueOnce(detail)
      .mockResolvedValueOnce(committedDetail)
    mockedChat.mockRejectedValue(
      new PRDevelopmentAPIError("service unavailable", 503),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Was my message saved?")
    await user.click(screen.getByRole("button", { name: "Send" }))

    expect(await screen.findByText("service unavailable")).toBeVisible()
    expect(composer).toHaveValue("Was my message saved?")
    await user.click(
      screen.getByRole("button", { name: "Reload conversation" }),
    )

    expect(await screen.findByText("Was my message saved?")).toBeVisible()
    await waitFor(() => expect(composer).toHaveValue(""))
    expect(
      screen.getByText(/Your message was saved, but the AI response/i),
    ).toBeVisible()
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
  })

  it("adopts a fully completed ambiguous turn without reporting AI failure", async () => {
    const completedDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 4,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"a".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "Did the full turn finish?",
          created_at: "2026-08-05T12:02:45Z",
        },
        {
          id: `pdm_${"b".repeat(32)}`,
          ordinal: 3,
          role: "assistant",
          content: "Yes, the answer was committed too.",
          created_at: "2026-08-05T12:02:46Z",
        },
      ],
    }
    mockedGet
      .mockResolvedValueOnce(detail)
      .mockResolvedValueOnce(completedDetail)
    mockedChat.mockRejectedValue(
      new PRDevelopmentAPIError("service unavailable", 503),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Did the full turn finish?")
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(await screen.findByText("service unavailable")).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Reload conversation" }),
    )

    expect(
      await screen.findByText("Yes, the answer was committed too."),
    ).toBeVisible()
    await waitFor(() => expect(composer).toHaveValue(""))
    expect(screen.queryByText("service unavailable")).toBeNull()
    expect(
      screen.queryByText(/message was saved, but the AI response/i),
    ).toBeNull()
  })

  it("hydrates safe conflict detail and offers retry and refetch without losing the draft", async () => {
    const recoveredDetail: PRDevelopmentCaseDetail = {
      ...detail,
      conversation_version: 3,
      messages: [
        ...detail.messages,
        {
          id: `pdm_${"7".repeat(32)}`,
          ordinal: 2,
          role: "user",
          content: "A message from another tab.",
          created_at: "2026-08-05T12:03:00Z",
        },
      ],
    }
    mockedChat.mockRejectedValue(
      new PRDevelopmentAPIError(
        "conversation changed; reload before retrying",
        409,
        recoveredDetail,
      ),
    )
    const user = userEvent.setup()
    renderPage({ view: "development", case: caseID })

    const composer = await screen.findByLabelText(
      "Message AI about this feedback",
    )
    await user.type(composer, "Keep my draft")
    await user.click(screen.getByRole("button", { name: "Send" }))

    expect(await screen.findByText("A message from another tab.")).toBeVisible()
    expect(composer).toHaveValue("Keep my draft")
    expect(
      screen.getByText("conversation changed; reload before retrying"),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() =>
      expect(mockedChat).toHaveBeenLastCalledWith(caseID, 3, "Keep my draft"),
    )
    await waitFor(() => expect(composer).toHaveFocus())
    await user.click(
      screen.getByRole("button", { name: "Reload conversation" }),
    )
    await waitFor(() => expect(mockedGet).toHaveBeenCalledTimes(2))
  })

  it("selects the first case and keeps repository and PR filtering in development view", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderPage({ view: "development" }, onSearchChange)

    await waitFor(() =>
      expect(onSearchChange).toHaveBeenCalledWith(
        { view: "development", case: caseID },
        true,
      ),
    )

    const repository = screen.getByLabelText("Repository")
    const pullNumber = screen.getByLabelText("Pull request number")
    await user.type(repository, " octo/filtered ")
    await user.type(pullNumber, "84")
    await user.click(screen.getByRole("button", { name: "Apply" }))
    expect(onSearchChange).toHaveBeenLastCalledWith(
      {
        view: "development",
        repository: "octo/filtered",
        pull_number: 84,
      },
      true,
    )

    await user.click(screen.getByRole("button", { name: "Attention policies" }))
    expect(onSearchChange).toHaveBeenLastCalledWith({ view: "policies" })
  })

  it("polls for AI attention on an unselected case and opens its canonical chat", async () => {
    vi.useFakeTimers({
      toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"],
    })
    try {
      setWideLayoutForTest(false)
      const onSearchChange = vi.fn()
      mockedList
        .mockResolvedValueOnce({ cases: [summary, replaySummary] })
        .mockResolvedValue({
          cases: [summary, { ...replaySummary, attention_required: true }],
        })

      renderPage({ view: "development" }, onSearchChange)
      await act(async () => {
        await Promise.resolve()
      })
      expect(mockedList).toHaveBeenCalledTimes(1)
      expect(screen.queryByText("AI attention")).not.toBeInTheDocument()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000)
      })

      expect(mockedList).toHaveBeenCalledTimes(2)
      const attentionMarker = screen.getByText("AI attention")
      fireEvent.click(attentionMarker.closest("button")!)
      expect(onSearchChange).toHaveBeenLastCalledWith(
        {
          view: "development",
          case: replayCaseID,
          focus: "chat",
        },
        false,
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it("passes canonical route filters to the read-only list and resets both", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderPage(
      {
        view: "development",
        repository: "octo/repo",
        pull_number: 42,
      },
      onSearchChange,
    )

    await waitFor(() =>
      expect(mockedList).toHaveBeenCalledWith({
        repository: "octo/repo",
        pull_number: 42,
        limit: 40,
      }),
    )
    expect(screen.getByLabelText("Pull request number")).toHaveValue(42)
    await user.click(screen.getByRole("button", { name: "Reset" }))
    expect(onSearchChange).toHaveBeenLastCalledWith(
      { view: "development" },
      true,
    )
  })
})

function renderPage(
  search: {
    view: "development"
    case?: string
    repository?: string
    pull_number?: number
    focus?: "chat"
  },
  onSearchChange = vi.fn(),
  client = newTestQueryClient(),
) {
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <PRDevelopmentPage search={search} onSearchChange={onSearchChange} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

function newTestQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
}

function setWideLayoutForTest(matches: boolean) {
  vi.mocked(window.matchMedia).mockImplementation((query: string) => ({
    matches: matches && query === "(min-width: 1024px)",
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
}
