import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  NotificationAPIError,
  getDevelopmentNotification,
  listDevelopmentNotifications,
  mutateDevelopmentNotifications,
  updateNotificationSettings,
} from "@/api/notifications"

describe("notification API", () => {
  beforeEach(() => vi.restoreAllMocks())

  it("encodes JQL-like queries and opaque cursors", async () => {
    const request = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ notifications: [] }), { status: 200 }),
      )

    await listDevelopmentNotifications({
      query: 'status = open AND repository ~ "owner/repo"',
      cursor: "opaque/+cursor",
      limit: 50,
    })

    expect(request).toHaveBeenCalledWith(
      "/api/notifications?query=status+%3D+open+AND+repository+%7E+%22owner%2Frepo%22&cursor=opaque%2F%2Bcursor&limit=50",
      expect.objectContaining({ credentials: "same-origin" }),
    )
  })

  it("accepts wrapped detail responses", async () => {
    const notification = { id: "ntf_11111111111111111111111111111111" }
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ notification }), { status: 200 }),
    )

    await expect(getDevelopmentNotification(notification.id)).resolves.toEqual(
      notification,
    )
  })

  it("sends per-notification revision fences for bulk mutations", async () => {
    const request = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ notifications: [] }), { status: 200 }),
      )

    await mutateDevelopmentNotifications({
      action: "snooze",
      items: [
        { id: "ntf_11111111111111111111111111111111", expected_version: 4 },
      ],
      snoozed_until: "2026-08-25T10:00:00Z",
      request_id: "request-1",
    })

    const init = request.mock.calls[0]?.[1]
    expect(init?.method).toBe("POST")
    expect(JSON.parse(String(init?.body))).toEqual({
      action: "snooze",
      items: [
        { id: "ntf_11111111111111111111111111111111", expected_version: 4 },
      ],
      snoozed_until: "2026-08-25T10:00:00Z",
      request_id: "request-1",
    })
  })

  it("projects server query positions into typed errors", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          code: "invalid_query",
          message: "expected a value",
          position: 17,
        }),
        { status: 400 },
      ),
    )

    const error = await updateNotificationSettings({
      include_repository_in_push: false,
      expected_version: 1,
      request_id: "request-2",
    }).catch((reason: unknown) => reason)

    expect(error).toBeInstanceOf(NotificationAPIError)
    expect(error).toMatchObject({
      status: 400,
      code: "invalid_query",
      position: 17,
      message: "expected a value",
    })
  })
})
