import { beforeEach, describe, expect, it, vi } from "vitest"

import { launcherFetch } from "@/api/http"
import {
  createPRWorkspace,
  getPRWorkspace,
  listPRWorkspaces,
  mutatePRWorkspaceDeferredGroup,
  publishPRWorkspacePhase,
  reconcilePRWorkspacePublication,
  regroupPRWorkspaceDeferredFindings,
  sendPRWorkspaceMessage,
  setPRWorkspaceFindingDisposition,
  syncPRWorkspaceAutomaticDeferredIssues,
  updatePRWorkspaceDeferredGroup,
} from "@/api/pr-workspaces"

vi.mock("@/api/http", () => ({ launcherFetch: vi.fn() }))

const mockedLauncherFetch = vi.mocked(launcherFetch)

const workspaceRecord = {
  id: `prw_${"1".repeat(32)}`,
  provider: "github",
  provider_origin: "https://github.com",
  repository_id: "100",
  repository: "octo/repo",
  pull_request_id: "200",
  pull_number: 42,
  phase: "review",
  execution_state: "running",
  active_charter_id: `pcr_${"4".repeat(32)}`,
  provider_head_sha: "b".repeat(40),
  version: 3,
  created_at: "2026-08-13T10:00:00Z",
  updated_at: "2026-08-13T10:01:00Z",
} as const

const providerSnapshot = {
  provider: "github",
  provider_origin: "https://github.com",
  repository_id: "100",
  repository: "octo/repo",
  pull_request_id: "200",
  pull_number: 42,
  title: "Fix lost updates",
  body: "Keep optimistic concurrency intact.",
  author_id: "300",
  author_login: "octocat",
  authenticated_user_id: "300",
  base_ref: "main",
  base_sha: "a".repeat(40),
  head_repository_id: "100",
  head_ref: "fix/store",
  head_sha: "b".repeat(40),
  state: "open",
  owned: true,
  head_writable: true,
  can_review: true,
  can_create_issue: true,
  observed_at: "2026-08-13T10:00:00Z",
} as const

const pullRequestURL = "https://github.com/octo/repo/pull/42"

const aggregate = {
  workspace: workspaceRecord,
  provider_snapshot: providerSnapshot,
  charters: [],
  stage_runs: [],
  findings: [],
  messages: [],
  corrections: [],
  repository_lessons: [],
  nudge_rounds: [],
  deferred_groups: [],
  repair_attempts: [],
  validation_runs: [],
  gates: [],
  publications: [],
  activity: [],
}

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

describe("PR workspace API", () => {
  beforeEach(() => mockedLauncherFetch.mockReset())

  it("lists strict workspace summaries with encoded filters", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        workspaces: [workspaceRecord],
        next_cursor: "next+/=",
      }),
    )

    await expect(
      listPRWorkspaces({
        repository: "octo/repo with space",
        phase: "review",
        needs_action: true,
        limit: 50,
        cursor: "cursor+/=",
      }),
    ).resolves.toMatchObject({
      workspaces: [{ id: workspaceRecord.id }],
      next_cursor: "next+/=",
    })
    expect(mockedLauncherFetch).toHaveBeenCalledWith(
      "/api/pr-workspaces?repository=octo%2Frepo+with+space&phase=review&needs_action=true&limit=50&cursor=cursor%2B%2F%3D",
      { signal: undefined },
    )
  })

  it("normalizes omitted progressive aggregate arrays", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        workspace: workspaceRecord,
        provider_snapshot: providerSnapshot,
      }),
    )

    await expect(getPRWorkspace(workspaceRecord.id)).resolves.toMatchObject({
      workspace: workspaceRecord,
      charters: [],
      findings: [],
      gates: [],
    })
  })

  it("projects workspace corrections before a charter exists", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        workspace: {
          ...workspaceRecord,
          phase: "charter",
          active_charter_id: undefined,
        },
        corrections: [
          {
            id: `pco_${"5".repeat(32)}`,
            kind: "factual",
            applicability: "both",
            target_type: "workspace",
            target_id: workspaceRecord.id,
            original_claim: "AI workspace context",
            correction: "The boundary hour is invalid.",
            charter_id: "",
            head_sha: providerSnapshot.head_sha,
            promoted: false,
            created_at: "2026-08-13T10:00:30Z",
          },
        ],
      }),
    )

    const projected = await getPRWorkspace(workspaceRecord.id)
    expect(projected).toMatchObject({
      corrections: [
        {
          target_type: "workspace",
          correction: "The boundary hour is invalid.",
        },
      ],
    })
    expect(projected).not.toHaveProperty("corrections.0.charter_id")
  })

  it("sends request IDs and optimistic versions on mutations", async () => {
    mockedLauncherFetch.mockImplementation(async () => jsonResponse(aggregate))

    await createPRWorkspace({
      pull_request_url: pullRequestURL,
      request_id: `prq_${"2".repeat(32)}`,
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith("/api/pr-workspaces", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        pull_request_url: pullRequestURL,
        request_id: `prq_${"2".repeat(32)}`,
      }),
      signal: undefined,
    })

    await setPRWorkspaceFindingDisposition(workspaceRecord.id, "pfn/unsafe", {
      expected_version: 3,
      request_id: `prq_${"3".repeat(32)}`,
      disposition: "deferred",
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceRecord.id}/findings/pfn%2Funsafe/disposition`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          expected_version: 3,
          request_id: `prq_${"3".repeat(32)}`,
          disposition: "deferred",
        }),
      }),
    )
  })

  it("sends stage-targeted shared guidance and optional correction intent", async () => {
    mockedLauncherFetch.mockResolvedValue(jsonResponse(aggregate))
    const input = {
      expected_version: 3,
      request_id: `prq_${"7".repeat(32)}`,
      content: "Keep the retry change within the confirmed charter.",
      stage: "implementation",
      mark_as_correction: true,
      applicability: "both" as const,
    }

    await sendPRWorkspaceMessage(workspaceRecord.id, input)

    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceRecord.id}/messages`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(input),
      }),
    )
  })

  it("projects message bindings and typed stage evidence", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        messages: [
          {
            id: `pms_${"2".repeat(32)}`,
            role: "user",
            stage: "review",
            content: "Check the compare-and-swap caller.",
            charter_id: `pcr_${"4".repeat(32)}`,
            head_sha: "b".repeat(40),
            created_at: "2026-08-13T10:01:30Z",
          },
        ],
        stage_runs: [
          {
            id: `psr_${"3".repeat(32)}`,
            stage: "review",
            state: "succeeded",
            charter_id: `pcr_${"4".repeat(32)}`,
            head_sha: "b".repeat(40),
            attempt: 1,
            evidence: {
              stage: "review",
              run_id: `psr_${"3".repeat(32)}`,
              summary: "Reviewed the store update path.",
              coverage: {
                reviewed_areas: ["pkg/store"],
                unreviewed_areas: [],
                tests_considered: ["store concurrency tests"],
                residual_risks: ["provider race"],
              },
              finding_ids: [],
              validation: { state: "passed" },
              prompt_digest: "sha256:review-prompt",
              created_at: "2026-08-13T10:02:00Z",
            },
            started_at: "2026-08-13T10:01:31Z",
            finished_at: "2026-08-13T10:02:00Z",
          },
        ],
      }),
    )

    await expect(getPRWorkspace(workspaceRecord.id)).resolves.toMatchObject({
      messages: [
        {
          stage: "review",
          charter_id: `pcr_${"4".repeat(32)}`,
          head_sha: "b".repeat(40),
        },
      ],
      stage_runs: [
        {
          evidence: {
            coverage: { reviewed_areas: ["pkg/store"] },
            validation: { state: "passed" },
          },
        },
      ],
    })
  })

  it("publishes each PR phase and reconciles unknown outcomes with head fences", async () => {
    mockedLauncherFetch.mockImplementation(async () => jsonResponse(aggregate))
    const fence = {
      expected_version: 3,
      request_id: `prq_${"5".repeat(32)}`,
      expected_head_revision: "github-etag-3",
    }

    await publishPRWorkspacePhase(workspaceRecord.id, "review", {
      ...fence,
      finding_ids: [`pfn_${"6".repeat(32)}`],
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceRecord.id}/publications/review`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          ...fence,
          finding_ids: [`pfn_${"6".repeat(32)}`],
        }),
      }),
    )

    await publishPRWorkspacePhase(workspaceRecord.id, "implementation", fence)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceRecord.id}/publications/implementation`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(fence),
      }),
    )

    await reconcilePRWorkspacePublication(
      workspaceRecord.id,
      "ppb/unsafe",
      fence,
    )
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceRecord.id}/publications/ppb%2Funsafe/reconcile`,
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify(fence),
      }),
    )
  })

  it("uses encoded aggregate routes for the complete deferred-group workflow", async () => {
    mockedLauncherFetch.mockImplementation(async () => jsonResponse(aggregate))
    const fence = {
      expected_version: 3,
      request_id: `prq_${"8".repeat(32)}`,
    }
    const workspaceID = workspaceRecord.id
    const unsafeGroupID = "pdg/unsafe"

    await regroupPRWorkspaceDeferredFindings(workspaceID, fence)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceID}/deferred-groups/regroup`,
      expect.objectContaining({ method: "POST", body: JSON.stringify(fence) }),
    )

    await syncPRWorkspaceAutomaticDeferredIssues(workspaceID, fence)
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceID}/deferred-groups/automatic-sync`,
      expect.objectContaining({ method: "POST", body: JSON.stringify(fence) }),
    )

    await updatePRWorkspaceDeferredGroup(workspaceID, unsafeGroupID, {
      ...fence,
      title: "Bounded follow-up",
      body: "Preserve evidence.",
      labels: ["follow-up"],
    })
    expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
      `/api/pr-workspaces/${workspaceID}/deferred-groups/pdg%2Funsafe`,
      expect.objectContaining({ method: "PATCH" }),
    )

    for (const action of [
      "split",
      "merge",
      "link",
      "publish",
      "reconcile",
    ] as const) {
      await mutatePRWorkspaceDeferredGroup(workspaceID, unsafeGroupID, action, {
        ...fence,
        action_evidence: action,
      })
      expect(mockedLauncherFetch).toHaveBeenLastCalledWith(
        `/api/pr-workspaces/${workspaceID}/deferred-groups/pdg%2Funsafe/${action}`,
        expect.objectContaining({ method: "POST" }),
      )
    }
  })

  it("projects deferred publication suppression after a rejected gate", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        deferred_groups: [
          {
            id: `pdg_${"9".repeat(32)}`,
            title: "Deferred retry cleanup",
            body: "Track retry cleanup outside this PR.",
            finding_ids: [`pfn_${"8".repeat(32)}`],
            scope: {
              distance: "S2_related_followup",
              size: "S",
              files: 1,
              semantic_lines: 18,
              modules: 1,
              estimated: true,
              type_compatible: true,
              confidence: 0.9,
            },
            publication_suppressed: true,
            suppression_reason: "publication_gate_block",
            version: 2,
            created_at: "2026-08-13T10:04:00Z",
            updated_at: "2026-08-13T10:05:00Z",
          },
        ],
      }),
    )

    await expect(getPRWorkspace(workspaceRecord.id)).resolves.toMatchObject({
      deferred_groups: [
        {
          publication_suppressed: true,
          suppression_reason: "publication_gate_block",
        },
      ],
    })
  })

  it("rejects malformed workspace identity projections", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        workspace: { ...workspaceRecord, provider_head_sha: null },
        provider_snapshot: providerSnapshot,
      }),
    )
    await expect(getPRWorkspace(workspaceRecord.id)).rejects.toMatchObject({
      status: 502,
      code: "malformed_response",
    })
  })

  it("rejects malformed progressive aggregate records", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({ ...aggregate, findings: [{ id: "incomplete" }] }),
    )
    await expect(getPRWorkspace(workspaceRecord.id)).rejects.toMatchObject({
      status: 502,
      code: "malformed_response",
    })
  })

  it("projects only typed, allowlisted gate evidence", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        gates: [
          {
            id: `pgr_${"4".repeat(32)}`,
            decision_point: "pr.implementation.scope",
            purpose: "authorization",
            state: "waiting_user",
            policy_revision: "sha256:policy",
            subject_revision: "sha256:subject",
            turns: [
              {
                stage_id: "human-scope",
                kind: "human",
                title: "Approve exact large scope",
                status: "waiting",
              },
            ],
            evidence: {
              charter_type: "fix",
              charter_goal: "Prevent lost updates.",
              candidate_sha: "c".repeat(40),
              changed_files: ["pkg/store/store.go"],
              scope: {
                distance: "S0_exact",
                size: "M",
                presence: "candidate_present",
                files: 4,
                semantic_lines: 140,
                modules: 2,
                estimated: false,
                type_compatible: true,
                confidence: 0.96,
                change_evidence: [
                  {
                    path: "pkg/store/store.go",
                    hunk: "@@ CompareAndSwap @@",
                    module: "pkg/store",
                    semantic_lines: 14,
                    presence: "candidate_present",
                    scope_distance: "S0_exact",
                    change_size: "M",
                    type_compatible: true,
                    confidence: 0.96,
                    charter_clauses: ["Prevent lost updates."],
                    explanation: "This hunk implements the charter goal.",
                  },
                ],
              },
              validation_state: "succeeded",
              validation_checks: [
                {
                  id: "unit",
                  name: "Unit tests",
                  status: "passed",
                  duration_ms: 1200,
                },
              ],
              finding_ids: [`pfn_${"5".repeat(32)}`],
              finding_count: 1,
              hard_scope: true,
              hard_scope_finding_ids: [`pfn_${"5".repeat(32)}`],
              publication_kind: "branch_push",
              payload_digest: "sha256:payload",
              expected_head_sha: "b".repeat(40),
              provider_revision: "etag-4",
              repository: "acme/store",
              review_summary: "One blocking concurrency finding.",
              publication_findings: [
                {
                  id: `pfn_${"5".repeat(32)}`,
                  title: "Lost update remains",
                  file: "pkg/store/store.go",
                  line: 42,
                  message: "Use the compare-and-swap result.",
                },
              ],
              issue_title: "Follow up on store retry handling",
              issue_body: "Retry cleanup was deferred from this PR.",
              issue_labels: ["deferred", "store"],
              repair_summary: "Guarded the compare-and-swap update.",
            },
            created_at: "2026-08-13T10:02:00Z",
          },
        ],
      }),
    )

    await expect(getPRWorkspace(workspaceRecord.id)).resolves.toMatchObject({
      gates: [
        {
          evidence: {
            charter_type: "fix",
            scope: {
              distance: "S0_exact",
              size: "M",
              presence: "candidate_present",
              change_evidence: [
                { path: "pkg/store/store.go", hunk: "@@ CompareAndSwap @@" },
              ],
            },
            validation_checks: [{ name: "Unit tests", status: "passed" }],
            hard_scope: true,
            hard_scope_finding_ids: [`pfn_${"5".repeat(32)}`],
            publication_kind: "branch_push",
            repository: "acme/store",
            review_summary: "One blocking concurrency finding.",
            publication_findings: [
              {
                title: "Lost update remains",
                file: "pkg/store/store.go",
                line: 42,
              },
            ],
            issue_labels: ["deferred", "store"],
            repair_summary: "Guarded the compare-and-swap update.",
          },
        },
      ],
    })
  })

  it("projects automatic Gate execution metadata and field values", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        gates: [
          {
            id: `pgr_${"9".repeat(32)}`,
            decision_point: "pr.review.start",
            state: "succeeded",
            policy_revision: "sha256:policy",
            subject_revision: "sha256:subject",
            turns: [
              {
                stage_id: "verified-by-domain",
                kind: "gate/exec",
                title: "",
                status: "succeeded",
                "actor-kind": "deterministic",
                "execution-id": "gate-execution-1",
                "action-revision": "action-1",
                "input-hash": "sha256:input",
                "field-values": { action: "approve" },
              },
            ],
            created_at: "2026-08-13T10:02:00Z",
            finished_at: "2026-08-13T10:02:01Z",
          },
        ],
      }),
    )

    await expect(getPRWorkspace(workspaceRecord.id)).resolves.toMatchObject({
      gates: [
        {
          turns: [
            {
              kind: "gate/exec",
              title: "",
              status: "succeeded",
              actor_kind: "deterministic",
              execution_id: "gate-execution-1",
              action_revision: "action-1",
              input_hash: "sha256:input",
              field_values: { action: "approve" },
            },
          ],
        },
      ],
    })
  })

  it("rejects unsafe external publication links", async () => {
    mockedLauncherFetch.mockResolvedValue(
      jsonResponse({
        ...aggregate,
        publications: [
          {
            id: `ppb_${"7".repeat(32)}`,
            kind: "github_review",
            state: "succeeded",
            payload_digest: "sha256:publication",
            external_url: "javascript:alert(1)",
            attempts: 1,
            created_at: "2026-08-13T10:00:00Z",
            updated_at: "2026-08-13T10:01:00Z",
          },
        ],
      }),
    )
    await expect(getPRWorkspace(workspaceRecord.id)).rejects.toMatchObject({
      status: 502,
      code: "malformed_response",
    })
  })
})
