import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

vi.mock("@/api/repository-reviews", () => ({
  getRepositoryReviewRawSource: vi.fn(
    async (_automationID: string, sourceID: string) => ({
      source: { id: sourceID.startsWith("rfn_") ? "rrw_1" : sourceID },
    }),
  ),
}))

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi.fn().mockResolvedValue({
    authenticated: true,
    initialized: true,
  }),
}))

vi.mock(
  "@/components/repository-reviews/repository-review-repository-route",
  () => ({
    resolveRepositoryFindingRouteID: vi.fn(
      async (_automationID: string, findingID: string) =>
        findingID === "rfn_legacy" ? "rrf_1" : findingID,
    ),
  }),
)

vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => (
    <div data-testid="app-layout">{children}</div>
  ),
}))

vi.mock("@/components/repository-reviews/repository-review-runs-page", () => ({
  RepositoryReviewRunsPage: ({
    onOpen,
    onOpenRawFindings,
  }: {
    onOpen: (item: { id: string }) => void
    onOpenRawFindings: (item: { id: string }) => void
  }) => (
    <>
      <button type="button" onClick={() => onOpen({ id: "auto_1" })}>
        Open review
      </button>
      <button type="button" onClick={() => onOpenRawFindings({ id: "auto_1" })}>
        Open raw findings from review row
      </button>
    </>
  ),
}))

vi.mock(
  "@/components/repository-reviews/repository-review-detail-page",
  () => ({
    RepositoryReviewDetailPage: ({
      id,
      onFindings,
      onRawFindings,
    }: {
      id: string
      onFindings: () => void
      onRawFindings: () => void
    }) => (
      <div>
        <output>Detail {id}</output>
        <button type="button" onClick={onFindings}>
          Open findings
        </button>
        <button type="button" onClick={onRawFindings}>
          Open raw findings
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-findings-page",
  () => ({
    RepositoryReviewFindingsPage: ({
      automationID,
      onBack,
      onOpenFinding,
      onOpenRepositoryFindings,
      onOpenRepositoryFinding,
    }: {
      automationID: string
      onBack: () => void
      onOpenFinding: (id: string) => void
      onOpenRepositoryFindings: () => void
      onOpenRepositoryFinding: (id: string) => void
    }) => (
      <div>
        <output>Findings {automationID}</output>
        <button type="button" onClick={onBack}>
          Back from run findings
        </button>
        <button type="button" onClick={() => onOpenFinding("finding_1")}>
          Open finding
        </button>
        <button type="button" onClick={onOpenRepositoryFindings}>
          View repository findings
        </button>
        <button type="button" onClick={() => onOpenRepositoryFinding("rrf_1")}>
          Open repository finding
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-repository-findings-page",
  () => ({
    RepositoryReviewRepositoryFindingsPage: ({
      automationID,
      onBack,
      onOpenFinding,
    }: {
      automationID: string
      onBack: () => void
      onOpenFinding: (id: string) => void
    }) => (
      <div>
        <output>Repository findings {automationID}</output>
        <button type="button" onClick={onBack}>
          Back from findings
        </button>
        <button type="button" onClick={() => onOpenFinding("rrf_1")}>
          Open repository finding
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-raw-findings-page",
  () => ({
    RepositoryReviewRawFindingsPage: ({
      automationID,
      onBack,
      onOpenRawFinding,
    }: {
      automationID: string
      onBack: () => void
      onOpenRawFinding: (id: string) => void
    }) => (
      <div>
        <output>Raw findings {automationID}</output>
        <button type="button" onClick={onBack}>
          Back from raw findings
        </button>
        <button type="button" onClick={() => onOpenRawFinding("rrw_1")}>
          Open raw finding
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-raw-finding-page",
  () => ({
    RepositoryReviewRawFindingPage: ({ sourceID }: { sourceID: string }) => (
      <output>Raw finding {sourceID}</output>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-finding-page",
  () => ({
    RepositoryReviewFindingPage: ({
      findingID,
      resourceKind,
      onOpenRepositoryFinding,
      onLinkIssue,
    }: {
      findingID: string
      resourceKind: "run" | "repository"
      onOpenRepositoryFinding: (id: string) => void
      onLinkIssue: (id: string) => void
    }) => (
      <div>
        <output>
          {resourceKind === "repository" ? "Repository finding" : "Finding"}{" "}
          {findingID}
        </output>
        {resourceKind === "run" && (
          <button
            type="button"
            onClick={() => onOpenRepositoryFinding("rrf_1")}
          >
            Open linked repository finding
          </button>
        )}
        {resourceKind === "repository" && (
          <button type="button" onClick={() => onLinkIssue("rfn_1")}>
            Link existing issue
          </button>
        )}
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-link-issue-page",
  () => ({
    RepositoryReviewLinkIssuePage: ({ findingID }: { findingID: string }) => (
      <output>Link issue {findingID}</output>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-issues-page",
  () => ({
    RepositoryReviewIssuesPage: ({
      automationID,
    }: {
      automationID: string
    }) => <output>Issues {automationID}</output>,
  }),
)

vi.mock("@/components/repository-reviews/repository-review-issue-page", () => ({
  RepositoryReviewIssuePage: ({
    draftID,
    onEdit,
    onOpenFinding,
    onManageLink,
  }: {
    draftID: string
    onEdit: () => void
    onOpenFinding: (findingID: string) => void
    onManageLink: (findingID: string) => void
  }) => (
    <div>
      <output>Issue {draftID}</output>
      <button type="button" onClick={onEdit}>
        Edit issue
      </button>
      <button type="button" onClick={() => onOpenFinding("rfn_1")}>
        Open run finding
      </button>
      <button type="button" onClick={() => onManageLink("rrf_1")}>
        Manage repository link
      </button>
    </div>
  ),
}))

vi.mock(
  "@/components/repository-reviews/repository-review-issue-editor-page",
  () => ({
    RepositoryReviewIssueEditorPage: ({
      draftID,
      onBack,
    }: {
      draftID: string
      onBack: () => void
    }) => (
      <div>
        <output>Edit issue {draftID}</output>
        <button type="button" onClick={onBack}>
          Back from issue editor
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-profiles-page",
  () => ({
    repositoryReviewProfileDefaultQuery: "ALL ORDER BY name ASC",
    repositoryReviewProfileViews: ["list", "table", "grid"],
    RepositoryReviewProfilesPage: () => <output>Profiles workspace</output>,
    RepositoryReviewProfileDetailPage: ({
      profileID,
    }: {
      profileID: string
    }) => <output>Profile {profileID}</output>,
    RepositoryReviewProfileEditorPage: ({
      profileID,
    }: {
      profileID?: string
    }) => <output>{profileID ? `Edit ${profileID}` : "New profile"}</output>,
  }),
)
vi.mock(
  "@/components/repository-reviews/repository-review-repositories-page",
  () => ({
    RepositoryReviewRepositoriesPage: ({
      onAdd,
      onOpen,
      onEdit,
      onOpenFindings,
    }: {
      onAdd: () => void
      onOpen: (item: { id: string }) => void
      onEdit: (item: { id: string }) => void
      onOpenFindings: (item: { id: string }) => void
    }) => (
      <div>
        <output>Repositories workspace</output>
        <button type="button" onClick={onAdd}>
          Add repository
        </button>
        <button type="button" onClick={() => onOpen({ id: "auto_1" })}>
          Open repository configuration
        </button>
        <button type="button" onClick={() => onEdit({ id: "auto_1" })}>
          Edit repository from collection
        </button>
        <button type="button" onClick={() => onOpenFindings({ id: "auto_1" })}>
          Open findings from collection
        </button>
      </div>
    ),
    RepositoryReviewRepositoryDetailPage: ({
      automationID,
      onBack,
      onEdit,
      onFindings,
    }: {
      automationID: string
      onBack: () => void
      onEdit: () => void
      onFindings: () => void
    }) => (
      <div>
        <output>Repository configuration {automationID}</output>
        <button type="button" onClick={onBack}>
          Back from repository detail
        </button>
        <button type="button" onClick={onEdit}>
          Edit repository from detail
        </button>
        <button type="button" onClick={onFindings}>
          Open findings from detail
        </button>
      </div>
    ),
    RepositoryReviewRepositoryEditorPage: ({
      automationID,
      onBack,
    }: {
      automationID?: string
      onBack: () => void
    }) => (
      <div>
        <output>
          {automationID
            ? `Edit repository ${automationID}`
            : "New repository configuration"}
        </output>
        <button type="button" onClick={onBack}>
          Back from repository editor
        </button>
      </div>
    ),
  }),
)
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

describe("repository review routes", () => {
  it("keeps parent and run-finding collection state independent", async () => {
    const router = testRouter(
      "/repository-reviews?q=status%20%3D%20running&view=grid",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(await screen.findByRole("button", { name: "Open review" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/repository-reviews/auto_1"),
    )
    expect(router.state.location.search).toEqual({
      q: "status = running",
      view: "grid",
    })
    await user.click(screen.getByRole("button", { name: "Open findings" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })
    await user.click(
      screen.getByRole("button", { name: "Back from run findings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/repository-reviews/auto_1"),
    )
    expect(router.state.location.search).toEqual({
      q: "status = running",
      view: "grid",
    })
  })

  it("preserves raw finding collection state through detail and back navigation", async () => {
    const router = testRouter(
      "/repository-reviews?q=status%20%3D%20running&view=grid",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(await screen.findByRole("button", { name: "Open review" }))
    await user.click(
      await screen.findByRole("button", { name: "Open raw findings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/raw-findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY created DESC",
    })

    await user.click(screen.getByRole("button", { name: "Open raw finding" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/raw-findings/rrw_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY created DESC",
    })
    await router.history.back()
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/raw-findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY created DESC",
    })
    await user.click(
      screen.getByRole("button", { name: "Back from raw findings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/repository-reviews/auto_1"),
    )
    expect(router.state.location.search).toEqual({
      q: "status = running",
      view: "grid",
    })
  })

  it.each([
    [
      "/repository-reviews/auto_1/findings?view=unsupported&unknown=value",
      { q: "ALL ORDER BY severity DESC, updated DESC" },
    ],
    [
      "/repository-reviews/repositories/auto_1/findings?offset=50&unknown=value",
      { q: "ALL ORDER BY severity DESC, updated DESC" },
    ],
    [
      "/repository-reviews/auto_1/issues?generation_id=rig_1&scope=current&unknown=value",
      { q: "ALL ORDER BY updated DESC", generation_id: "rig_1" },
    ],
  ])(
    "canonicalizes the child collection address bar for %s",
    async (path, expected) => {
      const router = testRouter(path)
      render(<RouterProvider router={router} />)

      await waitFor(() =>
        expect(
          Object.fromEntries(
            new URLSearchParams(router.state.location.searchStr),
          ),
        ).toEqual(expected),
      )
    },
  )

  it("redirects an aggregate report URL to the repositories workspace and preserves safe state", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/report?q=severity%20%3D%20high&view=grid&scope=all&offset=50&generation_id=rig_1",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "severity = high",
      view: "grid",
    })

    await user.click(
      await screen.findByRole("button", { name: "Open repository finding" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "severity = high",
      view: "grid",
    })
  })

  it("keeps a current-scope legacy report on the run findings route", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/report?q=severity%20%3D%20high&view=list&scope=current&offset=50",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "severity = high",
      view: "list",
    })
  })

  it("canonicalizes an old aggregate findings URL into the repositories subtree", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings?q=severity%20%3D%20high&view=grid&scope=all&offset=50",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "severity = high",
      view: "grid",
    })
  })

  it("canonicalizes an old aggregate detail URL into the repositories subtree", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings/rrf_1?q=severity%20%3D%20high&scope=all&offset=50",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1",
      ),
    )
    expect(router.state.location.search).toEqual({ q: "severity = high" })
    expect(await screen.findByText("Repository finding rrf_1")).toBeVisible()
  })

  it("canonicalizes an old aggregate issue-link URL into the repositories subtree", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings/rrf_1/link-issue?q=severity%20%3D%20high&scope=all&offset=50",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1/link-issue",
      ),
    )
    expect(router.state.location.search).toEqual({ q: "severity = high" })
  })

  it.each([
    [
      "/repository-reviews/auto_1/findings/rfn_legacy?scope=all",
      "/repository-reviews/auto_1/raw-findings/rrw_1",
    ],
    [
      "/repository-reviews/auto_1/findings/rfn_legacy/link-issue?scope=all",
      "/repository-reviews/auto_1/raw-findings/rrw_1",
    ],
  ])(
    "resolves a legacy action occurrence URL into %s",
    async (path, target) => {
      const router = testRouter(path)
      render(<RouterProvider router={router} />)

      await waitFor(() => expect(router.state.location.pathname).toBe(target))
      expect(router.state.location.search).toEqual({
        q: "ALL ORDER BY created DESC",
      })
    },
  )

  it("returns an old current-scope issue-link URL to the run finding", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings/finding_1/link-issue?q=severity%20%3D%20high&scope=current&offset=50",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings/finding_1",
      ),
    )
    expect(router.state.location.search).toEqual({ q: "severity = high" })
    expect(await screen.findByText("Finding finding_1")).toBeVisible()
  })

  it("opens the canonical repository finding from a run finding", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings/finding_1?scope=current",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "ALL ORDER BY severity DESC, updated DESC",
      }),
    )

    await user.click(
      await screen.findByRole("button", {
        name: "Open linked repository finding",
      }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })
  })

  it("opens the repository finding collection from run findings", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/findings?q=severity%20%3D%20high&view=grid&scope=current&offset=50",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.search).toEqual({
        q: "severity = high",
        view: "grid",
      }),
    )

    await user.click(
      await screen.findByRole("button", { name: "View repository findings" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })
    await user.click(screen.getByRole("button", { name: "Back from findings" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ORDER BY repository ASC",
    })
  })

  it("keeps the canonical repository finding ID while opening issue linking", async () => {
    const router = testRouter(
      "/repository-reviews/repositories/auto_1/findings/rrf_1?scope=all",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(
      await screen.findByRole("button", { name: "Link existing issue" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1/link-issue",
      ),
    )
  })

  it("uses resource-owned routes from an issue preview opened with repository state", async () => {
    const runRouter = testRouter(
      "/repository-reviews/auto_1/issues/draft_1?scope=all&offset=50",
    )
    const user = userEvent.setup()
    const runView = render(<RouterProvider router={runRouter} />)

    await user.click(
      await screen.findByRole("button", { name: "Open run finding" }),
    )
    await waitFor(() =>
      expect(runRouter.state.location.pathname).toBe(
        "/repository-reviews/auto_1/raw-findings/rrw_1",
      ),
    )
    expect(runRouter.state.location.search).toEqual({
      q: "ALL ORDER BY created DESC",
    })
    runView.unmount()

    const repositoryRouter = testRouter(
      "/repository-reviews/auto_1/issues/draft_1?scope=all&offset=50",
    )
    render(<RouterProvider router={repositoryRouter} />)
    await user.click(
      await screen.findByRole("button", { name: "Manage repository link" }),
    )
    await waitFor(() =>
      expect(repositoryRouter.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings/rrf_1/link-issue",
      ),
    )
    expect(repositoryRouter.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })
  })

  it("opens the dedicated issue editor and preserves issue collection state", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/issues/draft_1?q=state%20%3D%20editing&view=grid&generation_id=rig_1",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(await screen.findByRole("button", { name: "Edit issue" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/issues/draft_1/edit",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "state = editing",
      view: "grid",
      generation_id: "rig_1",
    })

    await user.click(
      screen.getByRole("button", { name: "Back from issue editor" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/issues/draft_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "state = editing",
      view: "grid",
      generation_id: "rig_1",
    })
  })

  it("navigates through repository detail, edit, and findings without losing collection state", async () => {
    const router = testRouter(
      "/repository-reviews/repositories?q=status%20%3D%20paused&view=grid",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(
      await screen.findByRole("button", {
        name: "Open repository configuration",
      }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = paused",
      view: "grid",
    })

    await user.click(
      screen.getByRole("button", { name: "Edit repository from detail" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/edit",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = paused",
      view: "grid",
    })

    await user.click(
      screen.getByRole("button", { name: "Back from repository editor" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = paused",
      view: "grid",
    })

    await user.click(
      screen.getByRole("button", { name: "Open findings from detail" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })

    await user.click(screen.getByRole("button", { name: "Back from findings" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories",
      ),
    )
    expect(router.state.location.search).toEqual({
      q: "status = paused",
      view: "grid",
    })
  })

  it("opens repository edit and findings routes directly from the collection", async () => {
    const editRouter = testRouter(
      "/repository-reviews/repositories?q=repository%20~%20picoclaw&view=table",
    )
    const user = userEvent.setup()
    const editView = render(<RouterProvider router={editRouter} />)

    await user.click(
      await screen.findByRole("button", {
        name: "Edit repository from collection",
      }),
    )
    await waitFor(() =>
      expect(editRouter.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/edit",
      ),
    )
    expect(editRouter.state.location.search).toMatchObject({
      q: "repository ~ picoclaw",
      view: "table",
    })
    editView.unmount()

    const findingsRouter = testRouter(
      "/repository-reviews/repositories?q=repository%20~%20picoclaw&view=table",
    )
    render(<RouterProvider router={findingsRouter} />)
    await user.click(
      await screen.findByRole("button", {
        name: "Open findings from collection",
      }),
    )
    await waitFor(() =>
      expect(findingsRouter.state.location.pathname).toBe(
        "/repository-reviews/repositories/auto_1/findings",
      ),
    )
    expect(findingsRouter.state.location.search).toEqual({
      q: "ALL ORDER BY severity DESC, updated DESC",
    })
    await user.click(screen.getByRole("button", { name: "Back from findings" }))
    await waitFor(() =>
      expect(findingsRouter.state.location.pathname).toBe(
        "/repository-reviews/repositories",
      ),
    )
    expect(findingsRouter.state.location.search).toEqual({
      q: "repository ~ picoclaw",
      view: "table",
    })
  })

  it("returns from new repository configuration with collection state intact", async () => {
    const router = testRouter(
      "/repository-reviews/repositories?q=status%20%3D%20idle&view=list",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(
      await screen.findByRole("button", { name: "Add repository" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories/new",
      ),
    )
    expect(router.state.location.search).toMatchObject({
      q: "status = idle",
      view: "list",
    })

    await user.click(
      screen.getByRole("button", { name: "Back from repository editor" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/repositories",
      ),
    )
    expect(router.state.location.search).toMatchObject({
      q: "status = idle",
      view: "list",
    })
  })

  it("redirects the legacy Results URL to the collection", async () => {
    const router = testRouter(
      "/repository-reviews/results?q=status%20%3D%20paused&view=table",
    )
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/repository-reviews"),
    )
    expect(router.state.location.search).toEqual({
      q: "status = paused",
      view: "table",
    })
  })

  it.each([
    [
      "/repository-reviews/auto_1/findings/finding_1?scope=current",
      "Finding finding_1",
    ],
    ["/repository-reviews/auto_1/issues", "Issues auto_1"],
    ["/repository-reviews/auto_1/issues/draft_1", "Issue draft_1"],
    ["/repository-reviews/repositories", "Repositories workspace"],
    ["/repository-reviews/repositories/new", "New repository configuration"],
    [
      "/repository-reviews/repositories/auto_1",
      "Repository configuration auto_1",
    ],
    ["/repository-reviews/repositories/auto_1/edit", "Edit repository auto_1"],
    [
      "/repository-reviews/repositories/auto_1/findings",
      "Repository findings auto_1",
    ],
    [
      "/repository-reviews/repositories/auto_1/findings/rrf_1",
      "Repository finding rrf_1",
    ],
    [
      "/repository-reviews/repositories/auto_1/findings/rrf_1/link-issue",
      "Link issue rrf_1",
    ],
    ["/repository-reviews/profiles", "Profiles workspace"],
    ["/repository-reviews/profiles/new", "New profile"],
    ["/repository-reviews/profiles/profile_1", "Profile profile_1"],
    ["/repository-reviews/profiles/profile_1/edit", "Edit profile_1"],
    ["/repository-reviews/profiles/new", "New profile"],
    ["/repository-reviews/profiles/profile_1", "Profile profile_1"],
    ["/repository-reviews/profiles/profile_1/edit", "Edit profile_1"],
  ])("directly renders %s", async (path, text) => {
    const router = testRouter(path)
    render(<RouterProvider router={router} />)
    expect(await screen.findByText(text)).toBeVisible()
  })
})

function testRouter(initialEntry: string) {
  return createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [initialEntry] }),
    context: {
      queryClient: new QueryClient({
        defaultOptions: { queries: { retry: false } },
      }),
    },
  })
}
