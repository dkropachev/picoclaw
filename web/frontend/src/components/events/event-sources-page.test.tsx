import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { AnchorHTMLAttributes, ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type EventChannelSource,
  type EventSourcesSettings,
  type EventWebhookSource,
  type LoadedEventSources,
  loadEventSources,
  newEventWebhookSource,
  saveEventSources,
} from "@/api/event-sources"
import { EventSourcesPage } from "@/components/events/event-sources-page"
import { SidebarProvider } from "@/components/ui/sidebar"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

vi.mock("@/api/event-sources", () => ({
  loadEventSources: vi.fn(),
  saveEventSources: vi.fn(),
  newEventWebhookSource: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
  },
}))

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    params,
    ...props
  }: {
    children: ReactNode
    to: string
    params?: Record<string, string>
  } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a {...props} href={to.replace("$name", params?.name ?? "$name")}>
      {children}
    </a>
  ),
}))

const persistedWebhook = webhook({
  id: "webhook-primary",
  name: "primary",
  persistedName: "primary",
  enabled: true,
  repositories: ["scylladb/gocql"],
  targetUser: "review-user",
  secretConfigured: true,
  persistedFormat: "github",
})

describe("EventSourcesPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
    Object.defineProperty(HTMLElement.prototype, "hasPointerCapture", {
      configurable: true,
      value: vi.fn(() => false),
    })
    Object.defineProperty(HTMLElement.prototype, "setPointerCapture", {
      configurable: true,
      value: vi.fn(),
    })
    Object.defineProperty(HTMLElement.prototype, "releasePointerCapture", {
      configurable: true,
      value: vi.fn(),
    })
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    })
  })

  beforeEach(() => {
    vi.mocked(loadEventSources).mockReset()
    vi.mocked(saveEventSources).mockReset()
    vi.mocked(newEventWebhookSource).mockReset()
    vi.mocked(showSaveSuccessOrRestartToast).mockReset()
    vi.mocked(refreshGatewayState).mockReset()

    vi.mocked(saveEventSources).mockResolvedValue()
    vi.mocked(newEventWebhookSource).mockReturnValue(
      webhook({
        id: "webhook-new",
        name: "",
        format: "github",
        secretUpdate: "replace",
      }),
    )
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: true,
    })
  })

  it("keeps a configured secret masked, preserves it on save, and refreshes restart state", async () => {
    const loaded = eventSources({ webhooks: [persistedWebhook] })
    renderEventSources(loaded)
    const user = userEvent.setup()

    const secretInput = await screen.findByLabelText("Signing secret")
    expect(screen.getByLabelText("Connector name")).toHaveAttribute("readonly")
    expect(secretInput).toHaveValue("")
    expect(secretInput).toHaveAttribute(
      "placeholder",
      "Configured — type to replace",
    )
    expect(document.body).not.toHaveTextContent("[NOT_HERE]")

    await user.type(secretInput, "temporary replacement")
    await user.clear(secretInput)

    const payloadLimit = screen.getByLabelText("Maximum payload bytes")
    await user.clear(payloadLimit)
    await user.type(payloadLimit, "2048")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
      expect(refreshGatewayState).toHaveBeenCalledWith({ force: true })
      expect(showSaveSuccessOrRestartToast).toHaveBeenCalledWith(
        expect.any(Function),
        expect.any(String),
        expect.any(String),
        true,
      )
    })

    const saved = vi.mocked(saveEventSources).mock.calls[0]?.[0]
    expect(saved?.maxPayloadBytes).toBe("2048")
    expect(saved?.webhooks[0]).toMatchObject({
      name: "primary",
      secretConfigured: true,
      secretUpdate: "preserve",
      secret: "",
    })
  })

  it("imports a GitHub repository list and saves target-user routing metadata", async () => {
    renderEventSources(eventSources({ webhooks: [persistedWebhook] }))
    const user = userEvent.setup()

    const targetUser = await screen.findByLabelText("GitHub user to notify")
    expect(
      screen.getByText(
        "Used to mark review requests, assignments, and @mentions that target you. Native webhook deliveries also mark submitted feedback from other reviewers on pull requests you authored. Webhook targeting is routing metadata only.",
      ),
    ).toBeInTheDocument()
    await user.clear(targetUser)
    await user.type(targetUser, "scylla-reviewer")
    await user.click(
      screen.getByRole("switch", { name: "Poll GitHub notifications" }),
    )

    const repositoryFile = new File(["unused"], "repo_list.txt", {
      type: "text/plain",
    })
    Object.defineProperty(repositoryFile, "text", {
      value: vi
        .fn()
        .mockResolvedValue(
          "scylladb/scylla\nscylladb/gocql\nSCYLLADB/GOCQL\n\n",
        ),
    })
    await user.upload(
      screen.getByLabelText("Import owner/repo text file"),
      repositoryFile,
    )

    await waitFor(() => {
      expect(screen.getByLabelText("Watched repositories")).toHaveValue(
        "scylladb/scylla\nscylladb/gocql\nSCYLLADB/GOCQL\n\n",
      )
    })
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
    })
    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks[0],
    ).toMatchObject({
      repositories: ["scylladb/scylla", "scylladb/gocql"],
      targetUser: "scylla-reviewer",
      pollNotifications: true,
    })
  })

  it("allows an enabled GitHub notification poller without a webhook secret", async () => {
    renderEventSources(
      eventSources({
        webhooks: [
          webhook({
            id: "poll-only",
            name: "github-poll",
            enabled: true,
            pollNotifications: true,
          }),
        ],
      }),
    )
    const user = userEvent.setup()

    await screen.findByRole("switch", { name: "Poll GitHub notifications" })
    await user.type(screen.getByLabelText("GitHub user to notify"), "reviewer")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
    })
    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks[0],
    ).toMatchObject({
      enabled: true,
      pollNotifications: true,
      secretConfigured: false,
    })
  })

  it("rotates one secret, clears a disabled source, and removes another source", async () => {
    const replacementSecret = "r".repeat(32)
    const loaded = eventSources({
      webhooks: [
        persistedWebhook,
        webhook({
          id: "webhook-clearing",
          name: "clearing",
          enabled: true,
          secretConfigured: true,
        }),
        webhook({
          id: "webhook-removing",
          name: "removing",
          enabled: true,
          secretConfigured: true,
        }),
      ],
    })
    renderEventSources(loaded)
    const user = userEvent.setup()

    const primary = await screen.findByRole("group", { name: "primary" })
    await user.type(
      within(primary).getByLabelText("Signing secret"),
      replacementSecret,
    )

    const clearing = screen.getByRole("group", { name: "clearing" })
    await user.click(
      within(clearing).getByRole("switch", {
        name: "Enable webhook clearing",
      }),
    )
    await user.click(within(clearing).getByRole("button", { name: "Clear" }))

    const removing = screen.getByRole("group", { name: "removing" })
    await user.click(
      within(removing).getByRole("button", {
        name: "Remove webhook removing",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
    })
    const savedWebhooks =
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks ?? []
    expect(
      savedWebhooks.find((source) => source.name === "primary"),
    ).toMatchObject({
      secretUpdate: "replace",
      secret: replacementSecret,
    })
    expect(
      savedWebhooks.find((source) => source.name === "clearing"),
    ).toMatchObject({
      enabled: false,
      secretUpdate: "clear",
      secret: "",
    })
    expect(savedWebhooks.some((source) => source.name === "removing")).toBe(
      false,
    )
  })

  it("rejects invalid numeric, connector, and signing-secret inputs before the API call", async () => {
    const loaded = eventSources({ webhooks: [] })
    renderEventSources(loaded)
    const user = userEvent.setup()

    const retention = await screen.findByLabelText("Retention days")
    await user.clear(retention)
    await user.type(retention, "0")
    await user.click(screen.getByRole("button", { name: "Add webhook" }))
    await user.type(screen.getByLabelText("Connector name"), "9 bad")
    await user.type(screen.getByLabelText("GitHub user to notify"), "-bad")
    await user.type(
      screen.getByLabelText("Watched repositories"),
      "missing-owner",
    )
    await user.type(screen.getByLabelText("Signing secret"), "short")
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText(
        "Retention days must be a positive whole number or blank.",
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        /Use 1–64 characters: start with a letter, then letters, numbers/,
      ),
    ).toBeInTheDocument()
    expect(screen.getByLabelText("Signing secret")).toHaveAccessibleDescription(
      "GitHub secrets must be 32–256 UTF-8 bytes with no leading or trailing whitespace.",
    )
    expect(
      screen.getByText(
        "Each repository must be one trimmed owner/repo name of at most 256 bytes.",
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "Use a trimmed GitHub login of at most 128 letters, numbers, or internal hyphens.",
      ),
    ).toBeInTheDocument()
    expect(saveEventSources).not.toHaveBeenCalled()
    expect(refreshGatewayState).not.toHaveBeenCalled()
  })

  it("requires a compatible replacement when a persisted webhook format changes", async () => {
    renderEventSources(eventSources({ webhooks: [persistedWebhook] }))
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("combobox", {
        name: "Webhook format for primary",
      }),
    )
    await user.click(screen.getByRole("option", { name: "Standard Webhooks" }))
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText(
        "Changing webhook format requires a compatible replacement signing secret.",
      ),
    ).toBeInTheDocument()
    expect(saveEventSources).not.toHaveBeenCalled()

    const replacement = `whsec_${btoa("s".repeat(32))}`
    await user.type(screen.getByLabelText("Signing secret"), replacement)
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
    })
    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks[0],
    ).toMatchObject({
      format: "standard",
      secretUpdate: "replace",
      secret: replacement,
    })
  })

  it("rejects ASCII connector names that differ only by case", async () => {
    renderEventSources(
      eventSources({
        webhooks: [
          webhook({ id: "uppercase", name: "I" }),
          webhook({ id: "lowercase", name: "i" }),
        ],
      }),
    )
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("switch", {
        name: "Enable durable event ingestion",
      }),
    )
    await user.click(await screen.findByRole("button", { name: "Save" }))

    expect(
      await screen.findAllByText(
        "Connector names must be unique, including differences in letter case.",
      ),
    ).toHaveLength(2)
    expect(saveEventSources).not.toHaveBeenCalled()
  })

  it("rejects enabled adapters for missing and disabled Delta Chat channels", async () => {
    const loaded = eventSources({
      channels: [
        channel({
          id: "channel-disabled",
          name: "delta-disabled",
          available: true,
          channelEnabled: false,
        }),
        channel({
          id: "channel-missing",
          name: "delta-missing",
          available: false,
          channelEnabled: false,
        }),
      ],
    })
    renderEventSources(loaded)
    const user = userEvent.setup()

    await user.click(
      await screen.findByRole("switch", {
        name: "Enable event adapter for delta-disabled",
      }),
    )
    await user.click(
      screen.getByRole("switch", {
        name: "Enable event adapter for delta-missing",
      }),
    )
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText(
        "Enable the referenced Delta Chat channel before enabling this adapter.",
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "This adapter must reference an existing Delta Chat channel.",
      ),
    ).toBeInTheDocument()
    expect(saveEventSources).not.toHaveBeenCalled()
  })

  it("does not replace an unsaved draft when query data refreshes", async () => {
    const loaded = eventSources({ webhooks: [persistedWebhook] })
    const { client } = renderEventSources(loaded)
    const user = userEvent.setup()

    const retention = await screen.findByLabelText("Retention days")
    await user.clear(retention)
    await user.type(retention, "45")

    act(() => {
      client.setQueryData(
        ["event-sources", "settings"],
        eventSources({
          ...loaded.settings,
          retentionDays: "99",
        }),
      )
    })

    expect(retention).toHaveValue(45)
  })

  it("locks draft controls while a save is pending", async () => {
    let resolveSave: (() => void) | undefined
    vi.mocked(saveEventSources).mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveSave = resolve
      }),
    )
    renderEventSources(eventSources({ webhooks: [persistedWebhook] }))
    const user = userEvent.setup()

    const secretInput = await screen.findByLabelText("Signing secret")
    const replacement = "r".repeat(32)
    await user.type(secretInput, replacement)
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
      expect(secretInput).toBeDisabled()
    })
    await user.type(secretInput, "must-not-append")
    expect(secretInput).toHaveValue(replacement)
    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks[0],
    ).toMatchObject({
      secretUpdate: "replace",
      secret: replacement,
    })

    await act(async () => {
      resolveSave?.()
    })
    await waitFor(() => {
      expect(showSaveSuccessOrRestartToast).toHaveBeenCalledTimes(1)
    })
  })

  it("discards a committed replacement secret before a failed masked reload", async () => {
    const loaded = eventSources({ webhooks: [persistedWebhook] })
    renderEventSources(loaded)
    const user = userEvent.setup()

    const secretInput = await screen.findByLabelText("Signing secret")
    vi.mocked(loadEventSources).mockRejectedValue(
      new Error("masked event-source reload failed"),
    )
    const replacement = "r".repeat(32)
    await user.type(secretInput, replacement)
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
      expect(secretInput).toHaveValue("")
      expect(secretInput).toHaveAttribute(
        "placeholder",
        "Configured — type to replace",
      )
    })
    expect(document.body).not.toHaveTextContent(replacement)
    expect(showSaveSuccessOrRestartToast).not.toHaveBeenCalled()
    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].webhooks[0],
    ).toMatchObject({
      secretUpdate: "replace",
      secret: replacement,
    })
  })

  it("saves Delta Chat delivery-mode and unverified-email changes", async () => {
    const loaded = eventSources({
      channels: [
        channel({
          id: "channel-ready",
          name: "delta-ready",
          enabled: false,
          configured: false,
          available: true,
          channelEnabled: true,
        }),
      ],
    })
    renderEventSources(loaded)
    const user = userEvent.setup()

    const deliveryMode = await screen.findByRole("combobox", {
      name: "Delivery mode for delta-ready",
    })
    const allowUnverified = screen.getByRole("switch", {
      name: "Allow unverified email for delta-ready",
    })
    expect(deliveryMode).toBeDisabled()
    expect(allowUnverified).toBeDisabled()

    await user.click(
      screen.getByRole("switch", {
        name: "Enable event adapter for delta-ready",
      }),
    )
    expect(deliveryMode).toBeEnabled()
    expect(allowUnverified).toBeEnabled()

    await user.click(deliveryMode)
    await user.click(await screen.findByRole("option", { name: "Event only" }))
    await user.click(allowUnverified)
    expect(
      screen.getByText(
        /Unverified email can be spoofed. Use deterministic workflow rules/,
      ),
    ).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => {
      expect(saveEventSources).toHaveBeenCalledTimes(1)
    })

    expect(
      vi.mocked(saveEventSources).mock.calls[0]?.[0].channels[0],
    ).toMatchObject({
      name: "delta-ready",
      enabled: true,
      mode: "event_only",
      allowUnverifiedEmail: true,
    })
  })
})

function renderEventSources(loaded: LoadedEventSources) {
  vi.mocked(loadEventSources).mockResolvedValue(loaded)
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })

  const view = render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <EventSourcesPage />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  return { ...view, client }
}

function eventSources(
  overrides: Partial<EventSourcesSettings> = {},
): LoadedEventSources {
  const settings: EventSourcesSettings = {
    enabled: true,
    databasePath: "",
    retentionDays: "30",
    maxPayloadBytes: "1048576",
    redactFields: ["authorization"],
    webhooks: [persistedWebhook],
    channels: [],
    gatewayHost: "192.168.0.100",
    gatewayPort: 18789,
    ...overrides,
  }
  return {
    settings,
    persisted: {
      webhookNames: settings.webhooks
        .filter((source) => source.name !== "")
        .map((source) => source.name),
      channelNames: settings.channels
        .filter((source) => source.configured)
        .map((source) => source.name),
    },
  }
}

function webhook(
  overrides: Partial<EventWebhookSource> = {},
): EventWebhookSource {
  return {
    id: "webhook-default",
    name: "github",
    enabled: false,
    format: "github",
    repositories: [],
    targetUser: "",
    pollNotifications: false,
    secretConfigured: false,
    secretUpdate: "preserve",
    secret: "",
    ...overrides,
  }
}

function channel(
  overrides: Partial<EventChannelSource> = {},
): EventChannelSource {
  return {
    id: "channel-default",
    name: "deltachat",
    enabled: false,
    source: "email",
    mode: "mirror",
    allowUnverifiedEmail: false,
    configured: true,
    available: true,
    channelEnabled: true,
    channelType: "deltachat",
    ...overrides,
  }
}
