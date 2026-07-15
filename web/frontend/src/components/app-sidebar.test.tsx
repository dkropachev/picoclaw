import { render, screen } from "@testing-library/react"
import { Provider } from "jotai"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

let pathname = "/threads/search"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouterState: () => ({
    location: {
      pathname,
    },
  }),
}))

vi.mock("@/hooks/use-sidebar-channels", () => ({
  useSidebarChannels: () => ({
    channelItems: [],
    hasMoreChannels: false,
    showAllChannels: false,
    toggleShowAllChannels: vi.fn(),
  }),
}))

function renderSidebar() {
  render(
    <Provider>
      <SidebarProvider>
        <AppSidebar collapsible="none" />
      </SidebarProvider>
    </Provider>,
  )
}

describe("AppSidebar", () => {
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

  beforeEach(() => {
    pathname = "/threads/search"
  })

  it("links Threads navigation directly to the thread search workspace", () => {
    renderSidebar()

    expect(screen.getByRole("link", { name: "Threads" })).toHaveAttribute(
      "href",
      "/threads/search",
    )
    expect(screen.queryByRole("link", { name: "Search" })).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: "Thread" })).not.toBeInTheDocument()
  })

  it("keeps Threads navigation on search when viewing a concrete thread", () => {
    pathname = "/threads/open/session-thread"

    renderSidebar()

    expect(screen.getByRole("link", { name: "Threads" })).toHaveAttribute(
      "href",
      "/threads/search",
    )
  })
})
