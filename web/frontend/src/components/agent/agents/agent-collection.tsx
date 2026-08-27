import {
  IconActivity,
  IconAdjustments,
  IconDeviceFloppy,
  IconEdit,
  IconLoader2,
  IconPlus,
  IconStar,
} from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type AgentDeleteBlocker,
  type AgentInfo,
  type AgentMutationEffects,
  AgentsAPIError,
  bulkDeleteAgents,
  createAgent,
  getAgent,
  getAgents,
  setDefaultAgent,
  updateAgent,
} from "@/api/agents"
import type { CollectionBulkDeleteResponse } from "@/api/collection"
import { getModels } from "@/api/models"
import { workflowAuthoringCapabilitiesQueryKey } from "@/api/workflows"
import { AgentActivityPanel } from "@/components/agent/agents/agent-activity-panel"
import { AgentCapabilitiesPanel } from "@/components/agent/agents/agent-capabilities-panel"
import {
  type AgentDraft,
  type AgentDraftErrors,
  agentDraftFromInfo,
  agentInputFromDraft,
  emptyAgentDraft,
  validateAgentDraft,
} from "@/components/agent/agents/agent-form"
import { AgentTokenListField } from "@/components/agent/agents/agent-token-list-field"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
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
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

/* eslint-disable react-refresh/only-export-components -- Route search helpers and their collection components share one pilot contract module. */

export const agentCollectionDefaultQuery = "ORDER BY position ASC"
export const agentCollectionSupportedViews = ["list", "table", "grid"] as const

const agentCollectionQueryKey = ["agents", "collection"] as const
const inheritAccountValue = "__picoclaw_inherit_account__"

export function normalizeAgentCollectionSearch(
  raw: object,
): CollectionRouteSearch {
  return normalizeCollectionRouteSearch(raw, {
    defaultQuery: agentCollectionDefaultQuery,
    supportedViews: agentCollectionSupportedViews,
  })
}

export function agentCollectionSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  return collectionRouteSearchIsCanonical(raw, normalized)
}

export interface AgentCollectionNavigation {
  onAdd: () => void
  onOpen: (agent: AgentInfo) => void
  onEdit: (agent: AgentInfo) => void
  onCapabilities: (agent: AgentInfo) => void
  onActivity: (agent: AgentInfo) => void
}

export function AgentCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: AgentCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const notifyMutation = useAgentMutationNotifier()
  const activeQuery = normalizeAgentCollectionSearch(search).q
  const query = useInfiniteQuery({
    queryKey: [...agentCollectionQueryKey, activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      getAgents({
        query: activeQuery,
        cursor: pageParam || undefined,
        limit: 50,
        signal,
      }),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = useMemo(
    () => [
      ...new Map(
        (query.data?.pages.flatMap((page) => page.agents) ?? []).map(
          (agent) => [agent.id, agent],
        ),
      ).values(),
    ],
    [query.data?.pages],
  )
  const first = query.data?.pages[0]

  const defaultMutation = useMutation({
    mutationFn: (agent: AgentInfo) => {
      if (!first?.config_revision) {
        throw new Error("Configuration revision is unavailable")
      }
      return setDefaultAgent(agent.id, first.config_revision)
    },
    onSuccess: async (response, agent) => {
      await Promise.all([
        query.refetch(),
        queryClient.invalidateQueries({
          queryKey: workflowAuthoringCapabilitiesQueryKey,
        }),
      ])
      await notifyMutation(
        response.effects,
        `${agent.name || agent.id} is now the default agent.`,
        agent.id,
      )
    },
  })
  const defaultMutationPending = defaultMutation.isPending
  const mutateDefault = defaultMutation.mutate

  const definition = useMemo<CollectionDefinition<AgentInfo>>(
    () => ({
      key: "agents",
      title: "Agents",
      defaultQuery: agentCollectionDefaultQuery,
      supportedViews: agentCollectionSupportedViews,
      defaultView: "list",
      getItemID: (agent) => agent.id,
      getItemLabel: (agent) => agent.name || agent.id,
      getItemIdentity: (agent) => ({
        title: agent.name || agent.id,
        description: agent.id,
        metadata: agent.model?.primary
          ? `Primary alias: ${agent.model.primary}`
          : "Inherits the default model policy",
      }),
      columns: [
        {
          id: "workspace",
          header: "Workspace",
          cell: (agent) => agent.workspace || "Inherited",
        },
        {
          id: "account",
          header: "Account",
          cell: (agent) => agent.account_ref || "Inherited",
        },
        {
          id: "model",
          header: "Primary alias",
          cell: (agent) => agent.model?.primary || "Inherited",
        },
      ],
      gridFacts: [
        {
          id: "workspace",
          label: "Workspace",
          value: (agent) => agent.workspace || "Inherited",
        },
        {
          id: "account",
          label: "Account",
          value: (agent) => agent.account_ref || "Inherited",
        },
        {
          id: "primary",
          label: "Primary alias",
          value: (agent) => agent.model?.primary || "Inherited",
        },
        {
          id: "fallbacks",
          label: "Fallbacks",
          value: (agent) => fallbackSummary(agent),
        },
      ],
      badges: [
        {
          id: "default",
          label: (agent) => (agent.is_default ? "Default" : null),
          variant: "secondary",
        },
        {
          id: "implicit",
          label: (agent) => (agent.implicit ? "Implicit" : null),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit agent",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
        {
          id: "capabilities",
          label: "Capabilities",
          icon: <IconAdjustments />,
          onSelect: navigation.onCapabilities,
        },
        {
          id: "activity",
          label: "Activity",
          icon: <IconActivity />,
          onSelect: navigation.onActivity,
        },
        {
          id: "default",
          label: "Set as default",
          icon: <IconStar />,
          hidden: (agent) => agent.is_default,
          disabled: () => defaultMutationPending,
          onSelect: (agent) => mutateDefault(agent),
        },
      ],
    }),
    [
      defaultMutationPending,
      mutateDefault,
      navigation.onActivity,
      navigation.onCapabilities,
      navigation.onEdit,
    ],
  )

  const bulkDelete = async (
    ids: string[],
  ): Promise<CollectionBulkDeleteResponse> => {
    if (!first?.config_revision) {
      throw new Error("Configuration revision is unavailable")
    }
    const response = await bulkDeleteAgents(ids, first.config_revision)
    const reconciliation: Promise<unknown>[] = [
      queryClient.invalidateQueries({
        queryKey: workflowAuthoringCapabilitiesQueryKey,
      }),
    ]
    if (response.deleted_ids.length > 0) {
      reconciliation.push(
        notifyMutation(
          response.effects,
          `Deleted ${response.deleted_ids.length} agent${response.deleted_ids.length === 1 ? "" : "s"}.`,
          t("navigation.agents", "Agents"),
        ),
      )
    }
    await Promise.all(reconciliation)
    return {
      deleted_ids: response.deleted_ids,
      failures: response.failures.map((failure) => ({
        id: failure.id,
        code: failure.code,
        blockers: failure.blockers?.map(formatAgentBlocker),
      })),
    }
  }

  return (
    <StandardCollectionPage
      definition={definition}
      search={search}
      onSearchChange={onSearchChange}
      items={items}
      total={first?.total}
      schema={first?.query_schema}
      canonicalQuery={first?.canonical_query}
      loading={query.isLoading}
      fetching={query.isFetching}
      error={defaultMutation.error ?? query.error}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={navigation.onOpen}
      addAction={
        <Button type="button" size="sm" onClick={navigation.onAdd}>
          <IconPlus /> Add agent
        </Button>
      }
      onBulkDelete={bulkDelete}
      afterBulkDelete={() => query.refetch()}
      emptyTitle="No agents match this query"
      emptyDescription="Create an agent or clear the active query."
    />
  )
}

interface AgentDetailNavigation {
  onBack: () => void
  onEdit: () => void
  onCapabilities: () => void
  onActivity: () => void
}

export function AgentCollectionDetailPage({
  agentID,
  onBack,
  onEdit,
  onCapabilities,
  onActivity,
}: { agentID: string } & AgentDetailNavigation) {
  const queryClient = useQueryClient()
  const notifyMutation = useAgentMutationNotifier()
  const query = useAgentDetailQuery(agentID)
  const agent = query.data?.agent
  const defaultMutation = useMutation({
    mutationFn: () => {
      if (!query.data) throw new Error("Agent details are unavailable")
      return setDefaultAgent(agentID, query.data.config_revision)
    },
    onSuccess: async (response) => {
      await Promise.all([
        query.refetch(),
        queryClient.invalidateQueries({ queryKey: agentCollectionQueryKey }),
        queryClient.invalidateQueries({
          queryKey: workflowAuthoringCapabilitiesQueryKey,
        }),
      ])
      await notifyMutation(
        response.effects,
        `${agent?.name || agentID} is now the default agent.`,
        agentID,
      )
    },
  })
  const error = query.error
  return (
    <CollectionDetailShell
      title={agent?.name || agentID}
      identity={
        <span className="text-muted-foreground max-w-48 truncate font-mono text-xs">
          {agentID}
        </span>
      }
      loading={query.isLoading}
      notFound={agentWasNotFound(error)}
      error={
        error && !agentWasNotFound(error) ? errorMessage(error) : undefined
      }
      onBack={onBack}
      onRetry={() => void query.refetch()}
      status={agent ? <AgentStatus agent={agent} /> : undefined}
      actions={
        agent ? (
          <>
            {!agent.is_default && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={defaultMutation.isPending}
                onClick={() => defaultMutation.mutate()}
              >
                {defaultMutation.isPending ? (
                  <IconLoader2 className="animate-spin" />
                ) : (
                  <IconStar />
                )}
                Set default
              </Button>
            )}
            <Button type="button" size="sm" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
          </>
        ) : undefined
      }
    >
      {agent && (
        <div className="space-y-4">
          {defaultMutation.error && (
            <div
              className="bg-destructive/10 text-destructive rounded-lg p-3 text-sm"
              role="alert"
            >
              {errorMessage(defaultMutation.error)}
            </div>
          )}
          <AgentOverview
            agent={agent}
            onCapabilities={onCapabilities}
            onActivity={onActivity}
          />
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function AgentCollectionCapabilitiesPage({
  agentID,
  onBack,
  onEdit,
}: {
  agentID: string
  onBack: () => void
  onEdit: () => void
}) {
  return (
    <AgentRelatedPage
      agentID={agentID}
      title="Agent capabilities"
      onBack={onBack}
      onEdit={onEdit}
    >
      <AgentCapabilitiesPanel agentID={agentID} />
    </AgentRelatedPage>
  )
}

export function AgentCollectionActivityPage({
  agentID,
  onBack,
  onEdit,
}: {
  agentID: string
  onBack: () => void
  onEdit: () => void
}) {
  return (
    <AgentRelatedPage
      agentID={agentID}
      title="Agent activity"
      onBack={onBack}
      onEdit={onEdit}
    >
      <AgentActivityPanel agentID={agentID} />
    </AgentRelatedPage>
  )
}

function AgentRelatedPage({
  agentID,
  title,
  onBack,
  onEdit,
  children,
}: {
  agentID: string
  title: string
  onBack: () => void
  onEdit: () => void
  children: ReactNode
}) {
  const query = useAgentDetailQuery(agentID)
  const error = query.error
  return (
    <CollectionDetailShell
      title={query.data?.agent.name || title}
      identity={
        <span className="text-muted-foreground max-w-48 truncate font-mono text-xs">
          {agentID}
        </span>
      }
      loading={query.isLoading}
      notFound={agentWasNotFound(error)}
      error={
        error && !agentWasNotFound(error) ? errorMessage(error) : undefined
      }
      onBack={onBack}
      backLabel="Back to agent"
      onRetry={() => void query.refetch()}
      status={query.data ? <AgentStatus agent={query.data.agent} /> : undefined}
      actions={
        query.data ? (
          <Button type="button" size="sm" variant="outline" onClick={onEdit}>
            <IconEdit /> Edit
          </Button>
        ) : undefined
      }
    >
      {query.data && (
        <div className="space-y-4">
          <h2 className="text-base font-semibold">{title}</h2>
          {children}
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function AgentCollectionEditorPage({
  mode,
  agentID,
  onBack,
  onSaved,
}: {
  mode: "create" | "edit"
  agentID?: string
  onBack: () => void
  onSaved: (agentID: string) => void
}) {
  const queryClient = useQueryClient()
  const notifyMutation = useAgentMutationNotifier()
  const agentsQuery = useQuery({
    queryKey: [...agentCollectionQueryKey, "editor-options"],
    queryFn: ({ signal }) =>
      getAgents({ query: agentCollectionDefaultQuery, limit: 200, signal }),
    retry: false,
  })
  const modelsQuery = useQuery({
    queryKey: ["models", "agent-editor-page"],
    queryFn: getModels,
    retry: false,
  })
  const agentQuery = useQuery({
    queryKey: ["agents", "detail", agentID],
    queryFn: () => getAgent(agentID!),
    enabled: mode === "edit" && Boolean(agentID),
    retry: false,
  })
  const [draft, setDraft] = useState<AgentDraft>(emptyAgentDraft)
  const [initialDraft, setInitialDraft] = useState<AgentDraft>(emptyAgentDraft)
  const [revision, setRevision] = useState("")
  const [errors, setErrors] = useState<AgentDraftErrors>({})
  const [serverError, setServerError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [conflictReloadAvailable, setConflictReloadAvailable] = useState(false)
  const [saving, setSaving] = useState(false)
  const [discardOpen, setDiscardOpen] = useState(false)
  const initializedRef = useRef("")
  const editorIdentityRef = useRef("")
  const allowNavigationRef = useRef(false)

  const configuredAgent = agentQuery.data?.agent
  const availableRevision =
    mode === "edit"
      ? agentQuery.data?.config_revision
      : agentsQuery.data?.config_revision
  useEffect(() => {
    const identity = `${mode}:${agentID ?? ""}`
    if (editorIdentityRef.current === identity) return
    editorIdentityRef.current = identity
    initializedRef.current = ""
    allowNavigationRef.current = false
    setDraft(emptyAgentDraft())
    setInitialDraft(emptyAgentDraft())
    setRevision("")
    setErrors({})
    setServerError("")
    setConflicted(false)
    setConflictReloadAvailable(false)
  }, [agentID, mode])
  useEffect(() => {
    if (!availableRevision) return
    const key =
      mode === "edit"
        ? `${agentID}:${availableRevision}`
        : `create:${availableRevision}`
    if (initializedRef.current) return
    const next =
      mode === "edit" && configuredAgent
        ? agentDraftFromInfo(configuredAgent)
        : emptyAgentDraft()
    initializedRef.current = key
    setDraft(next)
    setInitialDraft(next)
    setRevision(availableRevision)
  }, [agentID, availableRevision, configuredAgent, mode])

  const dirty = JSON.stringify(draft) !== JSON.stringify(initialDraft)
  const shouldBlockNavigation = useCallback(
    () => dirty && !allowNavigationRef.current,
    [dirty],
  )
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: shouldBlockNavigation,
    disabled: !dirty,
    withResolver: true,
  })
  useEffect(() => {
    if (navigationBlocker.status === "blocked") setDiscardOpen(true)
  }, [navigationBlocker.status])

  const agents = agentsQuery.data?.agents ?? []
  const models = modelsQuery.data?.models ?? []
  const modelAliases = modelsQuery.data?.model_aliases ?? []
  const modelAliasNames = modelAliases.map((alias) => alias.name)
  const modelRouterNames = models
    .filter(isModelRouter)
    .map((model) => model.model_name.trim())
    .filter(Boolean)
  const primaryModelNames = [
    ...new Set([...modelAliasNames, ...modelRouterNames]),
  ]
  const accountRefs = [
    ...new Set(
      models
        .filter((model) => model.enabled !== false && !isModelRouter(model))
        .map((model) => model.model_name.trim())
        .filter(Boolean),
    ),
  ].sort((left, right) => left.localeCompare(right))
  const peerIDs = agents
    .map((agent) => agent.id)
    .filter((id) => id !== draft.id)
  const retainedUnknownAgentIDs =
    mode === "edit"
      ? (configuredAgent?.subagents?.allow_agents.filter((id) => id !== "*") ??
        [])
      : []

  const update = <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => {
    setDraft((current) => ({ ...current, [key]: value }))
    setErrors((current) => ({ ...current, [errorField(key)]: undefined }))
    setServerError("")
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (saving || !revision) return
    const validation = validateAgentDraft(
      draft,
      agents.map((agent) => agent.id),
      mode === "edit" ? agentID : undefined,
      retainedUnknownAgentIDs,
    )
    setErrors(validation)
    if (Object.keys(validation).length > 0) return
    setSaving(true)
    setServerError("")
    try {
      const input = agentInputFromDraft(draft)
      const response =
        mode === "create"
          ? await createAgent(revision, input)
          : await updateAgent(agentID ?? input.id, revision, input)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: agentCollectionQueryKey }),
        queryClient.invalidateQueries({
          queryKey: workflowAuthoringCapabilitiesQueryKey,
        }),
      ])
      await notifyMutation(
        response.effects,
        `${mode === "create" ? "Created" : "Saved"} ${input.name || input.id}.`,
        input.id,
      )
      allowNavigationRef.current = true
      setInitialDraft(draft)
      onSaved(input.id)
    } catch (error) {
      if (
        error instanceof AgentsAPIError &&
        error.status === 409 &&
        error.code === "config_revision_mismatch"
      ) {
        setConflicted(true)
        setConflictReloadAvailable(false)
        setServerError(
          "Agent configuration changed after this editor opened. Your draft is preserved until you reload explicitly.",
        )
        const latestAgents = await agentsQuery.refetch()
        const latestAgent =
          mode === "edit" ? await agentQuery.refetch() : undefined
        setConflictReloadAvailable(
          latestAgents.data != null &&
            !latestAgents.isError &&
            (mode !== "edit" ||
              (latestAgent?.data != null && !latestAgent.isError)),
        )
      } else {
        setServerError(errorMessage(error))
      }
    } finally {
      setSaving(false)
    }
  }

  const reloadLatest = () => {
    const latestRevision =
      mode === "edit"
        ? agentQuery.data?.config_revision
        : agentsQuery.data?.config_revision
    if (!latestRevision) {
      setServerError("The latest configuration could not be loaded.")
      return
    }
    if (mode === "edit") {
      if (!agentQuery.data?.agent) {
        setServerError("This agent no longer exists.")
        return
      }
      const next = agentDraftFromInfo(agentQuery.data.agent)
      setDraft(next)
      setInitialDraft(next)
    }
    setRevision(latestRevision)
    setErrors({})
    setServerError("")
    setConflicted(false)
    setConflictReloadAvailable(false)
  }

  const loading =
    agentsQuery.isLoading ||
    modelsQuery.isLoading ||
    (mode === "edit" && agentQuery.isLoading)
  const loadError =
    agentsQuery.error ??
    modelsQuery.error ??
    (mode === "edit" ? agentQuery.error : null)
  const notFound = mode === "edit" && agentWasNotFound(agentQuery.error)
  const ready =
    !loading &&
    !loadError &&
    Boolean(revision) &&
    (mode === "create" || configuredAgent)

  return (
    <CollectionDetailShell
      title={
        mode === "create"
          ? "New agent"
          : configuredAgent?.name || agentID || "Edit agent"
      }
      identity={
        mode === "edit" ? (
          <span className="text-muted-foreground max-w-48 truncate font-mono text-xs">
            {agentID}
          </span>
        ) : undefined
      }
      loading={loading}
      notFound={notFound}
      error={loadError && !notFound ? errorMessage(loadError) : undefined}
      onBack={onBack}
      backLabel={mode === "create" ? "Back to agents" : "Back to agent"}
      onRetry={() => {
        void agentsQuery.refetch()
        void modelsQuery.refetch()
        if (mode === "edit") void agentQuery.refetch()
      }}
      status={
        configuredAgent ? <AgentStatus agent={configuredAgent} /> : undefined
      }
      contentClassName="max-w-3xl"
    >
      {ready && (
        <form className="space-y-6" onSubmit={(event) => void submit(event)}>
          <div className="border-border bg-muted/30 rounded-lg border p-3">
            <p className="text-sm font-medium">Configured policy</p>
            <p className="text-muted-foreground mt-1 text-xs">
              A workspace AGENT.md file may override configured identity, model,
              and skills at runtime.
            </p>
          </div>

          <AgentIdentityFields
            mode={mode}
            draft={draft}
            errors={errors}
            accountRefs={accountRefs}
            saving={saving}
            onUpdate={update}
          />
          <AgentModelFields
            draft={draft}
            errors={errors}
            modelAliasNames={modelAliasNames}
            primaryModelNames={primaryModelNames}
            saving={saving}
            onUpdate={update}
            onDraftChange={setDraft}
            onErrorsChange={setErrors}
            onClearServerError={() => setServerError("")}
          />
          <AgentSkillsFields
            draft={draft}
            errors={errors}
            saving={saving}
            onUpdate={update}
          />
          <AgentDelegationFields
            draft={draft}
            errors={errors}
            peerIDs={peerIDs}
            saving={saving}
            onUpdate={update}
          />

          {serverError && (
            <div
              className="bg-destructive/10 text-destructive rounded-lg p-3 text-sm"
              role="alert"
            >
              <p>{serverError}</p>
              {conflicted && conflictReloadAvailable && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="mt-3"
                  onClick={reloadLatest}
                >
                  Reload latest configuration
                </Button>
              )}
            </div>
          )}

          <div className="border-border bg-background sticky bottom-0 flex justify-end gap-2 border-t py-3">
            <Button
              type="button"
              variant="outline"
              disabled={saving}
              onClick={onBack}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={saving || conflicted}>
              {saving ? (
                <IconLoader2 className="animate-spin" />
              ) : (
                <IconDeviceFloppy />
              )}
              {saving ? "Saving…" : "Save agent"}
            </Button>
          </div>
        </form>
      )}

      <AlertDialog
        open={discardOpen}
        onOpenChange={(open) => {
          if (!open && navigationBlocker.status === "blocked") {
            navigationBlocker.reset()
          }
          setDiscardOpen(open)
        }}
      >
        <AlertDialogContent size="sm">
          <AlertDialogHeader>
            <AlertDialogTitle>Discard unsaved changes?</AlertDialogTitle>
            <AlertDialogDescription>
              Your changes to this configured policy will be lost.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                allowNavigationRef.current = true
                if (navigationBlocker.status === "blocked") {
                  navigationBlocker.proceed()
                }
                setDiscardOpen(false)
              }}
            >
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </CollectionDetailShell>
  )
}

function AgentIdentityFields({
  mode,
  draft,
  errors,
  accountRefs,
  saving,
  onUpdate,
}: {
  mode: "create" | "edit"
  draft: AgentDraft
  errors: AgentDraftErrors
  accountRefs: string[]
  saving: boolean
  onUpdate: <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => void
}) {
  return (
    <section
      className="border-border space-y-4 border-b pb-6"
      aria-labelledby="agent-editor-identity"
    >
      <h2 id="agent-editor-identity" className="text-sm font-semibold">
        Identity
      </h2>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="agent-page-id">Agent ID</Label>
          <Input
            id="agent-page-id"
            value={draft.id}
            disabled={mode === "edit" || saving}
            aria-invalid={Boolean(errors.id)}
            autoComplete="off"
            placeholder="reviewer"
            onChange={(event) => onUpdate("id", event.target.value)}
          />
          {errors.id ? (
            <p className="text-destructive text-xs">{errors.id}</p>
          ) : (
            <p className="text-muted-foreground text-xs">
              Lowercase letters, numbers, underscores, and hyphens.
            </p>
          )}
        </div>
        <div className="space-y-2">
          <Label htmlFor="agent-page-name">Configured name</Label>
          <Input
            id="agent-page-name"
            value={draft.name}
            disabled={saving}
            placeholder={draft.id || "Reviewer"}
            onChange={(event) => onUpdate("name", event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="agent-page-workspace">Workspace</Label>
          <Input
            id="agent-page-workspace"
            value={draft.workspace}
            disabled={saving}
            placeholder="Inherit the default workspace"
            onChange={(event) => onUpdate("workspace", event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="agent-page-account">Provider account</Label>
          <Select
            value={draft.accountRef || inheritAccountValue}
            disabled={saving}
            onValueChange={(value) =>
              onUpdate("accountRef", value === inheritAccountValue ? "" : value)
            }
          >
            <SelectTrigger id="agent-page-account" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={inheritAccountValue}>
                Inherit default account
              </SelectItem>
              {draft.accountRef && !accountRefs.includes(draft.accountRef) && (
                <SelectItem value={draft.accountRef} disabled>
                  {draft.accountRef} (not configured)
                </SelectItem>
              )}
              {accountRefs.map((accountRef) => (
                <SelectItem key={accountRef} value={accountRef}>
                  {accountRef}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
    </section>
  )
}

function AgentModelFields({
  draft,
  errors,
  modelAliasNames,
  primaryModelNames,
  saving,
  onUpdate,
  onDraftChange,
  onErrorsChange,
  onClearServerError,
}: {
  draft: AgentDraft
  errors: AgentDraftErrors
  modelAliasNames: string[]
  primaryModelNames: string[]
  saving: boolean
  onUpdate: <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => void
  onDraftChange: React.Dispatch<React.SetStateAction<AgentDraft>>
  onErrorsChange: React.Dispatch<React.SetStateAction<AgentDraftErrors>>
  onClearServerError: () => void
}) {
  return (
    <section
      className="border-border space-y-4 border-b pb-6"
      aria-labelledby="agent-editor-model"
    >
      <h2 id="agent-editor-model" className="text-sm font-semibold">
        Model policy
      </h2>
      <div className="grid gap-4 sm:grid-cols-2">
        <PolicyModeSelect
          id="agent-page-primary-mode"
          label="Primary model alias"
          value={draft.primaryMode}
          values={["inherit", "custom"]}
          disabled={saving}
          onChange={(value) => {
            onDraftChange((current) => ({
              ...current,
              modelConfigured: true,
              primaryMode: value as AgentDraft["primaryMode"],
            }))
            onErrorsChange((current) => ({ ...current, primary: undefined }))
            onClearServerError()
          }}
        />
        <PolicyModeSelect
          id="agent-page-fallback-mode"
          label="Fallback model aliases"
          value={draft.fallbackMode}
          values={["inherit", "none", "custom"]}
          disabled={saving}
          onChange={(value) => {
            onDraftChange((current) => ({
              ...current,
              modelConfigured: true,
              fallbackMode: value as AgentDraft["fallbackMode"],
            }))
            onErrorsChange((current) => ({ ...current, fallbacks: undefined }))
            onClearServerError()
          }}
        />
      </div>
      {draft.primaryMode === "custom" && (
        <div className="space-y-2">
          <Label htmlFor="agent-page-primary">Primary model alias</Label>
          <Select
            value={draft.primary}
            disabled={saving}
            onValueChange={(value) => onUpdate("primary", value)}
          >
            <SelectTrigger
              id="agent-page-primary"
              className="w-full"
              aria-invalid={Boolean(errors.primary)}
            >
              <SelectValue placeholder="Select model alias" />
            </SelectTrigger>
            <SelectContent>
              {draft.primary && !primaryModelNames.includes(draft.primary) && (
                <SelectItem value={draft.primary} disabled>
                  {draft.primary} (not configured)
                </SelectItem>
              )}
              {primaryModelNames.map((modelName) => (
                <SelectItem key={modelName} value={modelName}>
                  {modelName}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {errors.primary && (
            <p className="text-destructive text-xs">{errors.primary}</p>
          )}
        </div>
      )}
      {draft.fallbackMode === "custom" && (
        <AgentTokenListField
          label="Fallback order"
          description="Model aliases are tried from top to bottom."
          values={draft.fallbacks}
          input={draft.fallbackInput}
          suggestions={modelAliasNames}
          restrictToSuggestions
          disabled={saving}
          error={errors.fallbacks}
          placeholder="Select model alias"
          onChange={(values) => onUpdate("fallbacks", values)}
          onInputChange={(value) => onUpdate("fallbackInput", value)}
        />
      )}
      {draft.modelConfigured && (
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={saving}
          onClick={() => {
            onDraftChange((current) => ({
              ...current,
              modelConfigured: false,
              primaryMode: "inherit",
              primary: "",
              fallbackMode: "inherit",
              fallbacks: [],
              fallbackInput: "",
            }))
            onErrorsChange((current) => ({
              ...current,
              primary: undefined,
              fallbacks: undefined,
            }))
            onClearServerError()
          }}
        >
          Reset alias policy to inherited defaults
        </Button>
      )}
    </section>
  )
}

function AgentSkillsFields({
  draft,
  errors,
  saving,
  onUpdate,
}: {
  draft: AgentDraft
  errors: AgentDraftErrors
  saving: boolean
  onUpdate: <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => void
}) {
  return (
    <section
      className="border-border space-y-4 border-b pb-6"
      aria-labelledby="agent-editor-skills"
    >
      <h2 id="agent-editor-skills" className="text-sm font-semibold">
        Skills policy
      </h2>
      <PolicyModeSelect
        id="agent-page-skills-mode"
        label="Available skills"
        value={draft.skillsMode}
        values={["all", "selected"]}
        disabled={saving}
        onChange={(value) =>
          onUpdate("skillsMode", value as AgentDraft["skillsMode"])
        }
      />
      {draft.skillsMode === "selected" && (
        <AgentTokenListField
          label="Selected skills"
          values={draft.skills}
          input={draft.skillsInput}
          disabled={saving}
          error={errors.skills}
          placeholder="review-helper"
          onChange={(values) => onUpdate("skills", values)}
          onInputChange={(value) => onUpdate("skillsInput", value)}
        />
      )}
    </section>
  )
}

function AgentDelegationFields({
  draft,
  errors,
  peerIDs,
  saving,
  onUpdate,
}: {
  draft: AgentDraft
  errors: AgentDraftErrors
  peerIDs: string[]
  saving: boolean
  onUpdate: <Key extends keyof AgentDraft>(
    key: Key,
    value: AgentDraft[Key],
  ) => void
}) {
  return (
    <section className="space-y-4" aria-labelledby="agent-editor-delegation">
      <h2 id="agent-editor-delegation" className="text-sm font-semibold">
        Delegation policy
      </h2>
      <PolicyModeSelect
        id="agent-page-delegation-mode"
        label="May delegate to"
        value={draft.delegationMode}
        values={["none", "all", "selected"]}
        disabled={saving}
        onChange={(value) =>
          onUpdate("delegationMode", value as AgentDraft["delegationMode"])
        }
      />
      {draft.delegationMode === "selected" && (
        <AgentTokenListField
          label="Selected agents"
          description="Unknown configured IDs are retained until you remove them."
          values={draft.delegateAgentIDs}
          input={draft.delegateAgentInput}
          suggestions={peerIDs}
          disabled={saving}
          error={errors.delegation}
          placeholder="main"
          onChange={(values) => onUpdate("delegateAgentIDs", values)}
          onInputChange={(value) => onUpdate("delegateAgentInput", value)}
        />
      )}
    </section>
  )
}

function PolicyModeSelect({
  id,
  label,
  value,
  values,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: string
  values: string[]
  disabled: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value} disabled={disabled} onValueChange={onChange}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {values.map((candidate) => (
            <SelectItem key={candidate} value={candidate}>
              {policyLabel(candidate)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

function AgentOverview({
  agent,
  onCapabilities,
  onActivity,
}: {
  agent: AgentInfo
  onCapabilities: () => void
  onActivity: () => void
}) {
  const facts = [
    ["Workspace", agent.workspace || "Inherited"],
    ["Provider account", agent.account_ref || "Inherited"],
    ["Primary model alias", agent.model?.primary || "Inherited"],
    ["Fallback model aliases", fallbackSummary(agent)],
    [
      "Configured skills",
      agent.skills == null || agent.skills.length === 0
        ? "All skills"
        : agent.skills.join(", "),
    ],
    ["Delegation", delegationSummary(agent)],
  ]
  return (
    <div className="space-y-6">
      <section aria-labelledby="agent-policy-heading">
        <h2 id="agent-policy-heading" className="text-base font-semibold">
          Configured policy
        </h2>
        <dl className="border-border mt-3 divide-y rounded-lg border">
          {facts.map(([label, value]) => (
            <div
              key={label}
              className="grid gap-1 px-4 py-3 sm:grid-cols-[12rem_minmax(0,1fr)]"
            >
              <dt className="text-muted-foreground text-sm">{label}</dt>
              <dd className="min-w-0 text-sm break-words sm:text-right">
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </section>
      <section
        className="border-border flex flex-wrap gap-2 border-t pt-5"
        aria-label="Related agent sections"
      >
        <Button type="button" variant="outline" onClick={onCapabilities}>
          <IconAdjustments /> Capabilities
        </Button>
        <Button type="button" variant="outline" onClick={onActivity}>
          <IconActivity /> Activity
        </Button>
      </section>
    </div>
  )
}

function AgentStatus({ agent }: { agent: AgentInfo }) {
  return (
    <div className="flex flex-wrap gap-1">
      {agent.is_default && (
        <Badge variant="secondary">
          <IconStar /> Default
        </Badge>
      )}
      {agent.implicit && <Badge variant="outline">Implicit</Badge>}
      {!agent.is_default && !agent.implicit && (
        <Badge variant="outline">Configured</Badge>
      )}
    </div>
  )
}

function useAgentDetailQuery(agentID: string) {
  return useQuery({
    queryKey: ["agents", "detail", agentID],
    queryFn: () => getAgent(agentID),
    retry: false,
  })
}

function useAgentMutationNotifier() {
  const { t } = useTranslation()
  return useCallback(
    async (effects: AgentMutationEffects, message: string, name: string) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        message,
        name,
        effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
    },
    [t],
  )
}

function fallbackSummary(agent: AgentInfo): string {
  if (agent.model?.fallbacks == null) return "Inherited"
  if (agent.model.fallbacks.length === 0) return "None"
  return agent.model.fallbacks.join(" → ")
}

function delegationSummary(agent: AgentInfo): string {
  const targets = agent.subagents?.allow_agents ?? []
  if (targets.length === 0) return "No delegation"
  if (targets.length === 1 && targets[0] === "*") return "All peers"
  return targets.join(", ")
}

function formatAgentBlocker(blocker: AgentDeleteBlocker): string {
  const kind = blocker.kind.replaceAll("_", " ")
  const detail = blocker.name ?? blocker.agent_id
  return detail ? `${kind}: ${detail}` : kind
}

function policyLabel(value: string): string {
  switch (value) {
    case "inherit":
      return "Inherit"
    case "custom":
      return "Custom"
    case "none":
      return "None"
    case "all":
      return "All"
    case "selected":
      return "Selected"
    default:
      return value
  }
}

function errorField(key: keyof AgentDraft): keyof AgentDraftErrors {
  switch (key) {
    case "id":
      return "id"
    case "primary":
    case "primaryMode":
    case "modelConfigured":
      return "primary"
    case "fallbackMode":
    case "fallbacks":
    case "fallbackInput":
      return "fallbacks"
    case "skillsMode":
    case "skills":
    case "skillsInput":
      return "skills"
    case "delegationMode":
    case "delegateAgentIDs":
    case "delegateAgentInput":
      return "delegation"
    default:
      return "id"
  }
}

function agentWasNotFound(error: unknown): boolean {
  return error instanceof AgentsAPIError && error.status === 404
}

function errorMessage(error: unknown): string {
  if (!(error instanceof Error)) return "Agent request failed."
  if (!/^[a-z0-9_]+$/.test(error.message)) return error.message
  const words = error.message.replaceAll("_", " ")
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}.`
}

function isModelRouter(
  model: Awaited<ReturnType<typeof getModels>>["models"][number],
): boolean {
  return model.provider === "model-router" || model.model_router != null
}
