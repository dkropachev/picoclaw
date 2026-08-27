import {
  IconBrandGithub,
  IconChecks,
  IconExternalLink,
  IconFileCode,
  IconMessageCircle,
  IconRefresh,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewFinding,
  type RepositoryReviewFindingContext,
  type RepositoryReviewFixEffortEstimate,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationFinding,
  postRepositoryReviewFinding,
  reserveRepositoryReviewValidations,
  resolveRepositoryReviewPossibleDuplicate,
  retryRepositoryReviewRunFindingStatuses,
  syncRepositoryReviewFinding,
  updateRepositoryReviewFindingLifecycle,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import { CollectionDetailShell } from "@/components/collection"
import {
  discussionPrompt,
  githubRepositoryPath,
} from "@/components/repository-reviews/repository-review-actions"
import {
  runFindingRepositoryFindingID,
  runFindingStatusCanRetry,
  runFindingStatusDescription,
  runFindingStatusIsInProgress,
  runFindingStatusLabel,
} from "@/components/repository-reviews/repository-review-run-finding-status"
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
import { removeItemFromCollectionSelectionMemory } from "@/hooks/use-collection-route-state"
import { threadOpenSessionIdAtom } from "@/store/threads"

import { createRepositoryReviewGenerationID } from "./repository-review-generation"

export function RepositoryReviewFindingPage({
  automationID,
  findingID,
  resourceKind,
  onBack,
  onOpenRepositoryFinding,
  onOpenIssue,
  onLinkIssue,
  onGenerated,
  onOpenThread,
  onRepositoryFindingReplaced,
}: {
  automationID: string
  findingID: string
  resourceKind: "run" | "repository"
  onBack: () => void
  onOpenRepositoryFinding: (findingID: string) => void
  onOpenIssue: (draftID: string) => void
  onLinkIssue: (findingID?: string) => void
  onGenerated: (generationID: string) => void
  onOpenThread: (threadID: string) => void
  onRepositoryFindingReplaced?: (repositoryFindingID: string) => void
}) {
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const [instructionsOpen, setInstructionsOpen] = useState(false)
  const [customInstructions, setCustomInstructions] = useState("")
  const query = useQuery({
    queryKey: ["repository-review-finding", automationID, findingID],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFinding(automationID, findingID, signal),
    retry: false,
    refetchInterval: (current) => {
      const currentDetail = current.state.data
      const shouldPoll =
        resourceKind === "run"
          ? Boolean(
              currentDetail?.finding &&
              runFindingStatusIsInProgress(currentDetail.finding),
            )
          : Boolean(
              currentDetail?.repository_finding &&
              new Set(["pending", "running"]).has(
                currentDetail.repository_finding.validation_state,
              ),
            )
      return shouldPoll ? 2_000 : false
    },
  })
  const detail =
    resourceKind === "run"
      ? query.data?.finding.id === findingID
        ? query.data
        : undefined
      : query.data?.repository_finding?.id === findingID
        ? query.data
        : undefined
  const finding = detail?.finding
  const repositoryFinding = detail?.repository_finding
  const isRepositoryResource = resourceKind === "repository"
  const actionFinding = detail?.action_finding ?? finding
  const actionFindingID = repositoryFinding ? actionFinding?.id : findingID
  const issueID =
    finding?.issue_draft_id ||
    detail?.occurrences?.find((occurrence) => occurrence.issue_draft_id)
      ?.issue_draft_id ||
    detail?.issue?.id
  const repositoryIssueURL = repositoryFinding?.issue.url
  const notFound =
    (query.error instanceof RepositoryReviewAPIError &&
      query.error.status === 404) ||
    Boolean(query.data && !detail)
  const generateMutation = useMutation({
    mutationFn: async ({ instructions }: { instructions: string }) => {
      const generationID = createRepositoryReviewGenerationID()
      const response = await generateRepositoryReviewIssues(automationID, {
        generation_id: generationID,
        finding_ids: actionFindingID ? [actionFindingID] : [],
        instructions_mode: instructions ? "custom" : "default",
        ...(instructions ? { instructions } : {}),
      })
      return { generationID, response }
    },
    onSuccess: ({ generationID, response }) => {
      const outcomes = [
        ...(response.results ?? []),
        ...(response.result ? [response.result] : []),
      ]
      const savedDraft =
        (response.issues?.length ?? 0) > 0 ||
        Boolean(response.issue || response.draft) ||
        outcomes.some((outcome) => Boolean(outcome.draft_id))
      const succeeded =
        outcomes.some((outcome) => outcome.success === true) ||
        (outcomes.length === 0 && savedDraft)
      if (!savedDraft) {
        toast.error(outcomes[0]?.message || "No issue preview was saved.")
        return
      }
      setInstructionsOpen(false)
      setCustomInstructions("")
      if (succeeded) {
        toast.success("Issue preview drafted.")
      } else {
        toast.error(
          outcomes[0]?.message ||
            "Issue drafting failed. Open the saved attempt to inspect and retry it.",
        )
      }
      onGenerated(generationID)
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Preview generation failed.",
      ),
  })
  const postMutation = useMutation({
    mutationFn: () =>
      postRepositoryReviewFinding(automationID, actionFindingID ?? "", {
        expected_version: actionFinding?.version ?? 0,
      }),
    onSuccess: async (response) => {
      await query.refetch()
      if (response.issue?.external_url) {
        toast.success("Issue posted to GitHub.")
      } else {
        toast.info("Posting is awaiting reconciliation.")
      }
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Post failed.")
      void query.refetch()
    },
  })
  const syncMutation = useMutation({
    mutationFn: () =>
      syncRepositoryReviewFinding(automationID, repositoryFinding?.id ?? ""),
    onSuccess: async () => {
      await query.refetch()
      toast.success("GitHub issue state synchronized.")
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : "Sync failed."),
  })
  const validationMutation = useMutation({
    mutationFn: async () => {
      if (
        repositoryFinding?.issue.url &&
        (!repositoryFinding.issue.snapshot_at ||
          Date.now() -
            new Date(repositoryFinding.issue.snapshot_at).valueOf() >=
            15 * 60 * 1_000)
      ) {
        await syncRepositoryReviewFinding(automationID, repositoryFinding.id)
      }
      return reserveRepositoryReviewValidations(automationID, [
        repositoryFinding?.id ?? "",
      ])
    },
    onSuccess: async () => {
      await query.refetch()
      toast.success("Resolution validation queued.")
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Validation could not be queued.",
      )
      void query.refetch()
    },
  })
  const duplicateMutation = useMutation({
    mutationFn: ({
      candidateID,
      decision,
      candidateVersion,
    }: {
      candidateID: string
      decision: "merge" | "distinct"
      candidateVersion?: number
    }) =>
      resolveRepositoryReviewPossibleDuplicate(
        automationID,
        repositoryFinding?.id ?? "",
        {
          candidate_id: candidateID,
          decision,
          expected_provisional_version: repositoryFinding?.version ?? 0,
          ...(candidateVersion == null
            ? {}
            : { expected_candidate_version: candidateVersion }),
        },
      ),
    onSuccess: async (response) => {
      if (
        response.repository_finding?.id &&
        response.repository_finding.id !== repositoryFinding?.id
      ) {
        if (repositoryFinding?.id) {
          removeItemFromCollectionSelectionMemory(
            repositoryFindingSelectionKey(automationID),
            repositoryFinding.id,
          )
        }
        onRepositoryFindingReplaced?.(response.repository_finding.id)
        toast.success("Possible duplicate merged.")
        return
      }
      await query.refetch()
      toast.success("Possible duplicate resolved.")
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Duplicate decision failed.",
      ),
  })
  const lifecycleMutation = useMutation({
    mutationFn: (lifecycle: "open" | "dismissed") =>
      updateRepositoryReviewFindingLifecycle(
        automationID,
        repositoryFinding?.id ?? "",
        {
          lifecycle,
          expected_version: repositoryFinding?.version ?? 0,
        },
      ),
    onSuccess: async (_response, lifecycle) => {
      if (lifecycle === "dismissed" && repositoryFinding?.id) {
        removeItemFromCollectionSelectionMemory(
          repositoryFindingSelectionKey(automationID),
          repositoryFinding.id,
        )
      }
      await query.refetch()
      toast.success("Repository finding lifecycle updated.")
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Lifecycle update failed.",
      ),
  })
  const retryStatusMutation = useMutation({
    mutationFn: () =>
      retryRepositoryReviewRunFindingStatuses(automationID, [findingID]),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Run finding status queued.")
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
  useEffect(() => {
    if (
      !isRepositoryResource ||
      !repositoryFinding?.issue.url ||
      repositoryFinding.issue.conflict ||
      repositoryFinding.issue.state === "none" ||
      (repositoryFinding.issue.snapshot_at &&
        Date.now() - new Date(repositoryFinding.issue.snapshot_at).valueOf() <
          15 * 60 * 1_000)
    ) {
      return
    }
    syncMutation.mutate()
    // Snapshot identity is the refresh fence; mutation state must not retrigger
    // a failed provider call in a render loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    isRepositoryResource,
    repositoryFinding?.id,
    repositoryFinding?.issue.snapshot_at,
  ])
  const capabilities = detail?.capabilities
  const github =
    capabilities?.github ??
    Boolean(detail && githubRepositoryPath(detail.automation.repository))
  const canGenerate =
    capabilities?.can_generate ??
    Boolean(finding?.status === "open" && !issueID)
  const canLink =
    capabilities?.can_link_issue ??
    Boolean(
      github &&
      finding?.status === "open" &&
      !issueID &&
      repositoryFinding?.match_state !== "provisional",
    )
  const contexts = useMemo(
    () =>
      new Map((detail?.contexts ?? []).map((context) => [context.id, context])),
    [detail?.contexts],
  )
  const associatedIssueOccurrences = useMemo(() => {
    const byDraft = new Map<string, RepositoryReviewFinding>()
    for (const occurrence of detail?.occurrences ?? []) {
      if (
        occurrence.issue_draft_id &&
        !byDraft.has(occurrence.issue_draft_id)
      ) {
        byDraft.set(occurrence.issue_draft_id, occurrence)
      }
    }
    return [...byDraft.values()]
  }, [detail?.occurrences])

  const discuss = async () => {
    if (!detail || !finding) return
    try {
      const thread = await createThread({
        type: "reviewing",
        title: boundedText(finding.title, 256),
        context: {
          repository: detail.automation.repository,
          repository_review: automationID,
          finding_ids: finding.id,
          context_ids: finding.context_ids.join(","),
          ...(finding.commit_sha ? { commit: finding.commit_sha } : {}),
        },
        source_query: boundedText(
          `repository review ${detail.automation.repository}`,
          256,
        ),
      })
      const sessionID = thread.ui_session_id || thread.id
      const sent = await switchChatSessionAndSend(sessionID, {
        content: discussionPrompt(
          {
            id: automationID,
            repository: detail.automation.repository,
            last_commit_sha: detail.repository?.last_commit_sha,
            contexts: detail.contexts,
          },
          [finding],
        ),
      })
      if (!sent) {
        await dropThread(thread.id).catch(() => undefined)
        throw new Error("The finding discussion could not be started.")
      }
      setThreadOpenSessionID(sessionID)
      onOpenThread(sessionID)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Discussion failed.")
    }
  }

  return (
    <CollectionDetailShell
      title={
        (isRepositoryResource
          ? repositoryFinding?.canonical_title
          : undefined) ||
        finding?.title ||
        (isRepositoryResource ? "Repository finding" : "Run finding")
      }
      identity={
        <span
          className="block max-w-40 truncate font-mono text-xs sm:max-w-72"
          title={findingID}
        >
          {findingID}
        </span>
      }
      status={
        isRepositoryResource && repositoryFinding ? (
          <div className="flex items-center gap-2">
            <Badge
              variant={severityVariant(repositoryFinding.canonical_severity)}
            >
              {repositoryFinding.canonical_severity}
            </Badge>
            <Badge variant="outline">{repositoryFinding.lifecycle}</Badge>
            <Badge variant="secondary">{repositoryFinding.match_state}</Badge>
          </div>
        ) : finding ? (
          <div className="flex items-center gap-2">
            <Badge
              variant={severityVariant(finding.severity)}
              className={
                isHighSeverity(finding.severity)
                  ? "bg-destructive text-destructive-foreground dark:bg-destructive dark:text-destructive-foreground"
                  : undefined
              }
            >
              {finding.severity}
            </Badge>
            <Badge variant="outline">{finding.status}</Badge>
          </div>
        ) : undefined
      }
      actions={
        finding && !isRepositoryResource ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => void discuss()}
          >
            <IconMessageCircle /> Discuss with AI
          </Button>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Findings"
    >
      {detail && finding && (
        <div className="space-y-6">
          {!isRepositoryResource && (
            <section
              aria-labelledby="run-finding-status"
              className="border-border space-y-3 rounded-lg border p-4"
            >
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 id="run-finding-status" className="font-semibold">
                    Run finding status
                  </h2>
                  <p className="text-muted-foreground mt-1 text-sm">
                    {runFindingStatusDescription(finding)}
                  </p>
                </div>
                <Badge variant="outline">
                  {runFindingStatusLabel(finding)}
                </Badge>
              </div>
              <div className="flex flex-wrap gap-2">
                {runFindingRepositoryFindingID(finding) && (
                  <Button
                    type="button"
                    size="sm"
                    onClick={() =>
                      onOpenRepositoryFinding(
                        runFindingRepositoryFindingID(finding)!,
                      )
                    }
                  >
                    <IconExternalLink /> Open repository finding
                  </Button>
                )}
                {runFindingStatusCanRetry(finding) && (
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={retryStatusMutation.isPending}
                    onClick={() => retryStatusMutation.mutate()}
                  >
                    <IconRefresh />
                    {retryStatusMutation.isPending
                      ? "Retrying…"
                      : "Retry status"}
                  </Button>
                )}
              </div>
            </section>
          )}

          {isRepositoryResource && repositoryFinding && (
            <>
              <section
                aria-labelledby="repository-finding-lifecycle"
                className="space-y-3"
              >
                <h2 id="repository-finding-lifecycle" className="font-semibold">
                  Repository lifecycle
                </h2>
                <dl className="border-border grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
                  <ObservationRow
                    label="Match"
                    value={repositoryFinding.match_state}
                  />
                  <ObservationRow
                    label="Lifecycle"
                    value={repositoryFinding.lifecycle}
                  />
                  <ObservationRow
                    label="GitHub issue"
                    value={repositoryFinding.issue.state}
                  />
                  <ObservationRow
                    label="Validation"
                    value={repositoryFinding.validation_state}
                  />
                  <ObservationRow
                    label="Found commits"
                    value={repositoryFinding.found_commits.join(", ") || "None"}
                    mono
                  />
                  <ObservationRow
                    label="Fix commit"
                    value={repositoryFinding.fix_commit_sha || "Not confirmed"}
                    mono={Boolean(repositoryFinding.fix_commit_sha)}
                  />
                  <ObservationRow
                    label="Fix date"
                    value={
                      repositoryFinding.fix_commit_time
                        ? formatTimestamp(repositoryFinding.fix_commit_time)
                        : "Not confirmed"
                    }
                  />
                  <ObservationRow
                    label="First version"
                    value={
                      repositoryFinding.first_containing_tag || "Not identified"
                    }
                  />
                </dl>
                {repositoryFinding.issue.conflict && (
                  <div
                    role="alert"
                    className="border-destructive/50 bg-destructive/5 rounded-lg border p-4 text-sm"
                  >
                    <h3 className="font-medium">
                      Manual GitHub association conflict
                    </h3>
                    <p className="text-muted-foreground mt-1">
                      Merged occurrences reference different issues. Neither
                      association was discarded.
                    </p>
                    <ul className="mt-3 space-y-1">
                      {repositoryFinding.issue.conflict_urls?.map((url) => (
                        <li key={url}>
                          <a
                            className="break-all underline underline-offset-2"
                            href={url}
                            target="_blank"
                            rel="noreferrer"
                          >
                            {url}
                          </a>
                        </li>
                      ))}
                    </ul>
                    <div className="mt-3 flex flex-wrap gap-2">
                      {associatedIssueOccurrences.map((occurrence, index) => (
                        <Button
                          key={occurrence.issue_draft_id}
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            onOpenIssue(occurrence.issue_draft_id!)
                          }
                          aria-label={`Manage issue record for ${occurrence.id}`}
                          title={occurrence.id}
                        >
                          Manage issue {index + 1}
                        </Button>
                      ))}
                    </div>
                  </div>
                )}
                <div className="flex flex-wrap gap-2">
                  {repositoryFinding.issue.url &&
                    !repositoryFinding.issue.conflict && (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={syncMutation.isPending}
                        onClick={() => syncMutation.mutate()}
                      >
                        <IconRefresh />
                        {syncMutation.isPending ? "Syncing…" : "Sync GitHub"}
                      </Button>
                    )}
                  {repositoryFinding.match_state !== "provisional" &&
                    !repositoryFinding.issue.conflict &&
                    repositoryFinding.lifecycle !== "dismissed" && (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={
                          validationMutation.isPending || syncMutation.isPending
                        }
                        onClick={() => validationMutation.mutate()}
                      >
                        <IconChecks />
                        {validationMutation.isPending
                          ? "Queueing…"
                          : "Validate resolution"}
                      </Button>
                    )}
                </div>
              </section>

              {repositoryFinding.match_hints && (
                <section
                  aria-labelledby="repository-match-hints"
                  className="space-y-3"
                >
                  <h2 id="repository-match-hints" className="font-semibold">
                    Causal identity hints
                  </h2>
                  <dl className="border-border divide-border rounded-lg border">
                    <FindingText
                      label="Component"
                      value={
                        repositoryFinding.match_hints.component || "Unknown"
                      }
                    />
                    <FindingText
                      label="Operation"
                      value={
                        repositoryFinding.match_hints.operation || "Unknown"
                      }
                    />
                    <FindingText
                      label="Failure mode"
                      value={
                        repositoryFinding.match_hints.failure_mode || "Unknown"
                      }
                    />
                    <FindingText
                      label="Trigger"
                      value={repositoryFinding.match_hints.trigger || "Unknown"}
                    />
                    <FindingText
                      label="Violated invariant"
                      value={
                        repositoryFinding.match_hints.violated_invariant ||
                        "Unknown"
                      }
                    />
                    <FindingText
                      label="Outcome"
                      value={
                        repositoryFinding.match_hints.observable_outcome ||
                        "Unknown"
                      }
                    />
                    <FindingText
                      label="Related symbols"
                      value={
                        repositoryFinding.match_hints.related_symbols.join(
                          ", ",
                        ) || "Unknown"
                      }
                    />
                    <FindingText
                      label="Source anchors"
                      value={
                        repositoryFinding.match_hints.source_anchors.join(
                          ", ",
                        ) || "Unknown"
                      }
                    />
                    <FindingText
                      label="Distinguishing facts"
                      value={
                        repositoryFinding.match_hints.distinguishing_facts.join(
                          "\n",
                        ) || "Unknown"
                      }
                    />
                  </dl>
                </section>
              )}

              {repositoryFinding.fix_effort && (
                <section
                  aria-labelledby="repository-fix-effort"
                  className="space-y-3"
                >
                  <h2 id="repository-fix-effort" className="font-semibold">
                    Estimated fix effort
                  </h2>
                  <div className="grid gap-3 md:grid-cols-2">
                    <EffortCard
                      label="Quick containment"
                      effort={repositoryFinding.fix_effort.quick}
                    />
                    <EffortCard
                      label="Best-quality correction"
                      effort={repositoryFinding.fix_effort.quality}
                    />
                  </div>
                </section>
              )}

              <section
                aria-labelledby="repository-occurrences"
                className="space-y-3"
              >
                <h2 id="repository-occurrences" className="font-semibold">
                  Occurrence history
                </h2>
                <div className="space-y-2">
                  {repositoryFinding.path_symbol_history.map((history) => (
                    <article
                      key={`${history.review_finding_id}:${history.commit_sha}:${history.path}`}
                      className="border-border rounded-lg border p-3 text-sm"
                    >
                      <code className="break-all">{history.path}</code>
                      {history.symbol && (
                        <span className="text-muted-foreground ml-2">
                          {history.symbol}
                        </span>
                      )}
                      <p className="text-muted-foreground mt-1 font-mono text-xs break-all">
                        {history.commit_sha} ·{" "}
                        {formatTimestamp(history.observed_at)}
                      </p>
                    </article>
                  ))}
                </div>
              </section>

              {(repositoryFinding.possible_duplicates?.length ?? 0) > 0 && (
                <section
                  aria-labelledby="repository-duplicates"
                  className="space-y-3"
                >
                  <h2 id="repository-duplicates" className="font-semibold">
                    Possible duplicates
                  </h2>
                  {repositoryFinding.possible_duplicates?.map((duplicate) => (
                    <article
                      key={duplicate.candidate_id}
                      className="border-border rounded-lg border p-3 text-sm"
                    >
                      <strong className="font-mono">
                        {duplicate.candidate_id}
                      </strong>
                      <span className="text-muted-foreground ml-2">
                        {duplicate.relation} ·{" "}
                        {(duplicate.confidence * 100).toFixed(0)}%
                      </span>
                      {duplicate.explanation && (
                        <p className="mt-2">{duplicate.explanation}</p>
                      )}
                      {(duplicate.matching_anchors?.length ?? 0) > 0 && (
                        <p className="text-muted-foreground mt-2 break-words">
                          Matching anchors:{" "}
                          {duplicate.matching_anchors?.join(", ")}
                        </p>
                      )}
                      {(duplicate.conflicting_anchors?.length ?? 0) > 0 && (
                        <p className="text-destructive mt-1 break-words">
                          Conflicting anchors:{" "}
                          {duplicate.conflicting_anchors?.join(", ")}
                        </p>
                      )}
                      {repositoryFinding.match_state === "provisional" && (
                        <div className="mt-3 flex gap-2">
                          <Button
                            type="button"
                            size="sm"
                            disabled={
                              duplicateMutation.isPending ||
                              !detail.possible_duplicate_findings?.some(
                                (candidate) =>
                                  candidate.id === duplicate.candidate_id,
                              )
                            }
                            onClick={() => {
                              const candidate =
                                detail.possible_duplicate_findings?.find(
                                  (item) => item.id === duplicate.candidate_id,
                                )
                              duplicateMutation.mutate({
                                candidateID: duplicate.candidate_id,
                                candidateVersion: candidate?.version,
                                decision: "merge",
                              })
                            }}
                          >
                            Merge
                          </Button>
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            disabled={duplicateMutation.isPending}
                            onClick={() =>
                              duplicateMutation.mutate({
                                candidateID: duplicate.candidate_id,
                                decision: "distinct",
                              })
                            }
                          >
                            Distinct
                          </Button>
                        </div>
                      )}
                    </article>
                  ))}
                </section>
              )}

              {(repositoryFinding.resolution_history?.length ?? 0) > 0 && (
                <section
                  aria-labelledby="repository-resolution-history"
                  className="space-y-3"
                >
                  <h2
                    id="repository-resolution-history"
                    className="font-semibold"
                  >
                    Resolution history
                  </h2>
                  {repositoryFinding.resolution_history?.map(
                    (resolution, index) => (
                      <article
                        key={`${resolution.validated_at}:${index}`}
                        className="border-border rounded-lg border p-3 text-sm"
                      >
                        <strong>{resolution.outcome}</strong>
                        <span className="text-muted-foreground ml-2">
                          {formatTimestamp(resolution.validated_at)}
                        </span>
                        {resolution.fix_commit_sha && (
                          <p className="mt-1 font-mono text-xs break-all">
                            {resolution.fix_commit_sha}
                            {resolution.first_containing_tag
                              ? ` · ${resolution.first_containing_tag}`
                              : ""}
                          </p>
                        )}
                        {resolution.summary && (
                          <p className="mt-2">{resolution.summary}</p>
                        )}
                      </article>
                    ),
                  )}
                </section>
              )}
              {repositoryFinding.match_state !== "provisional" &&
                !new Set(["pending", "running"]).has(
                  repositoryFinding.validation_state,
                ) &&
                (repositoryFinding.lifecycle === "dismissed" ||
                  repositoryFinding.issue.state === "none") &&
                (repositoryFinding.lifecycle === "open" ||
                  repositoryFinding.lifecycle === "regressed" ||
                  repositoryFinding.lifecycle === "dismissed") && (
                  <div className="border-border flex justify-end border-t pt-4">
                    <Button
                      type="button"
                      variant="outline"
                      disabled={lifecycleMutation.isPending}
                      onClick={() =>
                        lifecycleMutation.mutate(
                          repositoryFinding.lifecycle === "dismissed"
                            ? "open"
                            : "dismissed",
                        )
                      }
                    >
                      {repositoryFinding.lifecycle === "dismissed"
                        ? "Reopen repository finding"
                        : "Dismiss repository finding"}
                    </Button>
                  </div>
                )}
            </>
          )}

          {isRepositoryResource && (
            <section className="border-border space-y-3 rounded-lg border p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h2 className="font-semibold">Canonical issue association</h2>
                  <p className="text-muted-foreground mt-1 text-sm">
                    {repositoryIssueURL
                      ? `This repository finding is associated with a ${repositoryFinding?.issue.origin || "verified"} GitHub issue.`
                      : issueID
                        ? "This finding has one saved preview or linked GitHub issue."
                        : "No issue preview or existing issue is associated yet."}
                  </p>
                </div>
                <div className="flex flex-wrap gap-2">
                  {repositoryIssueURL ? (
                    <>
                      <Button type="button" size="sm" asChild>
                        <a
                          href={repositoryIssueURL}
                          target="_blank"
                          rel="noreferrer"
                        >
                          <IconBrandGithub /> Open GitHub issue
                        </a>
                      </Button>
                      {issueID && (
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() => onOpenIssue(issueID)}
                        >
                          Manage association
                        </Button>
                      )}
                    </>
                  ) : issueID ? (
                    <Button
                      type="button"
                      size="sm"
                      onClick={() => onOpenIssue(issueID)}
                    >
                      <IconBrandGithub /> Open issue record
                    </Button>
                  ) : (
                    <>
                      {canGenerate && (
                        <>
                          <Button
                            type="button"
                            size="sm"
                            disabled={
                              generateMutation.isPending ||
                              postMutation.isPending
                            }
                            onClick={() => setInstructionsOpen(true)}
                          >
                            <IconSparkles />
                            {generateMutation.isPending
                              ? "Drafting…"
                              : "Draft issue"}
                          </Button>
                          {github && (
                            <Button
                              type="button"
                              size="sm"
                              disabled={
                                postMutation.isPending ||
                                generateMutation.isPending
                              }
                              onClick={() => postMutation.mutate()}
                            >
                              <IconBrandGithub />
                              {postMutation.isPending
                                ? "Posting…"
                                : "Post issue"}
                            </Button>
                          )}
                        </>
                      )}
                      {canLink && (
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() => onLinkIssue(actionFindingID)}
                        >
                          <IconBrandGithub /> Link existing issue
                        </Button>
                      )}
                    </>
                  )}
                </div>
              </div>
            </section>
          )}

          <section aria-labelledby="finding-location" className="space-y-3">
            <h2 id="finding-location" className="font-semibold">
              Location and provenance
            </h2>
            <div className="border-border bg-muted/20 grid gap-2 rounded-lg border p-4 text-sm">
              <p className="flex min-w-0 items-center gap-2">
                <IconFileCode className="text-muted-foreground size-4 shrink-0" />
                <code className="break-all">
                  {finding.file.path}
                  {finding.line == null ? "" : `:${finding.line}`}
                </code>
              </p>
              {finding.symbol && (
                <HashLine label="Symbol" value={finding.symbol} mono />
              )}
              <HashLine label="Commit SHA" value={finding.commit_sha} mono />
              <HashLine label="Blob SHA" value={finding.file.blob_sha} mono />
              <HashLine
                label="Exact size"
                value={`${formatBytes(finding.file.size_bytes)} (${finding.file.size_bytes} bytes)`}
              />
            </div>
          </section>

          <section aria-labelledby="finding-diagnosis" className="space-y-3">
            <h2 id="finding-diagnosis" className="font-semibold">
              Grounded diagnosis
            </h2>
            <dl className="border-border divide-border rounded-lg border">
              <FindingText
                label="Mechanism"
                value={finding.message || finding.evidence}
              />
              <FindingText label="Evidence" value={finding.evidence} />
              <FindingText label="Impact" value={finding.impact} />
              <FindingText
                label="Validation"
                value={finding.validation.summary}
              />
            </dl>
            {(finding.validation.checks?.length ?? 0) > 0 && (
              <div className="border-border rounded-lg border p-4 text-sm">
                <h3 className="flex items-center gap-2 font-medium">
                  <IconChecks className="size-4" /> Validation checks
                </h3>
                <ul className="text-muted-foreground mt-2 list-inside list-disc">
                  {finding.validation.checks?.map((check) => (
                    <li key={check}>{check}</li>
                  ))}
                </ul>
              </div>
            )}
          </section>

          {!isRepositoryResource && finding.match_hints && (
            <section
              aria-labelledby="occurrence-match-hints"
              className="space-y-3"
            >
              <h2 id="occurrence-match-hints" className="font-semibold">
                Causal identity hints
              </h2>
              <dl className="border-border divide-border rounded-lg border">
                <FindingText
                  label="Component"
                  value={finding.match_hints.component || "Unknown"}
                />
                <FindingText
                  label="Operation"
                  value={finding.match_hints.operation || "Unknown"}
                />
                <FindingText
                  label="Failure mode"
                  value={finding.match_hints.failure_mode || "Unknown"}
                />
                <FindingText
                  label="Trigger"
                  value={finding.match_hints.trigger || "Unknown"}
                />
                <FindingText
                  label="Violated invariant"
                  value={finding.match_hints.violated_invariant || "Unknown"}
                />
                <FindingText
                  label="Outcome"
                  value={finding.match_hints.observable_outcome || "Unknown"}
                />
                <FindingText
                  label="Related symbols"
                  value={
                    finding.match_hints.related_symbols.join(", ") || "Unknown"
                  }
                />
                <FindingText
                  label="Source anchors"
                  value={
                    finding.match_hints.source_anchors.join(", ") || "Unknown"
                  }
                />
                <FindingText
                  label="Distinguishing facts"
                  value={
                    finding.match_hints.distinguishing_facts.join("\n") ||
                    "Unknown"
                  }
                />
              </dl>
            </section>
          )}

          {!isRepositoryResource && finding.fix_effort && (
            <section
              aria-labelledby="occurrence-fix-effort"
              className="space-y-3"
            >
              <h2 id="occurrence-fix-effort" className="font-semibold">
                Estimated fix effort
              </h2>
              <div className="grid gap-3 md:grid-cols-2">
                <EffortCard
                  label="Quick containment"
                  effort={finding.fix_effort.quick}
                />
                <EffortCard
                  label="Best-quality correction"
                  effort={finding.fix_effort.quality}
                />
              </div>
            </section>
          )}

          <section aria-labelledby="finding-consensus" className="space-y-3">
            <h2 id="finding-consensus" className="font-semibold">
              Model consensus and observations
            </h2>
            <div className="flex flex-wrap gap-2">
              {finding.models.map((model) => (
                <Badge key={model} variant="outline">
                  {model}
                </Badge>
              ))}
              <span className="text-muted-foreground self-center text-xs">
                {finding.observation_count} validated observation
                {finding.observation_count === 1 ? "" : "s"}
              </span>
            </div>
            {(finding.observations?.length ?? 0) > 0 && (
              <div className="space-y-3">
                {finding.observations?.map((observation, index) => (
                  <article
                    key={`${observation.context_id}:${observation.model}:${index}`}
                    className="border-border rounded-lg border p-4 text-sm"
                  >
                    <div className="flex flex-wrap gap-2">
                      <strong>{observation.model}</strong>
                      <Badge variant="outline">{observation.severity}</Badge>
                      {observation.reviewer && (
                        <span className="text-muted-foreground text-xs">
                          reviewer {observation.reviewer}
                        </span>
                      )}
                    </div>
                    <dl className="mt-3 grid gap-2 text-sm">
                      <ObservationRow label="Title" value={observation.title} />
                      <ObservationRow
                        label="Location"
                        value={`${finding.file.path}${observation.line == null ? "" : `:${observation.line}`}`}
                        mono
                      />
                      {observation.symbol && (
                        <ObservationRow
                          label="Symbol"
                          value={observation.symbol}
                          mono
                        />
                      )}
                      {observation.message && (
                        <ObservationRow
                          label="Mechanism"
                          value={observation.message}
                        />
                      )}
                      <ObservationRow
                        label="Evidence"
                        value={observation.evidence}
                      />
                      <ObservationRow
                        label="Impact"
                        value={observation.impact}
                      />
                      <ObservationRow
                        label="Validation"
                        value={`${observation.validation.status} — ${observation.validation.summary}`}
                      />
                    </dl>
                    {(observation.validation.checks?.length ?? 0) > 0 && (
                      <ul className="text-muted-foreground mt-3 list-inside list-disc text-xs">
                        {observation.validation.checks?.map((check) => (
                          <li key={check}>{check}</li>
                        ))}
                      </ul>
                    )}
                  </article>
                ))}
              </div>
            )}
          </section>

          <section aria-labelledby="finding-contexts" className="space-y-3">
            <h2 id="finding-contexts" className="font-semibold">
              Immutable contexts
            </h2>
            {finding.context_ids.map((contextID) => (
              <FindingContextCard
                key={contextID}
                contextID={contextID}
                context={contexts.get(contextID)}
              />
            ))}
          </section>
        </div>
      )}
      {isRepositoryResource && (
        <Dialog
          open={instructionsOpen}
          onOpenChange={(open) => {
            if (generateMutation.isPending) return
            setInstructionsOpen(open)
            if (!open) setCustomInstructions("")
          }}
        >
          <DialogContent className="sm:max-w-xl">
            <DialogHeader>
              <DialogTitle>Draft issue</DialogTitle>
              <DialogDescription>
                Generate an editable issue preview. Optional instructions apply
                only to this preview and may change presentation, not the
                diagnosis.
              </DialogDescription>
            </DialogHeader>
            <label
              htmlFor="repository-review-finding-generation-instructions"
              className="space-y-2 text-sm"
            >
              <span className="font-medium">
                Custom presentation instructions
              </span>
              <Textarea
                id="repository-review-finding-generation-instructions"
                value={customInstructions}
                className="min-h-28"
                placeholder="Leave blank to use the profile's default issue format."
                onChange={(event) => setCustomInstructions(event.target.value)}
              />
            </label>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={generateMutation.isPending}
                onClick={() => {
                  setInstructionsOpen(false)
                  setCustomInstructions("")
                }}
              >
                Cancel
              </Button>
              <Button
                type="button"
                disabled={generateMutation.isPending || !actionFindingID}
                onClick={() =>
                  generateMutation.mutate({
                    instructions: customInstructions.trim(),
                  })
                }
              >
                <IconSparkles />
                {generateMutation.isPending ? "Drafting…" : "Draft preview"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </CollectionDetailShell>
  )
}

function repositoryFindingSelectionKey(automationID: string): string {
  return `repository-review-repository-findings:${automationID}`
}

function FindingContextCard({
  contextID,
  context,
}: {
  contextID: string
  context?: RepositoryReviewFindingContext
}) {
  return (
    <article className="border-border rounded-lg border p-4 text-sm">
      <code className="text-xs break-all">{contextID}</code>
      {!context ? (
        <p className="text-muted-foreground mt-2 text-sm">
          Context metadata is unavailable in this projection.
        </p>
      ) : (
        <div className="mt-3 space-y-3">
          <div className="grid gap-1 text-xs sm:grid-cols-2">
            <span>Model: {context.model}</span>
            <span>Reviewer: {context.reviewer || "Not reported"}</span>
            <span>Run: {context.run_id}</span>
            <span>Recorded: {formatTimestamp(context.created_at)}</span>
            <span className="break-all">Commit: {context.commit_sha}</span>
            <span className="break-all">
              Inventory: {context.inventory_hash}
            </span>
            <span className="break-all">
              Profile: {context.profile_hash || "Unavailable"}
            </span>
            <span className="break-all">
              Raw digest: {context.raw_digest || "Not retained"}
            </span>
          </div>
          <ul className="border-border divide-border rounded-md border">
            {context.files.map((file) => (
              <li
                key={`${file.path}:${file.blob_sha}`}
                className="border-b px-3 py-2 last:border-b-0"
              >
                <code className="block text-xs break-all">{file.path}</code>
                <span className="text-muted-foreground mt-1 block text-xs break-all">
                  blob {file.blob_sha} · {file.size_bytes} bytes
                  {file.category ? ` · ${file.category}` : ""}
                  {file.mode ? ` · mode ${file.mode}` : ""}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </article>
  )
}

function EffortCard({
  label,
  effort,
}: {
  label: string
  effort: RepositoryReviewFixEffortEstimate
}) {
  const known = effort.loc_max > 0 && Boolean(effort.class)
  return (
    <article className="border-border rounded-lg border p-4 text-sm">
      <div className="flex items-center justify-between gap-3">
        <h3 className="font-medium">{label}</h3>
        <Badge variant="outline">{known ? effort.class : "unknown"}</Badge>
      </div>
      <p className="mt-2 font-mono text-xs">
        {known
          ? `${effort.loc_min}–${effort.loc_max} changed LOC`
          : "Estimate unavailable"}
      </p>
      <p className="text-muted-foreground mt-2">
        {effort.rationale || "This legacy finding predates effort estimation."}
      </p>
    </article>
  )
}

function FindingText({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 border-b px-4 py-3 last:border-b-0 sm:grid-cols-[9rem_minmax(0,1fr)]">
      <dt className="font-medium">{label}</dt>
      <dd className="text-muted-foreground whitespace-pre-wrap">{value}</dd>
    </div>
  )
}

function ObservationRow({
  label,
  value,
  mono = false,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="grid gap-1 sm:grid-cols-[7rem_minmax(0,1fr)]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd
        className={mono ? "font-mono text-xs break-all" : "whitespace-pre-wrap"}
      >
        {value}
      </dd>
    </div>
  )
}

function HashLine({
  label,
  value,
  mono = false,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <p className="grid min-w-0 gap-1 sm:grid-cols-[8rem_minmax(0,1fr)]">
      <span className="text-muted-foreground">{label}</span>
      {mono ? <code className="break-all">{value}</code> : <span>{value}</span>}
    </p>
  )
}

function severityVariant(severity: string): "destructive" | "outline" {
  return isHighSeverity(severity) ? "destructive" : "outline"
}

function isHighSeverity(severity: string): boolean {
  return severity === "critical" || severity === "high"
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(2)} KiB`
  return `${(value / (1024 * 1024)).toFixed(2)} MiB`
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
