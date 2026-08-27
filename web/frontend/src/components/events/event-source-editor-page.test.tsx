import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  createEventSource,
  getEventSource,
  getEventSourceSettings,
  updateEventSource,
} from "@/api/event-sources"
import { SidebarProvider } from "@/components/ui/sidebar"

import { EventSourceEditorPage } from "./event-source-editor-page"

vi.mock("@/api/event-sources", () => ({
  getEventSourceSettings: vi.fn(),
  getEventSource: vi.fn(),
  createEventSource: vi.fn(),
  updateEventSource: vi.fn(),
}))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn().mockResolvedValue({ restartRequired: true }),
}))
vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))

describe("EventSourceEditorPage", () => {
  beforeAll(installRadixPolyfills)

  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(getEventSourceSettings).mockResolvedValue(settingsResponse())
    vi.mocked(getEventSource).mockResolvedValue(detailResponse())
  })

  it("offers eligible unconfigured adapters as creation choices", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    vi.mocked(createEventSource).mockResolvedValue({
      event_source: channelDetail,
      config_revision: "revision-2",
      effects,
    })
    renderEditor(
      <EventSourceEditorPage
        mode="create"
        onBack={vi.fn()}
        onSaved={onSaved}
      />,
    )

    await user.click(
      await screen.findByRole("combobox", { name: "Create from" }),
    )
    await user.click(
      screen.getByRole("option", { name: "Delta Chat adapter — support-mail" }),
    )
    expect(screen.getByLabelText("Channel")).toHaveValue("support-mail")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("evt_mail"))
    expect(createEventSource).toHaveBeenCalledWith(
      {
        kind: "channel",
        name: "support-mail",
        enabled: false,
        source: "email",
        mode: "mirror",
        allow_unverified_email: false,
      },
      "revision-1",
    )
  })

  it("creates a disabled webhook without inventing a secret replacement", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    vi.mocked(createEventSource).mockResolvedValue({
      event_source: {
        ...detailResponse().event_source,
        name: "build-system",
        enabled: false,
        status: "disabled",
        poll_notifications: false,
        repositories: [],
        target_user: "",
        secret_configured: false,
      },
      config_revision: "revision-2",
      effects,
    })
    renderEditor(
      <EventSourceEditorPage
        mode="create"
        onBack={vi.fn()}
        onSaved={onSaved}
      />,
    )

    await user.type(
      await screen.findByLabelText("Connector name"),
      "build-system",
    )
    const secret = screen.getByLabelText("Signing secret")
    await user.type(secret, "r".repeat(32))
    await user.clear(secret)
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(createEventSource).toHaveBeenCalledWith(
      {
        kind: "webhook",
        name: "build-system",
        enabled: false,
        format: "github",
        repositories: [],
        target_user: "",
        poll_notifications: false,
        secret_update: "preserve",
      },
      "revision-1",
    )
  })

  it("keeps existing secrets masked and sends explicit replacement only", async () => {
    const user = userEvent.setup()
    const onSaved = vi.fn()
    vi.mocked(updateEventSource).mockResolvedValue({
      ...detailResponse(),
      config_revision: "revision-2",
      effects,
    })
    renderEditor(
      <EventSourceEditorPage
        mode="edit"
        id="evt_github"
        onBack={vi.fn()}
        onSaved={onSaved}
      />,
    )

    const secret = await screen.findByLabelText("Signing secret")
    expect(secret).toHaveValue("")
    expect(secret).toHaveAttribute(
      "placeholder",
      "Configured — type to replace",
    )
    expect(screen.getByLabelText("Connector name")).toHaveAttribute("readonly")
    expect(screen.getByRole("button", { name: "Clear" })).toBeDisabled()
    const replacement = "r".repeat(32)
    await user.type(secret, replacement)
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith("evt_github"))
    expect(updateEventSource).toHaveBeenCalledWith(
      "evt_github",
      expect.objectContaining({
        secret_update: "replace",
        secret: replacement,
      }),
      "revision-1",
    )
    expect(document.body).not.toHaveTextContent(replacement)
    expect(getEventSourceSettings).not.toHaveBeenCalled()
  })

  it("blocks an enabled adapter whose backing channel is disabled", async () => {
    const user = userEvent.setup()
    vi.mocked(getEventSourceSettings).mockResolvedValue({
      ...settingsResponse(),
      eligible_channel_adapters: [
        {
          name: "disabled-mail",
          channel_type: "deltachat",
          channel_enabled: false,
        },
      ],
    })
    renderEditor(
      <EventSourceEditorPage
        mode="create"
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    await user.click(
      await screen.findByRole("combobox", { name: "Create from" }),
    )
    await user.click(
      screen.getByRole("option", {
        name: "Delta Chat adapter — disabled-mail",
      }),
    )
    await user.click(
      screen.getByRole("switch", { name: "Enable event adapter" }),
    )
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText(
        "Enable the referenced Delta Chat channel before enabling this adapter.",
      ),
    ).toBeVisible()
    expect(createEventSource).not.toHaveBeenCalled()
  })
})

const effects = {
  launcher_effect: "applied" as const,
  catalog_effect: "applied" as const,
  gateway_effect: "restart_required" as const,
}

const channelDetail = {
  id: "evt_mail",
  name: "support-mail",
  kind: "channel" as const,
  enabled: false,
  format: "deltachat" as const,
  status: "disabled" as const,
  poll_notifications: false,
  source: "email" as const,
  mode: "mirror" as const,
  allow_unverified_email: false,
  channel_enabled: true,
  channel_type: "deltachat",
}

function settingsResponse() {
  return {
    event_source_settings: {
      enabled: true,
      database_path: "",
      retention_days: 30,
      max_payload_bytes: 1_048_576,
      redact_fields: [],
    },
    eligible_channel_adapters: [
      {
        name: "support-mail",
        channel_type: "deltachat",
        channel_enabled: true,
      },
    ],
    config_revision: "revision-1",
  }
}

function detailResponse() {
  return {
    event_source: {
      id: "evt_github",
      name: "github",
      kind: "webhook" as const,
      enabled: true,
      format: "github" as const,
      status: "available" as const,
      poll_notifications: true,
      repositories: ["octo/picoclaw"],
      target_user: "octocat",
      secret_configured: true,
    },
    config_revision: "revision-1",
  }
}

function renderEditor(children: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>{children}</SidebarProvider>
    </QueryClientProvider>,
  )
}

function installRadixPolyfills() {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
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
}
