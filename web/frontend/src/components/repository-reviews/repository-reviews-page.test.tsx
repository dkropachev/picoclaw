import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type {
  RepositoryReviewIssueDraft,
  RepositoryReviewState,
  RepositoryReviewSummary,
} from "@/api/repository-reviews"
import {
  createRepositoryReviewIssueDraft,
  getRepositoryReview,
  listRepositoryReviews,
  publishRepositoryReviewIssueDraft,
  updateRepositoryReviewFinding,
  updateRepositoryReviewIssueDraft,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import {
  discussionPrompt,
  githubNewIssueURL,
} from "@/components/repository-reviews/repository-review-actions"
import { RepositoryReviewsPage } from "@/components/repository-reviews/repository-reviews-page"
import { SidebarProvider } from "@/components/ui/sidebar"
import { switchChatSessionAndSend } from "@/features/chat/controller"

vi.mock("@/api/repository-reviews", () => ({
  createRepositoryReviewIssueDraft: vi.fn(),
  getRepositoryReview: vi.fn(),
  listRepositoryReviews: vi.fn(),
  publishRepositoryReviewIssueDraft: vi.fn(),
  updateRepositoryReviewFinding: vi.fn(),
  updateRepositoryReviewIssueDraft: vi.fn(),
}))

vi.mock("@/api/threads", () => ({ createThread: vi.fn(), dropThread: vi.fn() }))

vi.mock("@/features/chat/controller", () => ({
  switchChatSessionAndSend: vi.fn(),
}))

vi.mock(
  "@/components/repository-reviews/repository-review-control-center",
  () => ({
    RepositoryReviewControlCenter: () => (
      <div data-testid="repository-review-control-center" />
    ),
  }),
)

const contextID = "rctx_context_1"
const state: RepositoryReviewState = {
  schema_version: 1,
  id: "rrp_repository_1",
  repository: "owner/repo",
  version: 7,
  review_version: 3,
  last_commit_sha: "commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  files: {},
  unsupported: {},
  findings: [
    {
      id: "rfn_finding_1",
      fingerprint: "sha256:finding-1",
      repository: "owner/repo",
      commit_sha: "commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      file: {
        path: "pkg/service.go",
        blob_sha: "blob-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        size_bytes: 1536,
        category: "source",
      },
      line: 42,
      severity: "high",
      title: "Lost update",
      message: "Concurrent writers can overwrite review state.",
      evidence: "The write lacks a version fence.",
      impact: "A completed review can disappear.",
      recommendation: "Use compare-and-swap.",
      validation: {
        status: "confirmed",
        summary: "Reproduced with two writers.",
        checks: ["race test"],
      },
      context_ids: [contextID],
      models: ["review-model-a", "review-model-b"],
      observation_count: 3,
      status: "open",
      version: 1,
      created_at: "2026-08-20T12:00:00Z",
      updated_at: "2026-08-20T12:00:00Z",
    },
    {
      id: "rfn_finding_2",
      fingerprint: "sha256:finding-2",
      repository: "owner/repo",
      commit_sha: "commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      file: {
        path: "pkg/store.go",
        blob_sha: "blob-cccccccccccccccccccccccccccccccccccc",
        size_bytes: 512,
      },
      severity: "medium",
      title: "Retry drops context",
      message: "The retry path loses the source snapshot.",
      evidence: "The context ID is not copied.",
      impact: "Validation cannot be recovered.",
      recommendation: "Retain the opaque context reference.",
      validation: {
        status: "confirmed",
        summary: "Observed after a retry.",
      },
      context_ids: [contextID],
      models: ["review-model-b"],
      observation_count: 1,
      status: "open",
      version: 1,
      created_at: "2026-08-20T12:00:00Z",
      updated_at: "2026-08-20T12:00:00Z",
    },
  ],
  contexts: [
    {
      id: contextID,
      repository: "owner/repo",
      commit_sha: "commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      inventory_hash: "sha256:inventory",
      profile_hash: "sha256:review-profile",
      run_id: "review-run-1",
      model: "review-model-a",
      reviewer: "reviewer-a",
      files: [
        {
          path: "pkg/service.go",
          blob_sha: "blob-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
          size_bytes: 1536,
        },
        {
          path: "pkg/store.go",
          blob_sha: "blob-cccccccccccccccccccccccccccccccccccc",
          size_bytes: 512,
        },
      ],
      raw_digest: "sha256:raw",
      created_at: "2026-08-20T12:00:00Z",
    },
  ],
  runs: [
    {
      id: "review-run-1",
      plan_id: "review-plan-1",
      commit_sha: "commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      inventory_hash: "sha256:inventory",
      reviewed_files: 2,
      unreviewed_files: 0,
      unsupported_files: 0,
      remaining_files: 0,
      unsupported_paths: [],
      skipped_files: 4,
      accepted_findings: 2,
      rejected_findings: 0,
      models: ["review-model-a", "review-model-b"],
      completed_at: "2026-08-20T12:00:00Z",
    },
  ],
  issue_drafts: [],
  updated_at: "2026-08-20T12:00:00Z",
}

const stateSummary: RepositoryReviewSummary = {
  schema_version: state.schema_version,
  id: state.id,
  repository: state.repository,
  version: state.version,
  review_version: state.review_version,
  last_commit_sha: state.last_commit_sha,
  finding_count: state.findings.length,
  open_finding_count: state.findings.length,
  issue_draft_count: 0,
  unsupported_count: 0,
  reviewed_file_count: 0,
  updated_at: state.updated_at,
}

const draft: RepositoryReviewIssueDraft = {
  id: "rid_draft_1",
  repository: "owner/repo",
  finding_ids: ["rfn_finding_1", "rfn_finding_2"],
  title: "Repository review: 2 validated bugs",
  body: "Issue body from the selected findings.",
  labels: ["bug"],
  state: "editing",
  version: 1,
  created_at: "2026-08-20T12:01:00Z",
  updated_at: "2026-08-20T12:01:00Z",
}

describe("RepositoryReviewsPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "scrollTo", {
      writable: true,
      value: vi.fn(),
    })
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.mocked(listRepositoryReviews).mockReset()
    vi.mocked(getRepositoryReview).mockReset()
    vi.mocked(updateRepositoryReviewFinding).mockReset()
    vi.mocked(createRepositoryReviewIssueDraft).mockReset()
    vi.mocked(updateRepositoryReviewIssueDraft).mockReset()
    vi.mocked(publishRepositoryReviewIssueDraft).mockReset()
    vi.mocked(createThread).mockReset()
    vi.mocked(dropThread).mockReset()
    vi.mocked(switchChatSessionAndSend).mockReset()
    vi.mocked(listRepositoryReviews).mockResolvedValue({
      repositories: [state],
    })
    vi.mocked(getRepositoryReview).mockResolvedValue(state)
    vi.mocked(updateRepositoryReviewFinding).mockResolvedValue({
      repository: stateSummary,
      finding: state.findings[0],
    })
    vi.mocked(createThread).mockResolvedValue({
      id: "thread-review",
      ui_session_id: "session-review",
      title: "Review findings",
      preview: "",
      type: "reviewing",
      message_count: 0,
      created: "2026-08-20T12:02:00Z",
      updated: "2026-08-20T12:02:00Z",
    })
    vi.mocked(switchChatSessionAndSend).mockResolvedValue(true)
    vi.mocked(dropThread).mockResolvedValue({
      id: "thread-review",
      title: "Review findings",
      preview: "",
      type: "reviewing",
      message_count: 0,
      created: "2026-08-20T12:02:00Z",
      updated: "2026-08-20T12:02:00Z",
    })
    vi.mocked(createRepositoryReviewIssueDraft).mockResolvedValue({
      repository: { ...stateSummary, version: 8, issue_draft_count: 1 },
      draft,
    })
    vi.mocked(updateRepositoryReviewIssueDraft).mockResolvedValue({
      repository: { ...stateSummary, version: 9, issue_draft_count: 1 },
      draft: { ...draft, version: 2 },
    })
    vi.mocked(publishRepositoryReviewIssueDraft).mockResolvedValue({
      repository: {
        ...stateSummary,
        version: 9,
        open_finding_count: 0,
        issue_draft_count: 1,
      },
      draft: {
        ...draft,
        state: "posted",
        version: 2,
        external_url: "https://github.com/owner/repo/issues/12",
      },
    })
    vi.stubGlobal("open", vi.fn())
  })

  it("keeps review setup visible before the first ledger completes", async () => {
    vi.mocked(listRepositoryReviews).mockResolvedValue({ repositories: [] })
    renderPage(vi.fn())

    expect(
      await screen.findByText("No repository review has completed yet."),
    ).toBeVisible()
    expect(screen.getByTestId("repository-review-control-center")).toBeVisible()
    expect(getRepositoryReview).not.toHaveBeenCalled()
  })

  it("shows exact hashes, validation, model consensus, and context files", async () => {
    renderPage(vi.fn())

    expect(await screen.findByText("Lost update")).toBeVisible()
    expect(
      screen.getAllByText("commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")[0],
    ).toBeVisible()
    expect(
      screen.getAllByText("blob-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")[0],
    ).toBeVisible()
    expect(screen.getAllByText(/1.50 KiB \(1536 bytes\)/)[0]).toBeVisible()
    expect(screen.getByText("Reproduced with two writers.")).toBeVisible()
    expect(screen.getByText("race test")).toBeVisible()
    expect(screen.getAllByText("review-model-a")[0]).toBeVisible()
    expect(
      screen.getByText(
        "Consensus: 2 model contributors · 3 validated observations",
      ),
    ).toBeVisible()

    await userEvent.click(screen.getAllByText("Context file references (1)")[0])
    expect(screen.getAllByText(contextID)[0]).toBeVisible()
    expect(screen.getAllByText("pkg/store.go")[0]).toBeVisible()
    expect(screen.getAllByText("sha256:review-profile")[0]).toBeVisible()
  })

  it("starts one reviewing thread for one or many selected findings", async () => {
    const user = userEvent.setup()
    const onOpenThread = vi.fn()
    renderPage(onOpenThread)

    await user.click(await screen.findByLabelText("Select Lost update"))
    await user.click(screen.getByLabelText("Select Retry drops context"))
    await user.click(screen.getByRole("button", { name: "Discuss with AI" }))

    await waitFor(() => {
      expect(createThread).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "reviewing",
          context: expect.objectContaining({
            repository_review: state.id,
            finding_ids: "rfn_finding_1,rfn_finding_2",
            context_ids: contextID,
          }),
        }),
      )
    })
    await waitFor(() =>
      expect(switchChatSessionAndSend).toHaveBeenCalledTimes(2),
    )
    expect(switchChatSessionAndSend).toHaveBeenCalledWith("session-review", {
      content: expect.stringContaining("Finding rfn_finding_1: Lost update"),
    })
    expect(
      vi.mocked(switchChatSessionAndSend).mock.calls[0]?.[1].content,
    ).toContain(`Context IDs: ${contextID}`)
    const prompt = vi.mocked(switchChatSessionAndSend).mock.calls[0]?.[1]
      .content
    expect(prompt).toContain("Profile hash: sha256:review-profile")
    expect(prompt).toContain(
      "Finding commit SHA: commit-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    )
    expect(prompt).toContain("Impact: A completed review can disappear.")
    expect(prompt).toContain("Recommendation: Use compare-and-swap.")
    expect(prompt).toContain("Validation checks: race test")
    expect(prompt).toContain(
      "pkg/service.go | blob blob-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb | 1536 bytes",
    )
    expect(prompt).toContain(
      "pkg/store.go | blob blob-cccccccccccccccccccccccccccccccccccc | 512 bytes",
    )
    expect(onOpenThread).toHaveBeenCalledWith("session-review")
  })

  it("does not navigate to an empty discussion when chat startup fails", async () => {
    const user = userEvent.setup()
    const onOpenThread = vi.fn()
    vi.mocked(switchChatSessionAndSend).mockResolvedValue(false)
    renderPage(onOpenThread)
    await user.click(await screen.findByLabelText("Select Lost update"))
    await user.click(screen.getByRole("button", { name: "Discuss with AI" }))
    await waitFor(() =>
      expect(dropThread).toHaveBeenCalledWith("thread-review"),
    )
    expect(onOpenThread).not.toHaveBeenCalled()
    expect(
      screen.getByText("The finding discussion could not be started."),
    ).toBeVisible()
  })

  it("prepares, edits, and opens a selected finding issue on GitHub", async () => {
    const user = userEvent.setup()
    renderPage(vi.fn())

    await user.click(await screen.findByLabelText("Select Lost update"))
    await user.click(screen.getByLabelText("Select Retry drops context"))
    await user.click(screen.getByRole("button", { name: "Prepare issue" }))

    await waitFor(() => {
      expect(createRepositoryReviewIssueDraft).toHaveBeenCalledWith(state.id, {
        finding_ids: ["rfn_finding_1", "rfn_finding_2"],
        expected_version: state.version,
      })
    })

    const editor = await screen.findByTestId("repository-review-selection")
    expect(editor).toBeVisible()
    const title = screen.getByLabelText(`Issue title for ${draft.id}`)
    await user.clear(title)
    await user.type(title, "Two repository bugs")
    await user.click(screen.getByRole("button", { name: "Save draft" }))

    await waitFor(() => {
      expect(updateRepositoryReviewIssueDraft).toHaveBeenCalledWith(
        state.id,
        draft.id,
        {
          title: "Two repository bugs",
          body: draft.body,
          labels: ["bug"],
          expected_version: draft.version,
        },
      )
    })

    await user.click(screen.getByRole("button", { name: "Open in GitHub" }))
    expect(window.open).toHaveBeenCalledWith(
      expect.stringMatching(
        /^https:\/\/github\.com\/owner\/repo\/issues\/new\?/u,
      ),
      "_blank",
      "noopener,noreferrer",
    )
  })

  it("prepares before the immediate post action", async () => {
    const user = userEvent.setup()
    renderPage(vi.fn())
    await user.click(await screen.findByLabelText("Select Lost update"))
    await user.click(screen.getByRole("button", { name: "Post now" }))

    await waitFor(() =>
      expect(publishRepositoryReviewIssueDraft).toHaveBeenCalledWith(
        state.id,
        draft.id,
        { expected_version: draft.version },
      ),
    )
    expect(createRepositoryReviewIssueDraft).toHaveBeenCalledBefore(
      vi.mocked(publishRepositoryReviewIssueDraft),
    )
    await waitFor(() =>
      expect(window.open).toHaveBeenCalledWith(
        "https://github.com/owner/repo/issues/12",
        "_blank",
        "noopener,noreferrer",
      ),
    )
  })

  it("hides the immediate post action for non-owner/repo identities", async () => {
    const nonCanonical = {
      ...state,
      id: "rrp_local",
      repository: "https://github.com/owner/repo",
      issue_drafts: [draft],
    }
    vi.mocked(listRepositoryReviews).mockResolvedValue({
      repositories: [nonCanonical],
    })
    vi.mocked(getRepositoryReview).mockResolvedValue(nonCanonical)
    renderPage(vi.fn())
    expect(await screen.findByText("Lost update")).toBeVisible()
    expect(screen.queryByRole("button", { name: "Post now" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Publish issue" })).toBeNull()
  })

  it("changes finding status with the repository version fence", async () => {
    const user = userEvent.setup()
    renderPage(vi.fn())
    const card = (await screen.findByText("Lost update")).closest(
      "[data-finding-id]",
    )
    expect(card).not.toBeNull()
    await user.click(
      within(card as HTMLElement).getByRole("button", { name: "Dismiss" }),
    )
    expect(updateRepositoryReviewFinding).toHaveBeenCalledWith(
      state.id,
      "rfn_finding_1",
      { status: "dismissed", expected_version: state.version },
    )
  })

  it("loads the next bounded finding page while retaining aggregate totals", async () => {
    const user = userEvent.setup()
    const firstPage: RepositoryReviewState = {
      ...state,
      finding_count: 52,
      finding_offset: 0,
      finding_total: 52,
      next_finding_offset: 50,
    }
    const secondPage: RepositoryReviewState = {
      ...firstPage,
      findings: [state.findings[1]],
      finding_offset: 50,
      next_finding_offset: undefined,
    }
    vi.mocked(getRepositoryReview).mockImplementation(
      (_repositoryID, _signal, options) =>
        Promise.resolve(options?.offset === 50 ? secondPage : firstPage),
    )

    renderPage(vi.fn())
    expect(await screen.findByText("Lost update")).toBeVisible()
    expect(screen.getByText("1–50 of 52")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Next findings" }))

    await waitFor(() =>
      expect(getRepositoryReview).toHaveBeenCalledWith(
        state.id,
        expect.anything(),
        { offset: 50, limit: 50, draftOffset: 0, draftLimit: 10 },
      ),
    )
    expect(await screen.findByText("51–52 of 52")).toBeVisible()
    expect(screen.getAllByText("52").length).toBeGreaterThan(0)
  })

  it("pages older issue drafts independently from finding pages", async () => {
    const user = userEvent.setup()
    const drafts = Array.from({ length: 12 }, (_, index) => ({
      ...draft,
      id: `rid_${index}`,
      title: `Draft ${index}`,
    }))
    vi.mocked(getRepositoryReview).mockImplementation(
      (_repositoryID, _signal, options) =>
        Promise.resolve({
          ...state,
          issue_draft_count: 12,
          issue_drafts:
            options?.draftOffset === 10 ? drafts.slice(0, 2) : drafts.slice(2),
          draft_offset: options?.draftOffset ?? 0,
          draft_total: 12,
          next_draft_offset: options?.draftOffset === 10 ? undefined : 10,
        }),
    )

    renderPage(vi.fn())
    expect(await screen.findByText("Draft 11")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Next issue drafts" }))

    await waitFor(() =>
      expect(getRepositoryReview).toHaveBeenCalledWith(
        state.id,
        expect.anything(),
        { offset: 0, limit: 50, draftOffset: 10, draftLimit: 10 },
      ),
    )
    expect(await screen.findByText("Draft 1")).toBeVisible()
    expect(screen.queryByText("Draft 11")).toBeNull()
  })
})

describe("githubNewIssueURL", () => {
  it("accepts only owner/repo identities", () => {
    expect(githubNewIssueURL("owner/repo", draft)).toContain(
      "https://github.com/owner/repo/issues/new?",
    )
    expect(githubNewIssueURL("https://github.com/owner/repo", draft)).toBe(
      undefined,
    )
    expect(githubNewIssueURL("owner/repo/extra", draft)).toBe(undefined)
  })
})

describe("discussionPrompt", () => {
  it("keeps one maximum-provenance finding discussable with complete manifests", () => {
    const longPath = `${"segment/".repeat(500)}file.go`
    const contexts = Array.from({ length: 64 }, (_, index) => ({
      ...state.contexts[0],
      id: `context-${index}`,
      files: Array.from({ length: 3 }, (__, fileIndex) => ({
        path: `${index}-${fileIndex}-${longPath}`,
        blob_sha: "a".repeat(40),
        size_bytes: 12,
      })),
    }))
    const finding = {
      ...state.findings[0],
      title: "t".repeat(64 << 10),
      evidence: "e".repeat(64 << 10),
      impact: "i".repeat(64 << 10),
      recommendation: "r".repeat(64 << 10),
      context_ids: contexts.map((context) => context.id),
    }
    const prompt = discussionPrompt(
      { ...state, findings: [finding], contexts },
      [finding],
    )
    expect(new TextEncoder().encode(prompt).byteLength).toBeLessThanOrEqual(
      2 << 20,
    )
    expect(prompt).toContain(contexts[63].files[2].path)
  })
})

function renderPage(onOpenThread: (threadID: string) => void) {
  const client = testClient()
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <RepositoryReviewsPage onOpenThread={onOpenThread} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
}

function testClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
}
