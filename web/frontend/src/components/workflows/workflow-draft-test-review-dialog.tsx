import { IconAlertTriangle, IconPlayerPlay } from "@tabler/icons-react"
import { useEffect, useRef, useState } from "react"

import type {
  WorkflowDefinitionInspectionEffect,
  WorkflowTriggerSimulation,
  WorkflowTriggerSimulationReview,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function WorkflowDraftTestReviewDialog({
  open,
  pending,
  identity,
  simulation,
  review,
  onOpenChange,
  onConfirm,
}: {
  open: boolean
  pending: boolean
  identity: string
  simulation: WorkflowTriggerSimulation
  review: WorkflowTriggerSimulationReview
  onOpenChange: (open: boolean) => void
  onConfirm: (identity: string) => void
}) {
  const [confirmedIdentity, setConfirmedIdentity] = useState<string | null>(
    null,
  )
  const confirmationGateRef = useRef({
    identity,
    consumed: false,
  })
  const confirmed = confirmedIdentity === identity

  useEffect(() => {
    confirmationGateRef.current = { identity, consumed: false }
    setConfirmedIdentity(null)
  }, [identity])

  useEffect(() => {
    if (!open) {
      confirmationGateRef.current = { identity, consumed: false }
      setConfirmedIdentity(null)
    }
  }, [identity, open])

  const changeOpen = (nextOpen: boolean) => {
    if (pending) {
      return
    }
    if (!nextOpen) {
      confirmationGateRef.current = { identity, consumed: false }
      setConfirmedIdentity(null)
    }
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <IconPlayerPlay className="size-4" />
            Review trigger execution
          </DialogTitle>
          <DialogDescription>
            This review came from the server simulation for the exact draft and
            scenario. Confirming starts one ordinary asynchronous workflow run.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4">
          <section
            aria-labelledby="workflow-trigger-simulation-summary"
            className="border-border grid gap-3 rounded-md border p-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3
                id="workflow-trigger-simulation-summary"
                className="text-sm font-medium"
              >
                Server simulation
              </h3>
              <Badge variant="outline">
                {simulation.selected_kind.replaceAll("_", " ")}
              </Badge>
            </div>
            <dl className="grid gap-2 text-xs sm:grid-cols-2">
              <ReviewDatum
                label="Production match"
                value={simulation.matched ? "Matched" : "Not matched"}
              />
              <ReviewDatum
                label="Effective trigger"
                value={
                  simulation.effective_kind?.replaceAll("_", " ") ??
                  simulation.selected_kind.replaceAll("_", " ")
                }
              />
              <ReviewDatum
                label="Inputs"
                value={String(simulation.context_summary.input_count)}
              />
              <ReviewDatum
                label="Provided secrets"
                value={String(simulation.context_summary.secret_count)}
              />
              <ReviewDatum
                label="Event context"
                value={simulation.context_summary.has_event ? "Yes" : "No"}
              />
              <ReviewDatum
                label="Session / delivery"
                value={
                  [
                    simulation.context_summary.has_session ? "session" : null,
                    simulation.context_summary.has_delivery ? "delivery" : null,
                  ]
                    .filter(Boolean)
                    .join(" + ") || "None"
                }
              />
            </dl>
          </section>

          <section
            aria-labelledby="workflow-trigger-review-actions"
            className="border-border grid gap-3 rounded-md border p-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3
                id="workflow-trigger-review-actions"
                className="text-sm font-medium"
              >
                Server-reviewed actions
              </h3>
              <div className="flex flex-wrap gap-1.5">
                <Badge variant="outline">
                  {review.job_count} {review.job_count === 1 ? "job" : "jobs"}
                </Badge>
                <Badge variant="outline">
                  {review.step_count}{" "}
                  {review.step_count === 1 ? "step" : "steps"}
                </Badge>
              </div>
            </div>
            {review.targets.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No action target was included in the bounded server review.
              </p>
            ) : (
              <ul className="grid gap-1.5">
                {review.targets.map((target) => (
                  <li
                    key={target}
                    className="bg-muted/40 rounded px-2 py-1.5 font-mono text-xs break-all"
                  >
                    {target}
                  </li>
                ))}
              </ul>
            )}
            {!review.complete || review.limits.length > 0 ? (
              <div
                role="alert"
                className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs"
              >
                The server review is incomplete
                {review.limits.length > 0
                  ? ` (${review.limits.join(", ").replaceAll("_", " ")})`
                  : ""}
                . Execution is blocked until the exact draft has a complete
                review.
              </div>
            ) : null}
          </section>

          <section
            aria-labelledby="workflow-trigger-review-effects"
            className="grid gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-3"
          >
            <div className="flex items-start gap-2">
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300" />
              <div className="min-w-0">
                <h3
                  id="workflow-trigger-review-effects"
                  className="text-sm font-medium"
                >
                  Server-reviewed possible effects
                </h3>
                {review.effects.length === 0 ? (
                  <p className="text-muted-foreground mt-1 text-xs">
                    No effect was projected.
                  </p>
                ) : (
                  <ul className="text-muted-foreground mt-1 grid list-disc gap-1 pl-4 text-xs">
                    {review.effects.map((effect) => (
                      <li key={effectKey(effect)}>
                        {effectLabel(effect)}
                        {effect.occurrences > 1
                          ? ` (${effect.occurrences} occurrences)`
                          : ""}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </section>

          <div className="border-border flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="workflow-trigger-confirm-effects">
                I reviewed this server simulation and its possible effects
              </Label>
              <p
                id="workflow-trigger-confirm-effects-help"
                className="text-muted-foreground mt-1 text-xs"
              >
                The token is valid only for this exact session, draft, selected
                trigger, scenario, and review.
              </p>
            </div>
            <Switch
              id="workflow-trigger-confirm-effects"
              checked={confirmed}
              disabled={pending || !simulation.executable || !review.complete}
              aria-describedby="workflow-trigger-confirm-effects-help"
              onCheckedChange={(checked) =>
                setConfirmedIdentity(checked ? identity : null)
              }
            />
          </div>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={pending}
            onClick={() => changeOpen(false)}
          >
            Keep editing
          </Button>
          <Button
            type="button"
            disabled={!confirmed || pending}
            onClick={() => {
              const gate = confirmationGateRef.current
              if (
                confirmedIdentity === identity &&
                gate.identity === identity &&
                !gate.consumed
              ) {
                gate.consumed = true
                setConfirmedIdentity(null)
                onConfirm(identity)
              }
            }}
          >
            <IconPlayerPlay className="size-4" />
            {pending ? "Starting execution" : "Confirm and execute"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ReviewDatum({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 font-medium capitalize">{value}</dd>
    </div>
  )
}

function effectKey(effect: WorkflowDefinitionInspectionEffect) {
  return `${effect.kind}\u0000${effect.target ?? ""}`
}

function effectLabel(effect: WorkflowDefinitionInspectionEffect) {
  const target = effect.target ? ` (${effect.target})` : ""
  switch (effect.kind) {
    case "model_or_delegated_action_possible":
      return `A model or delegated tool action may run${target}.`
    case "state_change_possible":
      return `PicoClaw-managed state may change${target}.`
    case "external_state_change_possible":
      return `External system state may change${target}.`
    case "transitive_effects_unknown":
      return `A reusable action can have transitive effects${target}.`
    case "unclassified_action":
      return `An action has effects the server could not classify${target}.`
  }
}
