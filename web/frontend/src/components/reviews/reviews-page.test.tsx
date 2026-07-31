import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

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
  })

  beforeEach(() => {
    vi.mocked(chatAboutReview).mockReset()
    vi.mocked(dropReviewFinding).mockReset()
    vi.mocked(getReview).mockReset()
    vi.mocked(listReviews).mockReset()
    vi.mocked(reconcileReview).mockReset()
    vi.mocked(rephraseReviewFinding).mockReset()
    vi.mocked(restoreReviewFinding).mockReset()
    vi.mocked(submitReview).mockReset()
    vi.mocked(updateReviewFinding).mockReset()

    vi.mocked(listReviews).mockResolvedValue({ cases: [reviewCase] })
    vi.mocked(getReview).mockResolvedValue(detail)
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
) {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <ReviewsPage search={search} onSearchChange={onSearchChange} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}
