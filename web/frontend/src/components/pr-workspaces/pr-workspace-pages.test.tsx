import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactNode, useState } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleGateProfileSnapshot,
  getPRLifecycleGateProfiles,
  putPRLifecycleGateProfiles,
} from "@/api/pr-lifecycle-gate-profiles"
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
import { PRLifecycleGateProfilesPage } from "@/components/pr-workspaces/pr-lifecycle-gate-profiles-page"
import { PRWorkspacePage } from "@/components/pr-workspaces/pr-workspace-page"
import { PRWorkspacePortfolioPage } from "@/components/pr-workspaces/pr-workspace-portfolio-page"
import { SidebarProvider } from "@/components/ui/sidebar"

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

function setMockNavigationBlocked() {
  const release = () => setMockNavigationIdle()
  mockedNavigationBlocker.current = {
    status: "blocked",
    current: undefined,
    next: undefined,
    action: undefined,
    proceed: release,
    reset: release,
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

vi.mock("@/api/pr-lifecycle-gate-profiles", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/pr-lifecycle-gate-profiles")>()
  return {
    ...original,
    getPRLifecycleGateProfiles: vi.fn(),
    putPRLifecycleGateProfiles: vi.fn(),
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

const gateProfiles: PRLifecycleGateProfileSnapshot = {
  gate_profiles: {
    default: {
      name: "Default",
      workflows: {
        "pr.review.complete": {
          id: "review_complete",
          name: "Review complete",
          purpose: "authorization",
          decision_point: "pr.review.complete",
          stages: [{ id: "automatic", kind: "zero" }],
        },
      },
    },
  },
  default_gate_profile_id: "default",
  repository_assignments: {},
  nudge: {
    review_minimum_additional: 2,
    review_maximum_additional: 5,
    completion_minimum_additional: 2,
    completion_maximum_additional: 5,
  },
  scope: {
    xs: { files: 1, semantic_lines: 20, modules: 1 },
    s: { files: 3, semantic_lines: 100, modules: 1 },
    m: { files: 10, semantic_lines: 500, modules: 3 },
  },
  deferred_issues: { mode: "ask" },
  flow: {
    schema: "pr-lifecycle-flow/v1",
    flows: [
      {
        id: "review",
        title: "Review workflow",
        entry: "review_complete",
        nodes: [
          {
            id: "charter_confirm",
            kind: "gate",
            title: "Approve purpose and scope",
            description: "Checks the initial PR charter.",
            decision_point: "pr.charter.confirm",
            ordinal: 1,
            editable: true,
          },
          {
            id: "charter_reconfirm",
            kind: "gate",
            title: "Approve revised purpose and scope",
            description: "Checks a revised PR charter.",
            decision_point: "pr.charter.reconfirm",
            ordinal: 2,
            editable: true,
          },
          {
            id: "review_start",
            kind: "gate",
            title: "Allow AI review",
            description: "Checks whether review may begin.",
            decision_point: "pr.review.start",
            ordinal: 3,
            editable: true,
          },
          {
            id: "review_complete",
            kind: "gate",
            title: "Accept review results",
            description: "Checks review coverage before follow-up begins.",
            decision_point: "pr.review.complete",
            ordinal: 4,
            editable: true,
          },
          {
            id: "finding_classify",
            kind: "gate",
            title: "Decide ambiguous finding scope",
            description: "Classifies an ambiguous finding.",
            decision_point: "pr.finding.classify",
            ordinal: 5,
            editable: true,
          },
          {
            id: "review_publish",
            kind: "gate",
            title: "Allow review publication",
            description: "Checks whether the review may be published.",
            decision_point: "pr.review.publish",
            ordinal: 10,
            editable: true,
          },
          {
            id: "deferred_publish",
            kind: "gate",
            title: "Allow follow-up issue",
            description: "Checks whether a follow-up issue may be created.",
            decision_point: "pr.deferred.publish",
            ordinal: 12,
            editable: true,
          },
          {
            id: "correction_promote",
            kind: "gate",
            title: "Allow repository lesson",
            description: "Checks whether correction guidance may be saved.",
            decision_point: "pr.correction.promote",
            ordinal: 13,
            editable: true,
          },
          {
            id: "publication_reconcile",
            kind: "gate",
            title: "Resolve unknown publication",
            description: "Checks how an unknown provider result is resolved.",
            decision_point: "pr.publication.reconcile",
            ordinal: 14,
            editable: true,
          },
        ],
        edges: [],
      },
      {
        id: "implementation",
        title: "Implementation workflow",
        entry: "implementation_start",
        nodes: [
          {
            id: "implementation_start",
            kind: "action",
            title: "Load selected findings",
            description: "Bring selected findings into implementation.",
            operation: "pr.implementation.findings.load",
            editable: false,
          },
          {
            id: "implementation_eligibility",
            kind: "gate",
            title: "Allow non-owned PR implementation",
            description: "Checks implementation authority.",
            decision_point: "pr.implementation.eligibility",
            ordinal: 6,
            editable: true,
          },
          {
            id: "implementation_start_gate",
            kind: "gate",
            title: "Allow AI implementation",
            description: "Checks whether implementation may begin.",
            decision_point: "pr.implementation.start",
            ordinal: 7,
            editable: true,
          },
          {
            id: "implementation_scope",
            kind: "gate",
            title: "Allow large or adjacent work",
            description: "Checks policy-gated candidate scope.",
            decision_point: "pr.implementation.scope",
            ordinal: 8,
            editable: true,
          },
          {
            id: "implementation_complete",
            kind: "gate",
            title: "Accept implementation",
            description: "Checks whether implementation is complete.",
            decision_point: "pr.implementation.complete",
            ordinal: 9,
            editable: true,
          },
          {
            id: "implementation_publish",
            kind: "gate",
            title: "Allow branch push",
            description: "Checks whether the branch may be pushed.",
            decision_point: "pr.implementation.publish",
            ordinal: 11,
            editable: true,
          },
        ],
        edges: [],
      },
    ],
  },
  flow_revision: "sha256:flow",
  catalog_revision: "sha256:catalog",
  config_revision: "sha256:config",
  effects: { gateway_effect: "applied" },
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
const mockedGetGateProfiles = vi.mocked(getPRLifecycleGateProfiles)
const mockedPutGateProfiles = vi.mocked(putPRLifecycleGateProfiles)

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
    mockedGetGateProfiles.mockResolvedValue(gateProfiles)
    mockedPutGateProfiles.mockResolvedValue(gateProfiles)
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
        onOpenGateProfiles={vi.fn()}
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

  it("prioritizes a waiting gate and clears its private draft when the gate changes", async () => {
    const user = userEvent.setup()
    const firstGateID = `pgr_${"1".repeat(32)}`
    const secondGateID = `pgr_${"2".repeat(32)}`
    const firstGate: PRWorkspace["gates"][number] = {
      id: firstGateID,
      decision_point: "pr.review.complete",
      purpose: "authorization",
      state: "waiting_user",
      policy_revision: "sha256:first-policy",
      subject_revision: "sha256:first-subject",
      turns: [
        {
          stage_id: "first-human",
          kind: "human",
          title: "Approve first gate",
          status: "waiting_user",
          questions: ["Is the first review complete?"],
        },
      ],
      created_at: "2026-08-13T10:05:00Z",
    }
    const secondGate: PRWorkspace["gates"][number] = {
      id: secondGateID,
      decision_point: "pr.implementation.complete",
      purpose: "authorization",
      state: "queued",
      policy_revision: "sha256:second-policy",
      subject_revision: "sha256:second-subject",
      turns: [
        {
          stage_id: "second-human",
          kind: "human",
          title: "Approve second gate",
          status: "queued",
          questions: ["Is the implementation fully complete?"],
        },
      ],
      created_at: "2026-08-13T10:06:00Z",
    }
    const firstWorkspace: PRWorkspace = {
      ...aggregate,
      workspace: { ...aggregate.workspace, execution_state: "waiting_gate" },
      gates: [firstGate, secondGate],
    }
    const secondWorkspace: PRWorkspace = {
      ...firstWorkspace,
      workspace: {
        ...firstWorkspace.workspace,
        execution_state: "waiting_user",
        version: 5,
      },
      gates: [
        {
          ...firstGate,
          state: "succeeded",
          outcome: "pass",
          turns: [
            {
              ...firstGate.turns[0],
              status: "succeeded",
              outcome: "pass",
            },
          ],
          finished_at: "2026-08-13T10:06:30Z",
        },
        {
          ...secondGate,
          state: "waiting_user",
          turns: [{ ...secondGate.turns[0], status: "waiting_user" }],
        },
      ],
    }
    const completedWorkspace: PRWorkspace = {
      ...secondWorkspace,
      workspace: {
        ...secondWorkspace.workspace,
        execution_state: "succeeded",
        version: 6,
      },
      gates: [
        secondWorkspace.gates[0],
        {
          ...secondWorkspace.gates[1],
          state: "succeeded",
          outcome: "pass",
          turns: [
            {
              ...secondWorkspace.gates[1].turns[0],
              status: "succeeded",
              outcome: "pass",
            },
          ],
          finished_at: "2026-08-13T10:07:30Z",
        },
      ],
    }
    mockedGet.mockResolvedValue(firstWorkspace)
    mockedRespondGate
      .mockResolvedValueOnce(secondWorkspace)
      .mockResolvedValueOnce(completedWorkspace)

    const { queryClient } = renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const gatePanel = await screen.findByTestId("pr-gates")
    const charter = screen.getByTestId("pr-stage-charter")
    expect(
      gatePanel.compareDocumentPosition(charter) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    await user.type(
      screen.getByLabelText("Is the first review complete?"),
      "Yes, after checking callers.",
    )
    await user.type(
      screen.getByLabelText("Decision comment"),
      "Only for the first gate",
    )
    await user.click(screen.getByRole("button", { name: "Pass" }))

    await waitFor(() =>
      expect(mockedRespondGate).toHaveBeenCalledWith(
        aggregate.workspace.id,
        firstGateID,
        expect.objectContaining({
          outcome: "pass",
          answers: { question_1: "Yes, after checking callers." },
          comment: "Only for the first gate",
        }),
      ),
    )
    const nextAnswer = await screen.findByLabelText(
      "Is the implementation fully complete?",
    )
    expect(nextAnswer).toHaveValue("")
    expect(screen.getByLabelText("Decision comment")).toHaveValue("")
    expect(screen.getByTestId("pr-gates")).toHaveAttribute("aria-busy", "false")

    // A poll started before the first response may finish afterward. It must not
    // replace the version-5 aggregate used by the newly displayed gate.
    mockedGet.mockResolvedValue(firstWorkspace)
    await queryClient.refetchQueries({
      queryKey: ["pr-workspace", aggregate.workspace.id],
    })
    expect(
      screen.getByLabelText("Is the implementation fully complete?"),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Pass" }))
    await waitFor(() =>
      expect(mockedRespondGate).toHaveBeenNthCalledWith(
        2,
        aggregate.workspace.id,
        secondGateID,
        expect.objectContaining({
          expected_version: 5,
          outcome: "pass",
          answers: {},
        }),
      ),
    )
  })

  it("clears private gate input when the next human stage uses the same gate", async () => {
    const user = userEvent.setup()
    const gateID = `pgr_${"3".repeat(32)}`
    const firstWorkspace: PRWorkspace = {
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        execution_state: "waiting_user",
      },
      gates: [
        {
          id: gateID,
          decision_point: "pr.implementation.complete",
          purpose: "authorization",
          state: "waiting_user",
          policy_revision: "sha256:two-human-policy",
          subject_revision: "sha256:two-human-subject",
          turns: [
            {
              stage_id: "human-scope",
              kind: "human",
              title: "Confirm scope",
              status: "waiting_user",
              questions: ["Is the candidate within the confirmed scope?"],
            },
            {
              stage_id: "human-completion",
              kind: "human",
              title: "Confirm completion",
              status: "queued",
              questions: ["Is the candidate fully complete?"],
            },
          ],
          created_at: "2026-08-13T10:05:00Z",
        },
      ],
    }
    const secondWorkspace: PRWorkspace = {
      ...firstWorkspace,
      workspace: { ...firstWorkspace.workspace, version: 5 },
      gates: [
        {
          ...firstWorkspace.gates[0],
          turns: [
            {
              ...firstWorkspace.gates[0].turns[0],
              status: "succeeded",
              outcome: "pass",
            },
            {
              ...firstWorkspace.gates[0].turns[1],
              status: "waiting_user",
            },
          ],
        },
      ],
    }
    const completedWorkspace: PRWorkspace = {
      ...secondWorkspace,
      workspace: {
        ...secondWorkspace.workspace,
        execution_state: "succeeded",
        version: 6,
      },
      gates: [
        {
          ...secondWorkspace.gates[0],
          state: "succeeded",
          outcome: "pass",
          turns: [
            secondWorkspace.gates[0].turns[0],
            {
              ...secondWorkspace.gates[0].turns[1],
              status: "succeeded",
              outcome: "pass",
            },
          ],
        },
      ],
    }
    mockedGet.mockResolvedValue(firstWorkspace)
    mockedRespondGate
      .mockResolvedValueOnce(secondWorkspace)
      .mockResolvedValueOnce(completedWorkspace)

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    await user.type(
      await screen.findByLabelText(
        "Is the candidate within the confirmed scope?",
      ),
      "Yes, every changed hunk is exact-scope.",
    )
    await user.type(
      screen.getByLabelText("Decision comment"),
      "Scope-stage-only comment",
    )
    await user.click(screen.getByRole("button", { name: "Pass" }))

    const completionAnswer = await screen.findByLabelText(
      "Is the candidate fully complete?",
    )
    expect(completionAnswer).toHaveValue("")
    expect(screen.getByLabelText("Decision comment")).toHaveValue("")

    await user.type(completionAnswer, "Yes, all acceptance criteria pass.")
    await user.type(
      screen.getByLabelText("Decision comment"),
      "Completion-stage-only comment",
    )
    await user.click(screen.getByRole("button", { name: "Pass" }))

    await waitFor(() =>
      expect(mockedRespondGate).toHaveBeenNthCalledWith(
        2,
        aggregate.workspace.id,
        gateID,
        expect.objectContaining({
          expected_version: 5,
          outcome: "pass",
          answers: {
            question_1: "Yes, all acceptance criteria pass.",
          },
          comment: "Completion-stage-only comment",
        }),
      ),
    )
  })

  it("keeps automatic gate stages read-only until a human turn is waiting", async () => {
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
          purpose: "authorization",
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
      /Automatic gate checks are still running/i,
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
        onOpenGateProfiles={vi.fn()}
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
          purpose: "authorization",
          state: "waiting_user",
          policy_revision: "sha256:policy-revision",
          subject_revision: "sha256:subject-revision",
          turns: [
            {
              stage_id: "human-scope",
              kind: "human",
              title: "Approve exact large scope",
              status: "waiting_user",
              questions: ["Is this exact work still appropriate for this PR?"],
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

  it("routes candidate-present implementation drift through its scope gate", async () => {
    const driftID = `pfn_${"7".repeat(32)}`
    mockedGet.mockResolvedValue({
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        phase: "implementation",
        execution_state: "waiting_gate",
      },
      findings: [
        {
          id: driftID,
          fingerprint: "sha256:candidate-drift",
          origin: "implementation",
          severity: "high",
          title: "Adjacent cleanup is already in the candidate",
          message: "Remove or explicitly resolve this candidate change.",
          scope: {
            distance: "S2_related_followup",
            size: "S",
            presence: "candidate_present",
            files: 1,
            semantic_lines: 18,
            modules: 1,
            estimated: false,
            type_compatible: true,
            confidence: 0.97,
          },
          disposition: "open",
          version: 1,
          created_at: "2026-08-13T10:03:00Z",
          updated_at: "2026-08-13T10:03:00Z",
        },
      ],
      gates: [
        {
          id: `pgr_${"8".repeat(32)}`,
          decision_point: "pr.implementation.scope",
          purpose: "authorization",
          target_id: `pra_${"9".repeat(32)}`,
          state: "waiting_user",
          policy_revision: "builtin:hard-scope-resolution-v1",
          subject_revision: "sha256:hard-scope",
          turns: [
            {
              stage_id: "human-hard-scope-resolution",
              kind: "human",
              title: "Resolve candidate code outside the confirmed charter",
              status: "waiting",
            },
          ],
          evidence: { finding_ids: [driftID] },
          created_at: "2026-08-13T10:04:00Z",
        },
      ],
    })
    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )

    const finding = within(
      (
        await screen.findByText("Adjacent cleanup is already in the candidate")
      ).closest("article")!,
    )
    expect(
      finding.getByText(/Resolve the implementation scope gate/),
    ).toBeVisible()
    expect(
      finding.queryByRole("button", { name: "Defer from this PR" }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Remove code + defer follow-up" }),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Revise and reconfirm charter" }),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Stop implementation" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Pass" }),
    ).not.toBeInTheDocument()
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

  it("continues reconciliation immediately after its human gate passes", async () => {
    const user = userEvent.setup()
    const publicationID = `ppb_${"7".repeat(32)}`
    const gateID = `pgr_${"8".repeat(32)}`
    const waiting: PRWorkspace = {
      ...aggregate,
      workspace: {
        ...aggregate.workspace,
        execution_state: "waiting_user",
      },
      publications: [
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
      gates: [
        {
          id: gateID,
          decision_point: "pr.publication.reconcile",
          target_id: publicationID,
          purpose: "authorization",
          state: "waiting_user",
          policy_revision: "sha256:policy",
          subject_revision: "sha256:subject",
          turns: [
            {
              stage_id: "human-reconcile",
              kind: "human",
              title: "Resolve ambiguous provider outcome",
              status: "waiting",
            },
          ],
          created_at: "2026-08-13T10:06:02Z",
        },
      ],
    }
    const authorized: PRWorkspace = {
      ...waiting,
      workspace: { ...waiting.workspace, version: 5 },
      gates: [
        {
          ...waiting.gates[0],
          state: "succeeded",
          outcome: "pass",
          turns: [
            {
              ...waiting.gates[0].turns[0],
              status: "answered",
              outcome: "pass",
            },
          ],
        },
      ],
    }
    mockedGet.mockResolvedValue(waiting)
    mockedRespondGate.mockResolvedValue(authorized)
    mockedReconcilePublication.mockResolvedValue({
      ...authorized,
      publications: [
        {
          ...authorized.publications[0],
          state: "succeeded",
          public_error_code: "",
        },
      ],
    })

    renderPage(
      <PRWorkspacePage workspaceID={aggregate.workspace.id} onBack={vi.fn()} />,
    )
    expect(
      await screen.findByRole("button", { name: "Check provider again" }),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Assume failed" })).toBeVisible()
    for (const unavailableOutcome of ["Pass", "Revise", "Defer", "Block"]) {
      expect(
        screen.queryByRole("button", { name: unavailableOutcome }),
      ).not.toBeInTheDocument()
    }
    await user.click(
      screen.getByRole("button", { name: "Check provider again" }),
    )

    await waitFor(() =>
      expect(mockedReconcilePublication).toHaveBeenCalledWith(
        aggregate.workspace.id,
        publicationID,
        {
          expected_version: 5,
          request_id: `prq_${"9".repeat(32)}`,
          expected_head_revision:
            authorized.provider_snapshot.provider_revision,
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
    mockedGetGateProfiles.mockResolvedValue({
      ...gateProfiles,
      deferred_issues: { mode: "automatic" },
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
    mockedGetGateProfiles.mockRejectedValueOnce(new Error("settings offline"))

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
    mockedGetGateProfiles.mockResolvedValue({
      ...gateProfiles,
      deferred_issues: { mode: "off" },
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

  it("keeps the profiles page list-only and delegates profile navigation", async () => {
    const user = userEvent.setup()
    const onOpenSettings = vi.fn()
    const onProfileChange = vi.fn()
    renderPage(
      <PRLifecycleGateProfilesPage
        onBack={vi.fn()}
        page="profiles"
        onOpenProfiles={vi.fn()}
        onOpenSettings={onOpenSettings}
        onProfileChange={onProfileChange}
      />,
    )

    expect(
      await screen.findByRole("heading", {
        name: "PR lifecycle gate profiles",
      }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { level: 2, name: "Profiles" }),
    ).toBeVisible()
    expect(screen.getByTestId("pr-gate-profiles")).toHaveAttribute(
      "data-profile-view",
      "profiles",
    )
    expect(screen.getByText("1 configured gate · 0 repositories")).toBeVisible()
    expect(screen.getByRole("button", { name: "Profiles" })).toHaveAttribute(
      "aria-current",
      "page",
    )
    expect(
      screen.getByRole("button", { name: "Lifecycle settings" }),
    ).not.toHaveAttribute("aria-current")
    expect(screen.queryByText("Review minimum")).not.toBeInTheDocument()
    expect(
      screen.queryByText("Deferred issue handling"),
    ).not.toBeInTheDocument()
    expect(screen.queryByText("XS modules")).not.toBeInTheDocument()
    expect(
      screen.queryByRole("heading", { name: "PR lifecycle gate flow" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByPlaceholderText("https://github.com|repository-id"),
    ).not.toBeInTheDocument()
    expect(
      document.querySelector("#pr-gate-workflow-editor"),
    ).not.toBeInTheDocument()

    const profileID = screen.getByLabelText("Stable profile ID")
    const profileName = screen.getByLabelText("Profile name")
    const addProfile = screen.getByRole("button", { name: "Add profile" })
    await user.type(profileID, "Bad Profile")
    await user.type(profileName, "Unreachable")
    expect(profileID).toHaveAttribute("aria-invalid", "true")
    expect(addProfile).toBeDisabled()
    expect(screen.queryByText("Unreachable")).not.toBeInTheDocument()
    await user.clear(profileID)
    await user.clear(profileName)
    await user.type(profileID, "default")
    await user.type(profileName, "Duplicate")
    expect(profileID).toHaveAttribute("aria-invalid", "true")
    expect(addProfile).toBeDisabled()
    expect(
      screen.getByText("A profile with this ID already exists."),
    ).toBeVisible()
    await user.clear(profileID)
    await user.clear(profileName)

    await user.click(
      screen.getByRole("button", { name: "Edit Default profile" }),
    )
    expect(onProfileChange).toHaveBeenLastCalledWith("default")
    expect(screen.getByRole("heading", { name: "Profiles" })).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Lifecycle settings" }))
    expect(onOpenSettings).toHaveBeenCalledTimes(1)
    expect(screen.queryByText("Review minimum")).not.toBeInTheDocument()
  })

  it("shows one lifecycle-settings tab at a time and reports tab navigation", async () => {
    const user = userEvent.setup()
    const onOpenProfiles = vi.fn()
    const onSettingsTabChange = vi.fn()

    function SettingsHarness() {
      const [tab, setTab] = useState<"nudging" | "scope" | "deferred">(
        "nudging",
      )
      return (
        <PRLifecycleGateProfilesPage
          onBack={vi.fn()}
          page="settings"
          settingsTab={tab}
          onOpenProfiles={onOpenProfiles}
          onOpenSettings={vi.fn()}
          onSettingsTabChange={(next) => {
            onSettingsTabChange(next)
            setTab(next)
          }}
        />
      )
    }

    renderPage(<SettingsHarness />)

    expect(
      await screen.findByRole("heading", { name: "Lifecycle settings" }),
    ).toBeVisible()
    expect(screen.getByTestId("pr-gate-profiles")).toHaveAttribute(
      "data-profile-view",
      "settings",
    )
    expect(screen.getByText("Review minimum")).toBeVisible()
    expect(screen.getByText("Completion maximum")).toBeVisible()
    expect(screen.queryByText("XS files")).not.toBeInTheDocument()
    expect(
      screen.queryByText("Deferred issue handling"),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole("heading", { name: "Profiles" })).toBeNull()

    screen.getByRole("tab", { name: "Nudging" }).focus()
    await user.keyboard("{ArrowRight}")
    expect(onSettingsTabChange).toHaveBeenLastCalledWith("scope")
    expect(screen.getByText("XS files")).toBeVisible()
    expect(screen.getByText("M modules")).toBeVisible()
    expect(screen.queryByText("Review minimum")).not.toBeInTheDocument()
    expect(
      screen.queryByText("Deferred issue handling"),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole("tab", { name: "Deferred issues" }))
    expect(onSettingsTabChange).toHaveBeenLastCalledWith("deferred")
    expect(screen.getByText("Deferred issue handling")).toBeVisible()
    expect(screen.queryByText("XS files")).not.toBeInTheDocument()
    expect(screen.queryByText("Review minimum")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Profiles" }))
    expect(onOpenProfiles).toHaveBeenCalledTimes(1)
  })

  it("opens gate details only in a dialog and reports when it closes", async () => {
    const user = userEvent.setup()
    const onDecisionPointChange = vi.fn()

    function GateEditorHarness() {
      const [decisionPoint, setDecisionPoint] =
        useState<PRLifecycleDecisionPoint>()
      return (
        <PRLifecycleGateProfilesPage
          onBack={vi.fn()}
          initialProfileID="default"
          initialDecisionPoint={decisionPoint}
          onDecisionPointChange={(next) => {
            onDecisionPointChange(next)
            setDecisionPoint(next)
          }}
        />
      )
    }

    renderPage(<GateEditorHarness />)

    const trigger = await screen.findByRole("button", {
      name: "Accept review results",
    })
    expect(trigger).toHaveAttribute("aria-haspopup", "dialog")
    expect(trigger).not.toHaveAttribute("aria-expanded")
    expect(trigger).not.toHaveAttribute("aria-pressed")
    expect(trigger).not.toHaveAttribute("data-gate-selected")
    expect(
      document.querySelector("#pr-gate-workflow-editor"),
    ).not.toBeInTheDocument()

    await user.click(trigger)

    const dialog = screen.getByRole("dialog", {
      name: "Accept review results",
    })
    expect(dialog).toBeVisible()
    expect(trigger).toHaveAttribute("data-gate-selected", "true")
    const editor = dialog.querySelector("#pr-gate-workflow-editor")
    expect(editor).toHaveAttribute("data-decision-point", "pr.review.complete")
    expect(document.querySelectorAll("#pr-gate-workflow-editor")).toHaveLength(
      1,
    )
    expect(onDecisionPointChange).toHaveBeenLastCalledWith("pr.review.complete")

    await user.click(within(dialog).getByRole("button", { name: "Done" }))
    expect(
      screen.queryByRole("dialog", { name: "Accept review results" }),
    ).not.toBeInTheDocument()
    expect(onDecisionPointChange).toHaveBeenLastCalledWith(undefined)
    await waitFor(() => expect(trigger).toHaveFocus())
    expect(trigger).not.toHaveAttribute("data-gate-selected")
    expect(trigger).not.toHaveAttribute("aria-expanded")
    expect(trigger).not.toHaveAttribute("aria-pressed")
  })

  it("keeps profile flow and gate modal navigation route-controlled", async () => {
    const user = userEvent.setup()
    const onProfileChange = vi.fn()
    const onDecisionPointChange = vi.fn()
    const onFlowChange = vi.fn()
    const onDiscardOpenChange = vi.fn()

    function ControlledEditorHarness() {
      const [profileID, setProfileID] = useState<string | undefined>("default")
      const [decisionPoint, setDecisionPoint] =
        useState<PRLifecycleDecisionPoint>()
      const [flowID, setFlowID] = useState<"review" | "implementation">(
        "review",
      )
      const [discardOpen, setDiscardOpen] = useState(false)
      return (
        <PRLifecycleGateProfilesPage
          onBack={vi.fn()}
          page="profile"
          initialProfileID={profileID}
          initialDecisionPoint={decisionPoint}
          activeFlowID={flowID}
          discardOpen={discardOpen}
          onProfileChange={(next) => {
            onProfileChange(next)
            setProfileID(next)
          }}
          onDecisionPointChange={(next) => {
            onDecisionPointChange(next)
            setDecisionPoint(next)
          }}
          onFlowChange={(next) => {
            onFlowChange(next)
            setFlowID(next)
          }}
          onDiscardOpenChange={(open) => {
            onDiscardOpenChange(open)
            setDiscardOpen(open)
          }}
        />
      )
    }

    renderPage(<ControlledEditorHarness />)

    expect(
      await screen.findByRole("heading", { name: "Edit Default gate profile" }),
    ).toBeVisible()
    expect(screen.getByTestId("pr-gate-profiles")).toHaveAttribute(
      "data-profile-view",
      "profile",
    )
    await user.click(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    )
    expect(onFlowChange).toHaveBeenLastCalledWith("implementation")
    expect(
      screen.getByRole("tab", { name: /^Implementation workflow/ }),
    ).toHaveAttribute("aria-selected", "true")

    await user.click(screen.getByRole("tab", { name: /^Review workflow/ }))
    await user.click(
      screen.getByRole("button", { name: "Accept review results" }),
    )
    expect(onDecisionPointChange).toHaveBeenLastCalledWith("pr.review.complete")
    const dialog = screen.getByRole("dialog", {
      name: "Accept review results",
    })
    const workflowName = within(dialog).getByLabelText("Workflow name")
    await user.clear(workflowName)
    await user.type(workflowName, "Route-controlled review completion")
    await user.click(within(dialog).getByRole("button", { name: "Done" }))
    expect(onDecisionPointChange).toHaveBeenLastCalledWith(undefined)
  })

  it("returns dialog focus to the exact repeated gate trigger", async () => {
    const user = userEvent.setup()
    const repeatedGateProfiles = structuredClone(gateProfiles)
    const implementationFlow = repeatedGateProfiles.flow.flows.find(
      (flow) => flow.id === "implementation",
    )!
    const completionGate = implementationFlow.nodes.find(
      (node) => node.id === "implementation_complete",
    )!
    implementationFlow.nodes.push({
      ...completionGate,
      id: "implementation_complete_direct",
    })
    mockedGetGateProfiles.mockResolvedValueOnce(repeatedGateProfiles)

    renderPage(
      <PRLifecycleGateProfilesPage
        onBack={vi.fn()}
        initialProfileID="default"
      />,
    )

    await user.click(
      await screen.findByRole("tab", { name: /^Implementation workflow/ }),
    )
    const triggers = screen.getAllByRole("button", {
      name: "Accept implementation",
    })
    expect(triggers).toHaveLength(2)

    await user.click(triggers[1])
    const dialog = screen.getByRole("dialog", {
      name: "Accept implementation",
    })
    await user.click(within(dialog).getByRole("button", { name: "Done" }))

    await waitFor(() => expect(triggers[1]).toHaveFocus())
    expect(triggers[0]).not.toHaveFocus()
  })

  it("preserves edits made while a gate-profile save is in flight", async () => {
    const user = userEvent.setup()
    let resolveSave!: (value: PRLifecycleGateProfileSnapshot) => void
    mockedPutGateProfiles.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveSave = resolve
        }),
    )
    renderPage(<PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />)

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    await user.clear(minimum)
    await user.type(minimum, "3")
    await user.click(screen.getByRole("button", { name: "Save profiles" }))
    await waitFor(() => expect(mockedPutGateProfiles).toHaveBeenCalledTimes(1))

    await user.clear(minimum)
    await user.type(minimum, "4")
    await act(async () => {
      resolveSave({
        ...gateProfiles,
        config_revision: "sha256:after-first-save",
        nudge: {
          ...gateProfiles.nudge,
          review_minimum_additional: 3,
        },
      })
    })

    await waitFor(() => expect(minimum).toHaveValue(4))
    expect(screen.getByRole("button", { name: "Save profiles" })).toBeEnabled()
    await user.click(screen.getByRole("button", { name: "Save profiles" }))
    await waitFor(() => expect(mockedPutGateProfiles).toHaveBeenCalledTimes(2))
    expect(mockedPutGateProfiles).toHaveBeenLastCalledWith(
      expect.objectContaining({
        expected_config_revision: "sha256:after-first-save",
        nudge: expect.objectContaining({ review_minimum_additional: 4 }),
      }),
    )
  })

  it("opens an alternate-profile gate deep link in the correct dialog", async () => {
    const onDecisionPointChange = vi.fn()
    mockedGetGateProfiles.mockResolvedValueOnce({
      ...gateProfiles,
      gate_profiles: {
        ...gateProfiles.gate_profiles,
        alternate: {
          name: "Alternate",
          workflows: {
            "pr.review.complete": {
              id: "alternate_review_complete",
              name: "Alternate review completion",
              purpose: "authorization",
              decision_point: "pr.review.complete",
              stages: [{ id: "automatic", kind: "zero" }],
            },
          },
        },
      },
      default_gate_profile_id: "alternate",
    })
    renderPage(
      <PRLifecycleGateProfilesPage
        onBack={vi.fn()}
        initialProfileID="alternate"
        initialDecisionPoint="pr.review.complete"
        onDecisionPointChange={onDecisionPointChange}
      />,
    )

    const dialog = await screen.findByRole("dialog", {
      name: "Accept review results",
    })
    expect(screen.getByTestId("pr-gate-profiles")).toHaveAttribute(
      "data-profile-view",
      "profile",
    )
    expect(within(dialog).getByText(/Alternate ·/)).toBeVisible()
    expect(
      within(dialog).getByDisplayValue("Alternate review completion"),
    ).toBeVisible()
    expect(dialog.querySelector("#pr-gate-workflow-editor")).toHaveAttribute(
      "data-decision-point",
      "pr.review.complete",
    )
    expect(onDecisionPointChange).not.toHaveBeenCalled()
  })

  it("uses the URL-selected flow for shared gate modal metadata", async () => {
    const shared = structuredClone(gateProfiles)
    shared.flow.flows[1].nodes.push({
      id: "implementation_deferred_publish",
      kind: "gate",
      title: "Approve implementation follow-up",
      description: "Approve deferred work found during implementation.",
      decision_point: "pr.deferred.publish",
      ordinal: 12,
      editable: true,
    })
    mockedGetGateProfiles.mockResolvedValue(shared)

    renderPage(
      <PRLifecycleGateProfilesPage
        activeFlowID="implementation"
        initialDecisionPoint="pr.deferred.publish"
        initialProfileID="default"
        onBack={vi.fn()}
        onDecisionPointChange={vi.fn()}
        onFlowChange={vi.fn()}
        onProfileChange={vi.fn()}
        page="profile"
      />,
    )

    expect(
      await screen.findByRole("dialog", {
        name: "Approve implementation follow-up",
      }),
    ).toBeVisible()
  })

  it("returns deep-linked dialog focus to its declared gate", async () => {
    const user = userEvent.setup()

    function DeepLinkedGateHarness() {
      const [decisionPoint, setDecisionPoint] = useState<
        PRLifecycleDecisionPoint | undefined
      >("pr.review.complete")
      return (
        <PRLifecycleGateProfilesPage
          onBack={vi.fn()}
          initialProfileID="default"
          initialDecisionPoint={decisionPoint}
          onDecisionPointChange={(next) => {
            setDecisionPoint(next)
          }}
        />
      )
    }

    renderPage(<DeepLinkedGateHarness />)

    const dialog = await screen.findByRole("dialog", {
      name: "Accept review results",
    })
    const trigger = screen.getByRole("button", {
      name: "Accept review results",
      hidden: true,
    })
    await user.click(within(dialog).getByRole("button", { name: "Done" }))

    await waitFor(() => expect(trigger).toHaveFocus())
  })

  it("drops a well-formed gate deep link that is absent from the YAML graph", async () => {
    const onDecisionPointChange = vi.fn()
    const insertedDialogs: HTMLElement[] = []
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const addedNode of record.addedNodes) {
          if (!(addedNode instanceof HTMLElement)) continue
          if (addedNode.matches('[role="dialog"]')) {
            insertedDialogs.push(addedNode)
          }
          insertedDialogs.push(
            ...addedNode.querySelectorAll<HTMLElement>('[role="dialog"]'),
          )
        }
      }
    })
    observer.observe(document.body, { childList: true, subtree: true })
    function UnknownGateHarness() {
      const [decisionPoint, setDecisionPoint] = useState<
        PRLifecycleDecisionPoint | undefined
      >("pr.review.ghost")
      return (
        <PRLifecycleGateProfilesPage
          onBack={vi.fn()}
          initialProfileID="default"
          initialDecisionPoint={decisionPoint}
          onDecisionPointChange={(next) => {
            onDecisionPointChange(next)
            setDecisionPoint(next)
          }}
        />
      )
    }
    try {
      renderPage(<UnknownGateHarness />)

      await screen.findByRole("button", { name: "Accept review results" })
      await waitFor(() =>
        expect(onDecisionPointChange).toHaveBeenLastCalledWith(undefined),
      )
    } finally {
      observer.disconnect()
    }
    expect(insertedDialogs).toHaveLength(0)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(document.querySelector("#pr-gate-workflow-editor")).toBeNull()
  })

  it("resets a dirty draft through the controlled discard modal", async () => {
    const user = userEvent.setup()
    const onDiscardOpenChange = vi.fn()
    function DiscardHarness() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <PRLifecycleGateProfilesPage
            discardOpen={open}
            onBack={vi.fn()}
            onDiscardOpenChange={(next) => {
              onDiscardOpenChange(next)
              setOpen(next)
            }}
            page="settings"
          />
          <button type="button" onClick={() => setOpen(true)}>
            Open controlled discard
          </button>
        </>
      )
    }
    renderPage(<DiscardHarness />)

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    await user.clear(minimum)
    await user.type(minimum, "3")
    setMockNavigationBlocked()
    await user.click(
      screen.getByRole("button", { name: "Open controlled discard" }),
    )

    expect(
      screen.getByRole("alertdialog", {
        name: "Discard gate profile changes?",
      }),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Keep editing" }))
    expect(minimum).toHaveValue(3)

    setMockNavigationBlocked()
    await user.click(
      screen.getByRole("button", { name: "Open controlled discard" }),
    )
    await user.click(screen.getByRole("button", { name: "Discard changes" }))
    expect(onDiscardOpenChange).toHaveBeenLastCalledWith(false)
    expect((await screen.findAllByRole("spinbutton"))[0]).toHaveValue(2)
  })

  it("defers a dirty header exit until discard is confirmed", async () => {
    const user = userEvent.setup()
    const onBack = vi.fn()
    renderPage(<PRLifecycleGateProfilesPage onBack={onBack} page="settings" />)

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    await user.clear(minimum)
    await user.type(minimum, "3")
    await user.click(
      screen.getByRole("button", { name: "Back to pull request work" }),
    )

    expect(onBack).not.toHaveBeenCalled()
    const discard = screen.getByRole("alertdialog", {
      name: "Discard gate profile changes?",
    })
    await user.click(
      within(discard).getByRole("button", { name: "Keep editing" }),
    )
    expect(onBack).not.toHaveBeenCalled()

    await user.click(
      screen.getByRole("button", { name: "Back to pull request work" }),
    )
    const reopenedDiscard = screen.getByRole("alertdialog", {
      name: "Discard gate profile changes?",
    })
    await user.click(
      within(reopenedDiscard).getByRole("button", { name: "Discard changes" }),
    )
    expect(onBack).toHaveBeenCalledOnce()
  })

  it("caches an unsaved configuration draft across pages and clears it on discard", async () => {
    const user = userEvent.setup()
    const client = createTestQueryClient()
    const first = renderPage(
      <PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />,
      client,
    )

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    await user.clear(minimum)
    await user.type(minimum, "3")
    expect(minimum).toHaveValue(3)
    first.unmount()

    function CachedDiscardHarness() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <PRLifecycleGateProfilesPage
            discardOpen={open}
            onBack={vi.fn()}
            onDiscardOpenChange={setOpen}
            page="settings"
          />
          <button type="button" onClick={() => setOpen(true)}>
            Open cached discard
          </button>
        </>
      )
    }
    const second = renderPage(<CachedDiscardHarness />, client)
    expect((await screen.findAllByRole("spinbutton"))[0]).toHaveValue(3)

    setMockNavigationBlocked()
    await user.click(
      screen.getByRole("button", { name: "Open cached discard" }),
    )
    await user.click(screen.getByRole("button", { name: "Discard changes" }))
    second.unmount()

    renderPage(
      <PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />,
      client,
    )
    expect((await screen.findAllByRole("spinbutton"))[0]).toHaveValue(2)
  })

  it("refreshes a clean cached configuration from the server", async () => {
    const client = createTestQueryClient()
    const first = renderPage(
      <PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />,
      client,
    )
    expect((await screen.findAllByRole("spinbutton"))[0]).toHaveValue(2)
    first.unmount()

    mockedGetGateProfiles.mockResolvedValue({
      ...gateProfiles,
      config_revision: "sha256:new-config",
      nudge: {
        ...gateProfiles.nudge,
        review_minimum_additional: 4,
      },
    })
    renderPage(
      <PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />,
      client,
    )

    await waitFor(() =>
      expect(screen.getAllByRole("spinbutton")[0]).toHaveValue(4),
    )
  })

  it("keeps a saved restart-required gate-profile effect visible", async () => {
    const user = userEvent.setup()
    mockedPutGateProfiles.mockResolvedValue({
      ...gateProfiles,
      config_revision: "sha256:restart-required",
      effects: { gateway_effect: "restart_required" },
    })
    renderPage(<PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />)

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    await user.clear(minimum)
    await user.type(minimum, "3")
    await user.click(screen.getByRole("button", { name: "Save profiles" }))

    expect(await screen.findByText("Gateway restart required")).toBeVisible()
    expect(
      screen.getByText(
        "Gate profiles were saved, but the running gateway must be restarted before future PR decisions use them.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Save profiles" })).toBeDisabled()
  })

  it("clears a restart-required banner after the launcher reports profiles applied", async () => {
    const user = userEvent.setup()
    mockedGetGateProfiles
      .mockResolvedValueOnce({
        ...gateProfiles,
        effects: { gateway_effect: "restart_required" },
      })
      .mockResolvedValue({
        ...gateProfiles,
        effects: { gateway_effect: "applied" },
      })
    renderPage(<PRLifecycleGateProfilesPage onBack={vi.fn()} page="settings" />)

    const minimum = (await screen.findAllByRole("spinbutton"))[0]
    expect(await screen.findByText("Gateway restart required")).toBeVisible()
    await user.clear(minimum)
    await user.type(minimum, "3")
    await user.click(
      screen.getByRole("button", { name: "Refresh pull request work" }),
    )

    await waitFor(() =>
      expect(
        screen.queryByText("Gateway restart required"),
      ).not.toBeInTheDocument(),
    )
    expect(minimum).toHaveValue(3)
    expect(screen.getByRole("button", { name: "Save profiles" })).toBeEnabled()
  })

  it("keeps mixed gate stage editors shrinkable on mobile", async () => {
    const user = userEvent.setup()
    mockedGetGateProfiles.mockResolvedValue({
      ...gateProfiles,
      gate_profiles: {
        default: {
          ...gateProfiles.gate_profiles.default,
          workflows: {
            "pr.review.complete": {
              ...gateProfiles.gate_profiles.default.workflows[
                "pr.review.complete"
              ],
              stages: [
                {
                  id: "isolated_review_completion",
                  kind: "ai_isolated_context",
                  title: "Independently verify review completion",
                  agent_id: "controller",
                  criteria:
                    "Verify all bounded review evidence without widening the pull request scope.",
                },
              ],
            },
          },
        },
      },
    })
    renderPage(
      <PRLifecycleGateProfilesPage
        onBack={vi.fn()}
        initialProfileID="default"
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Accept review results" }),
    )
    const dialog = screen.getByRole("dialog", {
      name: "Accept review results",
    })
    const editor = within(dialog).getByTestId("pr-gate-stage-editor")
    expect(editor).toHaveClass("min-w-0", "max-w-full")
    expect(within(dialog).getByLabelText("Stable stage ID")).toHaveClass(
      "min-w-0",
    )
    expect(within(dialog).getByLabelText("Stage type")).toHaveClass(
      "min-w-0",
      "max-w-full",
    )
    expect(within(dialog).getByLabelText("AI criteria")).toHaveClass(
      "min-w-0",
      "max-w-full",
    )
    expect(
      within(dialog).getByLabelText("AI criteria").parentElement,
    ).toHaveClass("min-w-0", "max-w-full")
    expect(
      within(dialog).queryByLabelText("Run condition (optional)"),
    ).not.toBeInTheDocument()
  })
})
