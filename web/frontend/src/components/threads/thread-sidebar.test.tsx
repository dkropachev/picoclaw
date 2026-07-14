import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { createThread, getThreads } from "@/api/threads"
import { ThreadSidebar } from "@/components/threads/thread-sidebar"
import { switchChatSession } from "@/features/chat/controller"

vi.mock("@/api/threads", () => ({
  getThreads: vi.fn(),
  createThread: vi.fn(),
}))

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
}))

const thread: ThreadSummary = {
  id: "thread-sidebar",
  ui_session_id: "session-sidebar",
  title: "Investigate websocket routing",
  preview: "Find why websocket routing fails",
  type: "investigating",
  context: {
    location: "/extra/dkropachev/picoclaw",
  },
  message_count: 5,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
}

describe("ThreadSidebar", () => {
  beforeEach(() => {
    vi.mocked(getThreads).mockReset()
    vi.mocked(createThread).mockReset()
    vi.mocked(switchChatSession).mockReset()
    vi.mocked(getThreads).mockResolvedValue([thread])
    vi.mocked(createThread).mockResolvedValue(thread)
  })

  it("loads and searches threads with the sidebar query", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadSidebar />
      </Provider>,
    )

    expect(
      await screen.findByText("Investigate websocket routing"),
    ).toBeInTheDocument()

    await user.type(
      screen.getByPlaceholderText("Search threads..."),
      "location:/extra",
    )

    await waitFor(() => {
      expect(getThreads).toHaveBeenLastCalledWith(
        expect.objectContaining({
          query: "location:/extra",
          type: "",
        }),
      )
    })
  })

  it("creates a thread and switches to it", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadSidebar />
      </Provider>,
    )

    await screen.findByText("Investigate websocket routing")
    await user.click(screen.getByRole("button", { name: "New thread" }))

    await waitFor(() => {
      expect(createThread).toHaveBeenCalledWith(
        expect.objectContaining({
          type: "general",
        }),
      )
      expect(switchChatSession).toHaveBeenCalledWith("session-sidebar")
    })
  })

  it("opens the thread UI session from a tile", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadSidebar />
      </Provider>,
    )

    await user.click(
      await screen.findByRole("button", {
        name: /investigate websocket routing/i,
      }),
    )

    expect(switchChatSession).toHaveBeenCalledWith("session-sidebar")
  })
})
