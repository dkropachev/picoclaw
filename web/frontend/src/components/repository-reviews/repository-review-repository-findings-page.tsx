import {
  IconAlertTriangle,
  IconChecks,
  IconExternalLink,
  IconSparkles,
} from "@tabler/icons-react"
import { useInfiniteQuery, useMutation } from "@tanstack/react-query"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type RepositoryReviewRepositoryFindingSummary,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationRepositoryFinding,
  listRepositoryReviewAutomationRepositoryFindingsPage,
  reserveRepositoryReviewValidations,
  syncRepositoryReviewFinding,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionSelectionState,
} from "@/components/collection"
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
import { Textarea } from "@/components/ui/textarea"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"

import {
  repositoryReviewAutomationIsActive,
  useRepositoryReviewFindingHealth,
} from "./repository-review-finding-health"
import {
  repositoryFindingAttentionLabel,
  repositoryFindingIssueLabel,
  repositoryFindingLifecycleLabel,
  repositoryFindingResolutionActionLabel,
  repositoryFindingResolutionLabel,
} from "./repository-review-finding-labels"
import { createRepositoryReviewGenerationID } from "./repository-review-generation"
import {
  type RepositoryReviewCollectionSearch,
  repositoryReviewRepositoryFindingsDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

export function RepositoryReviewRepositoryFindingsPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenFinding,
  onOpenIncompleteFindings,
  onGenerated,
}: {
  automationID: string
  search: RepositoryReviewCollectionSearch
  onSearchChange: (next: CollectionRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenFinding: (findingID: string) => void
  onOpenIncompleteFindings?: () => void
  onGenerated: (generationID: string) => void
}) {
  const [instructionsOpen, setInstructionsOpen] = useState(false)
  const [customInstructions, setCustomInstructions] = useState("")
  const [generationSelection, setGenerationSelection] = useState<string[]>([])
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewRepositoryFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: [
      "repository-review-repository-findings",
      automationID,
      activeQuery,
    ],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewAutomationRepositoryFindingsPage(
        automationID,
        {
          query: activeQuery,
          cursor: pageParam || undefined,
          limit: 50,
        },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
    refetchInterval: (current) =>
      current.state.data?.pages.some(
        (page) =>
          repositoryReviewAutomationIsActive(page.automation) ||
          page.repository_findings.some((finding) =>
            new Set(["pending", "running"]).has(finding.validation_state),
          ),
      )
        ? 2_000
        : false,
  })
  const pages = query.data?.pages
  const firstPage = pages?.[0]
  const healthQuery = useRepositoryReviewFindingHealth(
    automationID,
    firstPage?.automation,
  )
  const healthUpdatedAt = useRef("")
  useEffect(() => {
    const next = healthQuery.data?.updated_at ?? ""
    const previous = healthUpdatedAt.current
    healthUpdatedAt.current = next
    if (!previous || !next || previous === next) return
    void query.refetch()
  }, [healthQuery.data?.updated_at, query])
  const findings = useMemo(
    () => pages?.flatMap((page) => page.repository_findings) ?? [],
    [pages],
  )
  const findingsByID = useMemo(
    () => new Map(findings.map((finding) => [finding.id, finding])),
    [findings],
  )
  const issueSyncs = useRef(new Map<string, Promise<void>>())
  const synchronizeIssue = useCallback(
    (repositoryFindingID: string) => {
      const existing = issueSyncs.current.get(repositoryFindingID)
      if (existing) return existing
      const pending = syncRepositoryReviewFinding(
        automationID,
        repositoryFindingID,
      )
        .then(() => undefined)
        .catch((error) => {
          issueSyncs.current.delete(repositoryFindingID)
          throw error
        })
      issueSyncs.current.set(repositoryFindingID, pending)
      return pending
    },
    [automationID],
  )
  useEffect(() => {
    const stale = findings.filter(
      (finding) =>
        Boolean(finding.issue.url) &&
        !finding.issue.conflict &&
        !issueSyncs.current.has(finding.id) &&
        (!finding.issue.snapshot_at ||
          Date.now() - new Date(finding.issue.snapshot_at).valueOf() >=
            15 * 60 * 1_000),
    )
    if (stale.length === 0) return
    void (async () => {
      for (let index = 0; index < stale.length; index += 4) {
        await Promise.all(
          stale
            .slice(index, index + 4)
            .map((finding) => synchronizeIssue(finding.id)),
        )
      }
      await query.refetch()
    })().catch(() => undefined)
  }, [findings, query, synchronizeIssue])
  const loadedDraftEligibility = useMemo(
    () =>
      new Map(
        findings.map((finding) => [
          finding.id,
          repositoryFindingCanBeDrafted(finding),
        ]),
      ),
    [findings],
  )
  const [rememberedDraftEligibility, setRememberedDraftEligibility] = useState(
    () => new Map<string, boolean>(),
  )
  useEffect(() => {
    if (loadedDraftEligibility.size === 0) return
    setRememberedDraftEligibility((current) => {
      const next = new Map(current)
      for (const [findingID, eligible] of loadedDraftEligibility) {
        next.set(findingID, eligible)
      }
      return next
    })
  }, [loadedDraftEligibility])
  const generation = useMutation({
    mutationFn: async ({
      selectedIDs,
      custom,
    }: {
      selectedIDs: string[]
      custom: boolean
    }) => {
      const details = await Promise.all(
        selectedIDs.map((findingID) =>
          getRepositoryReviewAutomationRepositoryFinding(
            automationID,
            findingID,
          ),
        ),
      )
      if (
        details.some((detail) => detail.capabilities?.can_generate === false)
      ) {
        throw new Error(
          "One or more selected repository findings are not eligible for issue drafting.",
        )
      }
      if (
        details.some(
          (detail, index) =>
            !detail.repository_finding ||
            detail.repository_finding.id !== selectedIDs[index],
        )
      ) {
        throw new Error(
          "One or more selected repository findings no longer exist.",
        )
      }
      const actionFindingIDs = details.map(
        (detail) => detail.action_finding?.id || detail.finding.id,
      )
      if (new Set(actionFindingIDs).size !== actionFindingIDs.length) {
        throw new Error(
          "Selected repository findings do not have unique issue actions.",
        )
      }
      const generationID = createRepositoryReviewGenerationID()
      const response = await generateRepositoryReviewIssues(automationID, {
        generation_id: generationID,
        finding_ids: actionFindingIDs,
        instructions_mode: custom ? "custom" : "default",
        ...(custom ? { instructions: customInstructions.trim() } : {}),
      })
      return { generationID, response }
    },
    onSuccess: ({ generationID, response }) => {
      const outcomes = response.results ?? []
      const successes = outcomes.filter((outcome) => outcome.success).length
      const hasSavedAttempt =
        (response.issues?.length ?? 0) > 0 ||
        outcomes.some((outcome) => Boolean(outcome.draft_id))
      if (outcomes.length > 0 && successes === 0 && !hasSavedAttempt) {
        toast.error(
          outcomes[0]?.message || "No selected finding could be drafted.",
        )
        return
      }
      setInstructionsOpen(false)
      if (successes === 0) {
        toast.error(
          "Issue drafting failed. Open the saved attempts to inspect and retry them.",
        )
      } else if (successes < outcomes.length) {
        toast.warning(
          `${successes} of ${outcomes.length} issue previews were drafted. Review individual outcomes.`,
        )
      } else {
        toast.success(
          "Issue preview generation finished. Review individual outcomes before posting.",
        )
      }
      onGenerated(generationID)
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Generation failed.",
      ),
  })
  const validation = useMutation({
    mutationFn: async ({
      selectedIDs,
    }: {
      selectedIDs: string[]
      clearSelection: () => void
    }) => {
      const details = await Promise.all(
        selectedIDs.map((findingID) =>
          getRepositoryReviewAutomationRepositoryFinding(
            automationID,
            findingID,
          ),
        ),
      )
      const selected = details.flatMap((detail) =>
        detail.repository_finding ? [detail.repository_finding] : [],
      )
      if (selected.length !== selectedIDs.length) {
        throw new Error(
          "One or more selected repository findings no longer exist.",
        )
      }
      if (selected.some((finding) => finding.issue.conflict)) {
        throw new Error(
          "Resolve manual GitHub association conflicts before checking for a fix.",
        )
      }
      await Promise.all(
        selected
          .filter(
            (finding) =>
              finding.issue.url &&
              (!finding.issue.snapshot_at ||
                Date.now() - new Date(finding.issue.snapshot_at).valueOf() >=
                  15 * 60 * 1_000),
          )
          .map((finding) => synchronizeIssue(finding.id)),
      )
      return reserveRepositoryReviewValidations(automationID, selectedIDs)
    },
    onSuccess: async (_response, variables) => {
      variables.clearSelection()
      await query.refetch()
      toast.success("Selected fix checks queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Fix checks could not be queued.",
      )
      void query.refetch()
    },
  })
  const definition = useMemo<
    CollectionDefinition<RepositoryReviewRepositoryFindingSummary>
  >(
    () => ({
      key: `repository-review-repository-findings:${automationID}`,
      title: "Repository findings",
      defaultQuery: repositoryReviewRepositoryFindingsDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      tableLayout: "fixed",
      getItemID: (finding) => finding.id,
      getItemLabel: (finding) => finding.canonical_title,
      getItemIdentity: (finding) => {
        const metadata = `${latestLocation(finding) || "Cross-commit diagnosis"} · ${finding.canonical_severity} · ${repositoryFindingLifecycleLabel(finding.lifecycle)} · Updated ${formatCompactDate(finding.updated_at)}`
        return {
          title: finding.canonical_title,
          description: occurrenceIdentity(finding),
          metadata,
        }
      },
      columns: [
        {
          id: "severity",
          header: "Severity",
          cell: (finding) => finding.canonical_severity,
          className: "w-20 max-w-20 px-2 text-xs",
          headerClassName: "w-20 max-w-20 px-2",
        },
        {
          id: "occurrences",
          header: "Occurrences",
          cell: (finding) => (
            <span title={occurrenceIdentity(finding)}>
              {finding.occurrence_count} / {finding.found_commit_count} commit
              {finding.found_commit_count === 1 ? "" : "s"}
            </span>
          ),
          className: "w-28 max-w-28 px-2 text-xs",
          headerClassName: "w-28 max-w-28 px-2",
        },
        {
          id: "finding-state",
          header: "Finding state",
          cell: (finding) => (
            <StateWithAttention
              label={repositoryFindingLifecycleLabel(finding.lifecycle)}
              attention={
                finding.match_state === "provisional"
                  ? repositoryFindingAttentionLabel("duplicate_review")
                  : undefined
              }
            />
          ),
          className: "w-28 max-w-28 px-2 text-xs",
          headerClassName: "w-28 max-w-28 px-2",
        },
        {
          id: "issue",
          header: "Issue",
          cell: (finding) => (
            <StateWithAttention
              label={repositoryFindingIssueLabel(finding.issue.state)}
              attention={
                finding.issue.conflict
                  ? repositoryFindingAttentionLabel("issue_conflict")
                  : undefined
              }
            />
          ),
          className: "w-36 max-w-36 px-2 text-xs",
          headerClassName: "w-36 max-w-36 px-2",
        },
        {
          id: "resolution-check",
          header: "Resolution check",
          cell: (finding) => (
            <StateWithAttention
              label={repositoryFindingResolutionLabel(finding.validation_state)}
              attention={
                finding.validation_state === "failed"
                  ? repositoryFindingAttentionLabel("fix_check_failed")
                  : undefined
              }
            />
          ),
          className: "w-32 max-w-32 px-2 text-xs",
          headerClassName: "w-32 max-w-32 px-2",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (finding) => (
            <time
              dateTime={finding.updated_at}
              title={formatTimestamp(finding.updated_at)}
            >
              {formatCompactDate(finding.updated_at)}
            </time>
          ),
          className: "w-28 max-w-28 px-2 text-xs",
          headerClassName: "w-28 max-w-28 px-2",
        },
      ],
      gridFacts: [
        {
          id: "severity",
          label: "Severity",
          value: (finding) => finding.canonical_severity,
        },
        {
          id: "finding-state",
          label: "Finding state",
          value: (finding) =>
            repositoryFindingLifecycleLabel(finding.lifecycle),
        },
        {
          id: "issue",
          label: "Issue",
          value: (finding) => repositoryFindingIssueLabel(finding.issue.state),
        },
        {
          id: "resolution-check",
          label: "Resolution check",
          value: (finding) =>
            repositoryFindingResolutionLabel(finding.validation_state),
        },
      ],
      badges: [
        {
          id: "duplicate-review",
          label: (finding) =>
            finding.match_state === "provisional"
              ? repositoryFindingAttentionLabel("duplicate_review")
              : null,
          variant: "outline",
        },
        {
          id: "issue-conflict",
          label: (finding) =>
            finding.issue.conflict
              ? repositoryFindingAttentionLabel("issue_conflict")
              : null,
          variant: "outline",
        },
        {
          id: "fix-check-failed",
          label: (finding) =>
            finding.validation_state === "failed"
              ? repositoryFindingAttentionLabel("fix_check_failed")
              : null,
          variant: "outline",
        },
      ],
    }),
    [automationID],
  )

  const selectionActions = (state: StandardCollectionSelectionState) => {
    const selectedIDs = [...state.selectedIDs]
    const selectedFindings = selectedIDs.map((id) => findingsByID.get(id))
    const selectionBusy = generation.isPending || validation.isPending
    const canDraft =
      state.selectedCount > 0 &&
      selectedIDs.every(
        (findingID) =>
          (loadedDraftEligibility.get(findingID) ??
            rememberedDraftEligibility.get(findingID)) === true,
      )
    const selectedFixCheckState = batchFixCheckState(selectedFindings)
    const fixCheckInProgress =
      selectedFixCheckState === "pending" || selectedFixCheckState === "running"
    const canValidate =
      state.selectedCount > 0 &&
      state.selectedCount <= 50 &&
      selectedFindings.every(Boolean) &&
      !fixCheckInProgress
    return (
      <>
        <Button
          type="button"
          size="sm"
          disabled={selectionBusy || !canDraft}
          title={
            canDraft
              ? undefined
              : "Issue drafting requires open, unassociated repository findings that do not need review."
          }
          onClick={() => {
            setGenerationSelection(selectedIDs)
            setInstructionsOpen(true)
          }}
        >
          <IconSparkles /> Draft issue previews
        </Button>
        <Button
          type="button"
          size="sm"
          disabled={selectionBusy || !canValidate}
          title={
            canValidate
              ? undefined
              : fixCheckInProgress
                ? "Wait for the selected fix checks to finish."
                : "Check at most 50 repository findings at once."
          }
          onClick={() =>
            validation.mutate({
              selectedIDs,
              clearSelection: state.clearSelection,
            })
          }
        >
          <IconChecks />
          {validation.isPending
            ? "Fix check queued"
            : repositoryFindingResolutionActionLabel(selectedFixCheckState)}
        </Button>
      </>
    )
  }

  return (
    <>
      <StandardCollectionPage
        definition={definition}
        search={search}
        onSearchChange={onSearchChange}
        items={findings}
        total={firstPage?.total}
        schema={firstPage?.query_schema}
        canonicalQuery={firstPage?.canonical_query}
        loading={query.isLoading}
        fetching={query.isFetching || healthQuery.isFetching}
        error={query.error}
        context={{
          backLabel: "Repositories",
          onBack,
          identity: firstPage ? (
            <span className="text-muted-foreground truncate text-xs">
              {firstPage.automation.repository}
            </span>
          ) : undefined,
          status: firstPage ? (
            <Badge variant="outline">{firstPage.automation.status}</Badge>
          ) : undefined,
        }}
        onRefresh={async () => {
          await Promise.all([query.refetch(), healthQuery.refetch()])
        }}
        hasNextPage={query.hasNextPage}
        loadingMore={query.isFetchingNextPage}
        onLoadMore={query.fetchNextPage}
        onOpenItem={(finding) => onOpenFinding(finding.id)}
        selection={{
          disabled: generation.isPending || validation.isPending,
          maximumSelected: 200,
          isItemSelectable: (finding) =>
            finding.match_state !== "provisional" &&
            finding.lifecycle !== "dismissed" &&
            !finding.issue.conflict,
          renderActions: selectionActions,
        }}
        beforeResults={
          healthQuery.data &&
          healthQuery.data.run_findings.unrepresented > 0 ? (
            <section
              role="status"
              aria-labelledby="repository-findings-incomplete"
              className="border-border flex flex-wrap items-start justify-between gap-3 rounded-lg border p-4"
            >
              <div className="flex min-w-0 gap-3">
                <IconAlertTriangle
                  className="text-muted-foreground mt-0.5 size-5 shrink-0"
                  aria-hidden="true"
                />
                <div>
                  <h2
                    id="repository-findings-incomplete"
                    className="font-semibold"
                  >
                    Repository coverage is incomplete
                  </h2>
                  <p className="text-muted-foreground mt-1 text-sm">
                    {healthQuery.data.run_findings.unrepresented} run finding
                    {healthQuery.data.run_findings.unrepresented === 1
                      ? " is"
                      : "s are"}{" "}
                    still pending, processing, or failed before representation
                    in the canonical repository ledger.
                  </p>
                </div>
              </div>
              {onOpenIncompleteFindings && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={onOpenIncompleteFindings}
                >
                  <IconExternalLink /> View unrepresented run findings
                </Button>
              )}
            </section>
          ) : undefined
        }
        emptyTitle="No repository findings"
        emptyDescription="This repository does not have a repository finding yet."
      />

      <Dialog open={instructionsOpen} onOpenChange={setInstructionsOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              Draft {generationSelection.length} issue
              {generationSelection.length === 1 ? "" : "s"}
            </DialogTitle>
            <DialogDescription>
              Each selected repository finding gets its own saved AI-written
              preview. Optional instructions may change presentation only.
            </DialogDescription>
          </DialogHeader>
          <label
            htmlFor="repository-finding-generation-instructions"
            className="space-y-2 text-sm"
          >
            <span className="font-medium">
              Custom presentation instructions
            </span>
            <Textarea
              id="repository-finding-generation-instructions"
              value={customInstructions}
              className="min-h-28"
              placeholder="Leave blank to use the grounded default issue format."
              onChange={(event) => setCustomInstructions(event.target.value)}
            />
          </label>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={generation.isPending}
              onClick={() => setInstructionsOpen(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={
                generation.isPending || generationSelection.length === 0
              }
              onClick={() =>
                generation.mutate({
                  selectedIDs: generationSelection,
                  custom: customInstructions.trim() !== "",
                })
              }
            >
              {generation.isPending ? "Drafting…" : "Draft previews"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function occurrenceIdentity(
  finding: RepositoryReviewRepositoryFindingSummary,
): string {
  return `${finding.occurrence_count} occurrence${finding.occurrence_count === 1 ? "" : "s"} across ${finding.found_commit_count} commit${finding.found_commit_count === 1 ? "" : "s"}`
}

function latestLocation(
  finding: RepositoryReviewRepositoryFindingSummary,
): string {
  return finding.path
    ? `${finding.path}${finding.symbol ? ` · ${finding.symbol}` : ""}`
    : ""
}

function StateWithAttention({
  label,
  attention,
}: {
  label: string
  attention?: string
}) {
  return (
    <span className="flex min-w-0 flex-col items-start gap-1 leading-tight">
      <span className="min-w-0 break-words">{label}</span>
      {attention && (
        <Badge variant="outline" className="max-w-full px-1.5 text-[11px]">
          {attention}
        </Badge>
      )}
    </span>
  )
}

function batchFixCheckState(
  findings: Array<RepositoryReviewRepositoryFindingSummary | undefined>,
): RepositoryReviewRepositoryFindingSummary["validation_state"] {
  const states = findings.flatMap((finding) =>
    finding ? [finding.validation_state] : [],
  )
  if (states.includes("running")) return "running"
  if (states.includes("pending")) return "pending"
  if (states.length > 0 && states.every((state) => state === "failed")) {
    return "failed"
  }
  return "not_requested"
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}

function formatCompactDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleDateString(undefined, {
        year: "numeric",
        month: "short",
        day: "numeric",
      })
}

function repositoryFindingCanBeDrafted(
  finding: RepositoryReviewRepositoryFindingSummary,
): boolean {
  return (
    finding.match_state !== "provisional" &&
    (finding.lifecycle === "open" || finding.lifecycle === "regressed") &&
    !finding.issue.conflict &&
    finding.issue.state === "none"
  )
}
