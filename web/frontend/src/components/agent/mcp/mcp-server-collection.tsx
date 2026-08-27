import { IconEdit, IconPlus, IconSettings } from "@tabler/icons-react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { CollectionAPIError } from "@/api/collection"
import {
  type MCPConfigResponse,
  type MCPServer,
  type MCPServerCollectionSummary,
  type MCPServerInput,
  addMCPServer,
  bulkDeleteMCPServers,
  deleteMCPServerCredential,
  getMCPConfig,
  getMCPServer,
  listMCPServers,
  setMCPServerCredential,
  startMCPServerOAuth,
  updateMCPServer,
  updateMCPSettings,
} from "@/api/mcp"
import { serverInputFromServer } from "@/components/agent/mcp/mcp-server-form"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import { Field } from "@/components/shared-form"
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
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

const defaultQuery = "ORDER BY name ASC"
const supportedViews = ["list", "table", "grid"] as const

export function MCPServersCollectionPage({
  search,
  onSearchChange,
  onAdd,
  onOpen,
  onEdit,
  onSettings,
}: {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  onAdd: () => void
  onOpen: (server: MCPServerCollectionSummary) => void
  onEdit: (server: MCPServerCollectionSummary) => void
  onSettings: () => void
}) {
  const { t } = useTranslation()
  const activeQuery = normalizeCollectionRouteSearch(
    { ...search },
    { defaultQuery, supportedViews },
  ).q
  const query = useInfiniteQuery({
    queryKey: ["mcp-servers", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listMCPServers(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = query.data?.pages.flatMap((page) => page.servers) ?? []
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<MCPServerCollectionSummary>>(
    () => ({
      key: "mcp-servers",
      title: "MCP servers",
      defaultQuery,
      supportedViews,
      defaultView: "list",
      getItemID: (server) => server.name,
      getItemLabel: (server) => server.name,
      getItemIdentity: (server) => ({
        title: server.name,
        description: server.address,
        metadata: discoveryLabel(server.deferred),
      }),
      columns: [
        { id: "transport", header: "Transport", cell: (server) => server.type },
        {
          id: "endpoint",
          header: "Endpoint",
          cell: (server) => server.address || "—",
          className: "max-w-80 truncate font-mono text-xs",
        },
        {
          id: "auth",
          header: "Authentication",
          cell: (server) => server.auth.type,
        },
      ],
      gridFacts: [
        { id: "transport", label: "Transport", value: (server) => server.type },
        {
          id: "auth",
          label: "Authentication",
          value: (server) => server.auth.type,
        },
        {
          id: "environment",
          label: "Environment keys",
          value: (server) => server.environment_key_count,
        },
        {
          id: "headers",
          label: "Header keys",
          value: (server) => server.header_key_count,
        },
      ],
      badges: [
        {
          id: "enabled",
          label: (server) => (server.enabled ? "Enabled" : "Disabled"),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit server",
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
      onOpenItem={onOpen}
      addAction={
        <>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onSettings}
          >
            <IconSettings /> Settings
          </Button>
          <Button type="button" size="sm" onClick={onAdd}>
            <IconPlus /> Add server
          </Button>
        </>
      }
      onBulkDelete={async (ids) => {
        if (!first?.config_revision)
          throw new Error("Configuration revision is unavailable")
        const response = await bulkDeleteMCPServers(ids, first.config_revision)
        if (response.deleted_ids.length > 0) {
          const gateway = await refreshGatewayState({ force: true })
          showSaveSuccessOrRestartToast(
            t,
            `Deleted ${response.deleted_ids.length} MCP server${response.deleted_ids.length === 1 ? "" : "s"}.`,
            "MCP servers",
            response.effects.gateway_effect === "restart_required" ||
              gateway?.restartRequired === true,
          )
        }
        showMCPCleanupFailureWarning(response.cleanup_failures)
        return response
      }}
      afterBulkDelete={() => query.refetch()}
      emptyTitle="No MCP servers"
      emptyDescription="Add a local or remote server. Global integration settings live on their own route."
    />
  )
}

export function MCPServerDetailPage({
  name,
  onBack,
  onEdit,
}: {
  name: string
  onBack: () => void
  onEdit: () => void
}) {
  const query = useQuery({
    queryKey: ["mcp-server", name],
    queryFn: ({ signal }) => getMCPServer(name, signal),
    retry: false,
  })
  const server = query.data?.server
  const notFound =
    query.error instanceof CollectionAPIError && query.error.status === 404
  return (
    <CollectionDetailShell
      title={server?.name ?? "MCP server"}
      identity={<span className="font-mono text-xs">{name}</span>}
      status={
        server ? (
          <>
            <Badge variant="outline">{server.type}</Badge>
            <Badge variant={server.enabled ? "secondary" : "outline"}>
              {server.enabled ? "Enabled" : "Disabled"}
            </Badge>
          </>
        ) : undefined
      }
      actions={
        server ? (
          <Button type="button" size="sm" onClick={onEdit}>
            <IconEdit /> Edit
          </Button>
        ) : undefined
      }
      loading={query.isLoading}
      error={!notFound ? query.error?.message : undefined}
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All MCP servers"
    >
      {server && (
        <div className="space-y-6">
          <DetailRows
            rows={[
              ["Transport", server.type],
              [
                "Endpoint",
                server.type === "stdio" ? server.command : server.url,
              ],
              ["Arguments", server.args.join(" ") || "—"],
              ["Environment file", server.env_file || "—"],
              ["Authentication", server.auth.type],
              [
                "Credential status",
                server.auth.configured ? "Configured" : "Not configured",
              ],
              ["Discovery", discoveryLabel(server.deferred)],
            ]}
          />
          <section>
            <h2 className="mb-2 text-sm font-semibold">Exposed secret keys</h2>
            <div className="flex flex-wrap gap-2">
              {[...server.env_keys, ...server.header_keys].map((key) => (
                <Badge key={key} variant="outline" className="font-mono">
                  {key}
                </Badge>
              ))}
              {server.env_keys.length + server.header_keys.length === 0 && (
                <span className="text-muted-foreground text-sm">
                  No secret keys configured.
                </span>
              )}
            </div>
          </section>
        </div>
      )}
    </CollectionDetailShell>
  )
}

export function MCPServerEditorPage({
  name,
  onBack,
  onSaved,
}: {
  name?: string
  onBack: () => void
  onSaved: (name: string) => void
}) {
  const detail = useQuery({
    queryKey: ["mcp-server", name],
    queryFn: ({ signal }) => getMCPServer(name ?? "", signal),
    enabled: Boolean(name),
    retry: false,
  })
  const context = useQuery({
    queryKey: ["mcp-server-editor-context"],
    queryFn: ({ signal }) => listMCPServers({ limit: 1 }, signal),
    retry: false,
  })
  const notFound =
    detail.error instanceof CollectionAPIError && detail.error.status === 404
  return (
    <CollectionDetailShell
      title={name ? "Edit MCP server" : "New MCP server"}
      identity={
        name ? <span className="font-mono text-xs">{name}</span> : undefined
      }
      loading={(Boolean(name) && detail.isLoading) || context.isLoading}
      error={
        !notFound
          ? (detail.error?.message ?? context.error?.message)
          : undefined
      }
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void Promise.all([detail.refetch(), context.refetch()])}
      backLabel="All MCP servers"
    >
      {context.data && (!name || detail.data) && (
        <MCPServerForm
          initial={detail.data?.server}
          revision={
            detail.data?.config_revision ?? context.data.config_revision
          }
          onCancel={onBack}
          onSaved={onSaved}
        />
      )}
    </CollectionDetailShell>
  )
}

function MCPServerForm({
  initial,
  revision,
  onCancel,
  onSaved,
}: {
  initial?: MCPServer
  revision: string
  onCancel: () => void
  onSaved: (name: string) => void
}) {
  const { t } = useTranslation()
  const seed = initial ? serverInputFromServer(initial) : undefined
  const [serverName, setServerName] = useState(seed?.name ?? "")
  const [transport, setTransport] = useState<MCPServerInput["type"]>(
    seed?.type ?? "http",
  )
  const [url, setURL] = useState(seed?.url ?? "")
  const [command, setCommand] = useState(seed?.command ?? "")
  const [args, setArgs] = useState((seed?.args ?? []).join("\n"))
  const [envFile, setEnvFile] = useState(seed?.env_file ?? "")
  const [environment, setEnvironment] = useState("")
  const [customHeaders, setCustomHeaders] = useState("")
  const [authMode, setAuthMode] = useState<
    NonNullable<MCPServerInput["auth_mode"]>
  >(seed?.auth_mode ?? "none")
  const [bearerToken, setBearerToken] = useState("")
  const [enabled, setEnabled] = useState(seed?.enabled ?? true)
  const [deferred, setDeferred] = useState<boolean | null>(
    seed?.deferred ?? null,
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [currentRevision, setCurrentRevision] = useState(revision)

  useEffect(() => {
    const next = initial ? serverInputFromServer(initial) : undefined
    setServerName(next?.name ?? "")
    setTransport(next?.type ?? "http")
    setURL(next?.url ?? "")
    setCommand(next?.command ?? "")
    setArgs((next?.args ?? []).join("\n"))
    setEnvFile(next?.env_file ?? "")
    setEnvironment("")
    setCustomHeaders("")
    setAuthMode(next?.auth_mode ?? "none")
    setBearerToken("")
    setEnabled(next?.enabled ?? true)
    setDeferred(next?.deferred ?? null)
    setCurrentRevision(revision)
  }, [initial, revision])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const name = serverName.trim()
    if (!name || (transport === "stdio" ? !command.trim() : !url.trim())) {
      setError("Name and transport endpoint are required.")
      return
    }
    const preserved = initial ? serverInputFromServer(initial) : undefined
    let nextEnvironment: Record<string, string>
    let nextHeaders: Record<string, string>
    try {
      nextEnvironment = parseSecretLines(environment)
      nextHeaders = parseSecretLines(customHeaders)
    } catch (parseError) {
      setError(
        parseError instanceof Error
          ? parseError.message
          : "Invalid secret mapping",
      )
      return
    }
    if (
      transport !== "stdio" &&
      authMode === "bearer" &&
      !initial &&
      !bearerToken.trim()
    ) {
      setError(
        "A bearer token is required for a new bearer-authenticated server.",
      )
      return
    }
    if (
      transport !== "stdio" &&
      authMode === "custom" &&
      !initial &&
      Object.keys(nextHeaders).length === 0
    ) {
      setError("At least one custom header is required.")
      return
    }
    const payload: MCPServerInput = {
      ...preserved,
      name,
      enabled,
      deferred,
      type: transport,
      url: transport === "stdio" ? "" : url.trim(),
      command: transport === "stdio" ? command.trim() : "",
      env_file: transport === "stdio" ? envFile.trim() : "",
      args:
        transport === "stdio"
          ? args
              .split(/\r?\n/)
              .map((value) => value.trim())
              .filter(Boolean)
          : [],
      env:
        transport === "stdio" && Object.keys(nextEnvironment).length > 0
          ? nextEnvironment
          : transport === "stdio"
            ? preserved?.env
            : {},
      env_keys:
        transport === "stdio" && Object.keys(nextEnvironment).length > 0
          ? Object.keys(nextEnvironment)
          : transport === "stdio"
            ? preserved?.env_keys
            : [],
      headers:
        transport !== "stdio" &&
        authMode === "custom" &&
        Object.keys(nextHeaders).length > 0
          ? nextHeaders
          : transport !== "stdio" && authMode === "custom"
            ? preserved?.headers
            : {},
      header_keys:
        transport !== "stdio" &&
        authMode === "custom" &&
        Object.keys(nextHeaders).length > 0
          ? Object.keys(nextHeaders)
          : transport !== "stdio" && authMode === "custom"
            ? preserved?.header_keys
            : [],
      auth_mode: transport === "stdio" ? "none" : authMode,
    }
    const startsOAuth =
      transport !== "stdio" &&
      authMode === "oauth" &&
      (!initial || initial.auth.type !== "oauth")
    const oauthWindow = startsOAuth ? globalThis.open("", "_blank") : null
    setSaving(true)
    setError("")
    try {
      const response = initial
        ? await updateMCPServer(initial.name, payload, currentRevision)
        : await addMCPServer(payload, currentRevision)
      if (
        transport !== "stdio" &&
        authMode === "bearer" &&
        bearerToken.trim()
      ) {
        await setMCPServerCredential(name, bearerToken.trim())
      } else if (
        initial?.auth.configured &&
        (transport === "stdio" || authMode === "none" || authMode === "custom")
      ) {
        await deleteMCPServerCredential(name)
      }
      if (startsOAuth) {
        const flow = await startMCPServerOAuth(name)
        if (oauthWindow) {
          oauthWindow.opener = null
          oauthWindow.location.href = flow.auth_url
        } else {
          globalThis.open(flow.auth_url, "_blank", "noopener,noreferrer")
        }
      }
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        initial ? "MCP server saved." : "MCP server created.",
        name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      showMCPCleanupFailureWarning(response.cleanup_failures)
      onSaved(name)
    } catch (saveError) {
      oauthWindow?.close()
      if (saveError instanceof CollectionAPIError && saveError.status === 409) {
        try {
          const latest = await listMCPServers({ limit: 1 })
          setCurrentRevision(latest.config_revision)
          setError(
            "Configuration changed. Your draft is preserved; review it and save again against the latest revision.",
          )
          return
        } catch {
          // Fall through to the original conflict when the refresh also fails.
        }
      }
      setError(saveError instanceof Error ? saveError.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }

  return (
    <form
      className="mx-auto max-w-3xl space-y-6"
      onSubmit={(event) => void submit(event)}
    >
      <Field label="Server name" required>
        <Input
          value={serverName}
          aria-label="Server name"
          disabled={Boolean(initial) || saving}
          onChange={(event) => setServerName(event.target.value)}
        />
      </Field>
      <Field label="Transport" required>
        <Select
          value={transport}
          disabled={saving}
          onValueChange={(value) =>
            setTransport(value as MCPServerInput["type"])
          }
        >
          <SelectTrigger aria-label="Transport">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="http">HTTP</SelectItem>
            <SelectItem value="sse">Server-sent events</SelectItem>
            <SelectItem value="stdio">Standard I/O</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {transport === "stdio" ? (
        <>
          <Field label="Command" required>
            <Input
              value={command}
              aria-label="Command"
              disabled={saving}
              className="font-mono"
              onChange={(event) => setCommand(event.target.value)}
            />
          </Field>
          <Field label="Arguments" hint="One argument per line.">
            <Textarea
              value={args}
              aria-label="Arguments"
              disabled={saving}
              className="min-h-28 font-mono text-xs"
              onChange={(event) => setArgs(event.target.value)}
            />
          </Field>
          <Field label="Environment file">
            <Input
              value={envFile}
              aria-label="Environment file"
              disabled={saving}
              className="font-mono"
              onChange={(event) => setEnvFile(event.target.value)}
            />
          </Field>
          <Field
            label="Environment secrets"
            hint="Optional KEY=value lines. Existing secret values remain preserved when left blank."
          >
            <Textarea
              value={environment}
              aria-label="Environment secrets"
              disabled={saving}
              className="min-h-24 font-mono text-xs"
              onChange={(event) => setEnvironment(event.target.value)}
            />
          </Field>
        </>
      ) : (
        <Field label="Server URL" required>
          <Input
            type="url"
            value={url}
            aria-label="Server URL"
            disabled={saving}
            className="font-mono"
            onChange={(event) => setURL(event.target.value)}
          />
        </Field>
      )}
      {transport !== "stdio" && (
        <>
          <Field label="Authentication">
            <Select
              value={authMode}
              disabled={saving}
              onValueChange={(value) =>
                setAuthMode(value as NonNullable<MCPServerInput["auth_mode"]>)
              }
            >
              <SelectTrigger aria-label="Authentication">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">None</SelectItem>
                <SelectItem value="bearer">Bearer token</SelectItem>
                <SelectItem value="oauth">OAuth</SelectItem>
                <SelectItem value="custom">Custom headers</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          {authMode === "bearer" && (
            <Field
              label="Bearer token"
              hint={
                initial?.auth.configured
                  ? "Leave blank to preserve the configured token."
                  : undefined
              }
            >
              <Input
                type="password"
                value={bearerToken}
                aria-label="Bearer token"
                disabled={saving}
                autoComplete="new-password"
                onChange={(event) => setBearerToken(event.target.value)}
              />
            </Field>
          )}
          {authMode === "custom" && (
            <Field
              label="Custom headers"
              hint="Optional Header=value lines. Existing secret values remain preserved when left blank."
            >
              <Textarea
                value={customHeaders}
                aria-label="Custom headers"
                disabled={saving}
                className="min-h-24 font-mono text-xs"
                onChange={(event) => setCustomHeaders(event.target.value)}
              />
            </Field>
          )}
          {authMode === "oauth" && (
            <p className="text-muted-foreground text-xs">
              Saving opens the provider authorization page in a new tab.
            </p>
          )}
        </>
      )}
      <ToggleRow
        label="Enabled"
        description="Make this server available to eligible agents."
        checked={enabled}
        disabled={saving}
        onChange={setEnabled}
      />
      <Field
        label="Tool discovery"
        hint="Inherit the integration default, or force eager/deferred discovery."
      >
        <Select
          value={deferred == null ? "inherit" : deferred ? "deferred" : "eager"}
          disabled={saving}
          onValueChange={(value) =>
            setDeferred(value === "inherit" ? null : value === "deferred")
          }
        >
          <SelectTrigger aria-label="Tool discovery">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="inherit">Inherit default</SelectItem>
            <SelectItem value="eager">Eager</SelectItem>
            <SelectItem value="deferred">Deferred</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {initial && initial.auth.type !== "none" && (
        <p className="text-muted-foreground text-xs">
          Existing {initial.auth.type} credential metadata is preserved. Secret
          values are never returned to this editor.
        </p>
      )}
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
      <div className="border-border flex justify-end gap-2 border-t pt-4">
        <Button
          type="button"
          variant="outline"
          disabled={saving}
          onClick={onCancel}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </Button>
      </div>
    </form>
  )
}

export function MCPSettingsPage({ onBack }: { onBack: () => void }) {
  const query = useQuery({
    queryKey: ["mcp-settings"],
    queryFn: getMCPConfig,
    retry: false,
  })
  return (
    <CollectionDetailShell
      title="MCP settings"
      loading={query.isLoading}
      error={query.error?.message}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="MCP servers"
    >
      {query.data && (
        <MCPSettingsForm
          initial={query.data}
          onSaved={(next) => query.refetch().then(() => next)}
        />
      )}
    </CollectionDetailShell>
  )
}

function MCPSettingsForm({
  initial,
  onSaved,
}: {
  initial: MCPConfigResponse
  onSaved: (next: MCPConfigResponse) => unknown
}) {
  const { t } = useTranslation()
  const [enabled, setEnabled] = useState(initial.enabled)
  const [discovery, setDiscovery] = useState(initial.discovery.enabled)
  const [ttl, setTTL] = useState(String(initial.discovery.ttl))
  const [limit, setLimit] = useState(
    String(initial.discovery.max_search_results),
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const parsedTTL = Number(ttl)
    const parsedLimit = Number(limit)
    if (
      enabled &&
      discovery &&
      (!Number.isSafeInteger(parsedTTL) ||
        parsedTTL < 1 ||
        !Number.isSafeInteger(parsedLimit) ||
        parsedLimit < 1)
    ) {
      setError("Discovery TTL and maximum results must be positive integers.")
      return
    }
    setSaving(true)
    setError("")
    try {
      const next = await updateMCPSettings({
        enabled,
        discovery: {
          ...initial.discovery,
          enabled: discovery,
          ttl: parsedTTL,
          max_search_results: parsedLimit,
        },
      })
      onSaved(next)
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        "MCP settings saved.",
        "MCP settings",
        next.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : "Save failed")
    } finally {
      setSaving(false)
    }
  }
  return (
    <form
      className="mx-auto max-w-3xl space-y-6"
      onSubmit={(event) => void submit(event)}
    >
      <ToggleRow
        label="MCP integration"
        description="Enable configured MCP servers globally."
        checked={enabled}
        disabled={saving}
        onChange={setEnabled}
      />
      <ToggleRow
        label="Tool discovery"
        description="Search server tools dynamically."
        checked={discovery}
        disabled={saving || !enabled}
        onChange={setDiscovery}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="mcp-ttl">Discovery TTL</Label>
          <Input
            id="mcp-ttl"
            type="number"
            min="1"
            required
            value={ttl}
            disabled={saving}
            onChange={(event) => setTTL(event.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="mcp-results">Maximum results</Label>
          <Input
            id="mcp-results"
            type="number"
            min="1"
            required
            value={limit}
            disabled={saving}
            onChange={(event) => setLimit(event.target.value)}
          />
        </div>
      </div>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
      <div className="border-border flex justify-end border-t pt-4">
        <Button type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save settings"}
        </Button>
      </div>
    </form>
  )
}

function ToggleRow({
  label,
  description,
  checked,
  disabled,
  onChange,
}: {
  label: string
  description: string
  checked: boolean
  disabled: boolean
  onChange: (checked: boolean) => void
}) {
  return (
    <div className="border-border flex items-center gap-4 rounded-lg border px-3 py-3">
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium">{label}</p>
        <p className="text-muted-foreground text-xs">{description}</p>
      </div>
      <Switch
        checked={checked}
        disabled={disabled}
        aria-label={label}
        onCheckedChange={onChange}
      />
    </div>
  )
}

function DetailRows({ rows }: { rows: Array<[string, string]> }) {
  return (
    <dl className="border-border divide-border rounded-lg border text-sm">
      {rows.map(([label, value]) => (
        <div
          key={label}
          className="grid grid-cols-[minmax(8rem,auto)_minmax(0,1fr)] gap-4 border-b px-3 py-3 last:border-b-0"
        >
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 text-right break-words">{value || "—"}</dd>
        </div>
      ))}
    </dl>
  )
}

function showMCPCleanupFailureWarning(
  cleanupFailures?: ReadonlyArray<{ id: string; code: string }>,
) {
  if (!cleanupFailures?.length) return
  toast.warning("Credential cleanup needs attention.", {
    description:
      "The server changes were saved, but one or more unreferenced credentials could not be removed.",
  })
}

function parseSecretLines(value: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const line of value.split(/\r?\n/)) {
    if (!line.trim()) continue
    const separator = line.indexOf("=")
    const key = line.slice(0, separator).trim()
    const secret = separator >= 0 ? line.slice(separator + 1).trim() : ""
    if (!key || !secret) throw new Error(`Invalid secret mapping: ${line}`)
    if (result[key] != null) throw new Error(`Duplicate secret key: ${key}`)
    result[key] = secret
  }
  return result
}

function discoveryLabel(deferred: boolean | null): string {
  return deferred == null
    ? "Inherit discovery default"
    : deferred
      ? "Deferred discovery"
      : "Eager discovery"
}
