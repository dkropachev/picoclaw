const STATIC_CACHE = "picoclaw-static-v1"
const STATIC_ASSETS = [
  "/site.webmanifest",
  "/favicon.svg",
  "/web-app-manifest-192x192.png",
  "/web-app-manifest-512x512.png",
]

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(STATIC_CACHE)
      .then((cache) => cache.addAll(STATIC_ASSETS))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter(
              (key) =>
                key.startsWith("picoclaw-static-") && key !== STATIC_CACHE,
            )
            .map((key) => caches.delete(key)),
        ),
      )
      .then(() => self.clients.claim()),
  )
})

self.addEventListener("fetch", (event) => {
  const requestURL = new URL(event.request.url)
  if (
    event.request.method !== "GET" ||
    requestURL.origin !== self.location.origin ||
    !STATIC_ASSETS.includes(requestURL.pathname)
  ) {
    return
  }
  event.respondWith(
    caches
      .match(event.request)
      .then((cached) => cached || fetch(event.request)),
  )
})

self.addEventListener("push", (event) => {
  const payload = readPushPayload(event.data)
  const notificationID = safeNotificationID(payload.notification_id)
  const title = "PicoClaw needs your attention"
  const reason = reasonLabel(payload.reason)
  const repository = safeRepository(payload.repository)
  const body = repository ? `${reason} · ${repository}` : reason
  event.waitUntil(
    Promise.all([
      self.registration.showNotification(title, {
        body,
        icon: "/web-app-manifest-192x192.png",
        badge: "/favicon-96x96.png",
        tag: notificationID || "picoclaw-attention",
        renotify: true,
        data: { notification_id: notificationID },
      }),
      setBadge(payload.open_count),
    ]),
  )
})

self.addEventListener("notificationclick", (event) => {
  event.notification.close()
  const notificationID = safeNotificationID(
    event.notification.data?.notification_id,
  )
  const target = notificationID
    ? `/notifications/${encodeURIComponent(notificationID)}`
    : "/notifications"
  event.waitUntil(openOrFocus(target))
})

self.addEventListener("message", (event) => {
  if (event.data?.type === "SET_BADGE") {
    event.waitUntil(setBadge(event.data.count))
  }
})

function readPushPayload(data) {
  if (!data) return {}
  try {
    const value = data.json()
    return value && typeof value === "object" ? value : {}
  } catch {
    return {}
  }
}

function safeNotificationID(value) {
  return typeof value === "string" &&
    /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(value)
    ? value
    : ""
}

function reasonLabel(value) {
  const labels = {
    charter_ambiguity: "Charter needs clarification",
    scope_exception: "Scope decision required",
    steering_scope_change: "Scope change requires review",
    implementation_blocked: "Implementation is blocked",
    provider_outcome_unknown: "Provider outcome needs review",
    publication_approval: "Publication approval required",
  }
  return labels[value] || "Action required"
}

function safeRepository(value) {
  return typeof value === "string" &&
    value.length <= 512 &&
    !/[\u0000-\u001f\u007f]/.test(value)
    ? value
    : ""
}

async function setBadge(value) {
  const count = Number.isSafeInteger(value) && value > 0 ? value : 0
  if (count > 0 && typeof self.navigator.setAppBadge === "function") {
    await self.navigator.setAppBadge(count)
  } else if (
    count === 0 &&
    typeof self.navigator.clearAppBadge === "function"
  ) {
    await self.navigator.clearAppBadge()
  }
}

async function openOrFocus(path) {
  const windows = await self.clients.matchAll({
    type: "window",
    includeUncontrolled: true,
  })
  for (const client of windows) {
    if ("navigate" in client) await client.navigate(path)
    return client.focus()
  }
  return self.clients.openWindow(path)
}
