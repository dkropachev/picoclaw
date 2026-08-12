import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider } from "jotai"
import type { AnchorHTMLAttributes, ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

let pathname = "/threads/search"
let routeSearch: Record<string, unknown> = {}

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    search,
    activeOptions,
    ...props
  }: {
    children: ReactNode
    to: string
    search?: Record<string, unknown>
    activeOptions?: { exact?: boolean; includeSearch?: boolean }
  } & AnchorHTMLAttributes<HTMLAnchorElement>) =>
    (() => {
      const pathActive = activeOptions?.exact
        ? pathname === to
        : pathname === to || (to !== "/" && pathname.startsWith(`${to}/`))
      const searchActive =
        activeOptions?.includeSearch === false ||
        (activeOptions?.exact
          ? JSON.stringify(routeSearch) === JSON.stringify(search ?? {})
          : Object.entries(search ?? {}).every(
              ([key, value]) => routeSearch[key] === value,
            ))
      const nativeActive = pathActive && searchActive

      return (
        <a
          {...props}
          href={
            search && Object.keys(search).length > 0
              ? `${to}?${new URLSearchParams(
                  Object.entries(search).map(([key, value]) => [
                    key,
                    String(value),
                  ]),
                ).toString()}`
              : to
          }
          {...(nativeActive
            ? { "aria-current": "page", "data-status": "active" }
            : {})}
        >
          {children}
        </a>
      )
    })(),
  useRouterState: () => ({
    location: {
      pathname,
      search: routeSearch,
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
    routeSearch = {}
  })

  it("links Threads navigation directly to the thread search workspace", () => {
    renderSidebar()

    expect(screen.getByRole("link", { name: "Threads" })).toHaveAttribute(
      "href",
      "/threads/search",
    )
    expect(
      screen.queryByRole("link", { name: "Search" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("link", { name: "Thread" }),
    ).not.toBeInTheDocument()
  })

  it("keeps Threads navigation on search when viewing a concrete thread", () => {
    pathname = "/threads/open/session-thread"

    renderSidebar()

    expect(screen.getByRole("link", { name: "Threads" })).toHaveAttribute(
      "href",
      "/threads/search",
    )
  })

  it("links Events from Services and marks the route active", async () => {
    pathname = "/events"
    const user = userEvent.setup()

    renderSidebar()
    await user.click(screen.getByRole("button", { name: "Services" }))

    expect(screen.getByRole("link", { name: "Events" })).toHaveAttribute(
      "href",
      "/events",
    )
    const activeItem = screen
      .getByRole("link", { name: "Events" })
      .closest('[data-sidebar="menu-button"]')
    expect(activeItem).toHaveAttribute("data-active", "true")
  })

  it("shows the MCP link when the dedicated MCP route is active", () => {
    pathname = "/agent/mcp"

    renderSidebar()

    expect(screen.getByRole("link", { name: "MCP Servers" })).toHaveAttribute(
      "href",
      "/agent/mcp",
    )
    expect(screen.getByRole("link", { name: "MCP Servers" })).toBeVisible()
  })

  it("links agent management first in the Agent section", () => {
    pathname = "/agent/agents"

    renderSidebar()

    const agentsLink = screen.getByRole("link", { name: "Agents" })
    const hubLink = screen.getByRole("link", { name: "Hub" })
    expect(agentsLink).toHaveAttribute("href", "/agent/agents")
    expect(
      agentsLink.compareDocumentPosition(hubLink) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(agentsLink.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
  })

  it("groups pull request work and configuration in a collapsible PR Reviews section", async () => {
    pathname = "/reviews"
    const user = userEvent.setup()

    renderSidebar()

    const trigger = screen.getByRole("button", { name: "PR Reviews" })
    expect(trigger).toHaveAttribute("aria-expanded", "true")
    const work = screen.getByRole("link", { name: "Pull request work" })
    const configuration = screen.getByRole("link", {
      name: "Configuration",
    })
    expect(work).toHaveAttribute("href", "/reviews")
    expect(configuration).toHaveAttribute("href", "/reviews?view=policies")
    expect(work.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
    expect(work).toHaveAttribute("aria-current", "page")
    expect(work).toHaveAttribute("data-status", "active")
    expect(configuration).not.toHaveAttribute("aria-current")
    expect(configuration).not.toHaveAttribute("data-status")
    expect(
      configuration.closest('[data-sidebar="menu-button"]'),
    ).toHaveAttribute("data-active", "false")

    await user.click(trigger)
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(work).not.toBeVisible()
    expect(configuration).not.toBeVisible()
  })

  it("marks only PR Reviews configuration active for the policies view", () => {
    pathname = "/reviews"
    routeSearch = { view: "policies" }

    renderSidebar()

    const work = screen.getByRole("link", { name: "Pull request work" })
    const configuration = screen.getByRole("link", {
      name: "Configuration",
    })
    expect(work.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "false",
    )
    expect(
      configuration.closest('[data-sidebar="menu-button"]'),
    ).toHaveAttribute("data-active", "true")
    expect(work).not.toHaveAttribute("aria-current")
    expect(work).not.toHaveAttribute("data-status")
    expect(configuration).toHaveAttribute("aria-current", "page")
    expect(configuration).toHaveAttribute("data-status", "active")
  })
})
