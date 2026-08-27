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

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi.fn().mockResolvedValue({
    authenticated: true,
    initialized: true,
  }),
}))

vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => (
    <div data-testid="app-layout">{children}</div>
  ),
}))

vi.mock("@/components/repository-reviews/repository-review-runs-page", () => ({
  RepositoryReviewRunsPage: ({
    onOpen,
  }: {
    onOpen: (item: { id: string }) => void
  }) => (
    <button type="button" onClick={() => onOpen({ id: "auto_1" })}>
      Open review
    </button>
  ),
}))

vi.mock(
  "@/components/repository-reviews/repository-review-detail-page",
  () => ({
    RepositoryReviewDetailPage: ({
      id,
      onFindings,
    }: {
      id: string
      onFindings: () => void
    }) => (
      <div>
        <output>Detail {id}</output>
        <button type="button" onClick={onFindings}>
          Open findings
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
      onOpenFinding,
    }: {
      automationID: string
      onOpenFinding: (id: string) => void
    }) => (
      <div>
        <output>Findings {automationID}</output>
        <button type="button" onClick={() => onOpenFinding("finding_1")}>
          Open finding
        </button>
      </div>
    ),
  }),
)

vi.mock(
  "@/components/repository-reviews/repository-review-finding-page",
  () => ({
    RepositoryReviewFindingPage: ({ findingID }: { findingID: string }) => (
      <output>Finding {findingID}</output>
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
  RepositoryReviewIssuePage: ({ draftID }: { draftID: string }) => (
    <output>Issue {draftID}</output>
  ),
}))

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
    RepositoryReviewRepositoriesPage: () => (
      <output>Repositories workspace</output>
    ),
  }),
)
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

describe("repository review routes", () => {
  it("opens detail and findings while preserving collection state", async () => {
    const router = testRouter(
      "/repository-reviews?q=status%20%3D%20running&view=grid",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await user.click(await screen.findByRole("button", { name: "Open review" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/repository-reviews/auto_1"),
    )
    expect(router.state.location.search).toMatchObject({
      q: "status = running",
      view: "grid",
      scope: "current",
      offset: 0,
    })
    await user.click(screen.getByRole("button", { name: "Open findings" }))
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toMatchObject({
      q: "status = running",
      view: "grid",
    })
  })

  it("redirects the old report route and preserves all findings state", async () => {
    const router = testRouter(
      "/repository-reviews/auto_1/report?q=severity%20%3D%20high&view=grid&scope=all&offset=50&generation_id=rig_1",
    )
    const user = userEvent.setup()
    render(<RouterProvider router={router} />)

    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings",
      ),
    )
    expect(router.state.location.search).toMatchObject({
      q: "severity = high",
      view: "grid",
      scope: "all",
      offset: 50,
      generation_id: "rig_1",
    })

    await user.click(
      await screen.findByRole("button", { name: "Open finding" }),
    )
    await waitFor(() =>
      expect(router.state.location.pathname).toBe(
        "/repository-reviews/auto_1/findings/finding_1",
      ),
    )
    expect(router.state.location.search).toMatchObject({
      q: "severity = high",
      view: "grid",
      scope: "all",
      offset: 50,
      generation_id: "rig_1",
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
      "/repository-reviews/auto_1/findings/finding_1/link-issue",
      "Link issue finding_1",
    ],
    ["/repository-reviews/auto_1/issues", "Issues auto_1"],
    ["/repository-reviews/auto_1/issues/draft_1", "Issue draft_1"],
    ["/repository-reviews/repositories", "Repositories workspace"],
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
