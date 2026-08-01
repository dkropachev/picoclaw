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
      defaultAccountRef: "credential:openai:work",
      defaultModelName: "gpt-test",
      selectedAccountName: "credential:openai:work",
      selectedModelAlias: "gpt-test",
      hasAvailableModels: true,
      accountModels: [
        {
          accountName: "credential:openai:work",
          label: "openai:work",
          provider: "openai",
          authMethod: "oauth",
          credentialID: "openai:work",
        },
      ],
      accountRouterModels: [],
      aliasOptions: [{ name: "gpt-test", model: "gpt-5.4" }],
      isSavingSelection: false,
      handleSetAccount: vi.fn(),
      handleSetModelAlias: vi.fn(),
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

  it("disables chat when no account and alias defaults are configured", () => {
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
      defaultAccountRef: "",
      defaultModelName: "",
      selectedAccountName: "",
      selectedModelAlias: "",
      hasAvailableModels: true,
      accountModels: [],
      accountRouterModels: [],
      aliasOptions: [{ name: "coding", model: "gpt-5.4" }],
      isSavingSelection: false,
      handleSetAccount: vi.fn(),
      handleSetModelAlias: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeDisabled()
    expect(screen.getByRole("textbox")).toHaveAttribute(
      "placeholder",
      "Unable to chat: No account and model alias are configured. Set both on the Models page.",
    )
  })

  it("disables chat until both a selected account and alias are present", () => {
    vi.mocked(useChatModels).mockReturnValue({
      defaultAccountRef: "router-1",
      defaultModelName: "coding",
      selectedAccountName: "router-1",
      selectedModelAlias: "",
      hasAvailableModels: true,
      accountModels: [],
      accountRouterModels: [],
      aliasOptions: [{ name: "coding", model: "gpt-5.4" }],
      isSavingSelection: false,
      handleSetAccount: vi.fn(),
      handleSetModelAlias: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeDisabled()
    expect(screen.getByRole("textbox")).toHaveAttribute(
      "placeholder",
      "Unable to chat: Select both an account and a model alias.",
    )
  })

  it("keeps chat enabled when a configured account and alias are selected", () => {
    vi.mocked(useChatModels).mockReturnValue({
      defaultAccountRef: "credential:anthropic:work",
      defaultModelName: "coding",
      selectedAccountName: "credential:anthropic:work",
      selectedModelAlias: "coding",
      hasAvailableModels: true,
      accountModels: [
        {
          accountName: "credential:anthropic:work",
          label: "anthropic:work",
          provider: "anthropic",
          credentialID: "anthropic:work",
        },
      ],
      accountRouterModels: [],
      aliasOptions: [{ name: "coding", model: "claude-sonnet-4.6" }],
      isSavingSelection: false,
      handleSetAccount: vi.fn(),
      handleSetModelAlias: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeEnabled()
  })

  it("keeps chat enabled for a local chat-alias fallback when the persisted alias default is empty", () => {
    vi.mocked(useChatModels).mockReturnValue({
      defaultAccountRef: "router-1",
      defaultModelName: "",
      selectedAccountName: "router-1",
      selectedModelAlias: "chat",
      hasAvailableModels: true,
      accountModels: [],
      accountRouterModels: [
        {
          index: 0,
          model_name: "router-1",
          model: "",
          api_key: "",
          enabled: true,
          available: true,
          status: "available",
          is_default: true,
          is_virtual: true,
          provider: "router",
          router: { name: "router-1", enabled: true },
        },
      ],
      aliasOptions: [{ name: "chat", model: "gpt-5.6-sol" }],
      isSavingSelection: false,
      handleSetAccount: vi.fn(),
      handleSetModelAlias: vi.fn(),
    })

    render(
      <Provider>
        <ChatPage />
      </Provider>,
    )

    expect(screen.getByRole("textbox")).toBeEnabled()
    expect(screen.getByText("Model selector")).toBeInTheDocument()
  })
})
