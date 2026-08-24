import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
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
    expect(screen.queryByLabelText("Files per batch")).not.toBeInTheDocument()
    await user.click(screen.getByText(/^Advanced/))
    const files = screen.getByLabelText("Files per batch")
    await user.clear(files)
    await user.type(files, "12")
    await user.click(screen.getByText(/^Advanced/))
    expect(screen.queryByLabelText("Files per batch")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    await waitFor(() =>
      expect(createRepositoryReviewProfile).toHaveBeenCalled(),
    )
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).toMatchObject({
      name: "Core bugs",
      reviewer_model: "review-model",
      max_files_per_run: 12,
    })
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).not.toHaveProperty("reviewer_models")
  })

  it("requires central pricing and serializes guarded work", async () => {
    const user = userEvent.setup()
    vi.mocked(getRepositoryReviewAutomationOptions).mockResolvedValue({
      models: [
        {
          alias: "new-model",
          resolved_model: "provider/new-model",
          provider: "provider",
          available: true,
          price_known: false,
          input_price_per_1m: 0,
          output_price_per_1m: 0,
        },
      ],
      accounts: [],
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
    await user.type(screen.getByLabelText("Profile name"), "Unknown price")
    expect(
      screen.queryByLabelText("Maximum estimated cost ($)"),
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: /^Advanced/ }))
    expect(screen.getByLabelText("Maximum estimated cost ($)")).toHaveAttribute(
      "readonly",
    )
    expect(screen.getByLabelText("Maximum estimated cost ($)")).toHaveValue(0)
    expect(screen.getByLabelText("Maximum estimated cost ($)")).toHaveAttribute(
      "aria-describedby",
      "review-cost-pricing-help",
    )
    expect(
      screen.getByText(
        /requires pricing in the selected model's central configuration/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByRole("link", { name: /Configure pricing/i }),
    ).toHaveAttribute("href", "/models")
    expect(screen.getByLabelText("Parallel review workers")).toBeDisabled()
    expect(screen.getByLabelText("Parallel review workers")).toHaveValue(1)
    await user.click(screen.getByRole("button", { name: "Save profile" }))

    await waitFor(() =>
      expect(createRepositoryReviewProfile).toHaveBeenCalled(),
    )
    expect(
      vi.mocked(createRepositoryReviewProfile).mock.calls[0]?.[0],
    ).toMatchObject({
      reviewer_model: "new-model",
      max_parallel_children: 1,
      budget: { max_estimated_cost_usd: 0 },
    })
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
