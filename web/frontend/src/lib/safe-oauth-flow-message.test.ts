import { describe, expect, it } from "vitest"

import { safeOAuthFlowMessage } from "./safe-oauth-flow-message"

describe("safeOAuthFlowMessage", () => {
  it("normalizes control characters and bounds terminal flow messages", () => {
    expect(safeOAuthFlowMessage(" rejected\n\tby provider ", "fallback")).toBe(
      "rejected by provider",
    )
    expect(safeOAuthFlowMessage("x".repeat(900), "fallback")).toHaveLength(512)
    expect(safeOAuthFlowMessage("\n\t", "fallback")).toBe("fallback")
  })
})
