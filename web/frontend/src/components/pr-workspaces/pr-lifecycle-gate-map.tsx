import {
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useId,
  useState,
} from "react"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleGateKind,
  type PRLifecycleGateProfile,
  type PRLifecycleGateWorkflow,
  validatePRLifecycleGateWorkflow,
} from "@/api/pr-lifecycle-gate-profiles"
import {
  prLifecycleGateDecisionLabels,
  prLifecycleGateLabels,
} from "@/components/pr-workspaces/pr-lifecycle-gate-catalog"
import { cn } from "@/lib/utils"

interface PRLifecycleGateMapProps {
  selectedDecisionPoint?: PRLifecycleDecisionPoint
  workflows?: PRLifecycleGateProfile["workflows"]
  profileID?: string
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
  condition?: string
}

const gateSpecs = [
  {
    number: 1,
    decisionPoint: "pr.charter.confirm",
    title: prLifecycleGateLabels["pr.charter.confirm"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.charter.confirm"],
    detail: "Checks that the pull request goal and boundaries are approved.",
  },
  {
    number: 2,
    decisionPoint: "pr.charter.reconfirm",
    title: prLifecycleGateLabels["pr.charter.reconfirm"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.charter.reconfirm"],
    detail: "Reconfirms authority after the purpose or scope changes.",
    condition: "Purpose or scope changed",
  },
  {
    number: 3,
    decisionPoint: "pr.review.start",
    title: prLifecycleGateLabels["pr.review.start"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.review.start"],
    detail: "Decides whether AI may review the approved pull request scope.",
  },
  {
    number: 4,
    decisionPoint: "pr.review.complete",
    title: prLifecycleGateLabels["pr.review.complete"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.review.complete"],
    detail: "Checks review coverage and findings before follow-up begins.",
  },
  {
    number: 5,
    decisionPoint: "pr.finding.classify",
    title: prLifecycleGateLabels["pr.finding.classify"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.finding.classify"],
    detail:
      "Resolves whether an ambiguous finding belongs in this pull request.",
    condition: "Finding scope is ambiguous",
  },
  {
    number: 6,
    decisionPoint: "pr.implementation.eligibility",
    title: prLifecycleGateLabels["pr.implementation.eligibility"],
    decisionTitle:
      prLifecycleGateDecisionLabels["pr.implementation.eligibility"],
    detail: "Authorizes fixes on a pull request PicoClaw does not own.",
    condition: "Pull request is non-owned",
  },
  {
    number: 7,
    decisionPoint: "pr.implementation.start",
    title: prLifecycleGateLabels["pr.implementation.start"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.implementation.start"],
    detail: "Decides whether AI may implement the selected findings.",
  },
  {
    number: 8,
    decisionPoint: "pr.implementation.scope",
    title: prLifecycleGateLabels["pr.implementation.scope"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.implementation.scope"],
    detail: "Authorizes large exact or necessary adjacent code after audits.",
    condition: "Candidate work is large exact or necessary adjacent",
  },
  {
    number: 9,
    decisionPoint: "pr.implementation.complete",
    title: prLifecycleGateLabels["pr.implementation.complete"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.implementation.complete"],
    detail: "Confirms the scoped changes are complete and validated.",
  },
  {
    number: 10,
    decisionPoint: "pr.review.publish",
    title: prLifecycleGateLabels["pr.review.publish"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.review.publish"],
    detail: "Approves posting the selected findings to GitHub.",
  },
  {
    number: 11,
    decisionPoint: "pr.implementation.publish",
    title: prLifecycleGateLabels["pr.implementation.publish"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.implementation.publish"],
    detail: "Approves pushing the accepted changes to the pull request branch.",
  },
  {
    number: 12,
    decisionPoint: "pr.deferred.publish",
    title: prLifecycleGateLabels["pr.deferred.publish"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.deferred.publish"],
    detail: "Approves creating GitHub issues for deferred work.",
    condition: "Deferred issue mode asks first",
  },
  {
    number: 13,
    decisionPoint: "pr.correction.promote",
    title: prLifecycleGateLabels["pr.correction.promote"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.correction.promote"],
    detail: "Approves promoting a correction into repository guidance.",
    condition: "A correction may become guidance",
  },
  {
    number: 14,
    decisionPoint: "pr.publication.reconcile",
    title: prLifecycleGateLabels["pr.publication.reconcile"],
    decisionTitle: prLifecycleGateDecisionLabels["pr.publication.reconcile"],
    detail: "Approves checking GitHub before retrying an uncertain write.",
    condition: "A GitHub write result is unknown",
  },
] as const satisfies readonly GateSpec[]

type LifecycleFlowView = "review" | "implementation"

const implementationOnlyDecisionPoints = new Set<PRLifecycleDecisionPoint>([
  "pr.implementation.eligibility",
  "pr.implementation.start",
  "pr.implementation.scope",
  "pr.implementation.complete",
  "pr.implementation.publish",
])

const reviewOnlyDecisionPoints = new Set<PRLifecycleDecisionPoint>([
  "pr.charter.confirm",
  "pr.review.start",
  "pr.review.complete",
  "pr.finding.classify",
  "pr.review.publish",
])

function flowViewForDecisionPoint(
  decisionPoint: PRLifecycleDecisionPoint | undefined,
): LifecycleFlowView | undefined {
  if (!decisionPoint) return undefined
  return implementationOnlyDecisionPoints.has(decisionPoint)
    ? "implementation"
    : reviewOnlyDecisionPoints.has(decisionPoint)
      ? "review"
      : undefined
}

const flowGateIndexes = {
  review: [0, 1, 2, 3, 4, 9, 11, 12, 13],
  implementation: [1, 5, 6, 7, 8, 10, 11, 12, 13],
} as const satisfies Record<LifecycleFlowView, readonly number[]>

const flowSummaries = {
  review: [
    "GitHub review work is tracked and bounded by a confirmed PR charter.",
    "AI reviews the approved scope before findings are classified and accepted.",
    "Findings become in-scope, deferred, or dismissed without coupling review publication to implementation.",
    "In-scope findings may be published, implemented, or both; deferred work may become GitHub issues.",
  ],
  implementation: [
    "Selected in-scope findings enter a separately authorized implementation flow.",
    "AI changes the candidate, then an isolated audit checks every changed hunk against the charter.",
    "Hard scope violations stop before validation; allowed code is validated and audited for completion.",
    "Incomplete work loops back to implementation, while accepted work may be pushed to the pull request branch.",
  ],
} as const satisfies Record<LifecycleFlowView, readonly string[]>

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
  profileID,
  profileName,
  onSelect,
  className,
}: PRLifecycleGateMapProps) {
  const instanceID = useId().replaceAll(":", "")
  const titleID = `${instanceID}-title`
  const descriptionID = `${instanceID}-description`
  const [activeView, setActiveView] = useState<LifecycleFlowView>(
    () => flowViewForDecisionPoint(selectedDecisionPoint) ?? "review",
  )
  useEffect(() => {
    const owningView = flowViewForDecisionPoint(selectedDecisionPoint)
    if (owningView) setActiveView(owningView)
  }, [selectedDecisionPoint])
  const gate = (index: number, compact = false) => (
    <GateNode
      compact={compact}
      instanceID={instanceID}
      onSelect={onSelect}
      selected={selectedDecisionPoint === gateSpecs[index].decisionPoint}
      spec={gateSpecs[index]}
      workflow={workflows?.[gateSpecs[index].decisionPoint]}
      profileID={profileID}
    />
  )

  return (
    <section
      aria-labelledby={titleID}
      className={cn(
        "bg-card min-w-0 overflow-hidden rounded-xl border",
        className,
      )}
    >
      <div className="flex flex-col gap-3 px-4 py-4 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h2 id={titleID} className="text-sm font-semibold">
              PR lifecycle gate flow
            </h2>
            {profileName ? (
              <span className="bg-muted/50 text-muted-foreground min-w-0 rounded-md border px-2 py-0.5 text-xs [overflow-wrap:anywhere]">
                Profile · {profileName}
              </span>
            ) : null}
          </div>
          <p
            id={descriptionID}
            className="text-muted-foreground mt-1 max-w-3xl text-xs"
          >
            Review and implementation have separate flows. Each box is either an
            action or an editable gate; labeled arrows appear only where the
            flow branches.
          </p>
        </div>
        <div className="max-w-3xl min-w-0 space-y-1.5">
          <div
            aria-label="Diagram legend"
            className="text-muted-foreground flex flex-wrap gap-1.5 text-xs"
            role="group"
          >
            <Legend label="Action" variant="action" />
            <Legend label="Editable gate" variant="gate" />
            <Legend label="Locked safeguard" variant="required" />
          </div>
          <GateFormatLegend />
        </div>
      </div>

      <div
        className="bg-muted/10 w-full overflow-hidden border-t"
        data-gate-map-viewport
      >
        <div
          aria-describedby={descriptionID}
          className="w-full min-w-0 space-y-4 p-4"
          data-gate-map-content
          role="group"
        >
          <FlowViewTabs
            activeView={activeView}
            instanceID={instanceID}
            onChange={setActiveView}
          />

          <ActiveFlowSummary activeView={activeView} />

          {activeView === "review" ? (
            <div
              aria-labelledby={`${instanceID}-review-tab`}
              data-flow-view="review"
              id={`${instanceID}-review-panel`}
              role="tabpanel"
              tabIndex={0}
            >
              <div className="space-y-4">
                <FlowBand
                  eyebrow="REVIEW WORKFLOW"
                  number="01"
                  title="Request → scope → review"
                >
                  <ActionNode
                    detail="GitHub assigns the pull request to a reviewer."
                    label="Request PR review"
                  />
                  <FlowConnector />
                  <ActionNode
                    detail="A user or configured automation opens it in PicoClaw."
                    label="Track pull request"
                  />
                  <FlowConnector />
                  <ActionNode
                    detail="Record the goal, included work, and exclusions."
                    label="Define purpose and scope"
                  />
                  <FlowSplit
                    columns={2}
                    label="Charter approval"
                    splitID="charter-approval"
                  >
                    <BranchPath label="First scope" target="charter-first">
                      {gate(0, true)}
                    </BranchPath>
                    <BranchPath label="Revised" target="charter-revised">
                      {gate(1, true)}
                    </BranchPath>
                  </FlowSplit>
                  <FlowConnector />
                  {gate(2)}
                  <FlowConnector />
                  <ActionNode
                    detail="AI checks changed areas against the approved scope."
                    label="Review pull request"
                  />
                  <FlowConnector />
                  {gate(3)}
                </FlowBand>

                <FlowBand
                  eyebrow="REVIEW WORKFLOW"
                  number="02"
                  title="Classify → disposition"
                >
                  <ActionNode
                    detail="Compare each finding with the confirmed charter."
                    label="Assess finding scope"
                  />
                  <FlowSplit
                    columns={2}
                    label="Finding classification"
                    splitID="finding-classification"
                  >
                    <BranchPath label="Clear" target="finding-clear">
                      <ActionNode
                        compact
                        detail="Apply the deterministic charter and PR-type result."
                        label="Classify automatically"
                      />
                    </BranchPath>
                    <BranchPath label="Ambiguous" target="finding-ambiguous">
                      {gate(4, true)}
                    </BranchPath>
                  </FlowSplit>
                  <FlowSplit
                    columns={3}
                    label="Finding disposition"
                    splitID="finding-disposition"
                  >
                    <BranchPath label="In scope" target="finding-in-scope">
                      <ActionNode
                        compact
                        detail="Make it available to review publication and implementation."
                        label="Keep in pull request"
                      />
                    </BranchPath>
                    <BranchPath label="Defer" target="finding-defer">
                      <ActionNode
                        compact
                        detail="Group related follow-up work outside this pull request."
                        label="Group deferred findings"
                      />
                      <FlowConnector />
                      {gate(11)}
                      <FlowConnector />
                      <ActionNode
                        compact
                        detail="Create one GitHub issue for each approved group."
                        label="Create follow-up issues"
                      />
                    </BranchPath>
                    <BranchPath label="Dismiss" target="finding-dismiss">
                      <ActionNode
                        compact
                        detail="Close the finding without publishing or implementing it."
                        label="Dismiss finding"
                      />
                    </BranchPath>
                  </FlowSplit>
                </FlowBand>

                <FlowLaneSection
                  columns={2}
                  eyebrow="REVIEW WORKFLOW"
                  number="03"
                  title="Use in-scope findings"
                  titleID={`${instanceID}-review-use-title`}
                >
                  <FlowLane title="Review publication">
                    <ActionNode
                      compact
                      detail="Choose in-scope findings for the GitHub review."
                      label="Select review findings"
                    />
                    <FlowConnector />
                    {gate(9, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Post the approved findings to GitHub."
                      label="Publish GitHub review"
                    />
                  </FlowLane>
                  <FlowLane title="Implementation handoff">
                    <ActionNode
                      compact
                      detail="Choose findings that implementation will fix; review selection remains independent."
                      label="Select implementation findings"
                    />
                  </FlowLane>
                </FlowLaneSection>

                <FlowLaneSection
                  columns={2}
                  conditional
                  eyebrow="REVIEW WORKFLOW"
                  number="04"
                  title="Review follow-up gates"
                  titleID={`${instanceID}-review-follow-up-title`}
                >
                  <FlowLane title="User correction">
                    <ActionNode
                      compact
                      detail="Store feedback for review, implementation, or both."
                      label="Record user correction"
                    />
                    <FlowConnector />
                    {gate(12, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Add the approved correction to repository guidance."
                      label="Save repository guidance"
                    />
                  </FlowLane>
                  <FlowLane title="Unknown GitHub result">
                    <ActionNode
                      compact
                      detail="A GitHub write may have succeeded without confirmation."
                      label="Receive unknown result"
                    />
                    <FlowConnector />
                    {gate(13, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Re-observe GitHub or allow a safe retry."
                      label="Resolve publication result"
                    />
                  </FlowLane>
                </FlowLaneSection>
              </div>
            </div>
          ) : (
            <div
              aria-labelledby={`${instanceID}-implementation-tab`}
              data-flow-view="implementation"
              id={`${instanceID}-implementation-panel`}
              role="tabpanel"
              tabIndex={0}
            >
              <div className="space-y-4">
                <FlowBand
                  eyebrow="IMPLEMENTATION WORKFLOW"
                  number="01"
                  title="Authorize → implement → audit"
                >
                  <ActionNode
                    detail="Bring in the findings chosen for this pull request."
                    label="Load selected findings"
                  />
                  <FlowConnector />
                  {gate(5)}
                  <FlowConnector />
                  {gate(6)}
                  <FlowConnector />
                  <ActionNode
                    detail="AI changes only code required by the selected findings."
                    label="Implement selected fixes"
                  />
                  <FlowConnector />
                  <ActionNode
                    detail="Classify every changed hunk against scope, type, and size."
                    label="Audit candidate scope"
                  />
                  <FlowSplit
                    columns={2}
                    label="Candidate scope result"
                    splitID="candidate-scope-result"
                  >
                    <BranchPath label="Safe path" target="scope-safe">
                      <ActionNode
                        compact
                        detail="Run tests and every required project check."
                        label="Validate changes"
                      />
                      <FlowSplit
                        columns={2}
                        label="Validation result"
                        splitID="validation-result"
                        stacked
                      >
                        <BranchPath label="Passed" target="validation-passed">
                          <ActionNode
                            compact
                            detail="AI checks missing work and candidate scope drift."
                            label="Audit completion"
                          />
                          <FlowSplit
                            columns={2}
                            label="Completion audit result"
                            splitID="completion-audit-result"
                            stacked
                          >
                            <BranchPath
                              label="No gaps"
                              target="completion-done"
                            >
                              <ActionNode
                                compact
                                detail="Check completion findings against the final candidate scope."
                                label="Check final scope"
                              />
                              <FlowSplit
                                columns={2}
                                label="Final scope result"
                                splitID="final-scope-result"
                                stacked
                              >
                                <BranchPath
                                  label="Allowed"
                                  target="final-scope-allowed"
                                >
                                  {gate(7, true)}
                                  <FlowConnector />
                                  {gate(8, true)}
                                  <FlowConnector />
                                  {gate(10, true)}
                                  <FlowConnector />
                                  <ActionNode
                                    compact
                                    detail="Push the accepted candidate to the pull request branch."
                                    label="Push accepted changes"
                                  />
                                </BranchPath>
                                <BranchPath
                                  label="Hard stop"
                                  target="final-scope-hard"
                                >
                                  <ActionNode
                                    compact
                                    detail="Use the locked scope resolution before acceptance or publication."
                                    label="Return to hard-scope gate"
                                    loopTarget="scope-hard-stop"
                                  />
                                </BranchPath>
                              </FlowSplit>
                            </BranchPath>
                            <BranchPath
                              label="More work"
                              target="completion-more"
                            >
                              <ActionNode
                                compact
                                detail="Repair missing work or drift, then validate and audit again."
                                label="Resume implementation"
                              />
                            </BranchPath>
                          </FlowSplit>
                        </BranchPath>
                        <BranchPath label="Failed" target="validation-failed">
                          <ActionNode
                            compact
                            detail="Fix failed checks, then validate the candidate again."
                            label="Repair validation failures"
                          />
                        </BranchPath>
                      </FlowSplit>
                    </BranchPath>
                    <BranchPath label="Hard stop" target="scope-hard-stop">
                      <RequiredGateNode />
                      <FlowSplit
                        columns={2}
                        label="Hard scope resolution"
                        splitID="hard-scope-resolution"
                        stacked
                      >
                        <BranchPath label="Remove code" target="scope-remove">
                          <ActionNode
                            compact
                            detail="Remove candidate code, track follow-up, then audit scope again."
                            label="Remove and defer"
                          />
                        </BranchPath>
                        <BranchPath label="Revise scope" target="scope-revise">
                          <ActionNode
                            compact
                            detail="Change the PR charter to authorize the candidate."
                            label="Revise PR charter"
                          />
                          <FlowConnector />
                          {gate(1, true)}
                        </BranchPath>
                        <BranchPath label="Stop" target="scope-stop">
                          <ActionNode
                            compact
                            detail="Leave the candidate blocked and end implementation."
                            label="Stop implementation"
                          />
                        </BranchPath>
                      </FlowSplit>
                    </BranchPath>
                  </FlowSplit>
                </FlowBand>

                <FlowLaneSection
                  columns={3}
                  conditional
                  eyebrow="IMPLEMENTATION WORKFLOW"
                  number="02"
                  title="Implementation follow-up gates"
                  titleID={`${instanceID}-implementation-follow-up-title`}
                >
                  <FlowLane title="Deferred audit finding">
                    <ActionNode
                      compact
                      detail="Group follow-up work found during completion audit."
                      label="Group deferred findings"
                    />
                    <FlowConnector />
                    {gate(11, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Create one GitHub issue for each approved group."
                      label="Create follow-up issues"
                    />
                  </FlowLane>
                  <FlowLane title="User correction">
                    <ActionNode
                      compact
                      detail="Store feedback for review, implementation, or both."
                      label="Record user correction"
                    />
                    <FlowConnector />
                    {gate(12, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Add the approved correction to repository guidance."
                      label="Save repository guidance"
                    />
                  </FlowLane>
                  <FlowLane title="Unknown GitHub result">
                    <ActionNode
                      compact
                      detail="A push or issue write may lack confirmation."
                      label="Receive unknown result"
                    />
                    <FlowConnector />
                    {gate(13, true)}
                    <FlowConnector />
                    <ActionNode
                      compact
                      detail="Re-observe GitHub or allow a safe retry."
                      label="Resolve publication result"
                    />
                  </FlowLane>
                </FlowLaneSection>
              </div>
            </div>
          )}
        </div>
      </div>

      <ol aria-label={`${activeView} workflow gates`} className="sr-only">
        {flowGateIndexes[activeView].map((index) => {
          const spec = gateSpecs[index]
          return (
            <li key={spec.decisionPoint}>
              Gate {spec.number}: {spec.decisionTitle}. Editor: {spec.title}.{" "}
              {spec.detail}
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

function ActiveFlowSummary({ activeView }: { activeView: LifecycleFlowView }) {
  return (
    <ol aria-label={`${activeView} workflow ordered flow`} className="sr-only">
      {flowSummaries[activeView].map((summary) => (
        <li key={summary}>{summary}</li>
      ))}
    </ol>
  )
}

function FlowViewTabs({
  activeView,
  instanceID,
  onChange,
}: {
  activeView: LifecycleFlowView
  instanceID: string
  onChange: (view: LifecycleFlowView) => void
}) {
  const views: Array<{
    value: LifecycleFlowView
    title: string
    description: string
  }> = [
    {
      value: "review",
      title: "Review workflow",
      description: "Review changes and choose what happens to findings.",
    },
    {
      value: "implementation",
      title: "Implementation workflow",
      description: "Fix selected findings, enforce scope, and publish code.",
    },
  ]
  const selectAndFocus = (view: LifecycleFlowView) => {
    onChange(view)
    window.requestAnimationFrame(() => {
      document
        .getElementById(`${instanceID}-${view}-tab`)
        ?.focus({ preventScroll: true })
    })
  }
  const handleKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    view: LifecycleFlowView,
  ) => {
    let next: LifecycleFlowView | undefined
    if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      next = view === "review" ? "implementation" : "review"
    } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      next = view === "review" ? "implementation" : "review"
    } else if (event.key === "Home") {
      next = "review"
    } else if (event.key === "End") {
      next = "implementation"
    }
    if (!next) return
    event.preventDefault()
    selectAndFocus(next)
  }

  return (
    <div
      aria-label="PR workflow view"
      className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2"
      role="tablist"
    >
      {views.map(({ value, title, description }) => {
        const selected = value === activeView
        return (
          <button
            aria-controls={`${instanceID}-${value}-panel`}
            aria-selected={selected}
            className={cn(
              "focus-visible:ring-ring flex min-w-0 flex-col rounded-lg border px-3 py-2 text-left transition-colors outline-none focus-visible:ring-2",
              selected
                ? "border-primary bg-primary/10"
                : "bg-background hover:bg-muted/50",
            )}
            data-flow-view-tab={value}
            id={`${instanceID}-${value}-tab`}
            key={value}
            onClick={() => onChange(value)}
            onKeyDown={(event) => handleKeyDown(event, value)}
            role="tab"
            tabIndex={selected ? 0 : -1}
            type="button"
          >
            <strong className="text-xs">{title}</strong>
            <span className="text-muted-foreground mt-1 text-[11px] leading-snug">
              {description}
            </span>
          </button>
        )
      })}
    </div>
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
      <div className="mx-auto flex w-full max-w-3xl min-w-0 flex-col items-stretch gap-2">
        {children}
      </div>
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

function ActionNode({
  label,
  detail,
  compact = false,
  loopTarget,
}: {
  label: string
  detail: string
  compact?: boolean
  loopTarget?: string
}) {
  return (
    <div
      className={cn(
        "bg-secondary flex min-h-20 w-full min-w-0 flex-col rounded-xl border p-3",
        compact && "min-h-16",
      )}
      data-flow-kind="action"
      data-flow-loop-target={loopTarget}
    >
      <span className="text-muted-foreground text-[9px] font-bold tracking-wider">
        ACTION
      </span>
      <strong className="mt-1 text-xs leading-snug">{label}</strong>
      <span
        className="text-muted-foreground mt-2 text-[11px] leading-snug"
        data-flow-description
      >
        {detail}
      </span>
    </div>
  )
}

function FlowConnector() {
  return (
    <div
      aria-hidden="true"
      className="text-primary flex h-9 w-full min-w-0 shrink-0 items-center justify-center text-xl leading-none"
      data-flow-edge="linear"
    >
      ↓
    </div>
  )
}

function FlowSplit({
  splitID,
  label,
  columns,
  children,
  stacked = false,
}: {
  splitID: string
  label: string
  columns: 2 | 3
  children: ReactNode
  stacked?: boolean
}) {
  return (
    <div
      aria-label={`${label} branches`}
      className="w-full min-w-0"
      data-flow-branch={splitID}
      role="group"
    >
      <div
        aria-hidden="true"
        className="text-muted-foreground mb-1 flex min-w-0 items-center gap-2 text-[9px] font-bold tracking-wider uppercase"
        data-flow-split-label
      >
        <span className="bg-border h-px min-w-3 flex-1" />
        <span className="min-w-0 text-center">{label}</span>
        <span className="bg-border h-px min-w-3 flex-1" />
      </div>
      <div
        className={cn(
          "grid min-w-0 grid-cols-1 gap-3",
          !stacked && columns === 2 && "sm:grid-cols-2",
          !stacked && columns === 3 && "lg:grid-cols-3",
        )}
        data-flow-layout={stacked ? "stacked" : "columns"}
      >
        {children}
      </div>
    </div>
  )
}

function BranchPath({
  label,
  target,
  children,
}: {
  label: string
  target: string
  children: ReactNode
}) {
  return (
    <div
      aria-label={`${label} branch`}
      className="border-primary/25 flex min-w-0 flex-col rounded-l-lg border-l-2 pl-2"
      data-flow-branch-path={label}
      data-flow-branch-rail
      role="group"
    >
      <div
        className="flex h-14 min-w-0 flex-col items-center justify-center text-center"
        data-flow-branch-edge={label}
        data-flow-target={target}
      >
        <span
          className="bg-background text-foreground rounded border px-2 py-0.5 text-[10px] font-semibold"
          data-flow-branch-label
        >
          {label}
        </span>
        <span className="text-primary mt-1 text-lg leading-none" aria-hidden>
          ↓
        </span>
      </div>
      <div
        className="flex min-w-0 flex-1 flex-col gap-2"
        data-flow-branch-target={target}
      >
        {children}
      </div>
    </div>
  )
}

function FlowLaneSection({
  columns,
  conditional = false,
  eyebrow,
  number,
  title,
  titleID,
  children,
}: {
  columns: 2 | 3
  conditional?: boolean
  eyebrow: string
  number: string
  title: string
  titleID: string
  children: ReactNode
}) {
  return (
    <section
      aria-labelledby={titleID}
      className={cn(
        "bg-background/60 rounded-xl border p-3",
        conditional && "border-dashed",
      )}
    >
      <FlowHeading
        eyebrow={`${eyebrow}${conditional ? " · CONDITIONAL" : ""}`}
        number={number}
        title={title}
        titleID={titleID}
      />
      {conditional ? (
        <p className="text-muted-foreground mt-1 text-[11px] leading-snug">
          These connected paths appear only when their starting condition
          occurs.
        </p>
      ) : null}
      <div
        className={cn(
          "mx-auto mt-3 grid w-full max-w-3xl min-w-0 grid-cols-1 gap-3",
          columns === 2 && "sm:grid-cols-2",
          columns === 3 && "lg:grid-cols-3",
        )}
      >
        {children}
      </div>
    </section>
  )
}

function FlowLane({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section
      aria-label={`${title} flow`}
      className="bg-muted/10 flex min-w-0 flex-col rounded-xl border p-2.5"
      data-flow-lane={title}
    >
      <h4 className="text-muted-foreground mb-2 text-[10px] font-bold tracking-wider uppercase">
        {title}
      </h4>
      <div className="flex min-w-0 flex-1 flex-col gap-2">{children}</div>
    </section>
  )
}

function RequiredGateNode() {
  return (
    <div
      aria-label="Resolve candidate code outside the confirmed charter or PR type"
      className="border-destructive/70 bg-destructive/5 flex min-h-28 w-full min-w-0 flex-col rounded-xl border-2 p-3 shadow-sm"
      data-flow-kind="gate"
      data-required-gate="hard-scope"
      role="group"
    >
      <span className="flex w-full items-start justify-between gap-2">
        <span className="text-destructive text-[9px] font-bold tracking-wider">
          REQUIRED USER GATE
        </span>
        <span className="border-destructive/40 text-destructive rounded border px-1.5 py-0.5 text-[8px] font-bold tracking-wider uppercase">
          Locked
        </span>
      </span>
      <strong className="mt-1 text-xs leading-snug">Resolve hard scope</strong>
      <span className="text-destructive mt-1 text-[9px] leading-snug font-semibold">
        WHEN · Candidate code is S2, S3, or PR-type incompatible
      </span>
      <span
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
        data-gate-description
      >
        Stops before validation and requires removal, charter revision, or stop.
      </span>
      <span className="text-muted-foreground mt-auto pt-2 text-[9px] font-bold tracking-wider uppercase">
        User · fixed safeguard
      </span>
    </div>
  )
}

function GateNode({
  spec,
  workflow,
  profileID,
  selected,
  compact,
  instanceID,
  onSelect,
}: {
  spec: GateSpec
  workflow?: PRLifecycleGateWorkflow
  profileID?: string
  selected: boolean
  compact: boolean
  instanceID: string
  onSelect: (decisionPoint: PRLifecycleDecisionPoint) => void
}) {
  const format = summarizeGateFormat(workflow, spec.decisionPoint)
  const descriptionID = `${instanceID}-gate-${spec.number}-description`
  const conditionID = `${instanceID}-gate-${spec.number}-condition`
  const formatID = `${instanceID}-gate-${spec.number}-format`
  const activate = () => onSelect(spec.decisionPoint)
  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key !== "Enter" && event.key !== " ") return
    event.preventDefault()
    activate()
  }

  return (
    <button
      aria-describedby={`${descriptionID}${spec.condition ? ` ${conditionID}` : ""} ${formatID}`}
      aria-expanded={selected}
      aria-haspopup="dialog"
      aria-label={spec.decisionTitle}
      aria-pressed={selected}
      className={cn(
        "bg-primary/5 border-primary/60 hover:bg-primary/10 hover:border-primary focus-visible:ring-ring relative flex min-h-28 w-full min-w-0 flex-col rounded-xl border-2 p-3 text-left shadow-sm transition-colors outline-none focus-visible:ring-2",
        compact && "min-h-24 p-2.5",
        selected && "bg-primary/10 border-primary ring-primary/30 ring-2",
      )}
      data-decision-point={spec.decisionPoint}
      data-decision-title={spec.decisionTitle}
      data-edit-href={gateEditorHref(profileID, spec.decisionPoint)}
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
      {spec.condition ? (
        <span
          className="text-primary mt-1 text-[9px] leading-snug font-semibold"
          data-gate-condition
          id={conditionID}
        >
          WHEN · {spec.condition}
        </span>
      ) : null}
      <span
        className="text-muted-foreground mt-1 text-[10px] leading-tight"
        data-gate-description
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
        OPEN SETTINGS →
      </span>
      <span className="sr-only" id={formatID}>
        {format.accessible}
      </span>
    </button>
  )
}

function gateEditorHref(
  profileID: string | undefined,
  decisionPoint: PRLifecycleDecisionPoint,
): string {
  const profile = profileID ? `&profile=${encodeURIComponent(profileID)}` : ""
  return `/pull-requests?view=gate-profiles${profile}&gate=${decisionPoint}`
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
  variant: "action" | "gate" | "required"
}) {
  return (
    <span
      className={cn(
        "rounded-md border px-2 py-1",
        variant === "action" && "bg-secondary",
        variant === "gate" && "bg-primary/10 border-primary",
        variant === "required" &&
          "bg-destructive/5 border-destructive/60 text-destructive",
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
      role="group"
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
