import type {
  AgentInfo,
  AgentModelPolicy,
  AgentMutationInput,
} from "@/api/agents"

export type PrimaryPolicyMode = "inherit" | "custom"
export type FallbackPolicyMode = "inherit" | "none" | "custom"
export type SkillsPolicyMode = "all" | "selected"
export type DelegationPolicyMode = "none" | "all" | "selected"

export interface AgentDraft {
  id: string
  name: string
  workspace: string
  accountRef: string
  modelConfigured: boolean
  primaryMode: PrimaryPolicyMode
  primary: string
  fallbackMode: FallbackPolicyMode
  fallbacks: string[]
  fallbackInput: string
  skillsMode: SkillsPolicyMode
  skills: string[]
  skillsInput: string
  delegationMode: DelegationPolicyMode
  delegateAgentIDs: string[]
  delegateAgentInput: string
}

export interface AgentDraftErrors {
  id?: string
  primary?: string
  fallbacks?: string
  skills?: string
  delegation?: string
}

export const canonicalAgentIDPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

export function emptyAgentDraft(): AgentDraft {
  return {
    id: "",
    name: "",
    workspace: "",
    accountRef: "",
    modelConfigured: false,
    primaryMode: "inherit",
    primary: "",
    fallbackMode: "inherit",
    fallbacks: [],
    fallbackInput: "",
    skillsMode: "all",
    skills: [],
    skillsInput: "",
    delegationMode: "none",
    delegateAgentIDs: [],
    delegateAgentInput: "",
  }
}

export function agentDraftFromInfo(agent: AgentInfo): AgentDraft {
  return {
    id: agent.id,
    name: agent.name,
    workspace: agent.workspace,
    accountRef: agent.account_ref ?? "",
    modelConfigured: agent.model != null,
    primaryMode:
      agent.model != null && agent.model.primary !== "" ? "custom" : "inherit",
    primary: agent.model?.primary ?? "",
    fallbackMode: fallbackMode(agent.model),
    fallbacks: agent.model?.fallbacks ?? [],
    fallbackInput: "",
    skillsMode:
      agent.skills != null && agent.skills.length > 0 ? "selected" : "all",
    skills: agent.skills ?? [],
    skillsInput: "",
    delegationMode: delegationMode(agent.subagents?.allow_agents),
    delegateAgentIDs:
      agent.subagents?.allow_agents.filter((id) => id !== "*") ?? [],
    delegateAgentInput: "",
  }
}

function fallbackMode(model: AgentModelPolicy | null): FallbackPolicyMode {
  if (model?.fallbacks == null) return "inherit"
  if (model.fallbacks.length === 0) return "none"
  return "custom"
}

function delegationMode(
  allowAgents: string[] | undefined,
): DelegationPolicyMode {
  if (allowAgents == null || allowAgents.length === 0) return "none"
  if (allowAgents.length === 1 && allowAgents[0] === "*") return "all"
  return "selected"
}

export function validateAgentDraft(
  draft: AgentDraft,
  existingAgentIDs: string[],
  originalID?: string,
  retainedUnknownAgentIDs: string[] = [],
): AgentDraftErrors {
  const errors: AgentDraftErrors = {}
  const id = draft.id

  if (!canonicalAgentIDPattern.test(id)) {
    errors.id =
      "Use 1–64 lowercase letters, numbers, underscores, or hyphens, starting with a letter or number."
  } else if (id !== originalID && existingAgentIDs.includes(id)) {
    errors.id = "An agent with this ID already exists."
  }

  if (draft.primaryMode === "custom" && draft.primary.trim() === "") {
    errors.primary = "Choose a primary model alias or Inherit."
  }
  if (
    draft.fallbackMode === "custom" &&
    tokensWithPending(draft.fallbacks, draft.fallbackInput).length === 0
  ) {
    errors.fallbacks =
      "Add at least one fallback model alias or choose Inherit or None."
  }
  if (
    draft.skillsMode === "selected" &&
    tokensWithPending(draft.skills, draft.skillsInput).length === 0
  ) {
    errors.skills = "Add at least one skill or choose All skills."
  }
  if (draft.delegationMode === "selected") {
    const targets = tokensWithPending(
      draft.delegateAgentIDs,
      draft.delegateAgentInput,
    )
    if (targets.length === 0) {
      errors.delegation =
        "Add at least one agent or choose No delegation or All peers."
    } else if (
      targets.some(
        (target) =>
          target === id ||
          target === "*" ||
          !canonicalAgentIDPattern.test(target),
      )
    ) {
      errors.delegation =
        "Delegation targets must be canonical agent IDs and cannot include this agent."
    } else if (
      targets.some(
        (target) =>
          !existingAgentIDs.includes(target) &&
          !retainedUnknownAgentIDs.includes(target),
      )
    ) {
      errors.delegation =
        "Choose an existing agent. Unknown IDs already in this policy may be retained or removed."
    }
  }

  return errors
}

export function agentInputFromDraft(draft: AgentDraft): AgentMutationInput {
  const id = draft.id
  const primary = draft.primaryMode === "custom" ? draft.primary.trim() : ""
  const fallbacks =
    draft.fallbackMode === "inherit"
      ? null
      : draft.fallbackMode === "none"
        ? []
        : tokensWithPending(draft.fallbacks, draft.fallbackInput)
  const model =
    !draft.modelConfigured &&
    draft.primaryMode === "inherit" &&
    draft.fallbackMode === "inherit"
      ? null
      : { primary, fallbacks }
  const skills =
    draft.skillsMode === "all"
      ? null
      : tokensWithPending(draft.skills, draft.skillsInput)
  const subagents =
    draft.delegationMode === "none"
      ? null
      : {
          allow_agents:
            draft.delegationMode === "all"
              ? ["*"]
              : tokensWithPending(
                  draft.delegateAgentIDs,
                  draft.delegateAgentInput,
                ),
        }

  return {
    id,
    name: draft.name.trim(),
    workspace: draft.workspace.trim(),
    account_ref: draft.accountRef.trim(),
    model,
    skills,
    subagents,
  }
}

export function normalizeTokens(values: string[]): string[] {
  const seen = new Set<string>()
  const normalized: string[] = []
  for (const value of values) {
    const token = value.trim()
    if (token === "" || seen.has(token)) continue
    seen.add(token)
    normalized.push(token)
  }
  return normalized
}

function tokensWithPending(values: string[], pending: string): string[] {
  return normalizeTokens([...values, pending])
}
