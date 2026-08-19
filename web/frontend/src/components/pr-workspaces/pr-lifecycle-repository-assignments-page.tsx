import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDeviceFloppy,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react"

import {
  type PRLifecycleRepositoryAssignmentIssue,
  type PRLifecycleRepositoryAssignmentSnapshot,
  canonicalPRLifecycleRepositoryIdentity,
  getPRLifecycleRepositoryAssignments,
  putPRLifecycleRepositoryAssignments,
  validatePRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"
import { createPRWorkspaceRequestID } from "@/api/pr-workspaces"
import { PageHeader } from "@/components/page-header"
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
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

const assignmentQueryKey = ["pr-lifecycle", "repository-assignments"] as const
const assignmentDraftQueryKey = [
  "pr-lifecycle",
  "repository-assignments",
  "draft",
] as const

interface CachedRepositoryAssignmentDraft {
  baseline: string
  draft: PRLifecycleRepositoryAssignmentSnapshot
}

interface PRLifecycleRepositoryAssignmentsPageProps {
  onBack: () => void
  discardOpen?: boolean
  onDiscardOpenChange?: (open: boolean) => void | Promise<void>
}

export function PRLifecycleRepositoryAssignmentsPage({
  onBack,
  discardOpen,
  onDiscardOpenChange,
}: PRLifecycleRepositoryAssignmentsPageProps) {
  const queryClient = useQueryClient()
  const cachedDraft = queryClient.getQueryData<CachedRepositoryAssignmentDraft>(
    assignmentDraftQueryKey,
  )
  const cachedDirty =
    cachedDraft != null &&
    JSON.stringify(cachedDraft.draft.repositoryAssignments) !==
      cachedDraft.baseline
  const [draft, setDraft] =
    useState<PRLifecycleRepositoryAssignmentSnapshot | null>(() =>
      cachedDirty ? structuredClone(cachedDraft.draft) : null,
    )
  const [baseline, setBaseline] = useState(() =>
    cachedDirty ? cachedDraft.baseline : "",
  )
  const [newRepository, setNewRepository] = useState("")
  const [newConfigurationID, setNewConfigurationID] = useState("")
  const [localDiscardOpen, setLocalDiscardOpen] = useState(false)
  const [error, setError] = useState("")
  const dirty =
    draft != null && JSON.stringify(draft.repositoryAssignments) !== baseline
  const resolvedDiscardOpen = Boolean(discardOpen || localDiscardOpen)

  const query = useQuery({
    queryKey: assignmentQueryKey,
    queryFn: ({ signal }) => getPRLifecycleRepositoryAssignments(signal),
    retry: false,
  })

  useEffect(() => {
    if (!query.data || dirty) return
    const next = structuredClone(query.data)
    setDraft(next)
    setBaseline(JSON.stringify(next.repositoryAssignments))
    setNewConfigurationID((current) =>
      next.workflowConfigurations[current]
        ? current
        : next.defaultWorkflowConfiguration,
    )
  }, [dirty, query.data])

  useEffect(() => {
    if (!draft || !baseline) return
    queryClient.setQueryData<CachedRepositoryAssignmentDraft>(
      assignmentDraftQueryKey,
      { baseline, draft },
    )
  }, [baseline, draft, queryClient])

  const issues = useMemo(
    () => (draft ? validatePRLifecycleRepositoryAssignments(draft) : []),
    [draft],
  )
  const newRepositoryIssue = useMemo(() => {
    if (!newRepository) return ""
    const canonical = canonicalPRLifecycleRepositoryIdentity(newRepository)
    if (!canonical) {
      return "Use an exact https:// origin and repository ID separated by one |, without surrounding whitespace (maximum 1024 bytes)."
    }
    const collision = Object.keys(draft?.repositoryAssignments ?? {}).find(
      (repository) =>
        repository !== newRepository &&
        canonicalPRLifecycleRepositoryIdentity(repository) === canonical,
    )
    return collision
      ? `This identity collides with ${collision} after case and trailing-origin-slash normalization.`
      : ""
  }, [draft?.repositoryAssignments, newRepository])
  const saveMutation = useMutation({
    mutationFn: (value: PRLifecycleRepositoryAssignmentSnapshot) =>
      putPRLifecycleRepositoryAssignments({
        expectedConfigRevision: value.configRevision,
        requestID: createPRWorkspaceRequestID(),
        repositoryAssignments: value.repositoryAssignments,
      }),
    onSuccess: (next, submitted) => {
      const saved = structuredClone(next)
      const nextBaseline = JSON.stringify(saved.repositoryAssignments)
      const submittedAssignments = JSON.stringify(
        submitted.repositoryAssignments,
      )
      setDraft((current) => {
        if (
          !current ||
          JSON.stringify(current.repositoryAssignments) === submittedAssignments
        ) {
          return saved
        }
        return {
          ...current,
          workflowConfigurations: structuredClone(saved.workflowConfigurations),
          defaultWorkflowConfiguration: saved.defaultWorkflowConfiguration,
          configRevision: saved.configRevision,
          effects: structuredClone(saved.effects),
        }
      })
      setBaseline(nextBaseline)
      setError("")
      queryClient.setQueryData(assignmentQueryKey, next)
      queryClient.setQueryData<CachedRepositoryAssignmentDraft>(
        assignmentDraftQueryKey,
        { baseline: nextBaseline, draft: saved },
      )
      void queryClient.invalidateQueries({
        queryKey: ["pr-lifecycle", "workflow-configurations"],
      })
    },
    onError: (failure) =>
      setError(
        failure instanceof Error
          ? failure.message
          : "Repository assignments could not be saved.",
      ),
  })

  const shouldBlockNavigation = useCallback(
    ({
      current,
      next,
    }: {
      current: { pathname: string }
      next: { pathname: string }
    }) =>
      dirty &&
      current.pathname === "/pull-requests/repository-assignments" &&
      next.pathname !== "/pull-requests/repository-assignments",
    [dirty],
  )
  const blocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: () => dirty,
    disabled: !dirty,
    withResolver: true,
  })

  useEffect(() => {
    if (blocker.status !== "blocked" || resolvedDiscardOpen) return
    setLocalDiscardOpen(true)
    void onDiscardOpenChange?.(true)
  }, [blocker.status, onDiscardOpenChange, resolvedDiscardOpen])

  const updateAssignments = (update: (next: Record<string, string>) => void) =>
    setDraft((current) => {
      if (!current) return current
      const next = structuredClone(current)
      update(next.repositoryAssignments)
      return next
    })

  const addAssignment = (event: FormEvent) => {
    event.preventDefault()
    const repository = newRepository
    if (
      !repository ||
      newRepositoryIssue ||
      !draft?.workflowConfigurations[newConfigurationID]
    ) {
      return
    }
    updateAssignments(
      (assignments) => void (assignments[repository] = newConfigurationID),
    )
    setNewRepository("")
  }

  const requestBack = () => {
    if (dirty) {
      setLocalDiscardOpen(true)
      void onDiscardOpenChange?.(true)
      return
    }
    onBack()
  }
  const closeDiscard = (open: boolean) => {
    setLocalDiscardOpen(open)
    if (!open && blocker.status === "blocked") blocker.reset()
    void onDiscardOpenChange?.(open)
  }
  const discardChanges = async () => {
    const saved = query.data ? structuredClone(query.data) : null
    if (saved) {
      const nextBaseline = JSON.stringify(saved.repositoryAssignments)
      setDraft(saved)
      setBaseline(nextBaseline)
      queryClient.setQueryData<CachedRepositoryAssignmentDraft>(
        assignmentDraftQueryKey,
        { baseline: nextBaseline, draft: saved },
      )
    }
    setLocalDiscardOpen(false)
    await onDiscardOpenChange?.(false)
    if (blocker.status === "blocked") blocker.proceed()
    else onBack()
  }

  if (query.isError) {
    return (
      <RepositoryAssignmentsState
        text="Repository assignments are unavailable."
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }
  if (query.isPending || !draft) {
    return <RepositoryAssignmentsState text="Loading repository assignments…" />
  }

  const configurations = Object.entries(draft.workflowConfigurations)

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-repository-assignments"
      aria-busy={saveMutation.isPending}
    >
      <PageHeader
        title="Repository assignments"
        titleExtra={
          <Badge className="hidden sm:inline-flex" variant="outline">
            PR lifecycle
          </Badge>
        }
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Back"
          title="Back"
          onClick={requestBack}
        >
          <IconArrowLeft />
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Refresh"
          title="Refresh"
          onClick={() => void query.refetch()}
        >
          <IconRefresh />
        </Button>
        <Button
          type="button"
          disabled={!dirty || issues.length > 0 || saveMutation.isPending}
          aria-label="Save assignments"
          title="Save assignments"
          onClick={() => saveMutation.mutate(draft)}
        >
          <IconDeviceFloppy />
          <span className="hidden sm:inline">Save assignments</span>
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-8 md:px-6">
        <div className="mx-auto w-full max-w-5xl space-y-4">
          {(error || issues.length > 0) && (
            <RepositoryAssignmentIssues error={error} issues={issues} />
          )}
          {draft.effects.gatewayEffect === "restart-required" && (
            <div
              className="border-warning/50 bg-warning/10 flex items-start gap-2 rounded-lg border p-3 text-sm"
              role="status"
            >
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
              Restart the gateway to apply saved repository assignments.
            </div>
          )}
          <Card size="sm">
            <CardHeader>
              <CardTitle role="heading" aria-level={2}>
                Repository routing
              </CardTitle>
              <CardDescription>
                Assign a workflow configuration to a repository. Repositories
                without an explicit assignment use the default workflow
                configuration,{" "}
                {configurationLabel(draft, draft.defaultWorkflowConfiguration)}.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <form
                className="grid gap-2 md:grid-cols-[minmax(0,1.5fr)_minmax(12rem,1fr)_auto]"
                onSubmit={addAssignment}
              >
                <Input
                  aria-label="Repository identity"
                  placeholder="https://github.com|repository-id"
                  value={newRepository}
                  onChange={(event) => setNewRepository(event.target.value)}
                />
                <Select
                  value={newConfigurationID}
                  onValueChange={setNewConfigurationID}
                >
                  <SelectTrigger aria-label="Workflow configuration">
                    <SelectValue placeholder="Choose configuration" />
                  </SelectTrigger>
                  <SelectContent>
                    {configurations.map(([configurationID, configuration]) => (
                      <SelectItem key={configurationID} value={configurationID}>
                        {configuration.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  type="submit"
                  variant="outline"
                  disabled={
                    !newRepository ||
                    Boolean(newRepositoryIssue) ||
                    !draft.workflowConfigurations[newConfigurationID]
                  }
                >
                  <IconPlus /> Add assignment
                </Button>
                {newRepositoryIssue && (
                  <p
                    className="text-destructive text-xs md:col-span-3"
                    role="alert"
                  >
                    {newRepositoryIssue}
                  </p>
                )}
              </form>

              {Object.keys(draft.repositoryAssignments).length === 0 ? (
                <p className="text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm">
                  No explicit repository assignments. Every repository uses the
                  default workflow configuration.
                </p>
              ) : (
                <div className="space-y-2" aria-label="Repository assignments">
                  {Object.entries(draft.repositoryAssignments)
                    .sort(([left], [right]) => left.localeCompare(right))
                    .map(([repository, configurationID]) => (
                      <div
                        className="grid min-w-0 gap-2 rounded-lg border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(12rem,18rem)_auto] sm:items-center"
                        key={repository}
                      >
                        <span className="min-w-0 font-mono text-xs break-all">
                          {repository}
                        </span>
                        <Select
                          value={configurationID}
                          onValueChange={(nextConfigurationID) =>
                            updateAssignments(
                              (assignments) =>
                                void (assignments[repository] =
                                  nextConfigurationID),
                            )
                          }
                        >
                          <SelectTrigger
                            aria-label={`Workflow configuration for ${repository}`}
                          >
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {configurations.map(
                              ([candidateID, configuration]) => (
                                <SelectItem
                                  key={candidateID}
                                  value={candidateID}
                                >
                                  {configuration.name}
                                </SelectItem>
                              ),
                            )}
                          </SelectContent>
                        </Select>
                        <Button
                          type="button"
                          size="icon"
                          variant="ghost"
                          aria-label={`Remove ${repository}`}
                          title={`Remove ${repository}`}
                          onClick={() =>
                            updateAssignments(
                              (assignments) =>
                                void delete assignments[repository],
                            )
                          }
                        >
                          <IconTrash />
                        </Button>
                      </div>
                    ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      <AlertDialog open={resolvedDiscardOpen} onOpenChange={closeDiscard}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Discard repository assignment changes?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Your unsaved repository assignment changes will be lost.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={discardChanges}>
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function configurationLabel(
  snapshot: PRLifecycleRepositoryAssignmentSnapshot,
  configurationID: string,
): string {
  const name = snapshot.workflowConfigurations[configurationID]?.name
  return name ? `${name} (${configurationID})` : configurationID
}

function RepositoryAssignmentIssues({
  error,
  issues,
}: {
  error: string
  issues: PRLifecycleRepositoryAssignmentIssue[]
}) {
  return (
    <div
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
      role="alert"
    >
      <strong>
        {error ||
          `${issues.length} repository assignment ${issues.length === 1 ? "issue" : "issues"}.`}
      </strong>
      {issues.length > 0 && (
        <ul className="mt-2 list-disc space-y-1 pl-5">
          {issues.map((issue) => (
            <li key={`${issue.path}:${issue.message}`}>
              <span className="font-mono text-xs">{issue.path}</span> —{" "}
              {issue.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function RepositoryAssignmentsState({
  text,
  action,
}: {
  text: string
  action?: React.ReactNode
}) {
  return (
    <div className="flex h-full items-center justify-center p-6">
      <div className="text-muted-foreground flex flex-col items-center gap-3 text-sm">
        <p>{text}</p>
        {action}
      </div>
    </div>
  )
}
