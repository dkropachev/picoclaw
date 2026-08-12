import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { OAuthFlowState } from "@/api/oauth"
import { copyText } from "@/lib/clipboard"

import { DeviceCodeSheet } from "./device-code-sheet"

vi.mock("@/lib/clipboard", () => ({
  copyText: vi.fn(),
}))

const deviceFlow: OAuthFlowState = {
  flow_id: "device-renewal",
  provider: "openai",
  credential_id: "openai:work",
  method: "device_code",
  status: "pending",
  user_code: "ABCD-EFGH",
  verify_url: "https://auth.openai.com/device",
}

describe("DeviceCodeSheet", () => {
  beforeEach(() => {
    vi.mocked(copyText).mockReset()
    vi.mocked(copyText).mockResolvedValue(true)
  })

  it("copies each displayed login value independently with one click", async () => {
    const user = userEvent.setup()
    render(
      <DeviceCodeSheet
        open
        flow={deviceFlow}
        flowHint="Waiting for authorization..."
        onOpenChange={vi.fn()}
      />,
    )

    const copyCode = screen.getByRole("button", { name: "Copy User Code" })
    const copyURL = screen.getByRole("button", {
      name: "Copy Verification URL",
    })
    expect(screen.getAllByText("📋")).toHaveLength(2)
    expect(
      screen.getByRole("link", { name: "Open Verification Page" }),
    ).toHaveAttribute("href", deviceFlow.verify_url)

    await user.click(copyCode)
    await waitFor(() =>
      expect(copyText).toHaveBeenCalledWith(deviceFlow.user_code),
    )
    expect(
      screen.getByRole("button", { name: "User Code copied" }),
    ).toBeInTheDocument()
    expect(copyURL).toHaveAccessibleName("Copy Verification URL")

    await user.click(copyURL)
    await waitFor(() =>
      expect(copyText).toHaveBeenNthCalledWith(2, deviceFlow.verify_url),
    )
    expect(
      screen.getByRole("button", { name: "Verification URL copied" }),
    ).toBeInTheDocument()
  })

  it("does not copy placeholder values before a device flow is ready", () => {
    render(
      <DeviceCodeSheet open flow={null} flowHint="" onOpenChange={vi.fn()} />,
    )

    expect(
      screen.getByRole("button", { name: "Copy User Code" }),
    ).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Copy Verification URL" }),
    ).toBeDisabled()
    expect(screen.queryByRole("link")).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Open Verification Page" }),
    ).toBeDisabled()
    expect(screen.getByText("Starting device login...")).toBeInTheDocument()
  })

  it("announces clipboard failures and keeps both values retryable", async () => {
    const user = userEvent.setup()
    vi.mocked(copyText)
      .mockResolvedValueOnce(false)
      .mockRejectedValueOnce(new Error("clipboard unavailable"))
    render(
      <DeviceCodeSheet
        open
        flow={deviceFlow}
        flowHint="Waiting for authorization..."
        onOpenChange={vi.fn()}
      />,
    )

    const copyCode = screen.getByRole("button", { name: "Copy User Code" })
    await user.click(copyCode)
    expect(screen.getByText("Could not copy User Code")).toBeInTheDocument()
    expect(copyCode).toHaveTextContent("⚠️")
    expect(copyCode).toBeEnabled()

    const copyURL = screen.getByRole("button", {
      name: "Copy Verification URL",
    })
    await user.click(copyURL)
    expect(
      screen.getByText("Could not copy Verification URL"),
    ).toBeInTheDocument()
    expect(copyURL).toHaveTextContent("⚠️")
    expect(copyURL).toBeEnabled()
  })
})
