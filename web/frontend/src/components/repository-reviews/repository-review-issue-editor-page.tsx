import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { toast } from "sonner"

import {
  RepositoryReviewAPIError,
  type RepositoryReviewIssueDraft,
  getRepositoryReviewAutomationIssue,
  updateRepositoryReviewAutomationIssue,
} from "@/api/repository-reviews"
import { CollectionDetailShell } from "@/components/collection"
import { repositoryReviewIssueStateLabel } from "@/components/repository-reviews/repository-review-issue-state"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"

interface IssueEditorValue {
  identity: string
  title: string
  body: string
  labels: string
}

export function RepositoryReviewIssueEditorPage({
  automationID,
  draftID,
  onBack,
  onSaved,
}: {
  automationID: string
  draftID: string
  onBack: () => void
  onSaved: (issue: RepositoryReviewIssueDraft) => void
}) {
  const queryClient = useQueryClient()
  const editorIdentity = `${automationID}\u0000${draftID}`
  const [editorState, setEditor] = useState<IssueEditorValue | null>(null)
  const editor = editorState?.identity === editorIdentity ? editorState : null
  const queryKey = ["repository-review-issue", automationID, draftID] as const
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) =>
      getRepositoryReviewAutomationIssue(automationID, draftID, signal),
    retry: false,
  })
  const detail = query.data
  const issue = detail?.issue
  const canonical = issue?.canonical !== false && !issue?.read_only
  const editable =
    canonical && (detail?.capabilities?.can_edit ?? issue?.state === "editing")
  const notFound =
    query.error instanceof RepositoryReviewAPIError &&
    query.error.status === 404
  useEffect(() => {
    if (!issue || editor) return
    setEditor({
      identity: editorIdentity,
      title: issue.title,
      body: issue.body,
      labels: issue.labels?.join(", ") ?? "",
    })
  }, [editor, editorIdentity, issue])
  const save = useMutation({
    mutationFn: (value: IssueEditorValue) =>
      updateRepositoryReviewAutomationIssue(automationID, draftID, {
        title: value.title.trim(),
        body: value.body.trim(),
        labels: parseLabels(value.labels),
        expected_version: issue?.version ?? 0,
      }),
    onSuccess: (saved) => {
      queryClient.setQueryData(queryKey, saved)
      toast.success("Issue preview saved.")
      onSaved(saved.issue)
    },
    onError: (error) =>
      toast.error(
        error instanceof Error ? error.message : "Preview save failed.",
      ),
  })
  const dirty = Boolean(
    issue &&
    editor &&
    (editor.title.trim() !== issue.title ||
      editor.body.trim() !== issue.body ||
      parseLabels(editor.labels).join("\u0000") !==
        (issue.labels ?? []).join("\u0000")),
  )

  return (
    <CollectionDetailShell
      title="Edit issue preview"
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
          <Badge variant="outline">
            {repositoryReviewIssueStateLabel(issue.state)}
          </Badge>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Issue details"
      contentClassName="max-w-4xl"
    >
      {detail && issue && !editable && (
        <div
          role="status"
          className="border-border rounded-lg border p-4 text-sm"
        >
          This issue record is read only and cannot be edited.
        </div>
      )}
      {detail && issue && editable && editor && (
        <div className="space-y-5">
          <label
            htmlFor="repository-review-issue-title"
            className="grid gap-2 text-sm"
          >
            <span className="font-medium">Title</span>
            <Input
              id="repository-review-issue-title"
              value={editor.title}
              onChange={(event) =>
                setEditor({ ...editor, title: event.target.value })
              }
            />
          </label>
          <label
            htmlFor="repository-review-issue-body"
            className="grid gap-2 text-sm"
          >
            <span className="font-medium">GitHub-flavored Markdown body</span>
            <Textarea
              id="repository-review-issue-body"
              className="min-h-80 font-mono text-xs"
              value={editor.body}
              onChange={(event) =>
                setEditor({ ...editor, body: event.target.value })
              }
            />
          </label>
          <label
            htmlFor="repository-review-issue-labels"
            className="grid gap-2 text-sm"
          >
            <span className="font-medium">Labels · comma separated</span>
            <Input
              id="repository-review-issue-labels"
              value={editor.labels}
              onChange={(event) =>
                setEditor({ ...editor, labels: event.target.value })
              }
            />
          </label>
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={save.isPending}
              onClick={onBack}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={
                save.isPending ||
                !dirty ||
                !editor.title.trim() ||
                !editor.body.trim()
              }
              onClick={() => save.mutate(editor)}
            >
              {save.isPending ? "Saving…" : "Save preview"}
            </Button>
          </div>
        </div>
      )}
    </CollectionDetailShell>
  )
}

function parseLabels(value: string): string[] {
  return [
    ...new Set(
      value
        .split(",")
        .map((label) => label.trim())
        .filter(Boolean),
    ),
  ]
}
