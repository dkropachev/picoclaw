import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { listPRLifecycleRepositoryAssignments } from "@/api/pr-lifecycle-repository-assignments"
import {
  deletePRLifecycleWorkflowConfiguration,
  listPRLifecycleWorkflowConfigurations,
  makePRLifecycleWorkflowConfigurationDefault,
} from "@/api/pr-lifecycle-workflow-configurations"
import {
  PRLifecycleRepositoryAssignmentEditorPage,
  PRLifecycleWorkflowConfigurationsCollectionPage,
} from "@/components/pr-workspaces/pr-lifecycle-admin-collections"

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...original,
    useBlocker: vi.fn(() => ({
      status: "idle",
      proceed: vi.fn(),
      reset: vi.fn(),
    })),
  }
})

vi.mock("@/api/pr-lifecycle-repository-assignments", async (importOriginal) => {
  const original =
    await importOriginal<
      typeof import("@/api/pr-lifecycle-repository-assignments")
    >()
  return {
    ...original,
    listPRLifecycleRepositoryAssignments: vi.fn(),
  }
})

vi.mock(
  "@/api/pr-lifecycle-workflow-configurations",
  async (importOriginal) => {
    const original =
      await importOriginal<
        typeof import("@/api/pr-lifecycle-workflow-configurations")
      >()
    return {
      ...original,
      deletePRLifecycleWorkflowConfiguration: vi.fn(),
      listPRLifecycleWorkflowConfigurations: vi.fn(),
      makePRLifecycleWorkflowConfigurationDefault: vi.fn(),
    }
  },
)

vi.mock("@/components/collection", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/components/collection")>()
  return {
    ...original,
    StandardCollectionPage: ({
      definition,
      items,
    }: {
      definition: MockCollectionDefinition
      items: MockWorkflowSummary[]
    }) => (
      <div>
        {items.flatMap((item) =>
          (definition.actions ?? [])
            .filter((action) => !action.hidden?.(item))
            .map((action) => {
              const label =
                typeof action.label === "function"
                  ? action.label(item)
                  : action.label
              return (
                <button
                  key={`${item.id}:${action.id}`}
                  type="button"
                  disabled={action.disabled?.(item)}
                  onClick={() => void action.onSelect(item)}
                >
                  {label} {item.id}
                </button>
              )
            }),
        )}
      </div>
    ),
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

interface MockWorkflowSummary {
  id: string
  name: string
  is_default: boolean
  bindings: number
  deferred_issues: "off" | "ask" | "automatic"
}

interface MockCollectionDefinition {
  actions?: Array<{
    id: string
    label: string | ((item: MockWorkflowSummary) => string)
    hidden?: (item: MockWorkflowSummary) => boolean
    disabled?: (item: MockWorkflowSummary) => boolean
    onSelect: (item: MockWorkflowSummary) => void | Promise<void>
  }>
}

const collectionMetadata = {
  total: 1,
  next_cursor: "",
  canonical_query: "ORDER BY name ASC",
  query_schema: { fields: [] },
  config_revision: "revision-1",
  effects: {
    gateway_effect: "applied" as const,
    deferred_policy_effect: "applied" as const,
  },
}

describe("PR lifecycle administrative collections", () => {
  beforeEach(() => {
    vi.mocked(listPRLifecycleRepositoryAssignments).mockReset()
    vi.mocked(listPRLifecycleWorkflowConfigurations).mockReset()
    vi.mocked(makePRLifecycleWorkflowConfigurationDefault).mockReset()
    vi.mocked(deletePRLifecycleWorkflowConfiguration).mockReset()
  })

  it("loads every workflow configuration page before selecting create choices", async () => {
    const firstPage = Array.from({ length: 200 }, (_, index) => ({
      id: `config-${String(index).padStart(3, "0")}`,
      name: `Configuration ${index}`,
      is_default: false,
      bindings: 0,
      deferred_issues: "ask" as const,
    }))
    const secondPage = Array.from({ length: 56 }, (_, offset) => {
      const index = offset + 200
      return {
        id: `config-${String(index).padStart(3, "0")}`,
        name: `Configuration ${index}`,
        is_default: index === 255,
        bindings: 0,
        deferred_issues: "ask" as const,
      }
    })
    vi.mocked(listPRLifecycleRepositoryAssignments).mockResolvedValue({
      repository_assignments: [],
      ...collectionMetadata,
      canonical_query: "ORDER BY repository ASC",
    })
    vi.mocked(listPRLifecycleWorkflowConfigurations)
      .mockResolvedValueOnce({
        workflow_configurations: firstPage,
        ...collectionMetadata,
        total: 256,
        next_cursor: "cursor-2",
      })
      .mockResolvedValueOnce({
        workflow_configurations: secondPage,
        ...collectionMetadata,
        total: 256,
      })

    renderWithQueryClient(
      <PRLifecycleRepositoryAssignmentEditorPage
        mode="create"
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    await waitFor(() =>
      expect(listPRLifecycleWorkflowConfigurations).toHaveBeenNthCalledWith(
        2,
        { cursor: "cursor-2", limit: 200 },
        expect.any(AbortSignal),
      ),
    )
    expect(
      await screen.findByRole("combobox", { name: "Workflow configuration" }),
    ).toHaveTextContent("Configuration 255 (config-255)")
  })

  it("makes a workflow configuration default and confirms removal", async () => {
    const user = userEvent.setup()
    const configuration: MockWorkflowSummary = {
      id: "automated",
      name: "Automated",
      is_default: false,
      bindings: 1,
      deferred_issues: "automatic",
    }
    vi.mocked(listPRLifecycleWorkflowConfigurations).mockResolvedValue({
      workflow_configurations: [configuration],
      ...collectionMetadata,
    })
    vi.mocked(makePRLifecycleWorkflowConfigurationDefault).mockResolvedValue(
      {} as never,
    )
    vi.mocked(deletePRLifecycleWorkflowConfiguration).mockResolvedValue(
      {} as never,
    )

    renderWithQueryClient(
      <PRLifecycleWorkflowConfigurationsCollectionPage
        search={{ q: "ORDER BY name ASC" }}
        onSearchChange={vi.fn()}
        onOpen={vi.fn()}
        onEdit={vi.fn()}
        onNew={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Make default automated" }),
    )
    await waitFor(() =>
      expect(makePRLifecycleWorkflowConfigurationDefault).toHaveBeenCalledWith(
        "automated",
        "revision-1",
      ),
    )

    await user.click(
      screen.getByRole("button", { name: "Remove configuration automated" }),
    )
    await user.click(
      screen.getByRole("button", { name: /^Remove configuration$/u }),
    )
    await waitFor(() =>
      expect(deletePRLifecycleWorkflowConfiguration).toHaveBeenCalledWith(
        "automated",
        "revision-1",
      ),
    )
  })
})

function renderWithQueryClient(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>,
  )
}
