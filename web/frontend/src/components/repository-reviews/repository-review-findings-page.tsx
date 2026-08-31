import {
  IconExternalLink,
  IconMessageCircle,
  IconRefresh,
} from "@tabler/icons-react"
import { useInfiniteQuery, useMutation } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type RepositoryReviewRunFindingSummary,
  getRepositoryReviewAutomationFinding,
  listRepositoryReviewAutomationFindingsPage,
  retryRepositoryReviewHistoricalDeduplication,
  retryRepositoryReviewRunFindingStatuses,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import {
  type CollectionDefinition,
  StandardCollectionPage,
  type StandardCollectionSelectionState,
} from "@/components/collection"
import { discussionPrompt } from "@/components/repository-reviews/repository-review-actions"
import { RepositoryReviewFindingsProcessing } from "@/components/repository-reviews/repository-review-findings-processing"
import {
  runFindingRepositoryFindingID,
  runFindingStatusCanRetry,
  runFindingStatusCompactLabel,
  runFindingStatusIsInProgress,
  runFindingStatusLabel,
} from "@/components/repository-reviews/repository-review-run-finding-status"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"
import { threadOpenSessionIdAtom } from "@/store/threads"

import {
  type RepositoryReviewCollectionSearch,
  repositoryReviewRunFindingsDefaultQuery,
  repositoryReviewViews,
} from "./repository-review-route-state"

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
  onOpenRawFindings,
  onOpenRepositoryFindings,
  onOpenRepositoryFinding,
  onOpenThread,
}: {
  automationID: string
  search: RepositoryReviewCollectionSearch
  onSearchChange: (next: CollectionRouteSearch, replace?: boolean) => void
  onBack: () => void
  onOpenFinding: (findingID: string) => void
  onOpenRawFindings?: () => void
  onOpenRepositoryFindings: () => void
  onOpenRepositoryFinding: (findingID: string) => void
  onOpenThread: (threadID: string) => void
}) {
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: repositoryReviewRunFindingsDefaultQuery,
    supportedViews: repositoryReviewViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["repository-review-findings", automationID, activeQuery],
    initialPageParam: "",
    queryFn: ({ signal, pageParam }) =>
      listRepositoryReviewAutomationFindingsPage(
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
      current.state.data?.pages.some((page) => {
        if (isActive(page.automation)) return true
        if (
          page.historical_deduplication?.required &&
          page.historical_deduplication.status === "failed"
        ) {
          return false
        }
        return (
          page.findings.some(runFindingStatusIsInProgress) ||
          Boolean(
            page.findings_processing &&
            (page.findings_processing.pending > 0 ||
              page.findings_processing.processing > 0),
          ) ||
          historicalReplayIsActive(page.historical_deduplication)
        )
      })
        ? 2_000
        : false,
  })
  const pages = query.data?.pages
  const firstPage = pages?.[0]
  const findings = useMemo(
    () => pages?.flatMap((page) => page.findings) ?? [],
    [pages],
  )
  const loadedRunFindingState = useMemo(
    () =>
      new Map(
        findings.map((finding) => [
          finding.id,
          {
            repositoryFindingID: runFindingRepositoryFindingID(finding),
            canRetry: runFindingStatusCanRetry(finding),
          } satisfies RememberedRunFindingState,
        ]),
      ),
    [findings],
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
  const retryStatus = useMutation({
    mutationFn: (findingIDs: string[]) =>
      retryRepositoryReviewRunFindingStatuses(automationID, findingIDs),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Selected finding statuses queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Finding status could not be retried.",
      )
      void query.refetch()
    },
  })
  const retryHistorical = useMutation({
    mutationFn: () =>
      retryRepositoryReviewHistoricalDeduplication(automationID),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Historical deduplication queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Historical deduplication could not be retried.",
      )
      void query.refetch()
    },
  })
  const definition = useMemo<
    CollectionDefinition<RepositoryReviewRunFindingSummary>
  >(
    () => ({
      key: `repository-review-findings:${automationID}`,
      title: "Findings",
      defaultQuery: repositoryReviewRunFindingsDefaultQuery,
      supportedViews: repositoryReviewViews,
      defaultView: "list",
      getItemID: (finding) => finding.id,
      getItemLabel: (finding) => finding.title,
      getItemIdentity: (finding) => ({
        title: finding.title,
        description: findingLocation(finding),
        metadata: `Contributors: ${finding.contributors.join(", ") || "none"} · ${finding.raw_source_count ?? 0} raw source${finding.raw_source_count === 1 ? "" : "s"} · ${associationLabel(finding)} · Updated ${formatTimestamp(finding.updated_at)}`,
      }),
      columns: [
        {
          id: "repository",
          header: "Repository",
          cell: (finding) => finding.repository,
        },
        {
          id: "location",
          header: "Location",
          cell: findingLocation,
        },
        {
          id: "severity",
          header: "Severity",
          cell: (finding) => finding.severity,
          className: "w-28",
        },
        {
          id: "association",
          header: "Repository association",
          cell: associationLabel,
          className: "w-40",
        },
        {
          id: "contributors",
          header: "Contributors",
          cell: (finding) => finding.contributors.join(", ") || "—",
        },
        {
          id: "sources",
          header: "Raw sources",
          cell: (finding) => finding.raw_source_count ?? 0,
          className: "w-24 tabular-nums",
        },
        {
          id: "updated",
          header: "Updated",
          cell: (finding) => formatTimestamp(finding.updated_at),
          className: "w-44",
        },
      ],
      gridFacts: [
        {
          id: "repository",
          label: "Repository",
          value: (finding) => finding.repository,
        },
        {
          id: "severity",
          label: "Severity",
          value: (finding) => finding.severity,
        },
        {
          id: "association",
          label: "Repository association",
          value: associationLabel,
        },
        {
          id: "sources",
          label: "Raw sources",
          value: (finding) => finding.raw_source_count ?? 0,
        },
        {
          id: "updated",
          label: "Updated",
          value: (finding) => formatTimestamp(finding.updated_at),
        },
      ],
      badges: [
        {
          id: "severity",
          label: (finding) => finding.severity,
          variant: "outline",
        },
        {
          id: "run-status",
          label: (finding) => (
            <>
              <span className="hidden sm:inline">
                {runFindingStatusLabel(finding)}
              </span>
              <span className="sm:hidden">
                {runFindingStatusCompactLabel(finding)}
              </span>
            </>
          ),
          variant: "outline",
        },
      ],
    }),
    [automationID],
  )

  const selectedState = (selectedIDs: ReadonlySet<string>) =>
    [...selectedIDs].map(
      (findingID) =>
        loadedRunFindingState.get(findingID) ??
        rememberedRunFindingState.get(findingID),
    )

  const discuss = async (selectedIDs: ReadonlySet<string>) => {
    if (!firstPage || selectedIDs.size === 0) return
    if (selectedIDs.size > maximumDiscussionFindings) {
      toast.error(
        `Discuss at most ${maximumDiscussionFindings} findings in one thread.`,
      )
      return
    }
    try {
      const details = await Promise.all(
        [...selectedIDs].map((findingID) =>
          getRepositoryReviewAutomationFinding(automationID, findingID),
        ),
      )
      const selectedFindings = details.map((detail) => ({
        ...detail.finding,
        raw_source_total: detail.raw_source_total,
      }))
      const contextIDs = [
        ...new Set(selectedFindings.flatMap((finding) => finding.context_ids)),
      ]
      const thread = await createThread({
        type: "reviewing",
        title:
          selectedFindings.length === 1
            ? boundedText(selectedFindings[0]!.title, 256)
            : boundedText(
                `${firstPage.automation.repository}: ${selectedFindings.length} review findings`,
                256,
              ),
        context: {
          repository: firstPage.automation.repository,
          repository_review: automationID,
          finding_ids: selectedFindings.map((finding) => finding.id).join(","),
          context_ids: contextIDs.join(","),
          ...(firstPage.repository?.last_commit_sha
            ? { commit: firstPage.repository.last_commit_sha }
            : {}),
        },
        source_query: boundedText(
          `repository review ${firstPage.automation.repository}`,
          256,
        ),
      })
      const sessionID = thread.ui_session_id || thread.id
      for (const detail of details) {
        const sent = await switchChatSessionAndSend(sessionID, {
          content: discussionPrompt(
            {
              id: automationID,
              repository: firstPage.automation.repository,
              last_commit_sha: firstPage.repository?.last_commit_sha,
              contexts: detail.contexts,
            },
            [
              {
                ...detail.finding,
                raw_source_total: detail.raw_source_total,
              },
            ],
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

  const selectionActions = (state: StandardCollectionSelectionState) => {
    const remembered = selectedState(state.selectedIDs)
    const selectedRepositoryFindingID =
      state.selectedCount === 1 ? remembered[0]?.repositoryFindingID : undefined
    const canRetry =
      state.selectedCount > 0 &&
      remembered.every((findingState) => findingState?.canRetry)
    return (
      <>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={retryStatus.isPending}
          onClick={() => void discuss(state.selectedIDs)}
        >
          <IconMessageCircle /> Discuss with AI
        </Button>
        {canRetry && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={retryStatus.isPending}
            onClick={() => retryStatus.mutate([...state.selectedIDs])}
          >
            <IconRefresh />
            {retryStatus.isPending ? "Retrying…" : "Retry status"}
          </Button>
        )}
        {selectedRepositoryFindingID && (
          <Button
            type="button"
            size="sm"
            onClick={() => onOpenRepositoryFinding(selectedRepositoryFindingID)}
          >
            <IconExternalLink /> Open repository finding
          </Button>
        )}
      </>
    )
  }

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={findings}
      total={firstPage?.total}
      schema={firstPage?.query_schema}
      canonicalQuery={firstPage?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={query.error}
      context={{
        backLabel: "Review details",
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
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={(finding) => onOpenFinding(finding.id)}
      selection={{
        disabled: retryStatus.isPending,
        maximumSelected: 200,
        renderActions: selectionActions,
      }}
      beforeResults={
        firstPage ? (
          <>
            <RepositoryReviewFindingsProcessing
              counters={firstPage.findings_processing}
              historical={firstPage.historical_deduplication}
              retryingHistorical={retryHistorical.isPending}
              onRetryHistorical={() => retryHistorical.mutate()}
              onOpenRawFindings={onOpenRawFindings}
            />
            <div className="flex justify-end">
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={onOpenRepositoryFindings}
              >
                <IconExternalLink /> View repository findings
              </Button>
            </div>
          </>
        ) : undefined
      }
      emptyTitle={
        firstPage && isActive(firstPage.automation)
          ? "Review in progress"
          : "No findings"
      }
      emptyDescription={
        firstPage && isActive(firstPage.automation)
          ? "Findings will appear after the first validated checkpoint."
          : "This review has not completed a deduplicated finding. Raw findings remain available separately."
      }
    />
  )
}

function associationLabel(finding: RepositoryReviewRunFindingSummary): string {
  switch (finding.association) {
    case "new":
      return "New repository finding"
    case "existing":
      return "Existing repository finding"
    case "needs_review":
      return "Needs review"
    default:
      return runFindingStatusLabel(finding)
  }
}

function historicalReplayIsActive(historical?: {
  required: boolean
  status?: string
}): boolean {
  return Boolean(
    historical?.required &&
    new Set(["pending", "replaying", "merging"]).has(historical.status ?? ""),
  )
}

function findingLocation(finding: RepositoryReviewRunFindingSummary): string {
  return `${finding.path}${finding.line == null ? "" : `:${finding.line}`}${finding.symbol ? ` · ${finding.symbol}` : ""}`
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

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? value || "Not reported"
    : date.toLocaleString()
}

function boundedText(value: string, maximumBytes: number): string {
  const encoded = new TextEncoder().encode(value.trim())
  if (encoded.byteLength <= maximumBytes) return value.trim()
  return `${new TextDecoder().decode(encoded.slice(0, maximumBytes - 3))}…`
}
