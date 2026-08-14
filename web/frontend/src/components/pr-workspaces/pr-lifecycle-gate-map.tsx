import { type KeyboardEvent, type ReactNode, useId } from "react"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleGateKind,
  type PRLifecycleGateProfile,
  type PRLifecycleGateWorkflow,
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
  detail: string
}

const gateSpecs = [
  {
    number: 1,
    decisionPoint: "pr.charter.confirm",
    title: prLifecycleGateLabels["pr.charter.confirm"],
    detail: "First charter revision",
  },
  {
    number: 2,
    decisionPoint: "pr.charter.reconfirm",
    title: prLifecycleGateLabels["pr.charter.reconfirm"],
    detail: "Revised charter path",
  },
  {
    number: 3,
    decisionPoint: "pr.review.start",
    title: prLifecycleGateLabels["pr.review.start"],
    detail: "Before diff reaches the review agent",
  },
  {
    number: 4,
    decisionPoint: "pr.review.complete",
    title: prLifecycleGateLabels["pr.review.complete"],
    detail: "Overall findings and coverage",
  },
  {
    number: 5,
    decisionPoint: "pr.finding.classify",
    title: prLifecycleGateLabels["pr.finding.classify"],
    detail: "Once per ambiguous S1 or large S0 finding",
  },
  {
    number: 6,
    decisionPoint: "pr.implementation.eligibility",
    title: prLifecycleGateLabels["pr.implementation.eligibility"],
    detail: "Non-owned pull requests only",
  },
  {
    number: 7,
    decisionPoint: "pr.implementation.start",
    title: prLifecycleGateLabels["pr.implementation.start"],
    detail: "Before the pinned repair workspace",
  },
  {
    number: 8,
    decisionPoint: "pr.implementation.scope",
    title: prLifecycleGateLabels["pr.implementation.scope"],
    detail: "Large S0 or S1 candidate work",
  },
  {
    number: 9,
    decisionPoint: "pr.implementation.complete",
    title: prLifecycleGateLabels["pr.implementation.complete"],
    detail: "Validated, completion-audited candidate",
  },
  {
    number: 10,
    decisionPoint: "pr.review.publish",
    title: prLifecycleGateLabels["pr.review.publish"],
    detail: "Independent GitHub review effect",
  },
  {
    number: 11,
    decisionPoint: "pr.implementation.publish",
    title: prLifecycleGateLabels["pr.implementation.publish"],
    detail: "Independent branch push effect",
  },
  {
    number: 12,
    decisionPoint: "pr.deferred.publish",
    title: prLifecycleGateLabels["pr.deferred.publish"],
    detail: "Per deferred group in ask mode",
  },
  {
    number: 13,
    decisionPoint: "pr.correction.promote",
    title: prLifecycleGateLabels["pr.correction.promote"],
    detail: "Optional repository-level learning",
  },
  {
    number: 14,
    decisionPoint: "pr.publication.reconcile",
    title: prLifecycleGateLabels["pr.publication.reconcile"],
    detail: "Unknown provider outcomes only",
  },
] as const satisfies readonly GateSpec[]

const stageLabels: Record<PRLifecycleGateKind, string> = {
  zero: "0",
  deterministic: "D",
  ai_working_context: "AI-W",
  ai_isolated_context: "AI-I",
  human: "H",
}

const stageAccessibleLabels: Record<PRLifecycleGateKind, string> = {
  zero: "automatic pass",
  deterministic: "deterministic rule",
  ai_working_context: "AI with working context",
  ai_isolated_context: "AI with isolated context",
  human: "human decision",
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
  const gate = (index: number) => (
    <GateNode
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
              PR lifecycle event and data flow
            </h2>
            {profileName ? (
              <span className="bg-muted/50 text-muted-foreground rounded-md border px-2 py-0.5 text-xs">
                Profile · {profileName}
              </span>
            ) : null}
          </div>
          <p
            id={descriptionID}
            className="text-muted-foreground mt-1 max-w-4xl text-xs"
          >
            Follow the payload from a GitHub review assignment through durable
            workspace data, AI actions, editable gates, repair loops, and
            independent GitHub effects. Select any numbered gate to edit it.
          </p>
        </div>
        <div
          aria-label="Diagram node and gate-stage legend"
          className="text-muted-foreground flex max-w-3xl flex-wrap gap-1.5 text-xs"
        >
          <Legend label="external event" variant="external" />
          <Legend label="explicit action" variant="action" />
          <Legend label="durable data" variant="data" />
          <Legend label="editable gate" variant="gate" />
          <StageLegend code="0" label="automatic" />
          <StageLegend code="D" label="rule" />
          <StageLegend code="AI-W" label="AI + context" />
          <StageLegend code="AI-I" label="AI isolated" />
          <StageLegend code="H" label="human" />
        </div>
      </div>

      <div className="bg-muted/10 overflow-x-auto border-t">
        <div
          aria-describedby={descriptionID}
          className="min-w-[112rem] space-y-4 p-4"
          role="group"
        >
          <FlowBand
            eyebrow="AUTOMATIC INGRESS, THEN AN EXPLICIT HANDOFF"
            number="01"
            title="GitHub review request → event inbox → tracked PR workspace"
          >
            <FlowNode
              detail="You are added as a requested reviewer"
              kind="external"
              label="GitHub · pull_request.review_requested"
            />
            <FlowConnector label="repo · PR # · reviewer · base/head SHA" />
            <FlowNode
              detail="Signed webhook or read-only notification poll"
              kind="system"
              label="PicoClaw · verify, scope, redact, dedupe"
            />
            <FlowConnector label="authenticated normalized envelope" />
            <FlowNode
              detail="Generic event; optional configured workflow dispatch"
              kind="data"
              label="Durable event inbox"
            />
            <FlowConnector conditional label="no built-in PR bridge" />
            <FlowNode
              detail="User clicks Track PR, or custom automation supplies the PR URL"
              kind="gap"
              label="Explicit workspace handoff"
            />
            <FlowConnector label="pull request URL" />
            <FlowNode
              detail="Resolve exact repository, PR, viewer, ownership, and capabilities"
              kind="action"
              label="User / automation · Track PR"
            />
            <FlowConnector label="authoritative provider read" />
            <FlowNode
              detail="Identity · base/head · author · writable/review/issue capabilities"
              kind="data"
              label="Verified provider snapshot"
            />
          </FlowBand>

          <FlowBand
            eyebrow="AUTHORITY DATA"
            number="02"
            title="Verified snapshot → charter → confirmed scope authority"
          >
            <FlowNode
              detail="Explicit AI draft or user-authored draft"
              kind="action"
              label="Charter agent + user"
            />
            <FlowConnector label="type · goal · criteria · included/excluded" />
            <FlowNode
              detail="Fix · refactor · feature · documentation · test"
              kind="data"
              label="Draft charter revision"
            />
            <FlowConnector conditional label="first / revised" />
            <BranchStack label="Alternative charter gates">
              {gate(0)}
              {gate(1)}
            </BranchStack>
            <FlowConnector label="pass pins authority" />
            <FlowNode
              detail="Provider snapshot + confirmed charter + guidance + corrections + lessons"
              kind="data"
              label="Canonical shared facts bundle"
            />
            <FlowConnector
              conditional
              label="revise loops to charter"
              reverse
            />
            <FlowNode
              detail="Head or charter changes stale dependent evidence and require explicit refresh"
              kind="system"
              label="Revision and head fences"
            />
          </FlowBand>

          <FlowBand
            eyebrow="EXPLICIT RUN REVIEW; NUDGES ARE AUTOMATIC INSIDE THE RUN"
            number="03"
            title="Shared facts + immutable diff → review agent → persisted finding data"
          >
            {gate(2)}
            <FlowConnector label="snapshot + charter + corrections" />
            <FlowNode
              detail="Loads exact provider-fenced unified diff"
              kind="data"
              label="Immutable review input"
            />
            <FlowConnector label="diff + audience projection" />
            <FlowNode
              detail="Initial scan of every changed path and hunk"
              kind="agent"
              label="PicoClaw · review agent"
            />
            <FlowConnector label="findings + coverage evidence" />
            <FlowNode
              detail="Automatically asks for missed issues even after zero findings; tries and rewards variants"
              kind="loop"
              label="Bounded adaptive Find more loop"
            />
            <FlowConnector label="novel/duplicate results + prompt digests" />
            <FlowNode
              detail="Severity · evidence · S0–S3 · XS–L · type compatibility · coverage · nudge history"
              kind="data"
              label="Findings, stage evidence, nudge records"
            />
          </FlowBand>

          <FlowBand
            eyebrow="POST-REVIEW DECISIONS"
            number="04"
            title="Review result → completion gate + per-finding classification → triaged data"
          >
            <BranchStack label="Run after review output">
              {gate(3)}
              {gate(4)}
            </BranchStack>
            <FlowConnector label="#4 overall · #5 per ambiguous finding" />
            <FlowNode
              detail="S0 XS/S → in scope · S2/S3 or incompatible → deferred · ambiguous S1/large S0 → gate"
              kind="system"
              label="Deterministic scope and type split"
            />
            <FlowConnector label="disposition + user corrections" />
            <FlowNode
              detail="In scope · deferred · dismissed · revise charter"
              kind="data"
              label="Triaged finding set"
            />
            <FlowConnector conditional label="deferred findings" />
            <FlowNode
              detail="Editable, split/merge/linkable follow-up work"
              kind="data"
              label="Deferred groups"
            />
            <FlowConnector conditional label="in-scope finding IDs" />
            <FlowNode
              detail="User chooses implementation, review publication, or both independently"
              kind="action"
              label="Explicit next action"
            />
          </FlowBand>

          <FlowBand
            eyebrow="EXPLICIT RUN IMPLEMENTATION"
            number="05"
            title="Authorized findings → repair candidate → exact scope audit"
          >
            <BranchStack label="Ownership branch">
              {gate(5)}
              <FlowNode
                compact
                detail="Owned PR bypasses #6; writable head is still mandatory"
                kind="system"
                label="Owned + writable"
              />
            </BranchStack>
            <FlowConnector label="eligible finding IDs" />
            {gate(6)}
            <FlowConnector label="charter + findings + shared facts" />
            <FlowNode
              detail="Edit-only agent in a pinned local Git workspace"
              kind="agent"
              label="PicoClaw · implementation agent"
            />
            <FlowConnector label="changed files + candidate diff + prompt digest" />
            <FlowNode
              detail="Every real hunk mapped; deterministic size and PR-type checks added"
              kind="agent"
              label="Isolated scope auditor"
            />
            <FlowConnector label="S0–S3 · XS–L · presence · evidence" />
            <BranchStack label="Candidate scope branch">
              {gate(7)}
              <FlowNode
                compact
                detail="Candidate S2/S3/type mismatch: no pass; remove+defer, revise charter, or stop"
                kind="hard"
                label="Mandatory hard-scope resolution"
              />
            </BranchStack>
          </FlowBand>

          <FlowBand
            eyebrow="REPAIR, VALIDATION, AND COMPLETENESS LOOPS"
            number="06"
            title="Candidate diff → CI evidence → completion search → accepted candidate"
          >
            <FlowNode
              detail="Local checks are bound to the exact candidate SHA"
              kind="action"
              label="Git / CI · validate candidate"
            />
            <FlowConnector label="check results + summaries" />
            <FlowNode
              detail="Red checks become in-scope repair findings and loop back to implementation"
              kind="data"
              label="Validation evidence"
            />
            <FlowConnector conditional label="green candidate" />
            <FlowNode
              detail="Checks every acceptance criterion, missing work, and out-of-scope code"
              kind="agent"
              label="PicoClaw · completion audit"
            />
            <FlowConnector label="automatic Check again variants" />
            <FlowNode
              detail="Missing work → repair · follow-up → defer · candidate drift → hard scope"
              kind="loop"
              label="Completion nudge and repair loop"
            />
            <FlowConnector label="no unresolved required work" />
            {gate(8)}
            <FlowConnector label="pass + fresh context digest" />
            <FlowNode
              detail="Retained local commit + exact branch publication fence"
              kind="data"
              label="Authorized implementation candidate"
            />
          </FlowBand>

          <FlowBand
            eyebrow="THREE INDEPENDENT PROVIDER EFFECTS"
            number="07"
            title="Frozen payloads → publication gates → background worker → GitHub"
          >
            <PublicationLane
              gate={gate(9)}
              input="Selected in-scope findings + review summary"
              output="GitHub review comments · never approve/request changes"
              payload="Frozen review payload + stable marker"
            />
            <PublicationLane
              gate={gate(10)}
              input="Validated retained commit + exact head fence"
              output="Exact PR branch push · never merge"
              payload="Frozen branch payload + digest"
            />
            <PublicationLane
              gate={gate(11)}
              input="One deferred group; ask/automatic/off policy"
              output="GitHub follow-up issue + stable marker"
              payload="Frozen issue title/body/labels/finding IDs"
            />
          </FlowBand>

          <FlowBand
            eyebrow="SHARED USER CORRECTIONS"
            number="08"
            title="Corrections update the same workspace context; promotion is separate"
          >
            <FlowNode
              detail="Wrong claim + corrected rule + evidence + review/implementation/both applicability"
              kind="action"
              label="User records correction or guidance"
            />
            <FlowConnector label="stored immediately at charter + head fence" />
            <FlowNode
              detail="Audience-specific projections feed later review, repair, scope, and completion prompts"
              kind="data"
              label="Canonical shared facts bundle"
            />
            <FlowConnector conditional label="optional promote" />
            {gate(12)}
            <FlowConnector label="pass" />
            <FlowNode
              detail="Reusable only for the same repository, PR type, and audience"
              kind="data"
              label="Repository lesson"
            />
            <FlowConnector reverse label="dispositions reward nudge variants" />
            <FlowNode
              detail="Novel findings earn delayed outcomes; review and completion learn independently"
              kind="data"
              label="Nudge success history"
            />
          </FlowBand>

          <FlowBand
            eyebrow="AMBIGUOUS EXTERNAL RESULT; NEVER BLINDLY RETRY"
            number="09"
            title="Unknown review, branch, or issue write → reconciliation gate"
          >
            <FlowNode
              detail="Provider may have applied the write even though PicoClaw did not receive a trustworthy result"
              kind="data"
              label="Publication state: unknown"
            />
            <FlowConnector label="publication ID + frozen marker/head proof" />
            {gate(13)}
            <FlowConnector label="pass: bounded read-only observation" />
            <FlowNode
              detail="Re-observe exact marker or remote head; success records safe external evidence"
              kind="system"
              label="Check provider again"
            />
            <FlowConnector conditional label="block: assume failed" />
            <FlowNode
              detail="Release the lock for a new deliberate request; absence alone is not proof"
              kind="gap"
              label="Unlock without blind retry"
            />
          </FlowBand>
        </div>
      </div>

      <ol aria-label="PR lifecycle gates" className="sr-only">
        {gateSpecs.map((spec) => {
          const summary = summarizeStages(workflows?.[spec.decisionPoint])
          return (
            <li key={spec.decisionPoint}>
              Gate {spec.number}: {spec.title}, {spec.decisionPoint}.{" "}
              {spec.detail}. {summary.accessible}
              {selectedDecisionPoint === spec.decisionPoint
                ? ". Currently selected."
                : ""}
            </li>
          )
        })}
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
      <div className="mb-3 flex items-start gap-3">
        <span className="bg-primary text-primary-foreground rounded-md px-2 py-1 font-mono text-xs font-bold">
          {number}
        </span>
        <div>
          <p className="text-primary text-[10px] font-semibold tracking-wider">
            {eyebrow}
          </p>
          <h3 className="text-sm font-semibold">{title}</h3>
        </div>
      </div>
      <div className="flex min-h-32 items-center">{children}</div>
    </section>
  )
}

type FlowKind =
  | "external"
  | "action"
  | "agent"
  | "system"
  | "data"
  | "gap"
  | "loop"
  | "hard"

const flowKindLabel: Record<FlowKind, string> = {
  external: "GITHUB EVENT",
  action: "EXPLICIT ACTION",
  agent: "AI ACTION",
  system: "SYSTEM ACTION",
  data: "DURABLE DATA",
  gap: "HANDOFF / STOP",
  loop: "BOUNDED LOOP",
  hard: "HARD INVARIANT",
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
        "flex min-h-28 w-56 shrink-0 flex-col rounded-xl border p-3",
        compact && "min-h-24 w-64",
        kind === "external" && "bg-accent/50 border-primary/40",
        kind === "action" && "bg-secondary",
        kind === "agent" && "bg-primary/5 border-primary/40",
        kind === "system" && "bg-muted/40",
        kind === "data" && "bg-muted/30 border-dashed",
        kind === "gap" &&
          "bg-destructive/5 border-destructive/50 border-dashed",
        kind === "loop" && "bg-accent/40 border-primary/50 border-dashed",
        kind === "hard" && "bg-destructive/10 border-destructive/60",
      )}
      data-flow-kind={kind}
    >
      <span
        className={cn(
          "text-muted-foreground text-[9px] font-bold tracking-wider",
          kind === "hard" && "text-destructive",
          kind === "gap" && "text-destructive",
        )}
      >
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
  conditional = false,
  reverse = false,
}: {
  label: string
  conditional?: boolean
  reverse?: boolean
}) {
  return (
    <div
      aria-label={`Data flow: ${label}`}
      className="flex w-32 shrink-0 flex-col items-center px-2 text-center"
      data-flow-edge={label}
    >
      <span className="text-muted-foreground mb-2 text-[9px] leading-tight font-medium">
        {label}
      </span>
      <div className="flex w-full items-center" aria-hidden="true">
        {reverse ? (
          <span className="text-primary text-lg leading-none">‹</span>
        ) : null}
        <span
          className={cn(
            "border-muted-foreground/60 grow border-t",
            conditional && "border-dashed",
          )}
        />
        {!reverse ? (
          <span className="text-primary text-lg leading-none">›</span>
        ) : null}
      </div>
    </div>
  )
}

function BranchStack({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <div className="bg-muted/20 w-72 shrink-0 space-y-2 rounded-xl border border-dashed p-2">
      <p className="text-muted-foreground text-center text-[9px] font-semibold tracking-wider">
        {label}
      </p>
      {children}
    </div>
  )
}

function PublicationLane({
  input,
  gate,
  payload,
  output,
}: {
  input: string
  gate: ReactNode
  payload: string
  output: string
}) {
  return (
    <div className="mr-3 grid w-[35rem] shrink-0 grid-cols-[1fr_1fr] gap-2 rounded-xl border p-2">
      <div className="bg-muted/30 col-span-2 rounded-lg border border-dashed px-3 py-2">
        <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
          INPUT DATA
        </span>
        <p className="mt-0.5 text-[11px]">{input}</p>
      </div>
      {gate}
      <div className="bg-muted/30 rounded-lg border border-dashed px-3 py-2">
        <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
          FROZEN DATA
        </span>
        <p className="mt-0.5 text-[11px]">{payload}</p>
      </div>
      <div className="bg-primary/5 border-primary/30 col-span-2 rounded-lg border px-3 py-2">
        <span className="text-primary text-[9px] font-bold tracking-wider">
          BACKGROUND WORKER → GITHUB OUTPUT
        </span>
        <p className="mt-0.5 text-[11px]">{output}</p>
      </div>
    </div>
  )
}

function GateNode({
  spec,
  workflow,
  selected,
  instanceID,
  onSelect,
}: {
  spec: GateSpec
  workflow?: PRLifecycleGateWorkflow
  selected: boolean
  instanceID: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
}) {
  const summary = summarizeStages(workflow)
  const descriptionID = `${instanceID}-gate-${spec.number}-description`
  const activate = () => onSelect(spec.decisionPoint)
  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    activate()
  }

  return (
    <button
      aria-describedby={descriptionID}
      aria-label={spec.title}
      aria-pressed={selected}
      className={cn(
        "bg-card hover:border-primary focus-visible:ring-ring flex min-h-28 w-full min-w-64 flex-col rounded-xl border-2 p-3 text-left transition-colors outline-none focus-visible:ring-2",
        selected && "bg-primary/10 border-primary",
      )}
      data-decision-point={spec.decisionPoint}
      data-edit-href={`/pull-requests?view=gate-profiles&gate=${spec.decisionPoint}`}
      data-flow-kind="gate"
      data-gate-id={spec.decisionPoint}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      onKeyDown={handleKeyDown}
      type="button"
    >
      <span className="flex w-full items-start justify-between gap-2">
        <span className="text-primary text-[9px] font-bold tracking-wider">
          EDITABLE GATE
        </span>
        <span
          className={cn(
            "bg-muted flex size-6 items-center justify-center rounded-full font-mono text-[10px] font-bold",
            selected && "bg-primary text-primary-foreground",
          )}
        >
          {spec.number}
        </span>
      </span>
      <span className="mt-1 font-mono text-[9px]">{spec.decisionPoint}</span>
      <strong className="mt-1 text-xs">{spec.title}</strong>
      <span
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
        id={descriptionID}
      >
        {spec.detail}
      </span>
      <span
        className={cn(
          "bg-muted text-muted-foreground mt-auto w-full rounded-md border px-2 py-1 text-center font-mono text-[9px] font-semibold",
          summary.fallback && "border-primary/40 text-primary",
          summary.invalid && "border-destructive/50 text-destructive",
        )}
      >
        {summary.visual}
      </span>
    </button>
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

function StageLegend({ code, label }: { code: string; label: string }) {
  return (
    <span className="bg-muted/50 rounded-md border px-2 py-1">
      <span className="text-foreground font-mono font-semibold">{code}</span>{" "}
      {label}
    </span>
  )
}

function summarizeStages(workflow?: PRLifecycleGateWorkflow) {
  if (!workflow) {
    return {
      visual: "NOT CONFIGURED · FALLBACK H",
      accessible:
        "No workflow is configured, so the runtime uses a fallback human gate.",
      fallback: true,
      invalid: false,
    }
  }
  if (workflow.stages.length === 0) {
    return {
      visual: "NO STAGES · INVALID DRAFT",
      accessible: "The configured workflow has no stages and is invalid.",
      fallback: false,
      invalid: true,
    }
  }
  const visibleStages = workflow.stages.slice(0, 4)
  const remaining = workflow.stages.length - visibleStages.length
  const visual = [
    ...visibleStages.map((stage) => stageLabels[stage.kind]),
    ...(remaining > 0 ? [`+${remaining}`] : []),
  ].join(" → ")
  const accessible = `Configured stages, in order: ${workflow.stages
    .map((stage) => stageAccessibleLabels[stage.kind])
    .join(", then ")}.`
  return { visual, accessible, fallback: false, invalid: false }
}
