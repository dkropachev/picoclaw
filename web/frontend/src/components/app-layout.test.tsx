import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { AppLayout } from "@/components/app-layout"

vi.mock("@/components/app-header", () => ({
  AppHeader: () => <header>Header</header>,
}))
vi.mock("@/components/app-sidebar", () => ({
  AppSidebar: () => <nav>Navigation</nav>,
}))
vi.mock("@/components/gateway-setup-notice", () => ({
  GatewaySetupNotice: () => null,
}))
vi.mock("@/components/tour/tour-guide", () => ({ TourGuide: () => null }))
vi.mock("@/components/ui/sidebar", () => ({
  SidebarProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock("sonner", () => ({ Toaster: () => null }))

describe("AppLayout accessibility", () => {
  it("provides a keyboard skip link to the single app main landmark", async () => {
    const user = userEvent.setup()
    render(
      <AppLayout>
        <button>Page action</button>
      </AppLayout>,
    )

    await user.tab()
    const skipLink = screen.getByRole("link", { name: "Skip to main content" })
    expect(skipLink).toHaveFocus()
    expect(skipLink).toHaveAttribute("href", "#main-content")
    expect(screen.getByRole("main")).toHaveAttribute("id", "main-content")
    expect(screen.getByRole("main")).toHaveAttribute("tabindex", "-1")
  })
})
