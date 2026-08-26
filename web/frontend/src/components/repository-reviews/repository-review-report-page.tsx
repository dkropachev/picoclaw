import {
  IconMessageCircle,
  IconRefresh,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useMemo, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewFinding,
  type RepositoryReviewReportScope,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationFinding,
  getRepositoryReviewAutomationReport,
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

export function RepositoryReviewReportPage({
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
      "repository-review-report",
      automationID,
      search.scope,
      search.offset,
    ],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationReport(
        automationID,
        { scope: search.scope, offset: search.offset, limit: pageSize },
        signal,
      ),
    retry: false,
    refetchInterval: (current) =>
      current.state.data && isActive(current.state.data.automation)
        ? 2_000
        : false,
  })
  const report = query.data
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const generation = useMutation({
    mutationFn: async ({ custom }: { custom: boolean }) => {
      const generationID = createRepositoryReviewGenerationID()
      await generateRepositoryReviewIssues(automationID, {
        generation_id: generationID,
        finding_ids: [...state.selectedIDs],
        instructions_mode: custom ? "custom" : "default",
        ...(custom ? { instructions: customInstructions.trim() } : {}),
      })
      return generationID
    },
    onSuccess: (generationID) => {
      setInstructionsOpen(false)
      toast.success(
        "Issue preview generation finished. Review individual outcomes before publishing.",
      )
      onGenerated(generationID)
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Generation failed.",
      ),
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
        metadata:
          finding.message ||
          `${finding.models.length} model contributor${finding.models.length === 1 ? "" : "s"}`,
      }),
      columns: [],
      badges: [
        {
          id: "severity",
          label: (finding) => finding.severity,
          variant: "outline",
        },
        {
          id: "status",
          label: (finding) => finding.status,
          variant: "secondary",
        },
        {
          id: "association",
          label: (finding) =>
            finding.issue_draft_id ? "issue associated" : null,
          variant: "outline",
        },
      ],
    }),
    [],
  )

  const discuss = async () => {
    if (!report || state.selectedCount === 0) return
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
                `${report.automation.repository}: ${findings.length} review findings`,
                256,
              ),
        context: {
          repository: report.automation.repository,
          repository_review: automationID,
          finding_ids: findings.map((finding) => finding.id).join(","),
          context_ids: contextIDs.join(","),
          ...(report.repository?.last_commit_sha
            ? { commit: report.repository.last_commit_sha }
            : {}),
        },
        source_query: boundedText(
          `repository review ${report.automation.repository}`,
          256,
        ),
      })
      const sessionID = thread.ui_session_id || thread.id
      for (const detail of details) {
        const sent = await switchChatSessionAndSend(sessionID, {
          content: discussionPrompt(
            {
              id: automationID,
              repository: report.automation.repository,
              last_commit_sha: report.repository?.last_commit_sha,
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
        title="Repository review report"
        identity={
          report ? (
            <span className="text-muted-foreground truncate text-xs">
              {report.automation.repository}
            </span>
          ) : undefined
        }
        status={
          report ? (
            <Badge variant="outline">{report.automation.status}</Badge>
          ) : undefined
        }
        actions={
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            aria-label="Refresh review report"
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
        {report && (
          <div className="space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <label className="flex items-center gap-2 text-sm">
                <span className="font-medium">Finding scope</span>
                <select
                  aria-label="Finding scope"
                  className="border-input bg-background h-9 rounded-md border px-3 text-sm"
                  value={search.scope}
                  onChange={(event) =>
                    onSearchChange({
                      ...search,
                      scope: event.target.value as RepositoryReviewReportScope,
                      offset: 0,
                      generation_id: undefined,
                    })
                  }
                >
                  <option value="current">Current campaign</option>
                  <option value="all">All durable findings</option>
                </select>
              </label>
              <span className="text-muted-foreground text-sm">
                {report.total} finding{report.total === 1 ? "" : "s"}
              </span>
            </div>

            {state.selectedCount > 0 && (
              <div
                data-testid="repository-review-report-selection"
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
                  disabled={generation.isPending}
                  onClick={() => setInstructionsOpen(true)}
                >
                  <IconSparkles /> Generate issue previews
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

            {report.findings.length === 0 && isActive(report.automation) ? (
              <div className="border-border flex min-h-48 flex-col items-center justify-center rounded-lg border border-dashed p-6 text-center">
                <h2 className="font-semibold">Review in progress</h2>
                <p className="text-muted-foreground mt-2 text-sm">
                  The report is ready. Findings will appear after the first
                  durable checkpoint.
                </p>
              </div>
            ) : (
              <CollectionResults
                definition={definition}
                items={report.findings}
                view="list"
                selection={{
                  selectedIDs: state.selectedIDs,
                  additive: true,
                  maximumSelected: 200,
                  onSelectionChange: state.setSelection,
                }}
                onOpenItem={(finding) => onOpenFinding(finding.id)}
                emptyTitle="No durable findings"
                emptyDescription={
                  search.scope === "current"
                    ? "This campaign has not stored a validated finding."
                    : "This repository has no durable review findings."
                }
              />
            )}

            <ReportPagination
              offset={report.offset}
              total={report.total}
              nextOffset={report.next_offset}
              onChange={(offset) => onSearchChange({ ...search, offset })}
            />
          </div>
        )}
      </CollectionDetailShell>

      <Dialog open={instructionsOpen} onOpenChange={setInstructionsOpen}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              Generate {state.selectedCount} issue previews
            </DialogTitle>
            <DialogDescription>
              Each selected finding gets its own durable AI-written preview.
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
              {generation.isPending ? "Generating…" : "Generate previews"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function ReportPagination({
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

function boundedText(value: string, maximumBytes: number): string {
  const encoded = new TextEncoder().encode(value.trim())
  if (encoded.byteLength <= maximumBytes) return value.trim()
  return `${new TextDecoder().decode(encoded.slice(0, maximumBytes - 3))}…`
}
