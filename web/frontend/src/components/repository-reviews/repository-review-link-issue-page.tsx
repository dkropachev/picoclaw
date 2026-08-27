import { IconBrandGithub, IconSearch } from "@tabler/icons-react"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewIssueCandidate,
  findRepositoryReviewIssueCandidates,
  getRepositoryReviewAutomationFinding,
  linkRepositoryReviewIssue,
  unlinkRepositoryReviewIssue,
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
import { Input } from "@/components/ui/input"

interface PendingLink {
  url: string
  title: string
  replace: boolean
}

export function RepositoryReviewLinkIssuePage({
  automationID,
  findingID,
  onBack,
  onLinked,
}: {
  automationID: string
  findingID: string
  onBack: () => void
  onLinked: (draftID: string) => void
}) {
  const [issueURL, setIssueURL] = useState("")
  const [pendingLink, setPendingLink] = useState<PendingLink | null>(null)
  const [unlinkOpen, setUnlinkOpen] = useState(false)
  const query = useQuery({
    queryKey: ["repository-review-finding", automationID, findingID],
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationFinding(automationID, findingID, signal),
    retry: false,
  })
  const detail = query.data
  const finding = detail?.finding
  const issue = detail?.issue
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  const candidates = useMutation({
    mutationFn: () =>
      findRepositoryReviewIssueCandidates(automationID, findingID, {
        expected_version: finding?.version ?? 0,
      }),
    onSuccess: async (response) => {
      if (response.discovered_issue?.id) {
        toast.success(
          "A high-confidence existing issue was discovered and linked.",
        )
        onLinked(response.discovered_issue.id)
        return
      }
      await query.refetch()
    },
    onError: (error) =>
      toast.error(
        error instanceof Error
          ? error.message
          : "Existing issues could not be searched.",
      ),
  })
  const link = useMutation({
    mutationFn: (pending: PendingLink) =>
      linkRepositoryReviewIssue(automationID, findingID, {
        issue_url: pending.url,
        expected_version: finding?.version ?? 0,
        confirmed: true,
        ...(pending.replace ? { replace: true } : {}),
      }),
    onSuccess: (next) => {
      setPendingLink(null)
      toast.success("Existing GitHub issue linked.")
      const draftID = next.finding.issue_draft_id || next.issue?.id
      if (draftID) onLinked(draftID)
      else void query.refetch()
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Issue link failed.",
      ),
  })
  const unlink = useMutation({
    mutationFn: () =>
      unlinkRepositoryReviewIssue(automationID, findingID, {
        expected_version: finding?.version ?? 0,
        confirmed: true,
      }),
    onSuccess: async () => {
      setUnlinkOpen(false)
      toast.success("Issue association removed.")
      await query.refetch()
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Issue unlink failed.",
      ),
  })
  const github =
    detail?.capabilities?.github ??
    Boolean(detail && githubRepositoryPath(detail.automation.repository))
  const unassociatedOpen = finding?.status === "open" && !issue
  const canLink =
    detail?.capabilities?.can_link_issue ?? Boolean(github && unassociatedOpen)
  const canSearch =
    detail?.capabilities?.can_search_issues ??
    Boolean(github && unassociatedOpen)
  const canReplace =
    detail?.capabilities?.can_replace_issue ??
    (issue?.origin === "linked" || issue?.origin === "discovered")
  const canUnlink =
    detail?.capabilities?.can_unlink_issue ??
    issue?.unlinkable ??
    (issue?.origin === "linked" || issue?.origin === "discovered")

  return (
    <>
      <CollectionDetailShell
        title="Link existing GitHub issue"
        identity={
          finding ? (
            <span className="font-mono text-xs">{finding.id}</span>
          ) : undefined
        }
        loading={query.isLoading}
        error={!notFound ? query.error?.message : undefined}
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Finding details"
      >
        {detail && finding && (
          <div className="space-y-6">
            <section className="border-border rounded-lg border p-4">
              <h2 className="font-semibold">{finding.title}</h2>
              <p className="text-muted-foreground mt-1 text-sm">
                {finding.file.path}
                {finding.line == null ? "" : `:${finding.line}`}
              </p>
            </section>

            {!github || (!canLink && !issue) ? (
              <div
                role="status"
                className="border-border rounded-lg border border-dashed p-8 text-center"
              >
                <h2 className="font-semibold">
                  Existing-issue linking is unavailable
                </h2>
                <p className="text-muted-foreground mt-2 text-sm">
                  This review is not bound to a canonical GitHub repository.
                </p>
              </div>
            ) : (
              <>
                {issue && (
                  <section className="border-border space-y-3 rounded-lg border p-4">
                    <div className="flex flex-wrap items-start justify-between gap-3">
                      <div>
                        <h2 className="font-semibold">Current association</h2>
                        <p className="text-muted-foreground mt-1 text-sm">
                          {issue.title || issue.external_url || issue.id}
                        </p>
                      </div>
                      <Badge variant="outline">
                        {issue.origin || "legacy"}
                      </Badge>
                    </div>
                    {canUnlink && (
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => setUnlinkOpen(true)}
                      >
                        Unlink manual issue
                      </Button>
                    )}
                    {!canReplace && !canUnlink && (
                      <p className="text-muted-foreground text-xs">
                        Created and legacy canonical issues cannot be replaced
                        from this route.
                      </p>
                    )}
                  </section>
                )}

                {(!issue || canReplace) && (
                  <section
                    aria-labelledby="manual-issue-link"
                    className="space-y-3"
                  >
                    <div>
                      <h2 id="manual-issue-link" className="font-semibold">
                        {issue ? "Replace manual link" : "Enter an issue URL"}
                      </h2>
                      <p className="text-muted-foreground mt-1 text-sm">
                        The server re-fetches the chosen issue and validates its
                        repository before linking.
                      </p>
                    </div>
                    <div className="flex flex-col gap-2 sm:flex-row">
                      <Input
                        aria-label="GitHub issue URL"
                        value={issueURL}
                        placeholder="https://github.com/owner/repository/issues/123"
                        onChange={(event) => setIssueURL(event.target.value)}
                      />
                      <Button
                        type="button"
                        disabled={!isHTTPSURL(issueURL)}
                        onClick={() =>
                          setPendingLink({
                            url: issueURL.trim(),
                            title: issueURL.trim(),
                            replace: Boolean(issue),
                          })
                        }
                      >
                        Review link
                      </Button>
                    </div>
                  </section>
                )}

                {canSearch && (!issue || canReplace) && (
                  <section
                    aria-labelledby="issue-candidates"
                    className="space-y-3"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <div>
                        <h2 id="issue-candidates" className="font-semibold">
                          AI-ranked candidates
                        </h2>
                        <p className="text-muted-foreground mt-1 text-sm">
                          Search uses causal hints, stable symbols, anchors,
                          path history, and title. Exact high-confidence matches
                          may create a reversible discovered link after
                          re-fetch.
                        </p>
                      </div>
                      <Button
                        type="button"
                        variant="outline"
                        disabled={candidates.isPending}
                        onClick={() => candidates.mutate()}
                      >
                        <IconSearch />
                        {candidates.isPending
                          ? "Searching…"
                          : "Ask AI to find existing issues"}
                      </Button>
                    </div>
                    {candidates.data &&
                      candidates.data.candidates.length === 0 && (
                        <p className="border-border rounded-lg border border-dashed p-6 text-center text-sm">
                          No same-repository candidates were found.
                        </p>
                      )}
                    <div className="space-y-3">
                      {candidates.data?.candidates.map((candidate) => (
                        <CandidateCard
                          key={candidate.id || candidate.url}
                          candidate={candidate}
                          onChoose={() =>
                            setPendingLink({
                              url: candidate.url,
                              title: `#${candidate.number} ${candidate.title}`,
                              replace: Boolean(issue),
                            })
                          }
                        />
                      ))}
                    </div>
                  </section>
                )}
              </>
            )}
          </div>
        )}
      </CollectionDetailShell>

      <AlertDialog
        open={pendingLink !== null}
        onOpenChange={(open) => !open && setPendingLink(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingLink?.replace
                ? "Replace the manual issue link?"
                : "Link this existing issue?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingLink?.title}. The server will re-fetch and validate this
              exact issue. Nothing is linked until you confirm.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={link.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={link.isPending || !pendingLink}
              onClick={(event) => {
                event.preventDefault()
                if (pendingLink) link.mutate(pendingLink)
              }}
            >
              {link.isPending
                ? "Linking…"
                : pendingLink?.replace
                  ? "Replace link"
                  : "Link issue"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={unlinkOpen} onOpenChange={setUnlinkOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Unlink this manually linked issue?
            </AlertDialogTitle>
            <AlertDialogDescription>
              The GitHub issue is not changed. This finding becomes available
              for a new preview or link.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={unlink.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={unlink.isPending}
              onClick={(event) => {
                event.preventDefault()
                unlink.mutate()
              }}
            >
              {unlink.isPending ? "Unlinking…" : "Unlink issue"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function CandidateCard({
  candidate,
  onChoose,
}: {
  candidate: RepositoryReviewIssueCandidate
  onChoose: () => void
}) {
  return (
    <article className="border-border space-y-2 rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="font-medium">
            #{candidate.number} {candidate.title}
          </h3>
          <div className="mt-1 flex flex-wrap gap-2">
            {candidate.state && (
              <Badge variant="outline">{candidate.state}</Badge>
            )}
            {candidate.labels?.map((label) => (
              <Badge key={label} variant="secondary">
                {label}
              </Badge>
            ))}
          </div>
        </div>
        <Button type="button" size="sm" onClick={onChoose}>
          <IconBrandGithub /> Select
        </Button>
      </div>
      {candidate.explanation && (
        <p className="text-muted-foreground text-sm">{candidate.explanation}</p>
      )}
      <a
        className="text-primary block text-xs break-all underline underline-offset-2"
        href={candidate.url}
        target="_blank"
        rel="noopener noreferrer"
      >
        {candidate.url}
      </a>
    </article>
  )
}

function isHTTPSURL(value: string): boolean {
  try {
    const url = new URL(value.trim())
    return url.protocol === "https:" && !url.username && !url.password
  } catch {
    return false
  }
}
