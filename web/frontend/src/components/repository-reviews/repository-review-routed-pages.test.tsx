import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactElement, useState } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  RepositoryReviewAutomation,
  RepositoryReviewFinding,
  RepositoryReviewIssueDraft,
  RepositoryReviewSummary,
} from "@/api/repository-reviews"
import {
  RepositoryReviewAPIError,
  deleteRepositoryReviewAutomationIssue,
  findRepositoryReviewIssueCandidates,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomation,
  getRepositoryReviewAutomationFinding,
  getRepositoryReviewAutomationIssue,
  getRepositoryReviewAutomationReport,
  getRepositoryReviewCommitOptions,
  linkRepositoryReviewIssue,
  publishRepositoryReviewAutomationIssue,
  regenerateRepositoryReviewAutomationIssue,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  startRepositoryReviewAutomation,
  unlinkRepositoryReviewIssue,
} from "@/api/repository-reviews"
import { type ThreadSummary, createThread } from "@/api/threads"
import { RepositoryReviewDetailPage } from "@/components/repository-reviews/repository-review-detail-page"
import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { RepositoryReviewIssuePage } from "@/components/repository-reviews/repository-review-issue-page"
import { RepositoryReviewLinkIssuePage } from "@/components/repository-reviews/repository-review-link-issue-page"
import { RepositoryReviewReportPage } from "@/components/repository-reviews/repository-review-report-page"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"

import type { RepositoryReviewRouteSearch } from "./repository-review-route-state"

vi.mock("@/api/repository-reviews", () => ({
  RepositoryReviewAPIError: class RepositoryReviewAPIError extends Error {
    status: number

    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
  generateRepositoryReviewIssues: vi.fn(),
  getRepositoryReviewAutomation: vi.fn(),
  getRepositoryReviewAutomationFinding: vi.fn(),
  getRepositoryReviewAutomationIssue: vi.fn(),
  getRepositoryReviewAutomationReport: vi.fn(),
  getRepositoryReviewCommitOptions: vi.fn(),
  startRepositoryReviewAutomation: vi.fn(),
  pauseRepositoryReviewAutomation: vi.fn(),
  resumeRepositoryReviewAutomation: vi.fn(),
  restartRepositoryReviewAutomation: vi.fn(),
  updateRepositoryReviewAutomationFinding: vi.fn(),
  updateRepositoryReviewAutomationIssue: vi.fn(),
  deleteRepositoryReviewAutomationIssue: vi.fn(),
  regenerateRepositoryReviewAutomationIssue: vi.fn(),
  publishRepositoryReviewAutomationIssue: vi.fn(),
  findRepositoryReviewIssueCandidates: vi.fn(),
  linkRepositoryReviewIssue: vi.fn(),
  unlinkRepositoryReviewIssue: vi.fn(),
}))

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    titleExtra,
    children,
  }: {
    title: string
    titleExtra?: React.ReactNode
    children?: React.ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {titleExtra}
      {children}
    </header>
  ),
}))

vi.mock("@/api/threads", () => ({ createThread: vi.fn(), dropThread: vi.fn() }))
vi.mock("@/features/chat/controller", () => ({
  switchChatSessionAndSend: vi.fn(),
}))

const automation: RepositoryReviewAutomation = {
  id: "auto_1",
  version: 4,
  profile_id: "profile_1",
  profile_version: 2,
  branch: "main",
  name: "Core review",
  repository: "owner/repo",
  ref: "main",
  target: "all",
  account_ref: "acct",
  effective_account_ref: "acct",
  review_focus: "Correctness",
  scope_policy: {
    code_types: ["code"],
    include_folders: [],
    exclude_folders: [],
    free_text: "",
  },
  reviewer_models: ["reviewer"],
  issue_writer_model: "writer",
  compare_models: false,
  force: false,
  max_files_per_run: 24,
  max_content_bytes: 524288,
  max_parallel_children: 4,
  auto_continue: true,
  model_prices: {},
  budget: { guard_expression: "" },
  status: "running",
  run_ids: ["run_1"],
  usage: {
    prompt_tokens: 100,
    completion_tokens: 20,
    total_tokens: 120,
    cached_tokens: 0,
  },
  estimated_cost_usd: 0.1,
  progress: {
    stage: "reviewing",
    completed_batches: 1,
    total_batches: 2,
    reviewed_files: 4,
    remaining_files: 4,
    unsupported_files: 0,
    findings: 1,
  },
  model_stats: [],
  account_limits: [],
  created_at: "2026-08-26T00:00:00Z",
  updated_at: "2026-08-26T00:00:00Z",
}

const repositorySummary: RepositoryReviewSummary = {
  schema_version: 1,
  id: "repository_1",
  repository: automation.repository,
  version: 3,
  review_version: 2,
  last_commit_sha: "c".repeat(40),
  finding_count: 1,
  open_finding_count: 1,
  issue_draft_count: 1,
  updated_at: "2026-08-26T00:00:00Z",
}

const discussionThread: ThreadSummary = {
  id: "thread_1",
  ui_session_id: "review-session-1",
  title: "Lost update",
  preview: "",
  type: "reviewing",
  message_count: 0,
  created: "2026-08-26T00:00:00Z",
  updated: "2026-08-26T00:00:00Z",
}

const finding: RepositoryReviewFinding = {
  id: "finding_1",
  fingerprint: "fingerprint",
  repository: "owner/repo",
  commit_sha: "a".repeat(40),
  file: { path: "pkg/store.go", blob_sha: "b".repeat(40), size_bytes: 512 },
  line: 42,
  severity: "high",
  title: "Lost update",
  symbol: "Save",
  message: "Concurrent writes overwrite state.",
  evidence: "The write has no version fence.",
  impact: "A stored finding can disappear.",
  validation: {
    status: "confirmed",
    summary: "Traced both writers.",
    checks: ["race trace"],
  },
  context_ids: ["context_1"],
  models: ["reviewer"],
  observation_count: 1,
  observations: [],
  status: "open",
  version: 2,
  created_at: "2026-08-26T00:00:00Z",
  updated_at: "2026-08-26T00:00:00Z",
}

const issue: RepositoryReviewIssueDraft = {
  id: "draft_1",
  repository: "owner/repo",
  finding_ids: [finding.id],
  origin: "ai_generated",
  generation_id: "rrig_1",
  instructions_mode: "default",
  generator_model: "writer",
  generator_account: "acct",
  canonical: true,
  title: "Lost update can discard findings",
  body: "## Evidence\n\n- Version fence is absent.\n\n## Impact\n\nStored findings can disappear.",
  labels: ["bug"],
  state: "editing",
  version: 3,
  created_at: "2026-08-26T00:00:00Z",
  updated_at: "2026-08-26T00:00:00Z",
}

const linkedIssue: RepositoryReviewIssueDraft = {
  ...issue,
  id: "draft_linked",
  origin: "linked",
  generation_id: undefined,
  resolved_instructions: undefined,
  instructions_mode: undefined,
  generator_model: undefined,
  generator_account: undefined,
  title: "Existing lost-update report",
  body: "This record tracks an existing GitHub issue.",
  state: "posted",
  external_id: "42",
  external_url: "https://github.com/owner/repo/issues/42",
  version: 7,
  unlinkable: true,
  deletable: false,
  regeneratable: false,
  publishable: false,
}

describe("routed repository review pages", () => {
  beforeEach(() => {
    vi.resetAllMocks()
    resetCollectionRouteStateMemoryForTests()
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(automation)
    vi.mocked(getRepositoryReviewAutomationReport).mockResolvedValue({
      automation,
      repository: repositorySummary,
      findings: [finding],
      scope: "current",
      offset: 0,
      total: 1,
      capabilities: { can_generate: true, github: true },
    })
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      contexts: [
        {
          id: "context_1",
          repository: "owner/repo",
          commit_sha: finding.commit_sha,
          inventory_hash: "inventory",
          profile_hash: "profile",
          run_id: "run_1",
          model: "reviewer",
          files: [finding.file],
          created_at: "2026-08-26T00:00:00Z",
        },
      ],
      capabilities: { github: true, can_generate: true, can_link_issue: true },
    })
    vi.mocked(getRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      repository: repositorySummary,
      issue,
      finding,
      capabilities: {
        github: true,
        can_edit: true,
        can_delete: true,
        can_regenerate: true,
        can_publish: true,
      },
    })
    vi.mocked(generateRepositoryReviewIssues).mockResolvedValue({
      generation_id: "rrig_1",
      issues: [issue],
      results: [{ id: finding.id, draft_id: issue.id, success: true }],
    })
    vi.mocked(createThread).mockResolvedValue(discussionThread)
    vi.mocked(switchChatSessionAndSend).mockResolvedValue(true)
  })

  it.each([
    ["automation detail", "detail"],
    ["finding detail", "finding"],
    ["issue detail", "issue"],
  ] as const)(
    "renders the %s not-found state for a 404",
    async (_label, kind) => {
      rejectRouteLoader(
        kind,
        new RepositoryReviewAPIError(404, "The requested record is missing."),
      )
      renderPage(repositoryReviewRouteElement(kind))

      expect(
        await screen.findByRole("heading", { name: "Item not found" }),
      ).toBeVisible()
      expect(
        screen.getByText(
          "The requested item does not exist or is no longer available.",
        ),
      ).toBeVisible()
      expect(screen.queryByRole("button", { name: "Retry" })).toBeNull()
    },
  )

  it.each([
    ["automation detail", "detail"],
    ["finding detail", "finding"],
    ["issue detail", "issue"],
  ] as const)(
    "renders the %s error state for a server failure",
    async (label, kind) => {
      const message = `${label} is temporarily unavailable.`
      rejectRouteLoader(kind, new RepositoryReviewAPIError(503, message))
      renderPage(repositoryReviewRouteElement(kind))

      expect(
        await screen.findByRole("heading", {
          name: "Details could not be loaded",
        }),
      ).toBeVisible()
      expect(screen.getByText(message)).toBeVisible()
      expect(screen.getByRole("button", { name: "Retry" })).toBeVisible()
      expect(
        screen.queryByRole("heading", { name: "Item not found" }),
      ).toBeNull()
    },
  )

  it("keeps report and issue previews available while a review is active", async () => {
    const onReport = vi.fn()
    const onIssues = vi.fn()
    renderPage(
      <RepositoryReviewDetailPage
        id={automation.id}
        onBack={vi.fn()}
        onReport={onReport}
        onIssues={onIssues}
      />,
    )

    await userEvent.click(await screen.findByRole("button", { name: /Report/ }))
    await userEvent.click(
      screen.getByRole("button", { name: /Issue previews/ }),
    )
    expect(onReport).toHaveBeenCalled()
    expect(onIssues).toHaveBeenCalled()
    expect(screen.getByText("writer")).toBeVisible()
  })

  it("starts an idle review from its detail page", async () => {
    const idleReview: RepositoryReviewAutomation = {
      ...automation,
      version: 8,
      status: "idle",
      auto_continue: false,
      run_ids: [],
    }
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(idleReview)
    vi.mocked(startRepositoryReviewAutomation).mockResolvedValue({
      ...idleReview,
      version: 9,
      status: "running",
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewDetailPage
        id={idleReview.id}
        onBack={vi.fn()}
        onReport={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Start" }))
    await waitFor(() =>
      expect(startRepositoryReviewAutomation).toHaveBeenCalledWith(
        idleReview.id,
        { expected_version: idleReview.version },
      ),
    )
  })

  it("continues a paused review only after an explicit commit choice", async () => {
    const pausedReview: RepositoryReviewAutomation = {
      ...automation,
      version: 10,
      status: "paused",
    }
    const rememberedSHA = "d".repeat(40)
    const latestSHA = "e".repeat(40)
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(pausedReview)
    vi.mocked(getRepositoryReviewCommitOptions).mockResolvedValue({
      expected_version: 11,
      remembered: {
        sha: rememberedSHA,
        short_sha: rememberedSHA.slice(0, 8),
      },
      latest: { sha: latestSHA, short_sha: latestSHA.slice(0, 8) },
      newer_commit_available: true,
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...pausedReview,
      version: 12,
      status: "running",
      resolved_commit_sha: latestSHA,
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewDetailPage
        id={pausedReview.id}
        onBack={vi.fn()}
        onReport={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Continue" }))
    const dialog = await screen.findByRole("dialog", {
      name: "Choose commit to continue",
    })
    expect(resumeRepositoryReviewAutomation).not.toHaveBeenCalled()
    await user.click(
      screen.getByLabelText("Continue on latest commit", { exact: true }),
    )
    await user.click(
      within(dialog).getByRole("button", { name: "Continue review" }),
    )

    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith(
        pausedReview.id,
        { expected_version: 11, commit_sha: latestSHA },
      ),
    )
  })

  it("continues a failed campaign without removing the run-again option", async () => {
    const failedReview: RepositoryReviewAutomation = {
      ...automation,
      version: 12,
      status: "failed",
      pause_reason: "run_failed",
      pause_detail: "The provider was temporarily unavailable.",
    }
    const rememberedSHA = "f".repeat(40)
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(failedReview)
    vi.mocked(getRepositoryReviewCommitOptions).mockResolvedValue({
      expected_version: 13,
      remembered: {
        sha: rememberedSHA,
        short_sha: rememberedSHA.slice(0, 8),
      },
      latest: {
        sha: rememberedSHA,
        short_sha: rememberedSHA.slice(0, 8),
      },
      newer_commit_available: false,
    })
    vi.mocked(resumeRepositoryReviewAutomation).mockResolvedValue({
      ...failedReview,
      version: 14,
      status: "running",
      pause_reason: undefined,
      pause_detail: undefined,
      resolved_commit_sha: rememberedSHA,
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewDetailPage
        id={failedReview.id}
        onBack={vi.fn()}
        onReport={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    const continueButton = await screen.findByRole("button", {
      name: "Continue",
    })
    expect(screen.getByRole("button", { name: "Run again" })).toBeVisible()
    await user.click(continueButton)

    await waitFor(() =>
      expect(getRepositoryReviewCommitOptions).toHaveBeenCalledWith(
        failedReview.id,
      ),
    )
    await waitFor(() =>
      expect(resumeRepositoryReviewAutomation).toHaveBeenCalledWith(
        failedReview.id,
        { expected_version: 13 },
      ),
    )
    expect(restartRepositoryReviewAutomation).not.toHaveBeenCalled()
  })

  it("runs a completed review again from its detail page", async () => {
    const completedReview: RepositoryReviewAutomation = {
      ...automation,
      version: 12,
      status: "completed",
      completed_at: "2026-08-26T01:00:00Z",
    }
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(completedReview)
    vi.mocked(restartRepositoryReviewAutomation).mockResolvedValue({
      ...completedReview,
      version: 13,
      status: "running",
      completed_at: undefined,
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewDetailPage
        id={completedReview.id}
        onBack={vi.fn()}
        onReport={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Run again" }))
    await waitFor(() =>
      expect(restartRepositoryReviewAutomation).toHaveBeenCalledWith(
        completedReview.id,
        { expected_version: completedReview.version },
      ),
    )
  })

  it("selects a finding and generates one durable preview batch", async () => {
    let resolveGeneration!: (
      value: Awaited<ReturnType<typeof generateRepositoryReviewIssues>>,
    ) => void
    vi.mocked(generateRepositoryReviewIssues).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveGeneration = resolve
        }),
    )
    const user = userEvent.setup()
    const onGenerated = vi.fn()
    renderPage(
      <RepositoryReviewReportPage
        automationID={automation.id}
        search={{
          q: "ORDER BY repository ASC",
          scope: "current",
          offset: 0,
        }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={onGenerated}
        onOpenThread={vi.fn()}
      />,
    )

    const row = (await screen.findByText("Lost update")).closest(
      "[data-item-id]",
    )!
    await user.click(row)
    await user.click(
      screen.getByRole("button", { name: "Generate issue previews" }),
    )
    await user.click(screen.getByRole("button", { name: "Generate previews" }))

    await waitFor(() =>
      expect(generateRepositoryReviewIssues).toHaveBeenCalledWith(
        automation.id,
        expect.objectContaining({
          finding_ids: [finding.id],
          instructions_mode: "default",
        }),
      ),
    )
    expect(screen.getByRole("button", { name: "Generating…" })).toBeDisabled()
    expect(onGenerated).not.toHaveBeenCalled()

    resolveGeneration({
      generation_id: "rrig_1",
      issues: [issue],
      results: [{ id: finding.id, draft_id: issue.id, success: true }],
    })
    await waitFor(() =>
      expect(onGenerated).toHaveBeenCalledWith(expect.stringMatching(/^rig_/u)),
    )
  })

  it("starts a grounded reviewing thread without generating or associating an issue", async () => {
    const user = userEvent.setup()
    const onOpenThread = vi.fn()
    renderPage(
      <RepositoryReviewReportPage
        automationID={automation.id}
        search={{
          q: "ORDER BY repository ASC",
          scope: "current",
          offset: 0,
        }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={onOpenThread}
      />,
    )

    const row = (await screen.findByText(finding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(row)
    await user.click(screen.getByRole("button", { name: "Discuss with AI" }))

    await waitFor(() =>
      expect(createThread).toHaveBeenCalledWith({
        type: "reviewing",
        title: finding.title,
        context: {
          repository: automation.repository,
          repository_review: automation.id,
          finding_ids: finding.id,
          context_ids: finding.context_ids.join(","),
          commit: repositorySummary.last_commit_sha,
        },
        source_query: `repository review ${automation.repository}`,
      }),
    )
    expect(switchChatSessionAndSend).toHaveBeenCalledTimes(1)
    const [sessionID, message] = vi.mocked(switchChatSessionAndSend).mock
      .calls[0]!
    expect(sessionID).toBe(discussionThread.ui_session_id)
    expect(message.content).toContain(
      "Discuss these validated repository-review findings with me.",
    )
    expect(message.content).toContain(`Finding ${finding.id}: ${finding.title}`)
    expect(message.content).toContain(
      `Finding commit SHA: ${finding.commit_sha}`,
    )
    expect(message.content).toContain(`Blob SHA: ${finding.file.blob_sha}`)
    expect(message.content).toContain(`Evidence: ${finding.evidence}`)
    expect(message.content).toContain(`Impact: ${finding.impact}`)
    expect(message.content).toContain(`- Context ${finding.context_ids[0]}`)
    expect(onOpenThread).toHaveBeenCalledWith(discussionThread.ui_session_id)
    expect(generateRepositoryReviewIssues).not.toHaveBeenCalled()
    expect(findRepositoryReviewIssueCandidates).not.toHaveBeenCalled()
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()
    expect(publishRepositoryReviewAutomationIssue).not.toHaveBeenCalled()
  })

  it("switches report scope in both directions and resets its offset", async () => {
    vi.mocked(getRepositoryReviewAutomationReport).mockImplementation(
      async (_automationID, input) => ({
        automation,
        repository: repositorySummary,
        findings: [finding],
        scope: input?.scope === "all" ? "all" : "current",
        offset: input?.offset ?? 0,
        total: 101,
        capabilities: { can_generate: true, github: true },
      }),
    )
    const user = userEvent.setup()
    renderPage(<ReportScopeHarness />)

    const scope = await screen.findByRole("combobox", {
      name: "Finding scope",
    })
    expect(scope).toHaveValue("current")
    await user.selectOptions(scope, "all")
    await waitFor(() =>
      expect(getRepositoryReviewAutomationReport).toHaveBeenLastCalledWith(
        automation.id,
        { scope: "all", offset: 0, limit: 50 },
        expect.any(AbortSignal),
      ),
    )
    expect(screen.getByTestId("report-route-search")).toHaveTextContent(
      '"scope":"all","offset":0',
    )

    await user.selectOptions(
      screen.getByRole("combobox", { name: "Finding scope" }),
      "current",
    )
    await waitFor(() =>
      expect(getRepositoryReviewAutomationReport).toHaveBeenLastCalledWith(
        automation.id,
        { scope: "current", offset: 0, limit: 50 },
        expect.any(AbortSignal),
      ),
    )
    expect(screen.getByTestId("report-route-search")).toHaveTextContent(
      '"scope":"current","offset":0',
    )
  })

  it("polls an active report and stops polling after it becomes terminal", async () => {
    let requestCount = 0
    vi.mocked(getRepositoryReviewAutomationReport).mockImplementation(
      async () => {
        requestCount += 1
        return {
          automation: {
            ...automation,
            status: requestCount === 1 ? "running" : "completed",
          },
          repository: repositorySummary,
          findings: [finding],
          scope: "current",
          offset: 0,
          total: 1,
          capabilities: { can_generate: true, github: true },
        }
      },
    )
    const view = renderPage(
      <RepositoryReviewReportPage
        automationID={automation.id}
        search={{
          q: "ORDER BY repository ASC",
          scope: "current",
          offset: 0,
        }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    await waitFor(() =>
      expect(getRepositoryReviewAutomationReport).toHaveBeenCalledTimes(1),
    )
    await waitFor(
      () =>
        expect(getRepositoryReviewAutomationReport).toHaveBeenCalledTimes(2),
      { timeout: 3_000 },
    )
    expect(await screen.findByText("completed", { exact: true })).toBeVisible()

    await new Promise((resolve) => setTimeout(resolve, 2_200))
    expect(getRepositoryReviewAutomationReport).toHaveBeenCalledTimes(2)
    view.unmount()
  }, 8_000)

  it("retains explicit selection across report pages", async () => {
    const secondFinding: RepositoryReviewFinding = {
      ...finding,
      id: "finding_2",
      title: "Second durable finding",
      fingerprint: "fingerprint-2",
      issue_draft_id: undefined,
    }
    vi.mocked(getRepositoryReviewAutomationReport).mockImplementation(
      async (_automationID, input) => {
        const offset = input?.offset ?? 0
        return {
          automation,
          findings: offset === 50 ? [secondFinding] : [finding],
          scope: "current",
          offset,
          total: 51,
          ...(offset === 50 ? {} : { next_offset: 50 }),
          capabilities: { can_generate: true, github: true },
        }
      },
    )
    const user = userEvent.setup()
    renderPage(<ReportPagingHarness />)

    const first = (await screen.findByText(finding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(first)
    await user.click(screen.getByRole("button", { name: "Next findings" }))
    const second = (await screen.findByText(secondFinding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(second)
    expect(screen.getByText("2 selected", { exact: true })).toBeVisible()

    await user.click(
      screen.getByRole("button", { name: "Generate issue previews" }),
    )
    await user.click(screen.getByRole("button", { name: "Generate previews" }))
    await waitFor(() =>
      expect(generateRepositoryReviewIssues).toHaveBeenLastCalledWith(
        automation.id,
        expect.objectContaining({
          finding_ids: [finding.id, secondFinding.id],
        }),
      ),
    )
  })

  it("renders complete finding provenance without an untracked posted transition", async () => {
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={finding.id}
        onBack={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(await screen.findByText(finding.commit_sha)).toBeVisible()
    expect(screen.getAllByText(finding.file.blob_sha).length).toBeGreaterThan(0)
    expect(screen.getByText("Traced both writers.")).toBeVisible()
    expect(
      screen.queryByRole("button", { name: /Mark posted/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Generate preview" }),
    ).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Link existing issue" }),
    ).toBeVisible()
  })

  it("opens the canonical issue record from an associated finding", async () => {
    const associatedFinding: RepositoryReviewFinding = {
      ...finding,
      issue_draft_id: issue.id,
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding: associatedFinding,
      contexts: [],
      issue,
      capabilities: {
        github: true,
        can_generate: false,
        can_link_issue: false,
      },
    })
    const onOpenIssue = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={associatedFinding.id}
        onBack={vi.fn()}
        onOpenIssue={onOpenIssue}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Open issue record" }),
    )
    expect(onOpenIssue).toHaveBeenCalledWith(issue.id)
  })

  it("hides all link and search actions for a dismissed finding", async () => {
    const dismissedFinding: RepositoryReviewFinding = {
      ...finding,
      status: "dismissed",
      issue_draft_id: undefined,
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding: dismissedFinding,
      contexts: [],
      capabilities: {
        github: true,
        can_link_issue: false,
        can_search_issues: false,
      },
    })
    renderPage(
      <RepositoryReviewLinkIssuePage
        automationID={automation.id}
        findingID={dismissedFinding.id}
        onBack={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    expect(
      await screen.findByRole("heading", {
        name: "Existing-issue linking is unavailable",
      }),
    ).toBeVisible()
    expect(
      screen.queryByRole("textbox", { name: "GitHub issue URL" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", {
        name: "Ask AI to find existing issues",
      }),
    ).not.toBeInTheDocument()
    expect(findRepositoryReviewIssueCandidates).not.toHaveBeenCalled()
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()
  })

  it("replaces a manual link only after confirmation and never offers AI search", async () => {
    const linkedFinding: RepositoryReviewFinding = {
      ...finding,
      status: "posted",
      issue_draft_id: linkedIssue.id,
      version: 7,
    }
    const linkedDetail = {
      automation,
      repository: repositorySummary,
      finding: linkedFinding,
      contexts: [],
      issue: linkedIssue,
      capabilities: {
        github: true,
        can_link_issue: false,
        can_search_issues: false,
        can_replace_issue: true,
        can_unlink_issue: true,
      },
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue(
      linkedDetail,
    )
    const replacementURL = "https://github.com/owner/repo/issues/84"
    const replacedIssue = {
      ...linkedIssue,
      external_id: "84",
      external_url: replacementURL,
      version: 8,
    }
    vi.mocked(linkRepositoryReviewIssue).mockResolvedValue({
      ...linkedDetail,
      finding: { ...linkedFinding, version: 8 },
      issue: replacedIssue,
    })
    const onLinked = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewLinkIssuePage
        automationID={automation.id}
        findingID={linkedFinding.id}
        onBack={vi.fn()}
        onLinked={onLinked}
      />,
    )

    expect(
      await screen.findByRole("heading", { name: "Replace manual link" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", {
        name: "Ask AI to find existing issues",
      }),
    ).not.toBeInTheDocument()
    await user.type(
      screen.getByRole("textbox", { name: "GitHub issue URL" }),
      replacementURL,
    )
    await user.click(screen.getByRole("button", { name: "Review link" }))
    let dialog = screen.getByRole("alertdialog", {
      name: "Replace the manual issue link?",
    })
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Review link" }))
    dialog = screen.getByRole("alertdialog", {
      name: "Replace the manual issue link?",
    })
    await user.click(
      within(dialog).getByRole("button", { name: "Replace link" }),
    )
    await waitFor(() =>
      expect(linkRepositoryReviewIssue).toHaveBeenCalledWith(
        automation.id,
        linkedFinding.id,
        {
          issue_url: replacementURL,
          expected_version: linkedFinding.version,
          confirmed: true,
          replace: true,
        },
      ),
    )
    expect(onLinked).toHaveBeenCalledWith(linkedIssue.id)
    expect(findRepositoryReviewIssueCandidates).not.toHaveBeenCalled()
  })

  it("unlinks a manual issue only after explicit confirmation", async () => {
    const linkedFinding: RepositoryReviewFinding = {
      ...finding,
      status: "posted",
      issue_draft_id: linkedIssue.id,
      version: 7,
    }
    const linkedDetail = {
      automation,
      repository: repositorySummary,
      finding: linkedFinding,
      contexts: [],
      issue: linkedIssue,
      capabilities: {
        github: true,
        can_link_issue: false,
        can_search_issues: false,
        can_replace_issue: true,
        can_unlink_issue: true,
      },
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue(
      linkedDetail,
    )
    vi.mocked(unlinkRepositoryReviewIssue).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding: {
        ...linkedFinding,
        status: "open",
        issue_draft_id: undefined,
        version: 8,
      },
      contexts: [],
      capabilities: {
        github: true,
        can_link_issue: true,
        can_search_issues: true,
      },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewLinkIssuePage
        automationID={automation.id}
        findingID={linkedFinding.id}
        onBack={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Unlink manual issue" }),
    )
    let dialog = screen.getByRole("alertdialog", {
      name: "Unlink this manually linked issue?",
    })
    expect(unlinkRepositoryReviewIssue).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }))
    expect(unlinkRepositoryReviewIssue).not.toHaveBeenCalled()

    await user.click(
      screen.getByRole("button", { name: "Unlink manual issue" }),
    )
    dialog = screen.getByRole("alertdialog", {
      name: "Unlink this manually linked issue?",
    })
    await user.click(
      within(dialog).getByRole("button", { name: "Unlink issue" }),
    )
    await waitFor(() =>
      expect(unlinkRepositoryReviewIssue).toHaveBeenCalledWith(
        automation.id,
        linkedFinding.id,
        { expected_version: linkedFinding.version, confirmed: true },
      ),
    )
  })

  it("renders sanitized GitHub-flavored Markdown and explicit publication controls", async () => {
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={issue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onOpenFinding={vi.fn()}
        onManageLink={vi.fn()}
      />,
    )

    expect(
      await screen.findByRole("heading", { name: "Evidence" }),
    ).toBeVisible()
    expect(screen.getByText("Version fence is absent.")).toBeVisible()
    expect(screen.getByRole("button", { name: "Edit" })).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Regenerate with AI" }),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Publish issue" })).toBeVisible()
  })

  it("opens the associated finding from an issue record", async () => {
    const onOpenFinding = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={issue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onOpenFinding={onOpenFinding}
        onManageLink={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", {
        name: `Finding ${finding.id}`,
      }),
    )
    expect(onOpenFinding).toHaveBeenCalledWith(finding.id)
  })

  it("deletes an unpublished preview only after confirmation", async () => {
    vi.mocked(deleteRepositoryReviewAutomationIssue).mockResolvedValue({
      results: [
        {
          draft_id: issue.id,
          outcome: "deleted",
          success: true,
        },
      ],
    })
    const user = userEvent.setup()
    const onDeleted = vi.fn()
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={issue.id}
        onBack={vi.fn()}
        onDeleted={onDeleted}
        onOpenFinding={vi.fn()}
        onManageLink={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Delete preview" }),
    )
    const dialog = screen.getByRole("alertdialog", {
      name: "Delete this unpublished preview?",
    })
    expect(deleteRepositoryReviewAutomationIssue).not.toHaveBeenCalled()
    await user.click(
      within(dialog).getByRole("button", { name: "Delete preview" }),
    )

    await waitFor(() =>
      expect(deleteRepositoryReviewAutomationIssue).toHaveBeenCalledWith(
        automation.id,
        issue.id,
        { expected_version: issue.version, confirmed: true },
      ),
    )
    expect(onDeleted).toHaveBeenCalledTimes(1)
  })

  it("keeps the last good preview visible after a failed regeneration", async () => {
    const preservedIssue: RepositoryReviewIssueDraft = {
      ...issue,
      version: 4,
      generation_error: "The issue writer returned invalid structured output.",
      attempt_generation_id: "rrig_retry_failed",
      attempt_instructions_mode: "default",
      attempt_generator_model: "writer",
      attempt_generator_account: "acct",
      attempt_resolved_instructions: "Use the grounded default issue format.",
    }
    const preservedDetail = {
      automation,
      repository: repositorySummary,
      issue: preservedIssue,
      finding,
      capabilities: {
        github: true,
        can_edit: true,
        can_delete: true,
        can_regenerate: true,
        can_publish: true,
      },
    }
    vi.mocked(getRepositoryReviewAutomationIssue).mockResolvedValue(
      preservedDetail,
    )
    vi.mocked(regenerateRepositoryReviewAutomationIssue).mockResolvedValue(
      preservedDetail,
    )
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={preservedIssue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onOpenFinding={vi.fn()}
        onManageLink={vi.fn()}
      />,
    )

    expect(
      await screen.findByText(preservedIssue.generation_error!),
    ).toBeVisible()
    expect(
      screen.getByText("The last good preview remains available below."),
    ).toBeVisible()
    expect(screen.getByRole("heading", { name: "Evidence" })).toBeVisible()
    expect(screen.getByText("Version fence is absent.")).toBeVisible()
    expect(screen.getByText("Last failed regeneration attempt")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Regenerate with AI" }))
    await waitFor(() =>
      expect(regenerateRepositoryReviewAutomationIssue).toHaveBeenCalledWith(
        automation.id,
        preservedIssue.id,
        { expected_version: preservedIssue.version },
      ),
    )
    expect(screen.getByRole("heading", { name: "Evidence" })).toBeVisible()
    expect(screen.getByText("Version fence is absent.")).toBeVisible()
  })

  it("never offers AI regeneration for a canonical legacy editing draft", async () => {
    const legacyIssue: RepositoryReviewIssueDraft = {
      ...issue,
      id: "draft_legacy",
      origin: "legacy",
      generation_id: undefined,
      resolved_instructions: undefined,
      instructions_mode: undefined,
      generator_model: undefined,
      generator_account: undefined,
      canonical: true,
      read_only: false,
      state: "editing",
      regeneratable: undefined,
    }
    vi.mocked(getRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      repository: repositorySummary,
      issue: legacyIssue,
      finding,
      capabilities: {
        github: true,
        can_edit: true,
        can_delete: true,
        can_regenerate: false,
        can_publish: true,
      },
    })
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={legacyIssue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onOpenFinding={vi.fn()}
        onManageLink={vi.fn()}
      />,
    )

    expect(
      (await screen.findAllByText("legacy", { exact: true })).length,
    ).toBeGreaterThan(0)
    expect(
      screen.queryByRole("button", { name: /Regenerate|Retry generation/u }),
    ).not.toBeInTheDocument()
    expect(regenerateRepositoryReviewAutomationIssue).not.toHaveBeenCalled()
  })

  it("lets an interrupted generating preview retry its original reservation", async () => {
    const generatingIssue: RepositoryReviewIssueDraft = {
      ...issue,
      state: "generating",
      title: "",
      body: "",
      version: 1,
    }
    vi.mocked(getRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      issue: generatingIssue,
      finding,
      capabilities: { github: true, can_regenerate: true },
    })
    vi.mocked(regenerateRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      issue: {
        ...generatingIssue,
        state: "editing",
        title: issue.title,
        body: issue.body,
      },
      finding,
      capabilities: { github: true, can_regenerate: true },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={generatingIssue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onOpenFinding={vi.fn()}
        onManageLink={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Retry generation" }),
    )
    await waitFor(() =>
      expect(regenerateRepositoryReviewAutomationIssue).toHaveBeenCalledWith(
        automation.id,
        generatingIssue.id,
        { expected_version: generatingIssue.version },
      ),
    )
  })
})

type RepositoryReviewRouteKind = "detail" | "finding" | "issue"

function rejectRouteLoader(kind: RepositoryReviewRouteKind, error: Error) {
  if (kind === "detail") {
    vi.mocked(getRepositoryReviewAutomation).mockRejectedValue(error)
    return
  }
  if (kind === "finding") {
    vi.mocked(getRepositoryReviewAutomationFinding).mockRejectedValue(error)
    return
  }
  vi.mocked(getRepositoryReviewAutomationIssue).mockRejectedValue(error)
}

function repositoryReviewRouteElement(
  kind: RepositoryReviewRouteKind,
): ReactElement {
  if (kind === "detail") {
    return (
      <RepositoryReviewDetailPage
        id={automation.id}
        onBack={vi.fn()}
        onReport={vi.fn()}
        onIssues={vi.fn()}
      />
    )
  }
  if (kind === "finding") {
    return (
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={finding.id}
        onBack={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />
    )
  }
  return (
    <RepositoryReviewIssuePage
      automationID={automation.id}
      draftID={issue.id}
      onBack={vi.fn()}
      onDeleted={vi.fn()}
      onOpenFinding={vi.fn()}
      onManageLink={vi.fn()}
    />
  )
}

function ReportPagingHarness() {
  const [search, setSearch] = useState<RepositoryReviewRouteSearch>({
    q: "ORDER BY repository ASC",
    scope: "current",
    offset: 0,
  })
  return (
    <RepositoryReviewReportPage
      automationID={automation.id}
      search={search}
      onSearchChange={setSearch}
      onBack={vi.fn()}
      onOpenFinding={vi.fn()}
      onGenerated={vi.fn()}
      onOpenThread={vi.fn()}
    />
  )
}

function ReportScopeHarness() {
  const [search, setSearch] = useState<RepositoryReviewRouteSearch>({
    q: "ORDER BY repository ASC",
    scope: "current",
    offset: 50,
    generation_id: "rig_previous",
  })
  return (
    <>
      <output data-testid="report-route-search">
        {JSON.stringify(search)}
      </output>
      <RepositoryReviewReportPage
        automationID={automation.id}
        search={search}
        onSearchChange={setSearch}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />
    </>
  )
}

function renderPage(element: ReactElement) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: {
            queries: { retry: false },
            mutations: { retry: false },
          },
        })
      }
    >
      {element}
    </QueryClientProvider>,
  )
}
