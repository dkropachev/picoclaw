import { IconChecks, IconRefresh, IconSparkles } from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type RepositoryFinding,
  RepositoryReviewAPIError,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationFinding,
  getRepositoryReviewAutomationFindings,
  reserveRepositoryReviewValidations,
  syncRepositoryReviewFinding,
} from "@/api/repository-reviews"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  CollectionResults,
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
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"

import { createRepositoryReviewGenerationID } from "./repository-review-generation"
import {
  type RepositoryReviewRouteSearch,
  repositoryReviewDefaultQuery,
} from "./repository-review-route-state"

const pageSize = 50

export function RepositoryReviewRepositoryFindingsPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenFinding,
  onGenerated,
}: {
  automationID: string
  search: RepositoryReviewRouteSearch
  onSearchChange: (next: RepositoryReviewRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenFinding: (findingID: string) => void
  onGenerated: (generationID: string) => void
}) {
  const [instructionsOpen, setInstructionsOpen] = useState(false)
  const [customInstructions, setCustomInstructions] = useState("")
  const state = useCollectionRouteState({
    collectionKey: `repository-review-repository-findings:${automationID}`,
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: ["list"],
    defaultView: "list",
    search,
    onSearchChange: (collectionSearch: CollectionRouteSearch, replace) =>
      onSearchChange(
        {
          ...search,
          q: collectionSearch.q,
          scope: "all",
          ...(collectionSearch.view ? { view: collectionSearch.view } : {}),
        },
        replace,
      ),
  })
  const query = useQuery({
    queryKey: [
      "repository-review-repository-findings",
      automationID,
      search.offset,
    ],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFindings(
        automationID,
        { scope: "all", offset: search.offset, limit: pageSize },
        signal,
      ),
    retry: false,
    refetchInterval: (current) =>
      current.state.data &&
      (isActive(current.state.data.automation) ||
        current.state.data.repository_findings.some((finding) =>
          new Set(["pending", "running"]).has(finding.validation_state),
        ))
        ? 2_000
        : false,
  })
  const page = query.data
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
    const stale = (page?.repository_findings ?? []).filter(
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
  }, [page?.repository_findings, query, synchronizeIssue])
  const loadedDraftEligibility = useMemo(
    () =>
      new Map(
        (page?.repository_findings ?? []).map((finding) => [
          finding.id,
          repositoryFindingCanBeDrafted(finding),
        ]),
      ),
    [page?.repository_findings],
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
  const canDraftSelection =
    state.selectedCount > 0 &&
    [...state.selectedIDs].every(
      (findingID) =>
        (loadedDraftEligibility.get(findingID) ??
          rememberedDraftEligibility.get(findingID)) === true,
    )
  const canValidateSelection =
    state.selectedCount > 0 && state.selectedCount <= 50
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const generation = useMutation({
    mutationFn: async ({ custom }: { custom: boolean }) => {
      const selectedIDs = [...state.selectedIDs]
      const details = await Promise.all(
        selectedIDs.map((findingID) =>
          getRepositoryReviewAutomationFinding(automationID, findingID),
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
    mutationFn: async () => {
      const selectedIDs = [...state.selectedIDs]
      const details = await Promise.all(
        selectedIDs.map((findingID) =>
          getRepositoryReviewAutomationFinding(automationID, findingID),
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
          "Resolve manual GitHub association conflicts before validation.",
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
    onSuccess: async () => {
      state.clearSelection()
      await query.refetch()
      toast.success("Selected resolution validations queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "Validation batch failed.",
      )
      void query.refetch()
    },
  })
  const definition = useMemo<CollectionDefinition<RepositoryFinding>>(
    () => ({
      key: "repository-findings",
      title: "Repository findings",
      defaultQuery: "",
      supportedViews: ["list"],
      defaultView: "list",
      getItemID: (finding) => finding.id,
      getItemLabel: (finding) => finding.canonical_title,
      getItemIdentity: (finding) => {
        const latest = finding.path_symbol_history.at(-1)
        const foundCommitCount =
          finding.found_commit_count ?? finding.found_commits.length
        return {
          title: finding.canonical_title,
          description: latest
            ? `${latest.path}${latest.symbol ? ` · ${latest.symbol}` : ""}`
            : "Cross-commit diagnosis",
          metadata: `${foundCommitCount} observed commit${foundCommitCount === 1 ? "" : "s"} · ${finding.lifecycle} · issue ${finding.issue.state} · validation ${finding.validation_state}`,
        }
      },
      columns: [],
      badges: [
        {
          id: "severity",
          label: (finding) => finding.canonical_severity,
          variant: "outline",
        },
        {
          id: "status",
          label: (finding) => finding.match_state,
          variant: "secondary",
        },
      ],
    }),
    [],
  )

  return (
    <>
      <CollectionDetailShell
        title="Repository findings"
        identity={
          page ? (
            <span className="text-muted-foreground truncate text-xs">
              {page.automation.repository}
            </span>
          ) : undefined
        }
        status={
          page ? (
            <Badge variant="outline">{page.automation.status}</Badge>
          ) : undefined
        }
        actions={
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            aria-label="Refresh repository findings"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <IconRefresh />
          </Button>
        }
        loading={query.isLoading}
        error={!notFound ? query.error?.message : undefined}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Repositories"
        contentRef={state.setScrollContainerRef}
        onContentScroll={state.onResultsScroll}
      >
        {page && (
          <div className="space-y-4">
            <div className="flex items-center justify-end">
              <span className="text-muted-foreground text-sm">
                {page.repository_finding_total} finding
                {page.repository_finding_total === 1 ? "" : "s"}
              </span>
            </div>

            {state.selectedCount > 0 && (
              <div className="border-border bg-muted/20 flex flex-wrap items-center gap-2 rounded-lg border p-3">
                <strong className="mr-auto text-sm">
                  {state.selectedCount} selected
                </strong>
                <Button
                  type="button"
                  size="sm"
                  disabled={generation.isPending || !canDraftSelection}
                  title={
                    canDraftSelection
                      ? undefined
                      : "Issue drafting requires open, unassociated repository findings that do not need review."
                  }
                  onClick={() => setInstructionsOpen(true)}
                >
                  <IconSparkles /> Draft issue previews
                </Button>
                <Button
                  type="button"
                  size="sm"
                  disabled={validation.isPending || !canValidateSelection}
                  title={
                    canValidateSelection
                      ? undefined
                      : "Validate at most 50 repository findings at once."
                  }
                  onClick={() => validation.mutate()}
                >
                  <IconChecks />
                  {validation.isPending ? "Queueing…" : "Validate resolutions"}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={state.clearSelection}
                >
                  Clear selection
                </Button>
              </div>
            )}

            <CollectionResults
              definition={definition}
              items={page.repository_findings}
              view="list"
              selection={{
                selectedIDs: state.selectedIDs,
                additive: true,
                maximumSelected: 200,
                isItemDisabled: (finding) =>
                  finding.match_state === "provisional" ||
                  finding.lifecycle === "dismissed" ||
                  Boolean(finding.issue.conflict),
                onSelectionChange: state.setSelection,
              }}
              onOpenItem={(finding) => onOpenFinding(finding.id)}
              emptyTitle="No repository findings"
              emptyDescription="This repository does not have a repository finding yet."
            />

            <FindingsPagination
              offset={page.repository_finding_offset ?? 0}
              total={page.repository_finding_total}
              nextOffset={page.next_repository_finding_offset}
              onChange={(offset) =>
                onSearchChange({ ...search, scope: "all", offset })
              }
            />
          </div>
        )}
      </CollectionDetailShell>

      <Dialog open={instructionsOpen} onOpenChange={setInstructionsOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              Draft {state.selectedCount} issue
              {state.selectedCount === 1 ? "" : "s"}
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
              disabled={generation.isPending || state.selectedCount === 0}
              onClick={() =>
                generation.mutate({ custom: customInstructions.trim() !== "" })
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

function FindingsPagination({
  offset,
  total,
  nextOffset,
  onChange,
}: {
  offset: number
  total: number
  nextOffset?: number
  onChange: (offset: number) => void
}) {
  if (offset === 0 && nextOffset == null && total <= pageSize) return null
  return (
    <nav
      className="flex items-center justify-between gap-3"
      aria-label="Repository finding pages"
    >
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={offset === 0}
        onClick={() => onChange(Math.max(0, offset - pageSize))}
      >
        Previous findings
      </Button>
      <span className="text-muted-foreground text-xs">
        {total === 0 ? 0 : offset + 1}–{Math.min(total, offset + pageSize)} of{" "}
        {total}
      </span>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={nextOffset == null}
        onClick={() => nextOffset != null && onChange(nextOffset)}
      >
        Next findings
      </Button>
    </nav>
  )
}

function isActive(review: {
  status: string
  auto_continue: boolean
  progress: { stage: string }
}): boolean {
  return (
    review.status === "running" ||
    review.status === "stopping" ||
    (review.status === "idle" &&
      review.auto_continue &&
      review.progress.stage.trim().toLowerCase() === "next batch queued")
  )
}

function repositoryFindingCanBeDrafted(finding: RepositoryFinding): boolean {
  return (
    finding.match_state !== "provisional" &&
    (finding.lifecycle === "open" || finding.lifecycle === "regressed") &&
    !finding.issue.conflict &&
    finding.issue.state === "none"
  )
}
