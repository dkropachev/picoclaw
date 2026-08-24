import type { BrowserPushSubscriptionInput } from "@/api/notifications"

let registrationPromise: Promise<ServiceWorkerRegistration | undefined> | null =
  null

export function supportsPicoClawPush(): boolean {
  return (
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    typeof PushManager !== "undefined" &&
    typeof Notification !== "undefined" &&
    globalThis.isSecureContext
  )
}

export function registerPicoClawServiceWorker(): Promise<
  ServiceWorkerRegistration | undefined
> {
  if (registrationPromise) return registrationPromise
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) {
    return Promise.resolve(undefined)
  }
  registrationPromise = navigator.serviceWorker
    .register("/service-worker.js", { scope: "/" })
    .catch(() => undefined)
  return registrationPromise
}

export async function subscribeBrowserToPush(
  vapidPublicKey: string,
): Promise<BrowserPushSubscriptionInput> {
  if (!supportsPicoClawPush()) {
    throw new Error("Push notifications are not supported in this browser.")
  }
  const permission = await Notification.requestPermission()
  if (permission !== "granted") {
    throw new Error("Notification permission was not granted.")
  }
  const registration =
    (await registerPicoClawServiceWorker()) ??
    (await navigator.serviceWorker.ready)
  let subscription = await registration.pushManager.getSubscription()
  subscription ??= await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: decodeVAPIDPublicKey(vapidPublicKey),
  })
  const serialized = subscription.toJSON()
  if (
    !serialized.endpoint ||
    !serialized.keys?.auth ||
    !serialized.keys.p256dh
  ) {
    throw new Error("Browser returned an incomplete push subscription.")
  }
  return {
    endpoint: serialized.endpoint,
    ...(typeof serialized.expirationTime === "number"
      ? { expiration_time: serialized.expirationTime }
      : {}),
    keys: {
      auth: serialized.keys.auth,
      p256dh: serialized.keys.p256dh,
    },
  }
}

export async function unsubscribeBrowserFromPush(): Promise<void> {
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) {
    return
  }
  const registration = await navigator.serviceWorker.getRegistration("/")
  const subscription = await registration?.pushManager.getSubscription()
  await subscription?.unsubscribe()
}

export async function refreshPicoClawAppBadge(count: number): Promise<void> {
  if (typeof navigator === "undefined") return
  const badgeNavigator = navigator as Navigator & {
    setAppBadge?: (contents?: number) => Promise<void>
    clearAppBadge?: () => Promise<void>
  }
  try {
    if (count > 0) await badgeNavigator.setAppBadge?.(count)
    else await badgeNavigator.clearAppBadge?.()
  } catch {
    // Badge support is optional and must never break the in-app inbox.
  }
}

function decodeVAPIDPublicKey(value: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replace(/-/g, "+").replace(/_/g, "/")
  const decoded = globalThis.atob(base64)
  const bytes = new Uint8Array(new ArrayBuffer(decoded.length))
  for (let index = 0; index < decoded.length; index += 1) {
    bytes[index] = decoded.charCodeAt(index)
  }
  return bytes
}
