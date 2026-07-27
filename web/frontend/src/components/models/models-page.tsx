import {
  IconDatabase,
  IconGitBranch,
  IconLoader2,
  IconPlus,
} from "@tabler/icons-react"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ModelInfo,
  type ModelProviderOption,
  getModels,
  setDefaultModel,
} from "@/api/models"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AddModelSheet } from "./add-model-sheet"
import { CatalogDialog } from "./catalog-dialog"
import { DeleteModelDialog } from "./delete-model-dialog"
import { EditModelSheet } from "./edit-model-sheet"
import { ModelCard } from "./model-card"
import { ModelRouterSheet } from "./model-router-sheet"

function isAccountRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function isModelRouterModel(model: ModelInfo): boolean {
  return model.provider === "model-router" || model.model_router != null
}

function isVisibleModel(model: ModelInfo): boolean {
  return (
    !isAccountRouterModel(model) && !model.model_name.startsWith("credential:")
  )
}

export function ModelsPage() {
  const { t } = useTranslation()
  const [models, setModels] = useState<ModelInfo[]>([])
  const [providerOptions, setProviderOptions] = useState<ModelProviderOption[]>(
    [],
  )
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [addOpen, setAddOpen] = useState(false)
  const [catalogOpen, setCatalogOpen] = useState(false)
  const [editingModel, setEditingModel] = useState<ModelInfo | null>(null)
  const [deletingModel, setDeletingModel] = useState<ModelInfo | null>(null)
  const [routerOpen, setRouterOpen] = useState(false)
  const [editingRouter, setEditingRouter] = useState<ModelInfo | null>(null)
  const [settingDefaultIndex, setSettingDefaultIndex] = useState<number | null>(
    null,
  )

  const fetchModels = useCallback(async () => {
    setLoading(true)
    try {
      const data = await getModels()
      setModels(
        [...data.models].sort((a, b) => {
          if (a.is_default && !b.is_default) return -1
          if (!a.is_default && b.is_default) return 1
          if (a.available && !b.available) return -1
          if (!a.available && b.available) return 1
          return a.model_name.localeCompare(b.model_name)
        }),
      )
      setProviderOptions(data.provider_options ?? [])
      setFetchError("")
    } catch (e) {
      setFetchError(e instanceof Error ? e.message : t("models.loadError"))
    } finally {
      setLoading(false)
    }
  }, [t])

  useEffect(() => {
    void fetchModels()
  }, [fetchModels])

  const visibleModels = useMemo(() => models.filter(isVisibleModel), [models])

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
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("models.loadError"))
    } finally {
      setSettingDefaultIndex(null)
    }
  }

  const handleEdit = (model: ModelInfo) => {
    if (isModelRouterModel(model)) {
      setEditingRouter(model)
      setRouterOpen(true)
      return
    }
    setEditingModel(model)
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.models")}>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => setCatalogOpen(true)}
            disabled={providerOptions.length === 0}
          >
            <IconDatabase className="size-4" />
            {t("models.catalog.button")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setRouterOpen(true)}
          >
            <IconGitBranch className="size-4" />
            {t("models.modelRouter.button")}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => setAddOpen(true)}
            disabled={providerOptions.length === 0}
          >
            <IconPlus className="size-4" />
            {t("models.add.button")}
          </Button>
        </div>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
        <p className="text-muted-foreground text-sm">
          {t("models.description")}
        </p>

        {loading && (
          <div className="flex items-center justify-center py-20">
            <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
          </div>
        )}

        {fetchError && (
          <div className="bg-destructive/10 mt-4 rounded px-4 py-3 text-sm">
            <p className="text-destructive">{fetchError}</p>
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

        {!loading && !fetchError && (
          <div className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {visibleModels.map((model) => (
              <ModelCard
                key={`${model.index}-${model.model_name}`}
                model={model}
                onEdit={handleEdit}
                onSetDefault={(item) => void handleSetDefault(item)}
                onDelete={setDeletingModel}
                settingDefault={settingDefaultIndex === model.index}
              />
            ))}
            {visibleModels.length === 0 && (
              <div className="text-muted-foreground py-12 text-sm">
                {t("models.empty", "No models configured.")}
              </div>
            )}
          </div>
        )}
      </div>

      <AddModelSheet
        open={addOpen}
        existingModelNames={models.map((item) => item.model_name)}
        providerOptions={providerOptions}
        onClose={() => setAddOpen(false)}
        onSaved={fetchModels}
      />
      <EditModelSheet
        open={editingModel != null}
        model={editingModel}
        providerOptions={providerOptions}
        onClose={() => setEditingModel(null)}
        onSaved={fetchModels}
      />
      <ModelRouterSheet
        open={routerOpen}
        model={editingRouter}
        models={models}
        onClose={() => {
          setRouterOpen(false)
          setEditingRouter(null)
        }}
        onSaved={fetchModels}
      />
      <DeleteModelDialog
        model={deletingModel}
        onClose={() => setDeletingModel(null)}
        onDeleted={fetchModels}
      />
      <CatalogDialog
        open={catalogOpen}
        providerOptions={providerOptions}
        onClose={() => setCatalogOpen(false)}
        onModelAdded={fetchModels}
      />
    </div>
  )
}
