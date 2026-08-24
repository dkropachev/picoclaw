import {
  IconBrandGithub,
  IconBug,
  IconChecks,
  IconFileCode,
  IconMessageCircle,
  IconRefresh,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { type ReactNode, useEffect, useMemo, useState } from "react"

import {
  type RepositoryReviewFinding,
  type RepositoryReviewFindingContext,
  type RepositoryReviewFindingStatus,
  type RepositoryReviewIssueDraft,
  type RepositoryReviewState,
  type RepositoryReviewSummary,
  createRepositoryReviewIssueDraft,
  getRepositoryReview,
  listRepositoryReviews,
  publishRepositoryReviewIssueDraft,
  updateRepositoryReviewFinding,
  updateRepositoryReviewIssueDraft,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import { PageHeader } from "@/components/page-header"
import {
  discussionPrompt,
  githubNewIssueURL,
  githubRepositoryPath,
} from "@/components/repository-reviews/repository-review-actions"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import { threadOpenSessionIdAtom } from "@/store/threads"

const repositoryReviewsKey = ["repository-reviews"] as const
const maximumSelectedFindings = 200
const maximumDiscussionFindings = 20
// One finding can legitimately carry 64 bounded context manifests with three
// 4-KiB paths each; this envelope guarantees the required one-finding chat.
const maximumDiscussionPromptBytes = 2 << 20
const repositoryReviewKey = (repositoryID: string) => [
  "repository-reviews",
  repositoryID,
]
const repositoryReviewDetailKey = (
  repositoryID: string,
  findingOffset: number,
  draftOffset: number,
) => [...repositoryReviewKey(repositoryID), findingOffset, draftOffset]

export function RepositoryReviewsPage({
  onOpenThread,
}: {
  onOpenThread: (threadID: string) => void
}) {
  const queryClient = useQueryClient()
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const [repositoryID, setRepositoryID] = useState("")
  const [findingOffset, setFindingOffset] = useState(0)
  const [draftOffset, setDraftOffset] = useState(0)
  const [selectedFindingIDs, setSelectedFindingIDs] = useState<Set<string>>(
    () => new Set(),
  )
  const [activeDraftID, setActiveDraftID] = useState("")
  const [actionError, setActionError] = useState("")

  const repositoriesQuery = useQuery({
    queryKey: repositoryReviewsKey,
    queryFn: ({ signal }) => listRepositoryReviews(signal),
  })
  const repositories = useMemo(
    () => repositoriesQuery.data?.repositories ?? [],
    [repositoriesQuery.data?.repositories],
  )

  useEffect(() => {
    if (
      repositories.length > 0 &&
      !repositories.some((repository) => repository.id === repositoryID)
    ) {
      setRepositoryID(repositories[0].id)
    }
  }, [repositories, repositoryID])

  useEffect(() => {
    setSelectedFindingIDs(new Set())
    setFindingOffset(0)
    setDraftOffset(0)
    setActiveDraftID("")
    setActionError("")
  }, [repositoryID])

  useEffect(() => {
    setSelectedFindingIDs(new Set())
  }, [findingOffset])

  const repositoryQuery = useQuery({
    queryKey: repositoryReviewDetailKey(
      repositoryID,
      findingOffset,
      draftOffset,
    ),
    queryFn: ({ signal }) =>
      getRepositoryReview(repositoryID, signal, {
        offset: findingOffset,
        limit: 50,
        draftOffset,
        draftLimit: 10,
      }),
    enabled: Boolean(repositoryID),
  })
  const repository = repositoryQuery.data

  const updateRepositorySummary = (next: RepositoryReviewSummary) => {
    queryClient.setQueryData(
      repositoryReviewsKey,
      (current: { repositories: RepositoryReviewSummary[] } | undefined) => ({
        repositories: current?.repositories.some(
          (candidate) => candidate.id === next.id,
        )
          ? current.repositories.map((candidate) =>
              candidate.id === next.id ? next : candidate,
            )
          : [next, ...(current?.repositories ?? [])],
      }),
    )
  }

  const updateRepository = (
    next: RepositoryReviewSummary,
    updateDetail: (current: RepositoryReviewState) => RepositoryReviewState,
  ) => {
    queryClient.setQueryData<RepositoryReviewState>(
      repositoryReviewDetailKey(next.id, findingOffset, draftOffset),
      (current) => (current ? updateDetail({ ...current, ...next }) : current),
    )
    updateRepositorySummary(next)
    setActionError("")
  }

  const findingMutation = useMutation({
    mutationFn: ({
      repository,
      findingID,
      status,
    }: {
      repository: RepositoryReviewState
      findingID: string
      status: RepositoryReviewFindingStatus
    }) =>
      updateRepositoryReviewFinding(repository.id, findingID, {
        status,
        expected_version: repository.version,
      }),
    onSuccess: ({ repository: next, finding }) =>
      updateRepository(next, (current) => ({
        ...current,
        findings: current.findings.map((candidate) =>
          candidate.id === finding.id ? finding : candidate,
        ),
      })),
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoryQuery.refetch()
    },
  })

  const issueDraftMutation = useMutation({
    mutationFn: ({
      repository,
      findingIDs,
    }: {
      repository: RepositoryReviewState
      findingIDs: string[]
    }) =>
      createRepositoryReviewIssueDraft(repository.id, {
        finding_ids: findingIDs,
        expected_version: repository.version,
      }),
    onSuccess: ({ repository: next, draft }) => {
      updateRepositorySummary(next)
      queryClient.setQueryData<RepositoryReviewState>(
        repositoryReviewDetailKey(next.id, findingOffset, 0),
        (current) =>
          current
            ? {
                ...current,
                ...next,
                draft_offset: 0,
                draft_total: next.issue_draft_count ?? 1,
                issue_drafts: upsertIssueDraft(current.issue_drafts, draft),
              }
            : current,
      )
      setDraftOffset(0)
      setActiveDraftID(draft.id)
      setActionError("")
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoryQuery.refetch()
    },
  })

  const updateDraftMutation = useMutation({
    mutationFn: ({
      repository,
      draft,
      title,
      body,
      labels,
    }: {
      repository: RepositoryReviewState
      draft: RepositoryReviewIssueDraft
      title: string
      body: string
      labels: string[]
    }) =>
      updateRepositoryReviewIssueDraft(repository.id, draft.id, {
        title,
        body,
        labels,
        expected_version: draft.version,
      }),
    onSuccess: ({ repository: next, draft }) => {
      updateRepository(next, (current) => ({
        ...current,
        issue_drafts: upsertIssueDraft(current.issue_drafts, draft),
      }))
      setActiveDraftID(draft.id)
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoryQuery.refetch()
    },
  })

  const publishDraftMutation = useMutation({
    mutationFn: ({
      repository,
      draft,
    }: {
      repository: RepositoryReviewSummary
      draft: RepositoryReviewIssueDraft
    }) =>
      publishRepositoryReviewIssueDraft(repository.id, draft.id, {
        expected_version: draft.version,
      }),
    onSuccess: ({ repository: next, draft }) => {
      updateRepository(next, (current) => ({
        ...current,
        findings:
          draft.state === "posted"
            ? current.findings.map((finding) =>
                draft.finding_ids.includes(finding.id)
                  ? { ...finding, status: "posted" }
                  : finding,
              )
            : current.findings,
        issue_drafts: upsertIssueDraft(current.issue_drafts, draft),
      }))
      setActiveDraftID(draft.id)
      if (draft.external_url) openURL(draft.external_url)
    },
    onError: (error) => {
      setActionError(errorMessage(error))
      void repositoryQuery.refetch()
    },
  })

  const selectedFindings = useMemo(
    () =>
      repository?.findings.filter((finding) =>
        selectedFindingIDs.has(finding.id),
      ) ?? [],
    [repository, selectedFindingIDs],
  )
  const canPostToGitHub = Boolean(
    repository && githubRepositoryPath(repository.repository),
  )
  const busy =
    findingMutation.isPending ||
    issueDraftMutation.isPending ||
    updateDraftMutation.isPending ||
    publishDraftMutation.isPending

  const prepareIssue = async (postNow: boolean) => {
    if (!repository || selectedFindings.length === 0) return
    if (selectedFindings.length > maximumSelectedFindings) {
      setActionError(`Select at most ${maximumSelectedFindings} findings.`)
      return
    }
    setActionError("")
    try {
      const result = await issueDraftMutation.mutateAsync({
        repository,
        findingIDs: selectedFindings.map((finding) => finding.id),
      })
      setActiveDraftID(result.draft.id)
      if (postNow) {
        await publishDraftMutation.mutateAsync({
          repository: result.repository,
          draft: result.draft,
        })
      }
    } catch {
      // Mutation state owns the visible error and preserves the selection.
    }
  }

  const discussWithAI = async () => {
    if (!repository || selectedFindings.length === 0) return
    if (selectedFindings.length > maximumDiscussionFindings) {
      setActionError(
        `Discuss at most ${maximumDiscussionFindings} findings in one thread.`,
      )
      return
    }
    setActionError("")
    try {
      const contextIDs = uniqueStrings(
        selectedFindings.flatMap((finding) => finding.context_ids),
      )
      const prompts = selectedFindings.map((finding) =>
        discussionPrompt(repository, [finding]),
      )
      if (
        prompts.some(
          (prompt) =>
            new TextEncoder().encode(prompt).byteLength >
            maximumDiscussionPromptBytes,
        )
      ) {
        throw new Error(
          "A finding's required provenance exceeds the discussion limit.",
        )
      }
      const thread = await createThread({
        type: "reviewing",
        title: discussionTitle(repository, selectedFindings),
        context: {
          repository: repository.repository,
          repository_review: repository.id,
          finding_ids: selectedFindings.map((finding) => finding.id).join(","),
          context_ids: contextIDs.join(","),
          ...(repository.last_commit_sha
            ? { commit: repository.last_commit_sha }
            : {}),
        },
        source_query: boundedUTF8Text(
          `repository review ${repository.repository}`,
          256,
        ),
      })
      const threadSessionID = thread.ui_session_id || thread.id
      let sent = true
      for (const prompt of prompts) {
        sent =
          sent &&
          (await switchChatSessionAndSend(threadSessionID, {
            content: prompt,
          }))
      }
      if (!sent) {
        await dropThread(thread.id).catch(() => undefined)
        throw new Error("The finding discussion could not be started.")
      }
      setThreadOpenSessionID(threadSessionID)
      onOpenThread(threadSessionID)
    } catch (error) {
      setActionError(errorMessage(error))
    }
  }

  const refresh = () => {
    void repositoriesQuery.refetch()
    if (repositoryID) void repositoryQuery.refetch()
  }

  return (
    <div className="bg-background flex h-full min-h-0 flex-col">
      <PageHeader title="Review results">
        <Button
          type="button"
          size="icon"
          variant="outline"
          aria-label="Refresh review results"
          title="Refresh review results"
          disabled={repositoriesQuery.isFetching || repositoryQuery.isFetching}
          onClick={refresh}
        >
          <IconRefresh />
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-8 md:px-6">
        {repositoriesQuery.isPending ? (
          <CenteredMessage text="Loading review results…" />
        ) : repositoriesQuery.isError ? (
          <CenteredMessage
            text="Review results could not be loaded."
            action={
              <Button variant="outline" onClick={refresh}>
                Retry
              </Button>
            }
          />
        ) : repositories.length === 0 ? (
          <CenteredMessage text="No repository review results yet." />
        ) : (
          <div className="mx-auto grid w-full max-w-[96rem] gap-4 lg:grid-cols-[16rem_minmax(0,1fr)]">
            <RepositoryList
              repositories={repositories}
              selectedID={repositoryID}
              onSelect={setRepositoryID}
            />

            <main className="min-w-0 space-y-4">
              {repositoryQuery.isError && (
                <InlineError text="The latest repository state could not be loaded; showing the list snapshot." />
              )}
              {actionError && <InlineError text={actionError} />}
              {repository && (
                <>
                  <RepositorySummary repository={repository} />
                  <SelectionActions
                    repository={repository}
                    selectedCount={selectedFindings.length}
                    allSelected={
                      repository.findings.length > 0 &&
                      selectedFindings.length ===
                        Math.min(
                          repository.findings.length,
                          maximumSelectedFindings,
                        )
                    }
                    canPostToGitHub={canPostToGitHub}
                    busy={busy}
                    onToggleAll={(checked) =>
                      setSelectedFindingIDs(
                        checked
                          ? new Set(
                              repository.findings
                                .slice(0, maximumSelectedFindings)
                                .map((finding) => finding.id),
                            )
                          : new Set(),
                      )
                    }
                    onDiscuss={() => void discussWithAI()}
                    onPrepareIssue={() => void prepareIssue(false)}
                    onPostNow={() => void prepareIssue(true)}
                  />
                  <FindingsList
                    repository={repository}
                    selectedIDs={selectedFindingIDs}
                    busy={findingMutation.isPending}
                    onToggle={(findingID, selected) =>
                      setSelectedFindingIDs((current) => {
                        const next = new Set(current)
                        if (selected && next.size >= maximumSelectedFindings) {
                          setActionError(
                            `Select at most ${maximumSelectedFindings} findings.`,
                          )
                          return next
                        }
                        if (selected) next.add(findingID)
                        else next.delete(findingID)
                        return next
                      })
                    }
                    onStatus={(findingID, status) =>
                      findingMutation.mutate({
                        repository,
                        findingID,
                        status,
                      })
                    }
                  />
                  <CollectionPagination
                    offset={repository.finding_offset ?? findingOffset}
                    total={
                      repository.finding_total ?? repository.findings.length
                    }
                    pageSize={50}
                    itemLabel="findings"
                    onChange={setFindingOffset}
                  />
                  <IssueDrafts
                    repository={repository}
                    activeDraftID={activeDraftID}
                    busy={
                      updateDraftMutation.isPending ||
                      publishDraftMutation.isPending
                    }
                    canPublish={canPostToGitHub}
                    offset={repository.draft_offset ?? draftOffset}
                    total={
                      repository.draft_total ??
                      repository.issue_draft_count ??
                      repository.issue_drafts.length
                    }
                    onPageChange={setDraftOffset}
                    onSave={(draft, title, body, labels) =>
                      updateDraftMutation.mutate({
                        repository,
                        draft,
                        title,
                        body,
                        labels,
                      })
                    }
                    onPublish={(draft) =>
                      publishDraftMutation.mutate({ repository, draft })
                    }
                  />
                </>
              )}
            </main>
          </div>
        )}
      </div>
    </div>
  )
}

function CollectionPagination({
  offset,
  total,
  pageSize,
  itemLabel,
  onChange,
}: {
  offset: number
  total: number
  pageSize: number
  itemLabel: string
  onChange: (offset: number) => void
}) {
  if (total <= pageSize) return null
  return (
    <nav
      className="flex items-center justify-between gap-3"
      aria-label={`${itemLabel} pages`}
    >
      <Button
        size="sm"
        variant="outline"
        disabled={offset === 0}
        onClick={() => onChange(Math.max(0, offset - pageSize))}
      >
        Previous {itemLabel}
      </Button>
      <span className="text-muted-foreground text-xs">
        {offset + 1}–{Math.min(total, offset + pageSize)} of {total}
      </span>
      <Button
        size="sm"
        variant="outline"
        disabled={offset + pageSize >= total}
        onClick={() => onChange(offset + pageSize)}
      >
        Next {itemLabel}
      </Button>
    </nav>
  )
}

function RepositoryList({
  repositories,
  selectedID,
  onSelect,
}: {
  repositories: RepositoryReviewSummary[]
  selectedID: string
  onSelect: (repositoryID: string) => void
}) {
  return (
    <aside className="lg:sticky lg:top-0 lg:self-start">
      <Card size="sm">
        <CardHeader>
          <CardTitle>Repositories</CardTitle>
          <CardDescription>Durable bug-finding results</CardDescription>
        </CardHeader>
        <CardContent>
          <ul className="space-y-1">
            {repositories.map((repository) => {
              const open = repository.open_finding_count ?? 0
              const active = selectedID === repository.id
              return (
                <li key={repository.id}>
                  <button
                    type="button"
                    className={`hover:bg-muted w-full rounded-md px-2 py-2 text-left ${
                      active ? "bg-muted" : ""
                    }`}
                    aria-current={active ? "page" : undefined}
                    onClick={() => onSelect(repository.id)}
                  >
                    <span className="block truncate font-medium">
                      {repository.repository}
                    </span>
                    <span className="text-muted-foreground mt-0.5 block text-xs">
                      {open} open · {repository.finding_count ?? 0} total
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        </CardContent>
      </Card>
    </aside>
  )
}

function RepositorySummary({
  repository,
}: {
  repository: RepositoryReviewState
}) {
  const latestRun = repository.runs.at(-1)
  return (
    <Card size="sm" data-testid="repository-review-summary">
      <CardHeader>
        <CardTitle>{repository.repository}</CardTitle>
        <CardDescription>
          State version {repository.version} · updated{" "}
          {formatTimestamp(repository.updated_at)}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <HashLine
          label="Commit SHA"
          value={repository.last_commit_sha || "—"}
        />
        <div className="grid gap-2 sm:grid-cols-6">
          <Metric
            label="Reviewed files"
            value={
              repository.reviewed_file_count ??
              Object.keys(repository.files).length
            }
          />
          <Metric
            label="Findings"
            value={repository.finding_count ?? repository.findings.length}
          />
          <Metric label="Contexts on page" value={repository.contexts.length} />
          <Metric
            label="Issue drafts"
            value={
              repository.issue_draft_count ?? repository.issue_drafts.length
            }
          />
          <Metric
            label="Unsupported files"
            value={
              repository.unsupported_count ??
              Object.keys(repository.unsupported).length
            }
          />
          <Metric
            label="Policy-excluded files"
            value={repository.excluded_file_count ?? 0}
          />
        </div>
        {latestRun && (
          <p className="text-muted-foreground text-xs">
            Latest run: {latestRun.reviewed_files} reviewed ·{" "}
            {latestRun.skipped_files} unchanged skipped ·{" "}
            {latestRun.excluded_files ?? 0} inventory-policy excluded ·{" "}
            {latestRun.unreviewed_files} retryable/unreviewed ·{" "}
            {latestRun.unsupported_files} terminal unsupported ·{" "}
            {latestRun.remaining_files} remaining for the next run · models{" "}
            {latestRun.models.join(", ") || "unknown"}
          </p>
        )}
        {latestRun && latestRun.remaining_files > 0 && (
          <p className="border-border bg-muted/40 rounded-md border p-2 text-sm">
            This repository review is partial. Run the bug-finder workflow again
            to continue with the next {latestRun.remaining_files} files.
          </p>
        )}
        {latestRun && (latestRun.unreviewed_paths?.length ?? 0) > 0 && (
          <details className="rounded-md border p-2 text-xs">
            <summary className="cursor-pointer font-medium">
              Unavailable or failed paths ({latestRun.unreviewed_paths?.length})
            </summary>
            <ul className="text-muted-foreground mt-2 space-y-1">
              {latestRun.unreviewed_paths?.map((path) => (
                <li key={path}>
                  <code className="break-all">{path}</code>
                </li>
              ))}
            </ul>
          </details>
        )}
        {(repository.unsupported_count ??
          Object.keys(repository.unsupported).length) > 0 && (
          <details className="rounded-md border p-2 text-xs">
            <summary className="cursor-pointer font-medium">
              Terminal unsupported files (
              {repository.unsupported_count ??
                Object.keys(repository.unsupported).length}
              )
            </summary>
            <ul className="text-muted-foreground mt-2 space-y-1">
              {Object.values(repository.unsupported).map((file) => (
                <li key={`${file.path}:${file.blob_sha}`}>
                  <code className="break-all">{file.path}</code> · {file.reason}
                </li>
              ))}
            </ul>
            {(repository.unsupported_count ?? 0) >
              Object.keys(repository.unsupported).length && (
              <p className="text-muted-foreground mt-2">
                Showing the first {Object.keys(repository.unsupported).length}.
              </p>
            )}
          </details>
        )}
      </CardContent>
    </Card>
  )
}

function SelectionActions({
  repository,
  selectedCount,
  allSelected,
  canPostToGitHub,
  busy,
  onToggleAll,
  onDiscuss,
  onPrepareIssue,
  onPostNow,
}: {
  repository: RepositoryReviewState
  selectedCount: number
  allSelected: boolean
  canPostToGitHub: boolean
  busy: boolean
  onToggleAll: (checked: boolean) => void
  onDiscuss: () => void
  onPrepareIssue: () => void
  onPostNow: () => void
}) {
  return (
    <Card size="sm" data-testid="repository-review-selection">
      <CardContent className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={allSelected}
            disabled={repository.findings.length === 0 || busy}
            onChange={(event) => onToggleAll(event.target.checked)}
          />
          <span>
            {selectedCount > 0
              ? `${selectedCount} selected`
              : `Select all ${repository.findings.length}`}
          </span>
          {repository.findings.length > maximumSelectedFindings && (
            <span className="text-muted-foreground text-xs">
              First {maximumSelectedFindings} maximum
            </span>
          )}
        </label>
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            disabled={
              busy ||
              selectedCount === 0 ||
              selectedCount > maximumDiscussionFindings
            }
            onClick={onDiscuss}
          >
            <IconMessageCircle />
            Discuss with AI
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={busy || selectedCount === 0}
            onClick={onPrepareIssue}
          >
            <IconBug />
            Prepare issue
          </Button>
          {canPostToGitHub && (
            <Button
              size="sm"
              disabled={busy || selectedCount === 0}
              onClick={onPostNow}
            >
              <IconBrandGithub />
              Post now
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function FindingsList({
  repository,
  selectedIDs,
  busy,
  onToggle,
  onStatus,
}: {
  repository: RepositoryReviewState
  selectedIDs: Set<string>
  busy: boolean
  onToggle: (findingID: string, selected: boolean) => void
  onStatus: (findingID: string, status: RepositoryReviewFindingStatus) => void
}) {
  const contexts = new Map(
    repository.contexts.map((context) => [context.id, context]),
  )
  return (
    <section aria-labelledby="repository-findings-title" className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 id="repository-findings-title" className="text-base font-medium">
          Validated findings
        </h2>
        <Badge variant="secondary">{repository.findings.length}</Badge>
      </div>
      {repository.findings.length === 0 ? (
        <Card size="sm">
          <CardContent className="text-muted-foreground">
            No validated findings were stored for this repository.
          </CardContent>
        </Card>
      ) : (
        repository.findings.map((finding) => (
          <FindingCard
            key={finding.id}
            finding={finding}
            contexts={finding.context_ids.flatMap((id) => {
              const context = contexts.get(id)
              return context ? [context] : []
            })}
            selected={selectedIDs.has(finding.id)}
            busy={busy}
            onToggle={(selected) => onToggle(finding.id, selected)}
            onStatus={(status) => onStatus(finding.id, status)}
          />
        ))
      )}
    </section>
  )
}

function FindingCard({
  finding,
  contexts,
  selected,
  busy,
  onToggle,
  onStatus,
}: {
  finding: RepositoryReviewFinding
  contexts: RepositoryReviewFindingContext[]
  selected: boolean
  busy: boolean
  onToggle: (selected: boolean) => void
  onStatus: (status: RepositoryReviewFindingStatus) => void
}) {
  const highSeverity =
    finding.severity === "critical" || finding.severity === "high"
  const consensus = `${finding.models.length} model contributor${
    finding.models.length === 1 ? "" : "s"
  } · ${finding.observation_count} validated observation${
    finding.observation_count === 1 ? "" : "s"
  }`
  return (
    <Card
      size="sm"
      data-finding-id={finding.id}
      className={selected ? "border-primary" : undefined}
    >
      <CardHeader>
        <div className="flex min-w-0 items-start gap-3">
          <input
            className="mt-1"
            type="checkbox"
            checked={selected}
            aria-label={`Select ${finding.title}`}
            onChange={(event) => onToggle(event.target.checked)}
          />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge variant={highSeverity ? "destructive" : "outline"}>
                {finding.severity}
              </Badge>
              <Badge variant="outline">{finding.status}</Badge>
              <Badge
                variant={
                  finding.validation.status === "confirmed"
                    ? "secondary"
                    : "destructive"
                }
              >
                {finding.validation.status}
              </Badge>
            </div>
            <CardTitle className="mt-2">{finding.title}</CardTitle>
            {finding.symbol && (
              <code className="text-muted-foreground mt-1 block text-xs">
                {finding.symbol}
              </code>
            )}
            <CardDescription className="mt-1 break-words">
              {finding.message || finding.evidence}
            </CardDescription>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="bg-muted/30 grid gap-2 rounded-md border p-3 text-xs">
          <div className="flex min-w-0 flex-wrap items-center gap-x-2">
            <IconFileCode className="text-muted-foreground size-4 shrink-0" />
            <code className="break-all">
              {finding.file.path}
              {finding.line == null ? "" : `:${finding.line}`}
            </code>
            <span className="text-muted-foreground">
              {formatBytes(finding.file.size_bytes)} ({finding.file.size_bytes}{" "}
              bytes)
            </span>
          </div>
          <HashLine label="Commit SHA" value={finding.commit_sha} />
          <HashLine label="Blob SHA" value={finding.file.blob_sha} />
        </div>

        <dl className="grid gap-2 text-sm sm:grid-cols-2">
          <FindingText label="Evidence" value={finding.evidence} />
          <FindingText label="Impact" value={finding.impact} />
          <FindingText label="Recommendation" value={finding.recommendation} />
          <FindingText label="Validation" value={finding.validation.summary} />
        </dl>
        {finding.validation.checks && finding.validation.checks.length > 0 && (
          <div className="text-xs">
            <strong className="flex items-center gap-1.5">
              <IconChecks className="size-4" /> Validation checks
            </strong>
            <ul className="text-muted-foreground mt-1 list-inside list-disc">
              {finding.validation.checks.map((check) => (
                <li key={check}>{check}</li>
              ))}
            </ul>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <span className="text-muted-foreground mr-1">Models</span>
          {finding.models.map((model) => (
            <Badge key={model} variant="outline">
              {model}
            </Badge>
          ))}
          <span className="text-muted-foreground ml-auto">
            Consensus: {consensus}
          </span>
        </div>

        <FindingContexts finding={finding} contexts={contexts} />

        {(finding.observations?.length ?? 0) > 1 && (
          <details className="rounded-md border p-3 text-xs">
            <summary className="cursor-pointer font-medium">
              Model observation variants ({finding.observations?.length})
            </summary>
            <div className="mt-3 space-y-3">
              {finding.observations?.map((observation) => (
                <section
                  key={`${observation.context_id}:${observation.model}`}
                  className="bg-muted/30 rounded-md p-2"
                >
                  <strong>{observation.model}</strong> · {observation.severity}
                  {observation.reviewer ? ` · ${observation.reviewer}` : ""}
                  <p className="mt-1 whitespace-pre-wrap">
                    {observation.evidence}
                  </p>
                  <p className="text-muted-foreground mt-1 whitespace-pre-wrap">
                    Validation: {observation.validation.summary}
                  </p>
                </section>
              ))}
            </div>
          </details>
        )}

        <div className="flex flex-wrap justify-end gap-2">
          {finding.status === "open" ? (
            <>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() => onStatus("dismissed")}
              >
                Dismiss
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => onStatus("posted")}
              >
                Mark posted
              </Button>
            </>
          ) : (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => onStatus("open")}
            >
              Reopen
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function FindingContexts({
  finding,
  contexts,
}: {
  finding: RepositoryReviewFinding
  contexts: RepositoryReviewFindingContext[]
}) {
  return (
    <details className="rounded-md border p-3 text-xs">
      <summary className="cursor-pointer font-medium">
        Context file references ({finding.context_ids.length})
      </summary>
      <div className="mt-3 space-y-3">
        {finding.context_ids.map((contextID) => {
          const context = contexts.find(
            (candidate) => candidate.id === contextID,
          )
          return (
            <section key={contextID} className="bg-muted/30 rounded-md p-2">
              <code className="block break-all">{contextID}</code>
              {!context ? (
                <p className="text-muted-foreground mt-1">
                  Context metadata is unavailable in this snapshot.
                </p>
              ) : (
                <>
                  <p className="text-muted-foreground mt-1">
                    {context.model}
                    {context.reviewer ? ` · ${context.reviewer}` : ""} · run{" "}
                    {context.run_id}
                  </p>
                  <HashLine label="Context commit" value={context.commit_sha} />
                  {context.profile_hash && (
                    <HashLine
                      label="Review profile"
                      value={context.profile_hash}
                    />
                  )}
                  <ul className="mt-2 space-y-1">
                    {context.files.map((file) => (
                      <li
                        key={`${context.id}:${file.path}:${file.blob_sha}`}
                        className="grid gap-0.5 sm:grid-cols-[minmax(0,1fr)_auto] sm:gap-x-3"
                      >
                        <code className="break-all">{file.path}</code>
                        <span className="text-muted-foreground">
                          {formatBytes(file.size_bytes)} ({file.size_bytes}{" "}
                          bytes)
                        </span>
                        <code className="text-muted-foreground break-all sm:col-span-2">
                          blob {file.blob_sha}
                        </code>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </section>
          )
        })}
      </div>
    </details>
  )
}

function IssueDrafts({
  repository,
  activeDraftID,
  busy,
  canPublish,
  offset,
  total,
  onPageChange,
  onSave,
  onPublish,
}: {
  repository: RepositoryReviewState
  activeDraftID: string
  busy: boolean
  canPublish: boolean
  offset: number
  total: number
  onPageChange: (offset: number) => void
  onSave: (
    draft: RepositoryReviewIssueDraft,
    title: string,
    body: string,
    labels: string[],
  ) => void
  onPublish: (draft: RepositoryReviewIssueDraft) => void
}) {
  if (total === 0) return null
  return (
    <section aria-labelledby="repository-issue-drafts" className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 id="repository-issue-drafts" className="text-base font-medium">
          Issue drafts
        </h2>
        <Badge variant="outline">{total}</Badge>
      </div>
      {[...repository.issue_drafts].reverse().map((draft) => (
        <IssueDraftEditor
          key={draft.id}
          repository={repository.repository}
          draft={draft}
          highlighted={draft.id === activeDraftID}
          busy={busy}
          canPublish={canPublish}
          onSave={onSave}
          onPublish={onPublish}
        />
      ))}
      <CollectionPagination
        offset={offset}
        total={total}
        pageSize={10}
        itemLabel="issue drafts"
        onChange={onPageChange}
      />
    </section>
  )
}

function IssueDraftEditor({
  repository,
  draft,
  highlighted,
  busy,
  canPublish,
  onSave,
  onPublish,
}: {
  repository: string
  draft: RepositoryReviewIssueDraft
  highlighted: boolean
  busy: boolean
  canPublish: boolean
  onSave: (
    draft: RepositoryReviewIssueDraft,
    title: string,
    body: string,
    labels: string[],
  ) => void
  onPublish: (draft: RepositoryReviewIssueDraft) => void
}) {
  const [title, setTitle] = useState(draft.title)
  const [body, setBody] = useState(draft.body)
  const [labels, setLabels] = useState((draft.labels ?? []).join(", "))
  useEffect(() => {
    setTitle(draft.title)
    setBody(draft.body)
    setLabels((draft.labels ?? []).join(", "))
  }, [draft.body, draft.labels, draft.title, draft.version])
  const parsedLabels = parseLabels(labels)
  const editable = draft.state === "editing"
  const dirty =
    title.trim() !== draft.title ||
    body.trim() !== draft.body ||
    parsedLabels.join("\u0000") !== (draft.labels ?? []).join("\u0000")
  const githubURL = githubNewIssueURL(repository, {
    ...draft,
    title: title.trim(),
    body: body.trim(),
    labels: parsedLabels,
  })
  const externalURL = safeHTTPSURL(draft.external_url)
  return (
    <Card
      size="sm"
      data-issue-draft-id={draft.id}
      className={highlighted ? "border-primary" : undefined}
    >
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center justify-between gap-2">
          <span>{draft.title}</span>
          <Badge variant="outline">{draft.state}</Badge>
        </CardTitle>
        <CardDescription>
          {draft.finding_ids.length} findings · draft version {draft.version}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <Field label="Issue title">
          <Input
            value={title}
            disabled={!editable}
            aria-label={`Issue title for ${draft.id}`}
            onChange={(event) => setTitle(event.target.value)}
          />
        </Field>
        <Field label="Issue body">
          <Textarea
            value={body}
            disabled={!editable}
            className="min-h-40"
            aria-label={`Issue body for ${draft.id}`}
            onChange={(event) => setBody(event.target.value)}
          />
        </Field>
        <Field label="Labels · comma separated">
          <Input
            value={labels}
            disabled={!editable}
            aria-label={`Issue labels for ${draft.id}`}
            onChange={(event) => setLabels(event.target.value)}
          />
        </Field>
        <div className="flex flex-wrap justify-end gap-2">
          {externalURL && (
            <Button asChild size="sm" variant="outline">
              <a href={externalURL} target="_blank" rel="noreferrer">
                <IconBrandGithub /> Open issue
              </a>
            </Button>
          )}
          {canPublish &&
            (draft.state === "unknown" || draft.state === "publishing") && (
              <Button
                size="sm"
                disabled={busy}
                onClick={() => onPublish(draft)}
              >
                <IconBrandGithub /> Reconcile publication
              </Button>
            )}
          {editable && (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={busy || !title.trim() || !body.trim()}
                onClick={() =>
                  onSave(draft, title.trim(), body.trim(), parsedLabels)
                }
              >
                Save draft
              </Button>
              {githubURL && (
                <Button
                  size="sm"
                  disabled={!title.trim() || !body.trim()}
                  onClick={() => openURL(githubURL)}
                >
                  <IconBrandGithub /> Open in GitHub
                </Button>
              )}
              {canPublish && (
                <Button
                  size="sm"
                  disabled={busy || dirty || !title.trim() || !body.trim()}
                  onClick={() => onPublish(draft)}
                >
                  <IconBrandGithub /> Publish issue
                </Button>
              )}
            </>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

function FindingText({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="font-medium">{label}</dt>
      <dd className="text-muted-foreground mt-0.5 whitespace-pre-wrap">
        {value}
      </dd>
    </div>
  )
}

function HashLine({ label, value }: { label: string; value: string }) {
  return (
    <div className="mt-1 grid min-w-0 gap-1 text-xs sm:grid-cols-[auto_minmax(0,1fr)]">
      <span className="text-muted-foreground">{label}</span>
      <code className="break-all">{value}</code>
    </div>
  )
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="bg-muted/30 rounded-md border p-2">
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="mt-0.5 text-lg font-medium">{value}</div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  )
}

function InlineError({ text }: { text: string }) {
  return (
    <div
      role="alert"
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-md border p-3 text-sm"
    >
      {text}
    </div>
  )
}

function CenteredMessage({
  text,
  action,
}: {
  text: string
  action?: ReactNode
}) {
  return (
    <div className="flex min-h-64 flex-col items-center justify-center gap-3 text-center">
      <IconBug className="text-muted-foreground size-8" />
      <p className="text-muted-foreground text-sm">{text}</p>
      {action}
    </div>
  )
}

function discussionTitle(
  repository: RepositoryReviewState,
  findings: RepositoryReviewFinding[],
): string {
  const title =
    findings.length === 1
      ? findings[0].title
      : `${repository.repository}: ${findings.length} review findings`
  return boundedUTF8Text(title, 256)
}

function boundedUTF8Text(value: string, maximumBytes: number): string {
  const encoded = new TextEncoder().encode(value.trim())
  if (encoded.byteLength <= maximumBytes) return value.trim()
  return `${new TextDecoder().decode(encoded.slice(0, maximumBytes - 3))}…`
}

function openURL(url: string) {
  window.open(url, "_blank", "noopener,noreferrer")
}

function parseLabels(value: string): string[] {
  return uniqueStrings(
    value
      .split(",")
      .map((label) => label.trim())
      .filter(Boolean),
  )
}

function uniqueStrings(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))]
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return "—"
  if (value < 1024) return `${value} B`
  const units = ["KiB", "MiB", "GiB", "TiB"]
  let amount = value / 1024
  let index = 0
  while (amount >= 1024 && index < units.length - 1) {
    amount /= 1024
    index++
  }
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${units[index]}`
}

function formatTimestamp(value: string): string {
  const parsed = new Date(value)
  return Number.isNaN(parsed.valueOf()) ? value : parsed.toLocaleString()
}

function safeHTTPSURL(value: string | undefined): string | undefined {
  if (!value) return undefined
  try {
    const parsed = new URL(value)
    return parsed.protocol === "https:" && !parsed.username && !parsed.password
      ? parsed.toString()
      : undefined
  } catch {
    return undefined
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "The repository review action failed."
}

function upsertIssueDraft(
  drafts: RepositoryReviewIssueDraft[],
  draft: RepositoryReviewIssueDraft,
): RepositoryReviewIssueDraft[] {
  const next = drafts.filter((candidate) => candidate.id !== draft.id)
  next.push(draft)
  return next.slice(-10)
}
