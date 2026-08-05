import { describe, expect, it } from "vitest"

import {
  type ExactJSONObject,
  parseExactJSON,
  stringifyExactJSON,
} from "@/api/review-attention-json"
import type {
  ReviewAttentionGate,
  ReviewAttentionPolicyCatalog,
} from "@/api/review-attention-policies"

import {
  type ReviewAttentionGateDraft,
  type ReviewAttentionPolicyDraft,
  convertReviewAttentionGateKind,
  createReviewAttentionEditorKeyFactory,
  createReviewAttentionGateDraft,
  createReviewAttentionGlobalPolicyDraft,
  createReviewAttentionRepositoryDraft,
  createReviewAttentionRepositoryPolicyDraft,
  reorderReviewAttentionGates,
  resolveReviewAttentionPolicy,
  reviewAttentionConditionError,
  reviewAttentionPolicyDraftFromCatalog,
  validateReviewAttentionPolicyDraft,
} from "./review-attention-policy-model"

const agents = new Set(["main", "reviewer"])

function aiGate(
  editorKey: string,
  id: string,
  kind: "ai_working_context" | "ai_isolated_context" = "ai_working_context",
  agentID = "main",
): ReviewAttentionGateDraft {
  return {
    editorKey,
    id,
    kind,
    agentID,
    criteria: "Ask when product direction is required.",
    title: "Discuss product direction",
    questionsSource: '{"issue":9007199254740993,"Foo":1,"foo":2}',
  }
}

function deterministicGate(
  editorKey: string,
  id: string,
): ReviewAttentionGateDraft {
  return {
    editorKey,
    id,
    kind: "deterministic",
    when: "inputs.requires_attention == true",
    title: "Confirm policy",
    questionsSource: '["Continue?",null]',
  }
}

function validDraft(): ReviewAttentionPolicyDraft {
  return {
    global: [
      {
        editorKey: "global-1",
        decisionPoint: "review.submitted",
        gates: [
          aiGate("gate-1", "working"),
          aiGate("gate-2", "isolated", "ai_isolated_context", "reviewer"),
          deterministicGate("gate-3", "confirm"),
          { editorKey: "gate-4", id: "off", kind: "zero" },
        ],
      },
      {
        editorKey: "global-2",
        decisionPoint: "review.empty",
        gates: [],
      },
    ],
    repositories: [
      {
        editorKey: "repository-1",
        repository: "Acme/Widgets",
        policies: [
          {
            editorKey: "repository-policy-1",
            decisionPoint: "review.submitted",
            mode: "overlay",
            gates: [
              { editorKey: "gate-5", id: "isolated", kind: "zero" },
              deterministicGate("gate-6", "repository_rule"),
            ],
          },
          {
            editorKey: "repository-policy-2",
            decisionPoint: "review.ready",
            mode: "disable",
            gates: [],
          },
        ],
      },
      {
        editorKey: "repository-2",
        repository: "Empty/Policies",
        policies: [],
      },
    ],
  }
}

function issueCodes(draft: ReviewAttentionPolicyDraft) {
  return new Set(
    validateReviewAttentionPolicyDraft(draft, agents).issues.map(
      (issue) => issue.code,
    ),
  )
}

describe("review attention policy draft model", () => {
  it("creates deterministic stable editor keys and kind-specific defaults", () => {
    const nextKey = createReviewAttentionEditorKeyFactory(7)
    expect(createReviewAttentionGlobalPolicyDraft(nextKey)).toEqual({
      editorKey: "global-policy-8",
      decisionPoint: "",
      gates: [],
    })
    expect(createReviewAttentionRepositoryDraft(nextKey)).toEqual({
      editorKey: "repository-9",
      repository: "",
      policies: [],
    })
    expect(createReviewAttentionRepositoryPolicyDraft(nextKey)).toEqual({
      editorKey: "repository-policy-10",
      decisionPoint: "",
      mode: "inherit",
      gates: [],
    })
    expect(
      createReviewAttentionGateDraft("deterministic", "main", nextKey),
    ).toEqual({
      editorKey: "gate-11",
      id: "",
      kind: "deterministic",
      when: "true",
      title: "",
      questionsSource: "[]",
    })
  })

  it("converts gate kinds without retaining forbidden fields and reorders immutably", () => {
    const source = aiGate("stable-key", "ask")
    if (source.kind !== "ai_working_context") throw new Error("fixture")
    const deterministic = convertReviewAttentionGateKind(
      source,
      "deterministic",
      "reviewer",
    )
    expect(deterministic).toEqual({
      editorKey: "stable-key",
      id: "ask",
      kind: "deterministic",
      when: "true",
      title: source.title,
      questionsSource: source.questionsSource,
    })
    expect(
      convertReviewAttentionGateKind(deterministic, "zero", "main"),
    ).toEqual({ editorKey: "stable-key", id: "ask", kind: "zero" })

    const gates = [
      source,
      deterministicGate("two", "second"),
      { editorKey: "three", id: "third", kind: "zero" } as const,
    ]
    const reordered = reorderReviewAttentionGates(gates, 0, 2)
    expect(reordered.map((gate) => gate.id)).toEqual(["second", "third", "ask"])
    expect(gates.map((gate) => gate.id)).toEqual(["ask", "second", "third"])
    expect(reorderReviewAttentionGates(gates, -1, 2)).not.toBe(gates)
    expect(reorderReviewAttentionGates(gates, -1, 2)).toEqual(gates)
  })

  it("round-trips catalogs into keyed drafts without rounding question numbers", () => {
    const catalog: ReviewAttentionPolicyCatalog = {
      global: {
        "review.submitted": [
          {
            id: "exact",
            kind: "deterministic",
            when: "true",
            title: "Exact",
            questions: parseExactJSON(
              '{"large":9007199254740993,"Foo":1,"foo":2}',
            ),
          },
        ],
        "review.empty": [],
      },
      repositories: { "Empty/Policies": {} },
    }
    const draft = reviewAttentionPolicyDraftFromCatalog(catalog)
    const keys = [
      ...draft.global.map((policy) => policy.editorKey),
      ...draft.global.flatMap((policy) =>
        policy.gates.map((gate) => gate.editorKey),
      ),
      ...draft.repositories.map((repository) => repository.editorKey),
    ]
    expect(new Set(keys).size).toBe(keys.length)
    const validation = validateReviewAttentionPolicyDraft(draft, agents)
    expect(validation.issues).toEqual([])
    expect(validation.catalog).toBeDefined()
    const questions =
      validation.catalog?.global["review.submitted"][0].questions
    expect(stringifyExactJSON(questions!)).toBe(
      '{"large":9007199254740993,"Foo":1,"foo":2}',
    )
    expect(validation.catalog?.global["review.empty"]).toEqual([])
    expect(validation.catalog?.repositories["Empty/Policies"]).toEqual(
      Object.create(null),
    )
  })

  it("compiles a complete mixed catalog and reports deterministic metrics", () => {
    const validation = validateReviewAttentionPolicyDraft(validDraft(), agents)
    expect(validation.valid).toBe(true)
    expect(validation.issues).toEqual([])
    expect(validation.metrics).toMatchObject({
      repositories: 2,
      policies: 4,
      gates: 6,
    })
    expect(validation.metrics.canonicalBytes).toBeGreaterThan(0)
    expect(validation.metrics.requestBytes).toBeGreaterThan(0)
    expect(
      validation.catalog?.repositories["Acme/Widgets"]["review.submitted"],
    ).toMatchObject({ mode: "overlay" })

    const reordered = validDraft()
    reordered.global.reverse()
    reordered.repositories.reverse()
    const reorderedValidation = validateReviewAttentionPolicyDraft(
      reordered,
      agents,
    )
    expect(reorderedValidation.metrics.canonicalBytes).toBe(
      validation.metrics.canonicalBytes,
    )
    expect(reorderedValidation.metrics.requestBytes).toBe(
      validation.metrics.requestBytes,
    )
  })

  it("returns stable field issues for collection, mode, gate, and agent failures", () => {
    const draft = validDraft()
    draft.global.push({
      editorKey: "duplicate-global",
      decisionPoint: "review.submitted",
      gates: [],
    })
    draft.repositories.push({
      editorKey: "repository-collision",
      repository: "acme/widgets",
      policies: [],
    })
    draft.repositories[0].policies[0].gates = []
    const working = draft.global[0].gates[0]
    if (working.kind === "ai_working_context") working.agentID = "missing"
    draft.global[0].gates.push(
      aiGate("conflict", "other", "ai_working_context", "reviewer"),
    )
    draft.global[0].gates.push({
      ...draft.global[0].gates[3],
      editorKey: "duplicate",
      id: "other",
    })
    const deterministic = draft.global[0].gates[2]
    if (deterministic.kind === "deterministic")
      deterministic.questionsSource = "null"

    const codes = issueCodes(draft)
    expect(codes).toEqual(
      expect.objectContaining(
        new Set([
          "decision_point.duplicate",
          "repository.case_collision",
          "mode.gates_required",
          "gate.agent_unavailable",
          "gate.working_agent_conflict",
          "gate.id_duplicate",
          "gate.questions_required",
        ]),
      ),
    )
    const result = validateReviewAttentionPolicyDraft(draft, agents)
    expect(result.valid).toBe(false)
    expect(result.catalog).toBeUndefined()
    expect(result.issues).toEqual(
      [...result.issues].sort(
        (left, right) =>
          left.path.localeCompare(right.path) ||
          left.code.localeCompare(right.code),
      ),
    )
  })

  it("checks exact JSON, Unicode scalars, and encoded question bounds", () => {
    const invalid = validDraft()
    const working = invalid.global[0].gates[0]
    if (working.kind !== "ai_working_context") throw new Error("fixture")
    working.title = "bad\ud800"
    working.questionsSource = '{"same":1,"same":2}'
    let codes = issueCodes(invalid)
    expect(codes.has("gate.text_invalid")).toBe(true)
    expect(codes.has("gate.questions_invalid")).toBe(true)

    working.title = "Valid"
    working.questionsSource = JSON.stringify("x".repeat((128 << 10) + 1))
    codes = issueCodes(invalid)
    expect(codes.has("gate.questions_invalid")).toBe(true)

    working.questionsSource = JSON.stringify(
      "<".repeat(Math.floor((128 << 10) / 6) + 1),
    )
    codes = issueCodes(invalid)
    expect(codes.has("gate.questions_invalid")).toBe(true)

    working.questionsSource = `${" ".repeat(128 << 10)}{"ok":true}`
    expect(issueCodes(invalid).has("gate.questions_invalid")).toBe(false)

    working.questionsSource = null
    working.title = "\u0085"
    expect(issueCodes(invalid).has("gate.text_invalid")).toBe(true)
    working.title = "\ufeff"
    expect(issueCodes(invalid).has("gate.text_invalid")).toBe(false)
  })
})

describe("review attention deterministic condition validation", () => {
  it.each([
    "true",
    "not inputs.requires_attention",
    "inputs.score >= -1.5e2",
    "${{ inputs.kind == 'security' }}",
    "1e-10000",
    "\u00851\u0085",
    "-1.7976931348623158e308",
    "1_23.50_0_0e+1_2",
    "0x1.fp+2",
    "0x_1p2",
    "0x_1_2.3_4_5p+1_2",
    "0x1p-10000",
    "0x1.fffffffffffff7fffp1023",
    "-0x1.fffffffffffff7fffp1023",
    "0x0p+18446744073709551616",
    "0x1p-18446744073709551616",
    `2.${"2".repeat(4000)}e+1`,
    "1.7976931348623158e308",
    "NaN",
    "nan",
    "inf",
    "-infinity",
    "+INF",
  ])("accepts backend-compatible condition %s", (condition) => {
    expect(reviewAttentionConditionError(condition)).toBeNull()
  })

  it.each([
    ["", "nonblank"],
    ["${{ inputs.ready", "delimiters"],
    ["secrets.token", "inputs root"],
    ["inputs.ready&&true", "Unsupported"],
    ["(inputs.ready)", "Unsupported"],
    ["1_e2", "Unsupported"],
    ["\ufeff1\ufeff", "Unsupported"],
    ["1__2", "Unsupported"],
    ["1._2", "Unsupported"],
    ["1e_2", "Unsupported"],
    ["0x__1p2", "Unsupported"],
    ["0x_1_p2", "Unsupported"],
    ["0x_.1p2", "Unsupported"],
    ["0x1", "Unsupported"],
    ["+nan", "Unsupported"],
    ["-NaN", "Unsupported"],
    ["1.7976931348623159e308", "Unsupported"],
    ["-1.797693134862315808e308", "Unsupported"],
    ["0x1.fffffffffffff8p1023", "Unsupported"],
    ["-0x1.fffffffffffff8p1023", "Unsupported"],
    ["0x1p+18446744073709551616", "Unsupported"],
    ["1".repeat(4097), "4096"],
  ])("rejects %s", (condition, fragment) => {
    expect(reviewAttentionConditionError(condition)).toContain(fragment)
  })
})

describe("review attention effective policy resolution", () => {
  const globalGates: ReviewAttentionGate[] = [
    {
      id: "ask",
      kind: "ai_isolated_context",
      agent_id: "main",
      criteria: "Ask",
      title: "Ask user",
      questions: parseExactJSON('{"prompt":"Choose"}'),
    },
    { id: "identity", kind: "zero" },
  ]

  function catalog(
    mode: "inherit" | "overlay" | "replace" | "disable",
    gates: ReviewAttentionGate[] = [],
  ): ReviewAttentionPolicyCatalog {
    return {
      global: { "review.ready": globalGates },
      repositories: {
        "Acme/Widgets": {
          "review.ready": { mode, gates },
        },
      },
    }
  }

  it("resolves absent and explicit inheritance", () => {
    const inherited = resolveReviewAttentionPolicy(
      { global: { "review.ready": globalGates }, repositories: {} },
      "unknown/repo",
      "review.ready",
    )
    expect(inherited).toMatchObject({
      mode: "inherit",
      overrideConfigured: false,
      noop: false,
    })
    expect(inherited.entries.map((entry) => entry.action)).toEqual([
      "inherited",
      "inherited",
    ])

    const explicit = resolveReviewAttentionPolicy(
      catalog("inherit"),
      "acme/widgets",
      "review.ready",
    )
    expect(explicit.overrideConfigured).toBe(true)
    expect(explicit.mode).toBe("inherit")
  })

  it("overlays stable slots, records a zero tombstone, and appends new IDs", () => {
    const result = resolveReviewAttentionPolicy(
      catalog("overlay", [
        { id: "ask", kind: "zero" },
        {
          id: "confirm",
          kind: "deterministic",
          when: "true",
          title: "Confirm",
          questions: parseExactJSON('["Continue?"]'),
        },
      ]),
      "acme/widgets",
      "review.ready",
    )
    expect(result.entries.map(({ id, action }) => ({ id, action }))).toEqual([
      { id: "ask", action: "tombstoned" },
      { id: "identity", action: "inherited" },
      { id: "confirm", action: "appended" },
    ])
    expect(result.entries[0]).toMatchObject({
      globalPosition: 1,
      repositoryPosition: 1,
      effectivePosition: 1,
    })
  })

  it("supports replace/disable and returns detached question values", () => {
    const replacement: ReviewAttentionGate = {
      id: "replacement",
      kind: "ai_isolated_context",
      agent_id: "main",
      criteria: "Ask",
      title: "Replacement",
      questions: parseExactJSON('{"prompt":"Original"}'),
    }
    const replaced = resolveReviewAttentionPolicy(
      catalog("replace", [replacement]),
      "ACME/WIDGETS",
      "review.ready",
    )
    expect(replaced.entries[0].action).toBe("selected")
    expect(replaced.entries[0].effectivePosition).toBe(1)
    const questions = replaced.effective[0].questions as ExactJSONObject
    questions.prompt = "Changed"
    expect(stringifyExactJSON(replacement.questions!)).toBe(
      '{"prompt":"Original"}',
    )

    expect(
      resolveReviewAttentionPolicy(
        catalog("disable"),
        "Acme/Widgets",
        "review.ready",
      ),
    ).toMatchObject({ mode: "disable", entries: [], effective: [], noop: true })
  })

  it("flags invalid effective overlays without changing valid source layers", () => {
    const draft: ReviewAttentionPolicyDraft = {
      global: [
        {
          editorKey: "global",
          decisionPoint: "review.ready",
          gates: [
            aiGate("global-working", "global", "ai_working_context", "main"),
          ],
        },
      ],
      repositories: [
        {
          editorKey: "repository",
          repository: "Acme/Widgets",
          policies: [
            {
              editorKey: "policy",
              decisionPoint: "review.ready",
              mode: "overlay",
              gates: [
                aiGate(
                  "local-working",
                  "local",
                  "ai_working_context",
                  "reviewer",
                ),
              ],
            },
          ],
        },
      ],
    }
    expect(issueCodes(draft).has("effective.working_agent_conflict")).toBe(true)

    draft.global[0].gates = Array.from({ length: 64 }, (_, index) => ({
      editorKey: `zero-${index}`,
      id: `g${index.toString().padStart(2, "0")}`,
      kind: "zero" as const,
    }))
    draft.repositories[0].policies[0].gates = [
      { editorKey: "new", id: "new", kind: "zero" },
    ]
    const codes = issueCodes(draft)
    expect(codes.has("effective.gate_limit")).toBe(true)
  })
})
