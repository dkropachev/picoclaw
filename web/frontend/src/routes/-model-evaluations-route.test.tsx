import { QueryClient } from "@tanstack/react-query"
import {
  RouterProvider,
  createMemoryHistory,
  createRouter,
} from "@tanstack/react-router"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
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
  ModelEvaluationsPage: ({
    initialEvaluationID,
  }: {
    initialEvaluationID?: string
  }) => (
    <>
      <output>Separate model review probe workspace</output>
      {initialEvaluationID && (
        <output>Selected probe {initialEvaluationID}</output>
      )}
    </>
  ),
}))
vi.mock("@/components/model-evaluations/model-evaluation-report-page", () => ({
  ModelEvaluationReportPage: ({
    evaluationID,
    onBack,
  }: {
    evaluationID: string
    onBack: () => void
  }) => (
    <>
      <output>Visual model report for {evaluationID}</output>
      <button type="button" onClick={onBack}>
        Back to probes
      </button>
    </>
  ),
}))
vi.mock("@/features/chat/controller", () => ({ initializeChatStore: vi.fn() }))

describe("model review probes route", () => {
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
      await screen.findByText("Separate model review probe workspace"),
    ).toBeVisible()
  })

  it("renders a directly addressable dedicated report route", async () => {
    const user = userEvent.setup()
    const evaluationID = "rme_012d820e0d5cf890740e990be0bc3651"
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: [`/model-evaluations/${evaluationID}/report`],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })
    render(<RouterProvider router={router} />)
    expect(
      await screen.findByText(`Visual model report for ${evaluationID}`),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Back to probes" }))
    expect(
      await screen.findByText(`Selected probe ${evaluationID}`),
    ).toBeVisible()
    expect(router.state.location.pathname).toBe("/model-evaluations")
    expect(router.state.location.search).toEqual({ probe: evaluationID })
  })

  it("redirects malformed report identities to the probe workspace", async () => {
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({
        initialEntries: ["/model-evaluations/not-a-probe/report"],
      }),
      context: {
        queryClient: new QueryClient({
          defaultOptions: { queries: { retry: false } },
        }),
      },
    })
    render(<RouterProvider router={router} />)
    expect(
      await screen.findByText("Separate model review probe workspace"),
    ).toBeVisible()
    expect(router.state.location.pathname).toBe("/model-evaluations")
  })
})
