import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider, createStore } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { ThreadCardMessage } from "@/components/threads/thread-card-message"
import { switchChatSession } from "@/features/chat/controller"
import {
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}))

const thread: ThreadSummary = {
  id: "session-coding",
  ui_session_id: "session-coding",
  title: "Implement thread sidebar",
  preview: "Code in /extra/dkropachev/picoclaw",
  type: "coding",
  context: {
    location: "/extra/dkropachev/picoclaw",
    branch: "main",
  },
  message_count: 3,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
  discoverable: true,
}

describe("ThreadCardMessage", () => {
  beforeEach(() => {
    navigateMock.mockReset()
  })

  it("opens exact model query in thread sidebar search", async () => {
    const store = createStore()
    const user = userEvent.setup()

    render(
      <Provider store={store}>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_search.v1",
            query: "location:/extra/dkropachev/picoclaw",
            threads: [thread],
            total: 1,
          }}
        />
      </Provider>,
    )

    await user.click(screen.getByRole("button", { name: /thread search/i }))

    expect(store.get(threadSearchQueryAtom)).toBe(
      "location:/extra/dkropachev/picoclaw",
    )
    expect(store.get(threadSearchFocusNonceAtom)).toBe(1)
    expect(navigateMock).toHaveBeenCalledWith({ to: "/threads" })
  })

  it("switches into the thread workspace when a thread tile is clicked", async () => {
    const user = userEvent.setup()
    vi.mocked(switchChatSession).mockClear()

    render(
      <Provider>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_search.v1",
            query: "coding",
            threads: [thread],
            total: 1,
          }}
        />
      </Provider>,
    )

    await user.click(screen.getByRole("button", { name: /implement thread/i }))

    expect(switchChatSession).toHaveBeenCalledWith("session-coding")
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/$threadId",
      params: { threadId: "session-coding" },
    })
  })

  it("auto-switches for switch cards", async () => {
    vi.mocked(switchChatSession).mockClear()
    const payload = {
      type: "picoclaw.thread_switch.v1" as const,
      query: "coding",
      auto_switch: true,
      thread,
    }

    const { rerender } = render(
      <Provider>
        <ThreadCardMessage payload={payload} />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSession).toHaveBeenCalledWith("session-coding")
    })
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/$threadId",
      params: { threadId: "session-coding" },
    })

    rerender(
      <Provider>
        <ThreadCardMessage payload={{ ...payload }} />
      </Provider>,
    )

    expect(switchChatSession).toHaveBeenCalledTimes(1)
  })

  it("auto-switches v2 cards with target session id", async () => {
    vi.mocked(switchChatSession).mockClear()

    render(
      <Provider>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_switch.v2",
            query: "coding",
            auto_switch: true,
            thread,
            target_session_id: "ui-session-target",
          }}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSession).toHaveBeenCalledWith("ui-session-target")
    })
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/$threadId",
      params: { threadId: "ui-session-target" },
    })
  })
})
