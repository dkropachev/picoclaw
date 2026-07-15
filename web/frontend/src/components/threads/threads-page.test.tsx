import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider } from "jotai"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { createThread, dropThread, getThread, getThreads } from "@/api/threads"
import { ThreadOpenPage } from "@/components/threads/thread-open-page"
import { ThreadsPage } from "@/components/threads/threads-page"
import { SidebarProvider } from "@/components/ui/sidebar"
import {
  switchChatSession,
  switchChatSessionAndSend,
} from "@/features/chat/controller"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/api/threads", () => ({
  getThreads: vi.fn(),
  getThread: vi.fn(),
  createThread: vi.fn(),
  dropThread: vi.fn(),
}))

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
  switchChatSessionAndSend: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}))

interface MockChatPageProps {
  fallbackTitle?: string
  newChatLabel?: string
  onNewChat?: () => void
  activeThreadActions?: (thread: ThreadSummary) => ReactNode
  showSessionHistory?: boolean
}

vi.mock("@/components/chat/chat-page", () => ({
  ChatPage: ({
    fallbackTitle,
    newChatLabel,
    onNewChat,
    activeThreadActions,
    showSessionHistory = true,
  }: MockChatPageProps) => {
    const activeThread: ThreadSummary = {
      id: "thread-page",
      ui_session_id: "session-page",
      title: "Implement thread workspace",
      preview: "Show a selected thread as chat",
      type: "coding",
      context: {},
      message_count: 2,
      created: "2026-07-14T12:00:00Z",
      updated: "2026-07-14T12:05:00Z",
      discoverable: true,
    }
    return (
      <section
        aria-label="chat pane"
        data-history={String(showSessionHistory)}
      >
        Chat pane: {fallbackTitle}
        {newChatLabel && (
          <button type="button" onClick={onNewChat}>
            {newChatLabel}
          </button>
        )}
        {activeThreadActions?.(activeThread)}
      </section>
    )
  },
}))

const thread: ThreadSummary = {
  id: "thread-page",
  ui_session_id: "session-page",
  title: "Implement thread workspace",
  preview: "Show a selected thread as chat",
  type: "coding",
  context: {
    branch: "feature/thread-management",
  },
  message_count: 2,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
  discoverable: true,
}

describe("ThreadsPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    vi.mocked(getThreads).mockReset()
    vi.mocked(getThread).mockReset()
    vi.mocked(createThread).mockReset()
    vi.mocked(dropThread).mockReset()
    vi.mocked(switchChatSession).mockReset()
    vi.mocked(switchChatSessionAndSend).mockReset()
    navigateMock.mockReset()
    vi.mocked(getThreads).mockResolvedValue([thread])
    vi.mocked(getThread).mockResolvedValue(thread)
    vi.mocked(createThread).mockResolvedValue(thread)
    vi.mocked(dropThread).mockResolvedValue({ ...thread, discoverable: false })
  })

  it("renders thread navigation beside the normal chat pane", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadsPage />
      </Provider>,
    )

    expect(screen.getByLabelText("chat pane")).toHaveTextContent(
      "Chat pane: Thread",
    )

    await user.click(
      await screen.findByRole("button", {
        name: /implement thread workspace/i,
      }),
    )

    expect(switchChatSession).toHaveBeenCalledWith("session-page")
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/open/$threadId",
      params: { threadId: "session-page" },
    })
  })

  it("opens a thread as a normal chat window", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadOpenPage threadId="session-page" />
      </Provider>,
    )

    expect(screen.getByLabelText("chat pane")).toHaveTextContent(
      "Chat pane: Thread",
    )
    expect(screen.getByLabelText("chat pane")).toHaveAttribute(
      "data-history",
      "false",
    )
    expect(
      screen.queryByPlaceholderText("Search threads..."),
    ).not.toBeInTheDocument()
    expect(screen.queryByText("No threads yet")).not.toBeInTheDocument()
    await waitFor(() => {
      expect(getThread).toHaveBeenCalledWith("session-page")
      expect(switchChatSession).toHaveBeenCalledWith("session-page")
    })

    await user.click(
      within(screen.getByLabelText("chat pane")).getByRole("button", {
        name: "New thread",
      }),
    )

    await waitFor(() => {
      expect(createThread).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "general",
          title: "New thread",
          source_query: "New thread",
        }),
      )
      expect(switchChatSession).toHaveBeenCalledWith("session-page")
      expect(switchChatSessionAndSend).not.toHaveBeenCalled()
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/threads/open/$threadId",
        params: { threadId: "session-page" },
      })
    })
  })

  it("renders an empty thread page when no thread is open", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <SidebarProvider>
          <ThreadOpenPage />
        </SidebarProvider>
      </Provider>,
    )

    expect(screen.getByText("No threads yet")).toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "New thread" }),
    ).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "New thread" }))

    await waitFor(() => {
      expect(createThread).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "general",
          title: "New thread",
          source_query: "New thread",
        }),
      )
      expect(switchChatSession).toHaveBeenCalledWith("session-page")
      expect(switchChatSessionAndSend).not.toHaveBeenCalled()
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/threads/open/$threadId",
        params: { threadId: "session-page" },
      })
    })
  })

  it("drops the active thread from the thread chat header", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadOpenPage threadId="session-page" />
      </Provider>,
    )

    await user.click(
      within(screen.getByLabelText("chat pane")).getByRole("button", {
        name: "Drop thread",
      }),
    )

    await waitFor(() => {
      expect(dropThread).toHaveBeenCalledWith("thread-page")
      expect(navigateMock).toHaveBeenCalledWith({ to: "/threads/open" })
    })
  })

  it("redirects dropped thread URLs back to the empty Thread workspace", async () => {
    vi.mocked(getThread).mockResolvedValueOnce({
      ...thread,
      discoverable: false,
    })

    render(
      <Provider>
        <ThreadOpenPage threadId="session-page" />
      </Provider>,
    )

    await waitFor(() => {
      expect(getThread).toHaveBeenCalledWith("session-page")
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/threads/open",
        replace: true,
      })
    })
    expect(switchChatSession).not.toHaveBeenCalled()
  })
})
