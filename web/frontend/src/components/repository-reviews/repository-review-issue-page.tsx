import {
  IconBrandGithub,
  IconEdit,
  IconExternalLink,
  IconRefresh,
  IconSparkles,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import ReactMarkdown from "react-markdown"
import rehypeSanitize from "rehype-sanitize"
import remarkGfm from "remark-gfm"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  deleteRepositoryReviewAutomationIssue,
  getRepositoryReviewAutomationIssue,
  publishRepositoryReviewAutomationIssue,
  regenerateRepositoryReviewAutomationIssue,
} from "@/api/repository-reviews"
import { CollectionDetailShell } from "@/components/collection"
import { githubRepositoryPath } from "@/components/repository-reviews/repository-review-actions"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export function RepositoryReviewIssuePage({
  automationID,
  draftID,
  onBack,
  onDeleted,
  onEdit,
  onOpenFinding,
  onManageLink,
}: {
  automationID: string
  draftID: string
  onBack: () => void
  onDeleted: () => void
  onEdit: () => void
  onOpenFinding: (findingID: string) => void
  onManageLink: (findingID: string) => void
}) {
  const [deleteOpen, setDeleteOpen] = useState(false)
  const query = useQuery({
    queryKey: ["repository-review-issue", automationID, draftID],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationIssue(automationID, draftID, signal),
    retry: false,
    refetchInterval: (current) =>
      current.state.data &&
      new Set(["generating", "publishing", "unknown"]).has(
        current.state.data.issue.state,
      )
        ? 2_000
        : false,
  })
  const detail = query.data
  const issue = detail?.issue
  const repositoryFindingID = detail?.finding?.repository_finding_id
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const regenerate = useMutation({
    mutationFn: () =>
      regenerateRepositoryReviewAutomationIssue(automationID, draftID, {
        expected_version: issue?.version ?? 0,
      }),
    onSuccess: async (result) => {
      await query.refetch()
      if (result.issue.state === "generating") {
        toast.info("Issue preview generation is already in progress.")
      } else if (result.issue.generation_error) {
        toast.error("Regeneration failed; the last good preview was preserved.")
      } else {
        toast.success("Preview regeneration finished.")
      }
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "Regeneration failed; the last good preview was preserved.",
      )
      void query.refetch()
    },
  })
  const remove = useMutation({
    mutationFn: () =>
      deleteRepositoryReviewAutomationIssue(automationID, draftID, {
        expected_version: issue?.version ?? 0,
        confirmed: true,
      }),
    onSuccess: () => {
      setDeleteOpen(false)
      toast.success(
        "Unpublished preview deleted; the finding is available again.",
      )
      onDeleted()
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Preview deletion failed.",
      ),
  })
  const publish = useMutation({
    mutationFn: () =>
      publishRepositoryReviewAutomationIssue(automationID, draftID, {
        expected_version: issue?.version ?? 0,
        confirmed: true,
      }),
    onSuccess: async () => {
      await query.refetch()
      toast.success("Posting state reconciled.")
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Posting failed.")
      void query.refetch()
    },
  })
  const github =
    detail?.capabilities?.github ??
    Boolean(detail && githubRepositoryPath(detail.automation.repository))
  const canonical = issue?.canonical !== false && !issue?.read_only
  const editable =
    canonical && (detail?.capabilities?.can_edit ?? issue?.state === "editing")
  const deletable =
    canonical &&
    (detail?.capabilities?.can_delete ??
      issue?.deletable ??
      (issue?.state === "editing" || issue?.state === "failed"))
  const regeneratable =
    canonical &&
    (detail?.capabilities?.can_regenerate ??
      issue?.regeneratable ??
      (issue?.origin === "ai_generated" &&
        new Set(["generating", "editing", "failed"]).has(issue?.state ?? "")))
  const publishable =
    github &&
    canonical &&
    (detail?.capabilities?.can_publish ??
      issue?.publishable ??
      new Set(["editing", "publishing", "unknown"]).has(issue?.state ?? ""))
  const externalURL = safeHTTPSURL(issue?.external_url)

  return (
    <>
      <CollectionDetailShell
        title={issue?.title || "Issue preview"}
        identity={
          <span
            className="block max-w-40 truncate font-mono text-xs sm:max-w-72"
            title={draftID}
          >
            {draftID}
          </span>
        }
        status={
          issue ? (
            <div className="flex items-center gap-2">
              <Badge variant="outline">{issue.state}</Badge>
              <Badge variant="secondary">{issue.origin || "legacy"}</Badge>
              {!canonical && <Badge variant="destructive">read only</Badge>}
            </div>
          ) : undefined
        }
        actions={
          issue ? (
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                size="icon-sm"
                variant="outline"
                aria-label="Refresh issue preview"
                onClick={() => void query.refetch()}
              >
                <IconRefresh />
              </Button>
              {editable && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={onEdit}
                >
                  <IconEdit /> Edit
                </Button>
              )}
            </div>
          ) : undefined
        }
        loading={query.isLoading}
        error={!notFound ? query.error?.message : undefined}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Issue previews"
        contentClassName="max-w-6xl"
      >
        {detail && issue && (
          <div className="space-y-6">
            {issue.generation_error && (
              <div
                role="alert"
                className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
              >
                {issue.generation_error}
                {issue.body && (
                  <span className="mt-1 block text-xs">
                    The last good preview remains available below.
                  </span>
                )}
              </div>
            )}
            {!canonical && (
              <div
                role="status"
                className="border-border rounded-lg border p-3 text-sm"
              >
                This preserved legacy record is not the finding’s canonical
                association and cannot be edited or posted.
                {issue.conflict_reason ? ` ${issue.conflict_reason}` : ""}
              </div>
            )}

            <section className="border-border space-y-3 rounded-lg border p-4">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h2 className="font-semibold">Finding association</h2>
                <span className="text-muted-foreground text-xs">
                  {issue.finding_ids.length === 1
                    ? "One finding"
                    : `${issue.finding_ids.length} grouped legacy findings`}
                </span>
              </div>
              <div className="flex flex-wrap gap-2">
                {issue.finding_ids.map((findingID) => (
                  <Button
                    key={findingID}
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() => onOpenFinding(findingID)}
                    aria-label={`Open finding ${findingID}`}
                    title={findingID}
                  >
                    Finding {shortIdentity(findingID)}
                  </Button>
                ))}
                {(issue.origin === "linked" || issue.origin === "discovered") &&
                  issue.finding_ids[0] && (
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      onClick={() =>
                        onManageLink(
                          repositoryFindingID || issue.finding_ids[0]!,
                        )
                      }
                    >
                      Manage{" "}
                      {issue.origin === "discovered" ? "discovered" : "manual"}{" "}
                      link
                    </Button>
                  )}
              </div>
            </section>

            <section aria-labelledby="preview-provenance" className="space-y-3">
              <h2 id="preview-provenance" className="font-semibold">
                Generation provenance
              </h2>
              <dl className="border-border divide-border rounded-lg border text-sm">
                <DetailRow
                  label="Generation ID"
                  value={issue.generation_id || "Not generated"}
                  mono
                />
                <DetailRow
                  label="Instruction mode"
                  value={issue.instructions_mode || "legacy"}
                />
                <DetailRow
                  label="Generator model"
                  value={issue.generator_model || "Not recorded"}
                  mono
                />
                <DetailRow
                  label="Generator account"
                  value={issue.generator_account || "Not recorded"}
                  mono
                />
                <DetailRow
                  label="Generator profile"
                  value={profileSnapshotLabel(
                    issue.generator_profile_id,
                    issue.generator_profile_version,
                  )}
                  mono
                />
              </dl>
              {issue.resolved_instructions && (
                <details className="border-border rounded-lg border p-3 text-sm">
                  <summary className="cursor-pointer font-medium">
                    Resolved issue-writing instructions
                  </summary>
                  <p className="text-muted-foreground mt-2 whitespace-pre-wrap">
                    {issue.resolved_instructions}
                  </p>
                </details>
              )}
              {issue.generation_error && issue.attempt_generation_id && (
                <details className="border-border rounded-lg border p-3 text-sm">
                  <summary className="cursor-pointer font-medium">
                    Last failed regeneration attempt
                  </summary>
                  <dl className="mt-3 grid gap-2">
                    <DetailRow
                      label="Attempt ID"
                      value={issue.attempt_generation_id}
                      mono
                    />
                    <DetailRow
                      label="Instruction mode"
                      value={issue.attempt_instructions_mode || "Not recorded"}
                    />
                    <DetailRow
                      label="Generator model"
                      value={issue.attempt_generator_model || "Not recorded"}
                      mono
                    />
                    <DetailRow
                      label="Generator account"
                      value={issue.attempt_generator_account || "Not recorded"}
                      mono
                    />
                    <DetailRow
                      label="Generator profile"
                      value={profileSnapshotLabel(
                        issue.attempt_generator_profile_id,
                        issue.attempt_generator_profile_version,
                      )}
                      mono
                    />
                    <DetailRow
                      label="Resolved instructions"
                      value={
                        issue.attempt_resolved_instructions || "Not recorded"
                      }
                    />
                  </dl>
                </details>
              )}
            </section>

            <section aria-labelledby="rendered-preview" className="space-y-4">
              <div>
                <h2 id="rendered-preview" className="text-xl font-semibold">
                  {issue.title || "Untitled preview"}
                </h2>
                <div className="mt-2 flex flex-wrap gap-2">
                  {issue.labels?.map((label) => (
                    <Badge key={label} variant="secondary">
                      {label}
                    </Badge>
                  ))}
                </div>
              </div>
              {issue.body ? (
                <div className="prose dark:prose-invert prose-pre:overflow-x-auto border-border max-w-none rounded-lg border p-4 [overflow-wrap:anywhere]">
                  <ReactMarkdown
                    remarkPlugins={[remarkGfm]}
                    rehypePlugins={[rehypeSanitize]}
                  >
                    {issue.body}
                  </ReactMarkdown>
                </div>
              ) : (
                <div className="border-border rounded-lg border border-dashed p-8 text-center text-sm">
                  {issue.state === "generating"
                    ? "The AI-written preview is still generating."
                    : "No generated preview is available."}
                </div>
              )}
            </section>

            <div className="border-border flex flex-wrap items-center justify-end gap-2 border-t pt-4">
              {externalURL && (
                <Button asChild type="button" size="sm" variant="outline">
                  <a
                    href={externalURL}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <IconExternalLink /> Open GitHub issue
                  </a>
                </Button>
              )}
              {regeneratable && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={regenerate.isPending}
                  onClick={() => regenerate.mutate()}
                >
                  <IconSparkles />{" "}
                  {regenerate.isPending
                    ? "Regenerating…"
                    : issue.state === "generating"
                      ? "Retry generation"
                      : "Regenerate with AI"}
                </Button>
              )}
              {deletable && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setDeleteOpen(true)}
                >
                  <IconTrash /> Delete preview
                </Button>
              )}
              {publishable && (
                <Button
                  type="button"
                  size="sm"
                  disabled={publish.isPending}
                  onClick={() => publish.mutate()}
                >
                  <IconBrandGithub />
                  {publish.isPending
                    ? "Working…"
                    : issue.state === "unknown" || issue.state === "publishing"
                      ? "Reconcile publication"
                      : "Post issue"}
                </Button>
              )}
            </div>
          </div>
        )}
      </CollectionDetailShell>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Delete this unpublished preview?
            </AlertDialogTitle>
            <AlertDialogDescription>
              The preview is removed and its finding becomes available for
              generation or linking again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={remove.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending}
              onClick={(event) => {
                event.preventDefault()
                remove.mutate()
              }}
            >
              {remove.isPending ? "Deleting…" : "Delete preview"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function DetailRow({
  label,
  value,
  mono = false,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="grid gap-1 border-b px-3 py-3 last:border-b-0 sm:grid-cols-[11rem_minmax(0,1fr)]">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={mono ? "font-mono text-xs break-all" : "break-words"}>
        {value}
      </dd>
    </div>
  )
}

function profileSnapshotLabel(
  profileID: string | undefined,
  profileVersion: number | undefined,
): string {
  if (!profileID) return "Not recorded"
  return profileVersion ? `${profileID} · v${profileVersion}` : profileID
}

function shortIdentity(value: string): string {
  return value.length > 18 ? `${value.slice(0, 10)}…${value.slice(-6)}` : value
}

function safeHTTPSURL(value: string | undefined): string | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value)
    return url.protocol === "https:" && !url.username && !url.password
      ? url.toString()
      : undefined
  } catch {
    return undefined
  }
}
