import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest"

import {
  type ReviewAttentionAgentCatalog,
  ReviewAttentionAgentsAPIError,
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

    vi.mocked(getReviewAttentionAgents).mockResolvedValue(agentCatalog())
    vi.mocked(refreshGatewayState).mockResolvedValue({
      status: "running",
      canStart: true,
      restartRequired: false,
    })
  })

  it("shows loading, then renders every gate kind and repository override mode without executing anything", async () => {
    let resolvePolicies!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies).mockReturnValue(
      new Promise((resolve) => {
        resolvePolicies = resolve
      }),
    )
    const onShowInbox = vi.fn()
    const user = userEvent.setup()

    renderPage(onShowInbox)

    expect(screen.getByText("Loading review attention policies…")).toBeVisible()
    expect(screen.getByRole("button", { name: "Review inbox" })).toBeVisible()
    expect(getReviewAttentionAgents).not.toHaveBeenCalled()
    await user.click(screen.getByRole("button", { name: "Review inbox" }))
    expect(onShowInbox).toHaveBeenCalledOnce()

    await act(async () => resolvePolicies(mixedSnapshot()))

    expect(
      await screen.findByText("Review events trigger attention"),
    ).toBeVisible()
    expect(
      screen.getByText(
        "Use review.submitted for reviews you send and pr_development.review_attention_required for reviewer feedback on your PRs. Matching policies are queued and run when the attention runtime is active. Editing here never runs a gate, calls a model, modifies a repository, or publishes to GitHub.",
      ),
    ).toBeVisible()
    const decisionPoint = screen.getAllByLabelText("Decision point")[0]
    const presets = document.getElementById(
      decisionPoint.getAttribute("list") ?? "",
    )
    const presetOptions = Array.from(presets?.querySelectorAll("option") ?? [])
    expect(presetOptions.map((option) => option.value)).toEqual([
      "review.submitted",
      "pr_development.review_attention_required",
    ])
    expect(presetOptions.map((option) => option.label)).toEqual([
      "Outgoing review submitted",
      "My PR development review needs attention",
    ])
    await waitFor(() => expect(getReviewAttentionAgents).toHaveBeenCalledOnce())
    const initialAgentRequest = vi.mocked(getReviewAttentionAgents).mock
      .calls[0][0]
    expect(initialAgentRequest.expectedConfigRevision).toBe("config-revision-1")
    expect(initialAgentRequest).not.toHaveProperty("cursor")
    expect(
      screen
        .getAllByLabelText("Gate type")
        .map((field) => (field as HTMLSelectElement).value),
    ).toEqual([
      "ai_working_context",
      "ai_isolated_context",
      "deterministic",
      "zero",
    ])
    expect(screen.getAllByLabelText("What AI should look for")[0]).toHaveValue(
      "Ask the owner only when repository intent is required.",
    )
    expect(screen.getByLabelText("Deterministic condition")).toHaveValue(
      "inputs.review.blocking == true",
    )

    await user.click(screen.getByRole("button", { name: /octo\/repo/ }))

    expect(
      screen
        .getAllByLabelText("Override mode")
        .map((field) => (field as HTMLSelectElement).value),
    ).toEqual(["disable", "inherit", "overlay", "replace"])
    expect(screen.getAllByText("Effective repository policy")).toHaveLength(4)
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()
    expect(refreshGatewayState).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Review inbox" }))
    expect(onShowInbox).toHaveBeenCalledTimes(2)
  })

  it("pages a large decision scope and defers whole-catalog validation while editing", async () => {
    const revision = "large-policy-scope-revision"
    const global = Object.fromEntries(
      Array.from({ length: 9 }, (_, index) => [
        `review.point_${String(index).padStart(2, "0")}`,
        [{ id: `gate_${index}`, kind: "zero" as const }],
      ]),
    )
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(
        { global, repositories: {} },
        revision,
        "catalog-large-policy-scope",
      ),
    )
    vi.mocked(getReviewAttentionAgents).mockResolvedValue(
      agentPage(revision, [{ id: "main", name: "Main" }]),
    )
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByText("Decision policies 1–8 of 9")).toBeVisible()
    expect(screen.getAllByLabelText("Decision point")).toHaveLength(8)
    expect(screen.getAllByLabelText("Decision point")[0]).toHaveValue(
      "review.point_00",
    )

    await user.click(screen.getByRole("button", { name: "Next policies" }))
    expect(screen.getByText("Decision policies 9–9 of 9")).toBeVisible()
    const lastDecision = screen.getByLabelText("Decision point")
    expect(lastDecision).toHaveValue("review.point_08")
    await user.type(lastDecision, "x")
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Save policies" }),
      ).toBeEnabled(),
    )
  })

  it("associates actionable field errors and active scope state with their controls", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(mixedSnapshot())
    const user = userEvent.setup()
    renderPage()

    const globalScope = await screen.findByRole("button", {
      name: /Global defaults/,
    })
    const repositoryScope = screen.getByRole("button", { name: /octo\/repo/ })
    expect(globalScope).toHaveAttribute("aria-pressed", "true")
    expect(repositoryScope).toHaveAttribute("aria-pressed", "false")

    const gateID = screen.getAllByLabelText("Gate ID")[0]
    await user.clear(gateID)
    await waitFor(() => expect(gateID).toHaveAttribute("aria-invalid", "true"))
    const descriptionID = gateID.getAttribute("aria-describedby")
    expect(descriptionID).toBeTruthy()
    expect(document.getElementById(descriptionID!)).toHaveTextContent(
      "Gate ID must",
    )
    expect(
      screen.getByText(/Global · review\.submitted · gate 1 · id/),
    ).toBeVisible()

    await user.click(repositoryScope)
    expect(repositoryScope).toHaveAttribute("aria-pressed", "true")
    expect(globalScope).toHaveAttribute("aria-pressed", "false")
  })

  it("adds and reorders a second gate, then saves a lossless full replacement against the captured revision", async () => {
    const initial = replacementSnapshot()
    vi.mocked(getReviewAttentionAgents).mockResolvedValue(
      agentCatalog("config-revision-7"),
    )
    let resolveStaleRead!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveStaleRead = resolve
        }),
      )
    let resolveSave!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(putReviewAttentionPolicies).mockReturnValue(
      new Promise((resolve) => {
        resolveSave = resolve
      }),
    )
    const user = userEvent.setup()
    const onShowInbox = vi.fn()
    const { client } = renderPage(onShowInbox)

    await screen.findByRole("group", { name: "Gate 1" })
    await user.click(screen.getByRole("button", { name: "Add gate" }))
    const secondGate = screen.getByRole("group", { name: "Gate 2" })
    await user.type(within(secondGate).getByLabelText("Gate ID"), "second_gate")
    await user.click(
      within(secondGate).getByRole("button", { name: "Move gate up" }),
    )

    expect(
      screen
        .getAllByLabelText("Gate ID")
        .map((field) => (field as HTMLInputElement).value),
    ).toEqual(["second_gate", "deterministic_gate"])
    await user.click(screen.getByRole("button", { name: "Save policies" }))

    await waitFor(() =>
      expect(putReviewAttentionPolicies).toHaveBeenCalledOnce(),
    )
    const [catalog, expectedRevision] = vi.mocked(putReviewAttentionPolicies)
      .mock.calls[0]
    expect(expectedRevision).toBe("config-revision-7")
    expect(Object.keys(catalog.global)).toEqual(["review.submitted"])
    expect(catalog.global["review.submitted"].map((gate) => gate.id)).toEqual([
      "second_gate",
      "deterministic_gate",
    ])
    expect(catalog.global["review.submitted"][0]).toEqual({
      id: "second_gate",
      kind: "zero",
    })
    expect(
      stringifyExactJSON(catalog.global["review.submitted"][1].questions!),
    ).toBe(exactQuestionsSource)
    expect(Object.keys(catalog.repositories)).toEqual(["octo/keep"])
    expect(catalog.repositories["octo/keep"]["review.submitted"]).toEqual({
      mode: "inherit",
      gates: [],
    })
    expect(screen.getAllByLabelText("Gate ID")[0]).toBeDisabled()
    const inboxTab = screen.getByRole("button", { name: "Review inbox" })
    expect(inboxTab).toBeDisabled()
    await user.click(inboxTab)
    expect(onShowInbox).not.toHaveBeenCalled()

    // Simulate an external invalidation that starts an old read while the PUT
    // is in flight. The successful replacement must cancel and supersede it.
    let staleRefetch!: Promise<void>
    act(() => {
      staleRefetch = client.refetchQueries({
        queryKey: ["reviews", "attention-policies"],
        exact: true,
      })
    })
    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2),
    )

    await act(async () =>
      resolveSave({
        ...catalog,
        catalog_revision: "catalog-revision-8",
        config_revision: "config-revision-8",
        effects: { gateway_effect: "applied" },
      }),
    )
    expect(refreshGatewayState).toHaveBeenCalledWith({ force: true })
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalledOnce()
    expect(
      client.getQueryData<ReviewAttentionAgentCatalog>([
        "reviews",
        "attention-policy-agents",
        "config-revision-8",
        "first",
      ]),
    ).toMatchObject({ config_revision: "config-revision-8" })
    expect(
      client.getQueryData<ReviewAttentionAgentCatalog>([
        "reviews",
        "attention-policy-agents",
        "config-revision-7",
        "first",
      ]),
    ).toBeUndefined()
    expect(
      client
        .getQueryCache()
        .findAll({ queryKey: ["reviews", "attention-policy-agents"] })
        .map((query) => query.queryKey),
    ).toEqual([
      ["reviews", "attention-policy-agents", "config-revision-8", "first"],
    ])
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Save policies" }),
      ).toBeDisabled(),
    )

    await act(async () => {
      resolveStaleRead(
        conflictSnapshot(
          "A stale read must not replace the saved generation",
          "stale-revision",
          '{"stale":9007199254740991}',
        ),
      )
      await staleRefetch
    })
    expect(
      screen
        .getAllByLabelText("Gate ID")
        .map((field) => (field as HTMLInputElement).value),
    ).toEqual(["second_gate", "deterministic_gate"])
  })

  it("selects an agent on page two, keeps it on page one, and submits it against the same revision", async () => {
    const revision = "paged-selection-revision"
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      conflictSnapshot("Choose a reviewer", revision, '{"value":1}'),
    )
    vi.mocked(getReviewAttentionAgents).mockImplementation(async (request) => {
      expect(request.expectedConfigRevision).toBe(revision)
      if (request.cursor === "256") {
        return agentPage(revision, [{ id: "page-two", name: "Page Two" }])
      }
      expect(request).not.toHaveProperty("cursor")
      return agentPage(
        revision,
        [
          { id: "main", name: "Main" },
          { id: "page-one", name: "Page One" },
          { id: "reviewer", name: "Reviewer" },
        ],
        "256",
      )
    })
    vi.mocked(putReviewAttentionPolicies).mockImplementation(
      async (catalog) => ({
        ...catalog,
        catalog_revision: "catalog-paged-selection-next",
        config_revision: "paged-selection-revision-next",
        effects: { gateway_effect: "applied" },
      }),
    )
    const user = userEvent.setup()
    renderPage()

    expect(
      await screen.findByText("AI agent page 1 · 3 identities"),
    ).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Next agents" }))
    expect(
      await screen.findByText("AI agent page 2 · 1 identities"),
    ).toBeVisible()

    await user.selectOptions(screen.getByLabelText("AI agent"), "page-two")
    expect(screen.getByLabelText("AI agent")).toHaveValue("page-two")
    await user.click(screen.getByRole("button", { name: "Previous agents" }))
    expect(
      await screen.findByText("AI agent page 1 · 3 identities"),
    ).toBeVisible()
    expect(screen.getByLabelText("AI agent")).toHaveValue("page-two")
    expect(
      within(screen.getByLabelText("AI agent")).getByRole("option", {
        name: "page-two (page-two)",
      }),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Save policies" }))
    await waitFor(() =>
      expect(putReviewAttentionPolicies).toHaveBeenCalledOnce(),
    )
    const [submitted, expectedRevision] = vi.mocked(putReviewAttentionPolicies)
      .mock.calls[0]
    expect(expectedRevision).toBe(revision)
    expect(submitted.global["review.submitted"][0]).toMatchObject({
      agent_id: "page-two",
    })
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(3)
    for (const [request] of vi.mocked(getReviewAttentionAgents).mock.calls) {
      expect(request.expectedConfigRevision).toBe(revision)
    }
  })

  it("keeps only the active agent page cached and drops replaced or unselected prior-page options", async () => {
    const revision = "three-page-revision"
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      conflictSnapshot("Bounded agent pages", revision, '{"value":1}'),
    )
    vi.mocked(getReviewAttentionAgents).mockImplementation(async (request) => {
      if (request.cursor === "512") {
        return agentPage(revision, [{ id: "page-three", name: "Page Three" }])
      }
      if (request.cursor === "256") {
        return agentPage(
          revision,
          [{ id: "page-two", name: "Page Two" }],
          "512",
        )
      }
      return agentPage(
        revision,
        [
          { id: "page-one", name: "Page One" },
          { id: "reviewer", name: "Reviewer" },
        ],
        "256",
      )
    })
    const user = userEvent.setup()
    const { client } = renderPage()

    const currentSelector = () => screen.getByLabelText("AI agent")
    const initialSelector = await screen.findByLabelText("AI agent")
    expect(
      await within(initialSelector).findByRole("option", {
        name: "Page One (page-one)",
      }),
    ).toBeVisible()
    await expectOnlyAgentPageCached(client, revision, "first")

    await user.click(screen.getByRole("button", { name: "Next agents" }))
    expect(
      await within(currentSelector()).findByRole("option", {
        name: "Page Two (page-two)",
      }),
    ).toBeVisible()
    expect(
      within(currentSelector()).queryByRole("option", {
        name: "Page One (page-one)",
      }),
    ).toBeNull()
    await user.selectOptions(currentSelector(), "page-two")
    await expectOnlyAgentPageCached(client, revision, "256")

    await user.click(screen.getByRole("button", { name: "Next agents" }))
    expect(
      await within(currentSelector()).findByRole("option", {
        name: "Page Three (page-three)",
      }),
    ).toBeVisible()
    expect(
      within(currentSelector()).getByRole("option", {
        name: "page-two (page-two)",
      }),
    ).toBeVisible()
    await user.selectOptions(currentSelector(), "page-three")
    await waitFor(() =>
      expect(
        within(currentSelector()).queryByRole("option", {
          name: "page-two (page-two)",
        }),
      ).toBeNull(),
    )
    await expectOnlyAgentPageCached(client, revision, "512")
  })

  it("exposes each off-page selected identity only to its own gate", async () => {
    const revision = "isolated-options-revision"
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      snapshot(
        {
          global: {
            "review.submitted": [
              {
                id: "first_gate",
                kind: "ai_working_context",
                agent_id: "first-selected",
                criteria: "Inspect the working context.",
                title: "First gate",
              },
              {
                id: "second_gate",
                kind: "ai_isolated_context",
                agent_id: "second-selected",
                criteria: "Inspect an isolated context.",
                title: "Second gate",
              },
            ],
          },
          repositories: {},
        },
        revision,
        "catalog-isolated-options",
      ),
    )
    vi.mocked(getReviewAttentionAgents).mockResolvedValue(
      agentPage(revision, [{ id: "page-agent", name: "Page Agent" }]),
    )
    renderPage()

    const selectors = await screen.findAllByLabelText("AI agent")
    await waitFor(() => expect(selectors[0]).toBeEnabled())
    expect(
      within(selectors[0]).getByRole("option", {
        name: "first-selected (first-selected)",
      }),
    ).toBeVisible()
    expect(
      within(selectors[0]).queryByRole("option", {
        name: "second-selected (second-selected)",
      }),
    ).toBeNull()
    expect(
      within(selectors[1]).getByRole("option", {
        name: "second-selected (second-selected)",
      }),
    ).toBeVisible()
    expect(
      within(selectors[1]).queryByRole("option", {
        name: "first-selected (first-selected)",
      }),
    ).toBeNull()
  })

  it("keeps an off-page default agent usable when converting a gate to AI", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(
      replacementSnapshot(),
    )
    vi.mocked(getReviewAttentionAgents).mockResolvedValue({
      agents: [{ id: "main", name: "Main" }],
      default_agent_id: "off-page-default",
      config_revision: "config-revision-7",
    })
    const user = userEvent.setup()
    renderPage()

    expect(
      await screen.findByText("AI agent page 1 · 1 identities"),
    ).toBeVisible()
    await user.selectOptions(
      screen.getByLabelText("Gate type"),
      "ai_working_context",
    )

    const agentSelector = screen.getByLabelText("AI agent")
    expect(agentSelector).toHaveValue("off-page-default")
    expect(
      within(agentSelector).getByRole("option", {
        name: "off-page-default (off-page-default)",
      }),
    ).toBeVisible()
    await user.type(
      screen.getByLabelText("What AI should look for"),
      "Escalate only when repository-owner intent is required.",
    )
    expect(screen.getByRole("button", { name: "Save policies" })).toBeEnabled()
  })

  it("preserves the draft on a page-two 409 and reloads a clean first page at the new revision", async () => {
    const oldRevision = "stale-agent-page-revision"
    const newRevision = "fresh-agent-page-revision"
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(
        conflictSnapshot("Original title", oldRevision, '{"value":1}'),
      )
      .mockResolvedValueOnce(
        conflictSnapshot("Fresh title", newRevision, '{"value":2}'),
      )
    vi.mocked(getReviewAttentionAgents).mockImplementation(async (request) => {
      if (request.expectedConfigRevision === newRevision) {
        expect(request).not.toHaveProperty("cursor")
        return agentPage(newRevision, [
          { id: "main", name: "Main" },
          { id: "reviewer", name: "Reviewer" },
        ])
      }
      expect(request.expectedConfigRevision).toBe(oldRevision)
      if (request.cursor === "256") {
        throw new ReviewAttentionAgentsAPIError("config_revision_mismatch", 409)
      }
      expect(request).not.toHaveProperty("cursor")
      return agentPage(
        oldRevision,
        [
          { id: "main", name: "Main" },
          { id: "reviewer", name: "Reviewer" },
        ],
        "256",
      )
    })
    const user = userEvent.setup()
    const { client } = renderPage()

    expect(
      await screen.findByText("AI agent page 1 · 2 identities"),
    ).toBeVisible()
    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Keep this draft after the agent 409")
    await user.click(screen.getByRole("button", { name: "Next agents" }))
    expect(
      await screen.findByText(
        "The configuration changed while loading agents. Your draft is preserved; reload the latest policies.",
      ),
    ).toBeVisible()
    expect(title).toHaveValue("Keep this draft after the agent 409")
    expect(screen.getByRole("button", { name: "Save policies" })).toBeDisabled()
    expect(
      screen.queryByRole("button", { name: "Retry agents" }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole("button", { name: "Reload policies" }),
    ).toBeVisible()
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2)
    expect(getReviewAttentionPolicies).toHaveBeenCalledOnce()
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Reload policies" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", {
          name: "Discard this policy draft?",
        }),
      ).getByRole("button", { name: "Discard and reload" }),
    )

    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Fresh title",
      ),
    )
    expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2)
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(3)
    expect(
      vi.mocked(getReviewAttentionAgents).mock.calls.map(([request]) => ({
        revision: request.expectedConfigRevision,
        cursor: request.cursor,
      })),
    ).toEqual([
      { revision: oldRevision, cursor: undefined },
      { revision: oldRevision, cursor: "256" },
      { revision: newRevision, cursor: undefined },
    ])
    await expectOnlyAgentPageCached(client, newRevision, "first")
    expect(
      client
        .getQueryCache()
        .findAll({ queryKey: ["reviews", "attention-policy-agents"] })
        .some((query) => query.queryKey.includes(oldRevision)),
    ).toBe(false)
  })

  it("preserves a newer policy generation observed before a delayed save response", async () => {
    const initial = conflictSnapshot(
      "Initial title",
      "save-race-revision-a",
      '{"value":1}',
    )
    const newer = conflictSnapshot(
      "Generation C title",
      "save-race-revision-c",
      '{"value":3}',
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("save-race-revision-a"))
      .mockResolvedValueOnce(agentCatalog("save-race-revision-c"))
    let resolveNewerRead!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveNewerRead = resolve
        }),
      )
      .mockResolvedValue(newer)
    let resolveSave!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(putReviewAttentionPolicies).mockReturnValue(
      new Promise((resolve) => {
        resolveSave = resolve
      }),
    )
    const user = userEvent.setup()
    const { client } = renderPage()

    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Keep local title until C is loaded")
    await user.click(screen.getByRole("button", { name: "Save policies" }))
    await waitFor(() =>
      expect(putReviewAttentionPolicies).toHaveBeenCalledOnce(),
    )

    let newerRefetch!: Promise<void>
    act(() => {
      newerRefetch = client.refetchQueries({
        queryKey: ["reviews", "attention-policies"],
        exact: true,
      })
    })
    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2),
    )
    await act(async () => {
      resolveNewerRead(newer)
      await newerRefetch
    })

    const [savedCatalog] = vi.mocked(putReviewAttentionPolicies).mock.calls[0]
    await act(async () =>
      resolveSave({
        ...savedCatalog,
        catalog_revision: "save-race-catalog-b",
        config_revision: "save-race-revision-b",
        effects: { gateway_effect: "applied" },
      }),
    )

    expect(
      await screen.findByText(
        "The policies were saved, but a newer configuration generation was observed before the response completed. Your draft is preserved; reload the latest policies.",
      ),
    ).toBeVisible()
    expect(title).toHaveValue("Keep local title until C is loaded")
    expect(
      client.getQueryData<ReviewAttentionPolicySnapshot>([
        "reviews",
        "attention-policies",
      ]),
    ).toMatchObject({ config_revision: "save-race-revision-c" })
    expect(
      client.getQueryData<ReviewAttentionAgentCatalog>([
        "reviews",
        "attention-policy-agents",
        "save-race-revision-a",
        "first",
      ]),
    ).toMatchObject({ config_revision: "save-race-revision-a" })
    expect(showSaveSuccessOrRestartToast).toHaveBeenCalledOnce()
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Saving…" })).toBeNull(),
    )

    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", {
          name: "Discard this policy draft?",
        }),
      ).getByRole("button", { name: "Discard and reload" }),
    )
    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(3),
    )
    await waitFor(() =>
      expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2),
    )
    expect(
      client.getQueryData<ReviewAttentionPolicySnapshot>([
        "reviews",
        "attention-policies",
      ]),
    ).toMatchObject({ config_revision: "save-race-revision-c" })
    expect(
      client.getQueryData<ReviewAttentionAgentCatalog>([
        "reviews",
        "attention-policy-agents",
        "save-race-revision-c",
        "first",
      ]),
    ).toMatchObject({ config_revision: "save-race-revision-c" })
    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Generation C title",
      ),
    )
  })

  it("locks the complete editor while a confirmed destructive reload is pending", async () => {
    const initial = conflictSnapshot(
      "Initial title",
      "reload-revision-1",
      '{"value":1}',
    )
    const latest = conflictSnapshot(
      "Authoritative title",
      "reload-revision-2",
      '{"value":2}',
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("reload-revision-1"))
      .mockResolvedValueOnce(agentCatalog("reload-revision-2"))
    let resolveReload!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveReload = resolve
        }),
      )
    const user = userEvent.setup()
    renderPage()

    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Unsaved local title")
    await user.click(screen.getByRole("button", { name: "Reload" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", {
          name: "Discard this policy draft?",
        }),
      ).getByRole("button", { name: "Discard and reload" }),
    )

    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2),
    )
    expect(title).toBeDisabled()
    expect(screen.getByRole("button", { name: "Save policies" })).toBeDisabled()

    await act(async () => resolveReload(latest))
    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Authoritative title",
      ),
    )
    expect(screen.getByLabelText("Attention title")).toBeEnabled()
  })

  it("refreshes past an observed reload target when the config advances again", async () => {
    const initial = conflictSnapshot(
      "Generation A title",
      "converge-revision-a",
      '{"value":1}',
    )
    const observed = conflictSnapshot(
      "Generation B title",
      "converge-revision-b",
      '{"value":2}',
    )
    const latest = conflictSnapshot(
      "Generation C title",
      "converge-revision-c",
      '{"value":3}',
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("converge-revision-a"))
      .mockResolvedValueOnce(agentCatalog("converge-revision-c"))
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(latest)
    const user = userEvent.setup()
    const { client } = renderPage()

    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Keep this draft across B and C")
    act(() => {
      client.setQueryData(["reviews", "attention-policies"], observed)
    })
    expect(
      await screen.findByText(
        "A newer policy generation is available. Your draft has not been replaced.",
      ),
    ).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", {
          name: "Discard this policy draft?",
        }),
      ).getByRole("button", { name: "Discard and reload" }),
    )

    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2),
    )
    await waitFor(() =>
      expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2),
    )
    expect(
      client.getQueryData<ReviewAttentionPolicySnapshot>([
        "reviews",
        "attention-policies",
      ]),
    ).toMatchObject({ config_revision: "converge-revision-c" })
    expect(
      client.getQueryData<ReviewAttentionAgentCatalog>([
        "reviews",
        "attention-policy-agents",
        "converge-revision-c",
        "first",
      ]),
    ).toMatchObject({ config_revision: "converge-revision-c" })
    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Generation C title",
      ),
    )
    expect(screen.getByLabelText("Attention title")).not.toHaveValue(
      "Generation B title",
    )
  })

  it("preserves a dirty draft after a 409, never retries, and requires explicit reload", async () => {
    const initial = conflictSnapshot(
      "Owner input may be needed",
      "revision-1",
      '{"value":9007199254740993,"__proto__":{"safe":true}}',
    )
    const latest = conflictSnapshot(
      "Server-side title",
      "revision-2",
      '{"value":9007199254740995,"__proto__":{"safe":false}}',
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("revision-1"))
      .mockResolvedValueOnce(agentCatalog("revision-2"))
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockResolvedValue(latest)
    vi.mocked(putReviewAttentionPolicies).mockRejectedValueOnce(
      new ReviewAttentionPoliciesAPIError("config_revision_mismatch", 409),
    )
    const user = userEvent.setup()
    renderPage()

    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Keep my local direction")
    await user.click(screen.getByRole("button", { name: "Save policies" }))

    expect(
      await screen.findByText(
        "These policies changed elsewhere. Your draft is preserved; reload the latest version before saving.",
      ),
    ).toBeVisible()
    expect(screen.getByLabelText("Attention title")).toHaveValue(
      "Keep my local direction",
    )
    expect(screen.getByRole("button", { name: "Save policies" })).toBeDisabled()
    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2),
    )
    expect(putReviewAttentionPolicies).toHaveBeenCalledOnce()
    expect(refreshGatewayState).not.toHaveBeenCalled()
    expect(showSaveSuccessOrRestartToast).not.toHaveBeenCalled()

    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    const reloadDialog = screen.getByRole("alertdialog", {
      name: "Discard this policy draft?",
    })
    expect(reloadDialog).toBeVisible()
    await user.click(
      within(reloadDialog).getByRole("button", {
        name: "Discard and reload",
      }),
    )

    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Server-side title",
      ),
    )
    expect(screen.getByLabelText("Questions JSON (optional)")).toHaveValue(
      '{"value":9007199254740995,"__proto__":{"safe":false}}',
    )
    expect(
      screen.queryByText(
        "These policies changed elsewhere. Your draft is preserved; reload the latest version before saving.",
      ),
    ).not.toBeInTheDocument()
    expect(putReviewAttentionPolicies).toHaveBeenCalledOnce()
  })

  it("does not treat retained cache data as the latest generation when a conflict refetch fails", async () => {
    const initial = conflictSnapshot(
      "Initial server title",
      "failed-reload-revision-1",
      '{"value":1}',
    )
    const latest = conflictSnapshot(
      "Recovered server title",
      "failed-reload-revision-2",
      '{"value":2}',
    )
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog("failed-reload-revision-1"))
      .mockResolvedValueOnce(agentCatalog("failed-reload-revision-2"))
    let resolveRecovery!: (snapshot: ReviewAttentionPolicySnapshot) => void
    vi.mocked(getReviewAttentionPolicies)
      .mockResolvedValueOnce(initial)
      .mockRejectedValueOnce(new Error("latest generation unavailable"))
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveRecovery = resolve
        }),
      )
    vi.mocked(putReviewAttentionPolicies).mockRejectedValueOnce(
      new ReviewAttentionPoliciesAPIError("config_revision_mismatch", 409),
    )
    const user = userEvent.setup()
    renderPage()

    const title = await screen.findByLabelText("Attention title")
    await user.clear(title)
    await user.type(title, "Preserve this local title")
    await user.click(screen.getByRole("button", { name: "Save policies" }))

    expect(
      await screen.findByText(
        "These policies changed elsewhere, but the latest generation could not be loaded. Your draft is preserved; retry the reload before saving.",
      ),
    ).toBeVisible()
    expect(title).toHaveValue("Preserve this local title")
    expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(2)

    await user.click(screen.getByRole("button", { name: "Reload latest" }))
    await user.click(
      within(
        screen.getByRole("alertdialog", {
          name: "Discard this policy draft?",
        }),
      ).getByRole("button", { name: "Discard and reload" }),
    )
    await waitFor(() =>
      expect(getReviewAttentionPolicies).toHaveBeenCalledTimes(3),
    )
    expect(title).toBeDisabled()
    expect(title).toHaveValue("Preserve this local title")

    await act(async () => resolveRecovery(latest))
    await waitFor(() =>
      expect(screen.getByLabelText("Attention title")).toHaveValue(
        "Recovered server title",
      ),
    )
  })

  it("fails initial hydration closed and provides an explicit retry when the bounded agent catalog is malformed", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(mixedSnapshot())
    vi.mocked(getReviewAttentionAgents)
      .mockRejectedValueOnce(new Error("invalid_attention_agents_response"))
      .mockResolvedValueOnce(agentCatalog())
    const user = userEvent.setup()
    const onShowInbox = vi.fn()
    renderPage(onShowInbox)

    expect(
      await screen.findByText(
        "Review attention policies and configured agents could not be loaded as one trusted generation.",
      ),
    ).toBeVisible()
    expect(screen.queryByLabelText("AI agent")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Attention title")).not.toBeInTheDocument()
    await user.click(screen.getByRole("button", { name: "Review inbox" }))
    expect(onShowInbox).toHaveBeenCalledOnce()

    await user.click(screen.getByRole("button", { name: "Retry" }))

    const title = (await screen.findAllByLabelText("Attention title"))[0]
    expect(title).toHaveValue("Owner input may be needed")
    await user.clear(title)
    await user.type(title, "Edit only after trusted hydration")
    expect(screen.getAllByLabelText("AI agent")[0]).toBeEnabled()
    expect(title).toHaveValue("Edit only after trusted hydration")
    expect(screen.getByRole("button", { name: "Save policies" })).toBeEnabled()
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2)
  })

  it("never hydrates or offers agents from a delayed mismatched config generation", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(mixedSnapshot())
    let resolveOldAgents!: (catalog: ReviewAttentionAgentCatalog) => void
    vi.mocked(getReviewAttentionAgents).mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOldAgents = resolve
      }),
    )
    const user = userEvent.setup()
    renderPage()

    expect(
      await screen.findByText("Loading review attention policies…"),
    ).toBeVisible()
    expect(screen.queryByLabelText("Attention title")).not.toBeInTheDocument()
    await act(async () =>
      resolveOldAgents(agentCatalog("older-config-revision")),
    )

    expect(
      await screen.findByText(
        "Review attention policies and configured agents could not be loaded as one trusted generation.",
      ),
    ).toBeVisible()
    expect(screen.queryByLabelText("AI agent")).not.toBeInTheDocument()
    expect(screen.queryByLabelText("Attention title")).not.toBeInTheDocument()

    await user.click(screen.getByRole("button", { name: "Retry" }))
    const title = (await screen.findAllByLabelText("Attention title"))[0]
    expect(title).toHaveValue("Owner input may be needed")
    expect(screen.getAllByLabelText("AI agent")[0]).toBeEnabled()
    expect(getReviewAttentionAgents).toHaveBeenCalledTimes(2)
  })

  it("blocks saving when an agent refetch fails with retained catalog data", async () => {
    vi.mocked(getReviewAttentionPolicies).mockResolvedValue(mixedSnapshot())
    vi.mocked(getReviewAttentionAgents)
      .mockResolvedValueOnce(agentCatalog())
      .mockRejectedValueOnce(new Error("agent refresh unavailable"))
    const user = userEvent.setup()
    const { client } = renderPage()

    const title = await screen.findAllByLabelText("Attention title")
    await user.clear(title[0])
    await user.type(title[0], "Keep the draft after agent refresh failure")
    await act(async () => {
      await client.refetchQueries({
        queryKey: ["reviews", "attention-policy-agents"],
      })
    })

    expect(
      await screen.findByText(
        "Configured agents could not be loaded. AI gate validation and saving are paused; your policy draft is preserved.",
      ),
    ).toBeVisible()
    expect(title[0]).toHaveValue("Keep the draft after agent refresh failure")
    expect(screen.getByRole("button", { name: "Save policies" })).toBeDisabled()
    for (const selector of screen.getAllByLabelText("AI agent")) {
      expect(selector).toBeDisabled()
    }
    expect(putReviewAttentionPolicies).not.toHaveBeenCalled()
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

async function expectOnlyAgentPageCached(
  client: QueryClient,
  configRevision: string,
  cursor: string,
) {
  await waitFor(() =>
    expect(
      client
        .getQueryCache()
        .findAll({ queryKey: ["reviews", "attention-policy-agents"] })
        .map((query) => query.queryKey),
    ).toEqual([["reviews", "attention-policy-agents", configRevision, cursor]]),
  )
}

function agentPage(
  configRevision: string,
  agents: Array<{ id: string; name: string }>,
  nextCursor?: string,
): ReviewAttentionAgentCatalog {
  return {
    agents,
    default_agent_id: "main",
    config_revision: configRevision,
    ...(nextCursor === undefined ? {} : { next_cursor: nextCursor }),
  }
}

function agentCatalog(configRevision = "config-revision-1") {
  return {
    agents: [
      { id: "main", name: "Main" },
      { id: "reviewer", name: "Reviewer" },
    ],
    default_agent_id: "main",
    config_revision: configRevision,
  }
}

function mixedSnapshot(): ReviewAttentionPolicySnapshot {
  return snapshot({
    global: {
      "review.submitted": [
        {
          id: "ask_owner",
          kind: "ai_working_context",
          agent_id: "main",
          criteria: "Ask the owner only when repository intent is required.",
          title: "Owner input may be needed",
          questions: parseExactJSON('{"priority":9007199254740993}'),
        },
        {
          id: "independent_check",
          kind: "ai_isolated_context",
          agent_id: "reviewer",
          criteria: "Independently assess the submitted findings.",
          title: "Independent review",
        },
        {
          id: "blocking_check",
          kind: "deterministic",
          when: "inputs.review.blocking == true",
          title: "Blocking finding",
          questions: parseExactJSON('[{"id":"resolution"}]'),
        },
        { id: "no_attention", kind: "zero" },
      ],
    },
    repositories: {
      "octo/repo": {
        "review.disabled": { mode: "disable", gates: [] },
        "review.inherited": { mode: "inherit", gates: [] },
        "review.overlay": {
          mode: "overlay",
          gates: [{ id: "overlay_noop", kind: "zero" }],
        },
        "review.replaced": {
          mode: "replace",
          gates: [{ id: "replacement_noop", kind: "zero" }],
        },
      },
    },
  })
}

function replacementSnapshot(): ReviewAttentionPolicySnapshot {
  return snapshot(
    {
      global: {
        "review.submitted": [
          {
            id: "deterministic_gate",
            kind: "deterministic",
            when: "inputs.review.blocking == true",
            title: "Blocking review",
            questions: parseExactJSON(exactQuestionsSource),
          },
        ],
      },
      repositories: {
        "octo/keep": {
          "review.submitted": { mode: "inherit", gates: [] },
        },
      },
    },
    "config-revision-7",
    "catalog-revision-7",
  )
}

function conflictSnapshot(
  title: string,
  revision: string,
  questionsSource: string,
): ReviewAttentionPolicySnapshot {
  return snapshot(
    {
      global: {
        "review.submitted": [
          {
            id: "ask_owner",
            kind: "ai_isolated_context",
            agent_id: "reviewer",
            criteria: "Ask when the review needs repository-owner context.",
            title,
            questions: parseExactJSON(questionsSource),
          },
        ],
      },
      repositories: {},
    },
    revision,
    `catalog-${revision}`,
  )
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
