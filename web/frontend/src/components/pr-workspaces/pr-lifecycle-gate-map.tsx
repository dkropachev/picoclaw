import { type KeyboardEvent, useId } from "react"

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
  x: number
  y: number
  width: number
}

interface FlowPathProps {
  d: string
  markerID: string
  dashed?: boolean
  emphasis?: boolean
  label?: string
  labelX?: number
  labelY?: number
  labelWidth?: number
}

const gateHeight = 100

const gateSpecs = [
  {
    number: 1,
    decisionPoint: "pr.charter.confirm",
    title: prLifecycleGateLabels["pr.charter.confirm"],
    detail: "New or unchanged charter",
    x: 40,
    y: 160,
    width: 290,
  },
  {
    number: 2,
    decisionPoint: "pr.charter.reconfirm",
    title: prLifecycleGateLabels["pr.charter.reconfirm"],
    detail: "Reconfirmation path",
    x: 40,
    y: 350,
    width: 290,
  },
  {
    number: 3,
    decisionPoint: "pr.review.start",
    title: prLifecycleGateLabels["pr.review.start"],
    detail: "Exact provider diff",
    x: 460,
    y: 130,
    width: 290,
  },
  {
    number: 4,
    decisionPoint: "pr.review.complete",
    title: prLifecycleGateLabels["pr.review.complete"],
    detail: "Overall review result",
    x: 390,
    y: 340,
    width: 205,
  },
  {
    number: 5,
    decisionPoint: "pr.finding.classify",
    title: prLifecycleGateLabels["pr.finding.classify"],
    detail: "Per ambiguous finding",
    x: 615,
    y: 340,
    width: 205,
  },
  {
    number: 6,
    decisionPoint: "pr.implementation.eligibility",
    title: prLifecycleGateLabels["pr.implementation.eligibility"],
    detail: "Non-owner path only",
    x: 985,
    y: 115,
    width: 290,
  },
  {
    number: 7,
    decisionPoint: "pr.implementation.start",
    title: prLifecycleGateLabels["pr.implementation.start"],
    detail: "Selected in-scope findings",
    x: 985,
    y: 255,
    width: 290,
  },
  {
    number: 8,
    decisionPoint: "pr.implementation.scope",
    title: prLifecycleGateLabels["pr.implementation.scope"],
    detail: "S1 or large S0 only",
    x: 880,
    y: 650,
    width: 240,
  },
  {
    number: 9,
    decisionPoint: "pr.implementation.complete",
    title: prLifecycleGateLabels["pr.implementation.complete"],
    detail: "Validated candidate",
    x: 1140,
    y: 650,
    width: 240,
  },
  {
    number: 10,
    decisionPoint: "pr.review.publish",
    title: prLifecycleGateLabels["pr.review.publish"],
    detail: "Independent effect",
    x: 1440,
    y: 120,
    width: 205,
  },
  {
    number: 11,
    decisionPoint: "pr.implementation.publish",
    title: prLifecycleGateLabels["pr.implementation.publish"],
    detail: "Independent effect",
    x: 1670,
    y: 120,
    width: 205,
  },
  {
    number: 12,
    decisionPoint: "pr.deferred.publish",
    title: prLifecycleGateLabels["pr.deferred.publish"],
    detail: "Ask mode; independent",
    x: 1440,
    y: 350,
    width: 205,
  },
  {
    number: 13,
    decisionPoint: "pr.correction.promote",
    title: prLifecycleGateLabels["pr.correction.promote"],
    detail: "Optional repository lesson",
    x: 390,
    y: 930,
    width: 290,
  },
  {
    number: 14,
    decisionPoint: "pr.publication.reconcile",
    title: prLifecycleGateLabels["pr.publication.reconcile"],
    detail: "Re-observe or assume failed",
    x: 1670,
    y: 350,
    width: 205,
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
  const arrowID = `${instanceID}-arrow`

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
          <p id={descriptionID} className="text-muted-foreground mt-1 text-xs">
            Select a numbered gate to edit its workflow. Solid arrows show the
            normal action flow; dashed arrows are conditional paths, loops, or
            shared context.
          </p>
        </div>
        <div
          aria-label="Gate stage legend"
          className="text-muted-foreground flex max-w-2xl flex-wrap gap-1.5 text-xs"
        >
          <StageLegend code="0" label="automatic" />
          <StageLegend code="D" label="rule" />
          <StageLegend code="AI-W" label="AI + working context" />
          <StageLegend code="AI-I" label="AI + isolated context" />
          <StageLegend code="H" label="human" />
          <span className="rounded-md border border-dashed px-2 py-1">
            No workflow = fallback H
          </span>
        </div>
      </div>

      <div className="bg-muted/10 overflow-x-auto border-t">
        <svg
          aria-describedby={descriptionID}
          aria-labelledby={titleID}
          className="h-auto w-full min-w-[72rem]"
          role="group"
          viewBox="0 0 1920 1090"
        >
          <defs>
            <marker
              id={arrowID}
              markerHeight="8"
              markerWidth="8"
              orient="auto"
              refX="7"
              refY="4"
              viewBox="0 0 8 8"
            >
              <path className="fill-muted-foreground" d="M0 0 8 4 0 8Z" />
            </marker>
          </defs>

          <PhasePanel
            detail="#1–2 · choose by charter state"
            title="USER + CHARTER"
            width={330}
            x={20}
          />
          <PhasePanel
            detail="#3–5 · review and classification"
            title="PICOCLAW REVIEW"
            width={470}
            x={370}
          />
          <PhasePanel
            detail="#6–9 · writable head · scope + completion"
            title="PICOCLAW IMPLEMENTATION"
            width={540}
            x={860}
          />
          <PhasePanel
            detail="#10–12 effects · #14 recovery"
            title="GIT / GITHUB"
            width={480}
            x={1420}
          />

          <rect
            className="fill-muted/30 stroke-border"
            height="185"
            rx="16"
            strokeWidth="1"
            width="1880"
            x="20"
            y="890"
          />
          <text
            className="fill-foreground text-xs font-semibold tracking-wider"
            x="40"
            y="918"
          >
            SHARED CORRECTIONS + LEARNING
          </text>
          <text className="fill-muted-foreground text-xs" x="1000" y="918">
            #13 is optional; corrections feed both agents without promotion
          </text>

          <g className="pointer-events-none">
            <FlowPath d="M185 138V150H185V160" markerID={arrowID} />
            <FlowPath
              d="M70 138H25V325H185V350"
              dashed
              label="revised"
              labelWidth={58}
              labelX={29}
              labelY={316}
              markerID={arrowID}
            />
            <FlowPath d="M330 210H365V180H460" markerID={arrowID} />
            <FlowPath d="M330 400H355V285H365V180H460" markerID={arrowID} />

            <FlowPath d="M605 230V260" markerID={arrowID} />
            <FlowPath d="M605 298V320H492V340" markerID={arrowID} />
            <FlowPath
              d="M605 320H717V340"
              dashed
              label="per finding"
              labelWidth={78}
              labelX={665}
              labelY={315}
              markerID={arrowID}
            />
            <FlowPath d="M492 440V458H550V480" markerID={arrowID} />
            <FlowPath d="M717 440V458H660V480" markerID={arrowID} />

            <FlowPath
              d="M730 499H845V165H985"
              dashed
              label="non-owner"
              labelWidth={78}
              labelX={849}
              labelY={157}
              markerID={arrowID}
            />
            <FlowPath
              d="M730 518H825V305H985"
              label="owner"
              labelWidth={54}
              labelX={851}
              labelY={298}
              markerID={arrowID}
            />
            <FlowPath d="M1130 215V255" markerID={arrowID} />
            <FlowPath d="M1130 355V385" markerID={arrowID} />
            <FlowPath
              d="M1130 423V445H1000V465"
              dashed
              label="S2 / S3 / type drift"
              labelWidth={124}
              labelX={883}
              labelY={439}
              markerID={arrowID}
            />
            <FlowPath
              d="M1130 445H1260V465"
              label="allowed scope"
              labelWidth={92}
              labelX={1211}
              labelY={439}
              markerID={arrowID}
            />
            <FlowPath d="M1260 503V525H1130V545" markerID={arrowID} />
            <FlowPath
              d="M1380 484H1396V404H1265"
              dashed
              label="failed validation → repair"
              labelWidth={146}
              labelX={1245}
              labelY={389}
              markerID={arrowID}
            />
            <FlowPath
              d="M1000 564H965V404H995"
              dashed
              label="missing work → repair"
              labelWidth={130}
              labelX={861}
              labelY={389}
              markerID={arrowID}
            />
            <FlowPath
              d="M1130 583V625H1000V650"
              dashed
              label="S1 / large S0"
              labelWidth={88}
              labelX={956}
              labelY={621}
              markerID={arrowID}
            />
            <FlowPath
              d="M1130 625H1260V650"
              label="completion"
              labelWidth={76}
              labelX={1216}
              labelY={621}
              markerID={arrowID}
            />
            <FlowPath d="M1000 750V770H1100V790" markerID={arrowID} />
            <FlowPath d="M1260 750V770H1160V790" markerID={arrowID} />

            <FlowPath
              d="M730 499H845V95H1410V170H1440"
              label="review ready"
              labelWidth={82}
              labelX={1315}
              labelY={89}
              markerID={arrowID}
            />
            <FlowPath
              d="M1290 809H1410V300H1660V170H1670"
              label="candidate ready"
              labelWidth={96}
              labelX={1416}
              labelY={802}
              markerID={arrowID}
            />
            <FlowPath
              d="M730 518H845V605H1410V400H1440"
              dashed
              label="deferred group"
              labelWidth={96}
              labelX={1310}
              labelY={599}
              markerID={arrowID}
            />

            <FlowPath d="M1542 220V245" markerID={arrowID} />
            <FlowPath d="M1772 220V245" markerID={arrowID} />
            <FlowPath d="M1542 450V475" markerID={arrowID} />
            <FlowPath d="M1772 450V475" markerID={arrowID} />
            <FlowPath
              d="M1645 263H1657V330H1772V350"
              dashed
              label="unknown only"
              labelWidth={86}
              labelX={1659}
              labelY={324}
              markerID={arrowID}
            />
            <FlowPath d="M1772 281V350" dashed markerID={arrowID} />
            <FlowPath d="M1645 493H1657V400H1670" dashed markerID={arrowID} />

            <FlowPath
              d="M320 978H390"
              dashed
              label="promote?"
              labelWidth={66}
              labelX={324}
              labelY={972}
              markerID={arrowID}
            />
            <FlowPath d="M680 980H720" markerID={arrowID} />
            <FlowPath
              d="M320 998H1000"
              emphasis
              label="stored immediately"
              labelWidth={112}
              labelX={782}
              labelY={992}
              markerID={arrowID}
            />
            <FlowPath
              d="M1080 960V875H850V279H730"
              dashed
              emphasis
              label="review prompt"
              labelWidth={90}
              labelX={754}
              labelY={867}
              markerID={arrowID}
            />
            <FlowPath
              d="M1200 960V875H1395V404H1265"
              dashed
              emphasis
              label="implementation prompt"
              labelWidth={130}
              labelX={1260}
              labelY={867}
              markerID={arrowID}
            />
          </g>

          <ActionChip
            detail="PR type · goal · scope · exclusions"
            label="USER · define charter"
            width={230}
            x={70}
            y={100}
          />
          <ActionChip
            detail="Search exact diff + adaptive nudges"
            label="PICOCLAW · review agent"
            width={250}
            x={480}
            y={260}
          />
          <ActionChip
            detail="Triage: in scope · defer · dismiss · revise"
            label="USER · review findings"
            width={250}
            x={480}
            y={480}
          />
          <ActionChip
            detail="Edit only authorized findings; then audit scope"
            label="PICOCLAW · implementation agent"
            width={270}
            x={995}
            y={385}
          />
          <HardScopeStop x={880} y={465} />
          <ActionChip
            detail="Green checks required; failures loop to repair"
            label="GIT / CI · validate candidate"
            width={240}
            x={1140}
            y={465}
          />
          <ActionChip
            detail="Check required work + scope; nudge for more"
            label="PICOCLAW · completion audit"
            width={260}
            x={1000}
            y={545}
          />
          <ActionChip
            detail="All required gates passed"
            label="Candidate authorized"
            width={260}
            x={1030}
            y={790}
          />

          <ActionChip
            detail="Frozen selected findings"
            label="GITHUB · submit review"
            width={205}
            x={1440}
            y={245}
          />
          <ActionChip
            detail="Pinned validated head"
            label="GIT · push branch"
            width={205}
            x={1670}
            y={245}
          />
          <ActionChip
            detail="Frozen deferred group"
            label="GITHUB · create issue"
            width={205}
            x={1440}
            y={475}
          />
          <ActionChip
            detail="Check provider again or unlock after failure"
            label="PICOCLAW · reconcile"
            width={205}
            x={1670}
            y={475}
          />

          <ActionChip
            detail="Applies to review, implementation, or both"
            label="USER · record correction"
            width={280}
            x={40}
            y={960}
          />
          <ActionChip
            detail="Reusable within repository + PR type"
            label="Repository lesson"
            width={200}
            x={720}
            y={960}
          />
          <ActionChip
            detail="One source; audience-specific projections"
            label="Shared facts bundle"
            width={280}
            x={1000}
            y={960}
          />

          {gateSpecs.map((spec) => (
            <GateNode
              instanceID={instanceID}
              key={spec.decisionPoint}
              onSelect={onSelect}
              selected={selectedDecisionPoint === spec.decisionPoint}
              spec={spec}
              workflow={workflows?.[spec.decisionPoint]}
            />
          ))}
        </svg>
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

function StageLegend({ code, label }: { code: string; label: string }) {
  return (
    <span className="bg-muted/50 rounded-md border px-2 py-1">
      <span className="text-foreground font-mono font-semibold">{code}</span>{" "}
      {label}
    </span>
  )
}

function PhasePanel({
  title,
  detail,
  x,
  width,
}: {
  title: string
  detail: string
  x: number
  width: number
}) {
  return (
    <g className="pointer-events-none">
      <rect
        className="fill-muted/30 stroke-border"
        height="780"
        rx="16"
        strokeWidth="1"
        width={width}
        x={x}
        y="85"
      />
      <text
        className="fill-foreground text-xs font-semibold tracking-wider"
        x={x + 20}
        y="34"
      >
        {title}
      </text>
      <text className="fill-muted-foreground text-xs" x={x + 20} y="54">
        {detail}
      </text>
    </g>
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
  const summary = summarizeStages(workflow, spec.width)
  const labelID = `${instanceID}-gate-${spec.number}`
  const activate = () => onSelect(spec.decisionPoint)
  const handleKeyDown = (event: KeyboardEvent<SVGGElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    activate()
  }

  return (
    <g
      aria-labelledby={labelID}
      aria-pressed={selected}
      className="group cursor-pointer outline-none"
      data-decision-point={spec.decisionPoint}
      data-edit-href={`/pull-requests?view=gate-profiles&gate=${spec.decisionPoint}`}
      data-gate-id={spec.decisionPoint}
      data-workflow-configured={workflow ? "true" : "false"}
      onClick={activate}
      onKeyDown={handleKeyDown}
      role="button"
      tabIndex={0}
    >
      <title>
        Gate {spec.number}: {spec.title}. {summary.accessible}
      </title>
      <rect
        className={cn(
          "fill-card stroke-border group-hover:stroke-primary group-focus:stroke-ring transition-colors",
          selected && "fill-primary/10 stroke-primary",
        )}
        height={gateHeight}
        rx="13"
        strokeWidth={selected ? 3 : 2}
        width={spec.width}
        x={spec.x}
        y={spec.y}
      />
      <circle
        className={cn(
          "fill-muted stroke-border group-hover:stroke-primary",
          selected && "fill-primary stroke-primary",
        )}
        cx={spec.x + spec.width - 20}
        cy={spec.y + 20}
        r="12"
        strokeWidth="1"
      />
      <text
        className={cn(
          "fill-muted-foreground text-xs font-bold",
          selected && "fill-primary-foreground",
        )}
        dominantBaseline="middle"
        textAnchor="middle"
        x={spec.x + spec.width - 20}
        y={spec.y + 20}
      >
        {spec.number}
      </text>
      <text
        className="fill-muted-foreground font-mono text-[10px]"
        x={spec.x + 14}
        y={spec.y + 20}
      >
        {spec.decisionPoint}
      </text>
      <text
        className="fill-foreground text-sm font-semibold"
        id={labelID}
        x={spec.x + 14}
        y={spec.y + 45}
      >
        {spec.title}
      </text>
      <text
        className="fill-muted-foreground text-[11px]"
        x={spec.x + 14}
        y={spec.y + 62}
      >
        {spec.detail}
      </text>
      <rect
        className={cn(
          "fill-muted stroke-border",
          summary.fallback && "fill-secondary stroke-primary/50",
          summary.invalid && "fill-destructive/10 stroke-destructive/50",
        )}
        height="18"
        rx="9"
        strokeWidth="1"
        width={spec.width - 28}
        x={spec.x + 14}
        y={spec.y + 72}
      />
      <text
        className={cn(
          "fill-muted-foreground font-mono text-[10px] font-semibold",
          summary.fallback && "fill-primary",
          summary.invalid && "fill-destructive",
        )}
        dominantBaseline="middle"
        textAnchor="middle"
        x={spec.x + spec.width / 2}
        y={spec.y + 81}
      >
        {summary.visual}
      </text>
    </g>
  )
}

function summarizeStages(workflow?: PRLifecycleGateWorkflow, width = 290) {
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
  const visibleLimit = width < 230 ? 2 : 4
  const visibleStages = workflow.stages.slice(0, visibleLimit)
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

function FlowPath({
  d,
  markerID,
  dashed = false,
  emphasis = false,
  label,
  labelX = 0,
  labelY = 0,
  labelWidth = 80,
}: FlowPathProps) {
  return (
    <g>
      <path
        className={cn(
          "stroke-muted-foreground fill-none",
          emphasis && "stroke-primary",
        )}
        d={d}
        markerEnd={`url(#${markerID})`}
        strokeDasharray={dashed ? "7 6" : undefined}
        strokeLinecap="round"
        strokeLinejoin="round"
        strokeWidth="2"
        vectorEffect="non-scaling-stroke"
      />
      {label ? (
        <g>
          <rect
            className="fill-background/90"
            height="18"
            rx="5"
            width={labelWidth}
            x={labelX}
            y={labelY - 13}
          />
          <text
            className={cn(
              "fill-muted-foreground text-[10px] font-medium",
              emphasis && "fill-primary",
            )}
            textAnchor="middle"
            x={labelX + labelWidth / 2}
            y={labelY}
          >
            {label}
          </text>
        </g>
      ) : null}
    </g>
  )
}

function ActionChip({
  label,
  detail,
  x,
  y,
  width,
}: {
  label: string
  detail: string
  x: number
  y: number
  width: number
}) {
  return (
    <g className="pointer-events-none">
      <rect
        className="fill-secondary stroke-border"
        height="38"
        rx="10"
        strokeWidth="1"
        width={width}
        x={x}
        y={y}
      />
      <text
        className="fill-secondary-foreground text-[11px] font-semibold"
        textAnchor="middle"
        x={x + width / 2}
        y={y + 15}
      >
        {label}
      </text>
      <text
        className="fill-muted-foreground text-[9px]"
        textAnchor="middle"
        x={x + width / 2}
        y={y + 29}
      >
        {detail}
      </text>
    </g>
  )
}

function HardScopeStop({ x, y }: { x: number; y: number }) {
  return (
    <g className="pointer-events-none">
      <rect
        className="fill-destructive/10 stroke-destructive/50"
        height="58"
        rx="10"
        strokeDasharray="6 4"
        strokeWidth="2"
        width="240"
        x={x}
        y={y}
      />
      <text
        className="fill-destructive text-[11px] font-bold"
        textAnchor="middle"
        x={x + 120}
        y={y + 17}
      >
        BUILT-IN HARD-SCOPE STOP
      </text>
      <text
        className="fill-destructive text-[9px]"
        textAnchor="middle"
        x={x + 120}
        y={y + 33}
      >
        Not profile-editable; pass is unavailable
      </text>
      <text
        className="fill-destructive text-[9px]"
        textAnchor="middle"
        x={x + 120}
        y={y + 47}
      >
        Remove + defer · revise charter · stop
      </text>
    </g>
  )
}
