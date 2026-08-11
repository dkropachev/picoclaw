import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { createRef } from "react"
import { describe, expect, it, vi } from "vitest"

import { parseExactJSON } from "@/api/review-attention-json"
import { AttentionConversation } from "@/components/reviews/attention-conversation"
import {
  ATTENTION_ACTIVE_POLL_INTERVAL_MS,
  ATTENTION_STABLE_POLL_INTERVAL_MS,
  attentionProjectionPollInterval,
  attentionProjectionPolls,
  findActionableAttentionTurn,
} from "@/components/reviews/attention-conversation-model"

const responseToken = `sha256:${"a".repeat(64)}`

describe("AttentionConversation", () => {
  it("renders checking state and identifies polling projections", () => {
    const projection = {
      case_version: 3,
      status: "checking" as const,
      can_respond: false,
      turns: [],
    }
    renderConversation(projection)

    expect(
      screen.getByText("AI is checking whether your attention is needed…"),
    ).toBeVisible()
    expect(attentionProjectionPolls(projection)).toBe(true)
    expect(findActionableAttentionTurn(projection)).toBeUndefined()
    expect(
      attentionProjectionPolls({
        ...projection,
        status: "recovery_required",
      }),
    ).toBe(false)
    expect(
      attentionProjectionPolls({
        ...projection,
        status: "recovery_required",
        turns: [
          {
            status: "recovery_required",
            title: "Resume the saved reply",
            questions: parseExactJSON("[]"),
            response: "Keep compatibility",
          },
        ],
      }),
    ).toBe(true)
    expect(
      [
        undefined,
        { ...projection, status: "queued" as const },
        { ...projection, status: "none" as const },
      ].map(attentionProjectionPollInterval),
    ).toEqual([
      ATTENTION_STABLE_POLL_INTERVAL_MS,
      ATTENTION_ACTIVE_POLL_INTERVAL_MS,
      ATTENTION_STABLE_POLL_INTERVAL_MS,
    ])
    expect(
      [
        { ...projection, status: "waiting" as const, can_respond: true },
        { ...projection, status: "none" as const },
      ].map(attentionProjectionPollInterval),
    ).toEqual([
      ATTENTION_STABLE_POLL_INTERVAL_MS,
      ATTENTION_STABLE_POLL_INTERVAL_MS,
    ])
  })

  it("renders exact structured questions and delegates one response", async () => {
    const user = userEvent.setup()
    const onResponseChange = vi.fn()
    const onSubmit = vi.fn((event: React.FormEvent) => event.preventDefault())
    const projection = {
      case_version: 3,
      status: "waiting" as const,
      can_respond: true,
      turns: [
        {
          status: "waiting" as const,
          title: "Choose a direction",
          questions: parseExactJSON('{"risk":9007199254740993}'),
          response_token: responseToken,
        },
      ],
    }
    renderConversation(projection, { onResponseChange, onSubmit })

    expect(screen.getByText('{"risk":9007199254740993}')).toBeVisible()
    const input = screen.getByLabelText("Reply to the AI attention request")
    await user.type(input, "Proceed")
    expect(onResponseChange).toHaveBeenCalled()
    fireEvent.submit(input.closest("form")!)
    expect(onSubmit).toHaveBeenCalledOnce()
  })

  it("uses PR gate copy only for the own-PR context", () => {
    const projection = {
      case_version: 3,
      status: "waiting" as const,
      can_respond: true,
      turns: [
        {
          status: "waiting" as const,
          title: "Choose a direction",
          questions: parseExactJSON("[]"),
          response_token: responseToken,
        },
      ],
    }
    const { unmount } = renderConversation(projection, {
      context: "pr-development",
    })

    expect(screen.getByText("PR decision gates")).toBeVisible()
    expect(screen.getByText("Gate")).toBeVisible()
    expect(
      screen.getByText("The current PR gate is waiting for your reply."),
    ).toBeVisible()
    expect(screen.getByLabelText("Reply to the current PR gate")).toBeVisible()
    expect(
      screen.getByText(
        "Your reply continues this gate; it does not directly edit code, run CI, push commits, acknowledge a review, or merge the pull request.",
      ),
    ).toBeVisible()
    expect(screen.queryByText(/AI|Assistant/)).not.toBeInTheDocument()

    unmount()
    renderConversation(projection)
    expect(screen.getByText("AI attention")).toBeVisible()
    expect(
      screen.getByLabelText("Reply to the AI attention request"),
    ).toBeVisible()
    expect(
      screen.queryByText(/Your reply continues this gate/),
    ).not.toBeInTheDocument()
  })
})

function renderConversation(
  projection: Parameters<typeof AttentionConversation>[0]["projection"],
  overrides: Partial<Parameters<typeof AttentionConversation>[0]> = {},
) {
  return render(
    <AttentionConversation
      projection={projection}
      loading={false}
      loadError={undefined}
      response=""
      pending={false}
      maximumResponseBytes={32 << 10}
      attentionInputRef={createRef<HTMLTextAreaElement>()}
      recoveryButtonRef={createRef<HTMLButtonElement>()}
      onResponseChange={vi.fn()}
      onSubmit={(event) => event.preventDefault()}
      onRetryLoad={vi.fn()}
      onRetryContinuation={vi.fn()}
      {...overrides}
    />,
  )
}
