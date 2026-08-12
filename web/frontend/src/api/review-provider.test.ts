import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  ReviewProviderAPIError,
  getReviewProviderSnapshot,
  getReviewProviderStatus,
  mutateReviewProviderThread,
} from "@/api/review-provider"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)
const caseID = `prc_${"1".repeat(32)}`
const token = `rtt_${"a".repeat(43)}`

const snapshot = {
  availability: "available",
  connector: "primary",
  repository: "octo/repo",
  pull_number: 42,
  pull_request: {
    number: 42,
    title: "Keep live review state",
    state: "open",
    url: "https://github.com/octo/repo/pull/42",
    author: "author",
    draft: false,
    merged: false,
    updated_at: "2026-08-12T12:00:00Z",
  },
  capabilities: { thread_resolution: true },
  reviews: [
    {
      id: "101",
      state: "CHANGES_REQUESTED",
      body: "Please preserve the concurrency check.",
      url: "https://github.com/octo/repo/pull/42#pullrequestreview-101",
      author: "reviewer",
      commit_id: "a".repeat(40),
      submitted_at: "2026-08-12T11:30:00Z",
    },
  ],
  review_history_complete: true,
  threads_complete: true,
  limitations: [],
  threads: [
    {
      token,
      is_resolved: false,
      is_outdated: false,
      is_collapsed: false,
      can_resolve: true,
      total_count: 1,
      comments: [
        {
          body: "This can lose a concurrent update.",
          path: "pkg/store.go",
          line: 72,
          author: "reviewer",
          created_at: "2026-08-12T11:31:00Z",
          url: "https://github.com/octo/repo/pull/42#discussion_r1",
        },
      ],
    },
  ],
} as const

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("review provider API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("reads an encoded case-scoped live snapshot with bounded provider details", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(snapshot))

    await expect(getReviewProviderSnapshot("prc_/unsafe")).resolves.toEqual(
      snapshot,
    )
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/reviews/prc_%2Funsafe/provider",
      {},
    )
  })

  it("requests the cheap status-only projection through the same validator", async () => {
    const status = {
      availability: "available",
      connector: snapshot.connector,
      repository: snapshot.repository,
      pull_number: snapshot.pull_number,
      pull_request: snapshot.pull_request,
      capabilities: { thread_resolution: false },
      limitations: ["status_view"],
    }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(status))

    await expect(getReviewProviderStatus(caseID)).resolves.toEqual(status)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/reviews/${caseID}/provider?view=status`,
      undefined,
    )
  })

  it("keeps bounded status limitations when the provider is incompatible", async () => {
    const status = {
      availability: "incompatible",
      connector: snapshot.connector,
      repository: snapshot.repository,
      pull_number: snapshot.pull_number,
      capabilities: { thread_resolution: false },
      limitations: ["status_view", "provider_response_incompatible"],
    }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(status))

    await expect(getReviewProviderStatus(caseID)).resolves.toEqual(status)
  })

  it("rejects full-snapshot fields in the status-only projection", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        availability: "available",
        connector: snapshot.connector,
        repository: snapshot.repository,
        pull_number: snapshot.pull_number,
        pull_request: snapshot.pull_request,
        capabilities: { thread_resolution: false },
        limitations: ["status_view"],
        reviews: [],
      }),
    )

    await expect(getReviewProviderStatus(caseID)).rejects.toMatchObject({
      code: "invalid_provider_snapshot",
    })
  })

  it("accepts an explicitly partial snapshot without inventing thread authority", async () => {
    const partial = {
      ...snapshot,
      availability: "partial",
      capabilities: { thread_resolution: false },
      review_history_complete: false,
      threads_complete: false,
      limitations: [
        "review_history_pagination_stalled",
        "thread_identity_unavailable",
      ],
      threads: snapshot.threads.map((thread) => ({
        is_resolved: thread.is_resolved,
        is_outdated: thread.is_outdated,
        is_collapsed: thread.is_collapsed,
        can_resolve: false,
        total_count: thread.total_count,
        comments: thread.comments,
      })),
    }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(partial))

    await expect(getReviewProviderSnapshot(caseID)).resolves.toEqual(partial)
  })

  it("posts only the opaque token and exact resolve action, then validates the refreshed snapshot", async () => {
    const resolved = {
      ...snapshot,
      threads: snapshot.threads.map((thread) => ({
        ...thread,
        is_resolved: true,
      })),
    }
    mockedLauncherFetch.mockResolvedValue(jsonResponse(resolved))

    await expect(
      mutateReviewProviderThread(caseID, token, "resolve"),
    ).resolves.toEqual(resolved)
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      `/api/reviews/${caseID}/provider/thread`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, action: "resolve" }),
      },
    )
  })

  it("rejects invalid local thread authority before making a request", async () => {
    await expect(
      mutateReviewProviderThread(caseID, "raw-provider-thread-id", "resolve"),
    ).rejects.toEqual(
      expect.objectContaining({
        status: 400,
        code: "invalid_provider_thread_action",
      }),
    )
    expect(mockedLauncherFetch).not.toHaveBeenCalled()
  })

  it.each([
    {
      name: "unsafe review URL",
      value: {
        ...snapshot,
        reviews: [{ ...snapshot.reviews[0], url: "javascript:alert(1)" }],
      },
    },
    {
      name: "mismatched pull number",
      value: {
        ...snapshot,
        pull_request: { ...snapshot.pull_request, number: 43 },
      },
    },
    {
      name: "duplicate review identity",
      value: {
        ...snapshot,
        reviews: [snapshot.reviews[0], snapshot.reviews[0]],
      },
    },
    {
      name: "thread action without capability",
      value: {
        ...snapshot,
        capabilities: { thread_resolution: false },
      },
    },
    {
      name: "incomplete available response",
      value: { ...snapshot, review_history_complete: false },
    },
    {
      name: "unknown response member",
      value: { ...snapshot, private_provider_data: "must not project" },
    },
  ])("rejects a malformed projection: $name", async ({ value }) => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(value))

    await expect(getReviewProviderSnapshot(caseID)).rejects.toEqual(
      expect.objectContaining({
        status: 502,
        code: "invalid_provider_snapshot",
      }),
    )
  })

  it("rejects duplicate JSON members rather than trusting JSON.parse replacement", async () => {
    mockedLauncherFetch.mockResolvedValue(
      new Response(
        `${JSON.stringify(snapshot).slice(0, -1)},"availability":"partial"}`,
        { status: 200 },
      ),
    )

    await expect(getReviewProviderSnapshot(caseID)).rejects.toMatchObject({
      code: "invalid_provider_snapshot",
    })
  })

  it("keeps bounded server error codes and hides malformed error bodies", async () => {
    mockedLauncherFetch
      .mockResolvedValueOnce(jsonResponse({ error: "provider_busy" }, 503))
      .mockResolvedValueOnce(
        jsonResponse({ error: "unsafe", private_detail: "secret" }, 502),
      )

    await expect(getReviewProviderSnapshot(caseID)).rejects.toEqual(
      new ReviewProviderAPIError("provider_busy", 503),
    )
    await expect(getReviewProviderSnapshot(caseID)).rejects.toEqual(
      new ReviewProviderAPIError("provider_snapshot_unavailable", 502),
    )
  })
})
