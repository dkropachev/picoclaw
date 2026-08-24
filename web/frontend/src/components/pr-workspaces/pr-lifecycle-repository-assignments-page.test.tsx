import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { createDevelopmentRequestID } from "@/api/development-workspaces"
import {
  type PRLifecycleRepositoryAssignmentSnapshot,
  getPRLifecycleRepositoryAssignments,
  putPRLifecycleRepositoryAssignments,
  resolveDevelopmentRepository,
} from "@/api/pr-lifecycle-repository-assignments"
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
    resolveDevelopmentRepository: vi.fn(),
  }
})

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    createDevelopmentRequestID: vi.fn(() => "request-1"),
  }
})

const snapshot: PRLifecycleRepositoryAssignmentSnapshot = {
  repositories: {
    "https://github.com|100": { name: "octo/current", defaultBranch: "main" },
  },
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
const mockedResolve = vi.mocked(resolveDevelopmentRepository)

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
      repositories: structuredClone(input.repositories ?? {}),
      configRevision: "config-2",
    }))
    vi.mocked(createDevelopmentRequestID).mockReturnValue("request-1")
    mockedResolve.mockResolvedValue({
      identity: "https://github.com|200",
      name: "octo/new",
      default_branch: "main",
      can_implement: true,
    })
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
      await screen.findByRole("textbox", { name: "Repository URL" }),
      "https://github.com/octo/new",
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
        repositories: {
          "https://github.com|100": {
            name: "octo/current",
            defaultBranch: "main",
          },
          "https://github.com|200": { name: "octo/new", defaultBranch: "main" },
        },
      }),
    )
  })

  it("blocks invalid URLs and deduplicates provider-verified identities", async () => {
    const user = userEvent.setup()
    renderPage()

    const repositoryURL = await screen.findByRole("textbox", {
      name: "Repository URL",
    })
    const add = screen.getByRole("button", { name: "Add assignment" })
    const save = screen.getByRole("button", { name: "Save assignments" })

    await user.type(repositoryURL, "http://github.com/octo/new")
    expect(screen.getByRole("alert")).toHaveTextContent(
      /exact https github repository/i,
    )
    expect(add).toBeDisabled()
    expect(save).toBeDisabled()

    await user.clear(repositoryURL)
    await user.type(repositoryURL, "https://github.com/octo/current")
    mockedResolve.mockResolvedValueOnce({
      identity: "https://github.com|100",
      name: "octo/current",
      default_branch: "main",
      can_implement: true,
    })
    await user.click(add)
    await waitFor(() =>
      expect(mockedResolve).toHaveBeenCalledWith(
        "https://github.com/octo/current",
      ),
    )
    expect(screen.getAllByText("https://github.com|100")).toHaveLength(1)
    expect(save).toBeDisabled()
  })
})
