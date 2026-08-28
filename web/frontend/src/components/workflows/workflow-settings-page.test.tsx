import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { WorkflowSettingsPage } from "./workflow-settings-page"

const mocks = vi.hoisted(() => ({
  getWorkflowSettings: vi.fn(),
  patchWorkflowSettings: vi.fn(),
  reloadWorkflows: vi.fn(),
}))
vi.mock("@/api/workflows", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/workflows")>()),
  ...mocks,
}))
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

function settings(revision = "settings-1", definitions = "workflows") {
  const values = {
    enabled: true,
    tool_enabled: true,
    definitions_dir: definitions,
    max_concurrent_runs: 4,
    default_timeout_seconds: 300,
    max_call_depth: 4,
    retention_days: 30,
  }
  return {
    configured: values,
    effective: values,
    config_revision: revision,
    effects: {
      launcher_effect: "applied",
      catalog_effect: "applied",
      gateway_effect: "applied",
    },
  }
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <WorkflowSettingsPage onBack={vi.fn()} />
    </QueryClientProvider>,
  )
  return client
}

describe("routed workflow settings", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getWorkflowSettings.mockResolvedValue(settings())
    mocks.patchWorkflowSettings.mockImplementation(async (value) => ({
      ...settings("settings-2", value.definitions_dir),
      configured: {
        ...settings().configured,
        ...value,
      },
    }))
    mocks.reloadWorkflows.mockResolvedValue({
      reloaded_at: "2026-08-01T00:00:00Z",
      workflows: [],
      errors: [],
    })
  })

  it("saves the exact old revision and adopts the returned revision", async () => {
    const user = userEvent.setup()
    renderPage()
    const directory = await screen.findByLabelText("Definitions directory")
    await user.clear(directory)
    await user.type(directory, "automation")
    await user.click(screen.getByRole("button", { name: "Save settings" }))
    await waitFor(() =>
      expect(mocks.patchWorkflowSettings).toHaveBeenCalledWith(
        expect.objectContaining({
          definitions_dir: "automation",
          expected_config_revision: "settings-1",
        }),
      ),
    )
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Save settings" }),
      ).toBeDisabled(),
    )
  })

  it("disables saving for values above runtime maxima", async () => {
    const user = userEvent.setup()
    renderPage()
    const concurrency = await screen.findByLabelText("Concurrent runs")
    await user.clear(concurrency)
    await user.type(concurrency, "1025")
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled()
  })

  it("preserves a dirty draft across an external revision until explicit reload", async () => {
    const user = userEvent.setup()
    const client = renderPage()
    const directory = await screen.findByLabelText("Definitions directory")
    await user.clear(directory)
    await user.type(directory, "local-draft")
    client.setQueryData(
      ["workflows", "settings"],
      settings("settings-2", "server"),
    )

    expect(
      await screen.findByText(
        "Workflow settings changed elsewhere. Reload latest values before saving.",
      ),
    ).toBeVisible()
    expect(directory).toHaveValue("local-draft")
    await user.click(
      screen.getByRole("button", { name: "Reload latest values" }),
    )
    expect(directory).toHaveValue("server")
  })
})
