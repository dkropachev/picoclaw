import { afterEach, describe, expect, it, vi } from "vitest"

const originalServiceWorker = Object.getOwnPropertyDescriptor(
  globalThis.navigator,
  "serviceWorker",
)

describe("PWA notification client", () => {
  afterEach(() => {
    if (originalServiceWorker) {
      Object.defineProperty(
        globalThis.navigator,
        "serviceWorker",
        originalServiceWorker,
      )
    } else {
      Reflect.deleteProperty(globalThis.navigator, "serviceWorker")
    }
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    vi.resetModules()
  })

  it("requests permission only when push subscription is explicitly created", async () => {
    const requestPermission = vi.fn().mockResolvedValue("granted")
    const subscribe = vi.fn().mockResolvedValue({
      toJSON: () => ({
        endpoint: "https://push.example/subscription",
        expirationTime: null,
        keys: { auth: "auth-key", p256dh: "p256dh-key" },
      }),
    })
    const registration = {
      pushManager: {
        getSubscription: vi.fn().mockResolvedValue(null),
        subscribe,
      },
    }
    const register = vi.fn().mockResolvedValue(registration)
    Object.defineProperty(globalThis.navigator, "serviceWorker", {
      configurable: true,
      value: { register, ready: Promise.resolve(registration) },
    })
    vi.stubGlobal("PushManager", class PushManager {})
    vi.stubGlobal("Notification", { requestPermission })
    vi.stubGlobal("isSecureContext", true)

    const { subscribeBrowserToPush, supportsPicoClawPush } =
      await import("@/lib/pwa-notifications")

    expect(supportsPicoClawPush()).toBe(true)
    expect(requestPermission).not.toHaveBeenCalled()
    await expect(subscribeBrowserToPush("AQID")).resolves.toEqual({
      endpoint: "https://push.example/subscription",
      keys: { auth: "auth-key", p256dh: "p256dh-key" },
    })
    expect(requestPermission).toHaveBeenCalledOnce()
    expect(register).toHaveBeenCalledWith("/service-worker.js", { scope: "/" })
    expect(subscribe).toHaveBeenCalledWith({
      userVisibleOnly: true,
      applicationServerKey: new Uint8Array([1, 2, 3]),
    })
  })
})
