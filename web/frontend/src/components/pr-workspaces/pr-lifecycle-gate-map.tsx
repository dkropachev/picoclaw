import { type KeyboardEvent, type ReactNode, useId } from "react"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleGateKind,
  type PRLifecycleGateProfile,
  type PRLifecycleGateWorkflow,
  validatePRLifecycleGateWorkflow,
} from "@/api/pr-lifecycle-gate-profiles"
import { prLifecycleGateLabels } from "@/components/pr-workspaces/pr-lifecycle-gate-catalog"
import { cn } from "@/lib/utils"

interface PRLifecycleGateMapProps {
  selectedDecisionPoint: PRLifecycleDecisionPoint
  workflows?: PRLifecycleGateProfile["workflows"]
  profileName?: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
  className?: string
}

interface GateSpec {
  number: number
  decisionPoint: PRLifecycleDecisionPoint
  title: string
  decisionTitle: string
  detail: string
}

const gateSpecs = [
  {
    number: 1,
    decisionPoint: "pr.charter.confirm",
    title: prLifecycleGateLabels["pr.charter.confirm"],
    decisionTitle: "Approve purpose and scope",
    detail: "First charter revision",
  },
  {
    number: 2,
    decisionPoint: "pr.charter.reconfirm",
    title: prLifecycleGateLabels["pr.charter.reconfirm"],
    decisionTitle: "Approve revised purpose and scope",
    detail: "Revised charter path",
  },
  {
    number: 3,
    decisionPoint: "pr.review.start",
    title: prLifecycleGateLabels["pr.review.start"],
    decisionTitle: "Allow AI review",
    detail: "Before diff reaches the review agent",
  },
  {
    number: 4,
    decisionPoint: "pr.review.complete",
    title: prLifecycleGateLabels["pr.review.complete"],
    decisionTitle: "Accept review results",
    detail: "Overall findings and coverage",
  },
  {
    number: 5,
    decisionPoint: "pr.finding.classify",
    title: prLifecycleGateLabels["pr.finding.classify"],
    decisionTitle: "Decide ambiguous finding scope",
    detail: "Finding scope is ambiguous",
  },
  {
    number: 6,
    decisionPoint: "pr.implementation.eligibility",
    title: prLifecycleGateLabels["pr.implementation.eligibility"],
    decisionTitle: "Allow non-owned PR implementation",
    detail: "Non-owned pull requests only",
  },
  {
    number: 7,
    decisionPoint: "pr.implementation.start",
    title: prLifecycleGateLabels["pr.implementation.start"],
    decisionTitle: "Allow AI implementation",
    detail: "Before the pinned repair workspace",
  },
  {
    number: 8,
    decisionPoint: "pr.implementation.scope",
    title: prLifecycleGateLabels["pr.implementation.scope"],
    decisionTitle: "Allow large or adjacent work",
    detail: "Large or adjacent candidate work",
  },
  {
    number: 9,
    decisionPoint: "pr.implementation.complete",
    title: prLifecycleGateLabels["pr.implementation.complete"],
    decisionTitle: "Accept implementation",
    detail: "Validated, completion-audited candidate",
  },
  {
    number: 10,
    decisionPoint: "pr.review.publish",
    title: prLifecycleGateLabels["pr.review.publish"],
    decisionTitle: "Allow review publication",
    detail: "Independent GitHub review effect",
  },
  {
    number: 11,
    decisionPoint: "pr.implementation.publish",
    title: prLifecycleGateLabels["pr.implementation.publish"],
    decisionTitle: "Allow branch push",
    detail: "Independent branch push effect",
  },
  {
    number: 12,
    decisionPoint: "pr.deferred.publish",
    title: prLifecycleGateLabels["pr.deferred.publish"],
    decisionTitle: "Allow follow-up issue",
    detail: "Per deferred group in ask mode",
  },
  {
    number: 13,
    decisionPoint: "pr.correction.promote",
    title: prLifecycleGateLabels["pr.correction.promote"],
    decisionTitle: "Allow repository lesson",
    detail: "Optional repository-level learning",
  },
  {
    number: 14,
    decisionPoint: "pr.publication.reconcile",
    title: prLifecycleGateLabels["pr.publication.reconcile"],
    decisionTitle: "Allow result reconciliation",
    detail: "Unknown provider outcomes only",
  },
] as const satisfies readonly GateSpec[]

const advancedGates = [
  { index: 1, condition: "Purpose or scope changed" },
  { index: 12, condition: "A correction may become a repository lesson" },
  { index: 13, condition: "A GitHub write returned an unknown result" },
] as const

type GateFormat = "automatic" | "rule" | "ai" | "user" | "mixed" | "needs-setup"
type GateStageCategory = Exclude<GateFormat, "mixed" | "needs-setup">

interface GateFormatSummary {
  format: GateFormat
  label: string
  composition?: string
  fallback: boolean
  accessible: string
}

const stageCategory: Record<PRLifecycleGateKind, GateStageCategory> = {
  zero: "automatic",
  deterministic: "rule",
  ai_working_context: "ai",
  ai_isolated_context: "ai",
  human: "user",
}

const gateFormatLabels: Record<GateStageCategory, string> = {
  automatic: "Automatic",
  rule: "Rule",
  ai: "AI",
  user: "User",
}

export function PRLifecycleGateMap({
  selectedDecisionPoint,
  workflows,
  profileName,
  onSelect,
  className,
}: PRLifecycleGateMapProps) {
  const instanceID = useId().replaceAll(":", "")
  const titleID = `${instanceID}-title`
  const descriptionID = `${instanceID}-description`
  const gate = (index: number, compact = false) => (
    <GateNode
      compact={compact}
      instanceID={instanceID}
      onSelect={onSelect}
      selected={selectedDecisionPoint === gateSpecs[index].decisionPoint}
      spec={gateSpecs[index]}
      workflow={workflows?.[gateSpecs[index].decisionPoint]}
    />
  )

  return (
    <section
      aria-labelledby={titleID}
      className={cn("bg-card overflow-hidden rounded-xl border", className)}
    >
      <div className="flex flex-col gap-3 px-4 py-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={titleID} className="text-sm font-semibold">
              PR lifecycle gate flow
            </h2>
            {profileName ? (
              <span className="bg-muted/50 text-muted-foreground rounded-md border px-2 py-0.5 text-xs">
                Profile · {profileName}
              </span>
            ) : null}
          </div>
          <p
            id={descriptionID}
            className="text-muted-foreground mt-1 max-w-3xl text-xs"
          >
            Follow the actions people see from a GitHub review request through
            review, implementation, and publication. Select any numbered gate to
            edit its workflow.
          </p>
        </div>
        <div className="max-w-3xl space-y-1.5">
          <div
            aria-label="Diagram legend"
            className="text-muted-foreground flex flex-wrap gap-1.5 text-xs"
          >
            <Legend label="action" variant="action" />
            <Legend label="visible data" variant="data" />
            <Legend label="GitHub" variant="external" />
            <Legend label="editable gate" variant="gate" />
          </div>
          <GateFormatLegend />
        </div>
      </div>

      <ol aria-label="PR lifecycle ordered flow" className="sr-only">
        <li>GitHub sends a pull request review request.</li>
        <li>
          The user or explicitly configured automation tracks the pull request
          in PicoClaw; there is no built-in automatic workspace bridge.
        </li>
        <li>
          The pull request purpose and scope are recorded as visible data.
        </li>
        <li>Gate 1 approves that authority, then Gate 3 allows review.</li>
        <li>Gate 4 accepts the completed review and presents the findings.</li>
        <li>
          Conditional Gate 5 classifies an ambiguous finding beside the user’s
          findings decision.
        </li>
        <li>The user chooses to publish, fix, or defer findings.</li>
        <li>Gate 10 publishes a review to GitHub.</li>
        <li>
          Conditional Gate 6 authorizes a non-owned pull request before Gate 7
          starts implementation.
        </li>
        <li>
          Gate 8 evaluates applicable candidate scope; outside-scope work is
          resolved and rechecked before validation.
        </li>
        <li>Gate 9 accepts completion and Gate 11 pushes the branch.</li>
        <li>Gate 12 creates GitHub issues for deferred work.</li>
        <li>
          Rare and conditional decisions are available in the exceptions rail.
        </li>
      </ol>

      <div className="bg-muted/10 overflow-x-auto overscroll-x-contain border-t">
        <div
          aria-describedby={descriptionID}
          className="min-w-[124rem] space-y-4 p-4"
          role="group"
        >
          <FlowBand
            eyebrow="REVIEW"
            number="01"
            title="GitHub request → explicit tracking → purpose and scope → findings"
          >
            <FlowNode
              detail="A reviewer is requested on a pull request"
              kind="external"
              label="GitHub review request"
            />
            <FlowConnector label="explicit handoff" />
            <FlowNode
              detail="User action or explicitly configured automation; no built-in automatic workspace bridge"
              kind="action"
              label="Track PR in PicoClaw"
            />
            <FlowConnector label="purpose · scope" />
            <FlowNode
              detail="Set the goal, success criteria, included work, and exclusions"
              kind="data"
              label="PR purpose and scope"
            />
            <FlowConnector label="approval" />
            {gate(0)}
            <FlowConnector label="review request" />
            {gate(2)}
            <FlowConnector label="allowed" />
            <FlowNode
              detail="Analyze the pull request against its approved purpose and scope"
              kind="agent"
              label="AI review"
            />
            <FlowConnector label="findings" />
            {gate(3)}
            <FlowConnector label="review ready" />
            <FindingDecision gate={gate(4, true)} />
          </FlowBand>

          <section className="bg-background/60 rounded-xl border p-3">
            <FlowHeading
              eyebrow="FINDINGS DECISION"
              number="02"
              title="Publish, fix, and defer are separate choices"
            />
            <div className="grid grid-cols-3 gap-3">
              <ChoiceLane
                description="Send the selected findings as a GitHub review."
                title="Publish review"
              >
                {gate(9)}
                <FlowConnector label="publish" narrow />
                <FlowNode
                  compact
                  detail="Review comments"
                  kind="external"
                  label="GitHub review"
                />
              </ChoiceLane>
              <ChoiceLane
                description="Repair only the findings selected for this pull request."
                title="Fix findings"
              >
                <FlowNode
                  compact
                  detail="Findings chosen for repair in this pull request"
                  kind="data"
                  label="Selected findings to fix"
                />
                <ContinuationCue />
              </ChoiceLane>
              <ChoiceLane
                description="Keep follow-up work separate from the current change."
                title="Defer work"
              >
                <FlowNode
                  compact
                  detail="Grouped follow-up work"
                  kind="data"
                  label="Deferred findings"
                />
                <FlowConnector label="create issue" narrow />
                {gate(11)}
                <FlowConnector label="publish" narrow />
                <FlowNode
                  compact
                  detail="One issue per group"
                  kind="external"
                  label="GitHub issues"
                />
              </ChoiceLane>
            </div>
          </section>

          <FlowBand
            eyebrow="IMPLEMENTATION"
            number="03"
            title="Selected findings → scoped fix → validation → branch"
          >
            <ConditionalGate condition="Non-owned pull requests only">
              {gate(5, true)}
            </ConditionalGate>
            <FlowConnector label="then · owned PR skips #6" />
            {gate(6)}
            <FlowConnector label="allowed" />
            <FlowNode
              detail="Prepare a focused fix for the selected findings"
              kind="agent"
              label="AI implementation"
            />
            <FlowConnector label="candidate changes" />
            <ScopeDecision gate={gate(7, true)} />
            <FlowConnector label="accepted · in scope" />
            <FlowNode
              detail="Run tests and the required checks"
              kind="action"
              label="Validate changes"
            />
            <FlowConnector label="checks pass" />
            {gate(8)}
            <FlowConnector label="accept completion" />
            {gate(10)}
            <FlowConnector label="push" />
            <FlowNode
              detail="Updated pull request branch"
              kind="external"
              label="GitHub branch"
            />
          </FlowBand>

          <section
            aria-labelledby={`${instanceID}-advanced-title`}
            className="bg-background/60 rounded-xl border border-dashed p-3"
          >
            <div className="mb-3 flex items-start justify-between gap-4">
              <FlowHeading
                eyebrow="CONDITIONAL"
                number="04"
                title="Advanced / exception gates"
                titleID={`${instanceID}-advanced-title`}
              />
              <p className="text-muted-foreground max-w-xl text-right text-[11px] leading-snug">
                These gates appear only when their condition applies and remain
                editable here.
              </p>
            </div>
            <div className="grid grid-cols-3 gap-3">
              {advancedGates.map(({ index, condition }) => (
                <AdvancedGate
                  condition={condition}
                  key={gateSpecs[index].number}
                >
                  {gate(index, true)}
                </AdvancedGate>
              ))}
            </div>
          </section>
        </div>
      </div>

      <ol aria-label="PR lifecycle gates" className="sr-only">
        {gateSpecs.map((spec) => (
          <li key={spec.decisionPoint}>
            Gate {spec.number}: {spec.decisionTitle}. Editor: {spec.title}.{" "}
            {spec.detail}
            {selectedDecisionPoint === spec.decisionPoint
              ? ". Currently selected."
              : ""}
          </li>
        ))}
      </ol>
    </section>
  )
}

function FlowBand({
  number,
  eyebrow,
  title,
  children,
}: {
  number: string
  eyebrow: string
  title: string
  children: ReactNode
}) {
  return (
    <section className="bg-background/60 rounded-xl border p-3">
      <FlowHeading eyebrow={eyebrow} number={number} title={title} />
      <div className="flex min-h-36 items-stretch">{children}</div>
    </section>
  )
}

function FlowHeading({
  number,
  eyebrow,
  title,
  titleID,
}: {
  number: string
  eyebrow: string
  title: string
  titleID?: string
}) {
  return (
    <div className="mb-3 flex items-start gap-3">
      <span className="bg-primary text-primary-foreground rounded-md px-2 py-1 font-mono text-xs font-bold">
        {number}
      </span>
      <div>
        <p className="text-primary text-[10px] font-semibold tracking-wider">
          {eyebrow}
        </p>
        <h3 className="text-sm font-semibold" id={titleID}>
          {title}
        </h3>
      </div>
    </div>
  )
}

type FlowKind = "external" | "action" | "agent" | "data"

const flowKindLabel: Record<FlowKind, string> = {
  external: "GITHUB",
  action: "ACTION",
  agent: "AI ACTION",
  data: "VISIBLE DATA",
}

function FlowNode({
  label,
  detail,
  kind,
  compact = false,
}: {
  label: string
  detail: string
  kind: FlowKind
  compact?: boolean
}) {
  return (
    <div
      className={cn(
        "flex min-h-28 w-52 shrink-0 flex-col rounded-xl border p-3",
        compact && "min-h-24 w-44",
        kind === "external" && "bg-accent/50 border-primary/40",
        kind === "action" && "bg-secondary",
        kind === "agent" && "bg-primary/5 border-primary/40",
        kind === "data" && "bg-muted/30 border-dashed",
      )}
      data-flow-kind={kind}
    >
      <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
        {flowKindLabel[kind]}
      </span>
      <strong className="mt-1 text-xs leading-snug">{label}</strong>
      <span className="text-muted-foreground mt-2 text-[11px] leading-snug">
        {detail}
      </span>
    </div>
  )
}

function FlowConnector({
  label,
  narrow = false,
}: {
  label: string
  narrow?: boolean
}) {
  return (
    <div
      aria-label={`Flow: ${label}`}
      className={cn(
        "flex w-20 shrink-0 flex-col items-center justify-center px-1 text-center",
        narrow && "w-14",
      )}
      data-flow-edge={label}
    >
      <span className="text-foreground bg-background mb-1 rounded border px-1.5 py-0.5 text-[9px] leading-tight font-semibold">
        {label}
      </span>
      <div className="flex w-full items-center" aria-hidden="true">
        <span className="border-muted-foreground/60 grow border-t" />
        <span className="text-primary text-lg leading-none">›</span>
      </div>
    </div>
  )
}

function ChoiceLane({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <section className="bg-muted/20 min-h-52 rounded-xl border p-3">
      <h4 className="text-xs font-semibold">{title}</h4>
      <p className="text-muted-foreground mt-1 text-[11px]">{description}</p>
      <div className="mt-3 flex items-stretch">{children}</div>
    </section>
  )
}

function FindingDecision({ gate }: { gate: ReactNode }) {
  return (
    <div
      className="bg-muted/20 w-[32rem] shrink-0 rounded-xl border p-3"
      data-flow-branch="findings-decision"
    >
      <p className="text-primary text-[9px] font-bold tracking-wider">
        FINDINGS DECISION
      </p>
      <div className="mt-2 grid grid-cols-[1fr_14rem] gap-2">
        <div className="bg-secondary flex min-h-28 flex-col rounded-lg border p-3">
          <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
            ACTION
          </span>
          <strong className="mt-1 text-xs">
            Choose what to do with findings
          </strong>
          <span className="text-muted-foreground mt-2 text-[11px] leading-snug">
            Publish review · fix selected findings · defer follow-up
          </span>
        </div>
        <ConditionalGate condition="Finding scope is ambiguous">
          {gate}
        </ConditionalGate>
      </div>
    </div>
  )
}

function ContinuationCue() {
  return (
    <div
      className="text-primary ml-3 flex w-32 shrink-0 flex-col items-center justify-center rounded-lg border border-dashed px-3 text-center"
      data-flow-continuation="implementation"
    >
      <span className="text-2xl leading-none" aria-hidden="true">
        ↓
      </span>
      <strong className="mt-1 text-[11px] leading-snug">
        Continue to implementation
      </strong>
    </div>
  )
}

function ConditionalGate({
  condition,
  children,
}: {
  condition: string
  children: ReactNode
}) {
  return (
    <div className="bg-muted/20 flex min-w-56 flex-col rounded-lg border border-dashed p-2">
      <p className="text-muted-foreground mb-2 text-[9px] leading-snug font-bold tracking-wider">
        CONDITIONAL · {condition}
      </p>
      {children}
    </div>
  )
}

function ScopeDecision({ gate }: { gate: ReactNode }) {
  return (
    <div
      className="bg-muted/20 w-[43rem] shrink-0 rounded-xl border p-3"
      data-flow-branch="candidate-scope"
    >
      <p className="text-primary text-[9px] font-bold tracking-wider">
        CANDIDATE SCOPE DECISION
      </p>
      <h4 className="mt-1 text-xs font-semibold">Check candidate scope</h4>
      <div className="mt-2 grid grid-cols-[14rem_1fr] gap-2">
        <ConditionalGate condition="Large or adjacent candidate work">
          {gate}
        </ConditionalGate>
        <div className="space-y-2">
          <div
            className="bg-primary/5 border-primary/40 rounded-lg border px-3 py-2"
            data-scope-outcome="accepted"
          >
            <strong className="text-primary text-[10px]">
              ACCEPTED / IN SCOPE
            </strong>
            <p className="text-muted-foreground mt-1 text-[10px]">
              Continue to validation.
            </p>
          </div>
          <div
            className="bg-destructive/5 border-destructive/50 rounded-lg border border-dashed px-3 py-2"
            data-scope-outcome="outside"
          >
            <strong className="text-destructive text-[10px]">
              OUTSIDE SCOPE · USER CHOICE
            </strong>
            <p className="mt-1 text-[10px] font-semibold">
              Remove extra code · revise purpose/scope · defer follow-up · stop
            </p>
            <p
              className="text-muted-foreground mt-1 border-t border-dashed pt-1 text-[10px]"
              data-flow-edge="repair and recheck scope"
            >
              ↺ Repair candidate, then recheck scope before validation
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}

function AdvancedGate({
  condition,
  children,
}: {
  condition: string
  children: ReactNode
}) {
  return (
    <div className="bg-muted/20 flex min-w-0 flex-col rounded-xl border p-2">
      <p className="text-muted-foreground mb-2 min-h-8 text-[10px] leading-snug font-semibold">
        WHEN · {condition}
      </p>
      {children}
    </div>
  )
}

function GateNode({
  spec,
  workflow,
  selected,
  compact,
  instanceID,
  onSelect,
}: {
  spec: GateSpec
  workflow?: PRLifecycleGateWorkflow
  selected: boolean
  compact: boolean
  instanceID: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
}) {
  const format = summarizeGateFormat(workflow, spec.decisionPoint)
  const descriptionID = `${instanceID}-gate-${spec.number}-description`
  const formatID = `${instanceID}-gate-${spec.number}-format`
  const activate = () => onSelect(spec.decisionPoint)
  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    activate()
  }

  return (
    <button
      aria-describedby={`${descriptionID} ${formatID}`}
      aria-label={spec.decisionTitle}
      aria-pressed={selected}
      className={cn(
        "bg-primary/5 border-primary/60 hover:bg-primary/10 hover:border-primary focus-visible:ring-ring relative flex min-h-28 w-56 shrink-0 flex-col rounded-xl border-2 p-3 text-left shadow-sm transition-colors outline-none focus-visible:ring-2",
        compact && "min-h-24 w-full p-2.5",
        selected && "bg-primary/10 border-primary ring-primary/30 ring-2",
      )}
      data-decision-point={spec.decisionPoint}
      data-decision-title={spec.decisionTitle}
      data-edit-href={`/pull-requests?view=gate-profiles&gate=${spec.decisionPoint}`}
      data-editor-title={spec.title}
      data-flow-kind="gate"
      data-gate-format={format.format}
      data-gate-id={spec.decisionPoint}
      data-gate-number={spec.number}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      onKeyDown={handleKeyDown}
      type="button"
    >
      <span className="flex w-full items-start justify-between gap-2">
        <span className="text-primary text-[9px] font-bold tracking-wider">
          GATE DECISION
        </span>
        <span
          className={cn(
            "bg-primary text-primary-foreground flex size-6 shrink-0 items-center justify-center rounded-full font-mono text-[10px] font-bold",
            selected && "ring-primary/30 ring-2",
          )}
        >
          {spec.number}
        </span>
      </span>
      <strong className="mt-1 text-xs leading-snug">
        {spec.decisionTitle}
      </strong>
      <span
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
        id={descriptionID}
      >
        {spec.detail}
      </span>
      <span className="mt-auto flex flex-wrap items-center gap-1.5 pt-2">
        <span
          className={cn(
            "rounded-md border px-1.5 py-0.5 text-[9px] font-bold tracking-wider uppercase",
            gateFormatClassName(format.format),
          )}
        >
          {format.label}
        </span>
        {format.fallback ? (
          <span className="text-muted-foreground text-[8px] font-semibold tracking-wider uppercase">
            default fallback
          </span>
        ) : null}
      </span>
      {format.composition ? (
        <span className="text-foreground mt-1 text-[9px] font-semibold">
          {format.composition}
        </span>
      ) : null}
      <span className="text-primary mt-1 text-[9px] font-bold tracking-wider">
        SELECT TO EDIT →
      </span>
      <span className="sr-only" id={formatID}>
        {format.accessible}
      </span>
    </button>
  )
}

function summarizeGateFormat(
  workflow: PRLifecycleGateWorkflow | undefined,
  decisionPoint: PRLifecycleDecisionPoint,
): GateFormatSummary {
  if (!workflow) {
    return {
      format: "user",
      label: "User",
      fallback: true,
      accessible:
        "Gate format: User. No workflow is configured, so the runtime uses the default human fallback.",
    }
  }
  if (validatePRLifecycleGateWorkflow(workflow, decisionPoint).length > 0) {
    return {
      format: "needs-setup",
      label: "Needs setup",
      fallback: false,
      accessible:
        "Gate format: Needs setup. The configured workflow is incomplete or invalid.",
    }
  }

  const categories = workflow.stages.map((stage) => stageCategory[stage.kind])
  const uniqueCategories = new Set(categories)
  if (uniqueCategories.size === 1) {
    const category = categories[0]
    const label = gateFormatLabels[category]
    return {
      format: category,
      label,
      fallback: false,
      accessible: `Gate format: ${label}.`,
    }
  }

  const orderedCategories = categories.filter(
    (category, index) => index === 0 || category !== categories[index - 1],
  )
  const composition = orderedCategories
    .map((category) => gateFormatLabels[category])
    .join(" → ")
  return {
    format: "mixed",
    label: "Mixed",
    composition,
    fallback: false,
    accessible: `Gate format: Mixed. Ordered composition: ${orderedCategories
      .map((category) => gateFormatLabels[category])
      .join(", then ")}.`,
  }
}

function gateFormatClassName(format: GateFormat) {
  return cn(
    format === "automatic" && "bg-muted text-muted-foreground",
    format === "rule" && "bg-secondary text-secondary-foreground",
    format === "ai" && "bg-primary/10 border-primary/40 text-primary",
    format === "user" && "bg-accent text-accent-foreground",
    format === "mixed" && "bg-primary text-primary-foreground",
    format === "needs-setup" &&
      "bg-destructive/10 border-destructive/50 text-destructive",
  )
}

function Legend({
  label,
  variant,
}: {
  label: string
  variant: "external" | "action" | "data" | "gate"
}) {
  return (
    <span
      className={cn(
        "rounded-md border px-2 py-1",
        variant === "external" && "bg-accent/50 border-primary/40",
        variant === "action" && "bg-secondary",
        variant === "data" && "bg-muted/30 border-dashed",
        variant === "gate" && "bg-primary/10 border-primary",
      )}
    >
      {label}
    </span>
  )
}

function GateFormatLegend() {
  const formats: { format: GateFormat; label: string }[] = [
    { format: "automatic", label: "Automatic" },
    { format: "rule", label: "Rule" },
    { format: "ai", label: "AI" },
    { format: "user", label: "User" },
    { format: "mixed", label: "Mixed" },
  ]

  return (
    <div
      aria-label="Gate format legend"
      className="text-muted-foreground flex flex-wrap items-center gap-1 text-[10px]"
    >
      <span className="mr-1 font-semibold">Gate format</span>
      {formats.map(({ format, label }) => (
        <span
          className={cn(
            "rounded-md border px-1.5 py-0.5 font-bold tracking-wider uppercase",
            gateFormatClassName(format),
          )}
          key={format}
        >
          {label}
        </span>
      ))}
    </div>
  )
}
