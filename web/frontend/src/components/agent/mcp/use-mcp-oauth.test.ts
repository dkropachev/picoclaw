import { act, renderHook, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { getMCPOAuthFlow, startMCPServerOAuth } from "@/api/mcp"

import { useMCPOAuth } from "./use-mcp-oauth"

vi.mock("@/api/mcp", () => ({
  getMCPOAuthFlow: vi.fn(),
  startMCPServerOAuth: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}))

const mockedStartOAuth = vi.mocked(startMCPServerOAuth)
const mockedGetOAuthFlow = vi.mocked(getMCPOAuthFlow)

describe("useMCPOAuth", () => {
  beforeEach(() => {
    mockedStartOAuth.mockReset()
    mockedGetOAuthFlow.mockReset()
    vi.mocked(toast.error).mockReset()
  })

  it("opens the authorization URL and refreshes after a successful flow", async () => {
    mockedStartOAuth.mockResolvedValue({
      status: "ok",
      flow_id: "flow-1",
      server_name: "github",
      auth_url: "https://github.example/authorize",
      expires_at: "2030-01-01T00:00:00Z",
    })
    mockedGetOAuthFlow.mockResolvedValue({
      flow_id: "flow-1",
      server_name: "github",
      status: "success",
      expires_at: "2030-01-01T00:00:00Z",
      tool_count: 7,
    })

    const onSuccess = vi.fn()
    const popup = {
      closed: false,
      close: vi.fn(),
      location: { href: "" },
    } as unknown as Window
    const { result } = renderHook(() => useMCPOAuth(onSuccess))

    await act(async () => {
      await result.current.startLogin("github", popup)
    })

    expect(mockedStartOAuth).toHaveBeenCalledWith("github")
    expect(popup.location.href).toBe("https://github.example/authorize")
    await waitFor(() => {
      expect(onSuccess).toHaveBeenCalledWith(
        expect.objectContaining({
          flow_id: "flow-1",
          status: "success",
          tool_count: 7,
        }),
      )
    })
    expect(popup.close).toHaveBeenCalled()
  })

  it("stops waiting when the login popup is closed", async () => {
    mockedStartOAuth.mockResolvedValue({
      status: "ok",
      flow_id: "flow-closed",
      server_name: "github",
      auth_url: "https://github.example/authorize",
      expires_at: "2030-01-01T00:00:00Z",
    })
    mockedGetOAuthFlow.mockResolvedValue({
      flow_id: "flow-closed",
      server_name: "github",
      status: "pending",
      expires_at: "2030-01-01T00:00:00Z",
    })

    const popup = {
      closed: true,
      close: vi.fn(),
      location: { href: "" },
    } as unknown as Window
    const { result } = renderHook(() => useMCPOAuth(vi.fn()))

    await act(async () => {
      await result.current.startLogin("github", popup)
    })

    await waitFor(() => expect(result.current.loggingIn).toBe(false))
    expect(toast.error).toHaveBeenCalledWith(
      "The login window was closed before authentication finished.",
    )
  })
})
