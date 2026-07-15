import { describe, expect, it } from "vitest"

import {
  buildThreadInitialPrompt,
  buildThreadInitialPromptFromCandidates,
  buildThreadSourceQuery,
} from "@/features/chat/thread-seed"

describe("thread seed helpers", () => {
  it("uses New thread only as metadata fallback", () => {
    expect(buildThreadSourceQuery()).toBe("New thread")
    expect(buildThreadInitialPrompt()).toBe("")
    expect(
      buildThreadInitialPrompt(
        "Start this thread by asking what we should work on here.",
      ),
    ).toBe("")
  })

  it("extracts the real task from a start-thread request", () => {
    expect(
      buildThreadInitialPrompt(
        "can you start a thread about planning to relocate to japan, i want you to go over all the possible options how we as family of 3 can go there",
      ),
    ).toBe(
      "go over all the possible options how we as a family of 3 can relocate to japan",
    )
  })

  it("falls back across switch payload candidates", () => {
    expect(
      buildThreadInitialPromptFromCandidates(
        "",
        "New thread",
        "planning to relocate to japan",
      ),
    ).toBe("planning to relocate to japan")
  })
})
