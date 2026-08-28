import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  createDevelopmentRequestID,
  createDevelopmentWorkspace,
  listDevelopmentRepositories,
} from "@/api/development-workspaces"
import { DevelopmentIntakePage } from "@/components/development-workspaces/development-intake-page"

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    createDevelopmentRequestID: vi.fn(),
    createDevelopmentWorkspace: vi.fn(),
    listDevelopmentRepositories: vi.fn(),
  }
})
vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

const workspaceID = `devw_${"1".repeat(32)}`
const mockedCreate = vi.mocked(createDevelopmentWorkspace)
const mockedRequestID = vi.mocked(createDevelopmentRequestID)
const mockedRepositories = vi.mocked(listDevelopmentRepositories)

function renderPage(onCreated = vi.fn(), initialIssueURL?: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <DevelopmentIntakePage
        onBack={vi.fn()}
        onCreated={onCreated}
        initialIssueURL={initialIssueURL}
      />
    </QueryClientProvider>,
  )
  return onCreated
}

describe("development intake", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    mockedCreate.mockReset()
    mockedRequestID.mockReset()
    mockedRepositories.mockReset()
    mockedRequestID.mockReturnValue(`devq_${"2".repeat(32)}`)
    mockedRepositories.mockResolvedValue({
      repositories: [
        {
          identity: "https://github.com|100",
          name: "octo/repo",
          default_branch: "main",
          can_implement: true,
        },
      ],
    })
    mockedCreate.mockResolvedValue({
      id: workspaceID,
      intent: "implement_feature",
      source_kind: "issue",
      repository: "octo/repo",
      title: "Retry feedback",
      phase: "intake",
      execution_state: "queued",
      version: 1,
      created_at: "2026-08-24T10:00:00Z",
      updated_at: "2026-08-24T10:00:00Z",
      source: { kind: "issue", url: "https://github.com/octo/repo/issues/7" },
      changed_files: [],
      activity: [],
      validation_checks: [],
      gates: [],
      publications: [],
    })
  })

  it("mounts only the selected workflow and feature source fields", async () => {
    const user = userEvent.setup()
    renderPage()

    expect(
      screen
        .getByTestId("development-intake")
        .querySelector('[data-slot="collection-detail-shell"]'),
    ).toBeInTheDocument()
    expect(screen.queryByLabelText("GitHub issue URL")).not.toBeInTheDocument()
    expect(
      screen.queryByLabelText("GitHub pull request URL"),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: /^Implement feature/ }))
    expect(screen.getByLabelText("GitHub issue URL")).toBeVisible()
    expect(
      screen.queryByLabelText("GitHub pull request URL"),
    ).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Feature brief")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Write brief" }))
    expect(screen.queryByLabelText("GitHub issue URL")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Feature brief")).toBeVisible()
    expect(
      screen.queryByLabelText("GitHub pull request URL"),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: /^Pick up PR/ }))
    expect(screen.getByLabelText("GitHub pull request URL")).toBeVisible()
    expect(screen.queryByLabelText("GitHub issue URL")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Feature brief")).not.toBeInTheDocument()
  })

  it("opens an issue-prefilled feature form without mounting PR fields", () => {
    renderPage(vi.fn(), "https://github.com/octo/repo/issues/7")

    expect(screen.getByTestId("implement-feature-form")).toBeVisible()
    expect(screen.getByLabelText("GitHub issue URL")).toHaveValue(
      "https://github.com/octo/repo/issues/7",
    )
    expect(
      screen.queryByLabelText("GitHub pull request URL"),
    ).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Feature brief")).not.toBeInTheDocument()
  })

  it("submits issue implementation without a pull request field", async () => {
    const user = userEvent.setup()
    const onCreated = renderPage()
    await user.click(screen.getByRole("button", { name: /^Implement feature/ }))
    await user.type(
      screen.getByLabelText("GitHub issue URL"),
      "https://github.com/octo/repo/issues/7",
    )
    await user.click(
      screen.getByRole("button", { name: "Start implementation" }),
    )

    await waitFor(() =>
      expect(mockedCreate).toHaveBeenCalledWith({
        intent: "implement_feature",
        source: {
          kind: "issue",
          issue_url: "https://github.com/octo/repo/issues/7",
        },
        request_id: `devq_${"2".repeat(32)}`,
      }),
    )
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(workspaceID))
  })

  it("submits PR pickup without issue or brief fields", async () => {
    const user = userEvent.setup()
    mockedCreate.mockResolvedValueOnce({
      id: workspaceID,
      intent: "pickup_pr",
      source_kind: "pull_request",
      repository: "octo/repo",
      title: "Improve retry feedback",
      phase: "intake",
      execution_state: "queued",
      version: 1,
      created_at: "2026-08-24T10:00:00Z",
      updated_at: "2026-08-24T10:00:00Z",
      source: {
        kind: "pull_request",
        url: "https://github.com/octo/repo/pull/9",
      },
      changed_files: [],
      activity: [],
      validation_checks: [],
      gates: [],
      publications: [],
    })
    renderPage()
    await user.click(screen.getByRole("button", { name: /^Pick up PR/ }))
    await user.type(
      screen.getByLabelText("GitHub pull request URL"),
      "https://github.com/octo/repo/pull/9",
    )
    await user.click(screen.getByRole("button", { name: "Pick up PR" }))

    await waitFor(() =>
      expect(mockedCreate).toHaveBeenCalledWith({
        intent: "pickup_pr",
        pull_request_url: "https://github.com/octo/repo/pull/9",
        request_id: `devq_${"2".repeat(32)}`,
      }),
    )
  })

  it("submits a brief with one configured repository", async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByRole("button", { name: /^Implement feature/ }))
    await user.click(screen.getByRole("button", { name: "Write brief" }))
    await screen.findByText("octo/repo · main")
    await user.click(screen.getByRole("combobox", { name: "Repository" }))
    await user.click(screen.getByRole("option", { name: "octo/repo · main" }))
    fireEvent.change(screen.getByLabelText("Feature brief"), {
      target: { value: "Add retry feedback." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Start implementation" }),
    )

    await waitFor(() =>
      expect(mockedCreate).toHaveBeenCalledWith({
        intent: "implement_feature",
        source: {
          kind: "brief",
          repository_identity: "https://github.com|100",
          content: "Add retry feedback.",
        },
        request_id: `devq_${"2".repeat(32)}`,
      }),
    )
  })
})
