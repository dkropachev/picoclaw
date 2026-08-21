import { render, screen, waitFor } from "@testing-library/react"
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
    activeOptions,
    search,
    state: _state,
    ...props
  }: {
    children: ReactNode
    to: string
    activeOptions?: { exact?: boolean; includeSearch?: boolean }
    search?: Record<string, string>
    state?: unknown
  } & AnchorHTMLAttributes<HTMLAnchorElement>) =>
    (() => {
      void _state
      const pathActive = activeOptions?.exact
        ? pathname === to
        : pathname === to || (to !== "/" && pathname.startsWith(`${to}/`))

      return (
        <a
          {...props}
          href={
            search && Object.keys(search).length > 0
              ? `${to}?${new URLSearchParams(search).toString()}`
              : to
          }
          {...(pathActive
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

  it("reveals and marks Repository reviews from its direct route", () => {
    pathname = "/repository-reviews"

    renderSidebar()

    expect(screen.getByRole("button", { name: "Services" })).toHaveAttribute(
      "aria-expanded",
      "true",
    )
    const link = screen.getByRole("link", { name: "Repository reviews" })
    expect(link).toHaveAttribute("href", "/repository-reviews")
    expect(link).toBeVisible()
    expect(link.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
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

  it("links each Pull requests destination to a first-class URL", async () => {
    pathname = "/pull-requests"
    const user = userEvent.setup()

    renderSidebar()

    const trigger = screen.getByRole("button", { name: "Pull requests" })
    expect(trigger).toHaveAttribute("aria-expanded", "true")
    const work = screen.getByRole("link", { name: "Work" })
    const gateProfiles = screen.getByRole("link", {
      name: "Workflow configurations",
    })
    const repositoryAssignments = screen.getByRole("link", {
      name: "Repository assignments",
    })
    const lifecycleSettings = screen.getByRole("link", {
      name: "Lifecycle settings",
    })
    expect(work).toHaveAttribute("href", "/pull-requests")
    expect(gateProfiles).toHaveAttribute(
      "href",
      "/pull-requests/workflow-configurations",
    )
    expect(repositoryAssignments).toHaveAttribute(
      "href",
      "/pull-requests/repository-assignments",
    )
    expect(lifecycleSettings).toHaveAttribute(
      "href",
      "/pull-requests/settings?tab=nudging",
    )
    expect(work.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
      "data-active",
      "true",
    )
    expect(work).toHaveAttribute("aria-current", "page")
    expect(work).toHaveAttribute("data-status", "active")
    expect(gateProfiles).not.toHaveAttribute("aria-current")
    expect(gateProfiles).not.toHaveAttribute("data-status")
    expect(lifecycleSettings).not.toHaveAttribute("aria-current")
    expect(repositoryAssignments).not.toHaveAttribute("aria-current")
    expect(
      gateProfiles.closest('[data-sidebar="menu-button"]'),
    ).toHaveAttribute("data-active", "false")

    await user.click(trigger)
    expect(trigger).toHaveAttribute("aria-expanded", "false")
    expect(work).not.toBeVisible()
    expect(gateProfiles).not.toBeVisible()
    expect(repositoryAssignments).not.toBeVisible()
    expect(lifecycleSettings).not.toBeVisible()
  })

  it.each([
    ["/pull-requests", "Work"],
    ["/pull-requests/prw_example", "Work"],
    ["/pull-requests/workflow-configurations", "Workflow configurations"],
    [
      "/pull-requests/workflow-configurations/default/edit",
      "Workflow configurations",
    ],
    ["/pull-requests/repository-assignments", "Repository assignments"],
    ["/pull-requests/settings", "Lifecycle settings"],
    ["/pull-requests/settings/review", "Lifecycle settings"],
  ])("marks only %s navigation active", (route, activeName) => {
    pathname = route

    renderSidebar()

    for (const name of [
      "Work",
      "Workflow configurations",
      "Repository assignments",
      "Lifecycle settings",
    ]) {
      const link = screen.getByRole("link", { name })
      const active = name === activeName
      expect(link.closest('[data-sidebar="menu-button"]')).toHaveAttribute(
        "data-active",
        String(active),
      )
      if (active) {
        expect(link).toHaveAttribute("aria-current", "page")
        expect(link).toHaveAttribute("data-status", "active")
      } else {
        expect(link).not.toHaveAttribute("aria-current")
        expect(link).not.toHaveAttribute("data-status")
      }
    }
  })

  it("preserves the originating workspace across configuration links", () => {
    const workspaceID = `prw_${"a".repeat(32)}`
    pathname = "/pull-requests/workflow-configurations/default"
    routeSearch = { flow: "review", from: workspaceID }

    renderSidebar()

    expect(screen.getByRole("link", { name: "Work" })).toHaveAttribute(
      "href",
      `/pull-requests/${workspaceID}`,
    )
    expect(
      screen.getByRole("link", { name: "Workflow configurations" }),
    ).toHaveAttribute(
      "href",
      `/pull-requests/workflow-configurations?from=${workspaceID}`,
    )
    expect(
      screen.getByRole("link", { name: "Repository assignments" }),
    ).toHaveAttribute(
      "href",
      `/pull-requests/repository-assignments?from=${workspaceID}`,
    )
    expect(
      screen.getByRole("link", { name: "Lifecycle settings" }),
    ).toHaveAttribute(
      "href",
      `/pull-requests/settings?tab=nudging&from=${workspaceID}`,
    )
  })

  it("reveals a PR destination after navigation while preserving manual collapse", async () => {
    const user = userEvent.setup()
    const view = renderSidebar()
    const services = screen.getByRole("button", { name: "Services" })

    expect(services).toHaveAttribute("aria-expanded", "false")

    pathname = "/pull-requests/workflow-configurations/default"
    view.rerender(
      <Provider>
        <SidebarProvider>
          <AppSidebar collapsible="none" />
        </SidebarProvider>
      </Provider>,
    )

    await waitFor(() =>
      expect(services).toHaveAttribute("aria-expanded", "true"),
    )
    const pullRequests = screen.getByRole("button", {
      name: "Pull requests",
    })
    expect(pullRequests).toHaveAttribute("aria-expanded", "true")
    expect(
      screen.getByRole("link", { name: "Workflow configurations" }),
    ).toBeVisible()

    await user.click(pullRequests)
    expect(pullRequests).toHaveAttribute("aria-expanded", "false")
    view.rerender(
      <Provider>
        <SidebarProvider>
          <AppSidebar collapsible="none" />
        </SidebarProvider>
      </Provider>,
    )
    expect(pullRequests).toHaveAttribute("aria-expanded", "false")

    pathname = "/pull-requests/settings"
    view.rerender(
      <Provider>
        <SidebarProvider>
          <AppSidebar collapsible="none" />
        </SidebarProvider>
      </Provider>,
    )
    await waitFor(() =>
      expect(pullRequests).toHaveAttribute("aria-expanded", "true"),
    )

    await user.click(services)
    expect(services).toHaveAttribute("aria-expanded", "false")
    view.rerender(
      <Provider>
        <SidebarProvider>
          <AppSidebar collapsible="none" />
        </SidebarProvider>
      </Provider>,
    )
    expect(services).toHaveAttribute("aria-expanded", "false")

    pathname = "/pull-requests/prw_example"
    view.rerender(
      <Provider>
        <SidebarProvider>
          <AppSidebar collapsible="none" />
        </SidebarProvider>
      </Provider>,
    )
    await waitFor(() =>
      expect(services).toHaveAttribute("aria-expanded", "true"),
    )
    expect(screen.getByRole("link", { name: "Work" })).toBeVisible()
  })
})
