import { act, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, describe, expect, it, vi } from "vitest"

import type { ModelProviderOption } from "@/api/models"
import type { OAuthProviderStatus } from "@/api/oauth"

import { AccountOnboardingSheet } from "./account-onboarding-sheet"

const openAIProvider: OAuthProviderStatus = {
  provider: "openai",
  credential_id: "openai",
  display_name: "OpenAI",
  methods: ["browser", "device_code", "token"],
  logged_in: true,
  status: "expired",
  auth_method: "oauth",
}

const anthropicProvider: OAuthProviderStatus = {
  provider: "anthropic",
  credential_id: "anthropic",
  display_name: "Anthropic",
  methods: ["token"],
  logged_in: true,
  status: "expired",
  auth_method: "token",
}

const googleProvider: OAuthProviderStatus = {
  provider: "google-antigravity",
  credential_id: "google-antigravity",
  display_name: "Google Antigravity",
  methods: ["browser"],
  logged_in: true,
  status: "expired",
  auth_method: "oauth",
}

interface RenderRenewalOptions {
  account?: OAuthProviderStatus
  open?: boolean
  providers?: OAuthProviderStatus[]
  providerOptions?: ModelProviderOption[]
  activeAction?: string
  error?: string
}

function renderRenewal(options: RenderRenewalOptions) {
  const { account, providers, providerOptions, activeAction, error } = options
  const callbacks = {
    onOpenChange: vi.fn(),
    onStartBrowserOAuth: vi.fn().mockResolvedValue(true),
    onStartDeviceCode: vi.fn().mockResolvedValue(true),
    onSaveToken: vi.fn().mockResolvedValue(true),
  }

  const view = render(
    <AccountOnboardingSheet
      open={options.open ?? true}
      account={account}
      providers={providers ?? [openAIProvider, anthropicProvider]}
      providerOptions={providerOptions ?? []}
      registeredAccounts={account ? [account] : []}
      activeAction={activeAction ?? ""}
      error={error}
      {...callbacks}
    />,
  )

  return {
    ...callbacks,
    rerenderRenewal(next: RenderRenewalOptions) {
      view.rerender(
        <AccountOnboardingSheet
          open={next.open ?? true}
          account={next.account}
          providers={next.providers ?? [openAIProvider, anthropicProvider]}
          providerOptions={next.providerOptions ?? []}
          registeredAccounts={next.account ? [next.account] : []}
          activeAction={next.activeAction ?? ""}
          error={next.error}
          {...callbacks}
        />,
      )
    },
  }
}

async function renewalControls() {
  const provider = await screen.findByLabelText("Provider")
  const method = screen.getByLabelText("Login Method")
  const credentialID = screen.getByLabelText("Credential ID")

  return { provider, method, credentialID }
}

describe("AccountOnboardingSheet renewal mode", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: {
        configurable: true,
        value: vi.fn(() => false),
      },
      setPointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      releasePointerCapture: {
        configurable: true,
        value: vi.fn(),
      },
      scrollIntoView: {
        configurable: true,
        value: vi.fn(),
      },
    })
  })

  it("renews a named token account using its exact credential ID", async () => {
    const user = userEvent.setup()
    const account: OAuthProviderStatus = {
      ...anthropicProvider,
      credential_id: "anthropic:work",
    }
    const callbacks = renderRenewal({ account })
    const controls = await renewalControls()

    expect(
      screen.getByRole("heading", { name: "Renew Account Login" }),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        "Replace the saved login for this account without changing its identity or routing.",
      ),
    ).toBeInTheDocument()
    expect(controls.provider).toHaveValue("Anthropic")
    expect(controls.provider).toHaveProperty("readOnly", true)
    expect(controls.credentialID).toHaveValue("anthropic:work")
    expect(controls.credentialID).toHaveProperty("readOnly", true)
    expect(controls.method).toHaveValue("Token")
    expect(controls.method).toHaveProperty("readOnly", true)
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()

    await user.type(
      screen.getByPlaceholderText("Anthropic token"),
      "replacement-token",
    )
    await user.click(screen.getByRole("button", { name: "Save New Token" }))

    await waitFor(() =>
      expect(callbacks.onSaveToken).toHaveBeenCalledWith(
        "anthropic",
        "replacement-token",
        "anthropic:work",
      ),
    )
    expect(callbacks.onStartBrowserOAuth).not.toHaveBeenCalled()
    expect(callbacks.onStartDeviceCode).not.toHaveBeenCalled()
    expect(callbacks.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("locks Codex renewal to device login", async () => {
    const user = userEvent.setup()
    const callbacks = renderRenewal({ account: openAIProvider })
    const controls = await renewalControls()

    expect(controls.provider).toHaveValue("OpenAI")
    expect(controls.provider).toHaveProperty("readOnly", true)
    expect(controls.credentialID).toHaveValue("openai")
    expect(controls.credentialID).toHaveProperty("readOnly", true)
    expect(controls.method).toHaveValue("Device Code")
    expect(controls.method).toHaveProperty("readOnly", true)
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Start Renewal" }))

    await waitFor(() =>
      expect(callbacks.onStartDeviceCode).toHaveBeenCalledWith("openai"),
    )
    expect(callbacks.onStartBrowserOAuth).not.toHaveBeenCalled()
    expect(callbacks.onSaveToken).not.toHaveBeenCalled()
    expect(callbacks.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("starts browser renewal for the exact named credential ID", async () => {
    const user = userEvent.setup()
    const account: OAuthProviderStatus = {
      ...googleProvider,
      credential_id: "google-antigravity:work",
    }
    const callbacks = renderRenewal({ account })

    await user.click(
      await screen.findByRole("button", { name: "Start Renewal" }),
    )

    await waitFor(() =>
      expect(callbacks.onStartBrowserOAuth).toHaveBeenCalledWith(
        "google-antigravity",
        "google-antigravity:work",
      ),
    )
    expect(callbacks.onOpenChange).toHaveBeenCalledWith(false)
  })

  it("keeps a rejected replacement error visible inside the renewal sheet", async () => {
    renderRenewal({
      account: { ...anthropicProvider, credential_id: "anthropic:work" },
      error: "Replacement token was rejected",
    })

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Replacement token was rejected",
    )
  })

  it("preserves entered token when provider options arrive asynchronously", async () => {
    const user = userEvent.setup()
    const account = {
      ...anthropicProvider,
      credential_id: "anthropic:work",
    }
    const view = renderRenewal({ account })

    const tokenInput = await screen.findByLabelText("Token")
    await user.type(tokenInput, "replacement-token")

    view.rerenderRenewal({
      account,
      providerOptions: [
        {
          id: "deepseek",
          display_name: "DeepSeek",
          default_api_base: "https://api.deepseek.example/v1",
          empty_api_key_allowed: false,
          create_allowed: true,
          default_auth_method: "api_key",
        },
      ],
    })

    expect(screen.getByLabelText("Token")).toHaveValue("replacement-token")
  })

  it("clears names, tokens, and validation errors after closing", async () => {
    const user = userEvent.setup()
    const view = renderRenewal({})

    await user.click(
      await screen.findByRole("combobox", { name: "Login Method" }),
    )
    await user.click(await screen.findByRole("option", { name: "Token" }))
    await user.type(screen.getByLabelText("Account Name"), "private name")
    await user.type(screen.getByLabelText("Token"), "private-token")
    await user.click(screen.getByRole("button", { name: "Save Account" }))

    expect(
      screen.getByText(
        "Use only letters, numbers, dots, dashes, or underscores.",
      ),
    ).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(view.onOpenChange).toHaveBeenCalledWith(false)
    view.rerenderRenewal({ open: false })
    view.rerenderRenewal({ open: true })

    expect(await screen.findByLabelText("Account Name")).toHaveValue("")
    expect(
      screen.queryByText(
        "Use only letters, numbers, dots, dashes, or underscores.",
      ),
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole("combobox", { name: "Login Method" }))
    await user.click(await screen.findByRole("option", { name: "Token" }))
    expect(screen.getByLabelText("Token")).toHaveValue("")
  })

  it("blocks dismissal while submitting and ignores a stale completion", async () => {
    const user = userEvent.setup()
    let resolveBrowser!: (ok: boolean) => void
    const pendingBrowser = new Promise<boolean>((resolve) => {
      resolveBrowser = resolve
    })
    const account = {
      ...googleProvider,
      credential_id: "google-antigravity:work",
    }
    const nextAccount = {
      ...anthropicProvider,
      credential_id: "anthropic:next",
    }
    const view = renderRenewal({ account })
    view.onStartBrowserOAuth.mockReturnValueOnce(pendingBrowser)

    await user.click(
      await screen.findByRole("button", { name: "Start Renewal" }),
    )
    await waitFor(() =>
      expect(view.onStartBrowserOAuth).toHaveBeenCalledWith(
        "google-antigravity",
        "google-antigravity:work",
      ),
    )
    expect(screen.getByRole("button", { name: "Start Renewal" })).toBeDisabled()

    view.onOpenChange.mockClear()
    await user.click(screen.getByRole("button", { name: "Close" }))
    expect(view.onOpenChange).not.toHaveBeenCalled()
    expect(
      screen.getByRole("heading", { name: "Renew Account Login" }),
    ).toBeInTheDocument()

    view.rerenderRenewal({ account, open: false })
    view.rerenderRenewal({ account: nextAccount, open: true })
    expect(
      await screen.findByDisplayValue("anthropic:next"),
    ).toBeInTheDocument()

    await act(async () => {
      resolveBrowser(true)
      await pendingBrowser
    })

    expect(view.onOpenChange).not.toHaveBeenCalled()
    expect(screen.getByDisplayValue("anthropic:next")).toBeInTheDocument()
  })
})
