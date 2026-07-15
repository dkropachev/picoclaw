import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider, createStore } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { dropThread } from "@/api/threads"
import type { ThreadSummary } from "@/api/threads"
import { resetThreadCardAutoSeedForTests } from "@/components/threads/thread-card-auto-seed"
import { ThreadCardMessage } from "@/components/threads/thread-card-message"
import {
  switchChatSession,
  switchChatSessionAndSend,
} from "@/features/chat/controller"
import {
  threadSearchFocusNonceAtom,
  threadSearchQueryAtom,
} from "@/store/threads"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
  switchChatSessionAndSend: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}))

vi.mock("@/api/threads", () => ({
  dropThread: vi.fn(),
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
    vi.mocked(dropThread).mockReset()
    vi.mocked(dropThread).mockResolvedValue({ ...thread, discoverable: false })
    vi.mocked(switchChatSession).mockReset()
    vi.mocked(switchChatSessionAndSend).mockReset()
    vi.mocked(switchChatSessionAndSend).mockResolvedValue(true)
    resetThreadCardAutoSeedForTests()
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
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/search",
      search: { query: "location:/extra/dkropachev/picoclaw" },
    })
  })

  it("switches into the thread workspace when a thread tile is clicked", async () => {
    const user = userEvent.setup()

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
      to: "/threads/open/$threadId",
      params: { threadId: "session-coding" },
    })
  })

  it("drops a thread from a rendered thread card", async () => {
    const user = userEvent.setup()

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

    await user.click(screen.getByRole("button", { name: "Drop thread" }))

    await waitFor(() => {
      expect(dropThread).toHaveBeenCalledWith("session-coding")
    })
    expect(
      screen.queryByText("Implement thread sidebar"),
    ).not.toBeInTheDocument()
  })

  it("auto-switches for switch cards", async () => {
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
    expect(switchChatSessionAndSend).not.toHaveBeenCalled()
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/open/$threadId",
      params: { threadId: "session-coding" },
    })

    rerender(
      <Provider>
        <ThreadCardMessage payload={{ ...payload }} />
      </Provider>,
    )

    expect(switchChatSession).toHaveBeenCalledTimes(1)
  })

  it("auto-switches and starts work when a new empty thread opens", async () => {
    const emptyThread = {
      ...thread,
      message_count: 0,
      title: "Japan relocation planning",
      preview: "Planning to relocate to Japan",
    }

    render(
      <Provider>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_switch.v2",
            query:
              "can you start a thread about planning to relocate to japan, i want you to go over all the possible options how we as family of 3 can go there",
            auto_switch: true,
            thread: emptyThread,
          }}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSessionAndSend).toHaveBeenCalledWith("session-coding", {
        content:
          "go over all the possible options how we as a family of 3 can relocate to japan",
      })
    })
    expect(switchChatSession).not.toHaveBeenCalled()
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/open/$threadId",
      params: { threadId: "session-coding" },
    })
  })

  it("seeds an empty switch card from thread source query when payload query is blank", async () => {
    const emptyThread = {
      ...thread,
      message_count: 0,
      title: "Japan relocation planning",
      preview: "Planning to relocate to Japan",
      source_query:
        "can you start a thread about planning to relocate to japan, i want you to go over all the possible options how we as family of 3 can go there",
    }

    render(
      <Provider>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_switch.v2",
            query: "",
            auto_switch: true,
            thread: emptyThread,
          }}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSessionAndSend).toHaveBeenCalledWith("session-coding", {
        content:
          "go over all the possible options how we as a family of 3 can relocate to japan",
      })
    })
    expect(switchChatSession).not.toHaveBeenCalled()
  })

  it("seeds an empty switch card only once when the same card remounts", async () => {
    const emptyThread = {
      ...thread,
      message_count: 0,
      title: "Japan relocation planning",
      preview: "Planning to relocate to Japan",
      source_query:
        "can you start a thread about planning to relocate to japan, i want you to go over all the possible options how we as family of 3 can go there",
    }
    const payload = {
      type: "picoclaw.thread_switch.v2" as const,
      query: "",
      auto_switch: true,
      thread: emptyThread,
    }

    const { unmount } = render(
      <Provider>
        <ThreadCardMessage payload={payload} />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSessionAndSend).toHaveBeenCalledTimes(1)
    })

    unmount()

    render(
      <Provider>
        <ThreadCardMessage payload={payload} />
      </Provider>,
    )

    await waitFor(() => {
      expect(navigateMock).toHaveBeenCalledWith({
        to: "/threads/open/$threadId",
        params: { threadId: "session-coding" },
      })
    })
    expect(switchChatSessionAndSend).toHaveBeenCalledTimes(1)
    expect(switchChatSession).toHaveBeenCalledWith("session-coding")
  })

  it("auto-switches empty threads without fabricating a generic prompt", async () => {
    const emptyThread = {
      ...thread,
      message_count: 0,
      title: "New thread",
      preview: "New thread",
      source_query: "New thread",
    }

    render(
      <Provider>
        <ThreadCardMessage
          payload={{
            type: "picoclaw.thread_switch.v2",
            query: "",
            auto_switch: true,
            thread: emptyThread,
          }}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(switchChatSession).toHaveBeenCalledWith("session-coding")
    })
    expect(switchChatSessionAndSend).not.toHaveBeenCalled()
    expect(navigateMock).toHaveBeenCalledWith({
      to: "/threads/open/$threadId",
      params: { threadId: "session-coding" },
    })
  })

  it("auto-switches v2 cards with target session id", async () => {
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
      to: "/threads/open/$threadId",
      params: { threadId: "ui-session-target" },
    })
  })
})
