import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  getEventSourceSettings,
  updateEventSourceSettings,
} from "@/api/event-sources"
import { SidebarProvider } from "@/components/ui/sidebar"

import { EventSourceSettingsPage } from "./event-source-settings-page"

vi.mock("@/api/event-sources", () => ({
  getEventSourceSettings: vi.fn(),
  updateEventSourceSettings: vi.fn(),
}))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn().mockResolvedValue({ restartRequired: true }),
}))
vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }))

describe("EventSourceSettingsPage", () => {
  beforeAll(installBrowserPolyfills)
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(getEventSourceSettings).mockResolvedValue(settingsResponse())
  })

  it("owns only master ingress and storage policy", async () => {
    renderSettings()
    expect(
      await screen.findByRole("heading", { name: "Event source settings" }),
    ).toBeVisible()
    expect(
      await screen.findByLabelText("Enable durable event ingestion"),
    ).toBeChecked()
    expect(screen.getByLabelText("Retention days")).toHaveValue(30)
    expect(screen.queryByText("Webhook sources")).toBeNull()
    expect(screen.queryByText("Delta Chat email adapters")).toBeNull()
  })

  it("revision-fences normalized policy changes", async () => {
    const user = userEvent.setup()
    vi.mocked(updateEventSourceSettings).mockResolvedValue({
      ...settingsResponse(),
      event_source_settings: {
        ...settingsResponse().event_source_settings,
        retention_days: 45,
        redact_fields: ["customer_number", "token"],
      },
      config_revision: "revision-2",
      effects: {
        launcher_effect: "applied",
        catalog_effect: "applied",
        gateway_effect: "restart_required",
      },
    })
    renderSettings()
    const retention = await screen.findByLabelText("Retention days")
    await user.clear(retention)
    await user.type(retention, "45")
    await user.type(
      screen.getByLabelText("Additional redacted fields"),
      ", token, CUSTOMER_NUMBER",
    )
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(updateEventSourceSettings).toHaveBeenCalledWith(
        expect.objectContaining({
          retention_days: 45,
          redact_fields: ["customer_number", "token"],
        }),
        "revision-1",
      ),
    )
  })

  it("uses the returned revision for a second save", async () => {
    const user = userEvent.setup()
    vi.mocked(updateEventSourceSettings)
      .mockResolvedValueOnce({
        ...settingsResponse(),
        event_source_settings: {
          ...settingsResponse().event_source_settings,
          retention_days: 45,
        },
        config_revision: "revision-2",
        effects: {
          launcher_effect: "applied",
          catalog_effect: "applied",
          gateway_effect: "restart_required",
        },
      })
      .mockResolvedValueOnce({
        ...settingsResponse(),
        event_source_settings: {
          ...settingsResponse().event_source_settings,
          retention_days: 60,
        },
        config_revision: "revision-3",
        effects: {
          launcher_effect: "applied",
          catalog_effect: "applied",
          gateway_effect: "restart_required",
        },
      })
    renderSettings()
    const retention = await screen.findByLabelText("Retention days")

    await user.clear(retention)
    await user.type(retention, "45")
    await user.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(retention).toHaveValue(45))

    await user.clear(retention)
    await user.type(retention, "60")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(updateEventSourceSettings).toHaveBeenCalledTimes(2),
    )
    expect(updateEventSourceSettings).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ retention_days: 60 }),
      "revision-2",
    )
  })

  it("keeps a dirty draft across background query refresh", async () => {
    const user = userEvent.setup()
    const { client } = renderSettings()
    const retention = await screen.findByLabelText("Retention days")
    await user.clear(retention)
    await user.type(retention, "45")

    act(() => {
      client.setQueryData(["event-source-settings"], {
        ...settingsResponse(),
        event_source_settings: {
          ...settingsResponse().event_source_settings,
          retention_days: 99,
        },
      })
    })
    expect(retention).toHaveValue(45)
  })

  it("rejects zero policy limits before mutation", async () => {
    const user = userEvent.setup()
    renderSettings()
    const retention = await screen.findByLabelText("Retention days")
    await user.clear(retention)
    await user.type(retention, "0")
    await user.click(screen.getByRole("button", { name: "Save" }))
    expect(
      await screen.findByText(
        "Retention days must be a positive whole number or blank.",
      ),
    ).toBeVisible()
    expect(updateEventSourceSettings).not.toHaveBeenCalled()
  })
})

function settingsResponse() {
  return {
    event_source_settings: {
      enabled: true,
      database_path: "eventing/events.db",
      retention_days: 30,
      max_payload_bytes: 1_048_576,
      redact_fields: ["customer_number"],
    },
    eligible_channel_adapters: [],
    config_revision: "revision-1",
  }
}

function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const result = render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <EventSourceSettingsPage onBack={vi.fn()} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  return { ...result, client }
}

function installBrowserPolyfills() {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  })
}
