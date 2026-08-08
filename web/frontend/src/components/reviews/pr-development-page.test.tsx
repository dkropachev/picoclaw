import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  PRDevelopmentAPIError,
  type PRDevelopmentCase,
  type PRDevelopmentCaseDetail,
  chatAboutPRDevelopmentCase,
  getPRDevelopmentCase,
  listPRDevelopmentCases,
} from "@/api/pr-development"
import { PRDevelopmentPage } from "@/components/reviews/pr-development-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/pr-development", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/pr-development")>()
  return {
    ...actual,
    chatAboutPRDevelopmentCase: vi.fn(),
    getPRDevelopmentCase: vi.fn(),
    listPRDevelopmentCases: vi.fn(),
  }
})

const mockedList = vi.mocked(listPRDevelopmentCases)
const mockedGet = vi.mocked(getPRDevelopmentCase)
const mockedChat = vi.mocked(chatAboutPRDevelopmentCase)
const caseID = `pdc_${"1".repeat(32)}`
const replayCaseID = `pdc_${"2".repeat(32)}`
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
const feedback =
  'Fix this <a href="https://evil.test">steal</a>\n<script>alert(1)</script>'
const developmentCase = {
  ...summary,
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
} satisfies PRDevelopmentCaseDetail
const replaySummary = { ...summary, id: replayCaseID } as const

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
  })

  beforeEach(() => {
    setWideLayoutForTest(true)
    mockedList.mockReset()
    mockedGet.mockReset()
    mockedChat.mockReset()
    mockedList.mockResolvedValue({
      cases: [summary, summary, replaySummary],
    })
    mockedGet.mockResolvedValue(detail)
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

  it("sends one version-fenced message and updates the conversation cache", async () => {
    const updatedDetail: PRDevelopmentCaseDetail = {
      case: developmentCase,
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
      case: developmentCase,
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

  it("disables duplicate sends while the advisory response is pending", async () => {
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
    await user.type(composer, "Explain this.")
    await user.click(screen.getByRole("button", { name: "Send" }))
    expect(screen.getByRole("button", { name: "Sending…" })).toBeDisabled()
    expect(composer).toBeDisabled()
    expect(mockedChat).toHaveBeenCalledTimes(1)

    resolveChat?.(detail)
    await waitFor(() => expect(composer).toBeEnabled())
    expect(screen.queryByRole("button", { name: "Sending…" })).toBeNull()
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
      case: developmentCase,
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
      case: developmentCase,
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
      case: developmentCase,
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
      case: developmentCase,
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
