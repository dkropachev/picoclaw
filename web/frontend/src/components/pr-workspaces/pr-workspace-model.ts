import type {
  PRWorkspace,
  PRWorkspaceFinding,
  PRWorkspaceFindingDisposition,
  PRWorkspacePhase,
  PRWorkspaceScopeDistance,
  PRWorkspaceValidationEvidence,
} from "@/api/pr-workspaces"

export const prWorkspacePhases: PRWorkspacePhase[] = [
  "intake",
  "charter",
  "review",
  "triage",
  "implementation",
  "validation",
  "completion_audit",
  "publication",
  "complete",
]

export const prWorkspaceScopeDistances: PRWorkspaceScopeDistance[] = [
  "S0_exact",
  "S1_necessary_adjacent",
  "S2_related_followup",
  "S3_unrelated",
]

export const prWorkspaceFindingDispositions: PRWorkspaceFindingDisposition[] = [
  "open",
  "in_scope",
  "fixed",
  "deferred",
  "dismissed",
]

export function phaseIndex(phase: PRWorkspacePhase): number {
  return prWorkspacePhases.indexOf(phase)
}

export function findingDispositionCounts(
  findings: PRWorkspaceFinding[],
): Record<PRWorkspaceFindingDisposition, number> {
  const counts: Record<PRWorkspaceFindingDisposition, number> = {
    open: 0,
    in_scope: 0,
    fixed: 0,
    deferred: 0,
    dismissed: 0,
  }
  for (const finding of findings) counts[finding.disposition] += 1
  return counts
}

export function scopeMatrixCounts(
  findings: PRWorkspaceFinding[],
): Record<PRWorkspaceScopeDistance, Record<"XS" | "S" | "M" | "L", number>> {
  const matrix = Object.fromEntries(
    prWorkspaceScopeDistances.map((distance) => [
      distance,
      { XS: 0, S: 0, M: 0, L: 0 },
    ]),
  ) as Record<PRWorkspaceScopeDistance, Record<"XS" | "S" | "M" | "L", number>>
  for (const finding of findings)
    matrix[finding.scope.distance][finding.scope.size] += 1
  return matrix
}

export function canImplementWorkspace(workspace: PRWorkspace): {
  allowed: boolean
  reason:
    | "ready"
    | "charter"
    | "closed"
    | "not_writable"
    | "eligibility_gate"
    | "review"
    | "triage"
    | "phase"
} {
  const charter = activePRWorkspaceCharter(workspace)
  if (!charter?.confirmed) {
    return { allowed: false, reason: "charter" }
  }
  if (workspace.provider_snapshot.state !== "open") {
    return { allowed: false, reason: "closed" }
  }
  if (!workspace.provider_snapshot.head_writable) {
    return { allowed: false, reason: "not_writable" }
  }
  const reviewed = workspace.stage_runs.some(
    (run) =>
      run.stage === "review" &&
      run.state === "succeeded" &&
      run.charter_id === charter.id &&
      run.head_sha === workspace.provider_snapshot.head_sha,
  )
  if (!reviewed) {
    return { allowed: false, reason: "review" }
  }
  if (workspace.findings.some((finding) => finding.disposition === "open")) {
    return { allowed: false, reason: "triage" }
  }
  if (
    workspace.workspace.phase !== "triage" &&
    workspace.workspace.phase !== "implementation"
  ) {
    return { allowed: false, reason: "phase" }
  }
  if (workspace.workspace.phase === "implementation") {
    const retryable = ["failed", "blocked", "queued"].includes(
      workspace.workspace.execution_state,
    )
    const attempted = workspace.stage_runs.some(
      (run) =>
        run.stage === "implementation" &&
        run.charter_id === charter.id &&
        run.head_sha === workspace.provider_snapshot.head_sha &&
        run.state !== "canceled" &&
        run.state !== "stale",
    )
    if (!retryable || !attempted) {
      return { allowed: false, reason: "phase" }
    }
  }
  if (!workspace.provider_snapshot.owned) {
    const eligibility = workspace.gates.find(
      (gate) => gate.decision_point === "pr.implementation.eligibility",
    )
    if (eligibility?.state !== "succeeded") {
      return { allowed: false, reason: "eligibility_gate" }
    }
  }
  return { allowed: true, reason: "ready" }
}

export function needsUserAction(workspace: PRWorkspace): boolean {
  return (
    workspace.workspace.execution_state === "waiting_gate" ||
    workspace.workspace.execution_state === "waiting_user" ||
    workspace.workspace.execution_state === "unknown" ||
    workspace.gates.some(
      (gate) => gate.state === "waiting_gate" || gate.state === "waiting_user",
    ) ||
    workspace.publications.some(
      (publication) => publication.state === "unknown",
    )
  )
}

export function activePRWorkspaceCharter(
  workspace: PRWorkspace,
): PRWorkspace["charters"][number] | undefined {
  const latest = [...workspace.charters].sort(
    (left, right) => right.revision - left.revision,
  )[0]
  const activeID = workspace.workspace.active_charter_id
  if (activeID) {
    const active = workspace.charters.find((charter) => charter.id === activeID)
    // A newly saved/revised draft is intentionally not active until its
    // confirmation gate passes. Prefer it in the editor so an older confirmed
    // charter cannot accidentally remain selected after a head refresh.
    if (
      latest &&
      !latest.confirmed &&
      (!active || latest.revision > active.revision)
    ) {
      return latest
    }
    return active ?? latest
  }
  return latest
}

export function latestValidation(workspace: PRWorkspace) {
  const implementationRunID = latestImplementationRunID(workspace)
  if (!implementationRunID) return workspace.validation_runs.at(-1)
  return [...workspace.validation_runs]
    .reverse()
    .find((validation) => validation.stage_run_id === implementationRunID)
}

export function isPRWorkspaceValidationGreen(
  validation: PRWorkspaceValidationEvidence | undefined,
): boolean {
  return (
    validation?.state === "succeeded" &&
    validation.checks.length > 0 &&
    validation.checks.every(
      (check) => check.status === "passed" || check.status === "skipped",
    )
  )
}

export function latestRepairAttempt(workspace: PRWorkspace) {
  const implementationRunID = latestImplementationRunID(workspace)
  if (!implementationRunID) return workspace.repair_attempts.at(-1)
  return [...workspace.repair_attempts]
    .reverse()
    .find((repair) => repair.stage_run_id === implementationRunID)
}

function latestImplementationRunID(workspace: PRWorkspace): string | undefined {
  return [...workspace.stage_runs]
    .reverse()
    .find((run) => run.stage === "implementation")?.id
}
