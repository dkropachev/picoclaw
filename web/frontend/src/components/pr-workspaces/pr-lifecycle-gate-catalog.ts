import type { PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-gate-profiles"

export const prLifecycleGateLabels = {
  "pr.charter.confirm": "Confirm PR charter",
  "pr.charter.reconfirm": "Confirm revised charter",
  "pr.review.start": "Start review",
  "pr.review.complete": "Complete review",
  "pr.finding.classify": "Classify finding",
  "pr.implementation.eligibility": "Authorize non-owned PR",
  "pr.implementation.start": "Start implementation",
  "pr.implementation.scope": "Classify implementation",
  "pr.implementation.complete": "Complete implementation",
  "pr.review.publish": "Publish review",
  "pr.implementation.publish": "Push implementation",
  "pr.deferred.publish": "Create deferred issue",
  "pr.correction.promote": "Promote correction",
  "pr.publication.reconcile": "Resolve unknown result",
} as const satisfies Record<PRLifecycleDecisionPoint, string>

export function prLifecycleGateLabel(
  decisionPoint: PRLifecycleDecisionPoint,
): string {
  return prLifecycleGateLabels[decisionPoint]
}
