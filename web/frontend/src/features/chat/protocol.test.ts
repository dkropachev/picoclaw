import { getDefaultStore } from "jotai"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { type PicoMessage, handlePicoMessage } from "@/features/chat/protocol"
import { chatAtom } from "@/store/chat"

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
  },
}))

const store = getDefaultStore()
const sessionId = "session-typing"

function resetChatState() {
  store.set(chatAtom, {
    messages: [],
    connectionState: "connected",
    isTyping: false,
    activeSessionId: sessionId,
    hasHydratedActiveSession: true,
  })
}

function assistantCreate(): PicoMessage {
  return {
    type: "message.create",
    session_id: sessionId,
    payload: {
      message_id: "assistant-1",
      content: "partial answer",
    },
  }
}

describe("handlePicoMessage typing state", () => {
  beforeEach(() => {
    resetChatState()
  })

  afterEach(() => {
    handlePicoMessage({ type: "typing.stop", session_id: sessionId }, sessionId)
  })

  it("keeps thinking visible while server typing is active", () => {
    handlePicoMessage({ type: "typing.start", session_id: sessionId }, sessionId)
    handlePicoMessage(assistantCreate(), sessionId)

    expect(store.get(chatAtom).isTyping).toBe(true)
  })

  it("stops thinking when server typing stops", () => {
    handlePicoMessage({ type: "typing.start", session_id: sessionId }, sessionId)
    handlePicoMessage(assistantCreate(), sessionId)
    handlePicoMessage({ type: "typing.stop", session_id: sessionId }, sessionId)

    expect(store.get(chatAtom).isTyping).toBe(false)
  })

  it("still clears local optimistic thinking when no server typing is active", () => {
    store.set(chatAtom, (prev) => ({ ...prev, isTyping: true }))

    handlePicoMessage(assistantCreate(), sessionId)

    expect(store.get(chatAtom).isTyping).toBe(false)
  })
})
