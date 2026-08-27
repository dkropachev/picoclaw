import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createRepositoryReviewProfile,
  getRepositoryReviewAutomationOptions,
  getRepositoryReviewProfile,
  listRepositoryReviewProfilesPage,
  updateRepositoryReviewProfile,
} from "@/api/repository-reviews"
import {
  RepositoryReviewProfileEditorPage,
  RepositoryReviewProfilesPage,
} from "@/components/repository-reviews/repository-review-profiles-page"

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
  RepositoryReviewAPIError: class RepositoryReviewAPIError extends Error {
    status = 500
  },
  createRepositoryReviewProfile: vi.fn(),
  getRepositoryReviewAutomationOptions: vi.fn(),
  getRepositoryReviewProfile: vi.fn(),
  listRepositoryReviewProfilesPage: vi.fn(),
  repositoryReviewDefaultIssuePrompt:
    "Present confirmed diagnosis with evidence and provenance.",
  updateRepositoryReviewProfile: vi.fn(),
}))

describe("RepositoryReviewProfilesPage", () => {
  beforeEach(() => {
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
      accounts: [
        {
          id: "default-account",
          label: "Default account",
          status: "available",
          available: true,
          default: true,
          entries: [],
          models: ["review-model"],
        },
      ],
    })
    vi.mocked(createRepositoryReviewProfile).mockReset()
    vi.mocked(getRepositoryReviewProfile).mockReset()
    vi.mocked(listRepositoryReviewProfilesPage).mockReset()
    vi.mocked(updateRepositoryReviewProfile).mockReset()
  })

  it("uses the shared collection with canonical query and all three views", async () => {
    const user = userEvent.setup()
    const profile = storedProfile("", "review-model")
    vi.mocked(listRepositoryReviewProfilesPage).mockResolvedValue({
      profiles: [profile],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY name ASC",
      query_schema: { fields: [] },
    })
    const onSearchChange = vi.fn()
    const onOpen = vi.fn()
    renderCollection(onSearchChange, onOpen)

    expect(await screen.findByText("Stale profile")).toBeVisible()
    expect(
      document.querySelector('[data-slot="collection-shell"]'),
    ).not.toBeNull()
    await user.click(screen.getByRole("button", { name: "Grid view" }))
    expect(onSearchChange).toHaveBeenCalledWith(
      { q: "ALL ORDER BY name ASC", view: "grid" },
      true,
    )
    await user.dblClick(screen.getByText("Stale profile"))
    expect(onOpen).toHaveBeenCalledWith(profile)
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

    await user.type(await screen.findByLabelText("Profile name"), "Core bugs")
    expect(await screen.findByLabelText("Execution account")).toBeVisible()
    expect(screen.getByLabelText("Reviewer model")).toBeVisible()
    expect(
      screen.getByText(
        "Narrows defect classes only. Findings diagnose validated defects and never include fixes or remediation.",
      ),
    ).toBeVisible()
    expect(screen.getByLabelText("Review focus")).toHaveAttribute(
      "aria-describedby",
      "review-focus-help",
    )
    expect(screen.getByLabelText("Issue prompt")).toHaveValue(
      "Present confirmed diagnosis with evidence and provenance.",
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
    expect(await screen.findByLabelText("Execution account")).toHaveValue("")
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
      issue_prompt: "Present confirmed diagnosis with evidence and provenance.",
      max_files_per_run: 12,
      max_parallel_children: 6,
      scope_policy: { include_folders: ["pkg/core"] },
      budget: { guard_expression: "" },
    })
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).not.toHaveProperty("reviewer_models")
  })

  it("keeps blank writer inheritance and saves an explicit compatible writer", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [modelOption("review-model"), modelOption("writer-model")],
      accounts: [
        accountOption(
          "default-account",
          "Default account",
          ["review-model", "writer-model"],
          true,
        ),
      ],
    })
    vi.mocked(createRepositoryReviewProfile).mockImplementation(
      async (value) => ({
        ...value,
        id: "profile_writer",
        version: 1,
        created_at: "2026-08-23T00:00:00Z",
        updated_at: "2026-08-23T00:00:00Z",
      }),
    )
    renderPage()

    await user.type(await screen.findByLabelText("Profile name"), "Writer")
    const writer = screen.getByLabelText("Issue writer model")
    expect(writer).toHaveValue("")
    expect(
      within(writer).getByRole("option", {
        name: "Same as reviewer (review-model)",
      }),
    ).toBeVisible()
    await user.selectOptions(writer, "writer-model")
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    await waitFor(() =>
      expect(createRepositoryReviewProfile).toHaveBeenCalledWith(
        expect.objectContaining({
          reviewer_model: "review-model",
          issue_writer_model: "writer-model",
        }),
      ),
    )
  })

  it("allows an account-passive writer alias without exposing it as a reviewer", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        modelOption("review-model"),
        {
          ...modelOption("account-writer"),
          available: false,
          blocked_reason: "The default route is agentic.",
        },
      ],
      accounts: [
        accountOption(
          "default-account",
          "Default account",
          ["review-model"],
          true,
          ["review-model", "account-writer"],
        ),
      ],
    })
    renderPage()

    await user.type(await screen.findByLabelText("Profile name"), "Writer")
    const reviewer = screen.getByLabelText("Reviewer model")
    const writer = screen.getByLabelText("Issue writer model")
    expect(
      within(reviewer).getByRole("option", {
        name: "account-writer (The default route is agentic.)",
      }),
    ).toBeDisabled()
    expect(
      within(writer).getByRole("option", { name: "account-writer" }),
    ).toBeEnabled()
    await user.selectOptions(writer, "account-writer")
    expect(screen.getByRole("button", { name: "Save profile" })).toBeEnabled()
  })

  it("validates always-visible code scope without opening advanced", async () => {
    const user = userEvent.setup()
    renderPage()

    await user.type(
      await screen.findByLabelText("Profile name"),
      "Scoped review",
    )
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
          available: true,
          entries: [],
          models: ["review-model"],
        },
        {
          id: "credential:openai:empty",
          provider: "openai",
          label: "No models account",
          status: "available",
          available: true,
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

    await user.type(
      await screen.findByLabelText("Profile name"),
      "Guarded review",
    )
    const account = await screen.findByLabelText("Execution account")
    expect(account).toBeVisible()
    expect(account).toHaveValue("credential:openai:work")
    expect(
      screen.getByRole("option", { name: "Default account (unavailable)" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("option", { name: "Work account · openai" }),
    ).toHaveValue("credential:openai:work")
    expect(
      screen.getByRole("option", {
        name: "No models account · openai (no available reviewer models)",
      }),
    ).toBeDisabled()
    await user.click(await screen.findByRole("button", { name: /^Advanced/ }))

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
          available: true,
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

    await user.click(await screen.findByRole("button", { name: /^Advanced/ }))

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

  it("filters reviewer models by account and preserves or replaces the selection safely", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        modelOption("safe-a"),
        modelOption("safe-b"),
        {
          ...modelOption("blocked"),
          available: false,
          blocked_reason:
            "Agentic CLI models are not allowed for immutable repository review.",
        },
      ],
      accounts: [
        accountOption("primary", "Primary", ["safe-a", "blocked"], true),
        accountOption("backup", "Backup", ["safe-a", "safe-b"]),
        accountOption("secondary", "Secondary", ["safe-b"]),
        accountOption("blocked-only", "Blocked only", ["blocked"]),
      ],
    })
    renderPage()

    const account = await screen.findByLabelText("Execution account")
    const model = screen.getByLabelText("Reviewer model")
    expect(account).toHaveValue("")
    expect(model).toHaveValue("safe-a")
    expect(within(model).getByRole("option", { name: "safe-a" })).toBeEnabled()
    expect(
      within(model).getByRole("option", {
        name: "safe-b (Reviewer model is unavailable on Primary.)",
      }),
    ).toBeDisabled()
    expect(
      within(model).getByRole("option", {
        name: /blocked \(Agentic CLI models are not allowed/,
      }),
    ).toBeDisabled()
    expect(
      screen.getByRole("option", {
        name: "Blocked only · openai (no available reviewer models)",
      }),
    ).toBeDisabled()

    await user.selectOptions(account, "backup")
    expect(model).toHaveValue("safe-a")
    await user.selectOptions(model, "safe-b")
    await user.selectOptions(account, "")
    expect(model).toHaveValue("safe-a")
    await user.selectOptions(account, "secondary")
    expect(model).toHaveValue("safe-b")
  })

  it("fails closed when global and account availability contradict each other", async () => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          ...modelOption("blocked"),
          available: false,
          blocked_reason: "Reviewer route is blocked by policy.",
        },
      ],
      accounts: [accountOption("primary", "Primary", ["blocked"], true)],
    })
    renderPage()

    expect(await screen.findByLabelText("Execution account")).toHaveValue("")
    const model = screen.getByLabelText("Reviewer model")
    expect(model).toBeEnabled()
    expect(
      within(model).getByRole("option", {
        name: "blocked (Reviewer route is blocked by policy.)",
      }),
    ).toBeDisabled()
    expect(
      screen.getByText("No reviewer models are available on Primary."),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })

  it("skips an unavailable default credential but keeps telemetry-error accounts selectable", async () => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [modelOption("safe")],
      accounts: [
        {
          ...accountOption("expired", "Expired", ["safe"], true),
          status: "invalid",
          available: false,
        },
        {
          ...accountOption("telemetry-error", "Telemetry error", ["safe"]),
          status: "error",
          available: true,
        },
      ],
    })
    renderPage()

    const account = await screen.findByLabelText("Execution account")
    expect(account).toHaveValue("telemetry-error")
    expect(screen.getByLabelText("Reviewer model")).toHaveValue("safe")
    expect(
      screen.getByRole("option", {
        name: "Default account (currently Expired) (invalid)",
      }),
    ).toBeDisabled()
    expect(
      screen.getByRole("option", { name: "Expired · openai (invalid)" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("option", { name: "Telemetry error · openai" }),
    ).toBeEnabled()
  })

  it("shows stale account and model values and repairs both through account selection", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewProfile).mockResolvedValue(
      storedProfile("missing-account", "missing-model"),
    )
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [modelOption("safe")],
      accounts: [
        accountOption("valid-account", "Valid account", ["safe"], true),
      ],
    })
    renderPage(undefined, "profile_stale")
    const account = await screen.findByLabelText("Execution account")
    const model = screen.getByLabelText("Reviewer model")
    expect(account).toHaveValue("missing-account")
    expect(
      screen.getByRole("option", {
        name: "missing-account (unavailable)",
      }),
    ).toBeDisabled()
    expect(model).toHaveValue("missing-model")
    expect(
      screen.getByRole("option", { name: "missing-model (unavailable)" }),
    ).toBeDisabled()
    expect(
      screen.getByText(
        "Execution account missing-account is no longer available.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()

    await user.selectOptions(account, "valid-account")
    expect(model).toHaveValue("safe")
    expect(screen.queryByText(/is no longer available/)).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeEnabled()
  })

  it("explains missing execution routes and disables profile creation", async () => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [modelOption("safe")],
      accounts: [],
    })
    renderPage()

    expect(await screen.findByLabelText("Execution account")).toBeVisible()
    expect(
      screen.getByRole("option", { name: "Default account (unavailable)" }),
    ).toBeDisabled()
    expect(
      screen.getByText(
        "No default execution account is available. Choose an account.",
      ),
    ).toBeVisible()
    expect(screen.getByLabelText("Reviewer model")).toBeDisabled()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })

  it("explains an empty reviewer model catalog", async () => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [],
      accounts: [accountOption("primary", "Primary", [], true)],
    })
    renderPage()

    expect(await screen.findByLabelText("Execution account")).toHaveValue("")
    expect(screen.getByLabelText("Reviewer model")).toBeEnabled()
    expect(
      screen.getByText("No reviewer model aliases are configured."),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })

  it("keeps profile editing unavailable when model and account options fail to load", async () => {
    vi.mocked(getRepositoryReviewAutomationOptions).mockRejectedValue(
      new Error("options offline"),
    )
    renderPage()

    expect(await screen.findByText("Details could not be loaded")).toBeVisible()
    expect(screen.getByText("options offline")).toBeVisible()
  })

  it("disables saving when a refresh fails instead of trusting cached options", async () => {
    const user = userEvent.setup()
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    renderPage(queryClient)

    await user.type(
      await screen.findByLabelText("Profile name"),
      "Cached options",
    )
    expect(screen.getByRole("button", { name: "Save profile" })).toBeEnabled()

    vi.mocked(getRepositoryReviewAutomationOptions).mockRejectedValueOnce(
      new Error("refresh failed"),
    )
    await queryClient.refetchQueries({
      queryKey: ["repository-review-automation-options"],
    })
    expect(
      await screen.findByText(
        "Reviewer models and execution accounts could not be refreshed. Refresh before saving.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })

  it("shows save validation errors inside the profile editor", async () => {
    const user = userEvent.setup()
    vi.mocked(createRepositoryReviewProfile).mockRejectedValue(
      new Error("Selected model is unavailable on this account."),
    )
    renderPage()

    await user.type(
      await screen.findByLabelText("Profile name"),
      "Invalid route",
    )
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("unavailable on this account")
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
          available: true,
          default: true,
          entries: [],
          models: [],
        },
      ],
    })
    renderPage()

    await user.type(await screen.findByLabelText("Profile name"), "No route")
    expect(await screen.findByLabelText("Execution account")).toBeVisible()
    expect(screen.getByLabelText("Reviewer model")).toHaveValue("")
    expect(screen.getByLabelText("Reviewer model")).toBeEnabled()
    expect(
      screen.getByText("No reviewer models are available on Empty default."),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profile" })).toBeDisabled()
  })
})

function renderPage(
  queryClient: QueryClient | undefined = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  }),
  profileID?: string,
) {
  const client =
    queryClient ??
    new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <RepositoryReviewProfileEditorPage
        profileID={profileID}
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

function renderCollection(onSearchChange = vi.fn(), onOpen = vi.fn()) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <RepositoryReviewProfilesPage
        search={{ q: "ALL ORDER BY name ASC" }}
        onSearchChange={onSearchChange}
        onAdd={vi.fn()}
        onOpen={onOpen}
        onEdit={vi.fn()}
      />
    </QueryClientProvider>,
  )
}

function modelOption(alias: string) {
  return {
    alias,
    resolved_model: `openai/${alias}`,
    provider: "openai",
    available: true,
    price_known: true,
    input_price_per_1m: 1,
    output_price_per_1m: 4,
  }
}

function accountOption(
  id: string,
  label: string,
  models: string[],
  defaultAccount = false,
  writerModels: string[] = models,
) {
  return {
    id,
    label,
    provider: "openai",
    status: "available",
    available: true,
    default: defaultAccount,
    entries: [],
    models,
    writer_models: writerModels,
  }
}

function storedProfile(accountRef: string, reviewerModel: string) {
  return {
    id: "profile_stale",
    version: 1,
    name: "Stale profile",
    account_ref: accountRef,
    reviewer_model: reviewerModel,
    review_focus: "Find correctness bugs.",
    issue_prompt: "Present confirmed diagnosis with evidence and provenance.",
    force: false,
    auto_continue: true,
    max_files_per_run: 24,
    max_content_bytes: 524_288,
    max_parallel_children: 8,
    scope_policy: {
      code_types: ["code" as const],
      include_folders: [],
      exclude_folders: [],
      free_text: "",
    },
    budget: { guard_expression: "" },
    created_at: "2026-08-23T00:00:00Z",
    updated_at: "2026-08-23T00:00:00Z",
  }
}
