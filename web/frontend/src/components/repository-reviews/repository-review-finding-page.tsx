import {
  IconBrandGithub,
  IconChecks,
  IconFileCode,
  IconMessageCircle,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useSetAtom } from "jotai"
import { useMemo } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewFindingContext,
  generateRepositoryReviewIssues,
  getRepositoryReviewAutomationFinding,
  updateRepositoryReviewAutomationFinding,
} from "@/api/repository-reviews"
import { createThread, dropThread } from "@/api/threads"
import { CollectionDetailShell } from "@/components/collection"
import {
  discussionPrompt,
  githubRepositoryPath,
} from "@/components/repository-reviews/repository-review-actions"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { switchChatSessionAndSend } from "@/features/chat/controller"
import { threadOpenSessionIdAtom } from "@/store/threads"

import { createRepositoryReviewGenerationID } from "./repository-review-generation"

export function RepositoryReviewFindingPage({
  automationID,
  findingID,
  onBack,
  onOpenIssue,
  onLinkIssue,
  onGenerated,
  onOpenThread,
}: {
  automationID: string
  findingID: string
  onBack: () => void
  onOpenIssue: (draftID: string) => void
  onLinkIssue: () => void
  onGenerated: (generationID: string) => void
  onOpenThread: (threadID: string) => void
}) {
  const setThreadOpenSessionID = useSetAtom(threadOpenSessionIdAtom)
  const query = useQuery({
    queryKey: ["repository-review-finding", automationID, findingID],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFinding(automationID, findingID, signal),
    retry: false,
  })
  const detail = query.data
  const finding = detail?.finding
  const issueID = finding?.issue_draft_id || detail?.issue?.id
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const statusMutation = useMutation({
    mutationFn: (status: "open" | "dismissed") =>
      updateRepositoryReviewAutomationFinding(automationID, findingID, {
        status,
        expected_version: finding?.version ?? 0,
      }),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Finding status updated.")
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Status update failed.",
      ),
  })
  const generateMutation = useMutation({
    mutationFn: async () => {
      const generationID = createRepositoryReviewGenerationID()
      await generateRepositoryReviewIssues(automationID, {
        generation_id: generationID,
        finding_ids: [findingID],
        instructions_mode: "default",
      })
      return generationID
    },
    onSuccess: onGenerated,
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Preview generation failed.",
      ),
  })
  const capabilities = detail?.capabilities
  const github =
    capabilities?.github ??
    Boolean(detail && githubRepositoryPath(detail.automation.repository))
  const canGenerate =
    capabilities?.can_generate ??
    Boolean(finding?.status === "open" && !issueID)
  const canLink =
    capabilities?.can_link_issue ??
    Boolean(github && finding?.status === "open" && !issueID)
  const contexts = useMemo(
    () =>
      new Map((detail?.contexts ?? []).map((context) => [context.id, context])),
    [detail?.contexts],
  )

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
      title={finding?.title || "Repository finding"}
      identity={<span className="font-mono text-xs">{findingID}</span>}
      status={
        finding ? (
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
        finding ? (
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
      backLabel="Review report"
    >
      {detail && finding && (
        <div className="space-y-6">
          <section className="border-border space-y-3 rounded-lg border p-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className="font-semibold">Canonical issue association</h2>
                <p className="text-muted-foreground mt-1 text-sm">
                  {issueID
                    ? "This finding has one durable preview or linked GitHub issue."
                    : "No issue preview or existing issue is associated yet."}
                </p>
              </div>
              <div className="flex flex-wrap gap-2">
                {issueID ? (
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
                      <Button
                        type="button"
                        size="sm"
                        disabled={generateMutation.isPending}
                        onClick={() => generateMutation.mutate()}
                      >
                        <IconSparkles />
                        {generateMutation.isPending
                          ? "Generating…"
                          : "Generate preview"}
                      </Button>
                    )}
                    {canLink && (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={onLinkIssue}
                      >
                        <IconBrandGithub /> Link existing issue
                      </Button>
                    )}
                  </>
                )}
              </div>
            </div>
          </section>

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

          {finding.status !== "posted" && (
            <div className="border-border flex justify-end border-t pt-4">
              {finding.status === "open" ? (
                <Button
                  type="button"
                  variant="outline"
                  disabled={statusMutation.isPending || Boolean(issueID)}
                  onClick={() => statusMutation.mutate("dismissed")}
                >
                  Dismiss finding
                </Button>
              ) : (
                <Button
                  type="button"
                  variant="outline"
                  disabled={statusMutation.isPending}
                  onClick={() => statusMutation.mutate("open")}
                >
                  Reopen finding
                </Button>
              )}
            </div>
          )}
        </div>
      )}
    </CollectionDetailShell>
  )
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
