import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { createThread, getThreads } from "@/api/threads"
import { ThreadsPage } from "@/components/threads/threads-page"
import { switchChatSession } from "@/features/chat/controller"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/api/threads", () => ({
  getThreads: vi.fn(),
  createThread: vi.fn(),
  dropThread: vi.fn(),
}))

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}))

vi.mock("@/components/chat/chat-page", () => ({
  ChatPage: ({ fallbackTitle }: { fallbackTitle?: string }) => (
    <section aria-label="chat pane">Chat pane: {fallbackTitle}</section>
  ),
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
  beforeEach(() => {
    vi.mocked(getThreads).mockReset()
    vi.mocked(createThread).mockReset()
    vi.mocked(switchChatSession).mockReset()
    navigateMock.mockReset()
    vi.mocked(getThreads).mockResolvedValue([thread])
    vi.mocked(createThread).mockResolvedValue(thread)
  })

  it("renders thread navigation beside the normal chat pane", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadsPage />
      </Provider>,
    )

    expect(screen.getByLabelText("chat pane")).toHaveTextContent(
      "Chat pane: Threads",
    )

    await user.click(
      await screen.findByRole("button", {
        name: /implement thread workspace/i,
      }),
    )

    expect(switchChatSession).toHaveBeenCalledWith("session-page")
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/$threadId",
      params: { threadId: "session-page" },
    })
  })

  it("switches to the route-selected thread session", async () => {
    render(
      <Provider>
        <ThreadsPage threadId="session-page" />
      </Provider>,
    )

    await screen.findByText("Implement thread workspace")
    expect(switchChatSession).toHaveBeenCalledWith("session-page")
  })
})
