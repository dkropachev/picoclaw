import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactElement, useState } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  RepositoryFinding,
  RepositoryReviewAutomation,
  RepositoryReviewFinding,
  RepositoryReviewIssueDraft,
  RepositoryReviewIssueSummary,
  RepositoryReviewRawFinding,
  RepositoryReviewRepositoryFindingSummary,
  RepositoryReviewRunFindingSummary,
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
  getRepositoryReviewAutomationRepositoryFinding,
  getRepositoryReviewCommitOptions,
  getRepositoryReviewRawSource,
  linkRepositoryReviewIssue,
  listRepositoryReviewAutomationFindingsPage,
  listRepositoryReviewAutomationIssuesPage,
  listRepositoryReviewAutomationRawFindingsPage,
  listRepositoryReviewAutomationRepositoryFindingsPage,
  listRepositoryReviewFindingRawSources,
  publishRepositoryReviewAutomationIssue,
  publishRepositoryReviewIssues,
  regenerateRepositoryReviewAutomationIssue,
  reserveRepositoryReviewValidations,
  restartRepositoryReviewAutomation,
  resumeRepositoryReviewAutomation,
  retryRepositoryReviewHistoricalDeduplication,
  retryRepositoryReviewRawSource,
  retryRepositoryReviewRunFindingStatuses,
  startRepositoryReviewAutomation,
  unlinkRepositoryReviewIssue,
  updateRepositoryReviewAutomationIssue,
} from "@/api/repository-reviews"
import { type ThreadSummary, createThread } from "@/api/threads"
import { RepositoryReviewDetailPage } from "@/components/repository-reviews/repository-review-detail-page"
import { RepositoryReviewFindingPage } from "@/components/repository-reviews/repository-review-finding-page"
import { RepositoryReviewFindingsPage } from "@/components/repository-reviews/repository-review-findings-page"
import { RepositoryReviewIssueEditorPage } from "@/components/repository-reviews/repository-review-issue-editor-page"
import { RepositoryReviewIssuePage } from "@/components/repository-reviews/repository-review-issue-page"
import { RepositoryReviewIssuesPage } from "@/components/repository-reviews/repository-review-issues-page"
import { RepositoryReviewLinkIssuePage } from "@/components/repository-reviews/repository-review-link-issue-page"
import { RepositoryReviewRawFindingPage } from "@/components/repository-reviews/repository-review-raw-finding-page"
import { RepositoryReviewRawFindingsPage } from "@/components/repository-reviews/repository-review-raw-findings-page"
import { RepositoryReviewRepositoryFindingsPage } from "@/components/repository-reviews/repository-review-repository-findings-page"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"

import type { RepositoryReviewCollectionSearch } from "./repository-review-route-state"

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
  getRepositoryReviewAutomationRepositoryFinding: vi.fn(),
  getRepositoryReviewRawSource: vi.fn(),
  getRepositoryReviewAutomationIssue: vi.fn(),
  listRepositoryReviewAutomationFindingsPage: vi.fn(),
  listRepositoryReviewAutomationRepositoryFindingsPage: vi.fn(),
  listRepositoryReviewAutomationRawFindingsPage: vi.fn(),
  listRepositoryReviewFindingRawSources: vi.fn(),
  listRepositoryReviewAutomationIssuesPage: vi.fn(),
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
  publishRepositoryReviewIssues: vi.fn(),
  postRepositoryReviewFinding: vi.fn(),
  reserveRepositoryReviewValidations: vi.fn(),
  resolveRepositoryReviewPossibleDuplicate: vi.fn(),
  retryRepositoryReviewRunFindingStatuses: vi.fn(),
  retryRepositoryReviewHistoricalDeduplication: vi.fn(),
  retryRepositoryReviewRawSource: vi.fn(),
  syncRepositoryReviewFinding: vi.fn(),
  updateRepositoryReviewFindingLifecycle: vi.fn(),
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
  assignment_timeout_seconds: 3_600,
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
    coverage_available: false,
    coverage_exact: false,
    selected_files: 8,
    inspected_files: 0,
    reviewed_files: 4,
    remaining_files: 4,
    unsupported_files: 0,
    findings: 1,
    finding_aggregates: 1,
    unaggregated_findings: 0,
    assignment_progress: {
      total: 32,
      completed: 18,
      pending: 12,
      active: 2,
      by_focus: {
        correctness_state: { total: 8, completed: 4, pending: 3, active: 1 },
        security_trust: { total: 8, completed: 5, pending: 3, active: 0 },
        concurrency_recovery: {
          total: 8,
          completed: 5,
          pending: 2,
          active: 1,
        },
        integration_validation: {
          total: 8,
          completed: 4,
          pending: 4,
          active: 0,
        },
      },
    },
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
  models: ["openai/gpt-5.4"],
  observation_count: 1,
  observations: [
    {
      context_id: "context_1",
      model: "openai/gpt-5.4",
      model_alias: "reviewer",
      account: "openai-work",
      reviewer: "correctness",
      severity: "high",
      title: "Lost update",
      line: 42,
      message: "Concurrent writes overwrite state.",
      evidence: "The write has no version fence.",
      impact: "A stored finding can disappear.",
      validation: {
        status: "confirmed",
        summary: "Traced both writers.",
        checks: ["race trace"],
      },
    },
  ],
  repository_finding_id: "rrf_1",
  repository_match_state: "known",
  run_finding_status: "associated_existing",
  status: "open",
  version: 2,
  created_at: "2026-08-26T00:00:00Z",
  updated_at: "2026-08-26T00:00:00Z",
}

const rawFinding: RepositoryReviewRawFinding = {
  id: "rrw_1",
  version: 1,
  campaign_id: "rrc_1",
  repository: finding.repository,
  commit_sha: finding.commit_sha,
  file: finding.file,
  line: finding.line,
  severity: finding.severity,
  title: finding.title,
  symbol: finding.symbol,
  message: finding.message,
  evidence: finding.evidence,
  impact: finding.impact,
  validation: finding.validation,
  context_id: finding.context_ids[0],
  run_id: "run_1",
  assignment_id: "rra_1",
  model: finding.models[0]!,
  reviewer: "reviewer-1",
  deduplication_state: "completed",
  disposition: "new",
  deduplicated_finding_id: finding.id,
  created_at: finding.created_at,
  updated_at: finding.updated_at,
}

const repositoryFinding: RepositoryFinding = {
  id: "rrf_1",
  repository: "owner/repo",
  canonical_title: "Lost update across commits",
  canonical_severity: "high",
  review_finding_ids: [finding.id],
  found_commits: [finding.commit_sha],
  path_symbol_history: [
    {
      review_finding_id: finding.id,
      commit_sha: finding.commit_sha,
      path: finding.file.path,
      symbol: finding.symbol,
      observed_at: finding.created_at,
    },
  ],
  match_state: "known",
  lifecycle: "open",
  issue: { state: "none" },
  validation_state: "not_requested",
  version: 1,
  created_at: finding.created_at,
  updated_at: finding.updated_at,
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

const findingSummary: RepositoryReviewRunFindingSummary = {
  id: finding.id,
  repository: finding.repository,
  path: finding.file.path,
  line: finding.line,
  severity: finding.severity,
  title: finding.title,
  symbol: finding.symbol,
  status: finding.status,
  run_finding_status: finding.run_finding_status!,
  association: "existing",
  repository_finding_id: finding.repository_finding_id,
  contributors: finding.models,
  raw_source_count: 1,
  created_at: finding.created_at,
  updated_at: finding.updated_at,
}

const repositoryFindingSummary: RepositoryReviewRepositoryFindingSummary = {
  id: repositoryFinding.id,
  repository: repositoryFinding.repository,
  canonical_title: repositoryFinding.canonical_title,
  canonical_severity: repositoryFinding.canonical_severity,
  path: finding.file.path,
  symbol: finding.symbol,
  match_state: repositoryFinding.match_state,
  lifecycle: repositoryFinding.lifecycle,
  issue: repositoryFinding.issue,
  validation_state: repositoryFinding.validation_state,
  occurrence_count: 1,
  found_commit_count: 1,
  created_at: repositoryFinding.created_at,
  updated_at: repositoryFinding.updated_at,
}

const issueSummary: RepositoryReviewIssueSummary = {
  id: issue.id,
  repository: issue.repository,
  finding_count: issue.finding_ids.length,
  origin: issue.origin!,
  generation_id: issue.generation_id,
  canonical: true,
  publishable: true,
  title: issue.title,
  state: issue.state,
  version: issue.version,
  created_at: issue.created_at,
  updated_at: issue.updated_at,
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
  title: "Existing lost-update issue",
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
    vi.mocked(listRepositoryReviewAutomationFindingsPage).mockResolvedValue({
      automation,
      repository: repositorySummary,
      findings: [findingSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY severity DESC, updated DESC",
      query_schema: { fields: [] },
      capabilities: { can_generate: true, github: true },
    })
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      action_finding: finding,
      repository_finding: repositoryFinding,
      occurrences: [finding],
      contexts: [
        {
          id: "context_1",
          repository: "owner/repo",
          commit_sha: finding.commit_sha,
          inventory_hash: "inventory",
          profile_hash: "profile",
          run_id: "run_1",
          model: "openai/gpt-5.4",
          model_alias: "reviewer",
          account: "openai-work",
          reviewer: "correctness",
          files: [finding.file],
          created_at: "2026-08-26T00:00:00Z",
        },
      ],
      raw_source_total: 1,
      capabilities: { github: true, can_generate: true, can_link_issue: true },
    })
    vi.mocked(
      getRepositoryReviewAutomationRepositoryFinding,
    ).mockImplementation((...args) =>
      getRepositoryReviewAutomationFinding(...args),
    )
    vi.mocked(
      listRepositoryReviewAutomationRepositoryFindingsPage,
    ).mockResolvedValue({
      automation,
      repository: repositorySummary,
      repository_findings: [repositoryFindingSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY severity DESC, updated DESC",
      query_schema: { fields: [] },
      capabilities: { can_generate: true, github: true },
    })
    vi.mocked(listRepositoryReviewFindingRawSources).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding_id: finding.id,
      sources: [rawFinding],
      raw_findings: [],
      offset: 0,
      total: 1,
    })
    vi.mocked(listRepositoryReviewAutomationRawFindingsPage).mockResolvedValue({
      automation,
      repository: repositorySummary,
      raw_findings: [rawFinding],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY created DESC",
      query_schema: { fields: [] },
      findings_processing: {
        raw_total: 1,
        pending: 0,
        processing: 0,
        failed: 0,
        completed: 1,
        new: 1,
        duplicates: 0,
      },
    })
    vi.mocked(getRepositoryReviewRawSource).mockResolvedValue({
      automation,
      repository: repositorySummary,
      source: rawFinding,
      finding,
      context: {
        id: "context_1",
        repository: finding.repository,
        commit_sha: finding.commit_sha,
        inventory_hash: "inventory",
        profile_hash: "profile",
        run_id: "run_1",
        model: rawFinding.model,
        reviewer: rawFinding.reviewer,
        files: [finding.file],
        created_at: finding.created_at,
      },
    })
    vi.mocked(retryRepositoryReviewRawSource).mockResolvedValue({
      automation,
      repository: repositorySummary,
      source: { ...rawFinding, deduplication_state: "pending" },
    })
    vi.mocked(retryRepositoryReviewHistoricalDeduplication).mockResolvedValue({
      automation,
      repository: repositorySummary,
      historical_deduplication: { required: true, status: "pending" },
    })
    vi.mocked(listRepositoryReviewAutomationIssuesPage).mockResolvedValue({
      automation,
      repository: repositorySummary,
      issues: [issueSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY updated DESC",
      query_schema: { fields: [] },
      capabilities: { github: true, can_publish: true },
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
    vi.mocked(publishRepositoryReviewIssues).mockResolvedValue({
      results: [
        {
          draft_id: issue.id,
          success: true,
          outcome: "posted",
        },
      ],
    })
    vi.mocked(reserveRepositoryReviewValidations).mockResolvedValue({
      validation_jobs: [],
    })
    vi.mocked(retryRepositoryReviewRunFindingStatuses).mockResolvedValue({
      automation,
      repository: repositorySummary,
      findings: [],
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

  it.each([
    ["run", repositoryFinding.id],
    ["repository", finding.id],
  ] as const)(
    "rejects a %s detail response whose requested ID names the other resource",
    async (resourceKind, findingID) => {
      renderPage(
        <RepositoryReviewFindingPage
          automationID={automation.id}
          findingID={findingID}
          resourceKind={resourceKind}
          onBack={vi.fn()}
          onOpenRepositoryFinding={vi.fn()}
          onOpenIssue={vi.fn()}
          onLinkIssue={vi.fn()}
          onGenerated={vi.fn()}
          onOpenThread={vi.fn()}
        />,
      )

      expect(
        await screen.findByRole("heading", { name: "Item not found" }),
      ).toBeVisible()
    },
  )

  it("keeps findings and issue previews available while a review is active", async () => {
    const onFindings = vi.fn()
    const onRawFindings = vi.fn()
    const onIssues = vi.fn()
    renderPage(
      <RepositoryReviewDetailPage
        id={automation.id}
        onBack={vi.fn()}
        onFindings={onFindings}
        onRawFindings={onRawFindings}
        onIssues={onIssues}
      />,
    )

    await userEvent.click(
      await screen.findByRole("button", { name: /Findings/ }),
    )
    await userEvent.click(
      screen.getByRole("button", { name: /Issue previews/ }),
    )
    await userEvent.click(screen.getByRole("button", { name: /Raw findings/ }))
    expect(onFindings).toHaveBeenCalled()
    expect(onRawFindings).toHaveBeenCalled()
    expect(onIssues).toHaveBeenCalled()
    expect(screen.getByText("writer")).toBeVisible()
    expect(screen.getByText("4 of 8 files (50%)")).toBeVisible()
    expect(screen.getByText("Fully reviewed files")).toBeVisible()
    expect(screen.getAllByText("Findings").length).toBeGreaterThan(0)
    expect(screen.getAllByText("Raw findings").length).toBeGreaterThan(0)
    expect(screen.getByText("Unassociated occurrences")).toBeVisible()
    expect(screen.getByText("Unknown")).toBeVisible()
    expect(screen.getByText("Review assignment coverage")).toBeVisible()
    expect(screen.getByText("Total assignments")).toBeVisible()
    expect(screen.getByText("Completed assignments")).toBeVisible()
    expect(screen.getByText("Pending assignments")).toBeVisible()
    expect(screen.getByText("Active assignments")).toBeVisible()
    expect(screen.getByText("Correctness and state")).toBeVisible()
    expect(
      screen.getByText("4 of 8 completed · 3 pending · 1 active"),
    ).toBeVisible()
    expect(screen.getByText("Assignment deadline")).toBeVisible()
    expect(screen.getByText("60 minutes")).toBeVisible()
    expect(
      screen.getByText(/every required reviewer acknowledges the file/i),
    ).toBeVisible()
    expect(
      screen.getByText(/inspected-file coverage is unknown/i),
    ).toBeVisible()
  })

  it("shows truthful zero file progress for a finding-only paused campaign", async () => {
    const pausedReview: RepositoryReviewAutomation = {
      ...automation,
      status: "paused",
      pause_reason: "no_progress",
      pause_detail:
        "Automatic continuation stopped after a verified batch resolved zero files.",
      progress: {
        ...automation.progress,
        completed_batches: 16,
        total_batches: 32,
        reviewed_files: 0,
        remaining_files: 0,
        unsupported_files: 0,
        findings: 74,
        scope_frozen: true,
      },
      scope_plan: {
        commit_sha: "a".repeat(40),
        policy_hash: "b".repeat(64),
        hash: "c".repeat(64),
        summary: "Frozen scope",
        rationale: "",
        warnings: [],
        counts: {
          total_files: 10,
          code_type_files: 10,
          include_files: 10,
          excluded_files: 0,
          selected_files: 10,
        },
      },
    }
    vi.mocked(getRepositoryReviewAutomation).mockResolvedValue(pausedReview)
    renderPage(
      <RepositoryReviewDetailPage
        id={pausedReview.id}
        onBack={vi.fn()}
        onFindings={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    expect(await screen.findByText("0 of 10 files (0%)")).toBeVisible()
    expect(
      screen.getByText(
        /automatic continuation stopped after a verified batch/i,
      ),
    ).toBeVisible()
    expect(screen.getByRole("button", { name: "Continue" })).toBeVisible()
    expect(screen.getByText("74")).toBeVisible()
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
        onFindings={vi.fn()}
        onIssues={vi.fn()}
      />,
    )

    const inspected = await screen.findByText("Inspected files")
    expect(inspected.parentElement).toHaveTextContent("0")
    expect(
      screen.queryByText(/inspected-file coverage is unknown/i),
    ).not.toBeInTheDocument()
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
        onFindings={vi.fn()}
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
        onFindings={vi.fn()}
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
        onFindings={vi.fn()}
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

  it("selects a repository finding and generates one preview batch", async () => {
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
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      action_finding: finding,
      repository_finding: repositoryFinding,
      occurrences: [finding],
      contexts: [],
      capabilities: { github: true, can_generate: true, can_link_issue: true },
    })
    renderPage(
      <RepositoryReviewRepositoryFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={onGenerated}
      />,
    )

    const row = (
      await screen.findByText(repositoryFinding.canonical_title)
    ).closest("[data-item-id]")!
    await user.click(row)
    await user.click(
      screen.getByRole("button", { name: "Draft issue previews" }),
    )
    await user.click(screen.getByRole("button", { name: "Draft previews" }))

    await waitFor(() =>
      expect(generateRepositoryReviewIssues).toHaveBeenCalledWith(
        automation.id,
        expect.objectContaining({
          finding_ids: [finding.id],
          instructions_mode: "default",
        }),
      ),
    )
    expect(screen.getByRole("button", { name: "Drafting…" })).toBeDisabled()
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
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRepositoryFindings={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
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
    expect(message.content).toContain("Raw source count: 1")
    expect(message.content).toContain(`- Context ${finding.context_ids[0]}`)
    expect(onOpenThread).toHaveBeenCalledWith(discussionThread.ui_session_id)
    expect(generateRepositoryReviewIssues).not.toHaveBeenCalled()
    expect(findRepositoryReviewIssueCandidates).not.toHaveBeenCalled()
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()
    expect(publishRepositoryReviewAutomationIssue).not.toHaveBeenCalled()
  })

  it("keeps run findings current-only and opens the repository surface explicitly", async () => {
    const onOpenRepositoryFindings = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRepositoryFindings={onOpenRepositoryFindings}
        onOpenRepositoryFinding={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    await screen.findByText(finding.title)
    expect(listRepositoryReviewAutomationFindingsPage).toHaveBeenCalledWith(
      automation.id,
      { query: "ORDER BY repository ASC", cursor: undefined, limit: 50 },
      expect.any(AbortSignal),
    )
    expect(
      screen.queryByText(repositoryFinding.canonical_title),
    ).not.toBeInTheDocument()
    await user.click(
      screen.getByRole("button", { name: "View repository findings" }),
    )
    expect(onOpenRepositoryFindings).toHaveBeenCalledOnce()
  })

  it("shows paginated raw sources on a finding and opens canonical raw detail", async () => {
    const secondSource: RepositoryReviewRawFinding = {
      ...rawFinding,
      id: "rrw_2",
      title: "Second raw diagnosis",
      disposition: "duplicate",
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      contexts: [],
      raw_source_total: 2,
      capabilities: { github: true },
    })
    vi.mocked(listRepositoryReviewFindingRawSources).mockImplementation(
      async (_automationID, _findingID, input) => ({
        automation,
        repository: repositorySummary,
        finding_id: finding.id,
        sources: input?.offset === 25 ? [secondSource] : [rawFinding],
        raw_findings: [],
        offset: input?.offset ?? 0,
        total: 2,
        ...(input?.offset === 25 ? {} : { next_offset: 25 }),
      }),
    )
    const onOpenRawFinding = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={finding.id}
        resourceKind="run"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenRawFinding={onOpenRawFinding}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(await screen.findByText(/2 raw sources support/u)).toBeVisible()
    await user.click(
      await screen.findByRole("button", {
        name: new RegExp(rawFinding.title, "u"),
      }),
    )
    expect(onOpenRawFinding).toHaveBeenCalledWith(rawFinding.id)
    await user.click(
      screen.getByRole("button", { name: "Load more raw findings" }),
    )
    await user.click(
      await screen.findByRole("button", { name: /Second raw diagnosis/u }),
    )
    expect(onOpenRawFinding).toHaveBeenLastCalledWith(secondSource.id)
    expect(listRepositoryReviewFindingRawSources).toHaveBeenCalledWith(
      automation.id,
      finding.id,
      { offset: 25, limit: 25 },
      expect.any(AbortSignal),
    )
  })

  it("shows raw processing counters and explicitly retries a failed historical replay", async () => {
    vi.mocked(listRepositoryReviewAutomationFindingsPage).mockResolvedValue({
      automation: { ...automation, status: "failed" },
      repository: repositorySummary,
      findings: [findingSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY severity DESC, updated DESC",
      query_schema: { fields: [] },
      findings_processing: {
        raw_total: 87,
        pending: 4,
        processing: 0,
        failed: 1,
        completed: 82,
        new: 40,
        duplicates: 42,
      },
      historical_deduplication: {
        required: true,
        status: "failed",
        error: "Campaign recovery must be retried.",
      },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ALL ORDER BY severity DESC, updated DESC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRawFindings={vi.fn()}
        onOpenRepositoryFindings={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(await screen.findByText("87")).toBeVisible()
    expect(screen.getByText("Campaign recovery must be retried.")).toBeVisible()
    await user.click(
      screen.getByRole("button", {
        name: "Retry historical deduplication",
      }),
    )
    await waitFor(() =>
      expect(retryRepositoryReviewHistoricalDeduplication).toHaveBeenCalledWith(
        automation.id,
      ),
    )
  })

  it("loads the typed raw collection and normalizes a legacy source detail", async () => {
    const onOpenRawFinding = vi.fn()
    const user = userEvent.setup()
    const collectionView = renderPage(
      <RepositoryReviewRawFindingsPage
        automationID={automation.id}
        search={{ q: "ALL ORDER BY created DESC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenRawFinding={onOpenRawFinding}
        onOpenFinding={vi.fn()}
      />,
    )

    const item = (await screen.findByText(rawFinding.title)).closest(
      "[data-item-id]",
    )!
    await user.dblClick(item)
    expect(onOpenRawFinding).toHaveBeenCalledWith(rawFinding.id)
    expect(listRepositoryReviewAutomationRawFindingsPage).toHaveBeenCalledWith(
      automation.id,
      {
        query: "ALL ORDER BY created DESC",
        cursor: undefined,
        limit: 50,
      },
      expect.any(AbortSignal),
    )
    collectionView.unmount()

    const failedSource: RepositoryReviewRawFinding = {
      ...rawFinding,
      deduplication_state: "failed",
      disposition: "undecided",
      deduplicated_finding_id: undefined,
      failure: {
        code: "provider_failed",
        message: "The deduplication provider failed.",
        retryable: true,
        at: rawFinding.updated_at,
      },
    }
    vi.mocked(getRepositoryReviewRawSource).mockResolvedValue({
      automation,
      repository: repositorySummary,
      source: failedSource,
    })
    const onCanonicalSource = vi.fn()
    renderPage(
      <RepositoryReviewRawFindingPage
        automationID={automation.id}
        sourceID="rfn_legacy"
        onBack={vi.fn()}
        onCanonicalSource={onCanonicalSource}
        onOpenFinding={vi.fn()}
      />,
    )

    expect(
      await screen.findByText("The deduplication provider failed."),
    ).toBeVisible()
    await waitFor(() =>
      expect(onCanonicalSource).toHaveBeenCalledWith(failedSource.id),
    )
    await user.click(screen.getByRole("button", { name: "Retry raw finding" }))
    await waitFor(() =>
      expect(retryRepositoryReviewRawSource).toHaveBeenCalledWith(
        automation.id,
        failedSource.id,
      ),
    )
  })

  it("shows exact model provenance for a raw source and its immutable context", async () => {
    const source = {
      ...rawFinding,
      model_alias: "raw-reviewer-alias",
      account: "raw-review-account",
    }
    vi.mocked(getRepositoryReviewRawSource).mockResolvedValue({
      automation,
      repository: repositorySummary,
      source,
      finding,
      context: {
        id: "context_1",
        repository: finding.repository,
        commit_sha: finding.commit_sha,
        inventory_hash: "inventory",
        profile_hash: "profile",
        run_id: "run_1",
        model: source.model,
        model_alias: "context-reviewer-alias",
        account: "context-review-account",
        reviewer: source.reviewer,
        files: [finding.file],
        created_at: finding.created_at,
      },
    })

    renderPage(
      <RepositoryReviewRawFindingPage
        automationID={automation.id}
        sourceID={source.id}
        onBack={vi.fn()}
        onCanonicalSource={vi.fn()}
        onOpenFinding={vi.fn()}
      />,
    )

    const sourceProvenance = await screen.findByRole("region", {
      name: "Location and provenance",
    })
    expect(
      within(sourceProvenance).getByText("raw-reviewer-alias"),
    ).toBeVisible()
    expect(
      within(sourceProvenance).getByText("raw-review-account"),
    ).toBeVisible()

    const immutableContext = screen.getByRole("region", {
      name: "Immutable context",
    })
    expect(
      within(immutableContext).getByText("context-reviewer-alias"),
    ).toBeVisible()
    expect(
      within(immutableContext).getByText("context-review-account"),
    ).toBeVisible()
  })

  it("opens an associated repository finding from a single run selection", async () => {
    const onOpenRepositoryFinding = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRepositoryFindings={vi.fn()}
        onOpenRepositoryFinding={onOpenRepositoryFinding}
        onOpenThread={vi.fn()}
      />,
    )

    const row = (await screen.findByText(finding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(row)
    await user.click(
      screen.getByRole("button", { name: "Open repository finding" }),
    )
    expect(onOpenRepositoryFinding).toHaveBeenCalledWith(
      finding.repository_finding_id,
    )
  })

  it("retries a failed run finding status without offering issue drafting", async () => {
    const failedFinding: RepositoryReviewFinding = {
      ...finding,
      repository_finding_id: undefined,
      repository_match_state: undefined,
      run_finding_status: "failed",
      issue_draft_id: undefined,
    }
    const failedFindingSummary: RepositoryReviewRunFindingSummary = {
      ...findingSummary,
      run_finding_status: "failed",
      association: "unassociated",
      repository_finding_id: undefined,
    }
    vi.mocked(listRepositoryReviewAutomationFindingsPage).mockResolvedValue({
      automation: { ...automation, status: "failed" },
      repository: repositorySummary,
      findings: [failedFindingSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY severity DESC, updated DESC",
      query_schema: { fields: [] },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRepositoryFindings={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    const row = (await screen.findByText(failedFinding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(row)
    await user.click(screen.getByRole("button", { name: "Retry status" }))
    await waitFor(() =>
      expect(retryRepositoryReviewRunFindingStatuses).toHaveBeenCalledWith(
        automation.id,
        [failedFinding.id],
      ),
    )
    expect(
      screen.queryByRole("button", { name: "Draft issue previews" }),
    ).not.toBeInTheDocument()
  })

  it("drafts a canonical issue from a selected repository finding", async () => {
    vi.mocked(
      listRepositoryReviewAutomationRepositoryFindingsPage,
    ).mockResolvedValue({
      automation,
      repository: repositorySummary,
      repository_findings: [repositoryFindingSummary],
      total: 1,
      next_cursor: "",
      canonical_query: "ALL ORDER BY severity DESC, updated DESC",
      query_schema: { fields: [] },
      capabilities: { can_generate: true, github: true },
    })
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      action_finding: finding,
      repository_finding: repositoryFinding,
      occurrences: [finding],
      contexts: [],
      capabilities: { github: true, can_generate: true, can_link_issue: true },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewRepositoryFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={vi.fn()}
      />,
    )

    const item = (
      await screen.findByText(repositoryFinding.canonical_title)
    ).closest("[data-item-id]")!
    await user.click(item)
    await user.click(
      screen.getByRole("button", { name: "Draft issue previews" }),
    )
    await user.click(screen.getByRole("button", { name: "Draft previews" }))
    await waitFor(() =>
      expect(generateRepositoryReviewIssues).toHaveBeenCalledWith(
        automation.id,
        expect.objectContaining({ finding_ids: [finding.id] }),
      ),
    )
  })

  it("clears a repository-finding selection after queueing validation", async () => {
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewRepositoryFindingsPage
        automationID={automation.id}
        search={{ q: "ALL ORDER BY severity DESC, updated DESC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onGenerated={vi.fn()}
      />,
    )

    const item = (
      await screen.findByText(repositoryFinding.canonical_title)
    ).closest("[data-item-id]")!
    await user.click(item)
    expect(screen.getByText("1 selected", { exact: true })).toBeVisible()
    await user.click(
      screen.getByRole("button", { name: "Validate resolutions" }),
    )

    await waitFor(() =>
      expect(reserveRepositoryReviewValidations).toHaveBeenCalledWith(
        automation.id,
        [repositoryFinding.id],
      ),
    )
    await waitFor(() =>
      expect(
        screen.queryByText("1 selected", { exact: true }),
      ).not.toBeInTheDocument(),
    )
  })

  it("publishes explicitly selected previews and clears reconciled selection", async () => {
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssuesPage
        automationID={automation.id}
        search={{ q: "ALL ORDER BY updated DESC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenIssue={vi.fn()}
      />,
    )

    const item = (await screen.findByText(issue.title)).closest(
      "[data-item-id]",
    )!
    await user.click(item)
    await user.click(screen.getByRole("button", { name: "Post selected" }))

    await waitFor(() =>
      expect(publishRepositoryReviewIssues).toHaveBeenCalledWith(
        automation.id,
        {
          issues: [{ id: issue.id, expected_version: issue.version }],
          confirmed: true,
        },
      ),
    )
    expect(await screen.findByText("Posting outcomes")).toBeVisible()
    expect(screen.getByText(`${issue.id}: posted`)).toBeVisible()
    expect(
      screen.queryByText("1 selected", { exact: true }),
    ).not.toBeInTheDocument()
  })

  it("saves issue-preview edits through the dedicated version-fenced editor", async () => {
    const savedIssue = {
      ...issue,
      title: "Updated lost-update diagnosis",
      version: issue.version + 1,
    }
    vi.mocked(updateRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      repository: repositorySummary,
      issue: savedIssue,
      finding,
      capabilities: { github: true, can_edit: true },
    })
    const onSaved = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssueEditorPage
        automationID={automation.id}
        draftID={issue.id}
        onBack={vi.fn()}
        onSaved={onSaved}
      />,
    )

    const title = await screen.findByRole("textbox", { name: "Title" })
    await user.clear(title)
    await user.type(title, savedIssue.title)
    await user.click(screen.getByRole("button", { name: "Save preview" }))

    await waitFor(() =>
      expect(updateRepositoryReviewAutomationIssue).toHaveBeenCalledWith(
        automation.id,
        issue.id,
        {
          title: savedIssue.title,
          body: issue.body,
          labels: issue.labels,
          expected_version: issue.version,
        },
      ),
    )
    expect(onSaved).toHaveBeenCalledWith(savedIssue)
  })

  it("resets the issue editor when the routed draft identity changes", async () => {
    const secondIssue = {
      ...issue,
      id: "issue_2",
      title: "Second issue preview",
      version: issue.version + 1,
    }
    vi.mocked(getRepositoryReviewAutomationIssue).mockImplementation(
      async (_automationID, draftID) => ({
        automation,
        repository: repositorySummary,
        issue: draftID === secondIssue.id ? secondIssue : issue,
        finding,
        capabilities: { github: true, can_edit: true },
      }),
    )

    function IdentityHarness() {
      const [draftID, setDraftID] = useState(issue.id)
      return (
        <>
          <button type="button" onClick={() => setDraftID(secondIssue.id)}>
            Switch draft
          </button>
          <RepositoryReviewIssueEditorPage
            automationID={automation.id}
            draftID={draftID}
            onBack={vi.fn()}
            onSaved={vi.fn()}
          />
        </>
      )
    }

    const user = userEvent.setup()
    renderPage(<IdentityHarness />)
    expect(await screen.findByRole("textbox", { name: "Title" })).toHaveValue(
      issue.title,
    )
    await user.click(screen.getByRole("button", { name: "Switch draft" }))
    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue(
        secondIssue.title,
      ),
    )
  })

  it("polls active findings and stops polling after the review becomes terminal", async () => {
    let requestCount = 0
    vi.mocked(listRepositoryReviewAutomationFindingsPage).mockImplementation(
      async () => {
        requestCount += 1
        return {
          automation: {
            ...automation,
            status: requestCount === 1 ? "running" : "completed",
          },
          repository: repositorySummary,
          findings: [findingSummary],
          total: 1,
          next_cursor: "",
          canonical_query: "ALL ORDER BY severity DESC, updated DESC",
          query_schema: { fields: [] },
          capabilities: { can_generate: true, github: true },
        }
      },
    )
    const view = renderPage(
      <RepositoryReviewFindingsPage
        automationID={automation.id}
        search={{ q: "ORDER BY repository ASC" }}
        onSearchChange={vi.fn()}
        onBack={vi.fn()}
        onOpenFinding={vi.fn()}
        onOpenRepositoryFindings={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    await waitFor(() =>
      expect(listRepositoryReviewAutomationFindingsPage).toHaveBeenCalledTimes(
        1,
      ),
    )
    await waitFor(
      () =>
        expect(
          listRepositoryReviewAutomationFindingsPage,
        ).toHaveBeenCalledTimes(2),
      { timeout: 3_000 },
    )
    expect(await screen.findByText("completed", { exact: true })).toBeVisible()

    await new Promise((resolve) => setTimeout(resolve, 2_200))
    expect(listRepositoryReviewAutomationFindingsPage).toHaveBeenCalledTimes(2)
    view.unmount()
  }, 8_000)

  it("retains explicit selection across review-finding pages", async () => {
    const secondFinding: RepositoryReviewFinding = {
      ...finding,
      id: "finding_2",
      title: "Second review finding",
      fingerprint: "fingerprint-2",
      issue_draft_id: undefined,
    }
    const secondFindingSummary: RepositoryReviewRunFindingSummary = {
      ...findingSummary,
      id: secondFinding.id,
      title: secondFinding.title,
    }
    vi.mocked(listRepositoryReviewAutomationFindingsPage).mockImplementation(
      async (_automationID, input) => {
        const nextPage = input?.cursor === "cursor-2"
        return {
          automation,
          findings: nextPage ? [secondFindingSummary] : [findingSummary],
          total: 51,
          next_cursor: nextPage ? "" : "cursor-2",
          canonical_query: "ALL ORDER BY severity DESC, updated DESC",
          query_schema: { fields: [] },
          capabilities: { can_generate: true, github: true },
        }
      },
    )
    const user = userEvent.setup()
    renderPage(<FindingsPagingHarness />)

    const first = (await screen.findByText(finding.title)).closest(
      "[data-item-id]",
    )!
    await user.click(first)
    await user.click(screen.getByRole("button", { name: "Load more" }))
    const second = (await screen.findByText(secondFinding.title)).closest(
      "[data-item-id]",
    )!
    fireEvent.click(second, { ctrlKey: true })
    expect(screen.getByText("2 selected", { exact: true })).toBeVisible()
    expect(
      screen.getByRole("button", { name: "Discuss with AI" }),
    ).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Draft issue previews" }),
    ).not.toBeInTheDocument()
  })

  it("renders complete finding provenance without an untracked posted transition", async () => {
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={finding.id}
        resourceKind="run"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(await screen.findByText(finding.commit_sha)).toBeVisible()
    expect(screen.getAllByText(finding.file.blob_sha).length).toBeGreaterThan(0)
    expect(screen.getAllByText("Traced both writers.").length).toBeGreaterThan(
      0,
    )
    expect(screen.getByText("alias reviewer")).toBeVisible()
    expect(screen.getByText("account openai-work")).toBeVisible()
    expect(screen.getByText("Model alias: reviewer")).toBeVisible()
    expect(screen.getByText("Account: openai-work")).toBeVisible()
    expect(
      screen.queryByRole("button", { name: /Mark posted/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Draft issue" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Link existing issue" }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Open repository finding" }),
    ).toBeVisible()
  })

  it("retries a failed status from run finding details", async () => {
    const failedFinding: RepositoryReviewFinding = {
      ...finding,
      repository_finding_id: undefined,
      repository_match_state: undefined,
      run_finding_status: "failed",
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding: failedFinding,
      contexts: [],
      capabilities: { github: true },
    })
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={failedFinding.id}
        resourceKind="run"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(await screen.findByText("Failed", { exact: true })).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Retry status" }))
    await waitFor(() =>
      expect(retryRepositoryReviewRunFindingStatuses).toHaveBeenCalledWith(
        automation.id,
        [failedFinding.id],
      ),
    )
  })

  it("applies optional presentation instructions to one issue preview", async () => {
    const onGenerated = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={repositoryFinding.id}
        resourceKind="repository"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={onGenerated}
        onOpenThread={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Draft issue" }))
    const dialog = screen.getByRole("dialog", { name: "Draft issue" })
    await user.type(
      within(dialog).getByRole("textbox", {
        name: "Custom presentation instructions",
      }),
      "Use a compact incident style.",
    )
    await user.click(
      within(dialog).getByRole("button", { name: "Draft preview" }),
    )

    await waitFor(() =>
      expect(generateRepositoryReviewIssues).toHaveBeenCalledWith(
        automation.id,
        expect.objectContaining({
          finding_ids: [finding.id],
          instructions_mode: "custom",
          instructions: "Use a compact incident style.",
        }),
      ),
    )
    expect(onGenerated).toHaveBeenCalledWith(expect.stringMatching(/^rig_/u))
  })

  it("stays on the finding when issue drafting saves no preview", async () => {
    vi.mocked(generateRepositoryReviewIssues).mockResolvedValue({
      generation_id: "rrig_failed",
      issues: [],
      results: [
        {
          id: finding.id,
          success: false,
          outcome: "failed",
          message: "The writer returned malformed output.",
        },
      ],
    })
    const onGenerated = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={repositoryFinding.id}
        resourceKind="repository"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={onGenerated}
        onOpenThread={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Draft issue" }))
    await user.click(screen.getByRole("button", { name: "Draft preview" }))

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Draft preview" }),
      ).toBeEnabled(),
    )
    expect(onGenerated).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog", { name: "Draft issue" })).toBeVisible()
  })

  it("opens a saved generation attempt even when its draft failed", async () => {
    vi.mocked(generateRepositoryReviewIssues).mockResolvedValue({
      generation_id: "rrig_failed_saved",
      issues: [],
      results: [
        {
          id: finding.id,
          draft_id: issue.id,
          success: false,
          outcome: "failed",
          message: "The saved attempt needs a retry.",
        },
      ],
    })
    const onGenerated = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={repositoryFinding.id}
        resourceKind="repository"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={onGenerated}
        onOpenThread={vi.fn()}
      />,
    )

    await user.click(await screen.findByRole("button", { name: "Draft issue" }))
    await user.click(screen.getByRole("button", { name: "Draft preview" }))

    await waitFor(() =>
      expect(onGenerated).toHaveBeenCalledWith(expect.stringMatching(/^rig_/u)),
    )
    expect(screen.queryByRole("dialog", { name: "Draft issue" })).toBeNull()
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
      action_finding: associatedFinding,
      repository_finding: repositoryFinding,
      occurrences: [associatedFinding],
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
        findingID={repositoryFinding.id}
        resourceKind="repository"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
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

  it("renders repository finding causal, effort, occurrence, and resolution projections", async () => {
    const aggregate: RepositoryFinding = {
      ...repositoryFinding,
      match_hints: {
        component: "storage",
        operation: "commit concurrent finding update",
        failure_mode: "a stale writer overwrites the current ledger",
        trigger: "two writers commit from one version",
        violated_invariant: "every write is fenced by the current version",
        observable_outcome: "a validated finding disappears",
        related_symbols: ["Store.Save"],
        source_anchors: ["expected_version"],
        distinguishing_facts: ["requires two concurrent writers"],
      },
      fix_effort: {
        quick: {
          loc_min: 5,
          loc_max: 20,
          class: "small",
          rationale: "Localized containment.",
        },
        quality: {
          loc_min: 30,
          loc_max: 100,
          class: "medium",
          rationale: "The ownership contract spans related units.",
        },
      },
      lifecycle: "resolved",
      validation_state: "confirmed",
      fix_commit_sha: "c".repeat(40),
      fix_commit_time: "2026-08-26T01:00:00Z",
      first_containing_tag: "v1.2.3",
      resolution_history: [
        {
          outcome: "confirmed",
          fix_commit_sha: "c".repeat(40),
          validated_at: "2026-08-26T02:00:00Z",
          first_containing_tag: "v1.2.3",
          summary: "The supplied commit restores the version invariant.",
        },
      ],
    }
    vi.mocked(getRepositoryReviewAutomationFinding).mockResolvedValue({
      automation,
      repository: repositorySummary,
      finding,
      repository_finding: aggregate,
      occurrences: [finding],
      contexts: [],
      capabilities: { github: true },
    })
    renderPage(
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={aggregate.id}
        resourceKind="repository"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
        onOpenIssue={vi.fn()}
        onLinkIssue={vi.fn()}
        onGenerated={vi.fn()}
        onOpenThread={vi.fn()}
      />,
    )

    expect(
      await screen.findByRole("heading", { name: "Repository lifecycle" }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Causal identity hints" }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Estimated fix effort" }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Occurrence history" }),
    ).toBeVisible()
    expect(
      screen.getByRole("heading", { name: "Resolution history" }),
    ).toBeVisible()
    expect(screen.getAllByText(/v1\.2\.3/).length).toBeGreaterThan(0)
    expect(
      screen.queryByRole("button", { name: "Discuss with AI" }),
    ).not.toBeInTheDocument()
  })

  it("rejects a run finding ID on the repository issue-link surface", async () => {
    renderPage(
      <RepositoryReviewLinkIssuePage
        automationID={automation.id}
        findingID={finding.id}
        onBack={vi.fn()}
        onLinked={vi.fn()}
      />,
    )

    expect(
      await screen.findByRole("heading", { name: "Item not found" }),
    ).toBeVisible()
    expect(findRepositoryReviewIssueCandidates).not.toHaveBeenCalled()
    expect(linkRepositoryReviewIssue).not.toHaveBeenCalled()
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
      action_finding: dismissedFinding,
      repository_finding: repositoryFinding,
      occurrences: [dismissedFinding],
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
        findingID={repositoryFinding.id}
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
      repository_finding: repositoryFinding,
      occurrences: [linkedFinding],
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
        findingID={repositoryFinding.id}
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
      repository_finding: repositoryFinding,
      occurrences: [linkedFinding],
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
        findingID={repositoryFinding.id}
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
        onEdit={vi.fn()}
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
    expect(screen.getByRole("button", { name: "Post issue" })).toBeVisible()
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
        onEdit={vi.fn()}
        onOpenFinding={onOpenFinding}
        onManageLink={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("button", {
        name: `Open finding ${finding.id}`,
      }),
    )
    expect(onOpenFinding).toHaveBeenCalledWith(finding.id)
  })

  it("manages a linked issue through its canonical repository finding", async () => {
    const linkedFinding = {
      ...finding,
      status: "posted" as const,
      issue_draft_id: linkedIssue.id,
    }
    vi.mocked(getRepositoryReviewAutomationIssue).mockResolvedValue({
      automation,
      repository: repositorySummary,
      issue: linkedIssue,
      finding: linkedFinding,
      capabilities: {
        github: true,
        can_unlink_issue: true,
        can_replace_issue: true,
      },
    })
    const onManageLink = vi.fn()
    const user = userEvent.setup()
    renderPage(
      <RepositoryReviewIssuePage
        automationID={automation.id}
        draftID={linkedIssue.id}
        onBack={vi.fn()}
        onDeleted={vi.fn()}
        onEdit={vi.fn()}
        onOpenFinding={vi.fn()}
        onManageLink={onManageLink}
      />,
    )

    await user.click(
      await screen.findByRole("button", { name: "Manage manual link" }),
    )
    expect(onManageLink).toHaveBeenCalledWith(repositoryFinding.id)
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
        onEdit={vi.fn()}
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
        onEdit={vi.fn()}
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
        onEdit={vi.fn()}
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
        onEdit={vi.fn()}
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
        onFindings={vi.fn()}
        onIssues={vi.fn()}
      />
    )
  }
  if (kind === "finding") {
    return (
      <RepositoryReviewFindingPage
        automationID={automation.id}
        findingID={finding.id}
        resourceKind="run"
        onBack={vi.fn()}
        onOpenRepositoryFinding={vi.fn()}
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
      onEdit={vi.fn()}
      onOpenFinding={vi.fn()}
      onManageLink={vi.fn()}
    />
  )
}

function FindingsPagingHarness() {
  const [search, setSearch] = useState<RepositoryReviewCollectionSearch>({
    q: "ORDER BY repository ASC",
  })
  return (
    <RepositoryReviewFindingsPage
      automationID={automation.id}
      search={search}
      onSearchChange={setSearch}
      onBack={vi.fn()}
      onOpenFinding={vi.fn()}
      onOpenRepositoryFindings={vi.fn()}
      onOpenRepositoryFinding={vi.fn()}
      onOpenThread={vi.fn()}
    />
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
