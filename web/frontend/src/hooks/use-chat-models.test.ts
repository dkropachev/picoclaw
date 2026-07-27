import { renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { type ModelInfo, getModels, setDefaultModel } from "@/api/models"
import { useChatModels } from "@/hooks/use-chat-models"

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}))

vi.mock("@/api/models", () => ({
  getModels: vi.fn(),
  setDefaultModel: vi.fn(),
}))

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

function model(overrides: Partial<ModelInfo>): ModelInfo {
  return {
    index: 0,
    model_name: "model",
    model: "model",
    api_key: "",
    enabled: true,
    available: true,
    status: "available",
    is_default: false,
    is_virtual: false,
    ...overrides,
  }
}

describe("useChatModels", () => {
  beforeEach(() => {
    vi.mocked(getModels).mockReset()
    vi.mocked(setDefaultModel).mockReset()
  })

  it("keeps virtual account routers selectable and excludes other virtual models", async () => {
    vi.mocked(getModels).mockResolvedValue({
      models: [
        model({
          index: 0,
          model_name: "router-1",
          provider: "router",
          router: { name: "router-1", model: "gpt-5.5" },
          is_virtual: true,
        }),
        model({
          index: 1,
          model_name: "internal-virtual",
          is_virtual: true,
        }),
        model({
          index: 2,
          model_name: "gpt-api",
          provider: "openai",
          available: false,
          status: "unconfigured",
        }),
        model({
          index: 3,
          model_name: "credential:openai:work",
          provider: "openai",
          auth_method: "oauth",
          credential_id: "openai:work",
          is_virtual: true,
        }),
        model({
          index: 4,
          model_name: "credential:github-copilot:gh-copilot",
          provider: "github-copilot",
          auth_method: "token",
          credential_id: "github-copilot:gh-copilot",
          is_virtual: true,
        }),
      ],
      total: 3,
      default_model: "router-1",
      provider_options: [],
    })

    const { result } = renderHook(() => useChatModels({ isConnected: true }))

    await waitFor(() => {
      expect(result.current.defaultModelName).toBe("router-1")
    })

    expect(result.current.accountRouterModels.map((m) => m.model_name)).toEqual(
      ["router-1"],
    )
    expect(result.current.apiKeyModels.map((m) => m.model_name)).toEqual([
      "gpt-api",
      "credential:github-copilot:gh-copilot",
    ])
    expect(result.current.oauthModels.map((m) => m.model_name)).toEqual([
      "credential:openai:work",
    ])
  })
})
