import {
  IconDatabase,
  IconEdit,
  IconGitBranch,
  IconLoader2,
  IconPlus,
  IconTrash,
} from "@tabler/icons-react"
import { useCallback, useEffect, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ModelAlias,
  type ModelAliasCatalogEntry,
  type ModelInfo,
  type ModelProviderOption,
  getModels,
  setDefaultAccount,
  setDefaultModelAlias,
  setDefaultSelection,
} from "@/api/models"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AddModelSheet } from "./add-model-sheet"
import { CatalogDialog } from "./catalog-dialog"
import { DeleteModelAliasDialog } from "./delete-model-alias-dialog"
import { DeleteModelDialog } from "./delete-model-dialog"
import { EditModelSheet } from "./edit-model-sheet"
import { ModelAliasSheet } from "./model-alias-sheet"
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
  const [modelAliases, setModelAliases] = useState<ModelAlias[]>([])
  const [modelAliasCatalog, setModelAliasCatalog] = useState<
    ModelAliasCatalogEntry[]
  >([])
  const [defaultAccountRef, setDefaultAccountRef] = useState("")
  const [defaultModelName, setDefaultModelName] = useState("")
  const [configRevision, setConfigRevision] = useState("")
  const [draftAccountRef, setDraftAccountRef] = useState("")
  const [draftModelName, setDraftModelName] = useState("")
  const [providerOptions, setProviderOptions] = useState<ModelProviderOption[]>(
    [],
  )
  const [loading, setLoading] = useState(true)
  const [fetchError, setFetchError] = useState("")
  const [addOpen, setAddOpen] = useState(false)
  const [catalogOpen, setCatalogOpen] = useState(false)
  const [editingModel, setEditingModel] = useState<ModelInfo | null>(null)
  const [deletingModel, setDeletingModel] = useState<ModelInfo | null>(null)
  const [aliasOpen, setAliasOpen] = useState(false)
  const [editingAliasIndex, setEditingAliasIndex] = useState<number | null>(
    null,
  )
  const [presetAliasName, setPresetAliasName] = useState("")
  const [deletingAliasIndex, setDeletingAliasIndex] = useState<number | null>(
    null,
  )
  const [routerOpen, setRouterOpen] = useState(false)
  const [editingRouter, setEditingRouter] = useState<ModelInfo | null>(null)
  const [settingDefaultIndex, setSettingDefaultIndex] = useState<number | null>(
    null,
  )
  const [savingDefaultSelection, setSavingDefaultSelection] = useState(false)

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
      setModelAliases(data.model_aliases ?? [])
      setModelAliasCatalog(data.model_alias_catalog ?? [])
      setDefaultAccountRef(data.default_account_ref ?? "")
      setDefaultModelName(data.default_model ?? "")
      setConfigRevision(data.revision ?? "")
      setDraftAccountRef(data.default_account_ref ?? "")
      setDraftModelName(data.default_model ?? "")
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
  const concreteAccountRefs = useMemo(
    () =>
      [
        ...new Set(
          models
            .filter(
              (model) =>
                !isAccountRouterModel(model) && !isModelRouterModel(model),
            )
            .map((model) => model.model_name.trim())
            .filter(Boolean),
        ),
      ].sort((a, b) => a.localeCompare(b)),
    [models],
  )
  const selectableAccountRefs = useMemo(
    () =>
      [
        ...new Set(
          models
            .filter(
              (model) => model.enabled !== false && !isModelRouterModel(model),
            )
            .map((model) => model.model_name.trim())
            .filter(Boolean),
        ),
      ].sort((a, b) => a.localeCompare(b)),
    [models],
  )
  const selectableModelNames = useMemo(
    () => [
      ...modelAliases.map((alias) => alias.name),
      ...models
        .filter((model) => isModelRouterModel(model) && model.enabled !== false)
        .map((model) => model.model_name)
        .filter((name) => !modelAliases.some((alias) => alias.name === name)),
    ],
    [modelAliases, models],
  )
  const editingAlias =
    editingAliasIndex == null
      ? presetAliasName
        ? { name: presetAliasName, model: "" }
        : null
      : (modelAliases[editingAliasIndex] ?? null)
  const deletingAlias =
    deletingAliasIndex == null
      ? null
      : (modelAliases[deletingAliasIndex] ?? null)
  const defaultSelectionChanged =
    draftAccountRef !== defaultAccountRef || draftModelName !== defaultModelName
  const catalogAliasNames = useMemo(
    () => new Set(modelAliasCatalog.map((entry) => entry.name)),
    [modelAliasCatalog],
  )
  const customAliases = useMemo(
    () =>
      modelAliases
        .map((alias, index) => ({ alias, index }))
        .filter(({ alias }) => !catalogAliasNames.has(alias.name)),
    [catalogAliasNames, modelAliases],
  )

  const openAliasEditor = (index: number | null, presetName = "") => {
    setEditingAliasIndex(index)
    setPresetAliasName(presetName)
    setAliasOpen(true)
  }

  const renderAliasCard = (
    name: string,
    description: string,
    alias: ModelAlias | null,
    index: number | null,
  ) => (
    <article key={name} className="border-border bg-card rounded-xl border p-4">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <h3 className="truncate text-sm font-semibold">{name}</h3>
            {name === defaultModelName && (
              <span className="bg-primary/10 text-primary rounded px-1.5 py-0.5 text-[10px] font-medium">
                {t("models.badge.default")}
              </span>
            )}
          </div>
          {description && (
            <p className="text-muted-foreground mt-1 text-xs">{description}</p>
          )}
          <p
            className={`mt-2 truncate font-mono text-xs ${
              alias ? "text-foreground" : "text-muted-foreground"
            }`}
          >
            {alias?.model || t("models.alias.notConfigured", "Not configured")}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            size="icon-sm"
            variant="ghost"
            onClick={() =>
              openAliasEditor(index, catalogAliasNames.has(name) ? name : "")
            }
            aria-label={
              alias
                ? t("models.alias.edit", "Edit model alias")
                : t("models.alias.configure", "Configure model alias")
            }
            title={
              alias
                ? t("models.alias.edit", "Edit model alias")
                : t("models.alias.configure", "Configure model alias")
            }
          >
            <IconEdit className="size-4" />
          </Button>
          {alias && index != null && (
            <Button
              size="icon-sm"
              variant="ghost"
              disabled={name === defaultModelName}
              onClick={() => setDeletingAliasIndex(index)}
              aria-label={t("models.alias.delete", "Delete model alias")}
              title={
                name === defaultModelName
                  ? t(
                      "models.alias.deleteDefault",
                      "Change the default alias before deleting it.",
                    )
                  : t("models.alias.delete", "Delete model alias")
              }
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <IconTrash className="size-4" />
            </Button>
          )}
        </div>
      </div>
      {alias && (
        <div className="mt-3 space-y-1.5">
          {Object.entries(alias.account_overrides ?? {}).map(
            ([accountRef, overrideModel]) => (
              <div
                key={accountRef}
                className="bg-muted/60 grid min-w-0 grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-1 rounded px-2 py-1 font-mono text-[11px]"
              >
                <span className="truncate">{accountRef}</span>
                <span className="text-muted-foreground">→</span>
                <span className="truncate text-right">{overrideModel}</span>
              </div>
            ),
          )}
          {Object.keys(alias.account_overrides ?? {}).length === 0 && (
            <p className="text-muted-foreground text-xs">
              {t("models.alias.baseForAll", "Base model for every account")}
            </p>
          )}
        </div>
      )}
    </article>
  )

  const handleSetDefault = async (model: ModelInfo) => {
    const modelIsDefault = isModelRouterModel(model)
      ? model.model_name === defaultModelName
      : model.model_name === defaultAccountRef
    if (modelIsDefault) return
    setSettingDefaultIndex(model.index)
    try {
      if (isModelRouterModel(model)) {
        await setDefaultModelAlias(model.model_name)
      } else {
        await setDefaultAccount(model.model_name)
      }
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

  const handleSaveDefaultSelection = async () => {
    if (!draftAccountRef || !draftModelName || !defaultSelectionChanged) return
    setSavingDefaultSelection(true)
    try {
      await setDefaultSelection(draftAccountRef, draftModelName)
      await fetchModels()
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("models.defaultChangeSuccess"),
        `${draftAccountRef} / ${draftModelName}`,
        gateway?.restartRequired === true,
      )
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t("models.loadError"),
      )
    } finally {
      setSavingDefaultSelection(false)
    }
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
          <div className="mt-4 space-y-7">
            <section className="border-border bg-card rounded-xl border p-4">
              <div className="flex flex-col gap-4 lg:flex-row lg:items-end">
                <div className="min-w-0 flex-1">
                  <h2 className="text-sm font-semibold">
                    {t("models.defaultSelection.title", "Default selection")}
                  </h2>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {defaultAccountRef && defaultModelName
                      ? t(
                          "models.defaultSelection.current",
                          "Current: {{account}} / {{alias}}",
                          {
                            account: defaultAccountRef,
                            alias: defaultModelName,
                          },
                        )
                      : t(
                          "models.defaultSelection.none",
                          "No model configured. Choose both an account and a model alias.",
                        )}
                  </p>
                </div>
                <div className="grid min-w-0 flex-[2] gap-2 sm:grid-cols-2 lg:max-w-2xl">
                  <Select
                    value={draftAccountRef}
                    onValueChange={setDraftAccountRef}
                  >
                    <SelectTrigger
                      aria-label={t(
                        "models.defaultSelection.account",
                        "Default account",
                      )}
                    >
                      <SelectValue
                        placeholder={t(
                          "models.defaultSelection.selectAccount",
                          "Select account",
                        )}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {selectableAccountRefs.map((accountRef) => (
                        <SelectItem key={accountRef} value={accountRef}>
                          {accountRef}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Select
                    value={draftModelName}
                    onValueChange={setDraftModelName}
                  >
                    <SelectTrigger
                      aria-label={t(
                        "models.defaultSelection.alias",
                        "Default model alias",
                      )}
                    >
                      <SelectValue
                        placeholder={t(
                          "models.defaultSelection.selectAlias",
                          "Select model alias",
                        )}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {selectableModelNames.map((name) => (
                        <SelectItem key={name} value={name}>
                          {name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <Button
                  size="sm"
                  onClick={() => void handleSaveDefaultSelection()}
                  disabled={
                    savingDefaultSelection ||
                    !draftAccountRef ||
                    !draftModelName ||
                    !defaultSelectionChanged
                  }
                >
                  {savingDefaultSelection && (
                    <IconLoader2 className="size-4 animate-spin" />
                  )}
                  {t("models.defaultSelection.save", "Save default")}
                </Button>
              </div>
            </section>

            <section>
              <div>
                <h2 className="text-sm font-semibold">
                  {t("models.alias.developerTitle", "Developer aliases")}
                </h2>
                <p className="text-muted-foreground mt-1 text-xs">
                  {t(
                    "models.alias.developerDescription",
                    "Stable task roles used by chat, agents, and workflows. Assign models explicitly; no role has an implicit default.",
                  )}
                </p>
              </div>

              <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {modelAliasCatalog.map((entry) => {
                  const index = modelAliases.findIndex(
                    (alias) => alias.name === entry.name,
                  )
                  return renderAliasCard(
                    entry.name,
                    entry.description,
                    index >= 0 ? modelAliases[index] : null,
                    index >= 0 ? index : null,
                  )
                })}
              </div>
            </section>

            {(customAliases.length > 0 || modelAliasCatalog.length > 0) && (
              <section>
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <h2 className="text-sm font-semibold">
                      {t("models.alias.customTitle", "Custom aliases")}
                    </h2>
                    <p className="text-muted-foreground mt-1 text-xs">
                      {t(
                        "models.alias.customDescription",
                        "Optional specialized roles for capabilities outside the standard developer set.",
                      )}
                    </p>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => openAliasEditor(null)}
                  >
                    <IconPlus className="size-4" />
                    {t("models.alias.add", "Add alias")}
                  </Button>
                </div>
                {customAliases.length > 0 && (
                  <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                    {customAliases.map(({ alias, index }) =>
                      renderAliasCard(alias.name, "", alias, index),
                    )}
                  </div>
                )}
              </section>
            )}

            <section>
              <h2 className="text-sm font-semibold">
                {t("models.accountsTitle", "Provider accounts")}
              </h2>
              <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
                {visibleModels.map((model) => (
                  <ModelCard
                    key={`${model.index}-${model.model_name}`}
                    model={model}
                    onEdit={handleEdit}
                    onSetDefault={(item) => void handleSetDefault(item)}
                    onDelete={setDeletingModel}
                    settingDefault={settingDefaultIndex === model.index}
                    defaultChangePending={settingDefaultIndex !== null}
                    isDefault={
                      isModelRouterModel(model)
                        ? model.model_name === defaultModelName
                        : model.model_name === defaultAccountRef
                    }
                  />
                ))}
                {visibleModels.length === 0 && (
                  <div className="text-muted-foreground py-12 text-sm">
                    {t("models.empty", "No provider accounts configured.")}
                  </div>
                )}
              </div>
            </section>
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
        revision={configRevision}
        providerOptions={providerOptions}
        onClose={() => setEditingModel(null)}
        onSaved={fetchModels}
      />
      <ModelRouterSheet
        open={routerOpen}
        model={editingRouter}
        revision={configRevision}
        models={models}
        modelAliases={modelAliases}
        defaultModelName={defaultModelName}
        onClose={() => {
          setRouterOpen(false)
          setEditingRouter(null)
        }}
        onSaved={fetchModels}
      />
      <DeleteModelDialog
        model={deletingModel}
        revision={configRevision}
        onClose={() => setDeletingModel(null)}
        onDeleted={fetchModels}
      />
      <CatalogDialog
        open={catalogOpen}
        providerOptions={providerOptions}
        onClose={() => setCatalogOpen(false)}
        onModelAdded={fetchModels}
      />
      <ModelAliasSheet
        open={aliasOpen}
        alias={editingAlias}
        aliasIndex={editingAliasIndex}
        nameLocked={presetAliasName !== ""}
        revision={configRevision}
        existingNames={modelAliases.map((alias) => alias.name)}
        concreteAccountRefs={concreteAccountRefs}
        onClose={() => {
          setAliasOpen(false)
          setEditingAliasIndex(null)
          setPresetAliasName("")
        }}
        onSaved={fetchModels}
      />
      <DeleteModelAliasDialog
        alias={deletingAlias}
        aliasIndex={deletingAliasIndex}
        revision={configRevision}
        onClose={() => setDeletingAliasIndex(null)}
        onDeleted={fetchModels}
      />
    </div>
  )
}
