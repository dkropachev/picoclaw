import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { SkillImportPage } from "./skill-import-page"

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

describe("SkillImportPage", () => {
  it("keeps an accessible compact marketplace choice visible on mobile", async () => {
    const user = userEvent.setup()
    const onOpenMarketplace = vi.fn()
    renderPage(onOpenMarketplace)

    const action = screen.getByRole("button", { name: "Browse marketplace" })
    expect(action).not.toHaveClass("hidden")
    expect(action).toHaveAttribute("title", "Browse marketplace")
    expect(action.querySelector("svg")).not.toBeNull()

    await user.click(action)
    expect(onOpenMarketplace).toHaveBeenCalledOnce()
  })
})

function renderPage(onOpenMarketplace: () => void) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <SkillImportPage
        onBack={vi.fn()}
        onImported={vi.fn()}
        onOpenMarketplace={onOpenMarketplace}
      />
    </QueryClientProvider>,
  )
}
