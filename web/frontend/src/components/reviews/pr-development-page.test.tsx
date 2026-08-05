import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  getPRDevelopmentCase,
  listPRDevelopmentCases,
} from "@/api/pr-development"
import { PRDevelopmentPage } from "@/components/reviews/pr-development-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@/api/pr-development", () => ({
  getPRDevelopmentCase: vi.fn(),
  listPRDevelopmentCases: vi.fn(),
}))

const mockedList = vi.mocked(listPRDevelopmentCases)
const mockedGet = vi.mocked(getPRDevelopmentCase)
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
} as const
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
    mockedList.mockReset()
    mockedGet.mockReset()
    mockedList.mockResolvedValue({
      cases: [summary, summary, replaySummary],
    })
    mockedGet.mockResolvedValue({ case: developmentCase })
  })

  it("renders captured feedback as plain text and exposes only read-only links", async () => {
    renderPage({ view: "development", case: caseID })

    const feedbackRegion = await screen.findByTestId("pr-development-feedback")
    expect(feedbackRegion.textContent).toBe(feedback)
    expect(feedbackRegion.querySelector("a")).toBeNull()
    expect(feedbackRegion.querySelector("script")).toBeNull()
    expect(
      screen.queryByRole("link", { name: "steal" }),
    ).not.toBeInTheDocument()

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
    expect(
      screen.queryByRole("button", { name: /chat|checkout|push/i }),
    ).toBeNull()
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
) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <PRDevelopmentPage search={search} onSearchChange={onSearchChange} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}
