import { describe, expect, it } from "vitest"

import { normalizeDevelopmentWorkspaceSearch } from "@/routes/development_.$workspaceID"
import { normalizeNewDevelopmentSearch } from "@/routes/development_.new"

describe("development workspace search", () => {
  it("keeps an exact code path and opaque revision only on code tabs", () => {
    expect(
      normalizeDevelopmentWorkspaceSearch({
        tab: "changes",
        path: "src/retry copy.ts",
        revision: "candidate:2+/=",
      }),
    ).toEqual({
      tab: "changes",
      path: "src/retry copy.ts",
      revision: "candidate:2+/=",
    })
    expect(
      normalizeDevelopmentWorkspaceSearch({
        tab: "overview",
        path: "src/retry.ts",
        revision: "candidate:2",
      }),
    ).toEqual({ tab: "overview" })
  })

  it("rejects traversal, absolute paths, control characters, and repeated state", () => {
    for (const path of [
      "../secret",
      "/etc/passwd",
      "src//retry.ts",
      "src\\retry.ts",
    ]) {
      expect(
        normalizeDevelopmentWorkspaceSearch({
          tab: "files",
          path,
          revision: "candidate:2",
        }),
      ).toEqual({ tab: "files" })
    }
    expect(
      normalizeDevelopmentWorkspaceSearch({
        tab: ["files"],
        path: ["src/retry.ts"],
        revision: ["candidate:2"],
      }),
    ).toEqual({ tab: "overview" })
  })

  it("keeps only allowlisted required-action deep links on overview", () => {
    expect(
      normalizeDevelopmentWorkspaceSearch({
        tab: "overview",
        panel: "publication",
        entity: `pgr_${"a".repeat(32)}`,
      }),
    ).toEqual({
      tab: "overview",
      panel: "publication",
      entity: `pgr_${"a".repeat(32)}`,
    })
    expect(
      normalizeDevelopmentWorkspaceSearch({
        tab: "files",
        panel: "private-panel",
        entity: "../../secret",
      }),
    ).toEqual({ tab: "files" })
  })
})

describe("new development search", () => {
  it("keeps one bounded HTTPS issue URL", () => {
    expect(
      normalizeNewDevelopmentSearch({
        issue: "https://github.com/octo/repo/issues/42",
      }),
    ).toEqual({ issue: "https://github.com/octo/repo/issues/42" })
  })

  it("rejects unsafe, non-issue, repeated, and oversized issue state", () => {
    for (const issue of [
      "javascript:alert(1)",
      "https://user:secret@github.com/octo/repo/issues/42",
      "https://github.com/octo/repo/pull/42",
      ["https://github.com/octo/repo/issues/42"],
      `https://github.com/${"a".repeat(4096)}/issues/1`,
    ]) {
      expect(normalizeNewDevelopmentSearch({ issue })).toEqual({})
    }
  })
})
