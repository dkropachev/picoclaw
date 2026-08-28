import { IconEdit, IconPlus, IconStar, IconTrash } from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import { CollectionAPIError } from "@/api/collection"
import type { PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-flow"
import {
  type PRLifecycleRepositoryAssignment,
  type PRLifecycleRepositoryAssignmentInput,
  type PRLifecycleRepositoryAssignmentSummary,
  bulkDeletePRLifecycleRepositoryAssignments,
  createPRLifecycleRepositoryAssignment,
  deletePRLifecycleRepositoryAssignment,
  getPRLifecycleRepositoryAssignment,
  listPRLifecycleRepositoryAssignments,
  resolveDevelopmentRepository,
  updatePRLifecycleRepositoryAssignment,
} from "@/api/pr-lifecycle-repository-assignments"
import {
  type PRLifecycleDeferredIssueModeV3,
  type PRLifecycleGateBinding,
  type PRLifecycleWorkflowConfiguration,
  type PRLifecycleWorkflowConfigurationInput,
  type PRLifecycleWorkflowConfigurationItem,
  type PRLifecycleWorkflowConfigurationSummary,
  createPRLifecycleWorkflowConfiguration,
  defaultScopeDisposition,
  deletePRLifecycleWorkflowConfiguration,
  getPRLifecycleWorkflowConfiguration,
  isPRLifecycleWorkflowConfigurationID,
  listPRLifecycleWorkflowConfigurations,
  makePRLifecycleWorkflowConfigurationDefault,
  updatePRLifecycleWorkflowConfiguration,
  validatePRLifecycleWorkflowConfigurations,
} from "@/api/pr-lifecycle-workflow-configurations"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  StandardCollectionPage,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import {
  prLifecycleAdministrativeCollectionViews,
  repositoryAssignmentsDefaultQuery,
  workflowConfigurationsDefaultQuery,
} from "@/components/pr-workspaces/pr-lifecycle-collection-route-state"
import { PRLifecycleGateMap } from "@/components/pr-workspaces/pr-lifecycle-gate-map"
import {
  PRLifecycleGateActionDialog,
  PRLifecycleRestartNotice,
  PRLifecycleWorkflowConfigurationIssues,
  PRLifecycleWorkflowConfigurationSettings,
} from "@/components/pr-workspaces/pr-lifecycle-workflow-configurations-page"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
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
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"

const repositoryAssignmentsCollectionKey = [
  "pr-lifecycle",
  "repository-assignments",
  "collection",
] as const
const workflowConfigurationsCollectionKey = [
  "pr-lifecycle",
  "workflow-configurations",
  "collection",
] as const

export function PRLifecycleRepositoryAssignmentsCollectionPage({
  search,
  onSearchChange,
  onOpen,
  onEdit,
  onNew,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen: (assignment: PRLifecycleRepositoryAssignmentSummary) => void
  onEdit: (assignment: PRLifecycleRepositoryAssignmentSummary) => void
  onNew: () => void
}) {
  const activeQuery = search.q || repositoryAssignmentsDefaultQuery
  const query = useInfiniteQuery({
    queryKey: [...repositoryAssignmentsCollectionKey, activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listPRLifecycleRepositoryAssignments(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
  })
  const assignments = useMemo(
    () =>
      uniqueByID(
        query.data?.pages.flatMap((page) => page.repository_assignments) ?? [],
      ),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const definition = useMemo<
    CollectionDefinition<PRLifecycleRepositoryAssignmentSummary>
  >(
    () => ({
      key: "development-repository-assignments",
      title: "Repository assignments",
      defaultQuery: repositoryAssignmentsDefaultQuery,
      supportedViews: prLifecycleAdministrativeCollectionViews,
      defaultView: "list",
      getItemID: (assignment) => assignment.id,
      getItemLabel: (assignment) => assignment.repository,
      getItemIdentity: (assignment) => ({
        title: assignment.repository,
        description: assignment.default_branch,
      }),
      columns: [
        {
          id: "configuration",
          header: "Configuration",
          cell: (assignment) => assignment.configuration,
        },
        {
          id: "default-branch",
          header: "Default branch",
          cell: (assignment) => assignment.default_branch,
        },
      ],
      gridFacts: [
        {
          id: "configuration",
          label: "Configuration",
          value: (assignment) => assignment.configuration,
        },
        {
          id: "default-branch",
          label: "Default branch",
          value: (assignment) => assignment.default_branch,
        },
      ],
      badges: [
        {
          id: "configuration",
          label: (assignment) => assignment.configuration,
          variant: "secondary",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit assignment",
          icon: <IconEdit />,
          onSelect: onEdit,
        },
      ],
    }),
    [onEdit],
  )

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={assignments}
      total={first?.total}
      schema={first?.query_schema}
      canonicalQuery={first?.canonical_query}
      loading={query.isPending}
      fetching={query.isFetching}
      error={query.error}
      onRefresh={() => query.refetch()}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={() => query.fetchNextPage()}
      onOpenItem={onOpen}
      onBulkDelete={async (ids) => {
        if (!first?.config_revision) {
          throw new Error("Repository assignment revision is unavailable.")
        }
        return bulkDeletePRLifecycleRepositoryAssignments(
          ids,
          first.config_revision,
        )
      }}
      afterBulkDelete={() => query.refetch()}
      bulkDeleteConfirmation={{
        title: (count) =>
          `Remove ${count} repository assignment${count === 1 ? "" : "s"}?`,
        description:
          "Repositories without an assignment use the default workflow configuration.",
        actionLabel: "Remove assignments",
      }}
      addAction={
        <Button type="button" size="sm" onClick={onNew}>
          <IconPlus /> Add repository
        </Button>
      }
      emptyTitle="No repository assignments"
      emptyDescription="Add a verified repository and choose its workflow configuration."
    />
  )
}

export function PRLifecycleWorkflowConfigurationsCollectionPage({
  search,
  onSearchChange,
  onOpen,
  onEdit,
  onNew,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onOpen: (configuration: PRLifecycleWorkflowConfigurationSummary) => void
  onEdit: (configuration: PRLifecycleWorkflowConfigurationSummary) => void
  onNew: () => void
}) {
  const queryClient = useQueryClient()
  const [deleteTarget, setDeleteTarget] =
    useState<PRLifecycleWorkflowConfigurationSummary | null>(null)
  const activeQuery = search.q || workflowConfigurationsDefaultQuery
  const query = useInfiniteQuery({
    queryKey: [...workflowConfigurationsCollectionKey, activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listPRLifecycleWorkflowConfigurations(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (page) => page.next_cursor || undefined,
    retry: false,
  })
  const configurations = useMemo(
    () =>
      uniqueByID(
        query.data?.pages.flatMap((page) => page.workflow_configurations) ?? [],
      ),
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const makeDefault = useMutation({
    mutationFn: (configuration: PRLifecycleWorkflowConfigurationSummary) => {
      if (!first?.config_revision) {
        throw new Error("Workflow configuration revision is unavailable.")
      }
      return makePRLifecycleWorkflowConfigurationDefault(
        configuration.id,
        first.config_revision,
      )
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      await query.refetch()
      toast.success("Default workflow configuration updated")
    },
    onError: (error) => {
      toast.error(errorMessage(error))
      void query.refetch()
    },
  })
  const remove = useMutation({
    mutationFn: (configuration: PRLifecycleWorkflowConfigurationSummary) => {
      if (!first?.config_revision) {
        throw new Error("Workflow configuration revision is unavailable.")
      }
      return deletePRLifecycleWorkflowConfiguration(
        configuration.id,
        first.config_revision,
      )
    },
    onSuccess: async (_, configuration) => {
      setDeleteTarget(null)
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      await query.refetch()
      toast.success(`${configuration.name} was removed.`)
    },
    onError: (error) => {
      toast.error(errorMessage(error))
      void query.refetch()
    },
  })
  const definition = useMemo<
    CollectionDefinition<PRLifecycleWorkflowConfigurationSummary>
  >(
    () => ({
      key: "development-workflow-configurations",
      title: "Workflow configurations",
      defaultQuery: workflowConfigurationsDefaultQuery,
      supportedViews: prLifecycleAdministrativeCollectionViews,
      defaultView: "list",
      getItemID: (configuration) => configuration.id,
      getItemLabel: (configuration) => configuration.name,
      getItemIdentity: (configuration) => ({
        title: configuration.name,
        description: configuration.id,
        metadata: `${configuration.bindings} ${configuration.bindings === 1 ? "Gate override" : "Gate overrides"}`,
      }),
      columns: [
        {
          id: "bindings",
          header: "Bindings",
          cell: (configuration) => configuration.bindings,
        },
        {
          id: "deferred-issues",
          header: "Deferred issues",
          cell: (configuration) => titleCase(configuration.deferred_issues),
        },
      ],
      gridFacts: [
        {
          id: "bindings",
          label: "Bindings",
          value: (configuration) => configuration.bindings,
        },
        {
          id: "deferred-issues",
          label: "Deferred issues",
          value: (configuration) => titleCase(configuration.deferred_issues),
        },
      ],
      badges: [
        {
          id: "default",
          label: (configuration) =>
            configuration.is_default ? "Default" : null,
          variant: "secondary",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit configuration",
          icon: <IconEdit />,
          onSelect: onEdit,
        },
        {
          id: "make-default",
          label: "Make default",
          icon: <IconStar />,
          disabled: (configuration) =>
            configuration.is_default ||
            makeDefault.isPending ||
            remove.isPending,
          onSelect: (configuration) => makeDefault.mutate(configuration),
        },
        {
          id: "delete",
          label: "Remove configuration",
          icon: <IconTrash />,
          destructive: true,
          hidden: (configuration) =>
            configuration.is_default || configuration.id === "default",
          disabled: () => makeDefault.isPending || remove.isPending,
          onSelect: setDeleteTarget,
        },
      ],
    }),
    [makeDefault, onEdit, remove],
  )

  return (
    <>
      <StandardCollectionPage
        definition={definition}
        search={search}
        onSearchChange={onSearchChange}
        items={configurations}
        total={first?.total}
        schema={first?.query_schema}
        canonicalQuery={first?.canonical_query}
        loading={query.isPending}
        fetching={query.isFetching}
        error={query.error}
        onRefresh={() => query.refetch()}
        hasNextPage={query.hasNextPage}
        loadingMore={query.isFetchingNextPage}
        onLoadMore={() => query.fetchNextPage()}
        onOpenItem={onOpen}
        addAction={
          <Button type="button" size="sm" onClick={onNew}>
            <IconPlus /> New configuration
          </Button>
        }
        emptyTitle="No workflow configurations"
        emptyDescription="Create a workflow configuration to customize lifecycle Gates."
      />
      <AlertDialog
        open={deleteTarget != null}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setDeleteTarget(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Remove {deleteTarget?.name || "workflow configuration"}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Referenced configurations cannot be removed. Existing development
              history is retained.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={remove.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={remove.isPending || !deleteTarget}
              onClick={() => {
                if (deleteTarget) remove.mutate(deleteTarget)
              }}
            >
              Remove configuration
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

export function PRLifecycleRepositoryAssignmentDetailPage({
  assignmentID,
  onBack,
  onEdit,
  onDeleted,
}: {
  assignmentID: string
  onBack: () => void
  onEdit: () => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: [...repositoryAssignmentsCollectionKey, "detail", assignmentID],
    queryFn: ({ signal }) =>
      getPRLifecycleRepositoryAssignment(assignmentID, signal),
    retry: false,
  })
  const assignment = query.data?.repository_assignment
  const remove = useMutation({
    mutationFn: async () => {
      if (!query.data) throw new Error("Assignment is unavailable.")
      const response = await deletePRLifecycleRepositoryAssignment(
        assignmentID,
        query.data.config_revision,
      )
      const failure = response.failures.find((item) => item.id === assignmentID)
      if (failure) throw new Error(deleteFailureMessage(failure.code))
      return response
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: repositoryAssignmentsCollectionKey,
      })
      toast.success("Repository assignment removed")
      onDeleted()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <CollectionDetailShell
      title={assignment?.repository || "Repository assignment"}
      identity={<span className="font-mono text-xs">{assignmentID}</span>}
      status={
        assignment ? (
          <Badge variant="secondary">{assignment.configuration}</Badge>
        ) : undefined
      }
      loading={query.isPending}
      notFound={notFound}
      error={!notFound ? errorMessage(query.error) || undefined : undefined}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All repository assignments"
      actions={
        assignment ? (
          <>
            <Button type="button" size="sm" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
            <AlertDialog>
              <AlertDialogTrigger asChild>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={remove.isPending}
                >
                  <IconTrash /> Remove
                </Button>
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>
                    Remove {assignment.repository}?
                  </AlertDialogTitle>
                  <AlertDialogDescription>
                    New development work for this repository will use the
                    default workflow configuration.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>Cancel</AlertDialogCancel>
                  <AlertDialogAction
                    variant="destructive"
                    onClick={() => remove.mutate()}
                  >
                    Remove assignment
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </>
        ) : undefined
      }
    >
      {assignment && (
        <div className="grid gap-4 md:grid-cols-2">
          <DetailCard title="Repository" value={assignment.repository} />
          <DetailCard
            title="Workflow configuration"
            value={
              query.data?.workflow_configurations[assignment.configuration]
                ?.name || assignment.configuration
            }
          />
          <DetailCard
            title="Default branch"
            value={assignment.default_branch}
          />
          <DetailCard
            title="Provider identity"
            value={`${assignment.provider_origin}|${assignment.repository_id}`}
            mono
          />
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function PRLifecycleWorkflowConfigurationDetailPage({
  configurationID,
  onBack,
  onEdit,
  onDeleted,
}: {
  configurationID: string
  onBack: () => void
  onEdit: () => void
  onDeleted: () => void
}) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: [
      ...workflowConfigurationsCollectionKey,
      "detail",
      configurationID,
    ],
    queryFn: ({ signal }) =>
      getPRLifecycleWorkflowConfiguration(configurationID, signal),
    retry: false,
  })
  const configuration = query.data?.workflow_configuration
  const makeDefault = useMutation({
    mutationFn: () => {
      if (!query.data) throw new Error("Configuration is unavailable.")
      return makePRLifecycleWorkflowConfigurationDefault(
        configurationID,
        query.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      queryClient.setQueryData(
        [...workflowConfigurationsCollectionKey, "detail", configurationID],
        response,
      )
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      toast.success("Default workflow configuration updated")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const remove = useMutation({
    mutationFn: async () => {
      if (!query.data) throw new Error("Configuration is unavailable.")
      const response = await deletePRLifecycleWorkflowConfiguration(
        configurationID,
        query.data.config_revision,
      )
      const failure = response.failures.find(
        (item) => item.id === configurationID,
      )
      if (failure) throw new Error(deleteFailureMessage(failure.code))
      return response
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      toast.success("Workflow configuration removed")
      onDeleted()
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <CollectionDetailShell
      title={configuration?.name || "Workflow configuration"}
      identity={<span className="font-mono text-xs">{configurationID}</span>}
      status={
        configuration?.isDefault ? (
          <Badge variant="secondary">Default</Badge>
        ) : undefined
      }
      loading={query.isPending}
      notFound={notFound}
      error={!notFound ? errorMessage(query.error) || undefined : undefined}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All workflow configurations"
      actions={
        configuration ? (
          <>
            {!configuration.isDefault && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={makeDefault.isPending}
                onClick={() => makeDefault.mutate()}
              >
                <IconStar /> Make default
              </Button>
            )}
            <Button type="button" size="sm" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
            {!configuration.isDefault && configuration.id !== "default" && (
              <AlertDialog>
                <AlertDialogTrigger asChild>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={remove.isPending}
                  >
                    <IconTrash /> Remove
                  </Button>
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>
                      Remove {configuration.name}?
                    </AlertDialogTitle>
                    <AlertDialogDescription>
                      Referenced or default configurations cannot be removed.
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>Cancel</AlertDialogCancel>
                    <AlertDialogAction
                      variant="destructive"
                      onClick={() => remove.mutate()}
                    >
                      Remove configuration
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
          </>
        ) : undefined
      }
    >
      {configuration && (
        <div className="grid gap-4 md:grid-cols-2">
          <DetailCard title="Name" value={configuration.name} />
          <DetailCard
            title="Deferred issues"
            value={titleCase(configuration.deferredIssues.mode)}
          />
          <DetailCard
            title="Gate bindings"
            value={String(configuration.bindings.length)}
          />
          <DetailCard
            title="Catalog revision"
            value={query.data?.catalog_revision || "—"}
            mono
          />
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function PRLifecycleRepositoryAssignmentEditorPage({
  mode,
  assignmentID,
  onBack,
  onSaved,
}: {
  mode: "create" | "edit"
  assignmentID?: string
  onBack: () => void
  onSaved: (id: string) => void
}) {
  const queryClient = useQueryClient()
  const [repositoryURL, setRepositoryURL] = useState("")
  const [configurationID, setConfigurationID] = useState("")
  const [baseline, setBaseline] = useState("")
  const [error, setError] = useState("")
  const [discardOpen, setDiscardOpen] = useState(false)
  const detail = useQuery({
    queryKey: [...repositoryAssignmentsCollectionKey, "detail", assignmentID],
    queryFn: ({ signal }) =>
      getPRLifecycleRepositoryAssignment(assignmentID!, signal),
    enabled: mode === "edit" && Boolean(assignmentID),
    retry: false,
  })
  const collection = useQuery({
    queryKey: [...repositoryAssignmentsCollectionKey, "create-context"],
    queryFn: ({ signal }) =>
      listPRLifecycleRepositoryAssignments({ limit: 1 }, signal),
    enabled: mode === "create",
    retry: false,
  })
  const configurations = useInfiniteQuery({
    queryKey: [...workflowConfigurationsCollectionKey, "assignment-choices"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listPRLifecycleWorkflowConfigurations(
        { cursor: pageParam || undefined, limit: 200 },
        signal,
      ),
    getNextPageParam: (page, pages) =>
      page.next_cursor &&
      !pages
        .slice(0, -1)
        .some((previous) => previous.next_cursor === page.next_cursor)
        ? page.next_cursor
        : undefined,
    enabled: mode === "create",
    retry: false,
  })
  const creationConfigurations = useMemo(
    () =>
      uniqueByID(
        configurations.data?.pages.flatMap(
          (page) => page.workflow_configurations,
        ) ?? [],
      ),
    [configurations.data?.pages],
  )
  const existing = detail.data?.repository_assignment
  const choices = useMemo(() => {
    if (mode === "edit") {
      return Object.entries(detail.data?.workflow_configurations ?? {}).map(
        ([id, configuration]) => ({ id, name: configuration.name }),
      )
    }
    return creationConfigurations.map((configuration) => ({
      id: configuration.id,
      name: configuration.name,
    }))
  }, [creationConfigurations, detail.data, mode])

  useEffect(() => {
    if (
      mode === "create" &&
      configurations.hasNextPage &&
      !configurations.isFetchingNextPage
    ) {
      void configurations.fetchNextPage()
    }
  }, [configurations, mode])

  useEffect(() => {
    if (mode !== "edit" || !existing || baseline) return
    setConfigurationID(existing.configuration)
    setBaseline(existing.configuration)
  }, [baseline, existing, mode])
  useEffect(() => {
    if (
      mode !== "create" ||
      configurationID ||
      choices.length === 0 ||
      configurations.hasNextPage ||
      configurations.isFetchingNextPage
    ) {
      return
    }
    const defaultChoice = creationConfigurations.find(
      (configuration) => configuration.is_default,
    )
    setConfigurationID(defaultChoice?.id || choices[0]!.id)
  }, [
    choices,
    configurationID,
    creationConfigurations,
    configurations.hasNextPage,
    configurations.isFetchingNextPage,
    mode,
  ])

  const dirty =
    mode === "create"
      ? Boolean(repositoryURL.trim())
      : Boolean(baseline && configurationID !== baseline)
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) =>
      dirty && current.pathname !== next.pathname,
    enableBeforeUnload: () => dirty,
    disabled: !dirty,
    withResolver: true,
  })
  useEffect(() => {
    if (blocker.status === "blocked") setDiscardOpen(true)
  }, [blocker.status])

  const save = useMutation({
    mutationFn: async () => {
      if (!configurationID) throw new Error("Choose a workflow configuration.")
      if (mode === "edit") {
        if (!existing || !detail.data)
          throw new Error("Assignment is unavailable.")
        return updatePRLifecycleRepositoryAssignment(
          existing.id,
          assignmentInput(existing, configurationID),
          detail.data.config_revision,
        )
      }
      if (!collection.data)
        throw new Error("Configuration revision is unavailable.")
      const verified = await resolveDevelopmentRepository(repositoryURL.trim())
      if (!verified.can_implement) {
        throw new Error("This repository cannot be used for development work.")
      }
      const separator = verified.identity.indexOf("|")
      if (separator < 1 || separator === verified.identity.length - 1) {
        throw new Error("The provider returned an invalid repository identity.")
      }
      return createPRLifecycleRepositoryAssignment(
        {
          provider_origin: verified.identity.slice(0, separator),
          repository_id: verified.identity.slice(separator + 1),
          repository: verified.name,
          configuration: configurationID,
          default_branch: verified.default_branch,
        },
        collection.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      setBaseline(response.repository_assignment.configuration)
      setRepositoryURL("")
      setError("")
      await queryClient.invalidateQueries({
        queryKey: repositoryAssignmentsCollectionKey,
      })
      toast.success(
        mode === "create"
          ? "Repository assignment created"
          : "Repository assignment updated",
      )
      onSaved(response.repository_assignment.id)
    },
    onError: (failure) => setError(errorMessage(failure)),
  })
  const loadError = detail.error || collection.error || configurations.error
  const notFound =
    detail.error instanceof CollectionAPIError && detail.error.status === 404
  const loading =
    mode === "edit"
      ? detail.isPending
      : collection.isPending ||
        configurations.isPending ||
        configurations.hasNextPage ||
        configurations.isFetchingNextPage

  return (
    <>
      <CollectionDetailShell
        title={
          mode === "create"
            ? "Add repository assignment"
            : existing?.repository || "Edit repository assignment"
        }
        identity={
          assignmentID ? (
            <span className="font-mono text-xs">{assignmentID}</span>
          ) : undefined
        }
        loading={loading}
        notFound={notFound}
        error={!notFound ? errorMessage(loadError) || undefined : undefined}
        onBack={() => (dirty ? setDiscardOpen(true) : onBack())}
        onRetry={() => {
          void detail.refetch()
          void collection.refetch()
          void configurations.refetch()
        }}
        backLabel="All repository assignments"
        actions={
          <Button
            type="button"
            size="sm"
            disabled={
              save.isPending ||
              !configurationID ||
              (mode === "create" && !validRepositoryURL(repositoryURL)) ||
              (mode === "edit" && !dirty)
            }
            onClick={() => save.mutate()}
          >
            {mode === "create" ? "Add repository" : "Save assignment"}
          </Button>
        }
      >
        <div className="mx-auto max-w-2xl space-y-4">
          {error && <InlineError message={error} />}
          {(detail.data?.effects.gateway_effect === "restart_required" ||
            collection.data?.effects.gateway_effect === "restart_required") && (
            <PRLifecycleRestartNotice />
          )}
          <Card size="sm">
            <CardHeader>
              <CardTitle>Repository routing</CardTitle>
              <CardDescription>
                Verify one provider repository and choose the lifecycle policy
                used for new development work.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="repository-assignment-repository">
                  Repository
                </Label>
                {mode === "create" ? (
                  <Input
                    id="repository-assignment-repository"
                    value={repositoryURL}
                    onChange={(event) => setRepositoryURL(event.target.value)}
                    placeholder="https://github.com/owner/repository"
                    autoComplete="off"
                  />
                ) : (
                  <Input
                    id="repository-assignment-repository"
                    value={existing?.repository || ""}
                    readOnly
                  />
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="repository-assignment-configuration">
                  Workflow configuration
                </Label>
                <Select
                  value={configurationID}
                  onValueChange={setConfigurationID}
                >
                  <SelectTrigger id="repository-assignment-configuration">
                    <SelectValue placeholder="Choose a configuration" />
                  </SelectTrigger>
                  <SelectContent>
                    {choices.map((choice) => (
                      <SelectItem key={choice.id} value={choice.id}>
                        {choice.name} ({choice.id})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {existing && (
                <p className="text-muted-foreground text-xs">
                  Default branch: {existing.default_branch}
                </p>
              )}
            </CardContent>
          </Card>
        </div>
      </CollectionDetailShell>
      <DiscardDialog
        open={discardOpen}
        title="Discard repository assignment changes?"
        description="Your unsaved repository assignment changes will be lost."
        onCancel={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.reset()
        }}
        onDiscard={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.proceed()
          else onBack()
        }}
      />
    </>
  )
}

export function PRLifecycleWorkflowConfigurationCreatePage({
  onBack,
  onSaved,
}: {
  onBack: () => void
  onSaved: (id: string) => void
}) {
  const queryClient = useQueryClient()
  const [id, setID] = useState("")
  const [name, setName] = useState("")
  const [deferredIssues, setDeferredIssues] =
    useState<PRLifecycleDeferredIssueModeV3>("ask")
  const [error, setError] = useState("")
  const [discardOpen, setDiscardOpen] = useState(false)
  const context = useQuery({
    queryKey: [...workflowConfigurationsCollectionKey, "create-context"],
    queryFn: ({ signal }) =>
      listPRLifecycleWorkflowConfigurations({ limit: 1 }, signal),
    retry: false,
  })
  const dirty = Boolean(id || name)
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) =>
      dirty && current.pathname !== next.pathname,
    enableBeforeUnload: () => dirty,
    disabled: !dirty,
    withResolver: true,
  })
  useEffect(() => {
    if (blocker.status === "blocked") setDiscardOpen(true)
  }, [blocker.status])
  const create = useMutation({
    mutationFn: () => {
      if (!context.data)
        throw new Error("Configuration revision is unavailable.")
      return createPRLifecycleWorkflowConfiguration(
        {
          id,
          name: name.trim(),
          bindings: [],
          deferredIssues: { mode: deferredIssues },
          scopeDisposition: defaultScopeDisposition(),
        },
        context.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      setID("")
      setName("")
      setError("")
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      toast.success("Workflow configuration created")
      onSaved(response.workflow_configuration.id)
    },
    onError: (failure) => setError(errorMessage(failure)),
  })

  return (
    <>
      <CollectionDetailShell
        title="New workflow configuration"
        loading={context.isPending}
        error={errorMessage(context.error) || undefined}
        onBack={() => (dirty ? setDiscardOpen(true) : onBack())}
        onRetry={() => void context.refetch()}
        backLabel="All workflow configurations"
        actions={
          <Button
            type="button"
            size="sm"
            disabled={
              create.isPending ||
              !isPRLifecycleWorkflowConfigurationID(id) ||
              !name.trim()
            }
            onClick={() => create.mutate()}
          >
            Create configuration
          </Button>
        }
      >
        <div className="mx-auto max-w-2xl space-y-4">
          {error && <InlineError message={error} />}
          <Card size="sm">
            <CardHeader>
              <CardTitle>Configuration identity</CardTitle>
              <CardDescription>
                Create the stable configuration first, then customize its
                lifecycle Gate bindings.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="workflow-configuration-id">
                  Configuration ID
                </Label>
                <Input
                  id="workflow-configuration-id"
                  value={id}
                  onChange={(event) => setID(event.target.value)}
                  placeholder="configuration-id"
                  pattern="[a-z][a-z0-9-]{0,63}"
                  autoComplete="off"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="workflow-configuration-name">Name</Label>
                <Input
                  id="workflow-configuration-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="Configuration name"
                  autoComplete="off"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="workflow-configuration-deferred">
                  Deferred issue mode
                </Label>
                <Select
                  value={deferredIssues}
                  onValueChange={(value) =>
                    setDeferredIssues(value as PRLifecycleDeferredIssueModeV3)
                  }
                >
                  <SelectTrigger id="workflow-configuration-deferred">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="off">Off</SelectItem>
                    <SelectItem value="ask">Ask</SelectItem>
                    <SelectItem value="automatic">Automatic</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </CardContent>
          </Card>
        </div>
      </CollectionDetailShell>
      <DiscardDialog
        open={discardOpen}
        title="Discard new workflow configuration?"
        description="Your unsaved configuration will be lost."
        onCancel={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.reset()
        }}
        onDiscard={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.proceed()
          else onBack()
        }}
      />
    </>
  )
}

export function PRLifecycleWorkflowConfigurationEditorPage({
  configurationID,
  flowID,
  decisionPoint,
  onBack,
  onFlowChange,
  onDecisionPointChange,
}: {
  configurationID: string
  flowID: "review" | "implementation"
  decisionPoint?: PRLifecycleDecisionPoint
  onBack: () => void
  onFlowChange: (flow: "review" | "implementation") => void
  onDecisionPointChange: (decisionPoint?: PRLifecycleDecisionPoint) => void
}) {
  const queryClient = useQueryClient()
  const [draft, setDraft] =
    useState<PRLifecycleWorkflowConfigurationItem | null>(null)
  const [baseline, setBaseline] = useState("")
  const [error, setError] = useState("")
  const [discardOpen, setDiscardOpen] = useState(false)
  const query = useQuery({
    queryKey: [
      ...workflowConfigurationsCollectionKey,
      "detail",
      configurationID,
    ],
    queryFn: ({ signal }) =>
      getPRLifecycleWorkflowConfiguration(configurationID, signal),
    retry: false,
  })
  useEffect(() => {
    if (!query.data || draft || baseline) return
    const next = structuredClone(query.data.workflow_configuration)
    setDraft(next)
    setBaseline(JSON.stringify(next))
  }, [baseline, draft, query.data])
  const dirty = Boolean(draft && JSON.stringify(draft) !== baseline)
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) =>
      dirty && current.pathname !== next.pathname,
    enableBeforeUnload: () => dirty,
    disabled: !dirty,
    withResolver: true,
  })
  useEffect(() => {
    if (blocker.status === "blocked") setDiscardOpen(true)
  }, [blocker.status])
  const issues = useMemo(
    () =>
      draft && query.data
        ? validateItemConfiguration(draft, query.data.gate_catalog)
        : [],
    [draft, query.data],
  )
  const save = useMutation({
    mutationFn: () => {
      if (!draft || !query.data)
        throw new Error("Configuration is unavailable.")
      return updatePRLifecycleWorkflowConfiguration(
        configurationID,
        itemInput(draft),
        query.data.config_revision,
      )
    },
    onSuccess: async (response) => {
      const next = structuredClone(response.workflow_configuration)
      setDraft(next)
      setBaseline(JSON.stringify(next))
      setError("")
      queryClient.setQueryData(
        [...workflowConfigurationsCollectionKey, "detail", configurationID],
        response,
      )
      await queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      toast.success("Workflow configuration saved")
    },
    onError: (failure) => {
      setError(errorMessage(failure))
      void query.refetch()
    },
  })
  const makeDefault = useMutation({
    mutationFn: () => {
      if (dirty) throw new Error("Save changes before making this the default.")
      if (!query.data) throw new Error("Configuration is unavailable.")
      return makePRLifecycleWorkflowConfigurationDefault(
        configurationID,
        query.data.config_revision,
      )
    },
    onSuccess: (response) => {
      const next = structuredClone(response.workflow_configuration)
      setDraft(next)
      setBaseline(JSON.stringify(next))
      queryClient.setQueryData(
        [...workflowConfigurationsCollectionKey, "detail", configurationID],
        response,
      )
      void queryClient.invalidateQueries({
        queryKey: workflowConfigurationsCollectionKey,
      })
      toast.success("Default workflow configuration updated")
    },
    onError: (failure) => setError(errorMessage(failure)),
  })
  const updateDraft = useCallback(
    (update: (configuration: PRLifecycleWorkflowConfiguration) => void) => {
      setDraft((current) => {
        if (!current) return current
        const next = structuredClone(current)
        update(next)
        return next
      })
    },
    [],
  )
  const selectedGateNode = decisionPoint
    ? query.data?.flow.flows
        .flatMap((flow) => flow.nodes)
        .find(
          (node) =>
            node.kind === "gate" &&
            node.editable &&
            node.decision_point === decisionPoint,
        )
    : undefined
  const selectedCatalogEntry = decisionPoint
    ? query.data?.gate_catalog[decisionPoint]
    : undefined
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <>
      <CollectionDetailShell
        title={draft ? `Edit ${draft.name}` : "Edit workflow configuration"}
        identity={<span className="font-mono text-xs">{configurationID}</span>}
        status={
          draft?.isDefault ? (
            <Badge variant="secondary">Default</Badge>
          ) : undefined
        }
        loading={query.isPending}
        notFound={notFound}
        error={!notFound ? errorMessage(query.error) || undefined : undefined}
        onBack={() => (dirty ? setDiscardOpen(true) : onBack())}
        onRetry={() => void query.refetch()}
        backLabel="Workflow configuration"
        contentClassName="max-w-[96rem]"
        actions={
          draft ? (
            <Button
              type="button"
              size="sm"
              disabled={!dirty || issues.length > 0 || save.isPending}
              onClick={() => save.mutate()}
            >
              Save configuration
            </Button>
          ) : undefined
        }
      >
        {draft && query.data && (
          <div className="space-y-4">
            {(error || issues.length > 0) && (
              <PRLifecycleWorkflowConfigurationIssues
                error={error}
                issues={issues}
              />
            )}
            {query.data.effects.gateway_effect === "restart_required" && (
              <PRLifecycleRestartNotice />
            )}
            <PRLifecycleGateMap
              activeFlowID={flowID}
              flow={query.data.flow}
              flowRevision={query.data.flow_revision}
              selectedDecisionPoint={decisionPoint}
              gateCatalog={query.data.gate_catalog}
              bindings={draft.bindings}
              configName={draft.name}
              configID={configurationID}
              onFlowChange={onFlowChange}
              onSelect={onDecisionPointChange}
            />
            <PRLifecycleWorkflowConfigurationSettings
              config={draft}
              configID={configurationID}
              defaultConfigID={draft.isDefault ? configurationID : ""}
              onChange={updateDraft}
              onDeferredIssueModeChange={(mode) =>
                updateDraft(
                  (configuration) =>
                    void (configuration.deferredIssues.mode = mode),
                )
              }
              onMakeDefault={() => makeDefault.mutate()}
            />
          </div>
        )}
      </CollectionDetailShell>
      <PRLifecycleGateActionDialog
        open={Boolean(
          !discardOpen && draft && selectedGateNode && decisionPoint,
        )}
        nodeTitle={selectedGateNode?.title ?? decisionPoint ?? "Gate"}
        nodeDescription={selectedGateNode?.description}
        catalogEntry={selectedCatalogEntry}
        readOnly={
          configurationID === "default" ||
          decisionPoint === "pr.implementation.publish"
        }
        binding={
          selectedCatalogEntry
            ? draft?.bindings.find(
                (binding) =>
                  binding.workflowRef === selectedCatalogEntry.workflowRef &&
                  binding.gateRef === selectedCatalogEntry.gateRef,
              )
            : undefined
        }
        onOpenChange={(open) => {
          if (!open) onDecisionPointChange()
        }}
        onActionChange={(action) => {
          if (
            !selectedCatalogEntry ||
            configurationID === "default" ||
            decisionPoint === "pr.implementation.publish"
          ) {
            return
          }
          updateDraft((configuration) => {
            updateBinding(configuration.bindings, selectedCatalogEntry, action)
          })
        }}
      />
      <DiscardDialog
        open={discardOpen}
        title="Discard workflow configuration changes?"
        description="Your unsaved Gate and configuration changes will be lost."
        onCancel={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.reset()
        }}
        onDiscard={() => {
          setDiscardOpen(false)
          if (blocker.status === "blocked") blocker.proceed()
          else onBack()
        }}
      />
    </>
  )
}

function updateBinding(
  bindings: PRLifecycleGateBinding[],
  catalogEntry: { workflowRef: string; gateRef: string },
  action: PRLifecycleGateBinding["action"],
) {
  const index = bindings.findIndex(
    (binding) =>
      binding.workflowRef === catalogEntry.workflowRef &&
      binding.gateRef === catalogEntry.gateRef,
  )
  if (!action) {
    if (index >= 0) bindings.splice(index, 1)
    return
  }
  const binding = {
    workflowRef: catalogEntry.workflowRef,
    gateRef: catalogEntry.gateRef,
    action,
  }
  if (index >= 0) bindings[index] = binding
  else bindings.push(binding)
}

function validateItemConfiguration(
  item: PRLifecycleWorkflowConfigurationItem,
  gateCatalog: Record<
    string,
    import("@/api/pr-lifecycle-workflow-configurations").PRLifecycleGateCatalogEntry
  >,
) {
  return validatePRLifecycleWorkflowConfigurations({
    workflowConfigurations: { [item.id]: item },
    defaultWorkflowConfiguration: item.id,
    nudge: {
      reviewMinimumAdditional: 0,
      reviewMaximumAdditional: 0,
      completionMinimumAdditional: 0,
      completionMaximumAdditional: 0,
    },
    scope: {
      xs: { files: 0, semanticLines: 0, modules: 0 },
      s: { files: 0, semanticLines: 0, modules: 0 },
      m: { files: 0, semanticLines: 0, modules: 0 },
    },
    gateCatalog,
  })
}

function itemInput(
  item: PRLifecycleWorkflowConfigurationItem,
): PRLifecycleWorkflowConfigurationInput {
  return {
    id: item.id,
    name: item.name,
    bindings: item.bindings,
    deferredIssues: item.deferredIssues,
    scopeDisposition: item.scopeDisposition,
  }
}

function assignmentInput(
  assignment: PRLifecycleRepositoryAssignment,
  configuration: string,
): PRLifecycleRepositoryAssignmentInput {
  return {
    provider_origin: assignment.provider_origin,
    repository_id: assignment.repository_id,
    repository: assignment.repository,
    configuration,
    default_branch: assignment.default_branch,
  }
}

function DetailCard({
  title,
  value,
  mono = false,
}: {
  title: string
  value: string
  mono?: boolean
}) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent className={mono ? "font-mono text-xs break-all" : "text-sm"}>
        {value}
      </CardContent>
    </Card>
  )
}

function InlineError({ message }: { message: string }) {
  return (
    <div
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
      role="alert"
    >
      {message}
    </div>
  )
}

function DiscardDialog({
  open,
  title,
  description,
  onCancel,
  onDiscard,
}: {
  open: boolean
  title: string
  description: string
  onCancel: () => void
  onDiscard: () => void
}) {
  return (
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onCancel()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel onClick={onCancel}>Keep editing</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={onDiscard}>
            Discard changes
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function validRepositoryURL(value: string): boolean {
  try {
    const parsed = new URL(value)
    return (
      parsed.protocol === "https:" &&
      !parsed.username &&
      !parsed.password &&
      !parsed.search &&
      !parsed.hash &&
      parsed.pathname.split("/").filter(Boolean).length === 2
    )
  } catch {
    return false
  }
}

function uniqueByID<T extends { id: string }>(items: T[]): T[] {
  return [...new Map(items.map((item) => [item.id, item])).values()]
}

function titleCase(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./u, (letter) => letter.toUpperCase())
}

function deleteFailureMessage(code: string): string {
  switch (code) {
    case "default":
      return "The default workflow configuration cannot be removed."
    case "referenced":
      return "The item is still referenced and cannot be removed."
    case "not_found":
      return "The item no longer exists."
    case "stale_version":
      return "The configuration changed. Refresh and try again."
    default:
      return `The item could not be removed (${code}).`
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : error ? String(error) : ""
}
