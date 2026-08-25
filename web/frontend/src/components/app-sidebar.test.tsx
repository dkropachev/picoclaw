import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { Provider } from "jotai"
import type { AnchorHTMLAttributes, ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import { AppSidebar } from "@/components/app-sidebar"
import { SidebarProvider } from "@/components/ui/sidebar"

let pathname = "/development"

vi.mock("@/api/notifications", () => ({
  listDevelopmentNotifications: vi.fn(async () => ({
    notifications: [],
    counts: { open: 3, unread: 2, snoozed: 0 },
  })),
}))

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    activeOptions,
    search,
    ...props
  }: {
    children: ReactNode
    to: string
    activeOptions?: { exact?: boolean }
    search?: Record<string, string>
  } & AnchorHTMLAttributes<HTMLAnchorElement>) => {
    const active = activeOptions?.exact
      ? pathname === to
      : pathname === to || (to !== "/" && pathname.startsWith(`${to}/`))
    const searchString = search ? new URLSearchParams(search).toString() : ""
    return (
      <a
        {...props}
        href={searchString ? `${to}?${searchString}` : to}
        {...(active ? { "aria-current": "page", "data-status": "active" } : {})}
      >
        {children}
      </a>
    )
  },
  useRouterState: () => ({ location: { pathname, search: {} } }),
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
  return render(
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
      value: vi.fn().mockImplementation(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    })
  })

  beforeEach(() => {
    pathname = "/development"
  })

  it("exposes canonical development destinations without pull-request aliases", () => {
    renderSidebar()
    expect(screen.getByRole("link", { name: "Development" })).toHaveAttribute(
      "href",
      "/development",
    )
    expect(screen.getByRole("link", { name: /Notifications/ })).toHaveAttribute(
      "href",
      "/notifications",
    )
    expect(screen.getByRole("link", { name: "Repositories" })).toHaveAttribute(
      "href",
      "/development/repositories",
    )
    expect(screen.getByRole("link", { name: "Policies" })).toHaveAttribute(
      "href",
      "/development/workflow-configurations",
    )
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/development/settings",
    )
    expect(screen.queryByRole("link", { name: "Work" })).not.toBeInTheDocument()
  })

  it("marks nested development workspaces through the Development link", () => {
    pathname = "/development/devw_11111111111111111111111111111111"
    renderSidebar()
    expect(screen.getByRole("link", { name: "Development" })).toHaveAttribute(
      "data-status",
      "active",
    )
  })

  it("marks notification detail navigation and renders open count", async () => {
    pathname = "/notifications/dnt_11111111111111111111111111111111"
    renderSidebar()
    expect(screen.getByRole("link", { name: /Notifications/ })).toHaveAttribute(
      "data-status",
      "active",
    )
    expect(await screen.findByText("3")).toBeInTheDocument()
  })

  it("retains Threads and Events navigation", async () => {
    const user = userEvent.setup()
    pathname = "/events"
    renderSidebar()
    expect(screen.getByRole("link", { name: "Threads" })).toHaveAttribute(
      "href",
      "/threads/search",
    )
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

  it("reveals repository review navigation and marks Review runs", () => {
    pathname = "/repository-reviews"

    renderSidebar()

    expect(screen.getByRole("button", { name: "Services" })).toHaveAttribute(
      "aria-expanded",
      "true",
    )
    expect(
      screen.getByRole("button", { name: "Repository reviews" }),
    ).toHaveAttribute("aria-expanded", "true")
    const link = screen.getByRole("link", { name: "Review runs" })
    expect(link).toHaveAttribute("href", "/repository-reviews")
    expect(link).toBeVisible()
    expect(link.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
    expect(screen.getByRole("link", { name: "Repositories" })).toHaveAttribute(
      "href",
      "/repository-reviews/repositories",
    )
    expect(screen.getByRole("link", { name: "Profiles" })).toHaveAttribute(
      "href",
      "/repository-reviews/profiles",
    )
    expect(screen.getByRole("link", { name: "Results" })).toHaveAttribute(
      "href",
      "/repository-reviews/results",
    )
  })

  it("reveals and marks Model review probes from its direct route", () => {
    pathname = "/model-evaluations"

    renderSidebar()

    expect(screen.getByRole("button", { name: "Services" })).toHaveAttribute(
      "aria-expanded",
      "true",
    )
    expect(
      screen.getByRole("button", { name: "Repository reviews" }),
    ).toHaveAttribute("aria-expanded", "true")
    const link = screen.getByRole("link", { name: "Model review probes" })
    expect(link).toHaveAttribute("href", "/model-evaluations")
    expect(link.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
  })

  it("keeps Model review probes active on a dedicated report route", () => {
    pathname = "/model-evaluations/rme_012d820e0d5cf890740e990be0bc3651/report"

    renderSidebar()

    expect(screen.getByRole("button", { name: "Services" })).toHaveAttribute(
      "aria-expanded",
      "true",
    )
    expect(
      screen.getByRole("button", { name: "Repository reviews" }),
    ).toHaveAttribute("aria-expanded", "true")
    expect(
      screen
        .getByRole("link", { name: "Model review probes" })
        .closest('[data-sidebar="menu-button"]'),
    ).toHaveAttribute("data-active", "true")
  })

  it("shows the MCP link when the dedicated MCP route is active", () => {
    pathname = "/agent/mcp/servers"

    renderSidebar()

    expect(screen.getByRole("link", { name: "MCP Servers" })).toHaveAttribute(
      "href",
      "/agent/mcp/servers",
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
})
