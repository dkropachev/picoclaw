import { IconLoader2, IconPlus } from "@tabler/icons-react"
import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router"
import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type ModelInfo, getModels, setDefaultModel } from "@/api/models"
import { DeleteModelDialog } from "@/components/models/delete-model-dialog"
import { ModelCard } from "@/components/models/model-card"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { useCredentialsPage } from "@/hooks/use-credentials-page"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AnthropicCredentialCard } from "./anthropic-credential-card"
import { AntigravityCredentialCard } from "./antigravity-credential-card"
import { DeviceCodeSheet } from "./device-code-sheet"
import { LogoutConfirmDialog } from "./logout-confirm-dialog"
import { OpenAICredentialCard } from "./openai-credential-card"

export function CredentialsPage() {
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })

  if (pathname.startsWith("/credentials/account-router/")) {
    return <Outlet />
  }

  return <CredentialsHomePage />
}

function CredentialsHomePage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [models, setModels] = useState<ModelInfo[]>([])
  const [modelsLoading, setModelsLoading] = useState(true)
  const [modelsError, setModelsError] = useState("")
  const [deletingRouter, setDeletingRouter] = useState<ModelInfo | null>(null)
  const [settingDefaultIndex, setSettingDefaultIndex] = useState<number | null>(
    null,
  )
  const {
    loading,
    error,
    activeAction,
    activeFlow,
    flowHint,
    openAIToken,
    openAICredentialID,
    anthropicToken,
    openaiStatus,
    anthropicStatus,
    antigravityStatus,
    logoutDialogOpen,
    logoutConfirmProvider,
    logoutProviderLabel,
    deviceSheetOpen,
    deviceFlow,
    setOpenAIToken,
    setOpenAICredentialID,
    setAnthropicToken,
    startBrowserOAuth,
    startOpenAIDeviceCode,
    stopLoading,
    saveToken,
    askLogout,
    handleConfirmLogout,
    handleLogoutDialogOpenChange,
    handleDeviceSheetOpenChange,
  } = useCredentialsPage()

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
    void navigate({ to: "/credentials/account-router/new" })
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
      <PageHeader title={t("navigation.credentials")}>
        <Button size="sm" variant="outline" onClick={handleAddRouter}>
          <IconPlus className="size-4" />
          {t("models.router.button")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 sm:px-6">
        <div className="pt-2">
          <p className="text-muted-foreground text-sm">
            {t("credentials.description")}
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

        {loading ? (
          <div className="text-muted-foreground flex items-center gap-2 py-10 text-sm">
            <IconLoader2 className="size-4 animate-spin" />
            {t("credentials.loading")}
          </div>
        ) : (
          <>
            <div className="grid grid-cols-1 gap-4 py-5 lg:auto-rows-fr lg:grid-cols-3">
              <OpenAICredentialCard
                status={openaiStatus}
                activeAction={activeAction}
                token={openAIToken}
                credentialID={openAICredentialID}
                onTokenChange={setOpenAIToken}
                onCredentialIDChange={setOpenAICredentialID}
                onStartBrowserOAuth={() =>
                  void startBrowserOAuth(
                    "openai",
                    openAICredentialID.trim() || undefined,
                  )
                }
                onStartDeviceCode={() =>
                  void startOpenAIDeviceCode(
                    openAICredentialID.trim() || undefined,
                  )
                }
                onStopLoading={stopLoading}
                onSaveToken={() =>
                  void saveToken(
                    "openai",
                    openAIToken.trim(),
                    openAICredentialID.trim() || undefined,
                  )
                }
                onAskLogout={() =>
                  askLogout("openai", openAICredentialID.trim() || undefined)
                }
              />

              <AnthropicCredentialCard
                status={anthropicStatus}
                activeAction={activeAction}
                token={anthropicToken}
                onTokenChange={setAnthropicToken}
                onStopLoading={stopLoading}
                onSaveToken={() =>
                  void saveToken("anthropic", anthropicToken.trim())
                }
                onAskLogout={() => askLogout("anthropic")}
              />

              <AntigravityCredentialCard
                status={antigravityStatus}
                activeAction={activeAction}
                onStopLoading={stopLoading}
                onStartBrowserOAuth={() =>
                  void startBrowserOAuth("google-antigravity")
                }
                onAskLogout={() => askLogout("google-antigravity")}
              />
            </div>

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
                          to: "/credentials/account-router/$index",
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
          </>
        )}
      </div>

      <DeleteModelDialog
        model={deletingRouter}
        onClose={() => setDeletingRouter(null)}
        onDeleted={fetchModels}
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
