import { describe, expect, it } from "vitest"

import { isReservedAccountRouterCreateName } from "./account-router-validation"

describe("account router route identity validation", () => {
  it("reserves the static New route case-insensitively for creation", () => {
    expect(isReservedAccountRouterCreateName("new")).toBe(true)
    expect(isReservedAccountRouterCreateName(" NEW ")).toBe(true)
    expect(isReservedAccountRouterCreateName("new-router")).toBe(false)
  })
})
