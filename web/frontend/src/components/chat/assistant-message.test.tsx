import { render, screen, waitFor } from "@testing-library/react"
import { Provider } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { getThread, getThreads } from "@/api/threads"
import { AssistantMessage } from "@/components/chat/assistant-message"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/api/threads", () => ({
  getThread: vi.fn(),
  getThreads: vi.fn(),
}))

vi.mock("@/features/chat/controller", () => ({
  switchChatSession: vi.fn(),
}))

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}))

const japanThread: ThreadSummary = {
  id: "thread-japan-relocation",
  ui_session_id: "session-japan-relocation",
  title: "Japan relocation planning",
  preview: "Visa, housing, and moving checklist",
  type: "general",
  context: {
    topic: "relocation",
  },
  message_count: 7,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
  discoverable: true,
}

const japanOptionsThread: ThreadSummary = {
  id: "session-1784068360669-34183697",
  ui_session_id: "session-1784068360669-34183697",
  title: "Relocating to Japan: Family of 3 Options",
  preview: "Reviewing all possible relocation options",
  type: "investigating",
  context: {
    japan: "Family",
  },
  source_query:
    "planning to relocate to Japan as a family of 3, reviewing all possible relocation options",
  message_count: 2,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
  discoverable: true,
}

describe("AssistantMessage", () => {
  beforeEach(() => {
    vi.mocked(getThread).mockReset()
    vi.mocked(getThreads).mockReset()
    navigateMock.mockReset()
  })

  it("renders saved threads search tool calls as thread tiles", async () => {
    vi.mocked(getThreads).mockResolvedValue([japanThread])

    render(
      <Provider>
        <AssistantMessage
          content=""
          kind="tool_calls"
          toolCalls={[
            {
              id: "tool-call-threads",
              type: "function",
              function: {
                name: "threads",
                arguments: JSON.stringify({
                  action: "search",
                  limit: 10,
                  query: "japan relocation",
                  type: "general",
                }),
              },
            },
          ]}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(getThreads).toHaveBeenCalledWith(
        expect.objectContaining({
          query: "japan relocation",
          type: "general",
          limit: 10,
        }),
      )
    })

    expect(
      await screen.findByRole("button", {
        name: /japan relocation planning/i,
      }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/"action"/)).not.toBeInTheDocument()
  })

  it("renders saved threads switch tool calls as a thread tile", async () => {
    vi.mocked(getThreads).mockResolvedValue([japanOptionsThread])

    render(
      <Provider>
        <AssistantMessage
          content=""
          kind="tool_calls"
          toolCalls={[
            {
              id: "tool-call-switch",
              type: "function",
              function: {
                name: "threads",
                arguments: JSON.stringify({
                  action: "switch",
                  create_if_missing: true,
                  query:
                    "planning to relocate to Japan as a family of 3, reviewing all possible relocation options",
                  title: "Relocating to Japan: Family of 3 Options",
                  type: "investigating",
                }),
              },
            },
          ]}
        />
      </Provider>,
    )

    await waitFor(() => {
      expect(getThreads).toHaveBeenCalledWith(
        expect.objectContaining({
          query:
            "planning to relocate to Japan as a family of 3, reviewing all possible relocation options",
          type: "investigating",
          limit: 8,
        }),
      )
    })

    expect(
      await screen.findByRole("button", {
        name: /relocating to japan/i,
      }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/"action"/)).not.toBeInTheDocument()
  })
})
