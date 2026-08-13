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
  createReviewAttentionRepositoryAssignmentDraft,
  createReviewAttentionRuleDraft,
  createReviewAttentionRuleSetDraft,
  duplicateReviewAttentionRuleSetDraft,
  isReviewAttentionPolicyCatalogSemanticallyValid,
  reorderReviewAttentionGates,
  resolveReviewAttentionPolicy,
  resolveReviewAttentionRuleSet,
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
    ruleSets: [
      {
        editorKey: "set-default",
        id: "default",
        name: "Default",
        rules: [
          {
            editorKey: "rule-default",
            decisionPoint: "pr_development.before_push",
            gates: [
              aiGate("gate-1", "working"),
              aiGate("gate-2", "isolated", "ai_isolated_context", "reviewer"),
              deterministicGate("gate-3", "confirm"),
              { editorKey: "gate-4", id: "off", kind: "zero" },
            ],
          },
          {
            editorKey: "rule-empty",
            decisionPoint: "review.empty",
            gates: [],
          },
        ],
      },
      {
        editorKey: "set-release",
        id: "release",
        name: "Release checks",
        rules: [
          {
            editorKey: "rule-release",
            decisionPoint: "review.ready",
            gates: [deterministicGate("gate-5", "approve_release")],
          },
        ],
      },
      {
        editorKey: "set-quiet",
        id: "quiet",
        name: "Quiet",
        rules: [],
      },
    ],
    defaultRuleSetID: "release",
    repositoryAssignments: [
      {
        editorKey: "assignment-1",
        repository: "Acme/Widgets",
        ruleSetID: "default",
      },
      {
        editorKey: "assignment-2",
        repository: "Empty/Policies",
        ruleSetID: "quiet",
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

describe("review attention reusable-set draft model", () => {
  it("creates deterministic keys and kind-specific starter drafts", () => {
    const nextKey = createReviewAttentionEditorKeyFactory(7)
    expect(createReviewAttentionRuleSetDraft(nextKey, "team", "Team")).toEqual({
      editorKey: "rule-set-8",
      id: "team",
      name: "Team",
      rules: [],
    })
    expect(createReviewAttentionRuleDraft(nextKey)).toEqual({
      editorKey: "rule-9",
      decisionPoint: "",
      gates: [],
    })
    expect(createReviewAttentionRepositoryAssignmentDraft(nextKey)).toEqual({
      editorKey: "repository-assignment-10",
      repository: "",
      ruleSetID: "",
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

  it("duplicates content under a new immutable identity and regenerates every editor key", () => {
    const source = validDraft().ruleSets[0]
    const nextKey = createReviewAttentionEditorKeyFactory(20)
    const copy = duplicateReviewAttentionRuleSetDraft(
      source,
      "default_copy",
      "Default copy",
      nextKey,
    )

    expect(copy).toMatchObject({
      editorKey: "rule-set-21",
      id: "default_copy",
      name: "Default copy",
    })
    expect(copy.rules.map((rule) => rule.editorKey)).toEqual([
      "rule-22",
      "rule-27",
    ])
    expect(copy.rules[0].gates.map((gate) => gate.editorKey)).toEqual([
      "gate-23",
      "gate-24",
      "gate-25",
      "gate-26",
    ])
    expect(copy.rules[0].gates.map((gate) => gate.id)).toEqual(
      source.rules[0].gates.map((gate) => gate.id),
    )
    expect(copy.rules[0].gates[0]).not.toBe(source.rules[0].gates[0])
    expect(copy.rules[0].gates[0]).toMatchObject({
      questionsSource: '{"issue":9007199254740993,"Foo":1,"foo":2}',
    })
    expect(copy).not.toHaveProperty("defaultRuleSetID")
    expect(copy).not.toHaveProperty("repositoryAssignments")
    expect(source.editorKey).toBe("set-default")
  })

  it("converts gate kinds without forbidden fields and reorders immutably", () => {
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

  it("round-trips normalized catalogs without rounding question numbers", () => {
    const catalog: ReviewAttentionPolicyCatalog = {
      rule_sets: {
        default: {
          name: "Default",
          rules: {
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
        },
        quiet: { name: "Quiet", rules: {} },
      },
      default_rule_set_id: "quiet",
      repository_assignments: { "Empty/Policies": "default" },
    }
    const draft = reviewAttentionPolicyDraftFromCatalog(catalog)
    const keys = [
      ...draft.ruleSets.map((ruleSet) => ruleSet.editorKey),
      ...draft.ruleSets.flatMap((ruleSet) =>
        ruleSet.rules.map((rule) => rule.editorKey),
      ),
      ...draft.ruleSets.flatMap((ruleSet) =>
        ruleSet.rules.flatMap((rule) =>
          rule.gates.map((gate) => gate.editorKey),
        ),
      ),
      ...draft.repositoryAssignments.map((assignment) => assignment.editorKey),
    ]
    expect(new Set(keys).size).toBe(keys.length)
    expect(draft.defaultRuleSetID).toBe("quiet")
    const validation = validateReviewAttentionPolicyDraft(draft, agents)
    expect(validation.issues).toEqual([])
    expect(validation.catalog).toBeDefined()
    const questions =
      validation.catalog?.rule_sets.default.rules["review.submitted"][0]
        .questions
    expect(stringifyExactJSON(questions!)).toBe(
      '{"large":9007199254740993,"Foo":1,"foo":2}',
    )
    expect(validation.catalog?.rule_sets.default.rules["review.empty"]).toEqual(
      [],
    )
    expect(validation.catalog?.repository_assignments).toEqual({
      "Empty/Policies": "default",
    })
  })

  it("compiles a complete catalog and reports order-independent metrics", () => {
    const validation = validateReviewAttentionPolicyDraft(validDraft(), agents)
    expect(validation.valid).toBe(true)
    expect(validation.issues).toEqual([])
    expect(validation.metrics).toMatchObject({
      ruleSets: 3,
      repositories: 2,
      rules: 3,
      gates: 5,
    })
    expect(validation.metrics.canonicalBytes).toBeGreaterThan(0)
    expect(validation.metrics.requestBytes).toBeGreaterThan(0)
    expect(validation.catalog).toMatchObject({
      default_rule_set_id: "release",
      repository_assignments: {
        "Acme/Widgets": "default",
        "Empty/Policies": "quiet",
      },
    })

    const reordered = validDraft()
    reordered.ruleSets.reverse()
    reordered.ruleSets[2].rules.reverse()
    reordered.repositoryAssignments.reverse()
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

  it("enforces built-in identity, immutable name, references, and unique names", () => {
    const draft = validDraft()
    draft.ruleSets[0].name = "Renamed"
    draft.ruleSets[1].id = "Bad"
    draft.ruleSets[2].name = "release CHECKS"
    draft.defaultRuleSetID = "missing"
    draft.repositoryAssignments[0].ruleSetID = "missing"
    draft.repositoryAssignments.push({
      editorKey: "assignment-collision",
      repository: "acme/widgets",
      ruleSetID: "default",
    })

    expect(issueCodes(draft)).toEqual(
      expect.objectContaining(
        new Set([
          "rule_set.default_name",
          "rule_set.id_invalid",
          "rule_set.name_duplicate",
          "rule_set.default_reference",
          "rule_set.assignment_reference",
          "repository.case_collision",
        ]),
      ),
    )

    const missingBuiltIn = validDraft()
    missingBuiltIn.ruleSets = missingBuiltIn.ruleSets.slice(1)
    expect(issueCodes(missingBuiltIn).has("rule_set.default_missing")).toBe(
      true,
    )
  })

  it("returns stable field issues for rule, gate, text, and agent failures", () => {
    const draft = validDraft()
    const selected = draft.ruleSets[0]
    selected.rules.push({
      editorKey: "duplicate-rule",
      decisionPoint: "pr_development.before_push",
      gates: [],
    })
    selected.rules[0].gates.push(
      aiGate("conflict", "other", "ai_working_context", "reviewer"),
    )
    selected.rules[0].gates.push({
      editorKey: "duplicate-gate",
      id: "other",
      kind: "zero",
    })
    const working = selected.rules[0].gates[0]
    if (working.kind === "ai_working_context") working.agentID = "missing"
    const deterministic = selected.rules[0].gates[2]
    if (deterministic.kind === "deterministic") {
      deterministic.questionsSource = "null"
    }

    const codes = issueCodes(draft)
    expect(codes).toEqual(
      expect.objectContaining(
        new Set([
          "decision_point.duplicate",
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
    const working = invalid.ruleSets[0].rules[0].gates[0]
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
    expect(issueCodes(invalid).has("gate.questions_invalid")).toBe(true)

    working.questionsSource = `${" ".repeat(128 << 10)}{"ok":true}`
    expect(issueCodes(invalid).has("gate.questions_invalid")).toBe(false)

    working.questionsSource = null
    working.title = "\u0085"
    expect(issueCodes(invalid).has("gate.text_invalid")).toBe(true)
    working.title = "\ufeff"
    expect(issueCodes(invalid).has("gate.text_invalid")).toBe(false)
  })

  it("checks rule-set name whitespace, Unicode, byte, and duplicate-ID constraints", () => {
    const draft = validDraft()
    draft.ruleSets[1].name = " Release checks "
    draft.ruleSets[2].id = "release"
    expect(issueCodes(draft)).toEqual(
      expect.objectContaining(
        new Set(["rule_set.name_invalid", "rule_set.id_duplicate"]),
      ),
    )

    draft.ruleSets[1].name = "bad\ud800"
    expect(issueCodes(draft).has("rule_set.name_invalid")).toBe(true)
    draft.ruleSets[1].name = "x".repeat(129)
    expect(issueCodes(draft).has("rule_set.name_invalid")).toBe(true)

    draft.ruleSets[1].name = "İ"
    draft.ruleSets[2].name = "i"
    expect(issueCodes(draft).has("rule_set.name_duplicate")).toBe(true)
  })

  it("semantically validates the normalized transport catalog", () => {
    const valid = validateReviewAttentionPolicyDraft(
      validDraft(),
      agents,
    ).catalog
    expect(valid).toBeDefined()
    expect(isReviewAttentionPolicyCatalogSemanticallyValid(valid!)).toBe(true)

    const invalid = structuredClone(valid!)
    invalid.default_rule_set_id = "missing"
    expect(isReviewAttentionPolicyCatalogSemanticallyValid(invalid)).toBe(false)
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

describe("review attention reusable-set resolution", () => {
  const defaultGates: ReviewAttentionGate[] = [
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

  function catalog(): ReviewAttentionPolicyCatalog {
    return {
      rule_sets: {
        default: {
          name: "Default",
          rules: { "pr_development.before_push": defaultGates },
        },
        quiet: { name: "Quiet", rules: {} },
      },
      default_rule_set_id: "quiet",
      repository_assignments: { "Acme/Widgets": "default" },
    }
  }

  it("selects an exact repository assignment or the current fallback", () => {
    expect(
      resolveReviewAttentionRuleSet(catalog(), "unknown/repo"),
    ).toMatchObject({
      ruleSetID: "quiet",
      ruleSetName: "Quiet",
      assigned: false,
    })
    expect(
      resolveReviewAttentionRuleSet(catalog(), "acme/widgets"),
    ).toMatchObject({
      ruleSetID: "default",
      ruleSetName: "Default",
      assigned: true,
    })
  })

  it("previews the selected decision and returns detached question values", () => {
    const selected = resolveReviewAttentionPolicy(
      catalog(),
      "ACME/WIDGETS",
      "pr_development.before_push",
    )
    expect(selected).toMatchObject({
      ruleSetID: "default",
      assigned: true,
      noop: false,
    })
    expect(selected.entries.map((entry) => entry.action)).toEqual([
      "assigned",
      "assigned",
    ])
    const questions = selected.effective[0].questions as ExactJSONObject
    questions.prompt = "Changed"
    expect(stringifyExactJSON(defaultGates[0].questions!)).toBe(
      '{"prompt":"Choose"}',
    )

    expect(
      resolveReviewAttentionPolicy(
        catalog(),
        "unknown/repo",
        "pr_development.before_push",
      ),
    ).toMatchObject({
      ruleSetID: "quiet",
      assigned: false,
      entries: [],
      effective: [],
      noop: true,
    })
  })
})
