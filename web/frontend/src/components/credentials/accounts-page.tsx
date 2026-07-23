import {
  IconBrandGoogle,
  IconBrandOpenai,
  IconKey,
  IconLoader2,
  IconPlus,
  IconRoute,
  IconSparkles,
  IconTrash,
} from "@tabler/icons-react"
import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type ModelInfo, getModels, setDefaultModel } from "@/api/models"
import type { OAuthProvider, OAuthProviderStatus } from "@/api/oauth"
import { DeleteModelDialog } from "@/components/models/delete-model-dialog"
import { ModelCard } from "@/components/models/model-card"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { useCredentialsPage } from "@/hooks/use-credentials-page"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AccountOnboardingSheet } from "./account-onboarding-sheet"
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
  onAskLogout: (provider: OAuthProvider, credentialID?: string) => void
}

function AccountCard({ account, activeAction, onAskLogout }: AccountCardProps) {
  const { t } = useTranslation()
  const credentialID = getAccountCredentialID(account)
  const accountName = getAccountName(account, t("accounts.defaultName"))
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
                {account.display_name}
              </p>
            </div>
          </div>

          <div className="mt-4">
            <ProviderStatusLine
              status={account.status}
              authMethod={account.auth_method}
            />
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
        </div>

        <Button
          variant="ghost"
          size="sm"
          disabled={actionBusy}
          onClick={() => onAskLogout(account.provider, credentialID)}
          className="text-destructive hover:bg-destructive/10 hover:text-destructive sm:shrink-0"
        >
          {logoutLoading ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconTrash className="size-4" />
          )}
          {t("accounts.actions.remove")}
        </Button>
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

  useEffect(() => {
    void fetchModels()
  }, [fetchModels])

  const routers = models
    .filter((item) => item.provider === "router" || item.router != null)
    .sort((a, b) => {
      if (a.is_default && !b.is_default) return -1
      if (!a.is_default && b.is_default) return 1
      return a.model_name.localeCompare(b.model_name)
    })

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
        <div className="pt-2">
          <p className="text-muted-foreground text-sm">
            {t("accounts.description")}
          </p>
        </div>

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
          ) : registeredAccounts.length > 0 ? (
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
              {registeredAccounts.map((account) => (
                <AccountCard
                  key={getAccountCredentialID(account)}
                  account={account}
                  activeAction={activeAction}
                  onAskLogout={askLogout}
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

        <section className="border-border/70 border-t py-5">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold">
                {t("models.router.sectionTitle")}
              </h3>
              <p className="text-muted-foreground mt-1 text-xs">
                {t("models.router.sectionDescription")}
              </p>
            </div>
            {modelsLoading && (
              <IconLoader2 className="text-muted-foreground size-4 animate-spin" />
            )}
          </div>

          {modelsError && (
            <div className="bg-destructive/10 rounded-lg px-4 py-3 text-sm">
              <p className="text-destructive">{modelsError}</p>
              <Button
                size="sm"
                variant="outline"
                className="mt-3"
                onClick={() => void fetchModels()}
              >
                {t("models.retry")}
              </Button>
            </div>
          )}

          {!modelsLoading && !modelsError && routers.length === 0 && (
            <p className="text-muted-foreground text-sm">
              {t("models.router.empty")}
            </p>
          )}

          {!modelsLoading && !modelsError && routers.length > 0 && (
            <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
              {routers.map((router) => (
                <ModelCard
                  key={router.index}
                  model={router}
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
