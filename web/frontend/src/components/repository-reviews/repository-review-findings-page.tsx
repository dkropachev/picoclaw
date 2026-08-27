import {
  IconExternalLink,
  IconMessageCircle,
  IconRefresh,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewFinding,
  getRepositoryReviewAutomationFinding,
  getRepositoryReviewAutomationFindings,
  retryRepositoryReviewRunFindingStatuses,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  CollectionResults,
} from "@/components/collection"
import { discussionPrompt } from "@/components/repository-reviews/repository-review-actions"
import {
  runFindingRepositoryFindingID,
  runFindingStatusCanRetry,
  runFindingStatusIsInProgress,
  runFindingStatusLabel,
} from "@/components/repository-reviews/repository-review-run-finding-status"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import {
  type CollectionRouteSearch,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"
import { threadOpenSessionIdAtom } from "@/store/threads"

import {
  type RepositoryReviewRouteSearch,
  repositoryReviewDefaultQuery,
} from "./repository-review-route-state"

const pageSize = 50
const maximumDiscussionFindings = 20

interface RememberedRunFindingState {
  repositoryFindingID?: string
  canRetry: boolean
}

export function RepositoryReviewFindingsPage({
  automationID,
  search,
  onSearchChange,
  onBack,
  onOpenFinding,
  onOpenRepositoryFindings,
  onOpenRepositoryFinding,
  onOpenThread,
}: {
  automationID: string
  search: RepositoryReviewRouteSearch
  onSearchChange: (next: RepositoryReviewRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenFinding: (findingID: string) => void
  onOpenRepositoryFindings: () => void
  onOpenRepositoryFinding: (findingID: string) => void
  onOpenThread: (threadID: string) => void
  onGenerated?: (generationID: string) => void
}) {
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const state = useCollectionRouteState({
    collectionKey: `repository-review-report:${automationID}:current`,
    defaultQuery: repositoryReviewDefaultQuery,
    supportedViews: ["list"],
    defaultView: "list",
    search,
    onSearchChange: (collectionSearch: CollectionRouteSearch, replace) =>
      onSearchChange(
        {
          ...search,
          q: collectionSearch.q,
          scope: "current",
          ...(collectionSearch.view ? { view: collectionSearch.view } : {}),
        },
        replace,
      ),
  })
  const query = useQuery({
    queryKey: ["repository-review-findings", automationID, search.offset],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFindings(
        automationID,
        { scope: "current", offset: search.offset, limit: pageSize },
        signal,
      ),
    retry: false,
    refetchInterval: (current) =>
      current.state.data &&
      (isActive(current.state.data.automation) ||
        current.state.data.findings.some(runFindingStatusIsInProgress))
        ? 2_000
        : false,
  })
  const page = query.data
  const loadedRunFindingState = useMemo(
    () =>
      new Map(
        (page?.findings ?? []).map((finding) => [
          finding.id,
          {
            repositoryFindingID: runFindingRepositoryFindingID(finding),
            canRetry: runFindingStatusCanRetry(finding),
          } satisfies RememberedRunFindingState,
        ]),
      ),
    [page?.findings],
  )
  const [rememberedRunFindingState, setRememberedRunFindingState] = useState(
    () => new Map<string, RememberedRunFindingState>(),
  )
  useEffect(() => {
    if (loadedRunFindingState.size === 0) return
    setRememberedRunFindingState((current) => {
      const next = new Map(current)
      for (const [findingID, findingState] of loadedRunFindingState) {
        next.set(findingID, findingState)
      }
      return next
    })
  }, [loadedRunFindingState])
  const selectedIDs = [...state.selectedIDs]
  const selectedRunFindingState = selectedIDs.map(
    (findingID) =>
      loadedRunFindingState.get(findingID) ??
      rememberedRunFindingState.get(findingID),
  )
  const selectedRepositoryFindingID =
    selectedIDs.length === 1
      ? selectedRunFindingState[0]?.repositoryFindingID
      : undefined
  const canRetrySelection =
    selectedIDs.length > 0 &&
    selectedRunFindingState.every((findingState) => findingState?.canRetry)
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const retryStatus = useMutation({
    mutationFn: () =>
      retryRepositoryReviewRunFindingStatuses(automationID, selectedIDs),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Selected run finding statuses queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Run finding status could not be retried.",
      )
      void query.refetch()
    },
  })
  const definition = useMemo<CollectionDefinition<RepositoryReviewFinding>>(
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
          id: "run-status",
          label: runFindingStatusLabel,
          variant: "outline",
        },
      ],
    }),
    [],
  )

  const discuss = async () => {
    if (!page || state.selectedCount === 0) return
    if (state.selectedCount > maximumDiscussionFindings) {
      toast.error(
        `Discuss at most ${maximumDiscussionFindings} findings in one thread.`,
      )
      return
    }
    try {
      const details = await Promise.all(
        selectedIDs.map((findingID) =>
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
    <CollectionDetailShell
      title="Run findings"
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
          aria-label="Refresh run findings"
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
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={onOpenRepositoryFindings}
            >
              <IconExternalLink /> View repository findings
            </Button>
            <span className="text-muted-foreground text-sm">
              {page.total} finding{page.total === 1 ? "" : "s"}
            </span>
          </div>

          {state.selectedCount > 0 && (
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
              {canRetrySelection && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={retryStatus.isPending}
                  onClick={() => retryStatus.mutate()}
                >
                  <IconRefresh />
                  {retryStatus.isPending ? "Retrying…" : "Retry status"}
                </Button>
              )}
              {selectedRepositoryFindingID && (
                <Button
                  type="button"
                  size="sm"
                  onClick={() =>
                    onOpenRepositoryFinding(selectedRepositoryFindingID)
                  }
                >
                  <IconExternalLink /> Open repository finding
                </Button>
              )}
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

          {page.findings.length === 0 && isActive(page.automation) ? (
            <div className="border-border flex min-h-48 flex-col items-center justify-center rounded-lg border border-dashed p-6 text-center">
              <h2 className="font-semibold">Review in progress</h2>
              <p className="text-muted-foreground mt-2 text-sm">
                Findings will appear after the first validated checkpoint.
              </p>
            </div>
          ) : (
            <CollectionResults
              definition={definition}
              items={page.findings}
              view="list"
              selection={{
                selectedIDs: state.selectedIDs,
                additive: true,
                maximumSelected: 200,
                onSelectionChange: state.setSelection,
              }}
              onOpenItem={(finding) => onOpenFinding(finding.id)}
              emptyTitle="No run findings"
              emptyDescription="This review has not stored a validated finding."
            />
          )}

          <FindingsPagination
            offset={page.offset}
            total={page.total}
            nextOffset={page.next_offset}
            onChange={(offset) =>
              onSearchChange({ ...search, scope: "current", offset })
            }
          />
        </div>
      )}
    </CollectionDetailShell>
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
      aria-label="Run finding pages"
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

function boundedText(value: string, maximumBytes: number): string {
  const encoded = new TextEncoder().encode(value.trim())
  if (encoded.byteLength <= maximumBytes) return value.trim()
  return `${new TextDecoder().decode(encoded.slice(0, maximumBytes - 3))}…`
}
