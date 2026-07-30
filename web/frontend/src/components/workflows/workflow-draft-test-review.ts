export interface WorkflowDraftActionReview {
  jobCount: number
  stepCount: number
  targets: string[]
  rawOnlyCount: number
}

export interface WorkflowDraftTestReviewContext {
  targetRef: string
  yaml: string
  prompt: string
  mode: "event" | "manual"
  eventID?: string
  inputsJSON: string
  secretsJSON: string
  session: string
  deliveryJSON: string
  review: WorkflowDraftActionReview
}

export function workflowDraftTestReviewIdentity(
  context: WorkflowDraftTestReviewContext,
) {
  return JSON.stringify([
    context.targetRef,
    context.yaml,
    context.prompt,
    context.mode,
    ...(context.mode === "event"
      ? [context.eventID ?? ""]
      : [
          context.inputsJSON,
          context.secretsJSON,
          context.session,
          context.deliveryJSON,
        ]),
    context.review.jobCount,
    context.review.stepCount,
    context.review.targets,
    context.review.rawOnlyCount,
  ])
}
