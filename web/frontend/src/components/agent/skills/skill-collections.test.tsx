import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type SkillSupportItem,
  bulkDeleteSkills,
  getSkill,
  listSkills,
} from "@/api/skills"
import {
  SkillDetailPage,
  SkillsCollectionPage,
} from "@/components/agent/skills/skill-collections"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"

vi.mock("@/api/skills", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/skills")>()
  return {
    ...actual,
    bulkDeleteSkills: vi.fn(),
    deleteSkill: vi.fn(),
    getSkill: vi.fn(),
    listSkills: vi.fn(),
  }
})
vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    titleExtra,
    children,
  }: {
    title: string
    titleExtra?: ReactNode
    children?: ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {titleExtra}
      {children}
    </header>
  ),
}))

const workspaceSkill: SkillSupportItem = {
  id: "review-helper",
  name: "review-helper",
  path: "/workspace/skills/review-helper/SKILL.md",
  source: "workspace",
  description: "Review pull requests.",
  origin: "manual",
  origin_kind: "manual",
  removable: true,
}

const builtinSkill: SkillSupportItem = {
  id: "builtin-docs",
  name: "builtin-docs",
  path: "/builtin/skills/docs/SKILL.md",
  source: "builtin",
  description: "Read documentation.",
  origin: "builtin",
  origin_kind: "builtin",
  removable: false,
}

function skillsPage() {
  return {
    skills: [workspaceSkill, builtinSkill],
    total: 2,
    canonical_query: "ORDER BY name ASC",
    query_schema: { fields: [] },
  }
}

describe("skills collection controller", () => {
  beforeAll(() => {
    Object.defineProperties(HTMLElement.prototype, {
      hasPointerCapture: { configurable: true, value: vi.fn(() => false) },
      setPointerCapture: { configurable: true, value: vi.fn() },
      releasePointerCapture: { configurable: true, value: vi.fn() },
      scrollIntoView: { configurable: true, value: vi.fn() },
    })
  })

  beforeEach(() => {
    vi.clearAllMocks()
    resetCollectionRouteStateMemoryForTests()
    globalThis.localStorage.clear()
    vi.mocked(listSkills).mockResolvedValue(skillsPage())
  })

  it("renders List by default and permits selection only for explicitly removable skills", async () => {
    const user = userEvent.setup()
    renderSkills()
    expect(
      await screen.findByRole("region", { name: "Skills list" }),
    ).toBeVisible()

    await user.click(skillItem("builtin-docs"))
    expect(screen.queryByText("1 selected")).toBeNull()
    await user.click(skillItem("review-helper"))
    expect(screen.getByText("1 selected")).toBeVisible()
  })

  it("retains selection and stable failure codes after partial bulk deletion", async () => {
    vi.mocked(bulkDeleteSkills).mockResolvedValue({
      deleted_ids: [],
      failures: [{ id: "review-helper", code: "read_only_origin" }],
    })
    const user = userEvent.setup()
    renderSkills()
    await screen.findByText("review-helper")

    await user.click(skillItem("review-helper"))
    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(screen.getByRole("button", { name: "Remove selected" }))

    await waitFor(() =>
      expect(bulkDeleteSkills).toHaveBeenCalledWith(["review-helper"]),
    )
    expect(
      await screen.findByText("1 selected item was retained."),
    ).toBeVisible()
    expect(screen.getByText("Read only origin.")).toBeVisible()
    expect(screen.getByText("1 selected")).toBeVisible()
  })

  it("never infers removability from a workspace source", async () => {
    vi.mocked(listSkills).mockResolvedValue({
      ...skillsPage(),
      skills: [
        {
          ...workspaceSkill,
          id: "unmarked-workspace-skill",
          removable: undefined,
        },
      ],
      total: 1,
    })
    const user = userEvent.setup()
    renderSkills()

    await screen.findByText("review-helper")
    await user.click(skillItem("unmarked-workspace-skill"))
    expect(screen.queryByText("1 selected")).toBeNull()
  })

  it("loads routed detail by backend ID and retains specialized content views", async () => {
    vi.mocked(getSkill).mockResolvedValue({
      ...workspaceSkill,
      content: "# Review helper\n\nUse review rules.",
    })
    const user = userEvent.setup()
    renderWithClient(
      <SkillDetailPage skillID="review-helper" onBack={vi.fn()} />,
    )

    expect(
      await screen.findByRole("heading", { name: "Review helper" }),
    ).toBeVisible()
    expect(getSkill).toHaveBeenCalledWith(
      "review-helper",
      expect.any(AbortSignal),
    )
    await user.click(screen.getByRole("button", { name: "Raw" }))
    expect(screen.getByText(/Use review rules/)).toBeVisible()
  })
})

function renderSkills(view?: "list" | "table" | "grid") {
  return renderWithClient(
    <SkillsCollectionPage
      search={{ q: "ORDER BY name ASC", ...(view ? { view } : {}) }}
      onSearchChange={vi.fn()}
      onAdd={vi.fn()}
      onOpen={vi.fn()}
    />,
  )
}

function renderWithClient(element: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{element}</QueryClientProvider>,
  )
}

function skillItem(id: string): HTMLElement {
  const item = document.querySelector<HTMLElement>(`[data-item-id="${id}"]`)
  if (!item) throw new Error(`Missing skill item ${id}`)
  return item
}
