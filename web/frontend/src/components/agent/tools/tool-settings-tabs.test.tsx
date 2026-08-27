import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import type { ThreadPolicyConfig, WebSearchConfigResponse } from "@/api/tools"

import { ThreadPolicyTab } from "./thread-policy-tab"
import { WebSearchTab } from "./web-search-tab"

const webSearchDraft: WebSearchConfigResponse = {
  provider: "duckduckgo",
  current_service: "duckduckgo",
  prefer_native: false,
  providers: [
    {
      id: "duckduckgo",
      label: "DuckDuckGo",
      configured: true,
      current: true,
      requires_auth: false,
    },
  ],
  model_aliases: [],
  settings: {
    duckduckgo: { enabled: true, max_results: 10 },
  },
}

const threadPolicyDraft: ThreadPolicyConfig = {
  enabled: true,
  mode: "auto",
  instructions: "",
  rules: [],
}

describe("routed tool settings content", () => {
  it("keeps web search save controls while suppressing its embedded h1", () => {
    render(
      <WebSearchTab
        showHeader={false}
        draft={webSearchDraft}
        providerLabelMap={new Map([["duckduckgo", "DuckDuckGo"]])}
        expandedProvider={null}
        isLoading={false}
        hasError={false}
        isSaving={false}
        isDirty={false}
        onSave={vi.fn()}
        onToggleProviderExpand={vi.fn()}
        onUpdateDraft={vi.fn()}
      />,
    )

    expect(
      screen.queryByRole("heading", {
        level: 1,
        name: "Web Search Configuration",
      }),
    ).toBeNull()
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeVisible()
  })

  it("keeps thread policy save controls while suppressing its embedded h1", () => {
    render(
      <ThreadPolicyTab
        showHeader={false}
        draft={threadPolicyDraft}
        isLoading={false}
        hasError={false}
        isSaving={false}
        isDirty={false}
        onSave={vi.fn()}
        onUpdateDraft={vi.fn()}
      />,
    )

    expect(
      screen.queryByRole("heading", { level: 1, name: "Thread Policy" }),
    ).toBeNull()
    expect(screen.getByRole("button", { name: "Save Changes" })).toBeVisible()
  })
})
