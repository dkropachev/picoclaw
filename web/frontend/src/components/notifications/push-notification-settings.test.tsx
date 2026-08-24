import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createPushSubscriptionDevice,
  getNotificationSettings,
  listPushSubscriptionDevices,
} from "@/api/notifications"
import { PushNotificationSettings } from "@/components/notifications/push-notification-settings"
import { subscribeBrowserToPush } from "@/lib/pwa-notifications"

vi.mock("@/api/notifications", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/notifications")>()
  return {
    ...actual,
    createPushSubscriptionDevice: vi.fn(),
    deletePushSubscriptionDevice: vi.fn(),
    getNotificationSettings: vi.fn(),
    listPushSubscriptionDevices: vi.fn(),
    updateNotificationSettings: vi.fn(),
    updatePushSubscriptionDevice: vi.fn(),
  }
})

vi.mock("@/lib/pwa-notifications", () => ({
  subscribeBrowserToPush: vi.fn(),
  supportsPicoClawPush: () => true,
  unsubscribeBrowserFromPush: vi.fn(),
}))

describe("PushNotificationSettings", () => {
  beforeEach(() => {
    vi.mocked(getNotificationSettings).mockResolvedValue({
      include_repository_in_push: false,
      vapid_public_key: "AQID",
      version: 1,
    })
    vi.mocked(listPushSubscriptionDevices).mockResolvedValue({
      subscriptions: [],
    })
    vi.mocked(subscribeBrowserToPush).mockReset()
    vi.mocked(createPushSubscriptionDevice).mockReset()
    vi.mocked(subscribeBrowserToPush).mockResolvedValue({
      endpoint: "https://push.example/subscription",
      keys: { auth: "auth", p256dh: "p256dh" },
    })
    vi.mocked(createPushSubscriptionDevice).mockResolvedValue({
      id: "push-device-1",
      name: "My phone",
      enabled: true,
      version: 1,
      created_at: "2026-08-24T10:00:00Z",
      updated_at: "2026-08-24T10:00:00Z",
    })
  })

  it("does not request permission before explicit enable action", async () => {
    const user = userEvent.setup()
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={queryClient}>
        <PushNotificationSettings open onOpenChange={vi.fn()} />
      </QueryClientProvider>,
    )

    expect(subscribeBrowserToPush).not.toHaveBeenCalled()
    const name = await screen.findByLabelText("Device name")
    await user.clear(name)
    await user.type(name, "My phone")
    await user.click(
      screen.getByRole("button", { name: "Enable mobile notifications" }),
    )

    await waitFor(() =>
      expect(subscribeBrowserToPush).toHaveBeenCalledWith("AQID"),
    )
    expect(createPushSubscriptionDevice).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "My phone",
        endpoint: "https://push.example/subscription",
        keys: { auth: "auth", p256dh: "p256dh" },
      }),
    )
  })
})
