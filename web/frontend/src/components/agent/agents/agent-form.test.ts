import { describe, expect, it } from "vitest"

import type { AgentInfo } from "@/api/agents"

import {
  agentDraftFromInfo,
  agentInputFromDraft,
  emptyAgentDraft,
  validateAgentDraft,
} from "./agent-form"

describe("agent form policy mapping", () => {
  it("preserves inherited model policy separately from explicit no fallbacks", () => {
    const inherited = agentDraftFromInfo(agent({ model: null }))
    const none = agentDraftFromInfo(
      agent({ model: { primary: "", fallbacks: [] } }),
    )

    expect(inherited.fallbackMode).toBe("inherit")
    expect(none.fallbackMode).toBe("none")
    expect(agentInputFromDraft(inherited).model).toBeNull()
    expect(agentInputFromDraft(none).model).toEqual({
      primary: "",
      fallbacks: [],
    })
  })

  it("preserves an explicit all-inherit model object until reset is requested", () => {
    const explicit = agentDraftFromInfo(
      agent({ model: { primary: "", fallbacks: null } }),
    )

    expect(explicit.modelConfigured).toBe(true)
    expect(agentInputFromDraft(explicit).model).toEqual({
      primary: "",
      fallbacks: null,
    })

    explicit.modelConfigured = false
    expect(agentInputFromDraft(explicit).model).toBeNull()
  })

  it("keeps custom fallback order and maps all skills to null", () => {
    const draft = emptyAgentDraft()
    draft.id = "reviewer"
    draft.fallbackMode = "custom"
    draft.fallbacks = ["model-c", "model-a", "model-b"]
    draft.skillsMode = "all"
    draft.skills = ["stale-value"]

    expect(agentInputFromDraft(draft)).toMatchObject({
      model: {
        primary: "",
        fallbacks: ["model-c", "model-a", "model-b"],
      },
      skills: null,
    })
  })

  it("retains unknown delegation IDs while excluding wildcard and self modes", () => {
    const draft = agentDraftFromInfo(
      agent({
        id: "reviewer",
        subagents: { allow_agents: ["legacy-agent", "main"] },
      }),
    )

    expect(draft.delegationMode).toBe("selected")
    expect(draft.delegateAgentIDs).toEqual(["legacy-agent", "main"])
    expect(agentInputFromDraft(draft).subagents).toEqual({
      allow_agents: ["legacy-agent", "main"],
    })
    expect(
      validateAgentDraft(draft, ["reviewer", "main"], "reviewer", [
        "legacy-agent",
        "main",
      ]).delegation,
    ).toBeUndefined()

    draft.delegateAgentInput = "missing-agent"
    expect(
      validateAgentDraft(draft, ["reviewer", "main"], "reviewer", [
        "legacy-agent",
        "main",
      ]).delegation,
    ).toMatch(/existing agent/)
  })

  it("validates canonical unique IDs and nonempty selected policies", () => {
    const draft = emptyAgentDraft()
    draft.id = "Review Agent"
    draft.skillsMode = "selected"
    draft.delegationMode = "selected"
    draft.fallbackMode = "custom"

    expect(validateAgentDraft(draft, ["main"])).toEqual({
      id: expect.any(String),
      fallbacks: expect.any(String),
      skills: expect.any(String),
      delegation: expect.any(String),
    })

    draft.id = "main"
    expect(validateAgentDraft(draft, ["main"]).id).toMatch(/already exists/)
  })

  it("rejects normalized IDs and submits typed list entries that were not added", () => {
    const draft = emptyAgentDraft()
    draft.id = " reviewer "
    expect(validateAgentDraft(draft, []).id).toMatch(/lowercase/)

    draft.id = "reviewer"
    draft.fallbackMode = "custom"
    draft.fallbackInput = "model-b"
    draft.skillsMode = "selected"
    draft.skillsInput = "review-helper"
    draft.delegationMode = "selected"
    draft.delegateAgentInput = "main"

    expect(agentInputFromDraft(draft)).toMatchObject({
      id: "reviewer",
      model: { fallbacks: ["model-b"] },
      skills: ["review-helper"],
      subagents: { allow_agents: ["main"] },
    })
  })
})

function agent(overrides: Partial<AgentInfo> = {}): AgentInfo {
  return {
    id: "main",
    name: "",
    workspace: "",
    model: null,
    skills: null,
    subagents: null,
    is_default: true,
    default_configured: true,
    implicit: false,
    ...overrides,
  }
}
