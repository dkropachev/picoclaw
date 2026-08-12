import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { StrictMode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  ReviewAttentionAPIError,
  type ReviewAttentionProjection,
  getReviewAttention,
  respondToReviewAttention,
} from "@/api/review-attention"
import { parseExactJSON } from "@/api/review-attention-json"
import {
  ReviewAPIError,
  type ReviewCaseDetail,
  type ReviewFinding,
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
import {
  ReviewsPage,
  type ReviewsRouteSearch,
} from "@/components/reviews/reviews-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...original,
    useBlocker: vi.fn(() => ({ status: "idle" })),
  }
})

vi.mock("@/api/review-attention", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/review-attention")>()
  return {
    ...original,
    getReviewAttention: vi.fn(),
    respondToReviewAttention: vi.fn(),
  }
})

vi.mock("@/api/reviews", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/reviews")>()
  return {
    ...original,
    chatAboutReview: vi.fn(),
    dropReviewFinding: vi.fn(),
    getReview: vi.fn(),
    listReviews: vi.fn(),
    reconcileReview: vi.fn(),
    rephraseReviewFinding: vi.fn(),
    restoreReviewFinding: vi.fn(),
    submitReview: vi.fn(),
    updateReviewFinding: vi.fn(),
  }
})

const ids = {
  case: `prc_${"1".repeat(32)}`,
  finding: `prf_${"2".repeat(32)}`,
  messageUser: `prm_${"3".repeat(32)}`,
  messageAssistant: `prm_${"4".repeat(32)}`,
  submission: `prs_${"5".repeat(32)}`,
}

const reviewCase = {
  id: ids.case,
  event_id: `ev_${"6".repeat(32)}`,
  dispatch_id: `dsp_${"7".repeat(32)}`,
  run_id: `wr_${"8".repeat(32)}`,
  workflow_ref: "builtin://github-pr-review",
  connector: "primary",
  repository: "octo/repo",
  pull_number: 42,
  pull_url: "https://github.com/octo/repo/pull/42",
  base_sha: "a".repeat(40),
  head_sha: "b".repeat(40),
  summary: "One correctness issue was found.",
  tests: ["go test ./..."],
  residual_risks: ["The integration suite was not run."],
  status: "open" as const,
  version: 3,
  active_findings: 1,
  total_findings: 1,
  created_at: "2026-07-30T12:00:00Z",
  updated_at: "2026-07-30T12:01:00Z",
}

const finding: ReviewFinding = {
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
}

const detail: ReviewCaseDetail = {
  case: reviewCase,
  findings: [finding],
  messages: [],
}

const responseToken = `sha256:${"a".repeat(64)}`
const submittedDetail: ReviewCaseDetail = {
  ...detail,
  case: {
    ...reviewCase,
    status: "submitted",
    version: 7,
    submitted_at: "2026-07-30T12:04:01Z",
  },
  submission: {
    id: ids.submission,
    case_id: ids.case,
    draft_version: 3,
    status: "submitted",
    attempts: 1,
    external_review_id: "9876",
    external_url: reviewCase.pull_url,
    created_at: "2026-07-30T12:04:00Z",
    updated_at: "2026-07-30T12:04:01Z",
    submitted_at: "2026-07-30T12:04:01Z",
  },
}

function waitingAttention(
  overrides: Partial<ReviewAttentionProjection> = {},
): ReviewAttentionProjection {
  return {
    case_version: 7,
    status: "waiting",
    can_respond: true,
    turns: [
      {
        status: "waiting",
        title: "Choose a safe contract",
        questions: parseExactJSON('{"priority":9007199254740993}'),
        response_token: responseToken,
      },
    ],
    ...overrides,
  }
}

function answeredAttention(response: string): ReviewAttentionProjection {
  return {
    case_version: 7,
    status: "completed",
    can_respond: false,
    turns: [
      {
        status: "answered",
        title: "Choose a safe contract",
        questions: parseExactJSON('{"priority":9007199254740993}'),
        response,
      },
    ],
  }
}

describe("ReviewsPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
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
    vi.mocked(useBlocker).mockClear()
    vi.mocked(chatAboutReview).mockReset()
    vi.mocked(dropReviewFinding).mockReset()
    vi.mocked(getReview).mockReset()
    vi.mocked(listReviews).mockReset()
    vi.mocked(reconcileReview).mockReset()
    vi.mocked(rephraseReviewFinding).mockReset()
    vi.mocked(restoreReviewFinding).mockReset()
    vi.mocked(submitReview).mockReset()
    vi.mocked(updateReviewFinding).mockReset()
    vi.mocked(getReviewAttention).mockReset()
    vi.mocked(respondToReviewAttention).mockReset()

    vi.mocked(listReviews).mockResolvedValue({ cases: [reviewCase] })
    vi.mocked(getReview).mockResolvedValue(detail)
  })

  it("opens a role-specific editor and preserves its portfolio return state", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderReviews(
      {
        view: "review",
        case: ids.case,
        repo: "octo/repo",
        pr: 42,
        filter: "role = review",
      },
      onSearchChange,
    )

    expect(await screen.findByRole("button", { name: "Drop" })).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "All pull request work" }),
    )
    expect(onSearchChange).toHaveBeenCalledWith(
      {
        repo: "octo/repo",
        pr: 42,
        filter: "role = review",
        review_case: ids.case,
      },
      true,
    )
  })

  it("blocks navigation after a standalone editor acquires unsaved text", async () => {
    const user = userEvent.setup()
    renderReviews({
      view: "review",
      case: ids.case,
      repo: "octo/repo",
      pr: 42,
    })

    const title = await screen.findByLabelText("Title")
    await user.type(title, " changed")

    await waitFor(() => {
      const options = vi.mocked(useBlocker).mock.calls.at(-1)?.[0] as
        | { shouldBlockFn: () => boolean; enableBeforeUnload?: () => boolean }
        | undefined
      expect(options?.shouldBlockFn()).toBe(true)
      expect(options?.enableBeforeUnload?.()).toBe(true)
    })
    expect(screen.getByRole("button", { name: "Submit review" })).toBeDisabled()
    expect(submitReview).not.toHaveBeenCalled()

    await user.type(
      screen.getByPlaceholderText(
        "For example: make this concise and constructive",
      ),
      "Make this clearer",
    )
    expect(
      screen.getByRole("button", { name: "Preview rephrase" }),
    ).toBeDisabled()
    expect(rephraseReviewFinding).not.toHaveBeenCalled()
  })

  it("opens the policy view without leaking the selected review into its URL state", async () => {
    const onSearchChange = vi.fn()
    const user = userEvent.setup()
    renderReviews(
      {
        case: ids.case,
        status: "open",
        repository: "octo/repo",
      },
      onSearchChange,
    )

    await user.click(
      await screen.findByRole("button", { name: "Attention policies" }),
    )
    expect(onSearchChange).toHaveBeenCalledWith({ view: "policies" })
  })

  it("hands a submitted review into the attention chat and sends one fenced reply", async () => {
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention).mockResolvedValue(waitingAttention())
    vi.mocked(respondToReviewAttention).mockResolvedValue(
      answeredAttention("Keep v1"),
    )
    const user = userEvent.setup()
    renderReviews({ case: ids.case, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    expect(screen.getByLabelText("Message")).toBeDisabled()
    expect(screen.getByText('{"priority":9007199254740993}')).toBeVisible()
    await waitFor(() => expect(response).toHaveFocus())

    await user.type(response, "Keep v1")
    await user.click(
      screen.getByRole("button", { name: "Send attention reply" }),
    )

    await waitFor(() => {
      expect(respondToReviewAttention).toHaveBeenCalledWith(
        ids.case,
        7,
        responseToken,
        "Keep v1",
      )
    })
    expect(
      await screen.findByText("The attention conversation is complete."),
    ).toBeVisible()
    expect(
      screen.queryByLabelText("Reply to the AI attention request"),
    ).not.toBeInTheDocument()
  })

  it("special-renders only the exact AI envelope without hiding custom question fields", async () => {
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention).mockResolvedValue(
      waitingAttention({
        turns: [
          {
            status: "answered",
            title: "Confirm AI scope",
            questions: parseExactJSON(
              '{"gate_id":"scope","reason":"A choice is required.","questions":["Keep the bounded scope?"]}',
            ),
            response: "Keep it bounded.",
          },
          {
            status: "waiting",
            title: "Confirm custom data",
            questions: parseExactJSON(
              '{"reason":"Choose one.","questions":["Which one?"],"choices":["safe","fast"]}',
            ),
            response_token: responseToken,
          },
        ],
      }),
    )

    renderReviews({ case: ids.case, focus: "chat" })

    expect(await screen.findByText("Gate scope")).toBeVisible()
    expect(screen.getByText("A choice is required.")).toBeVisible()
    expect(screen.getByText("Keep the bounded scope?")).toBeVisible()
    expect(
      screen.getByText(
        '{"reason":"Choose one.","questions":["Which one?"],"choices":["safe","fast"]}',
      ),
    ).toBeVisible()
  })

  it("preserves an attention draft across a conflict refetch", async () => {
    const nextToken = `sha256:${"b".repeat(64)}`
    const latest = waitingAttention({
      case_version: 8,
      turns: [
        {
          status: "waiting",
          title: "Choose the updated contract",
          questions: parseExactJSON('["Keep v2?"]'),
          response_token: nextToken,
        },
      ],
    })
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention)
      .mockResolvedValueOnce(waitingAttention())
      .mockResolvedValue(latest)
    vi.mocked(respondToReviewAttention).mockRejectedValue(
      new ReviewAttentionAPIError("review_attention_conflict", 409),
    )
    const user = userEvent.setup()
    renderReviews({ case: ids.case, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    await user.type(response, "Preserve this local answer")
    await user.click(
      screen.getByRole("button", { name: "Send attention reply" }),
    )

    expect(
      await screen.findByText(
        "This attention request changed. The latest state was loaded and your reply is preserved.",
      ),
    ).toBeVisible()
    await waitFor(() => expect(getReviewAttention).toHaveBeenCalledTimes(2))
    expect(
      screen.getByLabelText("Reply to the AI attention request"),
    ).toHaveValue("Preserve this local answer")
    expect(screen.getByText("Choose the updated contract")).toBeVisible()
  })

  it("clears a lost-response error when the authoritative refetch contains the reply", async () => {
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention)
      .mockResolvedValueOnce(waitingAttention())
      .mockResolvedValue(answeredAttention("Keep v1"))
    vi.mocked(respondToReviewAttention).mockRejectedValue(
      new TypeError("transport closed after response"),
    )
    const user = userEvent.setup()
    renderReviews({ case: ids.case, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    await user.type(response, "Keep v1")
    await user.click(
      screen.getByRole("button", { name: "Send attention reply" }),
    )

    expect(
      await screen.findByText("The attention conversation is complete."),
    ).toBeVisible()
    expect(
      screen.queryByText(
        "The reply could not be sent. The latest state was loaded and your text is preserved.",
      ),
    ).not.toBeInTheDocument()
  })

  it("reports the UTF-8 response bound before submitting multibyte text", async () => {
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention).mockResolvedValue(waitingAttention())
    renderReviews({ case: ids.case, focus: "chat" })

    const response = await screen.findByLabelText(
      "Reply to the AI attention request",
    )
    fireEvent.change(response, { target: { value: "界".repeat(11_000) } })

    expect(response).toHaveAttribute("aria-invalid", "true")
    expect(screen.getByText("33000 / 32768 UTF-8 bytes")).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Send attention reply" }),
    ).toBeDisabled()
    expect(respondToReviewAttention).not.toHaveBeenCalled()
  })

  it("polls a runtime-disabled waiting turn until it becomes actionable", async () => {
    const disabledWaiting = waitingAttention({
      can_respond: false,
      turns: [
        {
          status: "waiting",
          title: "Choose a safe contract",
          questions: parseExactJSON("[]"),
        },
      ],
    })
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention)
      .mockResolvedValueOnce(disabledWaiting)
      .mockResolvedValue(waitingAttention())
    renderReviews({ case: ids.case, focus: "chat" })
    await waitFor(() => expect(getReviewAttention).toHaveBeenCalledTimes(1))
    expect(
      screen.queryByLabelText("Reply to the AI attention request"),
    ).not.toBeInTheDocument()

    await waitFor(
      () => {
        expect(getReviewAttention).toHaveBeenCalledTimes(2)
      },
      { timeout: 2500 },
    )
    expect(
      screen.getByLabelText("Reply to the AI attention request"),
    ).toBeVisible()

    await new Promise((resolve) => window.setTimeout(resolve, 1700))
    expect(getReviewAttention).toHaveBeenCalledTimes(2)
  })

  it("focuses a cached attention request after the Strict Mode effect restart", async () => {
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention).mockResolvedValue(waitingAttention())
    const callbacks = new Map<number, FrameRequestCallback>()
    const canceled = new Set<number>()
    let nextFrame = 0
    const requestFrame = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((callback) => {
        nextFrame++
        callbacks.set(nextFrame, callback)
        return nextFrame
      })
    const cancelFrame = vi
      .spyOn(window, "cancelAnimationFrame")
      .mockImplementation((frame) => {
        canceled.add(frame)
      })
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    queryClient.setQueryData(["reviews", "detail", ids.case], submittedDetail)
    queryClient.setQueryData(
      ["reviews", "attention", ids.case],
      waitingAttention(),
    )

    try {
      render(
        <StrictMode>
          <QueryClientProvider client={queryClient}>
            <SidebarProvider>
              <ReviewsPage
                search={{ case: ids.case, focus: "chat" }}
                onSearchChange={vi.fn()}
              />
            </SidebarProvider>
          </QueryClientProvider>
        </StrictMode>,
      )
      const response = await screen.findByLabelText(
        "Reply to the AI attention request",
      )
      act(() => {
        for (const [frame, callback] of callbacks) {
          if (!canceled.has(frame)) {
            callback(0)
          }
        }
      })
      expect(response).toHaveFocus()
    } finally {
      requestFrame.mockRestore()
      cancelFrame.mockRestore()
    }
  })

  it("does not carry a conversation draft into another cached case", async () => {
    const secondCaseID = `prc_${"9".repeat(32)}`
    const secondDetail: ReviewCaseDetail = {
      ...detail,
      case: {
        ...reviewCase,
        id: secondCaseID,
        repository: "octo/other",
        pull_number: 43,
        pull_url: "https://github.com/octo/other/pull/43",
      },
      findings: [],
    }
    const user = userEvent.setup()
    const rendered = renderReviews({ case: ids.case }, vi.fn(), [
      detail,
      secondDetail,
    ])

    const firstDraft = await screen.findByLabelText("Message")
    await user.type(firstDraft, "This belongs only to case one")
    expect(firstDraft).toHaveValue("This belongs only to case one")

    rendered.rerenderReviews({ case: secondCaseID })
    expect(await screen.findByText("octo/other #43")).toBeVisible()
    expect(screen.getByLabelText("Message")).toHaveValue("")
  })

  it("retries a saved recovery response and renders canceled failures read-only", async () => {
    const recovery: ReviewAttentionProjection = {
      case_version: 7,
      status: "recovery_required",
      can_respond: true,
      turns: [
        {
          status: "recovery_required",
          title: "Choose a safe contract",
          questions: parseExactJSON('["Keep v1?"]'),
          response: "Keep v1",
          response_token: responseToken,
        },
      ],
    }
    vi.mocked(getReview).mockResolvedValue(submittedDetail)
    vi.mocked(getReviewAttention).mockResolvedValue(recovery)
    vi.mocked(respondToReviewAttention).mockResolvedValue(
      answeredAttention("Keep v1"),
    )
    const user = userEvent.setup()
    const rendered = renderReviews({ case: ids.case, focus: "chat" })

    const retry = await screen.findByRole("button", {
      name: "Retry continuation",
    })
    await user.click(retry)
    await waitFor(() => {
      expect(respondToReviewAttention).toHaveBeenCalledWith(
        ids.case,
        7,
        responseToken,
        "Keep v1",
      )
    })

    rendered.unmount()
    vi.mocked(getReviewAttention).mockResolvedValue({
      case_version: 7,
      status: "failed",
      can_respond: false,
      turns: [
        {
          status: "canceled",
          title: "Choose a safe contract",
          questions: parseExactJSON("[]"),
        },
      ],
    })
    renderReviews({ case: ids.case, focus: "chat" })

    expect(await screen.findByText("Canceled")).toBeVisible()
    expect(
      screen.getByText("The attention check could not be completed."),
    ).toBeVisible()
    expect(
      screen.queryByLabelText("Reply to the AI attention request"),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Retry continuation" }),
    ).not.toBeInTheDocument()
  })

  it("loads the inbox and saves the complete human-edited finding", async () => {
    const savedFinding: ReviewFinding = {
      ...finding,
      title: "Prevent lost updates",
      message: "Use the expected version when writing state.",
      severity: "medium",
      revision: 2,
      updated_at: "2026-07-30T12:02:00Z",
    }
    vi.mocked(updateReviewFinding).mockResolvedValue({
      ...detail,
      case: {
        ...reviewCase,
        version: 4,
        updated_at: "2026-07-30T12:02:00Z",
      },
      findings: [savedFinding],
    })
    const user = userEvent.setup()
    renderReviews()

    expect(
      await screen.findByText("One correctness issue was found."),
    ).toBeVisible()
    expect(screen.getAllByText("octo/repo #42").length).toBeGreaterThan(0)

    const title = screen.getByLabelText("Title")
    await user.clear(title)
    await user.type(title, "Prevent lost updates")
    const comment = screen.getByLabelText("Comment")
    await user.clear(comment)
    await user.type(comment, "Use the expected version when writing state.")
    await user.selectOptions(screen.getByLabelText("Severity"), "medium")
    await user.click(screen.getByRole("button", { name: "Save finding" }))

    await waitFor(() => {
      expect(updateReviewFinding).toHaveBeenCalledWith(
        ids.case,
        ids.finding,
        3,
        expect.objectContaining({
          severity: "medium",
          title: "Prevent lost updates",
          file: "pkg/store.go",
          line: 72,
          message: "Use the expected version when writing state.",
          evidence: "The version predicate is missing.",
          impact: "Concurrent edits can be lost.",
          recommendation: "Include the expected version in the update.",
          validation: "Add a concurrent update test.",
        }),
      )
    })
  })

  it("refetches after a version conflict without discarding editor text", async () => {
    const newerDetail: ReviewCaseDetail = {
      ...detail,
      case: { ...reviewCase, version: 4 },
      findings: [
        {
          ...finding,
          title: "Server-side title",
          revision: 2,
        },
      ],
    }
    let resolveRefresh!: (value: ReviewCaseDetail) => void
    const refresh = new Promise<ReviewCaseDetail>((resolve) => {
      resolveRefresh = resolve
    })
    vi.mocked(getReview)
      .mockResolvedValueOnce(detail)
      .mockReturnValueOnce(refresh)
      .mockResolvedValue(newerDetail)
    vi.mocked(updateReviewFinding).mockRejectedValueOnce(
      new ReviewAPIError("review case changed", 409),
    )
    const user = userEvent.setup()
    renderReviews()

    const title = await screen.findByLabelText("Title")
    await user.clear(title)
    await user.type(title, "Keep my local wording")
    await user.click(screen.getByRole("button", { name: "Save finding" }))

    await waitFor(() => expect(updateReviewFinding).toHaveBeenCalledOnce())
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled()
    expect(
      screen.queryByText("This review changed elsewhere."),
    ).not.toBeInTheDocument()

    resolveRefresh(newerDetail)
    expect(
      await screen.findByText("This review changed elsewhere."),
    ).toBeVisible()
    await waitFor(() => expect(getReview).toHaveBeenCalledTimes(2))
    expect(screen.getByLabelText("Title")).toHaveValue("Keep my local wording")

    const savedFinding = {
      ...newerDetail.findings[0],
      title: "Keep my local wording",
      revision: 3,
    }
    vi.mocked(updateReviewFinding).mockResolvedValueOnce({
      ...newerDetail,
      case: { ...newerDetail.case, version: 5 },
      findings: [savedFinding],
    })
    await user.click(screen.getByRole("button", { name: "Save finding" }))

    await waitFor(() => {
      expect(updateReviewFinding).toHaveBeenLastCalledWith(
        ids.case,
        ids.finding,
        4,
        expect.objectContaining({ title: "Keep my local wording" }),
      )
    })
  })

  it("durably chats, previews a rephrase, and applies it through an edit", async () => {
    const chatted: ReviewCaseDetail = {
      ...detail,
      case: { ...reviewCase, version: 4 },
      messages: [
        {
          id: ids.messageUser,
          case_id: ids.case,
          ordinal: 0,
          kind: "chat",
          role: "user",
          content: "Why is this high severity?",
          created_at: "2026-07-30T12:02:00Z",
        },
        {
          id: ids.messageAssistant,
          case_id: ids.case,
          ordinal: 1,
          kind: "chat",
          role: "assistant",
          content: "It can overwrite a concurrent edit.",
          created_at: "2026-07-30T12:02:01Z",
        },
      ],
    }
    vi.mocked(chatAboutReview).mockResolvedValue(chatted)
    vi.mocked(rephraseReviewFinding).mockResolvedValue({
      detail: { ...chatted, case: { ...chatted.case, version: 5 } },
      suggestion: {
        title: "Guard against lost updates",
        message: "Please include the expected version in this write.",
      },
    })
    vi.mocked(updateReviewFinding).mockResolvedValue({
      ...chatted,
      case: { ...chatted.case, version: 6 },
      findings: [
        {
          ...finding,
          title: "Guard against lost updates",
          message: "Please include the expected version in this write.",
          revision: 2,
        },
      ],
    })
    const user = userEvent.setup()
    renderReviews()

    const chat = await screen.findByLabelText("Message")
    await user.type(chat, "Why is this high severity?")
    await user.click(screen.getByRole("button", { name: "Send message" }))

    expect(
      await screen.findByText("It can overwrite a concurrent edit."),
    ).toBeVisible()
    expect(chatAboutReview).toHaveBeenCalledWith(
      ids.case,
      3,
      "Why is this high severity?",
      undefined,
    )

    await user.type(
      screen.getByPlaceholderText(
        "For example: make this concise and constructive",
      ),
      "Make this concise",
    )
    await user.click(screen.getByRole("button", { name: "Preview rephrase" }))

    const preview = await screen.findByText("Guard against lost updates")
    expect(preview).toBeVisible()
    expect(rephraseReviewFinding).toHaveBeenCalledWith(
      ids.case,
      ids.finding,
      4,
      "Make this concise",
    )

    await user.click(
      screen.getByRole("button", { name: "Apply and save suggestion" }),
    )
    await waitFor(() => {
      expect(updateReviewFinding).toHaveBeenCalledWith(
        ids.case,
        ids.finding,
        5,
        expect.objectContaining({
          title: "Guard against lost updates",
          message: "Please include the expected version in this write.",
        }),
      )
    })
  })

  it("does not apply an older rephrase preview over newer manual edits", async () => {
    vi.mocked(rephraseReviewFinding).mockResolvedValue({
      detail: { ...detail, case: { ...reviewCase, version: 4 } },
      suggestion: {
        title: "AI replacement title",
        message: "AI replacement comment.",
      },
    })
    const user = userEvent.setup()
    renderReviews()

    await user.type(
      await screen.findByPlaceholderText(
        "For example: make this concise and constructive",
      ),
      "Make this concise",
    )
    await user.click(screen.getByRole("button", { name: "Preview rephrase" }))
    expect(await screen.findByText("AI replacement title")).toBeVisible()

    const title = screen.getByLabelText("Title")
    await user.clear(title)
    await user.type(title, "Newer manual title")

    expect(
      screen.getByRole("button", { name: "Apply and save suggestion" }),
    ).toBeDisabled()
    expect(title).toHaveValue("Newer manual title")
    expect(updateReviewFinding).not.toHaveBeenCalled()
  })

  it("drops and restores findings, and confirms submission before locking", async () => {
    const droppedFinding: ReviewFinding = {
      ...finding,
      state: "dropped",
      dropped_at: "2026-07-30T12:03:00Z",
      revision: 2,
    }
    const dropped: ReviewCaseDetail = {
      ...detail,
      case: {
        ...reviewCase,
        status: "all_dropped",
        version: 4,
        active_findings: 0,
      },
      findings: [droppedFinding],
    }
    vi.mocked(dropReviewFinding).mockResolvedValue(dropped)
    vi.mocked(restoreReviewFinding).mockResolvedValue({
      ...detail,
      case: { ...reviewCase, version: 5 },
      findings: [{ ...finding, revision: 3 }],
    })
    vi.mocked(submitReview).mockResolvedValue({
      ...detail,
      case: {
        ...reviewCase,
        status: "submitting",
        version: 4,
      },
      submission: {
        id: ids.submission,
        case_id: ids.case,
        draft_version: 3,
        status: "pending",
        attempts: 0,
        created_at: "2026-07-30T12:04:00Z",
        updated_at: "2026-07-30T12:04:00Z",
      },
    })
    const user = userEvent.setup()
    renderReviews()

    await user.click(await screen.findByRole("button", { name: "Drop" }))
    expect(
      await screen.findByText(
        "Nothing will be sent to GitHub. Restore a finding if the review should be submitted.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Submit review" })).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Restore" }))
    await waitFor(() => {
      expect(restoreReviewFinding).toHaveBeenCalledWith(
        ids.case,
        ids.finding,
        4,
      )
    })
  })

  it("requires confirmation and shows the automatically-polled submitting state", async () => {
    vi.mocked(submitReview).mockResolvedValue({
      ...detail,
      case: {
        ...reviewCase,
        status: "submitting",
        version: 4,
      },
      submission: {
        id: ids.submission,
        case_id: ids.case,
        draft_version: 3,
        status: "pending",
        attempts: 0,
        created_at: "2026-07-30T12:04:00Z",
        updated_at: "2026-07-30T12:04:00Z",
      },
    })
    const user = userEvent.setup()
    renderReviews()

    await user.click(
      await screen.findByRole("button", { name: "Submit review" }),
    )
    const dialog = await screen.findByRole("alertdialog")
    expect(
      within(dialog).getByText("Submit this review to GitHub?"),
    ).toBeVisible()
    expect(submitReview).not.toHaveBeenCalled()

    await user.click(
      within(dialog).getByRole("button", { name: "Submit to GitHub" }),
    )
    expect(await screen.findByText("Submission in progress")).toBeVisible()
    expect(submitReview).toHaveBeenCalledWith(ids.case, 3)
    expect(screen.getByRole("button", { name: "Submit review" })).toBeDisabled()
  })

  it("requires explicit GitHub inspection before reopening an unknown submission", async () => {
    const unknown: ReviewCaseDetail = {
      ...detail,
      case: {
        ...reviewCase,
        status: "submission_unknown",
        version: 5,
      },
      submission: {
        id: ids.submission,
        case_id: ids.case,
        draft_version: 3,
        status: "unknown",
        attempts: 1,
        public_error_code: "github_outcome_unknown",
        external_url: reviewCase.pull_url,
        created_at: "2026-07-30T12:04:00Z",
        updated_at: "2026-07-30T12:05:00Z",
      },
    }
    const reopened: ReviewCaseDetail = {
      ...unknown,
      case: {
        ...unknown.case,
        status: "open",
        version: 6,
      },
      submission: {
        ...unknown.submission!,
        status: "failed",
        public_error_code: "reconciled_absent",
        updated_at: "2026-07-30T12:06:00Z",
      },
    }
    vi.mocked(getReview).mockResolvedValue(unknown)
    vi.mocked(reconcileReview).mockResolvedValue(reopened)
    const user = userEvent.setup()
    renderReviews()

    expect(
      await screen.findByText("Submission outcome is unknown"),
    ).toBeVisible()
    await user.click(
      screen.getByRole("button", {
        name: "I confirmed no review was posted",
      }),
    )
    const dialog = await screen.findByRole("alertdialog")
    expect(
      within(dialog).getByText("Confirm no review was posted?"),
    ).toBeVisible()
    expect(reconcileReview).not.toHaveBeenCalled()

    await user.click(
      within(dialog).getByRole("button", { name: "Reopen review" }),
    )
    await waitFor(() => {
      expect(reconcileReview).toHaveBeenCalledWith(ids.case, 5, "absent")
    })
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Submit review" }),
      ).toBeEnabled()
    })
    expect(screen.getByLabelText("Title")).toBeEnabled()
  })
})

function renderReviews(
  search: ReviewsRouteSearch = { case: ids.case },
  onSearchChange = vi.fn(),
  seededDetails: ReviewCaseDetail[] = [],
) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  for (const seededDetail of seededDetails) {
    queryClient.setQueryData(
      ["reviews", "detail", seededDetail.case.id],
      seededDetail,
    )
  }
  const view = (nextSearch: ReviewsRouteSearch) => (
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <ReviewsPage search={nextSearch} onSearchChange={onSearchChange} />
      </SidebarProvider>
    </QueryClientProvider>
  )
  const rendered = render(view(search))
  return {
    ...rendered,
    rerenderReviews: (nextSearch: ReviewsRouteSearch) =>
      rendered.rerender(view(nextSearch)),
  }
}
