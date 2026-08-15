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

export const prLifecycleGateDecisionLabels = {
  "pr.charter.confirm": "Approve purpose and scope",
  "pr.charter.reconfirm": "Approve revised purpose and scope",
  "pr.review.start": "Allow AI review",
  "pr.review.complete": "Accept review results",
  "pr.finding.classify": "Decide ambiguous finding scope",
  "pr.implementation.eligibility": "Allow non-owned PR implementation",
  "pr.implementation.start": "Allow AI implementation",
  "pr.implementation.scope": "Allow large or adjacent work",
  "pr.implementation.complete": "Accept implementation",
  "pr.review.publish": "Allow review publication",
  "pr.implementation.publish": "Allow branch push",
  "pr.deferred.publish": "Allow follow-up issue",
  "pr.correction.promote": "Allow repository lesson",
  "pr.publication.reconcile": "Allow result reconciliation",
} as const satisfies Record<PRLifecycleDecisionPoint, string>

export function prLifecycleGateLabel(
  decisionPoint: PRLifecycleDecisionPoint,
): string {
  return prLifecycleGateLabels[decisionPoint]
}
