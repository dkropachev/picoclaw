import {
  IconChecks,
  IconMessageCircle,
  IconRefresh,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type RepositoryFinding,
  RepositoryReviewAPIError,
  type RepositoryReviewFinding,
  type RepositoryReviewFindingsScope,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationFinding,
  getRepositoryReviewAutomationFindings,
  reserveRepositoryReviewValidations,
  syncRepositoryReviewFinding,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  CollectionResults,
} from "@/components/collection"
import { discussionPrompt } from "@/components/repository-reviews/repository-review-actions"
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
import { switchChatSessionAndSend } from "@/features/chat/controller"
import {
  type CollectionRouteSearch,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"
import { threadOpenSessionIdAtom } from "@/store/threads"

import { createRepositoryReviewGenerationID } from "./repository-review-generation"
import {
  type RepositoryReviewRouteSearch,
  repositoryReviewDefaultQuery,
} from "./repository-review-route-state"

const pageSize = 50
const maximumDiscussionFindings = 20

export function RepositoryReviewFindingsPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenFinding,
  onGenerated,
  onOpenThread,
}: {
  automationID: string
  search: RepositoryReviewRouteSearch
  onSearchChange: (next: RepositoryReviewRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenFinding: (findingID: string) => void
  onGenerated: (generationID: string) => void
  onOpenThread: (threadID: string) => void
}) {
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const [instructionsOpen, setInstructionsOpen] = useState(false)
  const [customInstructions, setCustomInstructions] = useState("")
  const state = useCollectionRouteState({
    // Keep the pre-rename key so selection and scroll survive the /report
    // compatibility redirect within the same browser session.
    collectionKey: `repository-review-report:${automationID}:${search.scope}`,
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: ["list"],
    defaultView: "list",
    search,
    onSearchChange: (collectionSearch: CollectionRouteSearch, replace) =>
      onSearchChange(
        {
          ...search,
          q: collectionSearch.q,
          ...(collectionSearch.view ? { view: collectionSearch.view } : {}),
        },
        replace,
      ),
  })
  const query = useQuery({
    queryKey: [
      "repository-review-findings",
      automationID,
      search.scope,
      search.offset,
    ],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFindings(
        automationID,
        { scope: search.scope, offset: search.offset, limit: pageSize },
        signal,
      ),
    retry: false,
    refetchInterval: (current) =>
      current.state.data && isActive(current.state.data.automation)
        ? 2_000
        : current.state.data?.repository_findings.some((finding) =>
              new Set(["pending", "running"]).has(finding.validation_state),
            )
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
  const activeTotal =
    search.scope === "current" ? page?.total : page?.repository_finding_total
  const activeOffset =
    search.scope === "current" ? page?.offset : page?.repository_finding_offset
  const activeNextOffset =
    search.scope === "current"
      ? page?.next_offset
      : page?.next_repository_finding_offset
  const loadedDraftEligibility = useMemo(
    () =>
      new Map(
        search.scope === "current"
          ? (page?.findings ?? []).map((finding) => [
              finding.id,
              findingCanBeDrafted(finding),
            ])
          : (page?.repository_findings ?? []).map((finding) => [
              finding.id,
              repositoryFindingCanBeDrafted(finding),
            ]),
      ),
    [page?.findings, page?.repository_findings, search.scope],
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
          "One or more selected findings are not eligible for issue drafting.",
        )
      }
      const repositoryFindingIDs = details.flatMap((detail) =>
        detail.repository_finding?.id ? [detail.repository_finding.id] : [],
      )
      if (new Set(repositoryFindingIDs).size !== repositoryFindingIDs.length) {
        throw new Error(
          "Select at most one occurrence for each repository finding.",
        )
      }
      const actionFindingIDs =
        search.scope === "current"
          ? selectedIDs
          : details.map(
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
  const reviewFindingDefinition = useMemo<
    CollectionDefinition<RepositoryReviewFinding>
  >(
    () => ({
      key: "repository-review-findings",
      title: "Review findings",
      defaultQuery: "",
      supportedViews: ["list"],
      defaultView: "list",
      getItemID: (finding) => finding.id,
      getItemLabel: (finding) => finding.title,
      getItemIdentity: (finding) => ({
        title: finding.title,
        description: `${finding.file.path}${finding.line == null ? "" : `:${finding.line}`}`,
        metadata: `${finding.message || `${finding.models.length} model contributor${finding.models.length === 1 ? "" : "s"}`} · ${finding.status}${finding.issue_draft_id ? " · issue associated" : ""}`,
      }),
      columns: [],
      badges: [
        {
          id: "severity",
          label: (finding) => finding.severity,
          variant: "outline",
        },
        {
          id: "repository-match",
          label: (finding) =>
            finding.repository_match_state || "mapping pending",
          variant: "outline",
        },
      ],
    }),
    [],
  )
  const repositoryFindingDefinition = useMemo<
    CollectionDefinition<RepositoryFinding>
  >(
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
          id: "match",
          label: (finding) => finding.match_state,
          variant: "secondary",
        },
      ],
    }),
    [],
  )

  const discuss = async () => {
    if (!page || search.scope !== "current" || state.selectedCount === 0) return
    if (state.selectedCount > maximumDiscussionFindings) {
      toast.error(
        `Discuss at most ${maximumDiscussionFindings} findings in one thread.`,
      )
      return
    }
    try {
      const details = await Promise.all(
        [...state.selectedIDs].map((findingID) =>
          getRepositoryReviewAutomationFinding(automationID, findingID),
        ),
      )
      const findings = details.map((detail) => detail.finding)
      const contextIDs = [
        ...new Set(findings.flatMap((finding) => finding.context_ids)),
      ]
      const thread = await createThread({
        type: "reviewing",
        title:
          findings.length === 1
            ? boundedText(findings[0]!.title, 256)
            : boundedText(
                `${page.automation.repository}: ${findings.length} review findings`,
                256,
              ),
        context: {
          repository: page.automation.repository,
          repository_review: automationID,
          finding_ids: findings.map((finding) => finding.id).join(","),
          context_ids: contextIDs.join(","),
          ...(page.repository?.last_commit_sha
            ? { commit: page.repository.last_commit_sha }
            : {}),
        },
        source_query: boundedText(
          `repository review ${page.automation.repository}`,
          256,
        ),
      })
      const sessionID = thread.ui_session_id || thread.id
      for (const detail of details) {
        const sent = await switchChatSessionAndSend(sessionID, {
          content: discussionPrompt(
            {
              id: automationID,
              repository: page.automation.repository,
              last_commit_sha: page.repository?.last_commit_sha,
              contexts: detail.contexts,
            },
            [detail.finding],
          ),
        })
        if (!sent) {
          await dropThread(thread.id).catch(() => undefined)
          throw new Error("The finding discussion could not be started.")
        }
      }
      setThreadOpenSessionID(sessionID)
      onOpenThread(sessionID)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Discussion failed.")
    }
  }

  return (
    <>
      <CollectionDetailShell
        title="Findings"
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
            aria-label="Refresh findings"
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
        backLabel="Review details"
        contentRef={state.setScrollContainerRef}
        onContentScroll={state.onResultsScroll}
      >
        {page && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div
                role="group"
                aria-label="Finding view"
                className="border-border bg-muted/30 inline-flex rounded-md border p-1"
              >
                <FindingTab
                  label="This review"
                  scope="current"
                  activeScope={search.scope}
                  onChange={(scope) =>
                    onSearchChange({
                      ...search,
                      scope,
                      offset: 0,
                      generation_id: undefined,
                    })
                  }
                />
                <FindingTab
                  label="Repository findings"
                  scope="all"
                  activeScope={search.scope}
                  onChange={(scope) =>
                    onSearchChange({
                      ...search,
                      scope,
                      offset: 0,
                      generation_id: undefined,
                    })
                  }
                />
              </div>
              <span className="text-muted-foreground text-sm">
                {activeTotal ?? 0} finding{activeTotal === 1 ? "" : "s"}
              </span>
            </div>

            {search.scope === "current" && state.selectedCount > 0 && (
              <div
                data-testid="repository-review-findings-selection"
                className="border-border bg-muted/20 flex flex-wrap items-center gap-2 rounded-lg border p-3"
              >
                <strong className="mr-auto text-sm">
                  {state.selectedCount} selected
                </strong>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => void discuss()}
                >
                  <IconMessageCircle /> Discuss with AI
                </Button>
                <Button
                  type="button"
                  size="sm"
                  disabled={generation.isPending || !canDraftSelection}
                  title={
                    canDraftSelection
                      ? undefined
                      : "Issue drafting is available after every selected finding is mapped."
                  }
                  onClick={() => setInstructionsOpen(true)}
                >
                  <IconSparkles /> Draft issue previews
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

            {search.scope === "all" && state.selectedCount > 0 && (
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
                      : "Issue drafting requires open, unassociated, non-provisional repository findings."
                  }
                  onClick={() => setInstructionsOpen(true)}
                >
                  <IconSparkles /> Draft issue previews
                </Button>
                <Button
                  type="button"
                  size="sm"
                  disabled={validation.isPending}
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

            {search.scope === "current" &&
            page.findings.length === 0 &&
            isActive(page.automation) ? (
              <div className="border-border flex min-h-48 flex-col items-center justify-center rounded-lg border border-dashed p-6 text-center">
                <h2 className="font-semibold">Review in progress</h2>
                <p className="text-muted-foreground mt-2 text-sm">
                  Findings will appear after the first validated checkpoint.
                </p>
              </div>
            ) : search.scope === "current" ? (
              <CollectionResults
                definition={reviewFindingDefinition}
                items={page.findings}
                view="list"
                selection={{
                  selectedIDs: state.selectedIDs,
                  additive: true,
                  maximumSelected: 200,
                  onSelectionChange: state.setSelection,
                }}
                onOpenItem={(finding) => onOpenFinding(finding.id)}
                emptyTitle="No review findings"
                emptyDescription="This review has not stored a validated finding."
              />
            ) : (
              <CollectionResults
                definition={repositoryFindingDefinition}
                items={page.repository_findings}
                view="list"
                selection={{
                  selectedIDs: state.selectedIDs,
                  additive: true,
                  maximumSelected: 50,
                  isItemDisabled: (finding) =>
                    finding.match_state === "provisional" ||
                    finding.lifecycle === "dismissed" ||
                    Boolean(finding.issue.conflict),
                  onSelectionChange: state.setSelection,
                }}
                onOpenItem={(finding) => onOpenFinding(finding.id)}
                emptyTitle="No repository findings"
                emptyDescription="No review occurrences have been mapped to a repository finding."
              />
            )}

            <FindingsPagination
              offset={activeOffset ?? 0}
              total={activeTotal ?? 0}
              nextOffset={activeNextOffset}
              onChange={(offset) => onSearchChange({ ...search, offset })}
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
              Each selected finding gets its own saved AI-written preview.
              Optional instructions may change presentation only.
            </DialogDescription>
          </DialogHeader>
          <label
            htmlFor="repository-review-generation-instructions"
            className="space-y-2 text-sm"
          >
            <span className="font-medium">
              Custom presentation instructions
            </span>
            <Textarea
              id="repository-review-generation-instructions"
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

function FindingTab({
  label,
  scope,
  activeScope,
  onChange,
}: {
  label: string
  scope: RepositoryReviewFindingsScope
  activeScope: RepositoryReviewFindingsScope
  onChange: (scope: RepositoryReviewFindingsScope) => void
}) {
  const active = scope === activeScope
  return (
    <button
      type="button"
      aria-pressed={active}
      className={
        active
          ? "bg-background text-foreground rounded px-3 py-1.5 text-sm font-medium shadow-sm"
          : "text-muted-foreground hover:text-foreground rounded px-3 py-1.5 text-sm"
      }
      onClick={() => !active && onChange(scope)}
    >
      {label}
    </button>
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
      aria-label="Finding pages"
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

function findingCanBeDrafted(finding: RepositoryReviewFinding): boolean {
  return (
    Boolean(finding.repository_finding_id) &&
    finding.repository_match_state !== "provisional" &&
    finding.status === "open" &&
    !finding.issue_draft_id
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

function boundedText(value: string, maximumBytes: number): string {
  const encoded = new TextEncoder().encode(value.trim())
  if (encoded.byteLength <= maximumBytes) return value.trim()
  return `${new TextDecoder().decode(encoded.slice(0, maximumBytes - 3))}…`
}
