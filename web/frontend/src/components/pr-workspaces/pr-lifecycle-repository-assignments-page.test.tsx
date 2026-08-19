import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type PRLifecycleRepositoryAssignmentSnapshot,
  getPRLifecycleRepositoryAssignments,
  putPRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"
import { createPRWorkspaceRequestID } from "@/api/pr-workspaces"
import { PRLifecycleRepositoryAssignmentsPage } from "@/components/pr-workspaces/pr-lifecycle-repository-assignments-page"
import { SidebarProvider } from "@/components/ui/sidebar"

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...original,
    useBlocker: vi.fn(() => ({ status: "idle" })),
  }
})

vi.mock("@/api/pr-lifecycle-repository-assignments", async (importOriginal) => {
  const original =
    await importOriginal<
      typeof import("@/api/pr-lifecycle-repository-assignments")
    >()
  return {
    ...original,
    getPRLifecycleRepositoryAssignments: vi.fn(),
    putPRLifecycleRepositoryAssignments: vi.fn(),
  }
})

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return {
    ...original,
    createPRWorkspaceRequestID: vi.fn(() => "request-1"),
  }
})

const snapshot: PRLifecycleRepositoryAssignmentSnapshot = {
  workflowConfigurations: {
    default: { name: "Default", deferredIssues: { mode: "ask" } },
    strict: { name: "Strict", deferredIssues: { mode: "automatic" } },
  },
  defaultWorkflowConfiguration: "default",
  repositoryAssignments: {
    "https://github.com|100": "strict",
  },
  configRevision: "config-1",
  effects: {
    gatewayEffect: "applied",
    deferredPolicyEffect: "applied",
  },
}

const mockedGet = vi.mocked(getPRLifecycleRepositoryAssignments)
const mockedPut = vi.mocked(putPRLifecycleRepositoryAssignments)

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <PRLifecycleRepositoryAssignmentsPage onBack={vi.fn()} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

describe("PR lifecycle repository assignments page", () => {
  beforeAll(() => {
    Object.defineProperties(Element.prototype, {
      hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.clearAllMocks()
    mockedGet.mockResolvedValue(structuredClone(snapshot))
    mockedPut.mockImplementation(async (input) => ({
      ...structuredClone(snapshot),
      repositoryAssignments: structuredClone(input.repositoryAssignments),
      configRevision: "config-2",
    }))
    vi.mocked(createPRWorkspaceRequestID).mockReturnValue("request-1")
  })

  it("owns repository routing without exposing workflow editing controls", async () => {
    renderPage()

    expect(
      await screen.findByRole("heading", { name: "Repository assignments" }),
    ).toBeVisible()
    expect(screen.getByText("https://github.com|100")).toBeVisible()
    expect(
      screen.getByRole("combobox", {
        name: "Workflow configuration for https://github.com|100",
      }),
    ).toHaveTextContent("Strict")
    expect(
      screen.queryByRole("button", { name: /edit .*workflow configuration/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("textbox", { name: "Configuration name" }),
    ).not.toBeInTheDocument()
  })

  it("adds and saves assignments through the assignment-only mutation", async () => {
    const user = userEvent.setup()
    renderPage()

    await user.type(
      await screen.findByRole("textbox", { name: "Repository identity" }),
      "https://github.com|200",
    )
    await user.click(
      screen.getByRole("combobox", { name: "Workflow configuration" }),
    )
    await user.click(screen.getByRole("option", { name: "Strict" }))
    await user.click(screen.getByRole("button", { name: "Add assignment" }))
    await user.click(screen.getByRole("button", { name: "Save assignments" }))

    await waitFor(() =>
      expect(mockedPut).toHaveBeenCalledWith({
        expectedConfigRevision: "config-1",
        requestID: "request-1",
        repositoryAssignments: {
          "https://github.com|100": "strict",
          "https://github.com|200": "strict",
        },
      }),
    )
  })

  it("blocks invalid and canonically colliding repository identities", async () => {
    const user = userEvent.setup()
    renderPage()

    const identity = await screen.findByRole("textbox", {
      name: "Repository identity",
    })
    const add = screen.getByRole("button", { name: "Add assignment" })
    const save = screen.getByRole("button", { name: "Save assignments" })

    await user.type(identity, " http://github.com|200")
    expect(screen.getByRole("alert")).toHaveTextContent(/exact https:\/\//i)
    expect(add).toBeDisabled()
    expect(save).toBeDisabled()

    await user.clear(identity)
    await user.type(identity, "HTTPS://GITHUB.COM///|100")
    expect(screen.getByRole("alert")).toHaveTextContent(
      /collides.*https:\/\/github\.com\|100/i,
    )
    expect(add).toBeDisabled()
    expect(save).toBeDisabled()
  })
})
