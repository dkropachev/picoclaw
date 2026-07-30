import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type AgentInfo,
  AgentsAPIError,
  type AgentsResponse,
  createAgent,
  deleteAgent,
  getAgents,
  setDefaultAgent,
  updateAgent,
} from "@/api/agents"
import { AgentsPage } from "@/components/agent/agents/agents-page"
import { SidebarProvider } from "@/components/ui/sidebar"
import i18n from "@/i18n"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

vi.mock("@/api/agents", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/agents")>()
  return {
    ...actual,
    getAgents: vi.fn(),
    createAgent: vi.fn(),
    updateAgent: vi.fn(),
    deleteAgent: vi.fn(),
    setDefaultAgent: vi.fn(),
  }
})

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...actual,
    useBlocker: vi.fn(() => ({
      status: "idle",
      current: undefined,
      next: undefined,
      action: undefined,
      proceed: undefined,
      reset: undefined,
    })),
  }
})

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
  },
}))

describe("AgentsPage", () => {
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
    Object.defineProperty(window, "requestAnimationFrame", {
      writable: true,
      value: (callback: FrameRequestCallback) => {
        callback(0)
        return 0
      },
    })
  })

  beforeEach(async () => {
    await i18n.changeLanguage("en")
    vi.mocked(getAgents).mockReset()
    vi.mocked(createAgent).mockReset()
    vi.mocked(updateAgent).mockReset()
    vi.mocked(deleteAgent).mockReset()
    vi.mocked(setDefaultAgent).mockReset()
    vi.mocked(refreshGatewayState).mockReset()
    vi.mocked(showSaveSuccessOrRestartToast).mockReset()
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("preserves server order and exposes the virtual main agent safely", async () => {
    vi.mocked(getAgents).mockResolvedValue(
      collection(
        [
          agent({ id: "reviewer", name: "Reviewer", is_default: false }),
          agent({
            id: "main",
            name: "",
            implicit: true,
            default_configured: false,
          }),
        ],
        "revision-1",
      ),
    )

    const { container } = renderPage()

    await screen.findByText("Reviewer")
    expect(
      [...container.querySelectorAll("[data-agent-id]")].map((card) =>
        card.getAttribute("data-agent-id"),
      ),
    ).toEqual(["reviewer", "main"])

    const mainCard = container.querySelector('[data-agent-id="main"]')
    expect(mainCard).not.toBeNull()
    expect(within(mainCard as HTMLElement).getByText("Implicit")).toBeVisible()
    expect(
      within(mainCard as HTMLElement).getByRole("button", {
        name: "Delete main",
      }),
    ).toBeDisabled()
  })

  it("creates an agent with the opening revision and surfaces restart-required effects", async () => {
    const initial = collection(
      [agent({ implicit: true, default_configured: false })],
      "revision-1",
    )
    const created = collection(
      [
        agent({ implicit: false, default_configured: true }),
        agent({
          id: "reviewer",
          name: "Reviewer",
          is_default: false,
        }),
      ],
      "revision-2",
      "restart_required",
    )
    vi.mocked(getAgents).mockResolvedValue(initial)
    vi.mocked(createAgent).mockResolvedValue(created)
    const user = userEvent.setup()

    renderPage()
    await user.click(
      await screen.findByRole("button", { name: "Create agent" }),
    )

    const sheet = screen.getByRole("dialog", { name: "Create agent" })
    expect(sheet).toHaveClass("data-[side=right]:!w-full")
    expect(sheet).toHaveClass("data-[side=right]:sm:!max-w-xl")

    await user.type(screen.getByLabelText("Agent ID"), "reviewer")
    await user.type(screen.getByLabelText("Configured name"), "Reviewer")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(createAgent).toHaveBeenCalledWith("revision-1", {
        id: "reviewer",
        name: "Reviewer",
        workspace: "",
        model: null,
        skills: null,
        subagents: null,
      }),
    )
    await waitFor(() =>
      expect(showSaveSuccessOrRestartToast).toHaveBeenCalledWith(
        expect.any(Function),
        "Created reviewer.",
        "reviewer",
        true,
      ),
    )
    expect(refreshGatewayState).toHaveBeenCalledWith({ force: true })
    expect(
      screen.queryByRole("dialog", { name: "Create agent" }),
    ).not.toBeInTheDocument()
  })

  it("keeps a CAS-conflicted draft until an explicit reload and then uses the latest revision", async () => {
    const originalAgent = agent({
      id: "reviewer",
      name: "Reviewer",
      is_default: false,
      model: { primary: "", fallbacks: null },
    })
    const latestAgent = {
      ...originalAgent,
      name: "Server version",
    }
    const initial = collection([originalAgent], "revision-1")
    const latest = collection([latestAgent], "revision-2")
    const saved = collection(
      [{ ...latestAgent, name: "Reviewed version" }],
      "revision-3",
    )
    vi.mocked(getAgents)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest)
    vi.mocked(updateAgent)
      .mockRejectedValueOnce(
        new AgentsAPIError("config_revision_mismatch", 409, {
          code: "config_revision_mismatch",
        }),
      )
      .mockResolvedValueOnce(saved)
    const user = userEvent.setup()
    const { container } = renderPage()

    await screen.findByText("Reviewer")
    const reviewerCard = container.querySelector('[data-agent-id="reviewer"]')
    await user.click(
      within(reviewerCard as HTMLElement).getByRole("button", {
        name: "Edit reviewer",
      }),
    )
    const nameInput = screen.getByLabelText("Configured name")
    await user.clear(nameInput)
    await user.type(nameInput, "Draft version")
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(updateAgent).toHaveBeenNthCalledWith(
      1,
      "reviewer",
      "revision-1",
      expect.objectContaining({
        name: "Draft version",
        model: { primary: "", fallbacks: null },
      }),
    )
    expect(nameInput).toHaveValue("Draft version")
    expect(
      await screen.findByRole("button", {
        name: "Reload latest configuration",
      }),
    ).toBeVisible()
    expect(updateAgent).toHaveBeenCalledTimes(1)

    await user.click(
      screen.getByRole("button", { name: "Reload latest configuration" }),
    )
    expect(nameInput).toHaveValue("Server version")
    await user.clear(nameInput)
    await user.type(nameInput, "Reviewed version")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(updateAgent).toHaveBeenNthCalledWith(
        2,
        "reviewer",
        "revision-2",
        expect.objectContaining({
          name: "Reviewed version",
          model: { primary: "", fallbacks: null },
        }),
      ),
    )
  })

  it("sets the default and deletes with captured revisions while keeping response order", async () => {
    const reviewer = agent({
      id: "reviewer",
      name: "Reviewer",
      is_default: false,
    })
    const writer = agent({
      id: "writer",
      name: "Writer",
      is_default: false,
    })
    const main = agent()
    const initial = collection([reviewer, main, writer], "revision-1")
    const defaultChanged = collection(
      [
        writer,
        { ...reviewer, is_default: true },
        { ...main, is_default: false },
      ],
      "revision-2",
    )
    const deleted = collection(
      [
        { ...main, is_default: false },
        { ...reviewer, is_default: true },
      ],
      "revision-3",
    )
    vi.mocked(getAgents).mockResolvedValue(initial)
    vi.mocked(setDefaultAgent).mockResolvedValue(defaultChanged)
    vi.mocked(deleteAgent).mockResolvedValue(deleted)
    const user = userEvent.setup()
    const { container } = renderPage()

    await screen.findByText("Reviewer")
    const reviewerCard = container.querySelector('[data-agent-id="reviewer"]')
    await user.click(
      within(reviewerCard as HTMLElement).getByRole("button", {
        name: "Set default",
      }),
    )

    await waitFor(() =>
      expect(setDefaultAgent).toHaveBeenCalledWith("reviewer", "revision-1"),
    )
    await waitFor(() =>
      expect(cardOrder(container)).toEqual(["writer", "reviewer", "main"]),
    )

    const writerCard = container.querySelector('[data-agent-id="writer"]')
    await user.click(
      within(writerCard as HTMLElement).getByRole("button", {
        name: "Delete writer",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Delete agent" }))

    await waitFor(() =>
      expect(deleteAgent).toHaveBeenCalledWith("writer", "revision-2"),
    )
    await waitFor(() =>
      expect(cardOrder(container)).toEqual(["main", "reviewer"]),
    )
  })

  it("requires reopening delete from the latest list after a CAS conflict", async () => {
    const originalReviewer = agent({
      id: "reviewer",
      name: "Reviewer",
      workspace: "/workspace/reviewer",
      is_default: false,
    })
    const replacementReviewer = {
      ...originalReviewer,
      name: "Replacement reviewer",
      workspace: "/workspace/replacement",
    }
    const main = agent()
    const initial = collection([main, originalReviewer], "revision-1")
    const latest = collection([main, replacementReviewer], "revision-2")
    const deleted = collection([main], "revision-3")
    vi.mocked(getAgents)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest)
    vi.mocked(deleteAgent)
      .mockRejectedValueOnce(
        new AgentsAPIError("config_revision_mismatch", 409, {
          code: "config_revision_mismatch",
        }),
      )
      .mockResolvedValueOnce(deleted)
    const user = userEvent.setup()
    const { container } = renderPage()

    await screen.findByText("Reviewer")
    let reviewerCard = container.querySelector('[data-agent-id="reviewer"]')
    await user.click(
      within(reviewerCard as HTMLElement).getByRole("button", {
        name: "Delete reviewer",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Delete agent" }))

    expect(
      await screen.findByRole("button", { name: "Close and review latest" }),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Delete agent" })).toBeDisabled()
    expect(deleteAgent).toHaveBeenCalledTimes(1)
    expect(deleteAgent).toHaveBeenNthCalledWith(1, "reviewer", "revision-1")

    await user.click(
      screen.getByRole("button", { name: "Close and review latest" }),
    )
    expect(
      screen.queryByRole("alertdialog", { name: "Delete agent?" }),
    ).not.toBeInTheDocument()

    reviewerCard = container.querySelector('[data-agent-id="reviewer"]')
    expect(
      within(reviewerCard as HTMLElement).getByText("Replacement reviewer"),
    ).toBeVisible()
    expect(
      within(reviewerCard as HTMLElement).getByText("/workspace/replacement"),
    ).toBeVisible()

    await user.click(
      within(reviewerCard as HTMLElement).getByRole("button", {
        name: "Delete reviewer",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Delete agent" }))

    await waitFor(() =>
      expect(deleteAgent).toHaveBeenNthCalledWith(2, "reviewer", "revision-2"),
    )
    await waitFor(() =>
      expect(container.querySelector('[data-agent-id="reviewer"]')).toBeNull(),
    )
  })

  it("keeps a referenced-agent deletion open without offering CAS reload", async () => {
    const reviewer = agent({
      id: "reviewer",
      name: "Reviewer",
      is_default: false,
    })
    vi.mocked(getAgents).mockResolvedValue(
      collection([agent(), reviewer], "revision-1"),
    )
    vi.mocked(deleteAgent).mockRejectedValue(
      new AgentsAPIError("agent_referenced", 409, {
        code: "agent_referenced",
        blockers: [
          { kind: "dispatch_rule", name: "review-route" },
          { kind: "delegation", agent_id: "main" },
        ],
      }),
    )
    const user = userEvent.setup()
    const { container } = renderPage()

    await screen.findByText("Reviewer")
    const reviewerCard = container.querySelector('[data-agent-id="reviewer"]')
    await user.click(
      within(reviewerCard as HTMLElement).getByRole("button", {
        name: "Delete reviewer",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Delete agent" }))

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Agent referenced.",
    )
    expect(screen.getByRole("alert")).toHaveTextContent(
      "dispatch rule: review-route",
    )
    expect(
      screen.getByRole("alertdialog", { name: "Delete agent?" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Close and review latest" }),
    ).not.toBeInTheDocument()
    expect(deleteAgent).toHaveBeenCalledWith("reviewer", "revision-1")
    expect(getAgents).toHaveBeenCalledTimes(1)
  })
})

function cardOrder(container: HTMLElement): Array<string | null> {
  return [...container.querySelectorAll("[data-agent-id]")].map((card) =>
    card.getAttribute("data-agent-id"),
  )
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <AgentsPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

function agent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    id: "main",
    name: "Main",
    workspace: "",
    model: null,
    skills: null,
    subagents: null,
    is_default: true,
    default_configured: true,
    implicit: false,
    ...overrides,
  }
}

function collection(
  agents: AgentInfo[],
  revision: string,
  gatewayEffect: "applied" | "restart_required" = "applied",
): AgentsResponse {
  return {
    agents,
    default_agent_id:
      agents.find((candidate) => candidate.is_default)?.id ?? "main",
    config_revision: revision,
    effects: {
      launcher_effect: "applied",
      catalog_effect: "applied",
      gateway_effect: gatewayEffect,
    },
  }
}
