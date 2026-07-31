import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, describe, expect, it, vi } from "vitest"

import type { AgentInfo } from "@/api/agents"
import { SidebarProvider } from "@/components/ui/sidebar"

import { AgentDetailPage } from "./agent-detail-page"

describe("AgentDetailPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  it("keeps keyboard focus on the current tab until navigation commits", async () => {
    const onTabChange = vi.fn()
    const user = userEvent.setup()

    render(
      <SidebarProvider>
        <AgentDetailPage
          agent={agent()}
          agentID="reviewer"
          tab="overview"
          loading={false}
          loadError={false}
          onBack={vi.fn()}
          onTabChange={onTabChange}
          onEdit={vi.fn()}
          onRefresh={vi.fn()}
        />
      </SidebarProvider>,
    )

    const overview = screen.getByRole("tab", { name: "Overview" })
    overview.focus()
    await user.keyboard("{ArrowRight}")

    expect(onTabChange).toHaveBeenCalledWith("capabilities")
    expect(overview).toHaveFocus()
    expect(screen.getByRole("tab", { name: "Capabilities" })).not.toHaveFocus()
  })
})

function agent(): AgentInfo {
  return {
    id: "reviewer",
    name: "Reviewer",
    workspace: "",
    account_ref: "",
    model: null,
    skills: null,
    subagents: null,
    is_default: false,
    default_configured: true,
    implicit: false,
  }
}
