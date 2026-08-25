import { IconEdit, IconPlus } from "@tabler/icons-react"
import { useInfiniteQuery, useQuery } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import { CollectionAPIError } from "@/api/collection"
import {
  type ModelAlias,
  type ModelAliasSummary,
  type ModelRouterBlock,
  type ModelRouterConfig,
  type ModelRouterSummary,
  bulkDeleteModelAliases,
  bulkDeleteModelRouters,
  createModelAlias,
  createModelRouter,
  getModelAlias,
  getModelRouter,
  getModels,
  listModelAliases,
  listModelRouters,
  updateModelAliasByName,
  updateModelRouterByName,
} from "@/api/models"
import {
  type CollectionDefinition,
  CollectionDetailShell,
} from "@/components/collection"
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
import { refreshGatewayState } from "@/store/gateway"

import {
  type PilotCollectionSearch,
  StandardPilotCollectionPage,
} from "./standard-pilot-collection-page"

const aliasDefaultQuery = "ORDER BY name ASC"
const routerDefaultQuery = "ORDER BY name ASC"
const supportedViews = ["list", "table", "grid"] as const

interface CollectionNavigation<T> {
  onAdd: () => void
  onOpen: (item: T) => void
  onEdit: (item: T) => void
}

export function ModelAliasesCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: CollectionNavigation<ModelAliasSummary> & {
  search: PilotCollectionSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: aliasDefaultQuery,
    supportedViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["model-aliases", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listModelAliases(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = query.data?.pages.flatMap((page) => page.model_aliases) ?? []
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<ModelAliasSummary>>(
    () => ({
      key: "model-aliases",
      title: "Model aliases",
      defaultQuery: aliasDefaultQuery,
      supportedViews,
      defaultView: "list",
      getItemID: (item) => item.name,
      getItemLabel: (item) => item.name,
      getItemIdentity: (item) => ({
        title: item.name,
        description: item.model,
        metadata:
          item.override_count > 0
            ? `${item.override_count} account overrides`
            : "Default mapping",
      }),
      columns: [
        { id: "model", header: "Default model", cell: (item) => item.model },
        {
          id: "overrides",
          header: "Overrides",
          cell: (item) => item.override_count,
          className: "w-28 tabular-nums",
        },
        {
          id: "disabled",
          header: "Disabled",
          cell: (item) => item.disabled_account_count,
          className: "w-28 tabular-nums",
        },
      ],
      gridFacts: [
        { id: "model", label: "Default model", value: (item) => item.model },
        {
          id: "overrides",
          label: "Overrides",
          value: (item) => item.override_count,
        },
        {
          id: "disabled",
          label: "Disabled accounts",
          value: (item) => item.disabled_account_count,
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit alias",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
      ],
    }),
    [navigation.onEdit],
  )

  return (
    <StandardPilotCollectionPage
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
        <Button type="button" size="sm" onClick={navigation.onAdd}>
          <IconPlus /> Add alias
        </Button>
      }
      onBulkDelete={(ids) => {
        if (!first?.config_revision)
          throw new Error("Configuration revision is unavailable")
        return bulkDeleteModelAliases(ids, first.config_revision)
      }}
      afterBulkDelete={() => query.refetch()}
      emptyTitle="No configured model aliases"
      emptyDescription="Create an alias or start from a developer catalog template."
    />
  )
}

export function ModelRoutersCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: CollectionNavigation<ModelRouterSummary> & {
  search: PilotCollectionSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: routerDefaultQuery,
    supportedViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["model-routers", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listModelRouters(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = query.data?.pages.flatMap((page) => page.model_routers) ?? []
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<ModelRouterSummary>>(
    () => ({
      key: "model-routers",
      title: "Model routers",
      defaultQuery: routerDefaultQuery,
      supportedViews,
      defaultView: "list",
      getItemID: (item) => item.name,
      getItemLabel: (item) => item.name,
      getItemIdentity: (item) => ({
        title: item.name,
        description: `${item.rule_count} routing rules`,
        metadata: item.entry ? `Entry: ${item.entry}` : undefined,
      }),
      columns: [
        { id: "entry", header: "Entry", cell: (item) => item.entry || "—" },
        {
          id: "blocks",
          header: "Blocks",
          cell: (item) => item.block_count,
          className: "w-24 tabular-nums",
        },
        {
          id: "rules",
          header: "Rules",
          cell: (item) => item.rule_count,
          className: "w-24 tabular-nums",
        },
      ],
      gridFacts: [
        { id: "entry", label: "Entry", value: (item) => item.entry || "—" },
        {
          id: "blocks",
          label: "Blocks",
          value: (item) => item.block_count,
        },
        { id: "rules", label: "Rules", value: (item) => item.rule_count },
      ],
      badges: [
        {
          id: "enabled",
          label: (item) => (item.enabled ? "Enabled" : "Disabled"),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit router",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
      ],
    }),
    [navigation.onEdit],
  )

  return (
    <StandardPilotCollectionPage
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
        <Button type="button" size="sm" onClick={navigation.onAdd}>
          <IconPlus /> Add router
        </Button>
      }
      onBulkDelete={(ids) => {
        if (!first?.config_revision)
          throw new Error("Configuration revision is unavailable")
        return bulkDeleteModelRouters(ids, first.config_revision)
      }}
      afterBulkDelete={() => query.refetch()}
      emptyTitle="No model routers"
      emptyDescription="Create a router to choose model aliases from message characteristics."
    />
  )
}

export function ModelAliasDetailPage({
  name,
  onBack,
  onEdit,
}: {
  name: string
  onBack: () => void
  onEdit: () => void
}) {
  const query = useQuery({
    queryKey: ["model-alias", name],
    queryFn: ({ signal }) => getModelAlias(name, signal),
    retry: false,
  })
  const alias = query.data?.model_alias
  return (
    <CollectionDetailShell
      title={alias?.name ?? "Model alias"}
      identity={<span className="font-mono text-xs">{name}</span>}
      loading={query.isLoading}
      error={
        query.error &&
        !(
          query.error instanceof CollectionAPIError &&
          query.error.status === 404
        )
          ? query.error.message
          : undefined
      }
      notFound={
        query.error instanceof CollectionAPIError && query.error.status === 404
      }
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All model aliases"
      actions={
        alias ? (
          <Button type="button" size="sm" onClick={onEdit}>
            <IconEdit /> Edit
          </Button>
        ) : undefined
      }
    >
      {alias && <ModelAliasDetails alias={alias} />}
    </CollectionDetailShell>
  )
}

function ModelAliasDetails({ alias }: { alias: ModelAlias }) {
  const overrides = Object.entries(alias.account_overrides ?? {})
  return (
    <div className="space-y-6">
      <DetailList
        values={[
          ["Default model", alias.model],
          ["Account overrides", String(overrides.length)],
          ["Disabled accounts", String(alias.disabled_accounts?.length ?? 0)],
        ]}
      />
      {overrides.length > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold">Account overrides</h2>
          <DetailList values={overrides} />
        </section>
      )}
      {(alias.disabled_accounts?.length ?? 0) > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold">Disabled accounts</h2>
          <div className="flex flex-wrap gap-2">
            {alias.disabled_accounts?.map((account) => (
              <Badge key={account} variant="outline" className="font-mono">
                {account}
              </Badge>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

export function ModelRouterDetailPage({
  name,
  onBack,
  onEdit,
}: {
  name: string
  onBack: () => void
  onEdit: () => void
}) {
  const query = useQuery({
    queryKey: ["model-router", name],
    queryFn: ({ signal }) => getModelRouter(name, signal),
    retry: false,
  })
  const router = query.data?.model_router
  return (
    <CollectionDetailShell
      title={router?.name ?? "Model router"}
      identity={<span className="font-mono text-xs">{name}</span>}
      status={
        router ? (
          <Badge variant="outline">
            {router.enabled ? "Enabled" : "Disabled"}
          </Badge>
        ) : undefined
      }
      loading={query.isLoading}
      error={
        query.error &&
        !(
          query.error instanceof CollectionAPIError &&
          query.error.status === 404
        )
          ? query.error.message
          : undefined
      }
      notFound={
        query.error instanceof CollectionAPIError && query.error.status === 404
      }
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All model routers"
      actions={
        router ? (
          <Button type="button" size="sm" onClick={onEdit}>
            <IconEdit /> Edit
          </Button>
        ) : undefined
      }
    >
      {router && <ModelRouterDetails router={router} />}
    </CollectionDetailShell>
  )
}

function ModelRouterDetails({ router }: { router: ModelRouterConfig }) {
  const blocks = router.blocks ?? []
  return (
    <div className="space-y-5">
      <DetailList
        values={[
          ["Entry block", router.entry || "—"],
          ["Blocks", String(blocks.length)],
          [
            "Rules",
            String(
              blocks.reduce(
                (total, block) => total + (block.rules?.length ?? 0),
                0,
              ),
            ),
          ],
        ]}
      />
      <section>
        <h2 className="mb-2 text-sm font-semibold">Routing blocks</h2>
        <div className="border-border divide-border divide-y rounded-lg border">
          {blocks.map((block) => (
            <div
              key={block.id}
              className="flex min-w-0 items-center gap-3 px-3 py-3 text-sm"
            >
              <span className="min-w-0 flex-1 truncate font-mono">
                {block.id}
              </span>
              <Badge variant="outline">{block.type}</Badge>
              <span className="text-muted-foreground max-w-64 truncate">
                {block.model || `${block.rules?.length ?? 0} rules`}
              </span>
            </div>
          ))}
        </div>
      </section>
    </div>
  )
}

export function ModelAliasEditorPage({
  name,
  onBack,
  onSaved,
}: {
  name?: string
  onBack: () => void
  onSaved: (name: string) => void
}) {
  const detail = useQuery({
    queryKey: ["model-alias", name],
    queryFn: ({ signal }) => getModelAlias(name ?? "", signal),
    enabled: Boolean(name),
    retry: false,
  })
  const context = useQuery({
    queryKey: ["model-alias-editor-context"],
    queryFn: async ({ signal }) => {
      const [collection, models] = await Promise.all([
        listModelAliases({ limit: 1 }, signal),
        getModels(),
      ])
      return { collection, models }
    },
    retry: false,
  })
  return (
    <CollectionDetailShell
      title={name ? "Edit model alias" : "New model alias"}
      identity={
        name ? <span className="font-mono text-xs">{name}</span> : undefined
      }
      loading={(Boolean(name) && detail.isLoading) || context.isLoading}
      error={
        !(
          detail.error instanceof CollectionAPIError &&
          detail.error.status === 404
        )
          ? (detail.error?.message ?? context.error?.message)
          : undefined
      }
      notFound={
        detail.error instanceof CollectionAPIError &&
        detail.error.status === 404
      }
      onBack={onBack}
      onRetry={() => void Promise.all([detail.refetch(), context.refetch()])}
      backLabel="All model aliases"
    >
      {context.data && (!name || detail.data) && (
        <ModelAliasForm
          initial={detail.data?.model_alias}
          revision={
            detail.data?.config_revision ??
            context.data.collection.config_revision
          }
          templates={context.data.models.model_alias_catalog ?? []}
          onCancel={onBack}
          onSaved={onSaved}
        />
      )}
    </CollectionDetailShell>
  )
}

function ModelAliasForm({
  initial,
  revision,
  templates,
  onCancel,
  onSaved,
}: {
  initial?: ModelAlias
  revision: string
  templates: Array<{ name: string; description: string }>
  onCancel: () => void
  onSaved: (name: string) => void
}) {
  const [aliasName, setAliasName] = useState(initial?.name ?? "")
  const [model, setModel] = useState(initial?.model ?? "")
  const [overrides, setOverrides] = useState(
    formatOverrides(initial?.account_overrides),
  )
  const [disabled, setDisabled] = useState(
    (initial?.disabled_accounts ?? []).join("\n"),
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [currentRevision, setCurrentRevision] = useState(revision)

  useEffect(() => {
    setAliasName(initial?.name ?? "")
    setModel(initial?.model ?? "")
    setOverrides(formatOverrides(initial?.account_overrides))
    setDisabled((initial?.disabled_accounts ?? []).join("\n"))
    setCurrentRevision(revision)
  }, [initial, revision])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const nextName = aliasName.trim()
    if (!nextName || !model.trim()) {
      setError("Alias name and default model are required.")
      return
    }
    let accountOverrides: Record<string, string>
    try {
      accountOverrides = parseOverrides(overrides)
    } catch (parseError) {
      setError(
        parseError instanceof Error ? parseError.message : "Invalid overrides",
      )
      return
    }
    const payload: ModelAlias = {
      name: nextName,
      model: model.trim(),
      ...(Object.keys(accountOverrides).length > 0
        ? { account_overrides: accountOverrides }
        : {}),
      ...(disabled.trim()
        ? {
            disabled_accounts: disabled
              .split(/\r?\n/)
              .map((value) => value.trim())
              .filter(Boolean),
          }
        : {}),
    }
    setSaving(true)
    setError("")
    try {
      if (initial)
        await updateModelAliasByName(initial.name, payload, currentRevision)
      else await createModelAlias(payload, currentRevision)
      await refreshGatewayState({ force: true })
      toast.success(initial ? "Model alias saved." : "Model alias created.")
      onSaved(nextName)
    } catch (saveError) {
      if (saveError instanceof CollectionAPIError && saveError.status === 409) {
        try {
          const latest = await listModelAliases({ limit: 1 })
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
      {!initial && templates.length > 0 && (
        <Field
          label="Developer catalog template"
          hint="Templates seed a configured alias; they are not collection items until saved."
        >
          <Select value="" onValueChange={(value) => setAliasName(value)}>
            <SelectTrigger aria-label="Developer catalog template">
              <SelectValue placeholder="Choose a template (optional)" />
            </SelectTrigger>
            <SelectContent>
              {templates.map((template) => (
                <SelectItem key={template.name} value={template.name}>
                  {template.name} — {template.description}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
      <Field label="Alias name" required>
        <Input
          value={aliasName}
          aria-label="Alias name"
          disabled={Boolean(initial) || saving}
          onChange={(event) => setAliasName(event.target.value)}
          autoComplete="off"
        />
      </Field>
      <Field label="Default upstream model" required>
        <Input
          value={model}
          aria-label="Default upstream model"
          disabled={saving}
          onChange={(event) => setModel(event.target.value)}
        />
      </Field>
      <Field
        label="Account overrides"
        hint="One account=model mapping per line."
      >
        <Textarea
          value={overrides}
          aria-label="Account overrides"
          disabled={saving}
          className="min-h-28 font-mono text-xs"
          onChange={(event) => setOverrides(event.target.value)}
        />
      </Field>
      <Field label="Disabled accounts" hint="One account reference per line.">
        <Textarea
          value={disabled}
          aria-label="Disabled accounts"
          disabled={saving}
          className="min-h-24 font-mono text-xs"
          onChange={(event) => setDisabled(event.target.value)}
        />
      </Field>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
      <EditorActions saving={saving} onCancel={onCancel} />
    </form>
  )
}

export function ModelRouterEditorPage({
  name,
  onBack,
  onSaved,
}: {
  name?: string
  onBack: () => void
  onSaved: (name: string) => void
}) {
  const detail = useQuery({
    queryKey: ["model-router", name],
    queryFn: ({ signal }) => getModelRouter(name ?? "", signal),
    enabled: Boolean(name),
    retry: false,
  })
  const context = useQuery({
    queryKey: ["model-router-editor-context"],
    queryFn: async ({ signal }) => {
      const [routers, aliases] = await Promise.all([
        listModelRouters({ limit: 1 }, signal),
        listModelAliases({ limit: 200 }, signal),
      ])
      return { routers, aliases }
    },
    retry: false,
  })
  return (
    <CollectionDetailShell
      title={name ? "Edit model router" : "New model router"}
      identity={
        name ? <span className="font-mono text-xs">{name}</span> : undefined
      }
      loading={(Boolean(name) && detail.isLoading) || context.isLoading}
      error={
        !(
          detail.error instanceof CollectionAPIError &&
          detail.error.status === 404
        )
          ? (detail.error?.message ?? context.error?.message)
          : undefined
      }
      notFound={
        detail.error instanceof CollectionAPIError &&
        detail.error.status === 404
      }
      onBack={onBack}
      onRetry={() => void Promise.all([detail.refetch(), context.refetch()])}
      backLabel="All model routers"
    >
      {context.data && (!name || detail.data) && (
        <ModelRouterForm
          initial={detail.data?.model_router}
          revision={
            detail.data?.config_revision ?? context.data.routers.config_revision
          }
          aliases={context.data.aliases.model_aliases}
          onCancel={onBack}
          onSaved={onSaved}
        />
      )}
    </CollectionDetailShell>
  )
}

function ModelRouterForm({
  initial,
  revision,
  aliases,
  onCancel,
  onSaved,
}: {
  initial?: ModelRouterConfig
  revision: string
  aliases: Array<Pick<ModelAlias, "name">>
  onCancel: () => void
  onSaved: (name: string) => void
}) {
  const parsed = useMemo(() => parseRouterTargets(initial), [initial])
  const [routerName, setRouterName] = useState(initial?.name ?? "")
  const [fallback, setFallback] = useState(parsed.fallback)
  const [code, setCode] = useState(parsed.code)
  const [media, setMedia] = useState(parsed.media)
  const [containsText, setContainsText] = useState(parsed.containsText)
  const [containsTarget, setContainsTarget] = useState(parsed.containsTarget)
  const [regexText, setRegexText] = useState(parsed.regexText)
  const [regexTarget, setRegexTarget] = useState(parsed.regexTarget)
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const [currentRevision, setCurrentRevision] = useState(revision)
  const aliasNames = aliases.map((alias) => alias.name)

  useEffect(() => {
    setRouterName(initial?.name ?? "")
    setFallback(parsed.fallback)
    setCode(parsed.code)
    setMedia(parsed.media)
    setContainsText(parsed.containsText)
    setContainsTarget(parsed.containsTarget)
    setRegexText(parsed.regexText)
    setRegexTarget(parsed.regexTarget)
    setEnabled(initial?.enabled ?? true)
    setCurrentRevision(revision)
  }, [initial, parsed, revision])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const nextName = routerName.trim()
    const hasContains = Boolean(containsText.trim() && containsTarget)
    const hasRegex = Boolean(regexText.trim() && regexTarget)
    if (
      !nextName ||
      !fallback ||
      (!code && !media && !hasContains && !hasRegex) ||
      Boolean(containsText.trim()) !== Boolean(containsTarget) ||
      Boolean(regexText.trim()) !== Boolean(regexTarget)
    ) {
      setError(
        "Router name, default target, and at least one complete routing rule are required.",
      )
      return
    }
    if (regexText.trim()) {
      try {
        new RegExp(regexText.trim())
      } catch {
        setError("The regular-expression rule is invalid.")
        return
      }
    }
    const router = buildSimpleRouter(nextName, enabled, {
      fallback,
      code,
      media,
      containsText: containsText.trim(),
      containsTarget,
      regexText: regexText.trim(),
      regexTarget,
    })
    setSaving(true)
    setError("")
    try {
      if (initial)
        await updateModelRouterByName(
          initial.name ?? nextName,
          router,
          currentRevision,
        )
      else await createModelRouter(router, currentRevision)
      await refreshGatewayState({ force: true })
      toast.success(initial ? "Model router saved." : "Model router created.")
      onSaved(nextName)
    } catch (saveError) {
      if (saveError instanceof CollectionAPIError && saveError.status === 409) {
        try {
          const latest = await listModelRouters({ limit: 1 })
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
      <Field label="Router name" required>
        <Input
          value={routerName}
          aria-label="Router name"
          disabled={Boolean(initial) || saving}
          onChange={(event) => setRouterName(event.target.value)}
        />
      </Field>
      <TargetSelect
        label="Default target"
        value={fallback}
        aliases={aliasNames}
        disabled={saving}
        onChange={setFallback}
        required
      />
      <TargetSelect
        label="Messages containing code"
        value={code}
        aliases={aliasNames}
        disabled={saving}
        onChange={setCode}
      />
      <TargetSelect
        label="Messages containing media"
        value={media}
        aliases={aliasNames}
        disabled={saving}
        onChange={setMedia}
      />
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Text to match">
          <Input
            value={containsText}
            aria-label="Text to match"
            disabled={saving}
            onChange={(event) => setContainsText(event.target.value)}
          />
        </Field>
        <TargetSelect
          label="Text-match target"
          value={containsTarget}
          aliases={aliasNames}
          disabled={saving}
          onChange={setContainsTarget}
        />
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Regular expression">
          <Input
            value={regexText}
            aria-label="Regular expression"
            disabled={saving}
            className="font-mono"
            onChange={(event) => setRegexText(event.target.value)}
          />
        </Field>
        <TargetSelect
          label="Regular-expression target"
          value={regexTarget}
          aliases={aliasNames}
          disabled={saving}
          onChange={setRegexTarget}
        />
      </div>
      <div className="border-border flex items-center gap-4 rounded-lg border px-3 py-3">
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium">Enabled</p>
          <p className="text-muted-foreground text-xs">
            Disabled legacy routers remain disabled until explicitly enabled.
          </p>
        </div>
        <Switch
          checked={enabled}
          disabled={saving}
          aria-label="Router enabled"
          onCheckedChange={setEnabled}
        />
      </div>
      {error && (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      )}
      <EditorActions saving={saving} onCancel={onCancel} />
    </form>
  )
}

function TargetSelect({
  label,
  value,
  aliases,
  disabled,
  required,
  onChange,
}: {
  label: string
  value: string
  aliases: string[]
  disabled: boolean
  required?: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="space-y-2">
      <Label>
        {label}
        {required ? " *" : ""}
      </Label>
      <Select
        value={value || "__none__"}
        disabled={disabled}
        onValueChange={(next) => onChange(next === "__none__" ? "" : next)}
      >
        <SelectTrigger aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {!required && (
            <SelectItem value="__none__">No special target</SelectItem>
          )}
          {aliases.map((alias) => (
            <SelectItem key={alias} value={alias}>
              {alias}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}

function EditorActions({
  saving,
  onCancel,
}: {
  saving: boolean
  onCancel: () => void
}) {
  return (
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
  )
}

function DetailList({ values }: { values: Array<[string, string]> }) {
  return (
    <dl className="border-border divide-border rounded-lg border text-sm">
      {values.map(([label, value]) => (
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

function formatOverrides(overrides?: Record<string, string>) {
  return Object.entries(overrides ?? {})
    .map(([account, model]) => `${account}=${model}`)
    .join("\n")
}

function parseOverrides(value: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const line of value.split(/\r?\n/)) {
    if (!line.trim()) continue
    const separator = line.indexOf("=")
    if (separator < 1 || !line.slice(separator + 1).trim()) {
      throw new Error(`Invalid account override: ${line}`)
    }
    const account = line.slice(0, separator).trim()
    if (result[account] != null)
      throw new Error(`Duplicate account override: ${account}`)
    result[account] = line.slice(separator + 1).trim()
  }
  return result
}

function parseRouterTargets(router?: ModelRouterConfig) {
  const blocks = new Map(
    (router?.blocks ?? []).map((block) => [block.id, block]),
  )
  const entry = blocks.get(router?.entry ?? "")
  const target = (id?: string) => (id ? (blocks.get(id)?.model ?? "") : "")
  let code = ""
  let media = ""
  let containsText = ""
  let containsTarget = ""
  let regexText = ""
  let regexTarget = ""
  for (const rule of entry?.rules ?? []) {
    if (rule.match === "has_code") code = target(rule.target)
    if (rule.match === "has_media") media = target(rule.target)
    if (rule.match === "contains" && !containsTarget) {
      containsText = rule.value ?? ""
      containsTarget = target(rule.target)
    }
    if (rule.match === "regex" && !regexTarget) {
      regexText = rule.value ?? ""
      regexTarget = target(rule.target)
    }
  }
  return {
    fallback: target(entry?.fallback),
    code,
    media,
    containsText,
    containsTarget,
    regexText,
    regexTarget,
  }
}

function buildSimpleRouter(
  name: string,
  enabled: boolean,
  values: {
    fallback: string
    code: string
    media: string
    containsText: string
    containsTarget: string
    regexText: string
    regexTarget: string
  },
): ModelRouterConfig {
  const blocks: ModelRouterBlock[] = []
  const rules: NonNullable<ModelRouterBlock["rules"]> = []
  const target = (prefix: string, model: string) => {
    const id = `${prefix}-${
      model
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-|-$/g, "") || "model"
    }`
    if (!blocks.some((block) => block.id === id))
      blocks.push({ id, type: "model", model })
    return id
  }
  if (values.code)
    rules.push({ match: "has_code", target: target("code", values.code) })
  if (values.media)
    rules.push({ match: "has_media", target: target("media", values.media) })
  if (values.containsText && values.containsTarget) {
    rules.push({
      match: "contains",
      value: values.containsText,
      target: target("contains", values.containsTarget),
    })
  }
  if (values.regexText && values.regexTarget) {
    rules.push({
      match: "regex",
      value: values.regexText,
      target: target("regex", values.regexTarget),
    })
  }
  const fallbackID = target("default", values.fallback)
  return {
    name,
    enabled,
    entry: "entry",
    blocks: [
      { id: "entry", type: "rules", rules, fallback: fallbackID },
      ...blocks,
    ],
  }
}
