import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ChannelConfigResponse,
  type SupportedChannel,
  getChannelConfig,
  getChannelsCatalog,
  patchAppConfig,
} from "@/api/channels"
import { ChannelConfigPage } from "@/components/channels/channel-config-page"
import { useGateway } from "@/hooks/use-gateway"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

vi.mock("@/api/channels", () => ({
  getChannelConfig: vi.fn(),
  getChannelsCatalog: vi.fn(),
  patchAppConfig: vi.fn(),
}))

vi.mock("@/components/page-header", () => ({
  PageHeader: ({ title }: { title: string; children?: ReactNode }) => (
    <header>
      <h1>{title}</h1>
    </header>
  ),
}))

vi.mock("@/hooks/use-gateway", () => ({
  useGateway: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

const deltaChatChannel: SupportedChannel = {
  name: "deltachat",
  display_name: "Delta Chat",
  config_key: "deltachat",
}

const deltaChatConfig: ChannelConfigResponse = {
  config_key: "deltachat",
  configured_secrets: ["password"],
  config: {
    enabled: true,
    email: "operator@example.org",
    display_name: "Operations Bot",
    avatar_image: "/assets/delta-avatar.png",
    data_dir: "/var/lib/picoclaw/deltachat",
    rpc_server_path: "/usr/local/bin/deltachat-rpc-server",
    invite_link: "https://i.delta.chat/#operations",
    allow_crosspost: true,
    imap_server: "imap.example.org",
    imap_port: 993,
    smtp_server: "smtp.example.org",
    smtp_port: 465,
  },
}

const deltaChatDisabledCommonControls: ChannelConfigResponse = {
  ...deltaChatConfig,
  config: {
    ...deltaChatConfig.config,
    group_trigger: {
      mention_only: false,
      prefixes: [],
    },
    typing: {
      enabled: false,
    },
    placeholder: {
      enabled: false,
      text: [],
    },
  },
}

function getFieldInput(label: string): HTMLInputElement {
  const labelElement = screen.getByText(label, {
    exact: true,
    selector: "label",
  })
  const field = labelElement.closest('[data-slot="field"]')
  const input = field?.querySelector("input")
  if (!(input instanceof HTMLInputElement)) {
    throw new Error(`Input for "${label}" was not rendered`)
  }
  return input
}

describe("ChannelConfigPage Delta Chat editor", () => {
  beforeEach(() => {
    vi.mocked(getChannelsCatalog).mockReset()
    vi.mocked(getChannelConfig).mockReset()
    vi.mocked(patchAppConfig).mockReset()
    vi.mocked(useGateway).mockReset()
    vi.mocked(showSaveSuccessOrRestartToast).mockReset()
    vi.mocked(refreshGatewayState).mockReset()

    vi.mocked(getChannelsCatalog).mockResolvedValue({
      channels: [deltaChatChannel],
    })
    vi.mocked(getChannelConfig).mockResolvedValue(deltaChatConfig)
    vi.mocked(patchAppConfig).mockResolvedValue({ status: "ok" })
    vi.mocked(useGateway).mockReturnValue({
      state: "running",
      loading: false,
      canStart: true,
      startReason: undefined,
      restartRequired: false,
      start: vi.fn(async () => {}),
      stop: vi.fn(async () => {}),
      restart: vi.fn(async () => {}),
      error: null,
    })
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("renders required email, seeded optional fields, and a blank masked configured password", async () => {
    render(<ChannelConfigPage channelName="deltachat" />)

    expect(
      await screen.findByRole("heading", { name: "Delta Chat" }),
    ).toBeInTheDocument()
    expect(getFieldInput("Email")).toHaveValue("operator@example.org")
    expect(
      screen.getByText("Email", { exact: true, selector: "label" }),
    ).toHaveTextContent("*")

    const optionalFields: Array<[string, string]> = [
      ["Display Name", "Operations Bot"],
      ["Avatar Image", "/assets/delta-avatar.png"],
      ["Data Dir", "/var/lib/picoclaw/deltachat"],
      ["Rpc Server Path", "/usr/local/bin/deltachat-rpc-server"],
      ["Invite Link", "https://i.delta.chat/#operations"],
      ["Imap Server", "imap.example.org"],
      ["Smtp Server", "smtp.example.org"],
    ]
    for (const [label, value] of optionalFields) {
      expect(getFieldInput(label)).toHaveValue(value)
    }
    for (const [label, value] of [
      ["Imap Port", 993],
      ["Smtp Port", 465],
    ] as const) {
      expect(getFieldInput(label)).toHaveValue(value)
      expect(getFieldInput(label)).toHaveAttribute("type", "number")
      expect(getFieldInput(label)).toHaveAttribute("min", "0")
      expect(getFieldInput(label)).toHaveAttribute("max", "65535")
      expect(getFieldInput(label)).toHaveAttribute("step", "1")
    }
    expect(
      screen.getByRole("switch", { name: "Allow Crosspost" }),
    ).toBeChecked()

    const password = getFieldInput("Password")
    expect(password).toHaveAttribute("type", "password")
    expect(password).toHaveValue("")
    expect(password).toHaveAttribute(
      "placeholder",
      "A value is already set. Leave blank to keep it unchanged.",
    )
  })

  it("blocks saving when the required email is missing", async () => {
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    await user.clear(getFieldInput("Email"))
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText("This field is required."),
    ).toBeInTheDocument()
    expect(patchAppConfig).not.toHaveBeenCalled()
    expect(refreshGatewayState).not.toHaveBeenCalled()
  })

  it.each([
    ["negative", "-1"],
    ["fractional", "1.5"],
    ["out-of-range", "65536"],
  ])("blocks a %s Delta Chat port", async (_case, value) => {
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    fireEvent.change(getFieldInput("Imap Port"), {
      target: { value },
    })
    await user.click(screen.getByRole("button", { name: "Save" }))

    expect(
      await screen.findByText("Enter 0 or an integer from 1 to 65535."),
    ).toBeInTheDocument()
    expect(patchAppConfig).not.toHaveBeenCalled()
    expect(refreshGatewayState).not.toHaveBeenCalled()
  })

  it("accepts zero as an unset optional port and serializes a finite number", async () => {
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    fireEvent.change(getFieldInput("Imap Port"), {
      target: { value: "0" },
    })
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledTimes(1)
    })
    const patch = vi.mocked(patchAppConfig).mock.calls[0]?.[0]
    const settings = (
      patch?.channel_list as Record<string, Record<string, unknown>>
    )?.deltachat?.settings as Record<string, unknown>
    expect(settings.imap_port).toBe(0)
    expect(settings.imap_port).not.toBeNull()
    expect(Number.isNaN(settings.imap_port)).toBe(false)
  })

  it("preserves a configured password when its blank editor is untouched", async () => {
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    const displayName = getFieldInput("Display Name")
    await user.clear(displayName)
    await user.type(displayName, "Incident Desk")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledTimes(1)
    })
    const patch = vi.mocked(patchAppConfig).mock.calls[0]?.[0]
    expect(patch).toMatchObject({
      channel_list: {
        deltachat: {
          enabled: true,
          type: "deltachat",
          settings: {
            email: "operator@example.org",
            display_name: "Incident Desk",
          },
        },
      },
    })
    expect(
      (
        (patch?.channel_list as Record<string, Record<string, unknown>>)
          ?.deltachat?.settings as Record<string, unknown>
      ).password,
    ).toBeUndefined()
    expect(refreshGatewayState).toHaveBeenCalledWith({ force: true })
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalledWith(
      expect.any(Function),
      "Channel configuration saved.",
      "Delta Chat",
      false,
    )
  })

  it("preserves explicit false Delta Chat common controls during an unrelated save", async () => {
    vi.mocked(getChannelConfig).mockResolvedValue(
      deltaChatDisabledCommonControls,
    )
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    expect(
      screen.getByRole("switch", { name: "Group Mention Only" }),
    ).not.toBeChecked()
    expect(
      screen.getByRole("switch", { name: "Typing Indicator" }),
    ).not.toBeChecked()
    expect(
      screen.getByRole("switch", { name: "Placeholder Message" }),
    ).not.toBeChecked()

    const displayName = getFieldInput("Display Name")
    await user.clear(displayName)
    await user.type(displayName, "Unrelated Name Change")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledTimes(1)
    })
    expect(vi.mocked(patchAppConfig).mock.calls[0]?.[0]).toMatchObject({
      channel_list: {
        deltachat: {
          group_trigger: {
            mention_only: false,
          },
          typing: {
            enabled: false,
          },
          placeholder: {
            enabled: false,
          },
          settings: {
            display_name: "Unrelated Name Change",
          },
        },
      },
    })
  })

  it("sends a typed password replacement in Delta Chat settings", async () => {
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    await user.type(getFieldInput("Password"), "rotated-mail-password")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledWith(
        expect.objectContaining({
          channel_list: expect.objectContaining({
            deltachat: expect.objectContaining({
              settings: expect.objectContaining({
                password: "rotated-mail-password",
              }),
            }),
          }),
        }),
      )
    })
  })

  it("locks every editor control while a save is pending", async () => {
    let finishPatch: (() => void) | undefined
    vi.mocked(patchAppConfig).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finishPatch = () => resolve({ status: "ok" })
        }),
    )
    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    const displayName = getFieldInput("Display Name")
    await user.clear(displayName)
    await user.type(displayName, "Submitted Name")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledTimes(1)
    })
    const editorFieldset = displayName.closest("fieldset")
    expect(editorFieldset).toBeInstanceOf(HTMLFieldSetElement)
    expect(editorFieldset).toBeDisabled()
    for (const control of editorFieldset?.querySelectorAll(
      "button, input, select, textarea",
    ) ?? []) {
      expect(control).toBeDisabled()
    }

    await user.type(displayName, " Post-click Edit")
    expect(displayName).toHaveValue("Submitted Name")

    await act(async () => {
      finishPatch?.()
      await Promise.resolve()
    })
    await waitFor(() => {
      expect(showSaveSuccessOrRestartToast).toHaveBeenCalledTimes(1)
    })
  })

  it("clears a submitted password and reports a failed masked reload without a success toast", async () => {
    let rejectReload: ((reason?: unknown) => void) | undefined
    const reload = new Promise<ChannelConfigResponse>((_, reject) => {
      rejectReload = reject
    })
    vi.mocked(getChannelConfig)
      .mockReset()
      .mockResolvedValueOnce(deltaChatConfig)
      .mockReturnValueOnce(reload)

    render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()

    await screen.findByRole("heading", { name: "Delta Chat" })
    await user.type(getFieldInput("Password"), "rotated-mail-password")
    await user.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => {
      expect(patchAppConfig).toHaveBeenCalledTimes(1)
      expect(getChannelConfig).toHaveBeenCalledTimes(2)
      expect(getFieldInput("Password")).toHaveValue("")
    })
    expect(
      screen.queryByDisplayValue("rotated-mail-password"),
    ).not.toBeInTheDocument()

    await act(async () => {
      rejectReload?.(new Error("masked channel reload failed"))
      await Promise.resolve()
    })

    expect(
      await screen.findByText("masked channel reload failed"),
    ).toBeInTheDocument()
    expect(
      screen.queryByDisplayValue("rotated-mail-password"),
    ).not.toBeInTheDocument()
    expect(refreshGatewayState).not.toHaveBeenCalled()
    expect(showSaveSuccessOrRestartToast).not.toHaveBeenCalled()
  })

  it("fails a save whose strict masked reload is superseded", async () => {
    let finishStrictReload:
      | ((response: ChannelConfigResponse) => void)
      | undefined
    const strictReload = new Promise<ChannelConfigResponse>((resolve) => {
      finishStrictReload = resolve
    })
    vi.mocked(getChannelConfig)
      .mockReset()
      .mockResolvedValueOnce(deltaChatConfig)
      .mockReturnValueOnce(strictReload)
      .mockResolvedValueOnce(deltaChatConfig)
    vi.mocked(useGateway).mockReturnValue({
      state: "stopped",
      loading: false,
      canStart: true,
      startReason: undefined,
      restartRequired: false,
      start: vi.fn(async () => {}),
      stop: vi.fn(async () => {}),
      restart: vi.fn(async () => {}),
      error: null,
    })

    const { rerender } = render(<ChannelConfigPage channelName="deltachat" />)
    const user = userEvent.setup()
    await screen.findByRole("heading", { name: "Delta Chat" })
    const displayName = getFieldInput("Display Name")
    await user.clear(displayName)
    await user.type(displayName, "Superseded Save")
    await user.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => {
      expect(getChannelConfig).toHaveBeenCalledTimes(2)
    })

    vi.mocked(useGateway).mockReturnValue({
      state: "running",
      loading: false,
      canStart: true,
      startReason: undefined,
      restartRequired: false,
      start: vi.fn(async () => {}),
      stop: vi.fn(async () => {}),
      restart: vi.fn(async () => {}),
      error: null,
    })
    rerender(<ChannelConfigPage channelName="deltachat" />)
    await waitFor(() => {
      expect(getChannelConfig).toHaveBeenCalledTimes(3)
    })

    await act(async () => {
      finishStrictReload?.(deltaChatConfig)
      await Promise.resolve()
    })

    expect(
      await screen.findByText(
        "Channel configuration reload was superseded. Please try again.",
      ),
    ).toBeInTheDocument()
    expect(refreshGatewayState).not.toHaveBeenCalled()
    expect(showSaveSuccessOrRestartToast).not.toHaveBeenCalled()
  })
})
