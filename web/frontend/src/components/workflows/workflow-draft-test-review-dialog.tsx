import { IconAlertTriangle, IconPlayerPlay } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"

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

import type { WorkflowDraftActionReview } from "./workflow-draft-test-review"

export function WorkflowDraftTestReviewDialog({
  open,
  pending,
  identity,
  scenario,
  scenarioDetails = [],
  review,
  onOpenChange,
  onConfirm,
}: {
  open: boolean
  pending: boolean
  identity: string
  scenario: string
  scenarioDetails?: Array<{ label: string; value: string }>
  review: WorkflowDraftActionReview
  onOpenChange: (open: boolean) => void
  onConfirm: (identity: string) => void
}) {
  const [confirmedIdentity, setConfirmedIdentity] = useState<string | null>(
    null,
  )
  const confirmed = confirmedIdentity === identity
  const effects = useMemo(
    () => targetEffectWarnings(review.targets, review.rawOnlyCount),
    [review.rawOnlyCount, review.targets],
  )

  useEffect(() => {
    if (!open) {
      setConfirmedIdentity(null)
    }
  }, [open])

  const changeOpen = (nextOpen: boolean) => {
    if (pending) {
      return
    }
    if (!nextOpen) {
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
            Review draft test
          </DialogTitle>
          <DialogDescription>
            Rendering or saving structured actions never executes them. Review
            this exact draft scenario before starting a test run.
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-4">
          <section
            aria-labelledby="workflow-draft-test-scenario"
            className="border-border grid gap-2 rounded-md border p-3"
          >
            <h3
              id="workflow-draft-test-scenario"
              className="text-sm font-medium"
            >
              Selected scenario
            </h3>
            <p className="text-muted-foreground text-xs break-words">
              {scenario}
            </p>
            {scenarioDetails.length > 0 ? (
              <dl className="grid gap-2 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
                {scenarioDetails.map((detail) => (
                  <div key={detail.label} className="contents">
                    <dt className="text-muted-foreground font-medium">
                      {detail.label}
                    </dt>
                    <dd className="min-w-0 font-mono break-all whitespace-pre-wrap">
                      {detail.value}
                    </dd>
                  </div>
                ))}
              </dl>
            ) : null}
          </section>

          <section
            aria-labelledby="workflow-draft-test-actions"
            className="border-border grid gap-3 rounded-md border p-3"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h3
                id="workflow-draft-test-actions"
                className="text-sm font-medium"
              >
                Structured actions
              </h3>
              <div className="flex flex-wrap gap-1.5">
                <Badge variant="outline">
                  {review.jobCount} {review.jobCount === 1 ? "job" : "jobs"}
                </Badge>
                <Badge variant="outline">
                  {review.stepCount} {review.stepCount === 1 ? "step" : "steps"}
                </Badge>
              </div>
            </div>
            {review.targets.length === 0 ? (
              <p className="text-muted-foreground text-xs">
                No safe action targets were projected. Review the raw YAML
                before continuing.
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
            {review.rawOnlyCount > 0 ? (
              <div
                role="alert"
                className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs"
              >
                {review.rawOnlyCount} preserved{" "}
                {review.rawOnlyCount === 1 ? "item is" : "items are"} raw-only
                and cannot be fully summarized here. Inspect Workflow YAML
                before testing.
              </div>
            ) : null}
          </section>

          <section
            aria-labelledby="workflow-draft-test-effects"
            className="grid gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 p-3"
          >
            <div className="flex items-start gap-2">
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300" />
              <div>
                <h3
                  id="workflow-draft-test-effects"
                  className="text-sm font-medium"
                >
                  Possible effects
                </h3>
                <ul className="text-muted-foreground mt-1 grid list-disc gap-1 pl-4 text-xs">
                  {effects.map((effect) => (
                    <li key={effect}>{effect}</li>
                  ))}
                </ul>
              </div>
            </div>
          </section>

          <div className="border-border flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="workflow-draft-test-confirm-effects">
                I reviewed this scenario and its possible effects
              </Label>
              <p
                id="workflow-draft-test-confirm-effects-help"
                className="text-muted-foreground mt-1 text-xs"
              >
                A test is a real workflow run and may call models, tools, MCP
                servers, functions, or reusable workflows.
              </p>
            </div>
            <Switch
              id="workflow-draft-test-confirm-effects"
              checked={confirmed}
              disabled={pending}
              aria-describedby="workflow-draft-test-confirm-effects-help"
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
              if (confirmedIdentity === identity) {
                setConfirmedIdentity(null)
                onConfirm(identity)
              }
            }}
          >
            <IconPlayerPlay className="size-4" />
            {pending ? "Starting test" : "Confirm and run test"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function targetEffectWarnings(targets: string[], rawOnlyCount = 0) {
  const warnings: string[] = []
  if (targets.some((target) => target.startsWith("agent/"))) {
    warnings.push("Agent targets can invoke a model and delegated tools.")
  }
  if (targets.some((target) => target.startsWith("tool/"))) {
    warnings.push("Tool targets can read or change PicoClaw-managed state.")
  }
  if (targets.some((target) => target.startsWith("mcp/"))) {
    warnings.push("MCP targets can read or change external system state.")
  }
  if (targets.some((target) => target.startsWith("function/"))) {
    warnings.push("Native functions can inspect or update workflow state.")
  }
  const unrecognizedTarget = targets.some(
    (target) =>
      !target.startsWith("agent/") &&
      !target.startsWith("tool/") &&
      !target.startsWith("mcp/") &&
      !target.startsWith("function/"),
  )
  if (unrecognizedTarget || rawOnlyCount > 0 || warnings.length === 0) {
    warnings.push(
      "Reusable, advanced, or raw-only actions can have transitive effects that are not fully known.",
    )
  }
  return warnings
}
