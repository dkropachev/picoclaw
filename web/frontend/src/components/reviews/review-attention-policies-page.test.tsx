import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ComponentProps } from "react"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ReviewAttentionAgentCatalog,
  getReviewAttentionAgents,
} from "@/api/review-attention-agents"
import { parseExactJSON, stringifyExactJSON } from "@/api/review-attention-json"
import {
  ReviewAttentionPoliciesAPIError,
  type ReviewAttentionPolicyCatalog,
  type ReviewAttentionPolicySnapshot,
  getReviewAttentionPolicies,
  putReviewAttentionPolicies,
} from "@/api/review-attention-policies"
import { ReviewAttentionPoliciesPage } from "@/components/reviews/review-attention-policies-page"
import { SidebarProvider } from "@/components/ui/sidebar"
import i18n from "@/i18n"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

vi.mock("@/api/review-attention-agents", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/api/review-attention-agents")>()
  return { ...actual, getReviewAttentionAgents: vi.fn() }
})

vi.mock("@/api/review-attention-policies", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/api/review-attention-policies")>()
  return {
    ...actual,
    getReviewAttentionPolicies: vi.fn(),
    putReviewAttentionPolicies: vi.fn(),
  }
})

vi.mock("@tanstack/react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@tanstack/react-router")>()
  return {
    ...actual,
    Link: ({
      to,
      children,
      ...props
    }: ComponentProps<"a"> & { to: string }) => (
      <a href={to} {...props}>
        {children}
      </a>
    ),
    useBlocker: vi.fn(() => ({
      status: "idle",
      current: undefined,
      next: undefined,
      action: undefined,
      proceed: undefined,
      reset: undefined,
    })),
  }
})

vi.mock("@/lib/restart-required", () => ({
  showSaveSuccessOrRestartToast: vi.fn(),
}))

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(),
}))

const exactQuestionsSource =
  '{"large":9007199254740993,"negativeZero":-0,"exponent":1e+03,"Foo":1,"foo":2,"__proto__":{"safe":true}}'

describe("ReviewAttentionPoliciesPage", () => {
  beforeAll(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
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

  beforeEach(async () => {
    await i18n.changeLanguage("en")
    vi.mocked(getReviewAttentionAgents).mockReset()
    vi.mocked(getReviewAttentionPolicies).mockReset()
    vi.mocked(putReviewAttentionPolicies).mockReset()
    vi.mocked(refreshGatewayState).mockReset()
    vi.mocked(showSaveSuccessOrRestartToast).mockReset()

    vi.mocked(getReviewAttentionAgents).mockImplementation(
      async ({ expectedConfigRevision }) =>
        agentCatalog(expectedConfigRevision),
    )
  })

  it("starts with an immutable, inert built-in Default set and shows every known moment as Off", async () => {
    let resolvePolicies!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies).mockReturnValue(
      new Promise((resolve) => {
        resolvePolicies = resolve
      }),
    )
    const user = userEvent.setup()
    const onShowInbox = vi.fn()
    renderPage(onShowInbox)

    expect(screen.getByText("Loading attention rules…")).toBeVisible()
    expect(getReviewAttentionAgents).not.toHaveBeenCalled()
    await user.click(screen.getByRole("button", { name: "Review inbox" }))
    expect(onShowInbox).toHaveBeenCalledOnce()

    await act(async () => resolvePolicies(snapshot(emptyCatalog())))

    expect(
      await screen.findByRole("heading", {
        name: "Build reusable attention rule sets",
      }),
    ).toBeVisible()
    expect(
      screen.getByText(
        "The built-in Default set always exists and begins with every known workflow moment Off. Configure it directly, or duplicate any set to create another permanent name, then make a set the default or assign it to repositories.",
      ),
    ).toBeVisible()
    expect(
      screen.getByText(
        "This name is permanent. Duplicate the set when you need another named version.",
      ),
    ).toBeVisible()
    expect(
      screen.queryByRole("textbox", { name: /name/i }),
    ).not.toBeInTheDocument()

    const knownMoments = screen.getByRole("region", {
      name: "Known workflow moments",
    })
    for (const label of [
      "Outgoing review submitted",
      "My PR development review needs attention",
      "Before pushing my PR changes",
    ]) {
      const row = within(knownMoments).getByText(label).closest("li")
      expect(row).not.toBeNull()
      expect(within(row!).getByText("Off")).toBeVisible()
    }
    expect(screen.getByText("All moments Off")).toBeVisible()
    expect(
      screen.getByText("Nothing triggers human attention in this rule set."),
    ).toBeVisible()
    const deleteButton = screen.getByRole("button", { name: "Delete" })
    expect(deleteButton).toBeDisabled()
    expect(deleteButton).toHaveAttribute(
      "title",
      "The built-in Default set cannot be deleted.",
    )
    expect(
      screen.queryByLabelText("Repository behavior"),
    ).not.toBeInTheDocument()
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()
  })

  it("configures the built-in set and saves one exact normalized catalog with CAS", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(emptyCatalog()),
    )
    vi.mocked(putReviewAttentionPolicies).mockImplementation(async (catalog) =>
      snapshot(catalog, "config-revision-2", "catalog-revision-2"),
    )
    const user = userEvent.setup()
    renderPage()

    const knownMoments = await screen.findByRole("region", {
      name: "Known workflow moments",
    })
    const reviewSubmittedRow = within(knownMoments)
      .getByText("Outgoing review submitted")
      .closest("li")
    await user.click(
      within(reviewSubmittedRow!).getByRole("button", { name: "Configure" }),
    )
    await user.click(screen.getByRole("button", { name: "Add rule" }))

    expect(screen.getByLabelText("When this happens")).toHaveValue(
      "review.submitted",
    )
    expect(
      screen.getByText(
        "No checks are configured, so this rule will not ask for attention.",
      ),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Add check" }))
    await user.click(
      screen.getByRole("button", {
        name: /Ask when a fixed condition matches/,
      }),
    )

    expect(screen.getByLabelText("Check ID")).toHaveValue("confirm_with_owner")
    expect(screen.getByLabelText("How to decide")).toHaveValue("deterministic")
    await user.clear(screen.getByLabelText("Attention title"))
    await user.type(
      screen.getByLabelText("Attention title"),
      "Release confirmation",
    )
    fireEvent.change(screen.getByLabelText("Questions JSON"), {
      target: { value: exactQuestionsSource },
    })

    const save = screen.getByRole("button", { name: "Save rule sets" })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)

    await waitFor(() =>
      expect(putReviewAttentionPolicies).toHaveBeenCalledOnce(),
    )
    const [catalog, expectedRevision] = vi.mocked(putReviewAttentionPolicies)
      .mock.calls[0]
    expect(expectedRevision).toBe("config-revision-1")
    expect(Object.keys(catalog.rule_sets)).toEqual(["default"])
    expect(catalog.default_rule_set_id).toBe("default")
    expect(Object.keys(catalog.repository_assignments)).toEqual([])
    expect(catalog.rule_sets.default.name).toBe("Default")
    const gate = catalog.rule_sets.default.rules["review.submitted"][0]
    expect(gate).toMatchObject({
      id: "confirm_with_owner",
      kind: "deterministic",
      when: "true",
      title: "Release confirmation",
    })
    expect(stringifyExactJSON(gate.questions!)).toBe(exactQuestionsSource)
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalledOnce()
  })

  it("duplicates an exact independent copy under a unique permanent name", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(catalogWithConfiguredDefault()),
    )
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByLabelText("Attention title")).toHaveValue(
      "Original title",
    )
    await user.click(screen.getByRole("button", { name: "Duplicate" }))
    const duplicateDialog = screen.getByRole("dialog", {
      name: "Duplicate rule set",
    })
    const permanentName =
      within(duplicateDialog).getByLabelText("New permanent name")
    await user.type(permanentName, "default")
    expect(
      within(duplicateDialog).getByText("Rule-set names must be unique."),
    ).toBeVisible()
    expect(
      within(duplicateDialog).getByRole("button", {
        name: "Create duplicate",
      }),
    ).toBeDisabled()

    await user.clear(permanentName)
    await user.type(permanentName, "Strict Reviews")
    await user.click(
      within(duplicateDialog).getByRole("button", {
        name: "Create duplicate",
      }),
    )

    const strictEditor = screen.getByRole("region", { name: "Strict Reviews" })
    expect(
      within(strictEditor).getByText(
        "This name is permanent. Duplicate the set when you need another named version.",
      ),
    ).toBeVisible()
    expect(
      within(strictEditor).queryByText("Current default"),
    ).not.toBeInTheDocument()
    expect(screen.getByLabelText("Questions JSON")).toHaveValue(
      exactQuestionsSource,
    )
    await user.clear(screen.getByLabelText("Attention title"))
    await user.type(screen.getByLabelText("Attention title"), "Copy-only edit")

    await selectRuleSet(user, "Default")
    expect(screen.getByLabelText("Attention title")).toHaveValue(
      "Original title",
    )
    await selectRuleSet(user, "Strict Reviews")
    expect(screen.getByLabelText("Attention title")).toHaveValue(
      "Copy-only edit",
    )
    expect(
      screen.getByText(
        "No repository-specific assignments. Every repository follows the current default.",
      ),
    ).toBeVisible()
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()
  })

  it("makes any set the fallback, assigns it to multiple repositories, and permits an explicit built-in assignment", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(catalogWithNamedSets()),
    )
    vi.mocked(putReviewAttentionPolicies).mockImplementation(async (catalog) =>
      snapshot(catalog, "config-revision-2", "catalog-revision-2"),
    )
    const user = userEvent.setup()
    renderPage()

    await selectRuleSet(user, "Strict Reviews")
    await user.click(screen.getByRole("button", { name: "Make default" }))
    expect(screen.getByText("Current default")).toBeVisible()

    await user.clear(screen.getByLabelText("Attention title"))
    await user.type(
      screen.getByLabelText("Attention title"),
      "Edited shared rule",
    )
    await user.click(
      screen.getByRole("button", { name: "Assign repositories" }),
    )
    const assignmentDialog = screen.getByRole("dialog", {
      name: "Assign repositories to rule sets",
    })
    const repository = within(assignmentDialog).getByLabelText("Repository")
    const ruleSet = within(assignmentDialog).getByLabelText("Rule set")
    expect(ruleSet).toHaveValue("strict")

    await user.type(repository, "acme/one")
    await user.click(
      within(assignmentDialog).getByRole("button", { name: "Add assignment" }),
    )
    await user.type(repository, "acme/two")
    await user.click(
      within(assignmentDialog).getByRole("button", { name: "Add assignment" }),
    )
    await user.selectOptions(ruleSet, "default")
    await user.type(repository, "acme/off")
    await user.click(
      within(assignmentDialog).getByRole("button", { name: "Add assignment" }),
    )
    await user.click(
      within(assignmentDialog).getByRole("button", { name: "Done" }),
    )

    expect(screen.getByLabelText("Rule set for acme/one")).toHaveValue("strict")
    expect(screen.getByLabelText("Rule set for acme/two")).toHaveValue("strict")
    expect(screen.getByLabelText("Rule set for acme/off")).toHaveValue(
      "default",
    )
    await user.click(
      screen.getByRole("button", {
        name: /Remove acme\/one assignment; it will follow the current default/,
      }),
    )
    expect(
      screen.queryByLabelText("Rule set for acme/one"),
    ).not.toBeInTheDocument()
    expect(
      screen.getByText(
        /Repositories without an assignment follow the current default: Strict Reviews/,
      ),
    ).toBeVisible()

    const save = screen.getByRole("button", { name: "Save rule sets" })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)

    await waitFor(() =>
      expect(putReviewAttentionPolicies).toHaveBeenCalledOnce(),
    )
    const [catalog, expectedRevision] = vi.mocked(putReviewAttentionPolicies)
      .mock.calls[0]
    expect(expectedRevision).toBe("config-revision-1")
    expect(catalog.default_rule_set_id).toBe("strict")
    expect(
      Object.fromEntries(Object.entries(catalog.repository_assignments)),
    ).toEqual({
      "acme/off": "default",
      "acme/two": "strict",
    })
    expect(catalog.rule_sets.default.name).toBe("Default")
    expect(catalog.rule_sets.strict.rules["review.submitted"][0].title).toBe(
      "Edited shared rule",
    )
    expect(
      stringifyExactJSON(
        catalog.rule_sets.strict.rules["review.submitted"][0].questions!,
      ),
    ).toBe(exactQuestionsSource)
  })

  it("guards built-in, fallback, and assigned sets while confirming deletion of an unused set", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(catalogForDeletion()),
    )
    const user = userEvent.setup()
    renderPage()

    const deleteButton = await screen.findByRole("button", { name: "Delete" })
    expect(deleteButton).toBeDisabled()
    expect(deleteButton).toHaveAttribute(
      "title",
      "The built-in Default set cannot be deleted.",
    )

    await selectRuleSet(user, "Current Fallback")
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled()
    expect(
      screen.getByText("Choose another default before deleting this set."),
    ).toBeVisible()

    await selectRuleSet(user, "Assigned")
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled()
    expect(
      screen.getByText(
        "Remove its repository assignments before deleting this set.",
      ),
    ).toBeVisible()

    await selectRuleSet(user, "Unused")
    expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled()
    await user.click(screen.getByRole("button", { name: "Delete" }))
    const confirmation = screen.getByRole("alertdialog", {
      name: "Delete this rule set?",
    })
    await user.click(
      within(confirmation).getByRole("button", { name: "Cancel" }),
    )
    expect(ruleSetButton("Unused")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Delete" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", { name: "Delete this rule set?" }),
      ).getByRole("button", { name: "Delete rule set" }),
    )
    expect(queryRuleSetButton("Unused")).not.toBeInTheDocument()
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()
  })

  it("preserves a dirty draft on a CAS conflict and replaces it only after explicit reload confirmation", async () => {
    const initial = snapshot(emptyCatalog())
    const latest = snapshot(
      {
        rule_sets: {
          default: { name: "Default", rules: {} },
          server: { name: "Server Rules", rules: {} },
        },
        default_rule_set_id: "server",
        repository_assignments: {},
      },
      "config-revision-2",
      "catalog-revision-2",
    )
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockResolvedValue(latest)
    vi.mocked(putReviewAttentionPolicies).mockRejectedValueOnce(
      new ReviewAttentionPoliciesAPIError("config_revision_mismatch", 409),
    )
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole("button", { name: "Duplicate" }))
    await user.type(screen.getByLabelText("New permanent name"), "Local Draft")
    await user.click(screen.getByRole("button", { name: "Create duplicate" }))
    expect(screen.getByRole("heading", { name: "Local Draft" })).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Save rule sets" }))
    expect(
      await screen.findByText(
        "These rules changed elsewhere. Your draft is preserved; reload the latest version before saving.",
      ),
    ).toBeVisible()
    expect(screen.getByRole("heading", { name: "Local Draft" })).toBeVisible()
    expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2)

    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    const reloadConfirmation = screen.getByRole("alertdialog", {
      name: "Discard this rule draft?",
    })
    expect(
      within(reloadConfirmation).getByText(
        "Reloading replaces every unsaved rule edit with the latest trusted configuration.",
      ),
    ).toBeVisible()
    await user.click(
      within(reloadConfirmation).getByRole("button", {
        name: "Discard and reload",
      }),
    )

    await waitFor(() =>
      expect(queryRuleSetButton("Local Draft")).not.toBeInTheDocument(),
    )
    expect(ruleSetButton("Server Rules")).toBeVisible()
    await selectRuleSet(user, "Server Rules")
    expect(screen.getByText("Current default")).toBeVisible()
    expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(3)
    expect(getReviewAttentionAgents).toHaveBeenLastCalledWith({
      expectedConfigRevision: "config-revision-2",
    })
  })

  it("fails initial hydration closed until agents from the same configuration generation arrive", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(emptyCatalog()),
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("older-config-revision"))
      .mockResolvedValueOnce(agentCatalog("config-revision-1"))
    const user = userEvent.setup()
    renderPage()

    expect(
      await screen.findByText(
        "Attention rules and configured agents could not be loaded as one trusted generation.",
      ),
    ).toBeVisible()
    expect(
      screen.queryByRole("complementary", { name: "Rule sets" }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText("All moments Off")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Retry" }))

    expect(
      await screen.findByRole("heading", {
        name: "Build reusable attention rule sets",
      }),
    ).toBeVisible()
    expect(screen.getByText("All moments Off")).toBeVisible()
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2)
  })
})

function renderPage(onShowInbox = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <ReviewAttentionPoliciesPage
          onShowInbox={onShowInbox}
          onShowDevelopment={vi.fn()}
        />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  return { ...view, client }
}

async function selectRuleSet(
  user: ReturnType<typeof userEvent.setup>,
  name: string,
) {
  const list = await screen.findByRole("complementary", { name: "Rule sets" })
  await user.click(
    within(list).getByRole("button", {
      name: new RegExp(`^${escapeRegExp(name)}`),
    }),
  )
}

function ruleSetButton(name: string): HTMLElement {
  return within(
    screen.getByRole("complementary", { name: "Rule sets" }),
  ).getByRole("button", { name: new RegExp(`^${escapeRegExp(name)}`) })
}

function queryRuleSetButton(name: string): HTMLElement | null {
  return within(
    screen.getByRole("complementary", { name: "Rule sets" }),
  ).queryByRole("button", {
    name: new RegExp(`^${escapeRegExp(name)}`),
  })
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}

function emptyCatalog(): ReviewAttentionPolicyCatalog {
  return {
    rule_sets: { default: { name: "Default", rules: {} } },
    default_rule_set_id: "default",
    repository_assignments: {},
  }
}

function catalogWithConfiguredDefault(): ReviewAttentionPolicyCatalog {
  return {
    rule_sets: {
      default: {
        name: "Default",
        rules: {
          "review.submitted": [deterministicGate("Original title")],
        },
      },
    },
    default_rule_set_id: "default",
    repository_assignments: {},
  }
}

function catalogWithNamedSets(): ReviewAttentionPolicyCatalog {
  return {
    rule_sets: {
      default: { name: "Default", rules: {} },
      strict: {
        name: "Strict Reviews",
        rules: {
          "review.submitted": [deterministicGate("Strict title")],
        },
      },
      relaxed: { name: "Relaxed", rules: {} },
    },
    default_rule_set_id: "default",
    repository_assignments: {},
  }
}

function catalogForDeletion(): ReviewAttentionPolicyCatalog {
  return {
    rule_sets: {
      default: { name: "Default", rules: {} },
      current: { name: "Current Fallback", rules: {} },
      assigned: { name: "Assigned", rules: {} },
      unused: { name: "Unused", rules: {} },
    },
    default_rule_set_id: "current",
    repository_assignments: { "acme/assigned": "assigned" },
  }
}

function deterministicGate(title: string) {
  return {
    id: "confirm_owner",
    kind: "deterministic" as const,
    when: "true",
    title,
    questions: parseExactJSON(exactQuestionsSource),
  }
}

function agentCatalog(
  configRevision = "config-revision-1",
): ReviewAttentionAgentCatalog {
  return {
    agents: [
      { id: "main", name: "Main" },
      { id: "reviewer", name: "Reviewer" },
    ],
    default_agent_id: "main",
    config_revision: configRevision,
  }
}

function snapshot(
  catalog: ReviewAttentionPolicyCatalog,
  configRevision = "config-revision-1",
  catalogRevision = "catalog-revision-1",
): ReviewAttentionPolicySnapshot {
  return {
    ...catalog,
    catalog_revision: catalogRevision,
    config_revision: configRevision,
    effects: { gateway_effect: "applied" },
  }
}
