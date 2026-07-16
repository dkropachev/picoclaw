import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ThreadSummary } from "@/api/threads"
import { ThreadTile } from "@/components/threads/thread-tile"

const thread: ThreadSummary = {
  id: "thread-working",
  ui_session_id: "session-working",
  title: "Fix CI signal",
  preview: "Make the failing checks green",
  type: "coding",
  context: {},
  message_count: 4,
  created: "2026-07-14T12:00:00Z",
  updated: "2026-07-14T12:05:00Z",
  discoverable: true,
}

describe("ThreadTile", () => {
  it("shows when a thread is being worked on", () => {
    render(
      <ThreadTile thread={{ ...thread, is_working: true }} onOpen={vi.fn()} />,
    )

    expect(screen.getByText("Working")).toBeInTheDocument()
  })

  it("does not show working state for idle threads", () => {
    render(<ThreadTile thread={thread} onOpen={vi.fn()} />)

    expect(screen.queryByText("Working")).not.toBeInTheDocument()
  })
})
