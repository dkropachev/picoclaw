import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider, createStore } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { createThread, dropThread, getThreads } from "@/api/threads"
import { ThreadSidebar } from "@/components/threads/thread-sidebar"
import {
  switchChatSession,
  switchChatSessionAndSend,
} from "@/features/chat/controller"
import { threadOpenSessionIdAtom } from "@/store/threads"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/api/threads", () => ({
  getThreads: vi.fn(),
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
  discoverable: true,
}

describe("ThreadSidebar", () => {
  beforeEach(() => {
    vi.mocked(getThreads).mockReset()
    vi.mocked(createThread).mockReset()
    vi.mocked(dropThread).mockReset()
    vi.mocked(switchChatSession).mockReset()
    vi.mocked(switchChatSessionAndSend).mockReset()
    navigateMock.mockReset()
    vi.mocked(getThreads).mockResolvedValue([thread])
    vi.mocked(createThread).mockResolvedValue(thread)
    vi.mocked(dropThread).mockResolvedValue({ ...thread, discoverable: false })
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
          source_query: "New thread",
        }),
      )
      expect(switchChatSession).toHaveBeenCalledWith("session-sidebar")
      expect(switchChatSessionAndSend).not.toHaveBeenCalled()
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/threads/open/$threadId",
        params: { threadId: "session-sidebar" },
      })
    })
  })

  it("uses the search text as the initial thread prompt", async () => {
    const user = userEvent.setup()

    render(
      <Provider>
        <ThreadSidebar />
      </Provider>,
    )

    await screen.findByText("Investigate websocket routing")
    await user.type(
      screen.getByPlaceholderText("Search threads..."),
      "japan relocation",
    )
    await user.click(screen.getByRole("button", { name: "New thread" }))

    await waitFor(() => {
      expect(createThread).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "japan relocation",
          source_query: "japan relocation",
        }),
      )
      expect(switchChatSessionAndSend).toHaveBeenCalledWith("session-sidebar", {
        content: "japan relocation",
      })
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
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/open/$threadId",
      params: { threadId: "session-sidebar" },
    })
  })

  it("drops a thread from the sidebar without deleting the session", async () => {
    const store = createStore()
    store.set(threadOpenSessionIdAtom, "session-sidebar")
    const user = userEvent.setup()

    render(
      <Provider store={store}>
        <ThreadSidebar />
      </Provider>,
    )

    expect(
      await screen.findByText("Investigate websocket routing"),
    ).toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Drop thread" }))

    await waitFor(() => {
      expect(dropThread).toHaveBeenCalledWith("thread-sidebar")
    })
    expect(store.get(threadOpenSessionIdAtom)).toBe("")
    expect(
      screen.queryByText("Investigate websocket routing"),
    ).not.toBeInTheDocument()
  })
})
