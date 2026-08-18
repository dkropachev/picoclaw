import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type PRLifecycleFlow,
  type PRLifecycleFlowCatalog,
  type PRLifecycleGateProfileSnapshot,
  createPRLifecycleGateStage,
  getPRLifecycleDecisionPointPurpose,
  getPRLifecycleGateProfiles,
  isPRLifecycleDecisionPoint,
  validatePRLifecycleGateProfiles,
} from "@/api/pr-lifecycle-gate-profiles"
import { requestPRWorkspaceJSON } from "@/api/pr-workspaces"

vi.mock("@/api/pr-workspaces", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/api/pr-workspaces")>()
  return { ...original, requestPRWorkspaceJSON: vi.fn() }
})

const mockedRequest = vi.mocked(requestPRWorkspaceJSON)

const reviewGateSpecs = [
  ["pr.charter.confirm", 1],
  ["pr.charter.reconfirm", 2],
  ["pr.review.start", 3],
  ["pr.review.complete", 4],
  ["pr.finding.classify", 5],
  ["pr.deferred.publish", 12],
  ["pr.review.publish", 10],
  ["pr.correction.promote", 13],
  ["pr.publication.reconcile", 14],
] as const

const implementationGateSpecs = [
  ["pr.implementation.eligibility", 6],
  ["pr.implementation.start", 7],
  ["pr.implementation.scope", 8],
  ["pr.implementation.complete", 9],
  ["pr.implementation.publish", 11],
  ["pr.deferred.publish", 12],
  ["pr.charter.reconfirm", 2],
  ["pr.correction.promote", 13],
  ["pr.publication.reconcile", 14],
] as const

function createLinearFlow(
  id: "review" | "implementation",
  title: string,
  specs: ReadonlyArray<readonly [string, number]>,
  loop: boolean,
): PRLifecycleFlow {
  const entry = `${id}_trigger`
  const finish = `${id}_finish`
  const nodes: PRLifecycleFlow["nodes"] = [
    {
      id: entry,
      kind: "action",
      title: `Start ${id}`,
      description: `Receive the ${id} workflow trigger.`,
      operation: `pr.${id}.trigger`,
      editable: false,
    },
    ...specs.map(([decisionPoint, ordinal], index) => ({
      id: `${id}_gate_${index + 1}`,
      kind: "gate" as const,
      title: `Gate ${ordinal}`,
      description: `Evaluate ${decisionPoint}.`,
      decision_point: decisionPoint,
      ordinal,
      editable: true,
    })),
    {
      id: finish,
      kind: "action",
      title: `Finish ${id}`,
      description: `Complete the ${id} workflow.`,
      operation: `pr.${id}.finish`,
      editable: false,
    },
  ]
  const edges: PRLifecycleFlow["edges"] = nodes
    .slice(0, -1)
    .map((node, index) => ({
      from: node.id,
      to: nodes[index + 1].id,
      mode: "linear",
      loop: false,
    }))
  if (loop) {
    edges.push({
      from: finish,
      to: `${id}_gate_1`,
      mode: "linear",
      loop: true,
    })
  }
  return { id, title, entry, nodes, edges }
}

const flow: PRLifecycleFlowCatalog = {
  schema: "pr-lifecycle-flow/v1",
  flows: [
    createLinearFlow("review", "Review workflow", reviewGateSpecs, false),
    createLinearFlow(
      "implementation",
      "Implementation workflow",
      implementationGateSpecs,
      true,
    ),
  ],
}

const validSnapshot: PRLifecycleGateProfileSnapshot = {
  gate_profiles: {
    default: {
      name: "Default",
      workflows: {
        "pr.review.complete": {
          id: "review_complete",
          name: "Review complete",
          purpose: "authorization",
          decision_point: "pr.review.complete",
          stages: [{ id: "automatic", kind: "zero" }],
        },
      },
    },
  },
  default_gate_profile_id: "default",
  repository_assignments: {},
  nudge: {
    review_minimum_additional: 2,
    review_maximum_additional: 5,
    completion_minimum_additional: 2,
    completion_maximum_additional: 5,
  },
  scope: {
    xs: { files: 1, semantic_lines: 20, modules: 1 },
    s: { files: 3, semantic_lines: 100, modules: 1 },
    m: { files: 10, semantic_lines: 500, modules: 3 },
  },
  deferred_issues: { mode: "ask" },
  flow,
  flow_revision: `sha256:${"a".repeat(64)}`,
  catalog_revision: "sha256:catalog",
  config_revision: "sha256:config",
  effects: { gateway_effect: "applied" },
}

function cloneSnapshot(): PRLifecycleGateProfileSnapshot {
  return structuredClone(validSnapshot)
}

function cloneWithFork(
  mode: "choice" | "parallel" | "optional",
): PRLifecycleGateProfileSnapshot {
  const snapshot = cloneSnapshot()
  const review = snapshot.flow.flows[0]
  const source = "review_gate_4"
  const existing = review.edges.find((edge) => edge.from === source)!
  existing.mode = mode
  existing.label = "Continue"
  if (mode === "choice") existing.outcome = "continue"
  review.nodes.push({
    id: "review_alternate_finish",
    kind: "action",
    title: "Alternate finish",
    description: "Complete the alternate path.",
    operation: "pr.review.alternate.finish",
    editable: false,
  })
  review.edges.push({
    from: source,
    to: "review_alternate_finish",
    mode,
    ...(mode === "choice" ? { outcome: "alternate" } : {}),
    label: "Alternate",
    loop: false,
  })
  return snapshot
}

function addOptionalSidecar(
  snapshot: PRLifecycleGateProfileSnapshot,
  source = "review_gate_4",
): PRLifecycleGateProfileSnapshot {
  const review = snapshot.flow.flows[0]
  review.nodes.push({
    id: "review_optional_finish",
    kind: "action",
    title: "Optional finish",
    description: "Complete the independent optional path.",
    operation: "pr.review.optional.finish",
    editable: false,
  })
  review.edges.push({
    from: source,
    to: "review_optional_finish",
    mode: "optional",
    label: "Sidecar",
    loop: false,
  })
  return snapshot
}

function cloneWithParallelAndOptional(): PRLifecycleGateProfileSnapshot {
  const snapshot = cloneSnapshot()
  const existing = snapshot.flow.flows[0].edges.find(
    (edge) => edge.from === "review_gate_4",
  )!
  existing.mode = "parallel"
  existing.label = "Candidate"
  return addOptionalSidecar(snapshot)
}

function cloneWithLinearAndOptional(): PRLifecycleGateProfileSnapshot {
  const snapshot = cloneSnapshot()
  const existing = snapshot.flow.flows[0].edges.find(
    (edge) => edge.from === "review_gate_4",
  )!
  existing.label = "Done"
  return addOptionalSidecar(snapshot)
}

async function expectMalformed(snapshot: unknown) {
  mockedRequest.mockResolvedValueOnce(snapshot)
  await expect(getPRLifecycleGateProfiles()).rejects.toMatchObject({
    code: "malformed_pr_lifecycle_gate_profiles",
  })
}

describe("PR lifecycle gate profiles", () => {
  beforeEach(() => mockedRequest.mockReset())

  it("recognizes canonical dynamic decision points and creates exact stage shapes", () => {
    expect(isPRLifecycleDecisionPoint("pr.review.complete")).toBe(true)
    expect(isPRLifecycleDecisionPoint("pr.implementation.scope_check")).toBe(
      true,
    )
    expect(isPRLifecycleDecisionPoint("review.complete")).toBe(false)
    expect(isPRLifecycleDecisionPoint("pr.Review.complete")).toBe(false)
    expect(isPRLifecycleDecisionPoint("pr.review")).toBe(false)
    expect(getPRLifecycleDecisionPointPurpose("pr.charter.reconfirm")).toBe(
      "authorization",
    )
    expect(getPRLifecycleDecisionPointPurpose("pr.finding.classify")).toBe(
      "classification",
    )
    expect(
      getPRLifecycleDecisionPointPurpose("pr.review.ghost"),
    ).toBeUndefined()
    expect(
      isPRLifecycleDecisionPoint(
        "pr.review.complete.extra.extra.extra.extra.extra.extra",
      ),
    ).toBe(false)

    expect(createPRLifecycleGateStage("deterministic", "head_check")).toEqual({
      id: "head_check",
      kind: "deterministic",
      title: "",
      when: "true",
    })
    expect(createPRLifecycleGateStage("zero", "automatic")).toEqual({
      id: "automatic",
      kind: "zero",
    })
    expect(createPRLifecycleGateStage("human", "approval")).toMatchObject({
      id: "approval",
      kind: "human",
      questions: ["Approve this step?"],
    })
  })

  it("allows omitted declared gates but rejects workflows absent from the graph", () => {
    expect(validatePRLifecycleGateProfiles(validSnapshot)).toEqual([])

    const snapshot = cloneSnapshot()
    snapshot.gate_profiles.default.workflows["pr.review.ghost"] = {
      id: "review_ghost",
      name: "Ghost gate",
      purpose: "authorization",
      decision_point: "pr.review.ghost",
      stages: [{ id: "automatic", kind: "zero" }],
    }
    expect(validatePRLifecycleGateProfiles(snapshot)).toContainEqual({
      path: "gate_profiles.default.workflows.pr.review.ghost",
      message: "Workflow decision point is not declared by the lifecycle flow.",
    })
  })

  it("locks each configured workflow to its catalog purpose", () => {
    const snapshot = cloneSnapshot()
    snapshot.gate_profiles.default.workflows["pr.review.complete"].purpose =
      "classification"

    expect(validatePRLifecycleGateProfiles(snapshot)).toContainEqual({
      path: "gate_profiles.default.workflows.pr.review.complete.purpose",
      message: "Purpose must be authorization for this decision point.",
    })
  })

  it("validates staged all-of requirements and assignments", () => {
    const snapshot = cloneSnapshot()
    snapshot.default_gate_profile_id = "missing"
    snapshot.repository_assignments = { "not a repo": "missing" }
    snapshot.gate_profiles.default.workflows["pr.review.complete"].stages = [
      { id: "bad.stage", kind: "deterministic", title: "", when: "" },
      {
        id: "bad.stage",
        kind: "human",
        title: "Approve",
        questions: [],
      },
    ]

    expect(
      validatePRLifecycleGateProfiles(snapshot).map((issue) => issue.message),
    ).toEqual(
      expect.arrayContaining([
        "Choose an existing default profile.",
        "Stage ID must be unique.",
        "Stage title is required.",
        "Deterministic condition is required.",
        "Human stages require a question.",
        "Use https://provider-origin|repository-id.",
        "Assignment references a missing profile.",
      ]),
    )
  })

  it("projects the exact graph-backed config and rejects the legacy wrapper", async () => {
    mockedRequest.mockResolvedValueOnce(validSnapshot)
    await expect(getPRLifecycleGateProfiles()).resolves.toEqual(validSnapshot)
    expect(mockedRequest).toHaveBeenCalledWith(
      "/api/pr-lifecycle/gate-profiles",
      undefined,
      undefined,
    )

    await expectMalformed({ profiles: validSnapshot.gate_profiles })
  })

  it("accepts choice, mandatory parallel, and optional multi-path semantics", async () => {
    for (const mode of ["choice", "parallel", "optional"] as const) {
      mockedRequest.mockResolvedValueOnce(cloneWithFork(mode))
      const projected = await getPRLifecycleGateProfiles()
      expect(projected.flow.flows[0].edges).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ mode, label: "Alternate" }),
        ]),
      )
    }
  })

  it("accepts one unlabeled optional edge but rejects other singleton fork modes", async () => {
    const optional = cloneSnapshot()
    optional.flow.flows[0].edges[0].mode = "optional"
    mockedRequest.mockResolvedValueOnce(optional)
    await expect(getPRLifecycleGateProfiles()).resolves.toBeDefined()

    const labeledOptional = structuredClone(optional)
    labeledOptional.flow.flows[0].edges[0].label = "Maybe"
    await expectMalformed(labeledOptional)

    for (const mode of ["choice", "parallel"] as const) {
      const invalid = cloneSnapshot()
      invalid.flow.flows[0].edges[0].mode = mode
      await expectMalformed(invalid)
    }
  })

  it("accepts choice or parallel primary paths with optional sidecars", async () => {
    const choiceAndOptional = addOptionalSidecar(cloneWithFork("choice"))
    mockedRequest.mockResolvedValueOnce(choiceAndOptional)
    await expect(getPRLifecycleGateProfiles()).resolves.toBeDefined()

    mockedRequest.mockResolvedValueOnce(cloneWithParallelAndOptional())
    await expect(getPRLifecycleGateProfiles()).resolves.toBeDefined()

    mockedRequest.mockResolvedValueOnce(cloneWithLinearAndOptional())
    await expect(getPRLifecycleGateProfiles()).resolves.toBeDefined()
  })

  it("enforces stable, complete manifest gate ordinals", async () => {
    const ordinals = new Map<string, number>()
    for (const graph of validSnapshot.flow.flows) {
      for (const node of graph.nodes) {
        if (!node.editable) continue
        if (ordinals.has(node.decision_point!)) {
          expect(node.ordinal).toBe(ordinals.get(node.decision_point!))
        } else {
          ordinals.set(node.decision_point!, node.ordinal!)
        }
      }
    }
    expect([...ordinals.values()].sort((left, right) => left - right)).toEqual(
      Array.from({ length: 14 }, (_, index) => index + 1),
    )

    const reusedWithWrongOrdinal = cloneSnapshot()
    reusedWithWrongOrdinal.flow.flows[1].nodes[6].ordinal = 7
    await expectMalformed(reusedWithWrongOrdinal)

    const duplicateOrdinal = cloneSnapshot()
    duplicateOrdinal.flow.flows[0].nodes[2].ordinal = 1
    await expectMalformed(duplicateOrdinal)

    const swappedOrdinals = cloneSnapshot()
    swappedOrdinals.flow.flows[0].nodes[1].ordinal = 2
    swappedOrdinals.flow.flows[0].nodes[2].ordinal = 1
    await expectMalformed(swappedOrdinals)

    const missingOrdinal = cloneSnapshot()
    delete missingOrdinal.flow.flows[0].nodes[1].ordinal
    await expectMalformed(missingOrdinal)
  })

  it("fails closed on malformed graph schemas, revisions, nodes, and references", async () => {
    const badSchema = cloneSnapshot()
    badSchema.flow.schema = "pr-lifecycle-flow/v2"
    await expectMalformed(badSchema)

    const badRevision = cloneSnapshot()
    badRevision.flow_revision = "sha256:not-a-digest"
    await expectMalformed(badRevision)

    const missingOperation = cloneSnapshot()
    delete missingOperation.flow.flows[0].nodes[0].operation
    await expectMalformed(missingOperation)

    const missingTarget = cloneSnapshot()
    missingTarget.flow.flows[0].edges[0].to = "unknown_node"
    await expectMalformed(missingTarget)

    const malformedGate = cloneSnapshot()
    malformedGate.flow.flows[0].nodes[1].editable = false
    await expectMalformed(malformedGate)
  })

  it("fails closed on invalid linear, choice, loop, and DAG semantics", async () => {
    const labeledLinear = cloneSnapshot()
    labeledLinear.flow.flows[0].edges[0].label = "Next"
    await expectMalformed(labeledLinear)

    const duplicateChoiceLabel = cloneWithFork("choice")
    duplicateChoiceLabel.flow.flows[0].edges.at(-1)!.label = "CONTINUE"
    await expectMalformed(duplicateChoiceLabel)

    const missingChoiceOutcome = cloneWithFork("choice")
    delete missingChoiceOutcome.flow.flows[0].edges.at(-1)!.outcome
    await expectMalformed(missingChoiceOutcome)

    const longChoiceLabel = cloneWithFork("choice")
    longChoiceLabel.flow.flows[0].edges.at(-1)!.label = "Wait until later"
    await expectMalformed(longChoiceLabel)

    const invalidOutcome = cloneWithFork("choice")
    invalidOutcome.flow.flows[0].edges.at(-1)!.outcome = "Not valid"
    await expectMalformed(invalidOutcome)

    const controlLabel = cloneWithFork("choice")
    controlLabel.flow.flows[0].edges.at(-1)!.label = "Bad\u0000"
    await expectMalformed(controlLabel)

    const overlongLabel = cloneWithFork("choice")
    overlongLabel.flow.flows[0].edges.at(-1)!.label = "x".repeat(257)
    await expectMalformed(overlongLabel)

    const mixedFork = cloneWithFork("choice")
    const mixedEdge = mixedFork.flow.flows[0].edges.at(-1)!
    mixedEdge.mode = "optional"
    delete mixedEdge.outcome
    await expectMalformed(mixedFork)

    const unlabeledLinearAndOptional = addOptionalSidecar(cloneSnapshot())
    await expectMalformed(unlabeledLinearAndOptional)

    const duplicateEndpoint = cloneWithFork("choice")
    duplicateEndpoint.flow.flows[0].edges.push({
      from: "review_gate_4",
      to: "review_alternate_finish",
      mode: "optional",
      label: "Sidecar",
      loop: false,
    })
    await expectMalformed(duplicateEndpoint)

    const parallelOutcome = cloneWithFork("parallel")
    parallelOutcome.flow.flows[0].edges.at(-1)!.outcome = "alternate"
    await expectMalformed(parallelOutcome)

    const selfLoop = cloneSnapshot()
    const selfLoopEdge = selfLoop.flow.flows[1].edges.at(-1)!
    selfLoopEdge.to = selfLoopEdge.from
    await expectMalformed(selfLoop)

    const cycle = cloneSnapshot()
    cycle.flow.flows[1].edges.at(-1)!.loop = false
    await expectMalformed(cycle)
  })

  it("rejects unknown deferred issue automation modes", async () => {
    await expectMalformed({
      ...validSnapshot,
      deferred_issues: { mode: "sometimes" },
    })
  })
})
