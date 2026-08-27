import { IconAdjustments, IconEdit, IconPower } from "@tabler/icons-react"
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { CollectionAPIError } from "@/api/collection"
import {
  type ThreadPolicyConfig,
  type ToolAdaptationConfig,
  type ToolAdaptationProbeTarget,
  type ToolSupportItem,
  type WebSearchConfigResponse,
  getThreadPolicy,
  getTool,
  getToolAdaptation,
  getWebSearchConfig,
  listTools,
  runToolAdaptationProbe,
  setToolEnabled,
  updateThreadPolicy,
  updateToolAdaptation,
  updateWebSearchConfig,
} from "@/api/tools"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import type { CollectionRouteSearch } from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import {
  administrativeCollectionViews,
  normalizeToolsCollectionSearch,
  toolsDefaultQuery,
} from "../skill-tool-collection-route-state"
import { ThreadPolicyTab } from "./thread-policy-tab"
import { ToolAdaptationTab } from "./tool-adaptation-tab"
import { ToolStatusBadge } from "./tool-status-badge"
import { WebSearchTab } from "./web-search-tab"

interface ToolsCollectionNavigation {
  onOpen: (tool: ToolSupportItem) => void
  onEdit: (tool: ToolSupportItem) => void
  onAdaptation: () => void
}

export function ToolsCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: ToolsCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const activeQuery = normalizeToolsCollectionSearch(search).q
  const query = useInfiniteQuery({
    queryKey: ["tools", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listTools(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = useMemo(
    () => [
      ...new Map(
        (query.data?.pages.flatMap((page) => page.tools) ?? []).map((tool) => [
          tool.id,
          tool,
        ]),
      ).values(),
    ],
    [query.data?.pages],
  )
  const first = query.data?.pages[0]
  const toggle = useToolToggle()
  const toggleTool = toggle.mutate
  const togglePending = toggle.isPending
  const definition = useMemo<CollectionDefinition<ToolSupportItem>>(
    () => ({
      key: "tools",
      title: t("navigation.tools", "Tools"),
      defaultQuery: toolsDefaultQuery,
      supportedViews: administrativeCollectionViews,
      defaultView: "list",
      getItemID: (tool) => tool.id,
      getItemLabel: (tool) => tool.name,
      getItemIdentity: (tool) => ({
        title: tool.name,
        description: tool.description,
        metadata: `Category: ${formatCategory(tool.category)}`,
      }),
      columns: [
        {
          id: "category",
          header: "Category",
          cell: (tool) => formatCategory(tool.category),
        },
        {
          id: "status",
          header: "Status",
          cell: (tool) => formatToolStatus(tool.status),
        },
        {
          id: "reason",
          header: "Reason",
          cell: (tool) => toolReason(tool, t),
        },
        {
          id: "config-key",
          header: "Configuration",
          cell: (tool) => tool.config_key || "—",
        },
      ],
      gridFacts: [
        {
          id: "category",
          label: "Category",
          value: (tool) => formatCategory(tool.category),
        },
        {
          id: "status",
          label: "Status",
          value: (tool) => formatToolStatus(tool.status),
        },
        { id: "reason", label: "Reason", value: (tool) => toolReason(tool, t) },
        {
          id: "config-key",
          label: "Configuration",
          value: (tool) => tool.config_key || "—",
        },
      ],
      badges: [
        {
          id: "status",
          label: (tool) => formatToolStatus(tool.status),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Configure tool",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
        {
          id: "toggle",
          label: (tool) =>
            tool.status === "disabled" ? "Enable tool" : "Disable tool",
          icon: <IconPower />,
          disabled: () => togglePending,
          onSelect: (tool) =>
            toggleTool({
              tool,
              enabled: tool.status === "disabled",
            }),
        },
      ],
    }),
    [navigation.onEdit, t, togglePending, toggleTool],
  )

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
      error={query.error}
      onRefresh={query.refetch}
      hasNextPage={query.hasNextPage}
      loadingMore={query.isFetchingNextPage}
      onLoadMore={query.fetchNextPage}
      onOpenItem={navigation.onOpen}
      addAction={
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={navigation.onAdaptation}
        >
          <IconAdjustments /> Adaptation settings
        </Button>
      }
      emptyTitle="No tools available"
      emptyDescription="Tools appear here when the launcher exposes them to agents."
    />
  )
}

export function ToolDetailPage({
  toolID,
  onBack,
  onEdit,
}: {
  toolID: string
  onBack: () => void
  onEdit: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ["tools", "detail", toolID],
    queryFn: ({ signal }) => getTool(toolID, signal),
    retry: false,
  })
  const toggle = useToolToggle()
  const tool = query.data?.tool
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <CollectionDetailShell
      title={tool?.name || "Tool details"}
      identity={
        tool ? (
          <span className="text-muted-foreground hidden truncate font-mono text-xs sm:inline">
            {tool.id}
          </span>
        ) : undefined
      }
      status={tool ? <ToolStatusBadge status={tool.status} /> : undefined}
      actions={
        tool ? (
          <>
            <Button
              type="button"
              variant="outline"
              aria-label={
                tool.status === "disabled" ? "Enable tool" : "Disable tool"
              }
              title={
                tool.status === "disabled" ? "Enable tool" : "Disable tool"
              }
              disabled={toggle.isPending}
              onClick={() =>
                toggle.mutate({
                  tool,
                  enabled: tool.status === "disabled",
                })
              }
            >
              <IconPower />
              <span className="hidden sm:inline">
                {tool.status === "disabled" ? "Enable" : "Disable"}
              </span>
            </Button>
            <Button
              type="button"
              aria-label="Configure"
              title="Configure"
              onClick={onEdit}
            >
              <IconEdit /> <span className="hidden sm:inline">Configure</span>
            </Button>
          </>
        ) : undefined
      }
      loading={query.isLoading}
      error={notFound ? undefined : query.error?.message}
      notFound={notFound}
      onRetry={() => void query.refetch()}
      onBack={onBack}
    >
      {tool && (
        <div className="grid gap-4 md:grid-cols-2">
          <ToolMetadata label="Description" value={tool.description || "—"} />
          <ToolMetadata
            label="Category"
            value={formatCategory(tool.category)}
          />
          <ToolMetadata label="Status" value={formatToolStatus(tool.status)} />
          <ToolMetadata label="Reason" value={toolReason(tool, t)} />
          <ToolMetadata
            label="Configuration key"
            value={tool.config_key || "—"}
            mono
          />
          <ToolMetadata label="Stable ID" value={tool.id} mono />
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function ToolEditorPage({
  toolID,
  onBack,
}: {
  toolID: string
  onBack: () => void
}) {
  const query = useQuery({
    queryKey: ["tools", "detail", toolID],
    queryFn: ({ signal }) => getTool(toolID, signal),
    retry: false,
  })
  const tool = query.data?.tool
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404

  return (
    <CollectionDetailShell
      title={tool ? `Configure ${tool.name}` : "Configure tool"}
      identity={
        tool ? (
          <span className="text-muted-foreground hidden font-mono text-xs sm:inline">
            {tool.config_key || tool.id}
          </span>
        ) : undefined
      }
      status={tool ? <ToolStatusBadge status={tool.status} /> : undefined}
      loading={query.isLoading}
      error={notFound ? undefined : query.error?.message}
      notFound={notFound}
      onRetry={() => void query.refetch()}
      onBack={onBack}
    >
      {tool && <ToolEditorContent key={tool.id} tool={tool} />}
    </CollectionDetailShell>
  )
}

function ToolEditorContent({ tool }: { tool: ToolSupportItem }) {
  if (isWebSearchTool(tool)) return <WebSearchToolEditor />
  if (isThreadsTool(tool)) return <ThreadPolicyToolEditor />
  return <GenericToolEditor tool={tool} />
}

function GenericToolEditor({ tool }: { tool: ToolSupportItem }) {
  const [enabled, setEnabled] = useState(tool.status !== "disabled")
  const toggle = useToolToggle()
  const dirty = enabled !== (tool.status !== "disabled")
  return (
    <Card className="mx-auto max-w-2xl">
      <CardHeader>
        <CardTitle>Availability</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <p className="text-muted-foreground text-sm">{tool.description}</p>
        <div className="border-border flex items-center justify-between gap-4 rounded-lg border p-4">
          <div>
            <Label htmlFor="tool-enabled">Enabled</Label>
            <p className="text-muted-foreground mt-1 text-sm">
              Make this tool available to compatible agent sessions.
            </p>
          </div>
          <Switch
            id="tool-enabled"
            checked={enabled}
            disabled={toggle.isPending}
            onCheckedChange={setEnabled}
          />
        </div>
        <div className="flex justify-end">
          <Button
            type="button"
            disabled={!dirty || toggle.isPending}
            onClick={() => toggle.mutate({ tool, enabled })}
          >
            {toggle.isPending ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function WebSearchToolEditor() {
  const { t } = useTranslation()
  const [override, setOverride] = useState<WebSearchConfigResponse | null>(null)
  const [expandedProvider, setExpandedProvider] = useState<string | null>(null)
  const query = useQuery({
    queryKey: ["tools", "web-search-config"],
    queryFn: getWebSearchConfig,
  })
  const draft = override ?? query.data ?? null
  const dirty = Boolean(
    draft && query.data && JSON.stringify(draft) !== JSON.stringify(query.data),
  )
  const mutation = useMutation({
    mutationFn: updateWebSearchConfig,
    onSuccess: async () => {
      setOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.agent.tools.web_search.save_success"),
        t("pages.agent.tools.web_search.title"),
        gateway?.restartRequired === true,
      )
      query.refetch()
    },
    onError: showToolMutationError,
  })
  const labels = useMemo(
    () =>
      new Map((draft?.providers ?? []).map((item) => [item.id, item.label])),
    [draft?.providers],
  )

  return (
    <WebSearchTab
      showHeader={false}
      draft={draft}
      providerLabelMap={labels}
      expandedProvider={expandedProvider}
      isLoading={query.isLoading}
      hasError={query.isError}
      isSaving={mutation.isPending}
      isDirty={dirty}
      onSave={() => {
        if (draft) mutation.mutate(draft)
      }}
      onToggleProviderExpand={(id) =>
        setExpandedProvider((current) => (current === id ? null : id))
      }
      onUpdateDraft={(update) =>
        setOverride((current) => {
          const value = current ?? query.data
          return value ? update(value) : current
        })
      }
    />
  )
}

function ThreadPolicyToolEditor() {
  const { t } = useTranslation()
  const [override, setOverride] = useState<ThreadPolicyConfig | null>(null)
  const query = useQuery({
    queryKey: ["tools", "thread-policy"],
    queryFn: getThreadPolicy,
  })
  const draft = override ?? query.data ?? null
  const dirty = Boolean(
    draft && query.data && JSON.stringify(draft) !== JSON.stringify(query.data),
  )
  const mutation = useMutation({
    mutationFn: updateThreadPolicy,
    onSuccess: async () => {
      setOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.agent.tools.thread_policy.save_success"),
        t("pages.agent.tools.thread_policy.title"),
        gateway?.restartRequired === true,
      )
      query.refetch()
    },
    onError: showToolMutationError,
  })

  return (
    <ThreadPolicyTab
      showHeader={false}
      draft={draft}
      isLoading={query.isLoading}
      hasError={query.isError}
      isSaving={mutation.isPending}
      isDirty={dirty}
      onSave={() => {
        if (draft) mutation.mutate(draft)
      }}
      onUpdateDraft={(update) =>
        setOverride((current) => {
          const value = current ?? query.data
          return value ? update(value) : current
        })
      }
    />
  )
}

export function ToolAdaptationSettingsPage({ onBack }: { onBack: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [override, setOverride] = useState<ToolAdaptationConfig | null>(null)
  const query = useQuery({
    queryKey: ["tools", "adaptation"],
    queryFn: getToolAdaptation,
  })
  const draft = override ?? query.data ?? null
  const dirty = Boolean(
    draft && query.data && JSON.stringify(draft) !== JSON.stringify(query.data),
  )
  const save = useMutation({
    mutationFn: updateToolAdaptation,
    onSuccess: async (updated) => {
      queryClient.setQueryData(["tools", "adaptation"], updated)
      setOverride(null)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.agent.tools.adaptation.save_success"),
        t("pages.agent.tools.adaptation.title"),
        gateway?.restartRequired === true,
      )
    },
    onError: showToolMutationError,
  })
  const probe = useMutation({
    mutationFn: runToolAdaptationProbe,
    onSuccess: (result) => {
      toast.success(t("pages.agent.tools.adaptation.probe_success"), {
        description: `${result.tool_name} on ${result.visible_tool_surface}`,
      })
      query.refetch()
    },
    onError: (error) => {
      showToolMutationError(error)
      query.refetch()
    },
  })

  return (
    <CollectionDetailShell title="Tool adaptation" onBack={onBack}>
      <ToolAdaptationTab
        showHeader={false}
        draft={draft}
        isLoading={query.isLoading}
        hasError={query.isError}
        isSaving={save.isPending}
        isProbing={probe.isPending}
        probingProfile={probe.variables ?? null}
        isDirty={dirty}
        onSave={() => {
          if (draft) save.mutate(draft)
        }}
        onRunProbe={(profile: ToolAdaptationProbeTarget) =>
          probe.mutate(profile)
        }
        onUpdateDraft={(update) =>
          setOverride((current) => {
            const value = current ?? query.data
            return value ? update(value) : current
          })
        }
      />
    </CollectionDetailShell>
  )
}

function useToolToggle() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({
      tool,
      enabled,
    }: {
      tool: ToolSupportItem
      enabled: boolean
    }) => setToolEnabled(tool.id, enabled),
    onSuccess: async (_, variables) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${variables.tool.name} was ${variables.enabled ? "enabled" : "disabled"}.`,
        variables.tool.name,
        gateway?.restartRequired === true,
      )
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["tools"] }),
        queryClient.invalidateQueries({ queryKey: ["workflows", "settings"] }),
        queryClient.invalidateQueries({
          queryKey: ["workflows", "dependencies"],
        }),
      ])
    },
    onError: showToolMutationError,
  })
}

function showToolMutationError(error: unknown) {
  toast.error(error instanceof Error ? error.message : "Tool update failed.")
}

function isWebSearchTool(tool: ToolSupportItem): boolean {
  return (
    tool.name === "web_search" ||
    tool.config_key.toLowerCase().includes("web_search")
  )
}

function isThreadsTool(tool: ToolSupportItem): boolean {
  return (
    tool.name === "threads" || tool.config_key.toLowerCase().includes("thread")
  )
}

function formatToolStatus(status: ToolSupportItem["status"]): string {
  return status.charAt(0).toUpperCase() + status.slice(1)
}

function formatCategory(category: string): string {
  return category
    .split(/[-_]/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ")
}

function toolReason(
  tool: ToolSupportItem,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  const reason = tool.reason || tool.reason_code
  if (!reason) return "—"
  if (!/^[a-z0-9_.-]{1,64}$/.test(reason)) return reason.slice(0, 256)
  return t(`pages.agent.tools.reasons.${reason}`, {
    defaultValue: "Blocked by an unmet dependency.",
  })
}

function ToolMetadata({
  label,
  value,
  mono = false,
}: {
  label: string
  value: string
  mono?: boolean
}) {
  return (
    <div className="border-border bg-card rounded-lg border p-4">
      <div className="text-muted-foreground text-xs font-semibold tracking-wide uppercase">
        {label}
      </div>
      <div className={mono ? "mt-2 font-mono text-sm" : "mt-2 text-sm"}>
        {value}
      </div>
    </div>
  )
}
