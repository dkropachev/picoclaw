import { render, screen, waitFor } from "@testing-library/react"
import { Provider } from "jotai"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { getThreads } from "@/api/threads"
import { AssistantMessage } from "@/components/chat/assistant-message"

const navigateMock = vi.hoisted(() => vi.fn())

vi.mock("@/api/threads", () => ({
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

describe("AssistantMessage", () => {
  beforeEach(() => {
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
})
