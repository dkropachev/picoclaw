import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { getAccount } from "@/api/accounts"
import type { OAuthProviderStatus } from "@/api/oauth"
import { useCredentialsPage } from "@/hooks/use-credentials-page"

import { AccountAuthEditorPage } from "./account-auth-editor-page"

vi.mock("@/api/accounts", () => ({ getAccount: vi.fn() }))
vi.mock("@/hooks/use-credentials-page", () => ({
  useCredentialsPage: vi.fn(),
}))
vi.mock("@/components/collection", () => ({
  CollectionDetailShell: ({
    children,
    title,
    loading,
    error,
    notFound,
  }: {
    children: ReactNode
    title: string
    loading?: boolean
    error?: string
    notFound?: boolean
  }) => (
    <main>
      <h1>{title}</h1>
      {loading ? (
        <output>Loading</output>
      ) : error ? (
        <output>{error}</output>
      ) : notFound ? (
        <output>Not found</output>
      ) : (
        children
      )}
    </main>
  ),
}))
vi.mock("./device-code-sheet", () => ({ DeviceCodeSheet: () => null }))

const getAccountMock = vi.mocked(getAccount)
const useCredentialsPageMock = vi.mocked(useCredentialsPage)

const providers: OAuthProviderStatus[] = [
  {
    provider: "openai",
    credential_id: "openai",
    display_name: "OpenAI",
    methods: ["browser", "device_code", "token"],
    logged_in: false,
    status: "not_logged_in",
    credentials: [
      {
        provider: "openai",
        credential_id: "openai:work",
        display_name: "OpenAI",
        methods: ["browser", "device_code", "token"],
        logged_in: true,
        status: "expired",
        auth_method: "token",
      },
    ],
  },
  {
    provider: "anthropic",
    credential_id: "anthropic",
    display_name: "Anthropic",
    methods: ["token"],
    logged_in: false,
    status: "not_logged_in",
  },
]

function credentialController() {
  return {
    providers,
    loading: false,
    error: "",
    credentialsRevision: 0,
    activeAction: "",
    activeFlow: null,
    flowHint: "",
    deviceSheetOpen: false,
    deviceFlow: null,
    logoutDialogOpen: false,
    logoutConfirmProvider: "" as const,
    logoutProviderLabel: "",
    startBrowserOAuth: vi.fn().mockResolvedValue(true),
    startOpenAIDeviceCode: vi.fn().mockResolvedValue(true),
    saveToken: vi.fn().mockResolvedValue(true),
    stopLoading: vi.fn(),
    clearError: vi.fn(),
    askLogout: vi.fn(),
    handleConfirmLogout: vi.fn(),
    handleLogoutDialogOpenChange: vi.fn(),
    handleDeviceSheetOpenChange: vi.fn(),
  }
}

function renderEditor(element: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>,
  )
}

describe("AccountAuthEditorPage", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    vi.clearAllMocks()
    useCredentialsPageMock.mockReturnValue(
      credentialController() as ReturnType<typeof useCredentialsPage>,
    )
  })

  it("loads renewal directly by opaque ID and locks the exact account identity", async () => {
    getAccountMock.mockResolvedValue({
      account: {
        id: "opaque-hash",
        provider: "openai",
        account: "openai:work",
        status: "expired",
        auth_method: "token",
        expires_at: "",
      },
    })
    const controller = credentialController()
    useCredentialsPageMock.mockReturnValue(
      controller as ReturnType<typeof useCredentialsPage>,
    )

    renderEditor(
      <AccountAuthEditorPage
        mode="edit"
        id="opaque-hash"
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(await screen.findByLabelText("Credential ID")).toHaveValue(
      "openai:work",
    )
    expect(screen.getByLabelText("Credential ID")).toHaveProperty(
      "readOnly",
      true,
    )
    expect(screen.getByLabelText("Provider")).toHaveProperty("readOnly", true)
    expect(screen.getByLabelText("Login Method")).toHaveProperty(
      "readOnly",
      true,
    )
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()

    const user = userEvent.setup()
    await user.type(screen.getByLabelText("Token"), "replacement-token")
    await user.click(screen.getByRole("button", { name: "Save New Token" }))

    await waitFor(() =>
      expect(controller.saveToken).toHaveBeenCalledWith(
        "openai",
        "replacement-token",
        "openai:work",
      ),
    )
    expect(getAccountMock).toHaveBeenCalledWith(
      "opaque-hash",
      expect.any(AbortSignal),
    )
  })

  it("keeps provider choices on New and validates token account names before login", async () => {
    const controller = credentialController()
    useCredentialsPageMock.mockReturnValue(
      controller as ReturnType<typeof useCredentialsPage>,
    )
    renderEditor(
      <AccountAuthEditorPage
        mode="create"
        onBack={vi.fn()}
        onSaved={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole("combobox", { name: "Login Method" }),
    )
    await user.click(screen.getByRole("option", { name: "Token" }))
    await user.type(screen.getByLabelText("Account Name"), "private name")
    await user.type(screen.getByLabelText("Token"), "secret-token")
    await user.click(screen.getByRole("button", { name: "Save Account" }))

    expect(
      screen.getByText(
        "Use only letters, numbers, dots, dashes, or underscores.",
      ),
    ).toBeVisible()
    expect(controller.saveToken).not.toHaveBeenCalled()
    expect(getAccountMock).not.toHaveBeenCalled()
  })
})
