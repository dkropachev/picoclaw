import {
  IconArrowsShuffle,
  IconBrandGoogle,
  IconBrandOpenai,
  IconEdit,
  IconGitBranch,
  IconKey,
  IconLoader2,
  IconPlus,
  IconRoute,
  IconSparkles,
  IconStar,
  IconStarFilled,
  IconTrash,
} from "@tabler/icons-react"
import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router"
import type { TFunction } from "i18next"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type ModelInfo, getModels, setDefaultModel } from "@/api/models"
import {
  type CodexAccountLimitAccount,
  type CodexAccountLimitsResponse,
  type OAuthProvider,
  type OAuthProviderStatus,
  getCodexAccountLimits,
} from "@/api/oauth"
import { DeleteModelDialog } from "@/components/models/delete-model-dialog"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useCredentialsPage } from "@/hooks/use-credentials-page"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AccountOnboardingSheet } from "./account-onboarding-sheet"
import { CodexAccountLimitSummary } from "./codex-account-limits-panel"
import { DeviceCodeSheet } from "./device-code-sheet"
import { LogoutConfirmDialog } from "./logout-confirm-dialog"
import { ProviderStatusLine } from "./provider-status-line"

function getAccountCredentialID(account: OAuthProviderStatus): string {
  return account.credential_id || account.provider
}

function getAccountName(
  account: OAuthProviderStatus,
  defaultLabel: string,
): string {
  const credentialID = getAccountCredentialID(account)
  const prefix = `${account.provider}:`
  if (credentialID.startsWith(prefix)) {
    return credentialID.slice(prefix.length)
  }
  return defaultLabel
}

function getAccountProviderLabel(
  account: OAuthProviderStatus,
  codexLimit?: CodexAccountLimitAccount,
): string {
  const parts = [account.display_name]
  if (account.auth_method) {
    parts.push(account.auth_method)
  }
  const subscription = codexLimit?.plan?.trim()
  if (account.provider === "openai" && subscription) {
    return `${parts.join(" ")} (${subscription})`
  }
  return parts.join(" ")
}

function normalizeAccountMatchKey(value: string | undefined): string {
  return value?.trim().toLowerCase() ?? ""
}

function appendMatchKey(keys: string[], value: string | undefined) {
  const key = normalizeAccountMatchKey(value)
  if (key && !keys.includes(key)) {
    keys.push(key)
  }
}

function appendAccountIndexKey(
  index: Map<string, OAuthProviderStatus>,
  account: OAuthProviderStatus,
  value: string | undefined,
) {
  const key = normalizeAccountMatchKey(value)
  if (key && !index.has(key)) {
    index.set(key, account)
  }
}

function stripProviderPrefix(provider: OAuthProvider, value: string): string {
  const prefix = `${provider}:`
  return value.startsWith(prefix) ? value.slice(prefix.length) : value
}

function stripCredentialRef(value: string): string {
  const prefix = "credential:"
  return value.startsWith(prefix) ? value.slice(prefix.length) : value
}

function formatRouterAccountRef(value: string): string {
  const stripped = stripCredentialRef(value.trim())
  return stripped || value
}

function getCodexLimitMatchKeys(account: CodexAccountLimitAccount): string[] {
  const keys: string[] = []
  appendMatchKey(keys, account.id)
  if (account.id && !account.id.includes(":")) {
    appendMatchKey(keys, `openai:${account.id}`)
  }
  appendMatchKey(keys, account.account_id)
  appendMatchKey(keys, account.email)
  if (account.default || account.id === "default" || account.id === "openai") {
    appendMatchKey(keys, "openai")
    appendMatchKey(keys, "openai:default")
  }
  return keys
}

function indexCodexLimits(
  data: CodexAccountLimitsResponse | null,
): Map<string, CodexAccountLimitAccount> {
  const index = new Map<string, CodexAccountLimitAccount>()
  for (const account of data?.accounts ?? []) {
    for (const key of getCodexLimitMatchKeys(account)) {
      if (!index.has(key)) {
        index.set(key, account)
      }
    }
  }
  return index
}

function getRegisteredAccountMatchKeys(account: OAuthProviderStatus): string[] {
  const keys: string[] = []
  const credentialID = getAccountCredentialID(account)
  appendMatchKey(keys, credentialID)
  appendMatchKey(keys, stripProviderPrefix(account.provider, credentialID))
  appendMatchKey(keys, account.account_id)
  appendMatchKey(keys, account.email)
  return keys
}

function getAccountCodexLimits(
  account: OAuthProviderStatus,
  limitsByKey: Map<string, CodexAccountLimitAccount>,
): CodexAccountLimitAccount | undefined {
  if (account.provider !== "openai") {
    return undefined
  }
  for (const key of getRegisteredAccountMatchKeys(account)) {
    const limits = limitsByKey.get(key)
    if (limits) {
      return limits
    }
  }
  return undefined
}

function indexRegisteredAccounts(
  accounts: OAuthProviderStatus[],
): Map<string, OAuthProviderStatus> {
  const index = new Map<string, OAuthProviderStatus>()
  for (const account of accounts) {
    const credentialID = getAccountCredentialID(account)
    const providerScopedName = stripProviderPrefix(
      account.provider,
      credentialID,
    )
    appendAccountIndexKey(index, account, credentialID)
    appendAccountIndexKey(index, account, providerScopedName)
    appendAccountIndexKey(index, account, `credential:${credentialID}`)
    appendAccountIndexKey(index, account, `credential:${providerScopedName}`)
    appendAccountIndexKey(index, account, account.account_id)
    appendAccountIndexKey(index, account, account.email)
  }
  return index
}

function getRouterAccountRefs(model: ModelInfo): string[] {
  const refs: string[] = []
  const appendRef = (value: string | undefined) => {
    const trimmed = value?.trim()
    if (trimmed && !refs.includes(trimmed)) {
      refs.push(trimmed)
    }
  }

  for (const block of model.router?.blocks ?? []) {
    appendRef(block.account)
    for (const account of block.accounts ?? []) {
      appendRef(account)
    }
  }

  return refs
}

type RouterAccountStatus = OAuthProviderStatus["status"] | "missing"

interface RouterAccountDetail {
  ref: string
  label: string
  status: RouterAccountStatus
}

function getRouterAccountLabel(
  account: OAuthProviderStatus | undefined,
  ref: string,
  t: TFunction,
): string {
  if (!account) {
    return formatRouterAccountRef(ref)
  }

  const defaultName = t("accounts.defaultName")
  const accountName = getAccountName(account, defaultName)
  return accountName === defaultName ? account.display_name : accountName
}

function getRouterAccountDetails(
  model: ModelInfo,
  accountIndex: Map<string, OAuthProviderStatus>,
  t: TFunction,
): RouterAccountDetail[] {
  return getRouterAccountRefs(model).map((ref) => {
    const account =
      accountIndex.get(normalizeAccountMatchKey(ref)) ??
      accountIndex.get(normalizeAccountMatchKey(stripCredentialRef(ref)))

    return {
      ref,
      label: getRouterAccountLabel(account, ref, t),
      status: account?.status ?? "missing",
    }
  })
}

function getRouterAccountStatusLabel(
  status: RouterAccountStatus,
  t: TFunction,
): string {
  if (status === "missing") {
    return t("models.router.accountMissing")
  }
  if (status === "connected") {
    return t("credentials.status.connected")
  }
  if (status === "needs_refresh") {
    return t("credentials.status.needsRefresh")
  }
  if (status === "expired") {
    return t("credentials.status.expired")
  }
  return t("credentials.status.notLoggedIn")
}

function getRouterAccountStatusStyle(status: RouterAccountStatus): string {
  if (status === "connected") {
    return "bg-green-500/10 text-green-700 dark:text-green-300"
  }
  if (status === "needs_refresh") {
    return "bg-amber-500/10 text-amber-700 dark:text-amber-300"
  }
  if (status === "expired" || status === "missing") {
    return "bg-destructive/10 text-destructive"
  }
  return "bg-muted text-muted-foreground"
}

function getRouterAccountStatusDot(status: RouterAccountStatus): string {
  if (status === "connected") {
    return "bg-green-500"
  }
  if (status === "needs_refresh") {
    return "bg-amber-500"
  }
  if (status === "expired" || status === "missing") {
    return "bg-destructive"
  }
  return "bg-muted-foreground/40"
}

function getRouterSummary(
  model: ModelInfo,
  statusLabel: string,
  t: TFunction,
): {
  mode: "fallback" | "load_balance"
  primary: string
  secondary: string
} {
  const entry = model.router?.blocks?.find(
    (block) => block.id === model.router?.entry,
  )
  const fallbackBlock = entry?.fallback
    ? model.router?.blocks?.find((block) => block.id === entry.fallback)
    : undefined
  const fallbackAccount =
    fallbackBlock?.type === "account" ? fallbackBlock.account : undefined

  if (entry?.type === "load_balance") {
    const strategy = entry.strategy || "blind"
    return {
      mode: "load_balance",
      primary: t("models.router.cardLoadBalance", {
        count: entry.accounts?.length ?? 0,
        strategy: t(`models.router.strategyName.${strategy}`),
      }),
      secondary: fallbackAccount
        ? t("models.router.cardFallbackTarget", {
            account: formatRouterAccountRef(fallbackAccount),
          })
        : t("models.router.cardNoFallback"),
    }
  }

  if (entry?.type === "account") {
    return {
      mode: "fallback",
      primary: t("models.router.cardFallback", {
        account: entry.account
          ? formatRouterAccountRef(entry.account)
          : t("models.router.cardUnconfigured"),
      }),
      secondary: fallbackAccount
        ? t("models.router.cardFallbackTarget", {
            account: formatRouterAccountRef(fallbackAccount),
          })
        : t("models.router.cardNoFallback"),
    }
  }

  return {
    mode: "fallback",
    primary: t("models.router.cardUnconfigured"),
    secondary: statusLabel,
  }
}

function ProviderIcon({ provider }: { provider: OAuthProvider }) {
  if (provider === "openai") {
    return <IconBrandOpenai className="size-4" />
  }
  if (provider === "google-antigravity") {
    return <IconBrandGoogle className="size-4" />
  }
  return <IconSparkles className="size-4" />
}

interface AccountCardProps {
  account: OAuthProviderStatus
  activeAction: string
  codexLimit?: CodexAccountLimitAccount
  codexLimitsLoading: boolean
  codexLimitsError: string
  codexLimitsApiError: string
  onRefreshCodexLimits: () => void
  onAskLogout: (provider: OAuthProvider, credentialID?: string) => void
}

function AccountCard({
  account,
  activeAction,
  codexLimit,
  codexLimitsLoading,
  codexLimitsError,
  codexLimitsApiError,
  onRefreshCodexLimits,
  onAskLogout,
}: AccountCardProps) {
  const { t } = useTranslation()
  const credentialID = getAccountCredentialID(account)
  const accountName = getAccountName(account, t("accounts.defaultName"))
  const providerLabel = getAccountProviderLabel(account, codexLimit)
  const actionBusy = activeAction !== ""
  const logoutLoading = activeAction === `${account.provider}:logout`

  return (
    <article className="bg-card rounded-lg border p-4">
      <div className="flex min-w-0 flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-3">
            <span className="border-muted inline-flex size-8 shrink-0 items-center justify-center rounded-lg border">
              <ProviderIcon provider={account.provider} />
            </span>
            <div className="min-w-0">
              <h3 className="truncate text-sm font-semibold">{accountName}</h3>
              <p className="text-muted-foreground truncate text-xs">
                {providerLabel}
              </p>
            </div>
          </div>

          <dl className="mt-4 grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
            <div className="min-w-0">
              <dt className="text-muted-foreground">
                {t("accounts.fields.credentialID")}
              </dt>
              <dd className="text-foreground truncate font-mono">
                {credentialID}
              </dd>
            </div>
            {account.account_id && (
              <div className="min-w-0">
                <dt className="text-muted-foreground">
                  {t("credentials.labels.account")}
                </dt>
                <dd className="text-foreground truncate font-mono">
                  {account.account_id}
                </dd>
              </div>
            )}
            {account.email && (
              <div className="min-w-0">
                <dt className="text-muted-foreground">
                  {t("credentials.labels.email")}
                </dt>
                <dd className="text-foreground truncate">{account.email}</dd>
              </div>
            )}
            {account.project_id && (
              <div className="min-w-0">
                <dt className="text-muted-foreground">
                  {t("credentials.labels.project")}
                </dt>
                <dd className="text-foreground truncate font-mono">
                  {account.project_id}
                </dd>
              </div>
            )}
          </dl>

          {account.provider === "openai" ? (
            <CodexAccountLimitSummary
              account={codexLimit}
              loading={codexLimitsLoading}
              error={codexLimitsError}
              apiError={codexLimitsApiError}
              onRefresh={onRefreshCodexLimits}
            />
          ) : null}
        </div>

        <div className="flex items-center gap-2 sm:shrink-0">
          <ProviderStatusLine status={account.status} />
          <Button
            variant="ghost"
            size="sm"
            disabled={actionBusy}
            onClick={() => onAskLogout(account.provider, credentialID)}
            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
          >
            {logoutLoading ? (
              <IconLoader2 className="size-4 animate-spin" />
            ) : (
              <IconTrash className="size-4" />
            )}
            {t("accounts.actions.remove")}
          </Button>
        </div>
      </div>
    </article>
  )
}

interface AccountRouterCardProps {
  model: ModelInfo
  accountIndex: Map<string, OAuthProviderStatus>
  onEdit: (model: ModelInfo) => void
  onSetDefault: (model: ModelInfo) => void
  onDelete: (model: ModelInfo) => void
  settingDefault: boolean
}

function AccountRouterCard({
  model,
  accountIndex,
  onEdit,
  onSetDefault,
  onDelete,
  settingDefault,
}: AccountRouterCardProps) {
  const { t } = useTranslation()
  const statusLabel = t(`models.status.${model.status}`)
  const routerSummary = getRouterSummary(model, statusLabel, t)
  const accountDetails = getRouterAccountDetails(model, accountIndex, t)
  const canSetDefault =
    model.available &&
    !model.is_default &&
    !model.is_virtual &&
    model.default_model_allowed !== false
  const setDefaultLabel = t("models.action.setDefault")
  const setDefaultDisabledReason = (() => {
    if (settingDefault) return t("models.action.setDefaultDisabled.setting")
    if (!model.available)
      return t("models.action.setDefaultDisabled.unavailable")
    if (model.is_default) return t("models.action.setDefaultDisabled.isDefault")
    if (model.is_virtual) return t("models.action.setDefaultDisabled.isVirtual")
    if (model.default_model_allowed === false) {
      return t("models.action.setDefaultDisabled.unsupportedProvider")
    }
    return setDefaultLabel
  })()
  const deleteDisabled = model.is_default
  const deleteLabel = t("models.router.actionDelete")
  const deleteDisabledReason = model.is_default
    ? t("models.action.deleteDisabled.isDefault")
    : deleteLabel

  return (
    <article
      className={[
        "bg-card rounded-lg border p-4",
        model.available ? "border-border" : "border-border/70 bg-card/60",
      ].join(" ")}
    >
      <div className="flex min-w-0 flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-3">
            <span className="border-primary/30 bg-primary/10 text-primary inline-flex size-8 shrink-0 items-center justify-center rounded-lg border">
              <IconRoute className="size-4" />
            </span>
            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-2">
                <h3 className="truncate text-sm font-semibold">
                  {model.model_name}
                </h3>
                {model.is_default && (
                  <span className="bg-primary/10 text-primary shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none font-medium">
                    {t("models.badge.default")}
                  </span>
                )}
              </div>
              <p className="text-muted-foreground truncate text-xs">
                {t("models.badge.router")} - {model.model}
              </p>
            </div>
          </div>

          <dl className="mt-4 grid gap-x-6 gap-y-2 text-xs sm:grid-cols-2">
            <div className="min-w-0">
              <dt className="text-muted-foreground">
                {t("models.router.sharedModel")}
              </dt>
              <dd className="text-foreground truncate font-mono">
                {model.model}
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">
                {t("models.router.connectedAccounts")}
              </dt>
              <dd className="text-foreground truncate">
                {accountDetails.length > 0
                  ? accountDetails.length
                  : t("models.router.cardNoAccounts")}
              </dd>
            </div>
          </dl>

          <div className="mt-4 space-y-2">
            <div className="space-y-1">
              <p className="text-muted-foreground truncate text-xs leading-snug">
                {routerSummary.primary}
              </p>
              <p className="text-muted-foreground/80 flex min-w-0 items-center gap-1 text-[11px] leading-snug">
                {routerSummary.mode === "load_balance" ? (
                  <IconArrowsShuffle className="size-3 shrink-0" />
                ) : (
                  <IconGitBranch className="size-3 shrink-0" />
                )}
                <span className="truncate">{routerSummary.secondary}</span>
              </p>
            </div>
            <div className="flex min-w-0 flex-wrap gap-1.5">
              {accountDetails.length > 0 ? (
                accountDetails.map((account) => {
                  const accountStatusLabel = getRouterAccountStatusLabel(
                    account.status,
                    t,
                  )
                  return (
                    <span
                      key={account.ref}
                      className={[
                        "inline-flex max-w-full items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium",
                        getRouterAccountStatusStyle(account.status),
                      ].join(" ")}
                      title={`${account.label}: ${accountStatusLabel}`}
                    >
                      <span
                        className={[
                          "size-1.5 shrink-0 rounded-full",
                          getRouterAccountStatusDot(account.status),
                        ].join(" ")}
                      />
                      <span className="truncate">
                        {account.label}: {accountStatusLabel}
                      </span>
                    </span>
                  )
                })
              ) : (
                <span className="text-muted-foreground text-[11px]">
                  {t("models.router.cardNoAccounts")}
                </span>
              )}
            </div>
          </div>
        </div>

        <div className="flex items-center gap-2 sm:shrink-0">
          <span className="bg-muted text-muted-foreground rounded px-2 py-1 text-xs font-medium">
            {statusLabel}
          </span>

          {model.is_default ? (
            <span
              className="text-primary p-1"
              title={t("models.badge.default")}
            >
              <IconStarFilled className="size-3.5" />
            </span>
          ) : (
            <Tooltip delayDuration={!canSetDefault || settingDefault ? 0 : 700}>
              <TooltipTrigger asChild>
                <span
                  className={
                    !canSetDefault || settingDefault
                      ? "cursor-not-allowed"
                      : undefined
                  }
                  tabIndex={!canSetDefault || settingDefault ? 0 : undefined}
                  role={!canSetDefault || settingDefault ? "button" : undefined}
                  aria-disabled={
                    !canSetDefault || settingDefault ? true : undefined
                  }
                  aria-label={
                    !canSetDefault || settingDefault
                      ? setDefaultLabel
                      : undefined
                  }
                  title={
                    !canSetDefault || settingDefault
                      ? setDefaultLabel
                      : undefined
                  }
                >
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => onSetDefault(model)}
                    disabled={settingDefault || !canSetDefault}
                    aria-label={setDefaultLabel}
                    title={setDefaultLabel}
                  >
                    {settingDefault ? (
                      <IconLoader2 className="size-3.5 animate-spin" />
                    ) : (
                      <IconStar className="size-3.5" />
                    )}
                  </Button>
                </span>
              </TooltipTrigger>
              <TooltipContent>{setDefaultDisabledReason}</TooltipContent>
            </Tooltip>
          )}

          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onEdit(model)}
            aria-label={t("models.router.actionEdit")}
            title={t("models.router.actionEdit")}
          >
            <IconEdit className="size-3.5" />
          </Button>

          <Tooltip delayDuration={deleteDisabled ? 0 : 700}>
            <TooltipTrigger asChild>
              <span
                className={deleteDisabled ? "cursor-not-allowed" : undefined}
                tabIndex={deleteDisabled ? 0 : undefined}
                role={deleteDisabled ? "button" : undefined}
                aria-disabled={deleteDisabled ? true : undefined}
                aria-label={deleteDisabled ? deleteLabel : undefined}
                title={deleteDisabled ? deleteLabel : undefined}
              >
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => onDelete(model)}
                  disabled={deleteDisabled}
                  aria-label={deleteLabel}
                  title={deleteLabel}
                  className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                >
                  <IconTrash className="size-3.5" />
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>{deleteDisabledReason}</TooltipContent>
          </Tooltip>
        </div>
      </div>
    </article>
  )
}

export function AccountsPage() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname.startsWith("/accounts/account-router/")) {
    return <Outlet />
  }

  return <AccountsHomePage />
}

function AccountsHomePage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [onboardingOpen, setOnboardingOpen] = useState(false)
  const [models, setModels] = useState<ModelInfo[]>([])
  const [modelsLoading, setModelsLoading] = useState(true)
  const [modelsError, setModelsError] = useState("")
  const [codexLimits, setCodexLimits] =
    useState<CodexAccountLimitsResponse | null>(null)
  const [codexLimitsLoading, setCodexLimitsLoading] = useState(true)
  const [codexLimitsError, setCodexLimitsError] = useState("")
  const [deletingRouter, setDeletingRouter] = useState<ModelInfo | null>(null)
  const [settingDefaultIndex, setSettingDefaultIndex] = useState<number | null>(
    null,
  )
  const {
    providers,
    loading,
    error,
    activeAction,
    activeFlow,
    flowHint,
    logoutDialogOpen,
    logoutConfirmProvider,
    logoutProviderLabel,
    deviceSheetOpen,
    deviceFlow,
    startBrowserOAuth,
    startOpenAIDeviceCode,
    saveToken,
    askLogout,
    handleConfirmLogout,
    handleLogoutDialogOpenChange,
    handleDeviceSheetOpenChange,
  } = useCredentialsPage()

  const registeredAccounts = useMemo(() => {
    return providers.flatMap((provider) => provider.credentials ?? [])
  }, [providers])

  const codexLimitsByKey = useMemo(
    () => indexCodexLimits(codexLimits),
    [codexLimits],
  )
  const registeredAccountsByKey = useMemo(
    () => indexRegisteredAccounts(registeredAccounts),
    [registeredAccounts],
  )

  const fetchModels = useCallback(async () => {
    setModelsLoading(true)
    try {
      const data = await getModels()
      setModels(data.models)
      setModelsError("")
    } catch (err) {
      setModelsError(err instanceof Error ? err.message : t("models.loadError"))
    } finally {
      setModelsLoading(false)
    }
  }, [t])

  const fetchCodexLimits = useCallback(async () => {
    setCodexLimitsLoading(true)
    try {
      const data = await getCodexAccountLimits()
      setCodexLimits(data)
      setCodexLimitsError("")
    } catch (err) {
      setCodexLimitsError(
        err instanceof Error
          ? err.message
          : t("credentials.codexLimits.loadFailed"),
      )
    } finally {
      setCodexLimitsLoading(false)
    }
  }, [t])

  useEffect(() => {
    void fetchModels()
  }, [fetchModels])

  useEffect(() => {
    void fetchCodexLimits()
  }, [fetchCodexLimits])

  const routers = models
    .filter((item) => item.provider === "router" || item.router != null)
    .sort((a, b) => {
      if (a.is_default && !b.is_default) return -1
      if (!a.is_default && b.is_default) return 1
      return a.model_name.localeCompare(b.model_name)
    })

  const hasAccountCards = registeredAccounts.length > 0 || routers.length > 0

  const handleAddRouter = () => {
    void navigate({ to: "/accounts/account-router/new" })
  }

  const handleSetDefault = async (model: ModelInfo) => {
    if (model.is_default) return

    setSettingDefaultIndex(model.index)
    try {
      await setDefaultModel(model.model_name)
      await fetchModels()
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.defaultChangeSuccess"),
        model.model_name,
        gateway?.restartRequired === true,
      )
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t("models.loadError"))
    } finally {
      setSettingDefaultIndex(null)
    }
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.accounts")}>
        <Button size="sm" variant="outline" onClick={handleAddRouter}>
          <IconRoute className="size-4" />
          {t("models.router.button")}
        </Button>
        <Button size="sm" onClick={() => setOnboardingOpen(true)}>
          <IconPlus className="size-4" />
          {t("accounts.actions.add")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        {error && (
          <div className="text-destructive bg-destructive/10 mt-4 rounded-lg px-4 py-3 text-sm">
            {error}
          </div>
        )}

        {activeFlow && (
          <div className="bg-muted mt-4 rounded-lg border px-4 py-3 text-sm">
            <p className="font-medium">{t("credentials.flow.current")}</p>
            <p className="text-muted-foreground mt-1">{flowHint}</p>
          </div>
        )}

        <section className="py-5">
          {loading ? (
            <div className="text-muted-foreground flex items-center gap-2 py-10 text-sm">
              <IconLoader2 className="size-4 animate-spin" />
              {t("accounts.loading")}
            </div>
          ) : hasAccountCards || modelsLoading || modelsError ? (
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
              {registeredAccounts.map((account) => (
                <AccountCard
                  key={getAccountCredentialID(account)}
                  account={account}
                  activeAction={activeAction}
                  codexLimit={getAccountCodexLimits(account, codexLimitsByKey)}
                  codexLimitsLoading={codexLimitsLoading}
                  codexLimitsError={codexLimitsError}
                  codexLimitsApiError={codexLimits?.error ?? ""}
                  onRefreshCodexLimits={() => void fetchCodexLimits()}
                  onAskLogout={askLogout}
                />
              ))}

              {modelsLoading && (
                <article className="bg-card text-muted-foreground flex min-h-32 items-center gap-2 rounded-lg border p-4 text-sm">
                  <IconLoader2 className="size-4 animate-spin" />
                  {t("models.router.loading")}
                </article>
              )}

              {modelsError && (
                <article className="bg-destructive/10 rounded-lg border p-4 text-sm xl:col-span-2">
                  <p className="text-destructive">{modelsError}</p>
                  <Button
                    size="sm"
                    variant="outline"
                    className="mt-3"
                    onClick={() => void fetchModels()}
                  >
                    {t("models.retry")}
                  </Button>
                </article>
              )}

              {!modelsLoading &&
                !modelsError &&
                routers.map((router) => (
                  <AccountRouterCard
                    key={router.index}
                    model={router}
                    accountIndex={registeredAccountsByKey}
                    onEdit={(item) => {
                      void navigate({
                        to: "/accounts/account-router/$index",
                        params: { index: String(item.index) },
                      })
                    }}
                    onSetDefault={(item) => void handleSetDefault(item)}
                    onDelete={setDeletingRouter}
                    settingDefault={settingDefaultIndex === router.index}
                  />
                ))}
            </div>
          ) : (
            <div className="flex min-h-64 items-center justify-center">
              <div className="border-border/70 bg-card max-w-sm rounded-lg border p-6 text-center">
                <div className="border-muted mx-auto flex size-10 items-center justify-center rounded-lg border">
                  <IconKey className="size-5" />
                </div>
                <h3 className="mt-4 text-sm font-semibold">
                  {t("accounts.empty.title")}
                </h3>
                <p className="text-muted-foreground mt-2 text-sm">
                  {t("accounts.empty.description")}
                </p>
                <Button
                  size="sm"
                  className="mt-4"
                  onClick={() => setOnboardingOpen(true)}
                >
                  <IconPlus className="size-4" />
                  {t("accounts.actions.add")}
                </Button>
              </div>
            </div>
          )}
        </section>
      </div>

      <DeleteModelDialog
        model={deletingRouter}
        onClose={() => setDeletingRouter(null)}
        onDeleted={fetchModels}
      />

      <AccountOnboardingSheet
        open={onboardingOpen}
        providers={providers}
        registeredAccounts={registeredAccounts}
        activeAction={activeAction}
        onOpenChange={setOnboardingOpen}
        onStartBrowserOAuth={startBrowserOAuth}
        onStartDeviceCode={startOpenAIDeviceCode}
        onSaveToken={saveToken}
      />

      <LogoutConfirmDialog
        open={logoutDialogOpen}
        providerLabel={logoutProviderLabel}
        isSubmitting={activeAction === `${logoutConfirmProvider}:logout`}
        onOpenChange={handleLogoutDialogOpenChange}
        onConfirm={handleConfirmLogout}
      />

      <DeviceCodeSheet
        open={deviceSheetOpen}
        flow={deviceFlow}
        flowHint={flowHint}
        onOpenChange={handleDeviceSheetOpenChange}
      />
    </div>
  )
}
