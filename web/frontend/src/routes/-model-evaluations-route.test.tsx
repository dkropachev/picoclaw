import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import { routeTree } from "@/routeTree.gen"

vi.mock("@/api/launcher-auth", () => ({
  getLauncherAuthStatus: vi
    .fn()
    .mockResolvedValue({ authenticated: true, initialized: true }),
}))
vi.mock("@/components/app-layout", () => ({
  AppLayout: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}))
vi.mock("@/components/model-evaluations/model-evaluations-page", () => ({
  ModelEvaluationsPage: () => (
    <output>Separate model evaluation workspace</output>
  ),
}))
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

describe("model evaluations route", () => {
  it("renders the dedicated route", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({ initialEntries: ["/model-evaluations"] }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })
    render(<RouterProvider router={router} />)
    expect(
      await screen.findByText("Separate model evaluation workspace"),
    ).toBeVisible()
  })
})
