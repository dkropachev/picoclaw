import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  cleanupGitWorkspace,
  dropGitWorkspace,
  getGitWorkspaces,
  reconcileGitWorkspaces,
} from "@/api/git-workspaces"
import { launcherFetch } from "@/api/http"

vi.mock("@/api/http", () => ({
  launcherFetch: vi.fn(),
}))

const mockedLauncherFetch = vi.mocked(launcherFetch)

describe("git workspace API", () => {
  beforeEach(() => {
    mockedLauncherFetch.mockReset()
  })

  it("lists git workspace stats", async () => {
    mockedLauncherFetch.mockResolvedValueOnce(
      jsonResponse({
        root_dir: "/tmp/git-workspaces",
        max_total_size_bytes: 1024,
        ignored_cleanup_delay_seconds: 3600,
        drop_delay_seconds: 86400,
        total_size_bytes: 128,
        ignored_bytes: 64,
        repository_count: 1,
        workspace_count: 1,
        locked_workspace_count: 0,
        repositories: [],
        workspaces: [],
        history: [],
      }),
    )

    await expect(getGitWorkspaces()).resolves.toMatchObject({
      root_dir: "/tmp/git-workspaces",
      total_size_bytes: 128,
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/git-workspaces",
      undefined,
    )
  })

  it("sends maintenance, cleanup, and drop requests", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(
        jsonResponse({ cleaned: [], dropped: [], stats: {} }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          before_ignored_bytes: 64,
          after_ignored_bytes: 0,
          workspace: { id: "gw-1" },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ workspace: { id: "gw-1" } }))

    await reconcileGitWorkspaces()
    await cleanupGitWorkspace("gw-1")
    await dropGitWorkspace("gw-1")

    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      1,
      "/api/git-workspaces/reconcile",
      { method: "POST" },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      2,
      "/api/git-workspaces/cleanup",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ workspace_id: "gw-1" }),
      },
    )
    expect(mockedLauncherFetch).toHaveBeenNthCalledWith(
      3,
      "/api/git-workspaces/gw-1",
      { method: "DELETE" },
    )
  })
})

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  })
}
