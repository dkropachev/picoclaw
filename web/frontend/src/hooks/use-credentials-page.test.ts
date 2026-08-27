import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type OAuthFlowState,
  type OAuthLoginResponse,
  getOAuthFlow,
  getOAuthProviders,
  loginOAuth,
  pollOAuthFlow,
} from "@/api/oauth"

import { useCredentialsPage } from "./use-credentials-page"

vi.mock("react-i18next", () => {
  const t = (key: string) => key
  return { useTranslation: () => ({ t }) }
})

vi.mock("@/api/oauth", () => ({
  getOAuthFlow: vi.fn(),
  getOAuthProviders: vi.fn(),
  loginOAuth: vi.fn(),
  logoutOAuth: vi.fn(),
  pollOAuthFlow: vi.fn(),
}))

interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: Error) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((onResolve, onReject) => {
    resolve = onResolve
    reject = onReject
  })
  return { promise, resolve, reject }
}

function browserLogin(flowID: string): OAuthLoginResponse {
  return {
    status: "pending",
    provider: "google-antigravity",
    credential_id: "google-antigravity:work",
    method: "browser",
    flow_id: flowID,
    auth_url: "https://accounts.example/authorize",
  }
}

function browserFlow(
  flowID: string,
  status: OAuthFlowState["status"],
): OAuthFlowState {
  return {
    flow_id: flowID,
    provider: "google-antigravity",
    credential_id: "google-antigravity:work",
    method: "browser",
    status,
  }
}

function openAuthPopup() {
  const popup = {
    close: vi.fn(),
    location: { href: "" },
  } as unknown as Window
  vi.spyOn(window, "open").mockReturnValue(popup)
  return popup
}

describe("useCredentialsPage OAuth flow fencing", () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.mocked(getOAuthProviders).mockReset()
    vi.mocked(getOAuthProviders).mockResolvedValue({ providers: [] })
    vi.mocked(loginOAuth).mockReset()
    vi.mocked(getOAuthFlow).mockReset()
    vi.mocked(pollOAuthFlow).mockReset()
  })

  it("ignores a stopped browser popup and completion during a token renewal", async () => {
    const staleBrowser = deferred<OAuthFlowState>()
    const tokenRenewal = deferred<OAuthLoginResponse>()
    const popup = openAuthPopup()
    vi.mocked(loginOAuth).mockImplementation((request) => {
      if (request.method === "browser") {
        return Promise.resolve(browserLogin("flow-a"))
      }
      return tokenRenewal.promise
    })
    vi.mocked(getOAuthFlow).mockReturnValue(staleBrowser.promise)

    const { result } = renderHook(() => useCredentialsPage())
    await waitFor(() => expect(result.current.loading).toBe(false))
    const providerLoadsBeforeRenewal =
      vi.mocked(getOAuthProviders).mock.calls.length

    await act(async () => {
      expect(
        await result.current.startBrowserOAuth(
          "google-antigravity",
          "google-antigravity:work",
        ),
      ).toBe(true)
    })
    expect(popup.location.href).toBe("https://accounts.example/authorize")
    await waitFor(() => expect(getOAuthFlow).toHaveBeenCalledWith("flow-a"))

    act(() => result.current.stopLoading())

    let savePromise!: Promise<boolean>
    act(() => {
      savePromise = result.current.saveToken(
        "anthropic",
        "replacement-token",
        "anthropic:work",
      )
    })
    await waitFor(() =>
      expect(result.current.activeAction).toBe("anthropic:token"),
    )

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { type: "picoclaw-oauth-result", flowId: "flow-a" },
        }),
      )
    })
    await act(async () => {
      staleBrowser.resolve(browserFlow("flow-a", "success"))
      await Promise.resolve()
    })

    expect(getOAuthFlow).toHaveBeenCalledTimes(1)
    expect(getOAuthProviders).toHaveBeenCalledTimes(providerLoadsBeforeRenewal)
    expect(result.current.activeAction).toBe("anthropic:token")
    expect(result.current.activeFlow).toBeNull()
    expect(result.current.credentialsRevision).toBe(0)
    expect(result.current.error).toBe("")

    await act(async () => {
      tokenRenewal.resolve({
        status: "success",
        provider: "anthropic",
        credential_id: "anthropic:work",
        method: "token",
      })
      expect(await savePromise).toBe(true)
    })

    expect(result.current.activeAction).toBe("")
    expect(result.current.credentialsRevision).toBe(1)
    expect(getOAuthProviders).toHaveBeenCalledTimes(
      providerLoadsBeforeRenewal + 1,
    )
  })

  it("recovers an explicit hard-navigation callback without relying on window search", async () => {
    const onOAuthCallbackConsumed = vi.fn()
    vi.mocked(getOAuthFlow).mockResolvedValue({
      flow_id: "flow-hard-navigation",
      provider: "google-antigravity",
      credential_id: "google-antigravity:work",
      method: "browser",
      status: "success",
    })

    const { result } = renderHook(() =>
      useCredentialsPage({
        oauthCallbackFlowID: "flow-hard-navigation",
        onOAuthCallbackConsumed,
      }),
    )

    await waitFor(() =>
      expect(getOAuthFlow).toHaveBeenCalledWith("flow-hard-navigation"),
    )
    expect(onOAuthCallbackConsumed).toHaveBeenCalledOnce()
    await waitFor(() => expect(result.current.credentialsRevision).toBe(1))
    expect(result.current.activeFlow?.status).toBe("success")
  })

  it.each([
    {
      status: "error" as const,
      flowError: `Provider rejected login\n${"x".repeat(800)}`,
      expectedPrefix: "Provider rejected login ",
    },
    {
      status: "expired" as const,
      flowError: "",
      expectedPrefix: "credentials.flow.expired",
    },
  ])(
    "reports a bounded safe terminal $status callback after consuming it",
    async ({ status, flowError, expectedPrefix }) => {
      const onOAuthCallbackConsumed = vi.fn()
      vi.mocked(getOAuthFlow).mockResolvedValue({
        flow_id: `flow-${status}`,
        provider: "google-antigravity",
        credential_id: "google-antigravity:work",
        method: "browser",
        status,
        error: flowError,
      })

      const { result } = renderHook(() =>
        useCredentialsPage({
          oauthCallbackFlowID: `flow-${status}`,
          onOAuthCallbackConsumed,
        }),
      )

      expect(onOAuthCallbackConsumed).toHaveBeenCalledOnce()
      await waitFor(() =>
        expect(result.current.activeFlow?.status).toBe(status),
      )
      expect(result.current.error).toMatch(new RegExp(`^${expectedPrefix}`))
      expect(result.current.error).not.toMatch(/[\r\n\t]/)
      expect(result.current.error.length).toBeLessThanOrEqual(512)
      expect(result.current.credentialsRevision).toBe(0)
    },
  )

  it("ignores a stale browser error during device renewal and fences canceled device completion", async () => {
    const staleBrowser = deferred<OAuthFlowState>()
    const deviceRenewal = deferred<OAuthFlowState>()
    openAuthPopup()
    vi.mocked(loginOAuth)
      .mockResolvedValueOnce(browserLogin("flow-a"))
      .mockResolvedValueOnce({
        status: "pending",
        provider: "openai",
        credential_id: "openai:work",
        method: "device_code",
        flow_id: "flow-b",
        user_code: "ABCD-EFGH",
        verify_url: "https://openai.example/device",
        interval: 5,
      })
    vi.mocked(getOAuthFlow).mockReturnValue(staleBrowser.promise)
    vi.mocked(pollOAuthFlow).mockReturnValue(deviceRenewal.promise)

    const { result } = renderHook(() => useCredentialsPage())
    await waitFor(() => expect(result.current.loading).toBe(false))
    const providerLoadsBeforeRenewal =
      vi.mocked(getOAuthProviders).mock.calls.length

    await act(async () => {
      await result.current.startBrowserOAuth(
        "google-antigravity",
        "google-antigravity:work",
      )
    })
    await waitFor(() => expect(getOAuthFlow).toHaveBeenCalledWith("flow-a"))
    act(() => result.current.stopLoading())

    await act(async () => {
      expect(
        await result.current.startOpenAIDeviceCode("openai:work", {
          openImmediately: true,
        }),
      ).toBe(true)
    })
    await waitFor(() => expect(pollOAuthFlow).toHaveBeenCalledWith("flow-b"))
    expect(result.current.activeAction).toBe("openai:device")

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          data: { type: "picoclaw-oauth-result", flowId: "flow-a" },
        }),
      )
    })
    await act(async () => {
      staleBrowser.reject(new Error("late flow A error"))
      await Promise.resolve()
    })

    expect(getOAuthFlow).toHaveBeenCalledTimes(1)
    expect(result.current.activeAction).toBe("openai:device")
    expect(result.current.deviceSheetOpen).toBe(true)
    expect(result.current.deviceFlow?.flow_id).toBe("flow-b")
    expect(result.current.error).toBe("")
    expect(getOAuthProviders).toHaveBeenCalledTimes(providerLoadsBeforeRenewal)

    act(() => result.current.handleDeviceSheetOpenChange(false))
    expect(result.current.activeAction).toBe("")
    expect(result.current.deviceSheetOpen).toBe(false)
    expect(result.current.deviceFlow).toBeNull()

    await act(async () => {
      deviceRenewal.resolve({
        flow_id: "flow-b",
        provider: "openai",
        credential_id: "openai:work",
        method: "device_code",
        status: "success",
      })
      await Promise.resolve()
    })

    expect(result.current.activeFlow).toBeNull()
    expect(result.current.credentialsRevision).toBe(0)
    expect(getOAuthProviders).toHaveBeenCalledTimes(providerLoadsBeforeRenewal)
  })
})
