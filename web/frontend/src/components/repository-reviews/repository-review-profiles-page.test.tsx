import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createRepositoryReviewProfile,
  deleteRepositoryReviewProfile,
  getRepositoryReviewAutomationOptions,
  listRepositoryReviewProfiles,
  updateRepositoryReviewProfile,
} from "@/api/repository-reviews"
import { RepositoryReviewProfilesPage } from "@/components/repository-reviews/repository-review-profiles-page"

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: React.ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

vi.mock("@/api/repository-reviews", () => ({
  createRepositoryReviewProfile: vi.fn(),
  deleteRepositoryReviewProfile: vi.fn(),
  getRepositoryReviewAutomationOptions: vi.fn(),
  listRepositoryReviewProfiles: vi.fn(),
  updateRepositoryReviewProfile: vi.fn(),
}))

describe("RepositoryReviewProfilesPage", () => {
  beforeEach(() => {
    vi.mocked(listRepositoryReviewProfiles).mockResolvedValue({ profiles: [] })
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "review-model",
          resolved_model: "provider/review-model",
          provider: "provider",
          available: true,
          price_known: true,
          input_price_per_1m: 1,
          output_price_per_1m: 4,
        },
      ],
      accounts: [],
    })
    vi.mocked(createRepositoryReviewProfile).mockReset()
    vi.mocked(updateRepositoryReviewProfile).mockReset()
    vi.mocked(deleteRepositoryReviewProfile).mockReset()
  })

  it("keeps advanced values hidden, preserved, and sends one reviewer model", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewProfile).mockImplementation(
      async (value) => ({
        ...value,
        id: "profile_1",
        version: 1,
        created_at: "2026-08-23T00:00:00Z",
        updated_at: "2026-08-23T00:00:00Z",
      }),
    )
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.type(screen.getByLabelText("Profile name"), "Core bugs")
    expect(
      screen.getByText(
        "Narrows defect classes only. Findings diagnose validated defects and never include fixes or remediation.",
      ),
    ).toBeVisible()
    expect(screen.getByLabelText("Review focus")).toHaveAttribute(
      "aria-describedby",
      "review-focus-help",
    )
    expect(screen.getByText("Scope")).toBeVisible()
    const includeFolders = screen.getByLabelText("Include folders")
    expect(includeFolders).toBeVisible()
    expect(screen.getByLabelText("Exclude folders")).toBeVisible()
    expect(screen.getByLabelText("Additional scope guidance")).toBeVisible()
    expect(
      screen.getByRole("checkbox", { name: "Production code" }),
    ).toBeChecked()
    expect(screen.queryByLabelText("Files per batch")).not.toBeInTheDocument()
    expect(includeFolders).toHaveAttribute(
      "placeholder",
      "cmd\ninternal/review",
    )
    expect(screen.getByLabelText("Exclude folders")).toHaveAttribute(
      "placeholder",
      "generated\ntestdata",
    )
    await user.type(includeFolders, "pkg/core")
    await user.click(screen.getByText(/^Advanced/))
    expect(
      screen.queryByLabelText("Estimated output tokens"),
    ).not.toBeInTheDocument()
    expect(screen.getByLabelText("Execution account")).toHaveValue("")
    expect(
      screen.queryByLabelText("Account check interval (seconds)"),
    ).not.toBeInTheDocument()
    const workers = screen.getByLabelText("Parallel review workers")
    expect(workers).toBeEnabled()
    expect(workers).toHaveValue(8)
    expect(workers).toHaveAttribute("aria-describedby", "parallel-workers-help")
    await user.clear(workers)
    await user.type(workers, "6")
    const files = screen.getByLabelText("Files per batch")
    await user.clear(files)
    await user.type(files, "12")
    await user.click(screen.getByText(/^Advanced/))
    expect(screen.queryByLabelText("Files per batch")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Include folders")).toHaveValue("pkg/core")
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    await waitFor(() =>
      expect(createRepositoryReviewProfile).toHaveBeenCalled(),
    )
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).toMatchObject({
      name: "Core bugs",
      reviewer_model: "review-model",
      account_ref: "",
      max_files_per_run: 12,
      max_parallel_children: 6,
      scope_policy: { include_folders: ["pkg/core"] },
      budget: { guard_expression: "" },
    })
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).not.toHaveProperty("reviewer_models")
  })

  it("validates always-visible code scope without opening advanced", async () => {
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.type(screen.getByLabelText("Profile name"), "Scoped review")
    expect(screen.queryByLabelText("Files per batch")).not.toBeInTheDocument()

    await user.click(
      screen.getByRole("checkbox", { name: "Hot-path production code" }),
    )
    await user.click(screen.getByRole("checkbox", { name: "Production code" }))
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Select at least one code type.",
    )
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()

    await user.click(screen.getByRole("checkbox", { name: "Tests" }))
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeEnabled()
  })

  it("selects the execution account and saves one admission expression without serializing work", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "review-model",
          resolved_model: "openai/review-model",
          provider: "openai",
          available: true,
          price_known: true,
          input_price_per_1m: 1,
          output_price_per_1m: 4,
        },
      ],
      accounts: [
        {
          id: "credential:openai:work",
          provider: "openai",
          label: "Work account",
          status: "available",
          entries: [],
          models: ["review-model"],
        },
        {
          id: "credential:openai:empty",
          provider: "openai",
          label: "No models account",
          status: "available",
          entries: [],
          models: [],
        },
      ],
    })
    vi.mocked(createRepositoryReviewProfile).mockImplementation(
      async (value) => ({
        ...value,
        id: "profile_2",
        version: 1,
        created_at: "2026-08-23T00:00:00Z",
        updated_at: "2026-08-23T00:00:00Z",
      }),
    )
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.type(screen.getByLabelText("Profile name"), "Guarded review")
    expect(screen.queryByLabelText("Execution account")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: /^Advanced/ }))
    const account = screen.getByLabelText("Execution account")
    expect(account).toHaveValue("")
    expect(screen.getByRole("option", { name: "Default account" })).toHaveValue(
      "",
    )
    expect(
      screen.getByRole("option", { name: "Work account · openai" }),
    ).toHaveValue("credential:openai:work")
    expect(
      screen.getByRole("option", {
        name: "No models account · openai (no compatible review models)",
      }),
    ).toBeDisabled()
    await user.selectOptions(account, "credential:openai:work")

    const guard = screen.getByLabelText("Guard expression")
    expect(guard).toHaveAttribute(
      "placeholder",
      "account.limits.weekly.known and account.limits.weekly.remaining_percent >= 10 and\nspent.tokens.total < 500000 and spend.total.usd < 25",
    )
    expect(guard).toHaveAttribute("aria-describedby", "review-task-guard-help")
    expect(
      screen.getByText(
        /Start typing or press Ctrl\+Space for fields and operators/i,
      ),
    ).toBeVisible()
    expect(
      screen.queryByLabelText("Maximum total tokens"),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByLabelText("Maximum estimated cost ($)"),
    ).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Window limits")).not.toBeInTheDocument()
    expect(
      screen.queryByLabelText("Account check interval (seconds)"),
    ).not.toBeInTheDocument()

    const expression =
      "account.limits.weekly.remaining_percent >= 10 and spend.total.usd < 25"
    await user.type(guard, expression)
    const workers = screen.getByLabelText("Parallel review workers")
    expect(workers).toBeEnabled()
    expect(workers).toHaveValue(8)
    await user.clear(workers)
    await user.type(workers, "12")
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    await waitFor(() =>
      expect(createRepositoryReviewProfile).toHaveBeenCalled(),
    )
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).toMatchObject({
      reviewer_model: "review-model",
      account_ref: "credential:openai:work",
      max_parallel_children: 12,
      budget: { guard_expression: expression },
    })
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).not.toHaveProperty("estimated_output_tokens")
    expect(await screen.findByText("Account: Work account")).toBeVisible()
    expect(screen.getByText("Task guard configured")).toBeVisible()
    expect(screen.getByText("12 parallel workers")).toBeVisible()
  })

  it("autocompletes guard fields and opens the expression reference from help", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "review-model",
          resolved_model: "openai/review-model",
          provider: "openai",
          available: true,
          price_known: true,
          input_price_per_1m: 1,
          output_price_per_1m: 4,
        },
      ],
      accounts: [
        {
          id: "credential:openai:work",
          provider: "openai",
          label: "Work account",
          status: "available",
          default: true,
          models: ["review-model"],
          entries: [
            {
              name: "Five hour limit",
              status: "available",
              window: "5h",
              remaining_percent: 75,
            },
          ],
        },
      ],
    })
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.click(screen.getByRole("button", { name: /^Advanced/ }))

    expect(
      screen.queryByText("Guard expression reference"),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Operators: AND, OR, NOT/),
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "Guard expression help" }),
    )
    expect(screen.getByText("Guard expression reference")).toBeVisible()
    expect(screen.getByText(/Operators: AND, OR, NOT/)).toBeVisible()
    expect(screen.queryByText("Expression reference")).not.toBeInTheDocument()
    await user.keyboard("{Escape}")

    const guard = screen.getByRole("combobox", {
      name: "Guard expression",
    }) as HTMLTextAreaElement
    await user.type(guard, "spent.tok")
    expect(guard).toHaveAttribute("aria-autocomplete", "list")
    expect(guard).toHaveAttribute("aria-expanded", "true")
    expect(
      screen.getByRole("listbox", { name: "Guard expression suggestions" }),
    ).toBeVisible()
    expect(
      screen.getByRole("option", { name: /spent\.tokens\.total number field/ }),
    ).toBeVisible()

    await user.keyboard("{ArrowDown}{Enter}")
    expect(guard).toHaveValue("spent.tokens.total ")
    await user.click(screen.getByRole("option", { name: "< operator" }))
    await user.type(guard, "10 and account.limits.5")
    await user.click(
      screen.getByRole("option", {
        name: /account\.limits\.5h\.remaining_percent number field/,
      }),
    )
    expect(guard).toHaveValue(
      "spent.tokens.total < 10 and account.limits.5h.remaining_percent ",
    )

    await user.clear(guard)
    expect(screen.getByRole("option", { name: "( grouping" })).toBeVisible()
    await user.type(guard, "spent.tokens.total < t")
    expect(
      screen.queryByRole("option", { name: "true boolean" }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("option", { name: /spent\.tokens\.total number field/ }),
    ).toBeVisible()

    await user.clear(guard)
    await user.type(guard, "true = ")
    expect(screen.getByRole("option", { name: "true boolean" })).toBeVisible()
    expect(
      screen.queryByRole("option", { name: "10 number" }),
    ).not.toBeInTheDocument()

    await user.clear(guard)
    await user.type(guard, "spent.tokens.total < spend.total.usd ")
    expect(screen.getByRole("option", { name: "and keyword" })).toBeVisible()
    expect(
      screen.queryByRole("option", { name: "< operator" }),
    ).not.toBeInTheDocument()

    await user.clear(guard)
    await user.type(guard, "account.limits.known ")
    guard.setSelectionRange(8, 8)
    fireEvent.select(guard)
    await user.click(
      screen.getByRole("option", {
        name: "account.limits.known boolean field",
      }),
    )
    await user.keyboard("{End}")
    await user.type(guard, "and ")
    expect(guard).toHaveValue("account.limits.known and ")
  })

  it("shows save validation errors inside the open profile dialog", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewProfile).mockRejectedValue(
      new Error("Selected model is unavailable on this account."),
    )
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.type(screen.getByLabelText("Profile name"), "Invalid route")
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("unavailable on this account")
    expect(screen.getByRole("dialog")).toContainElement(alert)
  })

  it("does not auto-select a model for a default account with no compatible aliases", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "review-model",
          resolved_model: "openai/review-model",
          provider: "openai",
          available: true,
          price_known: true,
          input_price_per_1m: 1,
          output_price_per_1m: 4,
        },
      ],
      accounts: [
        {
          id: "empty-default",
          label: "Empty default",
          status: "available",
          default: true,
          entries: [],
          models: [],
        },
      ],
    })
    renderPage()

    await user.click(await screen.findByRole("button", { name: "New profile" }))
    await user.type(screen.getByLabelText("Profile name"), "No route")
    expect(screen.getByLabelText("Reviewer model")).toHaveValue("")
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })
})

function renderPage() {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <RepositoryReviewProfilesPage />
    </QueryClientProvider>,
  )
}
