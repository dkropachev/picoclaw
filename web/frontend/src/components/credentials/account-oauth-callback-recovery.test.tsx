import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { useCredentialsPage } from "@/hooks/use-credentials-page"

import { AccountOAuthCallbackRecovery } from "./account-oauth-callback-recovery"

const { toastError, toastSuccess } = vi.hoisted(() => ({
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { error: toastError, success: toastSuccess },
}))
vi.mock("@/hooks/use-credentials-page", () => ({
  useCredentialsPage: vi.fn(),
}))

const useCredentialsPageMock = vi.mocked(useCredentialsPage)

function credentialState(error: string) {
  return {
    credentialsRevision: 0,
    error,
  } as ReturnType<typeof useCredentialsPage>
}

function renderRecovery(error: string) {
  useCredentialsPageMock.mockImplementation((options) => {
    options?.onOAuthCallbackConsumed?.()
    return credentialState(error)
  })
  const queryClient = new QueryClient()
  const onConsumed = vi.fn()
  render(
    <QueryClientProvider client={queryClient}>
      <AccountOAuthCallbackRecovery
        flowID="terminal-flow"
        onConsumed={onConsumed}
      />
    </QueryClientProvider>,
  )
  return onConsumed
}

describe("AccountOAuthCallbackRecovery terminal notifications", () => {
  beforeEach(() => vi.clearAllMocks())

  it.each([
    ["error", `Provider rejected login\n${"x".repeat(800)}`],
    ["expired", "Account login expired."],
  ])("consumes and reports a bounded safe %s result", async (_, message) => {
    const onConsumed = renderRecovery(message)

    await waitFor(() => expect(onConsumed).toHaveBeenCalledOnce())
    await waitFor(() => expect(toastError).toHaveBeenCalledOnce())
    const reported = String(toastError.mock.calls[0]?.[0])
    expect(reported).not.toMatch(/[\r\n\t]/)
    expect(reported.length).toBeLessThanOrEqual(512)
    expect(toastSuccess).not.toHaveBeenCalled()
  })
})
