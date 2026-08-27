import {
  IconEdit,
  IconLogout,
  IconPlus,
  IconPower,
  IconRefresh,
  IconStar,
} from "@tabler/icons-react"
import { useInfiniteQuery, useMutation, useQuery } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type AccountRouter,
  type AccountRouterSummary,
  type AccountSummary,
  bulkDeleteAccountRouters,
  getAccount,
  getAccountRouter,
  listAccountRouters,
  listAccounts,
  setDefaultAccountRouter,
  updateAccountRouter,
} from "@/api/accounts"
import { CollectionAPIError } from "@/api/collection"
import {
  type CodexAccountLimitAccount,
  getCodexAccountLimits,
  logoutOAuth,
} from "@/api/oauth"
import {
  type CollectionDefinition,
  CollectionDetailShell,
  type StandardCollectionPageSearch,
} from "@/components/collection"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import { CodexAccountLimitSummary } from "@/components/credentials/codex-account-limits-panel"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  normalizeCollectionRouteSearch,
} from "@/hooks/use-collection-route-state"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import {
  accountCollectionViews,
  accountRoutersDefaultQuery,
  accountsDefaultQuery,
} from "./account-collection-route-state"
import { LogoutConfirmDialog } from "./logout-confirm-dialog"

interface AccountsCollectionNavigation {
  onAdd: () => void
  onOpen: (account: AccountSummary) => void
  onEdit: (account: AccountSummary) => void
  onRouters: () => void
}

export function AccountsCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: AccountsCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const [logoutTarget, setLogoutTarget] = useState<AccountSummary | null>(null)
  const [logoutPending, setLogoutPending] = useState(false)
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: accountsDefaultQuery,
    supportedViews: accountCollectionViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["accounts", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listAccounts(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = query.data?.pages.flatMap((page) => page.accounts) ?? []
  const first = query.data?.pages[0]
  const definition = useMemo<CollectionDefinition<AccountSummary>>(
    () => ({
      key: "accounts",
      title: "Accounts",
      defaultQuery: accountsDefaultQuery,
      supportedViews: accountCollectionViews,
      defaultView: "list",
      getItemID: (account) => account.id,
      getItemLabel: accountTitle,
      getItemIdentity: (account) => ({
        title: accountTitle(account),
        description: providerLabel(account.provider),
        metadata: account.auth_method
          ? `Authentication: ${formatAuthMethod(account.auth_method)}`
          : undefined,
      }),
      columns: [
        {
          id: "provider",
          header: "Provider",
          cell: (account) => providerLabel(account.provider),
        },
        {
          id: "auth-method",
          header: "Authentication",
          cell: (account) => formatAuthMethod(account.auth_method),
        },
        {
          id: "expires",
          header: "Expires",
          cell: (account) => formatAccountExpiry(account.expires_at),
        },
      ],
      gridFacts: [
        {
          id: "provider",
          label: "Provider",
          value: (account) => providerLabel(account.provider),
        },
        {
          id: "auth-method",
          label: "Authentication",
          value: (account) => formatAuthMethod(account.auth_method),
        },
        {
          id: "expires",
          label: "Expires",
          value: (account) => formatAccountExpiry(account.expires_at),
        },
      ],
      badges: [
        {
          id: "status",
          label: (account) => formatAccountStatus(account.status),
          variant: "outline",
        },
      ],
      actions: [
        {
          id: "renew",
          label: "Renew account",
          icon: <IconRefresh />,
          onSelect: navigation.onEdit,
        },
        {
          id: "logout",
          label: "Remove account",
          icon: <IconLogout />,
          destructive: true,
          onSelect: setLogoutTarget,
        },
      ],
    }),
    [navigation.onEdit],
  )

  const confirmLogout = async () => {
    if (!logoutTarget) return
    setLogoutPending(true)
    try {
      await logoutOAuth(logoutTarget.provider, logoutTarget.account)
      setLogoutTarget(null)
      toast.success(`${accountTitle(logoutTarget)} was removed.`)
      await query.refetch()
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : "The account was not removed.",
      )
    } finally {
      setLogoutPending(false)
    }
  }

  return (
    <>
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
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="hidden sm:inline-flex"
              onClick={navigation.onRouters}
            >
              Account routers
            </Button>
            <Button type="button" size="sm" onClick={navigation.onAdd}>
              <IconPlus /> Add account
            </Button>
          </div>
        }
        emptyTitle="No registered accounts"
        emptyDescription="Add a provider account to authenticate model requests."
      />
      <LogoutConfirmDialog
        open={logoutTarget != null}
        providerLabel={logoutTarget ? accountTitle(logoutTarget) : ""}
        isSubmitting={logoutPending}
        onOpenChange={(open) => {
          if (!open && !logoutPending) setLogoutTarget(null)
        }}
        onConfirm={confirmLogout}
      />
    </>
  )
}

interface AccountRoutersCollectionNavigation {
  onAdd: () => void
  onOpen: (router: AccountRouterSummary) => void
  onEdit: (router: AccountRouterSummary) => void
}

export function AccountRoutersCollectionPage({
  search,
  onSearchChange,
  ...navigation
}: AccountRoutersCollectionNavigation & {
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const activeQuery = normalizeCollectionRouteSearch(search, {
    defaultQuery: accountRoutersDefaultQuery,
    supportedViews: accountCollectionViews,
  }).q
  const query = useInfiniteQuery({
    queryKey: ["account-routers", "collection", activeQuery],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) =>
      listAccountRouters(
        { query: activeQuery, cursor: pageParam || undefined, limit: 50 },
        signal,
      ),
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  })
  const items = query.data?.pages.flatMap((page) => page.account_routers) ?? []
  const first = query.data?.pages[0]
  const defaultMutation = useMutation({
    mutationFn: (router: AccountRouterSummary) => {
      if (!first?.config_revision) {
        throw new Error("Configuration revision is unavailable")
      }
      return setDefaultAccountRouter(router.id, first.config_revision)
    },
    onSuccess: async (response, router) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${router.name} is now the default account router.`,
        router.name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      await query.refetch()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "The default router was not changed.",
      )
    },
  })
  const enabledMutation = useMutation({
    mutationFn: async (summary: AccountRouterSummary) => {
      const detail = await getAccountRouter(summary.id)
      const router = detail.account_router
      return updateAccountRouter(
        router.id,
        {
          name: router.name,
          enabled: !router.enabled,
          entry: router.entry,
          refresh_interval_seconds: router.refresh_interval_seconds,
          blocks: router.blocks,
        },
        detail.config_revision,
      )
    },
    onSuccess: async (response) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${response.account_router.name} was ${response.account_router.enabled ? "enabled" : "disabled"}.`,
        response.account_router.name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      await query.refetch()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "The router was not updated.",
      )
    },
  })
  const mutateDefault = defaultMutation.mutate
  const mutateEnabled = enabledMutation.mutate
  const actionPending = defaultMutation.isPending || enabledMutation.isPending
  const definition = useMemo<CollectionDefinition<AccountRouterSummary>>(
    () => ({
      key: "account-routers",
      title: "Account routers",
      defaultQuery: accountRoutersDefaultQuery,
      supportedViews: accountCollectionViews,
      defaultView: "list",
      getItemID: (router) => router.id,
      getItemLabel: (router) => router.name,
      getItemIdentity: (router) => ({
        title: router.name,
        description: router.entry ? `Entry: ${router.entry}` : "No entry block",
        metadata: `${router.accounts} account${router.accounts === 1 ? "" : "s"} · ${router.blocks} block${router.blocks === 1 ? "" : "s"}`,
      }),
      columns: [
        {
          id: "status",
          header: "Status",
          cell: (router) => formatAccountRouterStatus(router.status),
        },
        {
          id: "entry",
          header: "Entry",
          cell: (router) => router.entry || "—",
        },
        {
          id: "accounts",
          header: "Accounts",
          cell: (router) => router.accounts,
          className: "w-28 tabular-nums",
        },
        {
          id: "blocks",
          header: "Blocks",
          cell: (router) => router.blocks,
          className: "w-24 tabular-nums",
        },
      ],
      gridFacts: [
        { id: "entry", label: "Entry", value: (router) => router.entry || "—" },
        {
          id: "accounts",
          label: "Accounts",
          value: (router) => router.accounts,
        },
        { id: "blocks", label: "Blocks", value: (router) => router.blocks },
        { id: "status", label: "Status", value: (router) => router.status },
      ],
      badges: [
        {
          id: "status",
          label: (router) => formatAccountRouterStatus(router.status),
          variant: "outline",
        },
        {
          id: "default",
          label: (router) => (router.is_default ? "Default" : null),
        },
      ],
      actions: [
        {
          id: "edit",
          label: "Edit router",
          icon: <IconEdit />,
          onSelect: navigation.onEdit,
        },
        {
          id: "toggle-enabled",
          label: (router) =>
            router.is_default && router.enabled
              ? "Default router cannot be disabled"
              : router.enabled
                ? "Disable router"
                : "Enable router",
          icon: <IconPower />,
          disabled: (router) =>
            actionPending || (router.is_default && router.enabled),
          onSelect: mutateEnabled,
        },
        {
          id: "make-default",
          label: "Make default",
          icon: <IconStar />,
          hidden: (router) => router.is_default,
          disabled: (router) =>
            actionPending || !router.enabled || router.status !== "available",
          onSelect: mutateDefault,
        },
      ],
    }),
    [actionPending, mutateDefault, mutateEnabled, navigation.onEdit],
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
        <Button type="button" size="sm" onClick={navigation.onAdd}>
          <IconPlus /> Add router
        </Button>
      }
      onBulkDelete={async (ids) => {
        if (!first?.config_revision) {
          throw new Error("Configuration revision is unavailable")
        }
        return bulkDeleteAccountRouters(ids, first.config_revision)
      }}
      afterBulkDelete={async () => {
        await Promise.all([
          query.refetch(),
          refreshGatewayState({ force: true }),
        ])
      }}
      bulkDeleteConfirmation={{
        description:
          "Only explicitly selected routers will be deleted. Default or referenced routers will remain selected with their blocker.",
      }}
      emptyTitle="No account routers"
      emptyDescription="Create a router to direct requests across connected accounts."
    />
  )
}

export function AccountDetailPage({
  id,
  onBack,
  onEdit,
  onRemoved,
}: {
  id: string
  onBack: () => void
  onEdit: () => void
  onRemoved: () => void
}) {
  const [logoutOpen, setLogoutOpen] = useState(false)
  const query = useQuery({
    queryKey: ["account", id],
    queryFn: ({ signal }) => getAccount(id, signal),
    retry: false,
  })
  const account = query.data?.account
  const supportsLimits =
    account?.provider === "openai" || account?.provider === "github-copilot"
  const limitsQuery = useQuery({
    queryKey: ["account", id, "limits"],
    queryFn: getCodexAccountLimits,
    enabled: supportsLimits,
    retry: false,
  })
  const accountLimits = account
    ? findAccountLimits(account, limitsQuery.data?.accounts ?? [])
    : undefined
  const logout = useMutation({
    mutationFn: () => {
      if (!account) throw new Error("Account details are unavailable")
      return logoutOAuth(account.provider, account.account)
    },
    onSuccess: () => {
      setLogoutOpen(false)
      toast.success("Account removed.")
      onRemoved()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "The account was not removed.",
      )
    },
  })
  return (
    <>
      <CollectionDetailShell
        title={account ? accountTitle(account) : "Account"}
        identity={
          account ? (
            <span className="font-mono text-xs">{account.account}</span>
          ) : undefined
        }
        status={
          account ? (
            <Badge variant="outline">
              {formatAccountStatus(account.status)}
            </Badge>
          ) : undefined
        }
        loading={query.isLoading}
        error={detailError(query.error)}
        notFound={isNotFound(query.error)}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="All accounts"
        actions={
          account ? (
            <>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={onEdit}
              >
                <IconRefresh /> Renew
              </Button>
              <Button
                type="button"
                size="sm"
                variant="destructive"
                onClick={() => setLogoutOpen(true)}
              >
                <IconLogout /> Remove
              </Button>
            </>
          ) : undefined
        }
      >
        {account && (
          <div className="space-y-6">
            <AccountDetails account={account} />
            {supportsLimits && (
              <section>
                <h2 className="text-sm font-semibold">Usage limits</h2>
                <CodexAccountLimitSummary
                  account={accountLimits}
                  loading={limitsQuery.isLoading || limitsQuery.isFetching}
                  error={
                    limitsQuery.error instanceof Error
                      ? limitsQuery.error.message
                      : ""
                  }
                  apiError={limitsQuery.data?.error}
                  onRefresh={() => void limitsQuery.refetch()}
                />
              </section>
            )}
          </div>
        )}
      </CollectionDetailShell>
      <LogoutConfirmDialog
        open={logoutOpen}
        providerLabel={account ? accountTitle(account) : ""}
        isSubmitting={logout.isPending}
        onOpenChange={(open) => {
          if (!logout.isPending) setLogoutOpen(open)
        }}
        onConfirm={() => logout.mutate()}
      />
    </>
  )
}

export function AccountRouterDetailPage({
  id,
  onBack,
  onEdit,
}: {
  id: string
  onBack: () => void
  onEdit: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ["account-router", id],
    queryFn: ({ signal }) => getAccountRouter(id, signal),
    retry: false,
  })
  const router = query.data?.account_router
  const defaultMutation = useMutation({
    mutationFn: () => {
      if (!router) throw new Error("Router details are unavailable")
      if (!query.data?.config_revision) {
        throw new Error("Configuration revision is unavailable")
      }
      return setDefaultAccountRouter(router.id, query.data.config_revision)
    },
    onSuccess: async (response) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        `${router?.name ?? "Router"} is now the default account router.`,
        router?.name ?? "Account router",
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
      await query.refetch()
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : "The default router was not changed.",
      )
    },
  })
  return (
    <CollectionDetailShell
      title={router?.name ?? "Account router"}
      identity={<span className="font-mono text-xs">{id}</span>}
      status={
        router ? (
          <>
            <Badge variant="outline">
              {router.enabled ? "Enabled" : "Disabled"}
            </Badge>
            {router.is_default && <Badge>Default</Badge>}
          </>
        ) : undefined
      }
      loading={query.isLoading}
      error={detailError(query.error)}
      notFound={isNotFound(query.error)}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="All account routers"
      actions={
        router ? (
          <>
            {!router.is_default && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={
                  defaultMutation.isPending ||
                  !router.enabled ||
                  router.status !== "available"
                }
                onClick={() => defaultMutation.mutate()}
              >
                <IconStar /> Make default
              </Button>
            )}
            <Button type="button" size="sm" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
          </>
        ) : undefined
      }
    >
      {router && <AccountRouterDetails router={router} />}
    </CollectionDetailShell>
  )
}

function AccountDetails({ account }: { account: AccountSummary }) {
  return (
    <DetailList
      values={[
        ["Provider", providerLabel(account.provider)],
        ["Account reference", account.account],
        ["Status", formatAccountStatus(account.status)],
        ["Authentication", formatAuthMethod(account.auth_method)],
        ["Expires", formatAccountExpiry(account.expires_at)],
      ]}
    />
  )
}

function AccountRouterDetails({ router }: { router: AccountRouter }) {
  return (
    <div className="space-y-6">
      <DetailList
        values={[
          ["Name", router.name],
          ["Status", router.status || "—"],
          ["Entry block", router.entry || "—"],
          ["Accounts", String(router.accounts.length)],
          ["Blocks", String(router.blocks.length)],
          [
            "Refresh interval",
            router.refresh_interval_seconds
              ? `${router.refresh_interval_seconds} seconds`
              : "—",
          ],
        ]}
      />
      {router.blocks.length > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold">Decision blocks</h2>
          <div className="flex flex-wrap gap-2">
            {router.blocks.map((block) => (
              <Badge key={block.id} variant="outline" className="font-mono">
                {block.id} · {block.type}
              </Badge>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

function DetailList({ values }: { values: Array<[string, string]> }) {
  return (
    <dl className="divide-border overflow-hidden rounded-lg border text-sm">
      {values.map(([label, value]) => (
        <div
          key={label}
          className="grid gap-1 border-b p-3 last:border-b-0 sm:grid-cols-[12rem_1fr]"
        >
          <dt className="text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

function accountTitle(account: AccountSummary): string {
  const prefix = `${account.provider}:`
  const value = account.account.startsWith(prefix)
    ? account.account.slice(prefix.length)
    : account.account
  return value || providerLabel(account.provider)
}

function providerLabel(provider: string): string {
  if (provider === "openai") return "OpenAI"
  if (provider === "anthropic") return "Anthropic"
  if (provider === "google-antigravity") return "Google Antigravity"
  if (provider === "github-copilot") return "GitHub Copilot"
  return provider
}

function formatAuthMethod(method?: string): string {
  if (!method) return "—"
  if (method === "device_code") return "Device code"
  if (method === "browser") return "Browser OAuth"
  if (method === "token") return "Token"
  return method
}

function formatAccountStatus(status: AccountSummary["status"]): string {
  if (status === "needs_refresh") return "Needs refresh"
  if (status === "not_logged_in") return "Not logged in"
  return status.charAt(0).toUpperCase() + status.slice(1)
}

function formatAccountRouterStatus(status: string): string {
  if (status === "available") return "Available"
  if (status === "disabled") return "Disabled"
  if (status === "unconfigured") return "Unconfigured"
  if (status === "unreachable") return "Unreachable"
  if (status === "invalid") return "Invalid"
  return "Unknown"
}

function formatAccountExpiry(value?: string): string {
  if (!value) return "Does not expire"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date)
}

function findAccountLimits(
  account: AccountSummary,
  candidates: CodexAccountLimitAccount[],
): CodexAccountLimitAccount | undefined {
  const normalize = (value?: string) => value?.trim().toLowerCase() ?? ""
  const credential = normalize(account.account)
  const prefix = `${normalize(account.provider)}:`
  const suffix = credential.startsWith(prefix)
    ? credential.slice(prefix.length)
    : credential
  return candidates.find((candidate) => {
    if (
      candidate.provider &&
      normalize(candidate.provider) !== normalize(account.provider)
    ) {
      return false
    }
    const ids = [candidate.id, candidate.account_id, candidate.email].map(
      normalize,
    )
    return (
      ids.includes(credential) ||
      ids.includes(suffix) ||
      (candidate.default === true &&
        (suffix === "default" || credential === normalize(account.provider)))
    )
  })
}

function isNotFound(error: unknown): boolean {
  return error instanceof CollectionAPIError && error.status === 404
}

function detailError(error: unknown): string | undefined {
  if (!error || isNotFound(error)) return undefined
  return error instanceof Error ? error.message : String(error)
}
