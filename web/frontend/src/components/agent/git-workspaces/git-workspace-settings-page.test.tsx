import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { GitWorkspaceSettingsPage } from "./git-workspace-settings-page"

const mocks = vi.hoisted(() => ({
  getGitWorkspaceSettings: vi.fn(),
  updateGitWorkspaceSettings: vi.fn(),
}))
vi.mock("@/api/git-workspaces", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/git-workspaces")>()),
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

function response(revision = "revision-1", size = 1024) {
  return {
    configured: {
      max_total_size_bytes: size,
      ignored_cleanup_delay_seconds: 3600,
      drop_delay_seconds: 86400,
    },
    effective: {
      max_total_size_bytes: size || 20 * 1024 ** 3,
      ignored_cleanup_delay_seconds: 3600,
      drop_delay_seconds: 86400,
    },
    config_revision: revision,
  }
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <GitWorkspaceSettingsPage onBack={vi.fn()} />
    </QueryClientProvider>,
  )
  return client
}

describe("git workspace settings", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getGitWorkspaceSettings.mockResolvedValue(response())
    mocks.updateGitWorkspaceSettings.mockImplementation(async (settings) => ({
      ...response("revision-2", settings.max_total_size_bytes),
      configured: settings,
    }))
  })

  it("saves exact values against the loaded revision", async () => {
    const user = userEvent.setup()
    renderPage()
    const size = await screen.findByLabelText("Maximum total size (bytes)")
    await user.clear(size)
    await user.type(size, "2048")
    await user.click(screen.getByRole("button", { name: "Save settings" }))
    await waitFor(() =>
      expect(mocks.updateGitWorkspaceSettings).toHaveBeenCalledWith(
        {
          max_total_size_bytes: 2048,
          ignored_cleanup_delay_seconds: 3600,
          drop_delay_seconds: 86400,
        },
        "revision-1",
      ),
    )
  })

  it("rejects unsafe delay values locally", async () => {
    const user = userEvent.setup()
    renderPage()
    const delay = await screen.findByLabelText("Drop delay (seconds)")
    await user.clear(delay)
    await user.type(delay, "2147483648")
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled()
  })

  it("preserves dirty values across external revision until explicit reload", async () => {
    const user = userEvent.setup()
    const client = renderPage()
    const size = await screen.findByLabelText("Maximum total size (bytes)")
    await user.clear(size)
    await user.type(size, "2048")
    client.setQueryData(
      ["git-workspaces", "settings"],
      response("revision-2", 4096),
    )
    expect(
      await screen.findByText(/Git workspace settings changed elsewhere/),
    ).toBeVisible()
    expect(size).toHaveValue(2048)
    await user.click(
      screen.getByRole("button", { name: "Reload latest values" }),
    )
    expect(size).toHaveValue(4096)
  })
})
