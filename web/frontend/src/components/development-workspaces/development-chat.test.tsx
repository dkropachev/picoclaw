import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  createDevelopmentRequestID,
  getDevelopmentConversation,
  sendDevelopmentMessage,
} from "@/api/development-workspaces"
import { DevelopmentChat } from "@/components/development-workspaces/development-chat"

vi.mock("@/api/development-workspaces", async (importOriginal) => {
  const original =
    await importOriginal<typeof import("@/api/development-workspaces")>()
  return {
    ...original,
    createDevelopmentRequestID: vi.fn(),
    getDevelopmentConversation: vi.fn(),
    sendDevelopmentMessage: vi.fn(),
  }
})

const mockedRequestID = vi.mocked(createDevelopmentRequestID)
const mockedConversation = vi.mocked(getDevelopmentConversation)
const mockedSend = vi.mocked(sendDevelopmentMessage)
const workspaceID = `devw_${"1".repeat(32)}`

function renderChat(candidateRevision?: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <DevelopmentChat
        workspaceID={workspaceID}
        candidateRevision={candidateRevision}
      />
    </QueryClientProvider>,
  )
}

describe("development chat", () => {
  beforeEach(() => {
    mockedRequestID.mockReset()
    mockedConversation.mockReset()
    mockedSend.mockReset()
    mockedRequestID.mockReturnValue(`devq_${"2".repeat(32)}`)
    mockedConversation.mockResolvedValue({ revision: 7, messages: [] })
    mockedSend.mockResolvedValue({
      revision: 8,
      messages: [
        {
          id: "msg_1",
          role: "user",
          mode: "steer",
          status: "queued",
          content: "Keep the copy concise.",
          created_at: "2026-08-24T10:00:00Z",
        },
      ],
    })
  })

  it("separates read-only questions from steering messages", async () => {
    const user = userEvent.setup()
    renderChat("candidate:2")
    await screen.findByText(
      "Ask about the code or steer the next implementation step.",
    )

    expect(screen.getByRole("button", { name: "Steer" })).toHaveAttribute(
      "aria-pressed",
      "true",
    )
    await user.click(screen.getByRole("button", { name: "Ask" }))
    expect(screen.getByText(/Read-only answer/)).toBeVisible()
    await user.type(
      screen.getByLabelText("Ask development AI"),
      "Why this file?",
    )
    await user.click(screen.getByRole("button", { name: "Send question" }))

    await waitFor(() =>
      expect(mockedSend).toHaveBeenCalledWith(workspaceID, {
        mode: "ask",
        content: "Why this file?",
        expected_revision: 7,
        request_id: `devq_${"2".repeat(32)}`,
        candidate_revision: "candidate:2",
      }),
    )
  })

  it("queues steering against the exact conversation revision", async () => {
    const user = userEvent.setup()
    renderChat()
    await screen.findByText(
      "Ask about the code or steer the next implementation step.",
    )
    await user.type(
      screen.getByLabelText("Steer development AI"),
      "Keep the copy concise.",
    )
    await user.click(screen.getByRole("button", { name: "Send steering" }))

    await waitFor(() =>
      expect(mockedSend).toHaveBeenCalledWith(workspaceID, {
        mode: "steer",
        content: "Keep the copy concise.",
        expected_revision: 7,
        request_id: `devq_${"2".repeat(32)}`,
      }),
    )
    expect(await screen.findByText("Keep the copy concise.")).toBeVisible()
  })

  it("renders applied steering markers as user-facing status", async () => {
    mockedConversation.mockResolvedValue({
      revision: 1,
      messages: [
        {
          id: "msg_applied",
          role: "system",
          mode: "steer",
          status: "applied",
          content: "applied:msg_private_source",
          created_at: "2026-08-24T10:01:00Z",
        },
      ],
    })
    renderChat("candidate:2")

    expect(
      await screen.findByText(
        "Steering applied to the implementation candidate.",
      ),
    ).toBeVisible()
    expect(
      screen.queryByText("applied:msg_private_source"),
    ).not.toBeInTheDocument()
  })
})
