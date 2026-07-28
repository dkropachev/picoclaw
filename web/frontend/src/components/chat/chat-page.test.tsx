import { render, screen, waitFor } from "@testing-library/react"
import { Provider } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { getThread } from "@/api/threads"
import { ChatPage } from "@/components/chat/chat-page"
import { useChatModels } from "@/hooks/use-chat-models"
import { useGateway } from "@/hooks/use-gateway"
import { usePicoChat } from "@/hooks/use-pico-chat"
import { useSessionHistory } from "@/hooks/use-session-history"

vi.mock("@/api/threads", () => ({
  getThread: vi.fn(),
}))

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    titleExtra,
    children,
  }: {
    title: string
    titleExtra?: React.ReactNode
    children?: React.ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      <div>{titleExtra}</div>
      <div>{children}</div>
    </header>
  ),
}))

vi.mock("@/components/chat/model-selector", () => ({
  ModelSelector: () => <div>Model selector</div>,
}))

vi.mock("@/components/chat/session-history-menu", () => ({
  SessionHistoryMenu: () => <div>Session history</div>,
}))

vi.mock("@/hooks/use-pico-chat", () => ({
  usePicoChat: vi.fn(),
}))

vi.mock("@/hooks/use-gateway", () => ({
  useGateway: vi.fn(),
}))

vi.mock("@/hooks/use-chat-models", () => ({
  useChatModels: vi.fn(),
}))

vi.mock("@/hooks/use-session-history", () => ({
  useSessionHistory: vi.fn(),
}))

describe("ChatPage thread context", () => {
  beforeEach(() => {
    vi.mocked(getThread).mockReset()
    vi.mocked(getThread).mockResolvedValue({
      id: "thread-session",
      ui_session_id: "thread-session",
      title: "Model discovery",
      preview: "",
      type: "general",
      context: {},
      message_count: 0,
      created: "2026-07-28T00:00:00Z",
      updated: "2026-07-28T00:00:00Z",
    })
    vi.mocked(usePicoChat).mockReturnValue({
      messages: [],
      connectionState: "connected",
      isTyping: false,
      activeSessionId: "thread-session",
      contextUsage: undefined,
      sendMessage: vi.fn(),
      switchSession: vi.fn(),
      newChat: vi.fn(),
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
    vi.mocked(useChatModels).mockReturnValue({
      defaultModelName: "gpt-test",
      selectedAccountName: "credential:openai:work",
      selectedModelID: "gpt-test",
      hasAvailableModels: true,
      accountModels: [
        {
          accountName: "credential:openai:work",
          label: "openai:work",
          provider: "openai",
          authMethod: "oauth",
          credentialID: "openai:work",
          modelID: "gpt-test",
        },
      ],
      accountRouterModels: [],
      modelOptions: [{ id: "gpt-test" }],
      isLoadingModelOptions: false,
      modelDiscoveryError: null,
      handleSetAccount: vi.fn(),
      handleSetModel: vi.fn(),
      retryModelDiscovery: vi.fn(),
    })
    vi.mocked(useSessionHistory).mockReturnValue({
      sessions: [],
      hasMore: false,
      loadError: false,
      loadErrorMessage: "",
      observerRef: { current: null },
      loadSessions: vi.fn(),
      handleDeleteSession: vi.fn(),
    })
  })

  it("shows active empty thread metadata instead of a generic blank chat", async () => {
    vi.mocked(getThread).mockResolvedValue({
      id: "thread-session",
      ui_session_id: "thread-session",
      title: "Implement thread workspace",
      preview: "",
      type: "coding",
      context: {
        branch: "feature/thread-management",
      },
      message_count: 0,
      created: "2026-07-14T12:00:00Z",
      updated: "2026-07-14T12:05:00Z",
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    await waitFor(() => {
      expect(getThread).toHaveBeenCalledWith("thread-session")
    })

    expect(
      await screen.findByRole("heading", {
        name: "Implement thread workspace",
      }),
    ).toBeInTheDocument()
    expect(
      screen.getByText("No messages in this thread yet"),
    ).toBeInTheDocument()
    expect(
      screen.getByText("branch:feature/thread-management"),
    ).toBeInTheDocument()
    expect(
      screen.queryByText("What can I help you with?"),
    ).not.toBeInTheDocument()
  })

  it("disables chat while the selected account has no resolved model", () => {
    vi.mocked(getThread).mockResolvedValue({
      id: "thread-session",
      ui_session_id: "thread-session",
      title: "Model discovery",
      preview: "",
      type: "general",
      context: {},
      message_count: 0,
      created: "2026-07-28T00:00:00Z",
      updated: "2026-07-28T00:00:00Z",
    })
    vi.mocked(useChatModels).mockReturnValue({
      defaultModelName: "router-1",
      selectedAccountName: "router-1",
      selectedModelID: "",
      hasAvailableModels: true,
      accountModels: [],
      accountRouterModels: [],
      modelOptions: [],
      isLoadingModelOptions: true,
      modelDiscoveryError: null,
      handleSetAccount: vi.fn(),
      handleSetModel: vi.fn(),
      retryModelDiscovery: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeDisabled()
    expect(screen.getByRole("textbox")).toHaveAttribute(
      "placeholder",
      "Unable to chat: Loading models for the selected account.",
    )
  })

  it("shows a failed-discovery reason when no configured fallback remains", () => {
    vi.mocked(useChatModels).mockReturnValue({
      defaultModelName: "router-1",
      selectedAccountName: "router-1",
      selectedModelID: "",
      hasAvailableModels: true,
      accountModels: [],
      accountRouterModels: [],
      modelOptions: [],
      isLoadingModelOptions: false,
      modelDiscoveryError: "model discovery failed",
      handleSetAccount: vi.fn(),
      handleSetModel: vi.fn(),
      retryModelDiscovery: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeDisabled()
    expect(screen.getByRole("textbox")).toHaveAttribute(
      "placeholder",
      "Unable to chat: Model discovery failed. Retry model discovery above.",
    )
  })

  it("keeps chat enabled after discovery fails when a configured fallback remains", () => {
    vi.mocked(useChatModels).mockReturnValue({
      defaultModelName: "claude-work",
      selectedAccountName: "credential:anthropic:work",
      selectedModelID: "claude-sonnet-4.6",
      hasAvailableModels: true,
      accountModels: [
        {
          accountName: "credential:anthropic:work",
          label: "anthropic:work",
          provider: "anthropic",
          credentialID: "anthropic:work",
          modelID: "claude-sonnet-4.6",
        },
      ],
      accountRouterModels: [],
      modelOptions: [{ id: "claude-sonnet-4.6" }],
      isLoadingModelOptions: false,
      modelDiscoveryError: "model discovery failed",
      handleSetAccount: vi.fn(),
      handleSetModel: vi.fn(),
      retryModelDiscovery: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeEnabled()
  })
})
