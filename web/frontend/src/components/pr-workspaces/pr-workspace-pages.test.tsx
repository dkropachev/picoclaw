import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { type PRLifecycleFlowCatalog } from "@/api/pr-lifecycle-flow"
import {
  type PRLifecycleRepositoryAssignmentSnapshot,
  getPRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"
import {
  type PRLifecycleWorkflowConfigurationSnapshot,
  getPRLifecycleWorkflowConfigurations,
  putPRLifecycleWorkflowConfigurations,
} from "@/api/pr-lifecycle-workflow-configurations"
import { respondPRWorkspaceGate } from "@/api/pr-workspace-gates"
import {
  type PRWorkspace,
  PRWorkspaceAPIError,
  confirmPRWorkspaceCharter,
  createPRWorkspace,
  createPRWorkspaceCorrection,
  createPRWorkspaceRequestID,
  getPRWorkspace,
  listPRWorkspaces,
  mutatePRWorkspaceDeferredGroup,
  publishPRWorkspacePhase,
  reconcilePRWorkspacePublication,
  regroupPRWorkspaceDeferredFindings,
  revisePRWorkspaceCharter,
  sendPRWorkspaceMessage,
  setPRWorkspaceFindingDisposition,
  startPRWorkspaceRun,
  syncPRWorkspaceAutomaticDeferredIssues,
  updatePRWorkspaceDeferredGroup,
} from "@/api/pr-workspaces"
import { PRLifecycleWorkflowConfigurationsPage } from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"
import { PRWorkspacePage } from "@/components/pr-workspaces/pr-workspace-page"
import { PRWorkspacePortfolioPage } from "@/components/pr-workspaces/pr-workspace-portfolio-page"
import { SidebarProvider } from "@/components/ui/sidebar"

import prLifecycleFlowFixture from "../../../tests/fixtures/pr-lifecycle-flow.json" with { type: "json" }

const mockedNavigationBlocker = vi.hoisted(() => ({
  current: {
    status: "idle" as "idle" | "blocked",
    current: undefined,
    next: undefined,
    action: undefined,
    proceed: undefined as (() => void) | undefined,
    reset: undefined as (() => void) | undefined,
  },
}))

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...original,
    useBlocker: vi.fn(() => mockedNavigationBlocker.current),
    useRouter: vi.fn(() => ({
      navigate: vi.fn(),
      history: {
        location: {
          href: "/pull-requests/settings",
          state: {
            key: "test-entry",
            __TSR_key: "test-entry",
            __TSR_index: 0,
          },
        },
        replace: vi.fn(),
        subscribe: vi.fn(() => vi.fn()),
      },
    })),
  }
})

function setMockNavigationIdle() {
  mockedNavigationBlocker.current = {
    status: "idle",
    current: undefined,
    next: undefined,
    action: undefined,
    proceed: undefined,
    reset: undefined,
  }
}

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return {
    ...original,
    listPRWorkspaces: vi.fn(),
    createPRWorkspace: vi.fn(),
    createPRWorkspaceRequestID: vi.fn(() => `prq_${"9".repeat(32)}`),
    getPRWorkspace: vi.fn(),
    refreshPRWorkspace: vi.fn(),
    draftPRWorkspaceCharter: vi.fn(),
    savePRWorkspaceCharter: vi.fn(),
    revisePRWorkspaceCharter: vi.fn(),
    sendPRWorkspaceMessage: vi.fn(),
    confirmPRWorkspaceCharter: vi.fn(),
    startPRWorkspaceRun: vi.fn(),
    setPRWorkspaceFindingDisposition: vi.fn(),
    createPRWorkspaceCorrection: vi.fn(),
    promotePRWorkspaceCorrection: vi.fn(),
    mutatePRWorkspaceDeferredGroup: vi.fn(),
    regroupPRWorkspaceDeferredFindings: vi.fn(),
    syncPRWorkspaceAutomaticDeferredIssues: vi.fn(),
    updatePRWorkspaceDeferredGroup: vi.fn(),
    publishPRWorkspacePhase: vi.fn(),
    reconcilePRWorkspacePublication: vi.fn(),
  }
})

vi.mock("@/api/pr-workspace-gates", () => ({
  respondPRWorkspaceGate: vi.fn(),
}))

vi.mock(
  "@/api/pr-lifecycle-workflow-configurations",
  async (importOriginal) => {
    const original =
      await importOriginal<
        typeof import("@/api/pr-lifecycle-workflow-configurations")
      >()
    return {
      ...original,
      getPRLifecycleWorkflowConfigurations: vi.fn(),
      putPRLifecycleWorkflowConfigurations: vi.fn(),
    }
  },
)

vi.mock("@/api/pr-lifecycle-repository-assignments", async (importOriginal) => {
  const original =
    await importOriginal<
      typeof import("@/api/pr-lifecycle-repository-assignments")
    >()
  return {
    ...original,
    getPRLifecycleRepositoryAssignments: vi.fn(),
  }
})

const aggregate: PRWorkspace = {
  workspace: {
    id: `prw_${"1".repeat(32)}`,
    provider: "github",
    provider_origin: "https://github.com",
    repository_id: "100",
    repository: "octo/repo",
    pull_request_id: "200",
    pull_number: 42,
    phase: "review",
    execution_state: "waiting_user",
    active_charter_id: `pcr_${"2".repeat(32)}`,
    provider_head_sha: "b".repeat(40),
    version: 4,
    created_at: "2026-08-13T10:00:00Z",
    updated_at: "2026-08-13T10:05:00Z",
  },
  provider_snapshot: {
    provider: "github",
    provider_origin: "https://github.com",
    repository_id: "100",
    repository: "octo/repo",
    pull_request_id: "200",
    pull_number: 42,
    title: "Fix lost updates",
    body: "Keep optimistic concurrency intact.",
    author_id: "300",
    author_login: "octocat",
    authenticated_user_id: "300",
    base_ref: "main",
    base_sha: "a".repeat(40),
    head_repository_id: "100",
    head_ref: "fix/store",
    head_sha: "b".repeat(40),
    state: "open",
    owned: true,
    head_writable: true,
    can_review: true,
    can_create_issue: true,
    provider_revision: "github-etag-4",
    observed_at: "2026-08-13T10:00:00Z",
  },
  charters: [
    {
      id: `pcr_${"2".repeat(32)}`,
      revision: 1,
      type: "fix",
      goal: "Prevent lost updates.",
      acceptance_criteria: ["Concurrent writes conflict."],
      included_areas: ["pkg/store"],
      excluded_areas: ["Broad refactor"],
      non_goals: ["New storage engine"],
      base_sha: "a".repeat(40),
      head_sha: "b".repeat(40),
      confirmed: true,
      created_at: "2026-08-13T10:01:00Z",
      confirmed_at: "2026-08-13T10:02:00Z",
    },
  ],
  stage_runs: [
    {
      id: `psr_${"3".repeat(32)}`,
      stage: "review",
      state: "succeeded",
      charter_id: `pcr_${"2".repeat(32)}`,
      head_sha: "b".repeat(40),
      attempt: 1,
      summary: "Initial review found no issues.",
      started_at: "2026-08-13T10:03:00Z",
      finished_at: "2026-08-13T10:03:01Z",
    },
    {
      id: `psr_${"5".repeat(32)}`,
      stage: "completion_audit",
      state: "succeeded",
      charter_id: `pcr_${"2".repeat(32)}`,
      head_sha: "b".repeat(40),
      attempt: 1,
      summary: "The selected fix is complete within the charter.",
      started_at: "2026-08-13T10:04:00Z",
      finished_at: "2026-08-13T10:04:01Z",
    },
  ],
  findings: [],
  messages: [],
  corrections: [],
  repository_lessons: [],
  nudge_rounds: [
    {
      id: `pnr_${"4".repeat(32)}`,
      stage_run_id: `psr_${"3".repeat(32)}`,
      stage: "review",
      round: 1,
      minimum_rounds: 1,
      hard_cap: 3,
      strategy: "coverage_gaps",
      challenge: "Inspect unchecked callers.",
      variant_digest: "c".repeat(64),
      prompt_digest: "a".repeat(64),
      state: "succeeded",
      novel_findings: 0,
      duplicate_count: 0,
      resolved_findings: 0,
      created_at: "2026-08-13T10:04:00Z",
    },
  ],
  deferred_groups: [],
  repair_attempts: [],
  validation_runs: [],
  gates: [],
  publications: [],
  activity: [],
}

const lifecycleFlow =
  prLifecycleFlowFixture.flow as unknown as PRLifecycleFlowCatalog

const workflowConfigurations: PRLifecycleWorkflowConfigurationSnapshot = {
  workflowConfigurations: {
    default: {
      name: "Default",
      bindings: [],
      deferredIssues: { mode: "ask" },
    },
    editable: {
      name: "Editable",
      bindings: [],
      deferredIssues: { mode: "ask" },
    },
  },
  defaultWorkflowConfiguration: "default",
  nudge: {
    reviewMinimumAdditional: 2,
    reviewMaximumAdditional: 5,
    completionMinimumAdditional: 2,
    completionMaximumAdditional: 5,
  },
  scope: {
    xs: { files: 1, semanticLines: 20, modules: 1 },
    s: { files: 3, semanticLines: 100, modules: 1 },
    m: { files: 10, semanticLines: 500, modules: 3 },
  },
  gateCatalog: Object.fromEntries(
    lifecycleFlow.flows.flatMap((flow) =>
      flow.nodes.flatMap((node) =>
        node.decision_point
          ? [
              [
                node.decision_point,
                {
                  workflowRef: "workflows/pr-lifecycle.yml",
                  gateRef: `gates.${node.decision_point.replace(/^pr\./, "").replaceAll(".", "-")}`,
                  sourceAISupported:
                    node.decision_point === "pr.finding.classify",
                  prompt: `Complete ${node.title}.`,
                  fields: [
                    {
                      id: "action",
                      type: "select" as const,
                      label: "What should happen?",
                      required: true,
                      minSelections: 1,
                      maxSelections: 1,
                      options: [
                        { id: "approve", label: "Approve" },
                        { id: "revise", label: "Request revision" },
                      ],
                    },
                  ],
                  workflowRevision: "workflow-revision-1",
                  defaultAction: { type: "human" as const },
                  effectiveAction: { type: "human" as const },
                  actionSource: "workflow-default" as const,
                },
              ] as const,
            ]
          : [],
      ),
    ),
  ),
  flow: lifecycleFlow,
  flowRevision: prLifecycleFlowFixture.flow_revision,
  catalogRevision: "sha256:catalog",
  configRevision: "sha256:config",
  effects: { gatewayEffect: "applied", deferredPolicyEffect: "applied" },
}

const repositoryAssignments: PRLifecycleRepositoryAssignmentSnapshot = {
  workflowConfigurations: Object.fromEntries(
    Object.entries(workflowConfigurations.workflowConfigurations).map(
      ([configurationID, configuration]) => [
        configurationID,
        {
          name: configuration.name,
          deferredIssues: configuration.deferredIssues,
        },
      ],
    ),
  ),
  defaultWorkflowConfiguration:
    workflowConfigurations.defaultWorkflowConfiguration,
  repositoryAssignments: {},
  configRevision: workflowConfigurations.configRevision,
  effects: workflowConfigurations.effects,
}

const mockedList = vi.mocked(listPRWorkspaces)
const mockedCreate = vi.mocked(createPRWorkspace)
const mockedGet = vi.mocked(getPRWorkspace)
const mockedStartRun = vi.mocked(startPRWorkspaceRun)
const mockedReviseCharter = vi.mocked(revisePRWorkspaceCharter)
const mockedConfirmCharter = vi.mocked(confirmPRWorkspaceCharter)
const mockedSendMessage = vi.mocked(sendPRWorkspaceMessage)
const mockedFindingDisposition = vi.mocked(setPRWorkspaceFindingDisposition)
const mockedCreateCorrection = vi.mocked(createPRWorkspaceCorrection)
const mockedDeferredGroup = vi.mocked(mutatePRWorkspaceDeferredGroup)
const mockedRegroupDeferred = vi.mocked(regroupPRWorkspaceDeferredFindings)
const mockedSyncAutomaticDeferred = vi.mocked(
  syncPRWorkspaceAutomaticDeferredIssues,
)
const mockedUpdateDeferred = vi.mocked(updatePRWorkspaceDeferredGroup)
const mockedPublishPhase = vi.mocked(publishPRWorkspacePhase)
const mockedReconcilePublication = vi.mocked(reconcilePRWorkspacePublication)
const mockedRespondGate = vi.mocked(respondPRWorkspaceGate)
const mockedGetWorkflowConfigurations = vi.mocked(
  getPRLifecycleWorkflowConfigurations,
)
const mockedPutWorkflowConfigurations = vi.mocked(
  putPRLifecycleWorkflowConfigurations,
)
const mockedGetAssignments = vi.mocked(getPRLifecycleRepositoryAssignments)

function createTestQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function renderPage(node: ReactNode, client = createTestQueryClient()) {
  return Object.assign(
    render(
      <QueryClientProvider client={client}>
        <SidebarProvider>{node}</SidebarProvider>
      </QueryClientProvider>,
    ),
    { queryClient: client },
  )
}

describe("unified PR workspace pages", () => {
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
    setMockNavigationIdle()
    mockedList.mockResolvedValue({
      workspaces: [aggregate.workspace],
    })
    mockedCreate.mockResolvedValue(aggregate)
    mockedGet.mockResolvedValue(aggregate)
    mockedStartRun.mockResolvedValue(aggregate)
    mockedReviseCharter.mockResolvedValue(aggregate)
    mockedConfirmCharter.mockResolvedValue(aggregate)
    mockedSendMessage.mockResolvedValue(aggregate)
    mockedFindingDisposition.mockResolvedValue(aggregate)
    mockedCreateCorrection.mockResolvedValue(aggregate)
    mockedDeferredGroup.mockResolvedValue(aggregate)
    mockedRegroupDeferred.mockResolvedValue(aggregate)
    mockedSyncAutomaticDeferred.mockResolvedValue(aggregate)
    mockedUpdateDeferred.mockResolvedValue(aggregate)
    mockedPublishPhase.mockResolvedValue(aggregate)
    mockedReconcilePublication.mockResolvedValue(aggregate)
    mockedRespondGate.mockResolvedValue(aggregate)
    mockedGetWorkflowConfigurations.mockResolvedValue(workflowConfigurations)
    mockedPutWorkflowConfigurations.mockResolvedValue(workflowConfigurations)
    mockedGetAssignments.mockResolvedValue(repositoryAssignments)
    vi.mocked(createPRWorkspaceRequestID).mockReturnValue(
      `prq_${"9".repeat(32)}`,
    )
  })

  it("tracks and opens PRs from one operational portfolio", async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    renderPage(
      <PRWorkspacePortfolioPage
        onOpenWorkspace={onOpen}
        onOpenWorkflowConfigurations={vi.fn()}
      />,
    )

    expect(await screen.findByText("Fix lost updates")).toBeVisible()
    expect(screen.getByRole("heading", { name: "Needs you" })).toBeVisible()
    await user.click(screen.getByText("Fix lost updates"))
    expect(onOpen).toHaveBeenCalledWith(aggregate.workspace.id)

    await user.type(
      screen.getByLabelText("GitHub pull request URL"),
      "https://github.com/octo/repo/pull/43",
    )
    await user.click(screen.getByRole("button", { name: "Track PR" }))
    await waitFor(() =>
      expect(mockedCreate).toHaveBeenCalledWith({
        pull_request_url: "https://github.com/octo/repo/pull/43",
        request_id: `prq_${"9".repeat(32)}`,
      }),
    )
  })

  it("exposes stable workspace landmarks and only links lifecycle steps to real sections", async () => {
    const { container } = renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const detail = await screen.findByTestId("pr-workspace-detail")
    expect(detail).toHaveAttribute("aria-busy", "false")
    expect(
      screen.getByRole("heading", { level: 1, name: "octo/repo #42" }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { level: 2, name: "Fix lost updates" }),
    ).toBeVisible()

    const rail = screen.getByTestId("pr-lifecycle-rail")
    const lifecycleLinks = within(rail).getAllByRole("link")
    for (const link of lifecycleLinks) {
      const target = link.getAttribute("href")
      expect(target).toMatch(/^#pr-/)
      expect(container.querySelector(target!)).not.toBeNull()
    }
    expect(within(rail).getByText("Complete").closest("a")).toBeNull()

    expect(screen.getByTestId("pr-stage-charter")).toBeInTheDocument()
    expect(screen.getByTestId("pr-stage-review")).toBeInTheDocument()
    expect(screen.getByTestId("pr-stage-triage")).toBeInTheDocument()
    expect(screen.getByTestId("pr-stage-implementation")).toBeInTheDocument()
    expect(screen.getByTestId("pr-scope-matrix")).toContainElement(
      screen.getByRole("table", { name: /semantic distance/i }),
    )
  })

  it("keeps long-running AI lifecycle work visibly explained", async () => {
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, execution_state: "running" },
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const detail = await screen.findByTestId("pr-workspace-detail")
    expect(detail).toHaveAttribute("aria-busy", "true")
    expect(screen.getByRole("status")).toHaveTextContent(
      /AI lifecycle work is running/i,
    )
  })

  it("keeps cached workspace drafts visible when a background refresh fails", async () => {
    const user = userEvent.setup()
    const { queryClient } = renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const guidance = await screen.findByLabelText("Guidance")
    await user.type(guidance, "Preserve this unsaved guidance draft.")

    mockedGet.mockRejectedValue(new Error("temporary refresh failure"))
    await queryClient.refetchQueries({
      queryKey: ["pr-workspace", aggregate.workspace.id],
    })

    expect(screen.getByTestId("pr-workspace-detail")).toBeVisible()
    expect(guidance).toHaveValue("Preserve this unsaved guidance draft.")
    expect(
      await screen.findByText(
        "The latest workspace state could not be refreshed. The last loaded version remains visible and your unsaved input is preserved.",
      ),
    ).toBeVisible()

    mockedGet.mockResolvedValue(aggregate)
    await user.click(screen.getByRole("button", { name: "Retry refresh" }))
    await waitFor(() =>
      expect(
        screen.queryByText(/latest workspace state could not be refreshed/i),
      ).not.toBeInTheDocument(),
    )
    expect(guidance).toHaveValue("Preserve this unsaved guidance draft.")
  })

  it("keeps automatic Gate actions read-only unless a Human form is waiting", async () => {
    const gateID = `pgr_${"4".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        execution_state: "waiting_gate",
      },
      gates: [
        {
          id: gateID,
          decision_point: "pr.implementation.complete",
          state: "waiting_gate",
          policy_revision: "sha256:automatic-policy",
          subject_revision: "sha256:automatic-subject",
          turns: [
            {
              stage_id: "isolated-completion-check",
              kind: "ai_isolated_context",
              title: "Check completion independently",
              status: "waiting",
            },
          ],
          created_at: "2026-08-13T10:05:00Z",
        },
      ],
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const gates = await screen.findByTestId("pr-gates")
    expect(
      within(gates).getAllByText("Check completion independently")[0],
    ).toBeVisible()
    expect(within(gates).getByRole("status")).toHaveTextContent(
      /configured Gate action is still running/i,
    )
    expect(
      within(gates).queryByLabelText("Decision comment"),
    ).not.toBeInTheDocument()
    for (const outcome of ["Pass", "Revise", "Defer", "Block"]) {
      expect(
        within(gates).queryByRole("button", { name: outcome }),
      ).not.toBeInTheDocument()
    }
  })

  it("shares stage-targeted guidance, renders bound history, and preserves a conflicted draft", async () => {
    const user = userEvent.setup()
    const currentMessageID = `pms_${"4".repeat(32)}`
    const guidanceWorkspace: PRWorkspace = {
      ...aggregate,
      messages: [
        {
          id: `pms_${"3".repeat(32)}`,
          role: "user",
          stage: "workspace",
          content: "This guidance came from the previous head.",
          charter_id: aggregate.charters[0].id,
          head_sha: "a".repeat(40),
          created_at: "2026-08-13T09:59:00Z",
        },
        {
          id: currentMessageID,
          role: "user",
          stage: "review",
          content: "Check every compare-and-swap caller.",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          created_at: "2026-08-13T10:03:30Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(guidanceWorkspace)
    mockedSendMessage.mockRejectedValueOnce(
      new PRWorkspaceAPIError(
        "version_conflict",
        409,
        "Workspace changed while sending guidance.",
        guidanceWorkspace,
      ),
    )

    const { container } = renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(
      await screen.findByText("Check every compare-and-swap caller."),
    ).toBeVisible()
    expect(
      screen.getByRole("region", { name: "Shared guidance history" }),
    ).toHaveAttribute("tabindex", "0")
    expect(
      screen.getByText("This guidance came from the previous head."),
    ).toBeVisible()
    expect(screen.getByText("Earlier charter or head")).toBeVisible()
    expect(
      container.querySelector('time[datetime="2026-08-13T10:03:30Z"]'),
    ).toBeVisible()

    await user.click(screen.getByLabelText("Guidance target"))
    await user.click(screen.getByRole("option", { name: "PR implementation" }))
    await user.click(
      screen.getByRole("checkbox", {
        name: "Also record as a correction",
      }),
    )
    await user.click(screen.getByLabelText("Correction applies to"))
    await user.click(
      screen.getByRole("option", { name: "Review and implementation" }),
    )
    const guidance = screen.getByLabelText("Guidance")
    await user.type(
      guidance,
      "Keep retry handling narrow and do not refactor the provider.",
    )
    await user.click(screen.getByRole("button", { name: "Send guidance" }))

    expect(
      await screen.findByText("Workspace changed while sending guidance."),
    ).toBeVisible()
    expect(guidance).toHaveValue(
      "Keep retry handling narrow and do not refactor the provider.",
    )
    expect(screen.getByLabelText("Guidance target")).toHaveTextContent(
      "PR implementation",
    )
    expect(
      screen.getByRole("checkbox", {
        name: "Also record as a correction",
      }),
    ).toBeChecked()
    expect(mockedSendMessage).toHaveBeenCalledWith(aggregate.workspace.id, {
      expected_version: 4,
      request_id: `prq_${"9".repeat(32)}`,
      content: "Keep retry handling narrow and do not refactor the provider.",
      stage: "implementation",
      mark_as_correction: true,
      applicability: "both",
    })

    mockedSendMessage.mockResolvedValueOnce({
      ...guidanceWorkspace,
      workspace: { ...guidanceWorkspace.workspace, version: 5 },
      messages: [
        ...guidanceWorkspace.messages,
        {
          id: `pms_${"5".repeat(32)}`,
          role: "user",
          stage: "implementation",
          content:
            "Keep retry handling narrow and do not refactor the provider.",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          created_at: "2026-08-13T10:06:00Z",
        },
      ],
    })
    await user.click(screen.getByRole("button", { name: "Send guidance" }))
    await waitFor(() => expect(guidance).toHaveValue(""))
  })

  it("limits guidance targets once implementation has started", async () => {
    const user = userEvent.setup()
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "implementation",
        execution_state: "waiting_user",
      },
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const target = await screen.findByLabelText("Guidance target")
    expect(target).toHaveTextContent("PR implementation")
    await user.click(target)
    expect(
      screen.getByRole("option", {
        name: "Both PR review and implementation",
      }),
    ).toBeVisible()
    expect(
      screen.getByRole("option", { name: "PR implementation" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("option", { name: "PR review" }),
    ).not.toBeInTheDocument()
  })

  it("shows shared lifecycle evidence and permits nudging after zero findings", async () => {
    const user = userEvent.setup()
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, phase: "triage" },
    })
    const reviewPage = renderPage(
      <PRWorkspacePage
        workspaceID={aggregate.workspace.id}
        onBack={vi.fn()}
        onOpenWorkflowConfigurations={vi.fn()}
      />,
    )

    expect(await screen.findByText("PR charter")).toBeVisible()
    expect(screen.getByText("Review search and nudges")).toBeVisible()
    expect(
      screen.getAllByText("Initial review found no issues.")[0],
    ).toBeVisible()
    expect(screen.getByText("Findings and scope triage")).toBeVisible()
    expect(screen.getByText("Implementation and validation")).toBeVisible()
    expect(screen.getByText("Corrections and shared context")).toBeVisible()
    expect(screen.getAllByText("Publication").at(-1)).toBeVisible()
    expect(screen.getByText("Deferred work")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Find more" }))
    await waitFor(() =>
      expect(mockedStartRun).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "nudge-runs",
        expect.objectContaining({
          expected_version: 4,
          expected_head_revision: aggregate.provider_snapshot.provider_revision,
          stage: "review",
        }),
      ),
    )

    reviewPage.unmount()
    const implementationRunID = `psr_${"6".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, phase: "completion_audit" },
      stage_runs: [
        ...aggregate.stage_runs,
        {
          id: implementationRunID,
          stage: "implementation",
          state: "waiting_gate",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          attempt: 1,
          started_at: "2026-08-13T10:03:30Z",
          finished_at: "2026-08-13T10:03:59Z",
        },
      ],
      repair_attempts: [
        {
          id: `pra_${"6".repeat(32)}`,
          stage_run_id: implementationRunID,
          number: 1,
          state: "succeeded",
          instruction: "Implement the confirmed charter.",
          candidate_sha: "c".repeat(40),
          scope: {
            distance: "S0_exact",
            size: "XS",
            presence: "candidate_present",
            files: 1,
            semantic_lines: 5,
            modules: 1,
            estimated: false,
            type_compatible: true,
            confidence: 1,
          },
          prompt_digest: "sha256:repair",
          started_at: "2026-08-13T10:03:30Z",
          finished_at: "2026-08-13T10:03:45Z",
        },
      ],
      validation_runs: [
        {
          id: `pvr_${"6".repeat(32)}`,
          stage_run_id: implementationRunID,
          state: "succeeded",
          candidate_sha: "c".repeat(40),
          checks: [{ id: "tests", name: "Tests", status: "passed" }],
          started_at: "2026-08-13T10:03:45Z",
          finished_at: "2026-08-13T10:03:50Z",
        },
      ],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    await user.click(await screen.findByRole("button", { name: "Check again" }))
    await waitFor(() =>
      expect(mockedStartRun).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "nudge-runs",
        expect.objectContaining({
          expected_version: 4,
          expected_head_revision: aggregate.provider_snapshot.provider_revision,
          stage: "implementation_completion",
        }),
      ),
    )
  })

  it("implements a confirmed charter even when review found zero findings", async () => {
    const user = userEvent.setup()
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "triage",
      },
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Implement charter" }),
    )
    await waitFor(() =>
      expect(mockedStartRun).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "implementation-runs",
        expect.objectContaining({
          expected_version: 4,
          finding_ids: [],
        }),
      ),
    )
  })

  it("does not offer implementation before review and triage are complete", async () => {
    const openFinding = {
      id: `pfn_${"6".repeat(32)}`,
      fingerprint: "sha256:open-finding",
      origin: "review" as const,
      origin_run_id: aggregate.stage_runs[0].id,
      severity: "high",
      title: "Open review finding",
      message: "Classify this finding first.",
      scope: {
        distance: "S0_exact" as const,
        size: "XS" as const,
        presence: "candidate_present" as const,
        files: 1,
        semantic_lines: 1,
        modules: 1,
        estimated: false,
        type_compatible: true,
        confidence: 1,
      },
      disposition: "open" as const,
      version: 1,
      created_at: "2026-08-13T10:05:00Z",
      updated_at: "2026-08-13T10:05:00Z",
    }
    mockedGet.mockResolvedValueOnce({
      ...aggregate,
      stage_runs: aggregate.stage_runs.filter((run) => run.stage !== "review"),
    })
    const first = renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    expect(
      await screen.findByText(
        "Finish a successful review of this exact charter and PR revision first.",
      ),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Implement charter" }),
    ).toBeDisabled()
    first.unmount()

    mockedGet.mockResolvedValueOnce({
      ...aggregate,
      findings: [openFinding],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    expect(
      await screen.findByText(
        "Classify, defer, or dismiss every open finding before implementation.",
      ),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Implement charter" }),
    ).toBeDisabled()
  })

  it("explains a failed implementation and preserves a clear retry path", async () => {
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "implementation",
        execution_state: "failed",
      },
      stage_runs: [
        ...aggregate.stage_runs,
        {
          id: `psr_${"7".repeat(32)}`,
          stage: "implementation",
          state: "failed",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          attempt: 1,
          public_error: "repair_failed",
          started_at: "2026-08-13T10:06:00Z",
          finished_at: "2026-08-13T10:06:01Z",
        },
      ],
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("Implementation needs attention")
    expect(alert).toHaveTextContent(
      "The repair worker could not create a candidate change.",
    )
    expect(alert).toHaveTextContent(
      "Your charter and selected findings are retained.",
    )
    expect(
      screen.getByRole("button", { name: "Retry implementation" }),
    ).toBeEnabled()
  })

  it("blocks completion on failed validation and exposes bounded recovery evidence", async () => {
    const user = userEvent.setup()
    const implementationRunID = `psr_${"7".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "implementation",
        execution_state: "failed",
      },
      stage_runs: [
        ...aggregate.stage_runs,
        {
          id: implementationRunID,
          stage: "implementation",
          state: "failed",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          attempt: 2,
          summary: "Local validation could not produce reliable evidence.",
          public_error: "validation_infrastructure_failed",
          started_at: "2026-08-13T10:06:00Z",
          finished_at: "2026-08-13T10:06:01Z",
        },
      ],
      repair_attempts: [
        {
          id: `pra_${"7".repeat(32)}`,
          stage_run_id: implementationRunID,
          number: 1,
          state: "succeeded",
          instruction: "Implement the confirmed charter.",
          candidate_sha: "c".repeat(40),
          scope: {
            distance: "S0_exact",
            size: "XS",
            presence: "candidate_present",
            files: 1,
            semantic_lines: 5,
            modules: 1,
            estimated: false,
            type_compatible: true,
            confidence: 1,
          },
          prompt_digest: "sha256:repair",
          started_at: "2026-08-13T10:06:00Z",
          finished_at: "2026-08-13T10:06:01Z",
        },
      ],
      validation_runs: [
        {
          id: `pvr_${"7".repeat(32)}`,
          stage_run_id: implementationRunID,
          state: "failed",
          candidate_sha: "c".repeat(40),
          checks: [
            {
              id: "opaque-step-42",
              name: "Go unit tests",
              status: "infrastructure_error",
              summary: `dependency download unavailable ${"x".repeat(1_500)}PRIVATE_TAIL`,
              exit_code: 1,
              duration_ms: 275,
            },
          ],
          started_at: "2026-08-13T10:06:00Z",
          finished_at: "2026-08-13T10:06:01Z",
        },
        {
          id: `pvr_${"8".repeat(32)}`,
          stage_run_id: aggregate.stage_runs[0].id,
          state: "succeeded",
          candidate_sha: "d".repeat(40),
          checks: [{ id: "old-tests", name: "Old tests", status: "passed" }],
          started_at: "2026-08-13T10:07:00Z",
          finished_at: "2026-08-13T10:07:01Z",
        },
      ],
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(await screen.findByRole("alert")).toHaveTextContent("Go unit tests")
    expect(screen.getAllByText("infrastructure_error")).toHaveLength(2)
    expect(screen.queryByText("opaque-step-42")).not.toBeInTheDocument()
    expect(screen.queryByText(/PRIVATE_TAIL/u)).not.toBeInTheDocument()
    expect(
      screen
        .getAllByText(/dependency download unavailable/u)
        .every((diagnostic) => (diagnostic.textContent?.length ?? 0) <= 1_202),
    ).toBe(true)
    expect(
      screen.getByRole("button", { name: "Audit completion" }),
    ).toBeDisabled()

    await user.click(
      screen.getByRole("button", { name: "Retry implementation" }),
    )
    await waitFor(() =>
      expect(mockedStartRun).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "implementation-runs",
        expect.objectContaining({ expected_version: 4, finding_ids: [] }),
      ),
    )
  })

  it("summarizes adjacent identical activity without hiding distinct events", async () => {
    mockedGet.mockResolvedValue({
      ...aggregate,
      activity: [
        {
          ordinal: 1,
          kind: "implementation.validation",
          actor: "system",
          summary: "Validation infrastructure stopped.",
          entity_id: "candidate-1",
          created_at: "2026-08-13T10:06:00Z",
        },
        {
          ordinal: 2,
          kind: "implementation.validation",
          actor: "system",
          summary: "Validation infrastructure stopped.",
          entity_id: "candidate-1",
          created_at: "2026-08-13T10:07:00Z",
        },
        {
          ordinal: 3,
          kind: "implementation.retry",
          actor: "user",
          summary: "Implementation retry requested.",
          created_at: "2026-08-13T10:08:00Z",
        },
      ],
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(
      await screen.findByText("Implementation retry requested."),
    ).toBeVisible()
    expect(
      screen.getAllByText("Validation infrastructure stopped."),
    ).toHaveLength(1)
    expect(screen.getByText("· repeated 2 times")).toBeVisible()
  })

  it("shows the actual latest review attempt and nudge outcome", async () => {
    const latestRunID = `psr_${"9".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      stage_runs: [
        ...aggregate.stage_runs,
        {
          id: latestRunID,
          stage: "review",
          state: "failed",
          charter_id: aggregate.charters[0].id,
          head_sha: aggregate.provider_snapshot.head_sha,
          attempt: 2,
          summary: "The latest review stopped after a bounded model failure.",
          public_error: "review_nudge_failed",
          started_at: "2026-08-13T10:06:00Z",
          finished_at: "2026-08-13T10:06:01Z",
        },
      ],
      nudge_rounds: [
        ...aggregate.nudge_rounds,
        {
          id: `pnr_${"8".repeat(32)}`,
          stage_run_id: latestRunID,
          stage: "review",
          round: 2,
          minimum_rounds: 2,
          hard_cap: 5,
          strategy: "adversarial",
          challenge: "Challenge the strongest conclusion.",
          variant_digest: "d".repeat(64),
          prompt_digest: "e".repeat(64),
          state: "failed",
          public_error: "model_output_invalid",
          novel_findings: 0,
          duplicate_count: 1,
          resolved_findings: 0,
          created_at: "2026-08-13T10:06:01Z",
        },
      ],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(
      await screen.findByText(
        "The latest review stopped after a bounded model failure.",
      ),
    ).toBeVisible()
    expect(screen.getByText(/Latest run: attempt 2 · Failed/)).toBeVisible()
    expect(screen.getAllByText("model_output_invalid")[0]).toBeVisible()
    expect(screen.getByRole("button", { name: "Find more" })).toBeDisabled()
  })

  it("revises a stale active charter, selects the new draft, and reconfirms it", async () => {
    const user = userEvent.setup()
    const currentHead = "c".repeat(40)
    const draftID = `pcr_${"7".repeat(32)}`
    const staleWorkspace: PRWorkspace = {
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "charter",
        provider_head_sha: currentHead,
      },
      provider_snapshot: {
        ...aggregate.provider_snapshot,
        head_sha: currentHead,
        provider_revision: "github-etag-5",
      },
    }
    const revisedWorkspace: PRWorkspace = {
      ...staleWorkspace,
      workspace: {
        ...staleWorkspace.workspace,
        active_charter_id: undefined,
        version: 5,
      },
      charters: [
        ...staleWorkspace.charters,
        {
          ...staleWorkspace.charters[0],
          id: draftID,
          revision: 2,
          head_sha: currentHead,
          confirmed: false,
          confirmed_at: undefined,
        },
      ],
    }
    mockedGet.mockResolvedValue(staleWorkspace)
    mockedReviseCharter.mockResolvedValue(revisedWorkspace)
    mockedConfirmCharter.mockResolvedValue(revisedWorkspace)
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(await screen.findByText(/The PR head changed/)).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Save revised draft" }))
    await waitFor(() =>
      expect(mockedReviseCharter).toHaveBeenCalledWith(
        aggregate.workspace.id,
        expect.objectContaining({
          expected_version: 4,
          expected_charter_revision: 1,
          expected_head_revision: "github-etag-5",
        }),
      ),
    )
    const confirm = await screen.findByRole("button", {
      name: "Send to confirmation gate",
    })
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    await waitFor(() =>
      expect(mockedConfirmCharter).toHaveBeenCalledWith(
        aggregate.workspace.id,
        expect.objectContaining({
          expected_version: 5,
          expected_charter_revision: 2,
        }),
      ),
    )
  })

  it("explains graded out-of-scope handling and records a finding correction", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"8".repeat(32)}`
    const scopedWorkspace: PRWorkspace = {
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:related",
          origin: "review",
          severity: "medium",
          title: "Related cleanup",
          message: "This cleanup is useful but outside the charter.",
          evidence: "The changed caller is outside pkg/store.",
          impact: "Mixes two independently releasable changes.",
          recommendation: "Track a follow-up.",
          scope: {
            distance: "S2_related_followup",
            size: "M",
            presence: "follow_up",
            files: 5,
            semantic_lines: 180,
            modules: 2,
            estimated: true,
            type_compatible: true,
            confidence: 0.91,
            explanation: "Related, but unnecessary for the charter goal.",
            charter_clauses: ["pkg/store only"],
            change_evidence: [
              {
                path: "pkg/caller/caller.go",
                hunk: "@@ retryCaller @@",
                module: "pkg/caller",
                semantic_lines: 28,
                presence: "follow_up",
                scope_distance: "S2_related_followup",
                change_size: "M",
                type_compatible: true,
                confidence: 0.91,
                charter_clauses: ["pkg/store only"],
                explanation: "This caller can be changed independently.",
              },
            ],
          },
          disposition: "open",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(scopedWorkspace)
    mockedFindingDisposition.mockResolvedValue(scopedWorkspace)
    mockedCreateCorrection
      .mockRejectedValueOnce(
        new PRWorkspaceAPIError(
          "correction_temporarily_unavailable",
          503,
          "Correction could not be recorded",
        ),
      )
      .mockResolvedValueOnce(scopedWorkspace)
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const finding = within(
      (await screen.findByText("Related cleanup")).closest("article")!,
    )
    expect(
      finding.getByRole("heading", { level: 3, name: "Related cleanup" }),
    ).toBeVisible()
    expect(finding.getByText("S2/S3 · Defer")).toBeVisible()
    expect(finding.getByText(/5 files · 180 semantic lines/)).toBeVisible()
    expect(
      finding.getByText(/Follow-up work not present in the candidate/),
    ).toBeVisible()
    await user.click(finding.getByText("Path and hunk evidence (1)"))
    expect(finding.getByText("@@ retryCaller @@")).toBeVisible()
    await user.click(
      finding.getByRole("button", { name: "Defer from this PR" }),
    )
    await waitFor(() =>
      expect(mockedFindingDisposition).toHaveBeenCalledWith(
        aggregate.workspace.id,
        findingID,
        expect.objectContaining({ disposition: "deferred" }),
      ),
    )

    await user.click(
      finding.getByRole("button", { name: "Correct this finding" }),
    )
    await user.type(
      finding.getByLabelText("Related cleanup: Correction"),
      "The caller is actually part of pkg/store.",
    )
    await user.click(finding.getByRole("button", { name: "Record correction" }))
    expect(
      await screen.findByText("Correction could not be recorded"),
    ).toBeVisible()
    expect(finding.getByLabelText("Related cleanup: Correction")).toHaveValue(
      "The caller is actually part of pkg/store.",
    )

    await user.click(finding.getByRole("button", { name: "Record correction" }))
    await waitFor(() => expect(mockedCreateCorrection).toHaveBeenCalledTimes(2))
    expect(mockedCreateCorrection).toHaveBeenLastCalledWith(
      aggregate.workspace.id,
      expect.objectContaining({
        target_id: findingID,
        kind: "finding_quality",
        applicability: "both",
      }),
    )
    await waitFor(() =>
      expect(
        finding.queryByLabelText("Related cleanup: Correction"),
      ).not.toBeInTheDocument(),
    )
  })

  it("preserves a general correction draft until the mutation succeeds", async () => {
    const user = userEvent.setup()
    mockedCreateCorrection
      .mockRejectedValueOnce(
        new PRWorkspaceAPIError(
          "correction_temporarily_unavailable",
          503,
          "Correction could not be recorded",
        ),
      )
      .mockResolvedValueOnce(aggregate)
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const original = await screen.findByLabelText("Original AI claim")
    const corrected = screen.getByLabelText("Correction")
    await user.type(original, "No additional review findings exist.")
    await user.type(corrected, "A retry-path issue still exists.")
    await user.click(screen.getByRole("button", { name: "Record correction" }))

    expect(
      await screen.findByText("Correction could not be recorded"),
    ).toBeVisible()
    expect(original).toHaveValue("No additional review findings exist.")
    expect(corrected).toHaveValue("A retry-path issue still exists.")

    await user.click(screen.getByRole("button", { name: "Record correction" }))
    await waitFor(() => expect(mockedCreateCorrection).toHaveBeenCalledTimes(2))
    await waitFor(() => {
      expect(original).toHaveValue("")
      expect(corrected).toHaveValue("")
    })
  })

  it("renders allowlisted gate evidence without exposing private subjects", async () => {
    mockedGet.mockResolvedValue({
      ...aggregate,
      gates: [
        {
          id: `pgr_${"6".repeat(32)}`,
          decision_point: "pr.implementation.scope",
          state: "waiting_user",
          policy_revision: "sha256:policy-revision",
          subject_revision: "sha256:subject-revision",
          turns: [
            {
              stage_id: "human-scope",
              kind: "human",
              title: "Approve exact large scope",
              status: "waiting_user",
            },
          ],
          evidence: {
            charter_type: "fix",
            charter_goal: "Prevent lost updates.",
            candidate_sha: "c".repeat(40),
            changed_files: ["pkg/store/store.go"],
            scope: {
              distance: "S0_exact",
              size: "M",
              files: 4,
              semantic_lines: 140,
              modules: 2,
              estimated: false,
              type_compatible: true,
              confidence: 0.96,
              explanation: "Exact but above the small-change threshold.",
            },
            validation_state: "succeeded",
            validation_checks: [
              {
                id: "unit",
                name: "Unit tests",
                status: "passed",
                summary: "All store tests passed.",
              },
            ],
            finding_count: 1,
            publication_kind: "github_review",
            payload_digest: "sha256:review-payload",
            repository: "acme/store",
            review_summary: "Publish the exact review summary.",
            publication_findings: [
              {
                id: `pfn_${"8".repeat(32)}`,
                title: "Lost update remains",
                file: "pkg/store/store.go",
                line: 42,
                message: "Publish this exact finding message.",
              },
            ],
            issue_title: "Deferred retry cleanup",
            issue_body: "Publish this exact deferred issue body.",
            issue_labels: ["deferred", "store"],
            repair_summary: "Publish this exact implementation summary.",
          },
          created_at: "2026-08-13T10:05:00Z",
        },
      ],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const evidence = await screen.findByText("Safe gate evidence")
    await userEvent.click(evidence)
    expect(screen.getByText("pkg/store/store.go")).toBeVisible()
    expect(screen.getByText(/Unit tests: passed/)).toBeVisible()
    expect(screen.queryByText(/All store tests passed/)).not.toBeInTheDocument()
    expect(screen.queryByText(/pinned_subject/)).not.toBeInTheDocument()
    expect(screen.getByText(/acme\/store/)).toBeVisible()
    expect(screen.getByText("Publish the exact review summary.")).toBeVisible()
    expect(screen.getByText("Lost update remains")).toBeVisible()
    expect(screen.getByText("pkg/store/store.go:42")).toBeVisible()
    expect(
      screen.getByText("Publish this exact finding message."),
    ).toBeVisible()
    expect(screen.getByText("Deferred retry cleanup")).toBeVisible()
    expect(
      screen.getByText("Publish this exact deferred issue body."),
    ).toBeVisible()
    expect(screen.getByText("deferred")).toBeVisible()
    expect(
      screen.getByText("Publish this exact implementation summary."),
    ).toBeVisible()
  })

  it("publishes review and implementation separately and reconciles unknown outcomes", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"6".repeat(32)}`
    const publicationID = `ppb_${"7".repeat(32)}`
    const publicationWorkspace: PRWorkspace = {
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "publication",
        execution_state: "unknown",
      },
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:finding",
          origin: "review",
          severity: "high",
          title: "Lost update",
          message: "The write is not fenced.",
          scope: {
            distance: "S0_exact",
            size: "S",
            files: 1,
            semantic_lines: 12,
            modules: 1,
            estimated: false,
            type_compatible: true,
            confidence: 0.98,
          },
          disposition: "in_scope",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      publications: [
        {
          id: `ppb_${"5".repeat(32)}`,
          kind: "github_review",
          state: "succeeded",
          expected_head_sha: "a".repeat(40),
          payload_digest: "sha256:prior-review-publication",
          external_id: "review-41",
          external_url:
            "https://github.com/octo/repo/pull/42#pullrequestreview-41",
          attempts: 1,
          created_at: "2026-08-13T10:05:00Z",
          updated_at: "2026-08-13T10:05:01Z",
          published_at: "2026-08-13T10:05:01Z",
        },
        {
          id: publicationID,
          kind: "github_review",
          state: "unknown",
          expected_head_sha: aggregate.provider_snapshot.head_sha,
          payload_digest: "sha256:review-publication",
          public_error_code: "provider_outcome_unknown",
          attempts: 1,
          created_at: "2026-08-13T10:06:00Z",
          updated_at: "2026-08-13T10:06:01Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(publicationWorkspace)
    mockedPublishPhase.mockResolvedValue(publicationWorkspace)
    mockedReconcilePublication.mockResolvedValue(publicationWorkspace)

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(await screen.findByText("Publication error:")).toBeVisible()
    expect(
      screen.getByText(
        "Guidance history is read-only after the workspace enters publication.",
      ),
    ).toBeVisible()
    expect(screen.getByLabelText("Guidance")).toBeDisabled()
    expect(screen.getByLabelText("Guidance target")).toBeDisabled()
    expect(screen.getByRole("button", { name: "Send guidance" })).toBeDisabled()
    expect(screen.getByText("provider_outcome_unknown")).toBeVisible()
    expect(screen.getByRole("link", { name: "Open result" })).toHaveAttribute(
      "href",
      "https://github.com/octo/repo/pull/42#pullrequestreview-41",
    )
    await user.click(
      screen.getByRole("button", { name: "Publish implementation" }),
    )
    await waitFor(() =>
      expect(mockedPublishPhase).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "implementation",
        {
          expected_version: 4,
          request_id: `prq_${"9".repeat(32)}`,
          expected_head_revision:
            publicationWorkspace.provider_snapshot.provider_revision,
        },
      ),
    )

    await user.click(screen.getByRole("button", { name: "Reconcile outcome" }))
    await waitFor(() =>
      expect(mockedReconcilePublication).toHaveBeenCalledWith(
        aggregate.workspace.id,
        publicationID,
        {
          expected_version: 4,
          request_id: `prq_${"9".repeat(32)}`,
          expected_head_revision:
            publicationWorkspace.provider_snapshot.provider_revision,
        },
      ),
    )
  })

  it("publishes an in-scope finding set in the review request", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"8".repeat(32)}`
    const publishable: PRWorkspace = {
      ...aggregate,
      workspace: { ...aggregate.workspace, phase: "triage" },
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:finding",
          origin: "review",
          severity: "high",
          title: "Lost update",
          message: "The write is not fenced.",
          scope: {
            distance: "S0_exact",
            size: "S",
            files: 1,
            semantic_lines: 12,
            modules: 1,
            estimated: false,
            type_compatible: true,
            confidence: 0.98,
          },
          disposition: "in_scope",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(publishable)
    mockedPublishPhase.mockResolvedValue(publishable)

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    await user.click(
      await screen.findByRole("button", { name: "Publish review" }),
    )
    await waitFor(() =>
      expect(mockedPublishPhase).toHaveBeenCalledWith(
        aggregate.workspace.id,
        "review",
        {
          expected_version: 4,
          request_id: `prq_${"9".repeat(32)}`,
          expected_head_revision:
            publishable.provider_snapshot.provider_revision,
          finding_ids: [findingID],
        },
      ),
    )
  })

  it("shows user-suppressed automatic issue publication and permits an explicit retry", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"2".repeat(32)}`
    const groupID = `pdg_${"3".repeat(32)}`
    const deferredScope = {
      distance: "S2_related_followup" as const,
      size: "S" as const,
      files: 1,
      semantic_lines: 18,
      modules: 1,
      estimated: true,
      type_compatible: true,
      confidence: 0.91,
    }
    const suppressedWorkspace: PRWorkspace = {
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:suppressed-follow-up",
          origin: "review",
          severity: "medium",
          title: "Retry cleanup",
          message: "Track retry cleanup outside this PR.",
          scope: deferredScope,
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: groupID,
          title: "Deferred retry cleanup",
          body: "Create a bounded follow-up issue after user review.",
          finding_ids: [findingID],
          scope: deferredScope,
          publication_suppressed: true,
          suppression_reason: "publication_gate_block",
          version: 2,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:05:00Z",
        },
      ],
    }
    mockedGetAssignments.mockResolvedValue({
      ...repositoryAssignments,
      workflowConfigurations: {
        default: {
          ...repositoryAssignments.workflowConfigurations.default,
          deferredIssues: { mode: "automatic" },
        },
      },
    })
    mockedGet.mockResolvedValue(suppressedWorkspace)
    mockedDeferredGroup.mockResolvedValue(suppressedWorkspace)
    mockedSyncAutomaticDeferred.mockResolvedValue(suppressedWorkspace)
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const group = within(
      await screen.findByRole("article", { name: "Deferred retry cleanup" }),
    )
    expect(group.getByText("Paused")).toBeVisible()
    expect(
      group.getByText("Automatic publication paused by your decision"),
    ).toBeVisible()
    expect(
      group.getByText("The publication gate blocked this issue."),
    ).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Retry automatic issue sync" }),
    )
    await waitFor(() =>
      expect(mockedSyncAutomaticDeferred).toHaveBeenCalledWith(
        aggregate.workspace.id,
        expect.objectContaining({ expected_version: 4 }),
      ),
    )
    await user.click(
      group.getByRole("button", { name: "Retry issue publication" }),
    )

    await waitFor(() =>
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        groupID,
        "publish",
        {
          expected_version: 4,
          request_id: `prq_${"9".repeat(32)}`,
        },
      ),
    )
  })

  it("does not present a saved deferred policy as active before restart", async () => {
    const findingID = `pfn_${"a".repeat(32)}`
    const groupID = `pdg_${"b".repeat(32)}`
    const scope = {
      distance: "S2_related_followup" as const,
      size: "S" as const,
      files: 1,
      semantic_lines: 12,
      modules: 1,
      estimated: true,
      type_compatible: true,
      confidence: 0.9,
    }
    mockedGet.mockResolvedValue({
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:restart-pending",
          origin: "review",
          severity: "medium",
          title: "Restart pending follow-up",
          message: "Wait for the configured policy to become active.",
          scope,
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: groupID,
          title: "Restart pending group",
          body: "Do not publish with an unconfirmed runtime policy.",
          finding_ids: [findingID],
          scope,
          publication_suppressed: true,
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
      ],
    })
    mockedGetAssignments.mockResolvedValue({
      ...repositoryAssignments,
      workflowConfigurations: {
        ...repositoryAssignments.workflowConfigurations,
        default: {
          ...repositoryAssignments.workflowConfigurations.default,
          deferredIssues: { mode: "automatic" },
        },
      },
      effects: {
        gatewayEffect: "restart-required",
        deferredPolicyEffect: "restart-required",
      },
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(
      await screen.findByText(/saved deferred issue policy is waiting/i),
    ).toBeVisible()
    expect(
      screen.queryByText(/Follow-up issues are created automatically/i),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Retry automatic issue sync" }),
    ).not.toBeInTheDocument()
    const group = within(
      screen.getByRole("article", { name: "Restart pending group" }),
    )
    expect(
      group.queryByRole("button", { name: "Retry issue publication" }),
    ).not.toBeInTheDocument()
  })

  it("does not mistake unavailable deferred settings for publication being off", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"d".repeat(32)}`
    const groupID = `pdg_${"e".repeat(32)}`
    const scope = {
      distance: "S2_related_followup" as const,
      size: "S" as const,
      presence: "follow_up" as const,
      files: 1,
      semantic_lines: 12,
      modules: 1,
      estimated: true,
      type_compatible: true,
      confidence: 0.92,
    }
    mockedGet.mockResolvedValue({
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:settings-recovery-follow-up",
          origin: "review",
          severity: "medium",
          title: "Track settings recovery",
          message: "Publish this separately after settings load.",
          scope,
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: groupID,
          title: "Settings recovery follow-up",
          body: "A bounded issue draft that must wait for known settings.",
          finding_ids: [findingID],
          scope,
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
      ],
    })
    mockedGetAssignments.mockRejectedValueOnce(new Error("settings offline"))

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(
      await screen.findByText(
        "Deferred issue settings are unavailable. Issue publication controls are disabled until the settings load.",
      ),
    ).toBeVisible()
    expect(
      screen.queryByText(
        "GitHub issue publication is off. Deferred findings remain available here.",
      ),
    ).not.toBeInTheDocument()
    const group = within(
      screen.getByRole("article", { name: "Settings recovery follow-up" }),
    )
    expect(
      group.queryByRole("button", { name: "Publish issue" }),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Retry settings" }))
    expect(
      await group.findByRole("button", { name: "Publish issue" }),
    ).toBeVisible()
  })

  it("opens a successfully published deferred issue from its group", async () => {
    const findingID = `pfn_${"4".repeat(32)}`
    const groupID = `pdg_${"5".repeat(32)}`
    const publicationID = `ppb_${"6".repeat(32)}`
    const externalURL = "https://github.com/octo/repo/issues/17"
    const deferredScope = {
      distance: "S2_related_followup" as const,
      size: "S" as const,
      presence: "follow_up" as const,
      files: 1,
      semantic_lines: 18,
      modules: 1,
      estimated: true,
      type_compatible: true,
      confidence: 0.94,
    }
    const publishedWorkspace: PRWorkspace = {
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:published-follow-up",
          origin: "review",
          severity: "medium",
          title: "Bounded follow-up",
          message: "Track this work outside the current PR.",
          scope: deferredScope,
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: groupID,
          title: "Published follow-up",
          body: "The issue was created from this deferred group.",
          finding_ids: [findingID],
          scope: deferredScope,
          publication_id: publicationID,
          version: 2,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:05:00Z",
        },
      ],
      publications: [
        {
          id: publicationID,
          kind: "github_issue",
          state: "succeeded",
          target_id: groupID,
          payload_digest: "sha256:published-issue",
          external_id: "17",
          external_url: externalURL,
          attempts: 1,
          created_at: "2026-08-13T10:05:00Z",
          updated_at: "2026-08-13T10:05:01Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(publishedWorkspace)

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const group = within(
      await screen.findByRole("article", { name: "Published follow-up" }),
    )
    const openIssue = group.getByRole("link", { name: "Open issue" })
    expect(openIssue).toHaveAttribute("href", externalURL)
    expect(openIssue).toHaveAttribute("target", "_blank")
    expect(openIssue).toHaveAttribute("rel", "noreferrer")
  })

  it("makes a cleared deferred group retryable after its historical publication fails", async () => {
    const user = userEvent.setup()
    const findingID = `pfn_${"7".repeat(32)}`
    const groupID = `pdg_${"8".repeat(32)}`
    const publicationID = `ppb_${"9".repeat(32)}`
    const deferredScope = {
      distance: "S2_related_followup" as const,
      size: "S" as const,
      presence: "follow_up" as const,
      files: 1,
      semantic_lines: 18,
      modules: 1,
      estimated: true,
      type_compatible: true,
      confidence: 0.94,
    }
    const retryableWorkspace: PRWorkspace = {
      ...aggregate,
      findings: [
        {
          id: findingID,
          fingerprint: "sha256:retryable-follow-up",
          origin: "review",
          severity: "medium",
          title: "Retry bounded follow-up",
          message: "Track this work outside the current PR.",
          scope: deferredScope,
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: groupID,
          title: "Retryable follow-up",
          body: "The blocked publication can be attempted again.",
          finding_ids: [findingID],
          scope: deferredScope,
          version: 3,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:07:00Z",
        },
      ],
      publications: [
        {
          id: publicationID,
          kind: "github_issue",
          state: "failed",
          target_id: groupID,
          payload_digest: "sha256:blocked-issue",
          public_error_code: "publication_gate_blocked",
          attempts: 1,
          created_at: "2026-08-13T10:05:00Z",
          updated_at: "2026-08-13T10:06:00Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(retryableWorkspace)
    mockedDeferredGroup.mockResolvedValue(retryableWorkspace)

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const group = within(
      await screen.findByRole("article", { name: "Retryable follow-up" }),
    )
    expect(group.getByText("draft")).toBeVisible()
    expect(group.getByText("Previous issue publication failed")).toBeVisible()
    expect(group.getByText("Failed")).toBeVisible()
    expect(group.getByText("publication_gate_blocked")).toBeVisible()
    expect(group.getByLabelText("Issue publication history")).toHaveTextContent(
      "Provider attempts: 1",
    )
    expect(group.getByRole("button", { name: "Publish issue" })).toBeVisible()

    await user.click(group.getByRole("button", { name: "Publish issue" }))
    await waitFor(() =>
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        groupID,
        "publish",
        expect.objectContaining({ expected_version: 4 }),
      ),
    )
  })

  it("creates, edits, splits, merges, links, publishes, and reconciles deferred work", async () => {
    const user = userEvent.setup()
    const firstFindingID = `pfn_${"a".repeat(32)}`
    const secondFindingID = `pfn_${"b".repeat(32)}`
    const thirdFindingID = `pfn_${"c".repeat(32)}`
    const fourthFindingID = `pfn_${"0".repeat(32)}`
    const firstGroupID = `pdg_${"d".repeat(32)}`
    const secondGroupID = `pdg_${"e".repeat(32)}`
    const thirdGroupID = `pdg_${"0".repeat(32)}`
    const publicationID = `ppb_${"f".repeat(32)}`
    const deferredScope = {
      distance: "S2_related_followup" as const,
      size: "M" as const,
      files: 4,
      semantic_lines: 120,
      modules: 2,
      estimated: true,
      type_compatible: true,
      confidence: 0.9,
      explanation: "Follow-up work outside the current charter.",
    }
    const deferredWorkspace: PRWorkspace = {
      ...aggregate,
      findings: [
        ...[
          firstFindingID,
          secondFindingID,
          thirdFindingID,
          fourthFindingID,
        ].map((id, index) => ({
          id,
          fingerprint: `sha256:deferred-${index}`,
          origin: "review" as const,
          severity: "medium",
          title: [
            "First deferred",
            "Second deferred",
            "Third deferred",
            "Fourth deferred",
          ][index],
          message: "Track this outside the current pull request.",
          scope: deferredScope,
          disposition: "deferred" as const,
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        })),
      ],
      deferred_groups: [
        {
          id: firstGroupID,
          title: "First follow-up",
          body: "Handle the first two findings.",
          finding_ids: [firstFindingID, secondFindingID],
          scope: deferredScope,
          labels: ["follow-up"],
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
        {
          id: secondGroupID,
          title: "Second follow-up",
          body: "Handle the third finding.",
          finding_ids: [thirdFindingID],
          scope: deferredScope,
          publication_id: publicationID,
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
        {
          id: thirdGroupID,
          title: "Merge target",
          body: "Available for a merge.",
          finding_ids: [fourthFindingID],
          scope: deferredScope,
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
      ],
      publications: [
        {
          id: publicationID,
          kind: "github_issue",
          state: "unknown",
          target_id: secondGroupID,
          payload_digest: "sha256:issue",
          public_error_code: "provider_outcome_unknown",
          attempts: 1,
          created_at: "2026-08-13T10:05:00Z",
          updated_at: "2026-08-13T10:05:01Z",
        },
      ],
    }
    const currentAfterConflict: PRWorkspace = {
      ...deferredWorkspace,
      workspace: { ...deferredWorkspace.workspace, version: 5 },
      deferred_groups: deferredWorkspace.deferred_groups.map((group) => ({
        ...group,
        title:
          group.id === firstGroupID ? "Concurrent server title" : group.title,
        version: group.version + 1,
      })),
    }
    mockedGet.mockResolvedValue(deferredWorkspace)
    mockedRegroupDeferred.mockResolvedValue(deferredWorkspace)
    mockedDeferredGroup.mockResolvedValue(deferredWorkspace)
    mockedUpdateDeferred
      .mockRejectedValueOnce(
        new PRWorkspaceAPIError(
          "deferred_group_temporarily_unavailable",
          503,
          "Deferred group could not be saved",
          currentAfterConflict,
        ),
      )
      .mockResolvedValueOnce(deferredWorkspace)
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Regroup findings" }),
    )
    expect(mockedRegroupDeferred).toHaveBeenCalled()

    const first = within(
      screen.getByRole("article", { name: "First follow-up" }),
    )
    await user.click(first.getByRole("button", { name: "Publish issue" }))
    await user.click(first.getByRole("button", { name: "Edit and organize" }))
    await user.clear(first.getByLabelText("Issue title"))
    await user.type(first.getByLabelText("Issue title"), "Bounded follow-up")
    await user.click(first.getByRole("button", { name: "Save group" }))
    expect(
      await screen.findByText("Deferred group could not be saved"),
    ).toBeVisible()
    expect(first.getByLabelText("Issue title")).toHaveValue("Bounded follow-up")

    await user.click(first.getByRole("button", { name: "Save group" }))
    await waitFor(() => expect(mockedUpdateDeferred).toHaveBeenCalledTimes(2))
    expect(mockedUpdateDeferred).toHaveBeenLastCalledWith(
      aggregate.workspace.id,
      firstGroupID,
      expect.objectContaining({ title: "Bounded follow-up" }),
    )
    await waitFor(() =>
      expect(first.queryByLabelText("Issue title")).not.toBeInTheDocument(),
    )

    await user.click(first.getByRole("button", { name: "Edit and organize" }))
    await user.click(first.getByRole("checkbox", { name: "First deferred" }))
    await user.click(first.getByRole("button", { name: "Split selected" }))
    await user.click(first.getByLabelText("Merge with another group"))
    await user.click(screen.getByRole("option", { name: "Merge target" }))
    await user.click(first.getByRole("button", { name: "Merge" }))
    await user.type(
      first.getByLabelText("Link an existing GitHub issue"),
      "https://github.com/octo/repo/issues/99",
    )
    await user.click(first.getByRole("button", { name: "Link" }))

    const second = within(
      screen.getByRole("article", { name: "Second follow-up" }),
    )
    await user.click(second.getByRole("button", { name: "Reconcile" }))
    await waitFor(() => {
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        firstGroupID,
        "publish",
        expect.any(Object),
      )
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        firstGroupID,
        "split",
        expect.objectContaining({ finding_ids: [firstFindingID] }),
      )
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        firstGroupID,
        "merge",
        expect.objectContaining({ group_ids: [firstGroupID, thirdGroupID] }),
      )
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        firstGroupID,
        "link",
        expect.objectContaining({
          existing_issue_url: "https://github.com/octo/repo/issues/99",
        }),
      )
      expect(mockedDeferredGroup).toHaveBeenCalledWith(
        aggregate.workspace.id,
        secondGroupID,
        "reconcile",
        expect.objectContaining({ publication_id: publicationID }),
      )
    })
  })

  it("retains deferred work but hides GitHub issue actions when publication is off", async () => {
    mockedGetAssignments.mockResolvedValue({
      ...repositoryAssignments,
      repositoryAssignments: {
        "https://github.com|100": "no-publication",
      },
      workflowConfigurations: {
        ...repositoryAssignments.workflowConfigurations,
        "no-publication": {
          name: "No publication",
          deferredIssues: { mode: "off" },
        },
      },
    })
    mockedGet.mockResolvedValue({
      ...aggregate,
      findings: [
        {
          id: `pfn_${"1".repeat(32)}`,
          fingerprint: "sha256:deferred",
          origin: "review",
          severity: "medium",
          title: "Keep deferred",
          message: "Retain this finding without publication.",
          scope: {
            distance: "S3_unrelated",
            size: "S",
            files: 1,
            semantic_lines: 20,
            modules: 1,
            estimated: true,
            type_compatible: true,
            confidence: 0.99,
          },
          disposition: "deferred",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      deferred_groups: [
        {
          id: `pdg_${"2".repeat(32)}`,
          title: "Retained follow-up",
          body: "Visible without publication controls.",
          finding_ids: [`pfn_${"1".repeat(32)}`],
          scope: {
            distance: "S3_unrelated",
            size: "S",
            files: 1,
            semantic_lines: 20,
            modules: 1,
            estimated: true,
            type_compatible: true,
            confidence: 0.99,
          },
          version: 1,
          created_at: "2026-08-13T10:04:00Z",
          updated_at: "2026-08-13T10:04:00Z",
        },
      ],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(await screen.findByText("Retained follow-up")).toBeVisible()
    expect(screen.getByText(/GitHub issue publication is off/)).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Publish issue" }),
    ).not.toBeInTheDocument()
  })

  it("disables publication actions when provider capabilities are absent", async () => {
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, phase: "publication" },
      provider_snapshot: {
        ...aggregate.provider_snapshot,
        can_review: false,
        head_writable: false,
      },
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    expect(
      await screen.findByRole("button", { name: "Publish review" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Publish implementation" }),
    ).toBeDisabled()
    expect(
      screen.getByText("The provider did not grant review publication access."),
    ).toBeVisible()
    expect(
      screen.getByText(
        "The provider did not verify write access to the PR head.",
      ),
    ).toBeVisible()
  })

  it("renders a generic Gate form and submits only field values", async () => {
    const user = userEvent.setup()
    const gateID = `pgr_${"8".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, execution_state: "waiting_user" },
      gates: [
        {
          id: gateID,
          decision_point: "pr.charter.reconfirm",
          state: "waiting_user",
          policy_revision: "sha256:gate-v3",
          subject_revision: "sha256:subject",
          turns: [
            {
              stage_id: "gate-exec",
              kind: "gate/exec",
              title: "Reconfirm charter",
              status: "waiting_user",
              actor_kind: "human",
              action_revision: "action-1",
              gate_form: {
                gate_ref: "gates.charter-confirm",
                prompt: "Review the revised charter.",
                fields: [
                  {
                    id: "action",
                    type: "select",
                    label: "What should happen?",
                    required: true,
                    min_selections: 1,
                    max_selections: 1,
                    options: [
                      { id: "approve", label: "Approve" },
                      { id: "revise", label: "Request revision" },
                    ],
                  },
                  {
                    id: "explanation",
                    type: "long-text",
                    label: "Explanation",
                    required: true,
                  },
                  {
                    id: "confirmed",
                    type: "boolean",
                    label: "Confirm evidence",
                    required: true,
                  },
                  {
                    id: "affected-areas",
                    type: "select",
                    label: "Affected areas",
                    required: false,
                    min_selections: 1,
                    max_selections: 2,
                    options: [
                      { id: "implementation", label: "Implementation" },
                      { id: "tests", label: "Tests" },
                    ],
                  },
                  {
                    id: "reference",
                    type: "short-text",
                    label: "Reference",
                    required: false,
                  },
                ],
              },
            },
          ],
          created_at: "2026-08-13T10:05:00Z",
        },
      ],
    })
    mockedRespondGate.mockResolvedValue({
      ...aggregate,
      workspace: { ...aggregate.workspace, execution_state: "succeeded" },
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    expect(await screen.findByText("Review the revised charter.")).toBeVisible()
    await user.click(
      screen.getByRole("combobox", { name: "What should happen?" }),
    )
    await user.click(screen.getByRole("option", { name: "Request revision" }))
    await user.type(screen.getByLabelText("Explanation"), "Scope is unclear.")
    await user.click(screen.getByRole("combobox", { name: "Confirm evidence" }))
    await user.click(screen.getByRole("option", { name: "Yes" }))
    await user.click(screen.getByRole("checkbox", { name: "Implementation" }))
    await user.click(screen.getByRole("checkbox", { name: "Tests" }))
    await user.type(screen.getByLabelText("Reference"), "charter-v2")
    await user.click(
      screen.getByRole("button", { name: "Submit Gate response" }),
    )

    await waitFor(() =>
      expect(mockedRespondGate).toHaveBeenCalledWith(
        aggregate.workspace.id,
        gateID,
        expect.objectContaining({
          fieldValues: {
            action: "revise",
            explanation: "Scope is unclear.",
            confirmed: true,
            "affected-areas": ["implementation", "tests"],
            reference: "charter-v2",
          },
        }),
      ),
    )
  })

  it("edits deferred issue policy on the named Workflow configuration", async () => {
    const user = userEvent.setup()
    renderPage(
      <PRLifecycleWorkflowConfigurationsPage
        activeFlowID="review"
        initialConfigID="default"
        onBack={vi.fn()}
        page="config"
      />,
    )

    const mode = await screen.findByRole("combobox", {
      name: "Deferred issue mode",
    })
    expect(mode).toHaveTextContent("Ask")
    await user.click(mode)
    await user.click(screen.getByRole("option", { name: "Automatic" }))
    await user.click(screen.getByRole("button", { name: "Save configuration" }))

    await waitFor(() =>
      expect(mockedPutWorkflowConfigurations).toHaveBeenCalledWith(
        expect.objectContaining({
          workflowConfigurations: expect.objectContaining({
            default: expect.objectContaining({
              deferredIssues: { mode: "automatic" },
            }),
          }),
        }),
      ),
    )
  })

  it("keeps only the built-in default name and Gate bindings read only", async () => {
    const user = userEvent.setup()
    renderPage(
      <PRLifecycleWorkflowConfigurationsPage
        activeFlowID="review"
        initialConfigID="default"
        onBack={vi.fn()}
        page="config"
      />,
    )

    const name = await screen.findByLabelText("Configuration name")
    expect(name).toHaveValue("Default")
    expect(name).toBeDisabled()
    expect(
      screen.getByRole("combobox", { name: "Deferred issue mode" }),
    ).toBeEnabled()
    await user.hover(
      screen.getByRole("button", {
        name: "Why Configuration name is fixed",
      }),
    )
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      /built-in default configuration has a fixed name/i,
    )

    await user.click(
      screen.getByRole("button", { name: "Approve purpose and scope" }),
    )
    const dialog = await screen.findByRole("dialog", {
      name: "Approve purpose and scope",
    })
    expect(
      within(dialog).getByRole("combobox", { name: "Execution action" }),
    ).toBeDisabled()
    expect(within(dialog).getByRole("note")).toHaveTextContent(
      /create a custom configuration to override Gate actions/i,
    )
  })

  it("shows Workflow configuration defaults and creates an atomic AI override", async () => {
    const user = userEvent.setup()
    renderPage(
      <PRLifecycleWorkflowConfigurationsPage
        activeFlowID="review"
        initialDecisionPoint="pr.finding.classify"
        initialConfigID="editable"
        onBack={vi.fn()}
        page="config"
      />,
    )

    const dialog = await screen.findByRole("dialog", {
      name: "Decide ambiguous finding scope",
    })
    expect(within(dialog).getByText("Workflow default")).toBeVisible()
    expect(within(dialog).getAllByText("Human").length).toBeGreaterThan(0)
    expect(
      within(dialog).getByRole("heading", { name: "Gate request" }),
    ).toBeVisible()
    expect(within(dialog).getByText("What should happen?")).toBeVisible()
    expect(
      within(dialog).getByText(
        /Approve \(approve\), Request revision \(revise\)/,
      ),
    ).toBeVisible()
    await user.click(
      within(dialog).getByRole("combobox", { name: "Execution action" }),
    )
    await user.click(screen.getByRole("option", { name: "AI" }))
    expect(within(dialog).getByLabelText("Agent ID")).toHaveValue("main")
    expect(within(dialog).getAllByText("AI · main · Ephemeral")).toHaveLength(2)
    expect(
      within(dialog).getByRole("combobox", { name: "Session" }),
    ).toHaveTextContent("Ephemeral")
    expect(within(dialog).getByLabelText("History")).toHaveValue("None")
    expect(within(dialog).getByLabelText("History")).toBeDisabled()
    expect(within(dialog).getByLabelText("Cache")).toHaveValue("None")
    expect(within(dialog).getByLabelText("Cache")).toBeDisabled()
    expect(within(dialog).getByLabelText("Tools")).toHaveValue("None")
    expect(within(dialog).getByLabelText("Tools")).toBeDisabled()

    await user.click(within(dialog).getByRole("combobox", { name: "Session" }))
    await user.click(
      screen.getByRole("option", { name: "Originating snapshot" }),
    )
    expect(within(dialog).getByLabelText("Agent ID")).toHaveValue(
      "Same originating agent",
    )
    expect(within(dialog).getByLabelText("Agent ID")).toBeDisabled()
    expect(within(dialog).getByLabelText("History")).toHaveValue(
      "Exact source snapshot (read only)",
    )
    expect(within(dialog).getByLabelText("Cache")).toHaveValue("None")
    expect(within(dialog).getByLabelText("Tools")).toHaveValue("None")
    expect(within(dialog).getByLabelText("AI prompt")).toBeEnabled()
    expect(
      within(dialog).getByText(/stops without falling back/i),
    ).toBeVisible()
    await user.hover(
      within(dialog).getByRole("button", { name: "Why Tools is fixed" }),
    )
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      /same policy.*tool authority remains none/i,
    )

    await user.click(within(dialog).getByRole("combobox", { name: "Session" }))
    await user.click(screen.getByRole("option", { name: "Private snapshot" }))
    expect(within(dialog).getByLabelText("Agent ID")).toHaveValue("main")
    expect(within(dialog).getByLabelText("Agent ID")).toBeEnabled()
    expect(within(dialog).getByLabelText("History")).toHaveValue("Read only")
    expect(within(dialog).getByLabelText("History")).toBeDisabled()
    expect(
      within(dialog).getByRole("combobox", { name: "Cache" }),
    ).toHaveTextContent("Session")
    expect(within(dialog).getByLabelText("Tools")).toHaveValue("None")
    expect(within(dialog).getByLabelText("Tools")).toBeDisabled()

    await user.click(within(dialog).getByRole("combobox", { name: "Session" }))
    await user.click(screen.getByRole("option", { name: "Ephemeral" }))
    expect(within(dialog).getByLabelText("Agent ID")).toHaveValue("main")
    expect(within(dialog).getByLabelText("Agent ID")).toBeEnabled()
    await user.click(
      within(dialog).getByRole("button", { name: "Close — keep draft" }),
    )
    expect(
      screen.getByRole("button", { name: "Save configuration" }),
    ).toBeEnabled()
  })

  it("preserves an inherited unsupported source snapshot visibly", async () => {
    mockedGetWorkflowConfigurations.mockResolvedValueOnce({
      ...workflowConfigurations,
      gateCatalog: {
        ...workflowConfigurations.gateCatalog,
        "pr.charter.confirm": {
          ...workflowConfigurations.gateCatalog["pr.charter.confirm"],
          defaultAction: {
            type: "ai",
            prompt: "Recheck the originating finding.",
            session: "source",
          },
          effectiveAction: {
            type: "ai",
            prompt: "Recheck the originating finding.",
            session: "source",
          },
        },
      },
    })
    renderPage(
      <PRLifecycleWorkflowConfigurationsPage
        activeFlowID="review"
        initialDecisionPoint="pr.charter.confirm"
        initialConfigID="editable"
        onBack={vi.fn()}
        page="config"
      />,
    )

    const dialog = await screen.findByRole("dialog", {
      name: "Approve purpose and scope",
    })
    expect(within(dialog).getByLabelText("Session")).toHaveValue(
      "Originating snapshot",
    )
    expect(within(dialog).getByLabelText("Session")).toBeDisabled()
    expect(within(dialog).getByLabelText("AI prompt")).toHaveValue(
      "Recheck the originating finding.",
    )
    expect(within(dialog).getByLabelText("AI prompt")).toBeDisabled()
    expect(
      within(dialog).getByText(/inherits its published workflow default/i),
    ).toBeVisible()
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      /does not publish a source-bearing finding/i,
    )
  })

  it("preserves edits made while a Workflow configuration save is in flight", async () => {
    const user = userEvent.setup()
    let resolveSave!: (value: PRLifecycleWorkflowConfigurationSnapshot) => void
    mockedPutWorkflowConfigurations.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveSave = resolve
      }),
    )
    renderPage(
      <PRLifecycleWorkflowConfigurationsPage
        activeFlowID="review"
        initialConfigID="editable"
        onBack={vi.fn()}
        page="config"
      />,
    )

    const name = await screen.findByLabelText("Configuration name")
    await user.clear(name)
    await user.type(name, "Submitted name")
    await user.click(screen.getByRole("button", { name: "Save configuration" }))
    await user.clear(name)
    await user.type(name, "Continued editing")

    await act(async () => {
      resolveSave({
        ...workflowConfigurations,
        workflowConfigurations: {
          ...workflowConfigurations.workflowConfigurations,
          editable: {
            name: "Submitted name",
            bindings: [],
            deferredIssues: { mode: "ask" },
          },
        },
        configRevision: "sha256:config-2",
      })
    })

    await waitFor(() => expect(name).toHaveValue("Continued editing"))
    expect(
      screen.getByRole("button", { name: "Save configuration" }),
    ).toBeEnabled()
  })
})
