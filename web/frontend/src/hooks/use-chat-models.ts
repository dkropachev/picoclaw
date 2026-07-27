import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ModelInfo,
  type UpstreamModel,
  fetchUpstreamModels,
  getModels,
  setDefaultModel,
} from "@/api/models"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

interface UseChatModelsOptions {
  isConnected: boolean
}

export interface ChatAccountOption {
  accountName: string
  label: string
  provider?: string
  authMethod?: string
  credentialID?: string
  modelID?: string
  modelIndex?: number
}

function isAccountRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function isModelRouterModel(model: ModelInfo): boolean {
  return model.provider === "model-router" || model.model_router != null
}

function isCredentialAccountModel(model: ModelInfo): boolean {
  return model.model_name.startsWith("credential:")
}

function credentialAccountName(model: ModelInfo): string {
  if (isCredentialAccountModel(model)) {
    return model.model_name
  }
  const credentialID = model.credential_id?.trim()
  if (!credentialID) {
    return ""
  }
  return `credential:${credentialID.toLowerCase()}`
}

function displayAccountLabel(accountName: string): string {
  return accountName.startsWith("credential:")
    ? accountName.slice("credential:".length)
    : accountName
}

function modelOption(id: string): UpstreamModel | null {
  const trimmed = id.trim()
  return trimmed ? { id: trimmed } : null
}

export function useChatModels({ isConnected }: UseChatModelsOptions) {
  const { t } = useTranslation()
  const [modelList, setModelList] = useState<ModelInfo[]>([])
  const [defaultModelName, setDefaultModelName] = useState("")
  const [selectedAccountName, setSelectedAccountName] = useState("")
  const [selectedModelID, setSelectedModelID] = useState("")
  const [modelOptions, setModelOptions] = useState<UpstreamModel[]>([])
  const [isLoadingModelOptions, setIsLoadingModelOptions] = useState(false)
  const setDefaultRequestIdRef = useRef(0)

  const syncDefaultModelName = useCallback(
    (models: ModelInfo[], defaultModel: string) => {
      if (models.some((m) => m.model_name === defaultModel)) {
        setDefaultModelName(defaultModel)
        return
      }
      setDefaultModelName("")
    },
    [],
  )

  const loadModels = useCallback(async () => {
    try {
      const data = await getModels()
      setModelList(data.models)
      syncDefaultModelName(data.models, data.default_model)
    } catch {
      // silently fail
    }
  }, [syncDefaultModelName])

  useEffect(() => {
    const timerId = setTimeout(() => {
      void loadModels()
    }, 0)

    return () => clearTimeout(timerId)
  }, [isConnected, loadModels])

  const handleSetDefault = useCallback(
    async (modelName: string) => {
      if (modelName === defaultModelName) return
      const requestId = ++setDefaultRequestIdRef.current

      try {
        await setDefaultModel(modelName)
        const data = await getModels()
        if (requestId !== setDefaultRequestIdRef.current) {
          return
        }

        setModelList(data.models)
        syncDefaultModelName(data.models, data.default_model)
        const gateway = await refreshGatewayState({ force: true })
        showSaveSuccessOrRestartToast(
          t,
          t("models.defaultChangeSuccess"),
          modelName,
          gateway?.restartRequired === true,
        )
      } catch (err) {
        console.error("Failed to set default model:", err)
        toast.error(err instanceof Error ? err.message : t("models.loadError"))
      }
    },
    [defaultModelName, syncDefaultModelName, t],
  )

  const defaultSelectableModels = useMemo(
    () =>
      modelList.filter(
        (m) =>
          m.default_model_allowed !== false &&
          (m.is_virtual !== true ||
            isAccountRouterModel(m) ||
            isModelRouterModel(m) ||
            isCredentialAccountModel(m)),
      ),
    [modelList],
  )

  const accountModels = useMemo(() => {
    const byAccount = new Map<string, ChatAccountOption>()
    for (const model of defaultSelectableModels) {
      if (isAccountRouterModel(model) || isModelRouterModel(model)) {
        continue
      }
      const accountName = credentialAccountName(model)
      if (!accountName || byAccount.has(accountName)) {
        continue
      }
      byAccount.set(accountName, {
        accountName,
        label: displayAccountLabel(accountName),
        provider: model.provider,
        authMethod: model.auth_method,
        credentialID:
          model.credential_id ?? accountName.slice("credential:".length),
        modelID: model.model,
        modelIndex: model.is_virtual ? undefined : model.index,
      })
    }
    return [...byAccount.values()].sort((a, b) =>
      a.label.localeCompare(b.label),
    )
  }, [defaultSelectableModels])

  const accountRouterModels = useMemo(
    () => defaultSelectableModels.filter(isAccountRouterModel),
    [defaultSelectableModels],
  )

  const selectedAccount = useMemo(
    () =>
      accountModels.find(
        (account) => account.accountName === selectedAccountName,
      ),
    [accountModels, selectedAccountName],
  )

  const selectedAccountRouter = useMemo(
    () =>
      accountRouterModels.find(
        (router) => router.model_name === selectedAccountName,
      ),
    [accountRouterModels, selectedAccountName],
  )

  useEffect(() => {
    const defaultModel = modelList.find(
      (model) => model.model_name === defaultModelName,
    )
    const defaultAccountName =
      defaultModel && isAccountRouterModel(defaultModel)
        ? defaultModel.model_name
        : defaultModel
          ? credentialAccountName(defaultModel)
          : ""
    const nextAccountName =
      defaultAccountName ||
      accountModels[0]?.accountName ||
      accountRouterModels[0]?.model_name ||
      ""

    setSelectedAccountName((current) => {
      if (
        current &&
        (accountModels.some((account) => account.accountName === current) ||
          accountRouterModels.some((router) => router.model_name === current))
      ) {
        return current
      }
      return nextAccountName
    })

    setSelectedModelID((current) => {
      if (current) return current
      return defaultModel?.router?.model ?? defaultModel?.model ?? ""
    })
  }, [accountModels, accountRouterModels, defaultModelName, modelList])

  useEffect(() => {
    let cancelled = false
    const fallbackOption =
      modelOption(
        selectedAccountRouter?.router?.model ??
          selectedAccountRouter?.model ??
          selectedAccount?.modelID ??
          "",
      ) ?? null

    if (!selectedAccountName) {
      setModelOptions([])
      setSelectedModelID("")
      return () => {
        cancelled = true
      }
    }

    if (selectedAccountRouter) {
      const options = fallbackOption ? [fallbackOption] : []
      setModelOptions(options)
      setSelectedModelID((current) => current || options[0]?.id || "")
      return () => {
        cancelled = true
      }
    }

    if (!selectedAccount?.provider) {
      const options = fallbackOption ? [fallbackOption] : []
      setModelOptions(options)
      setSelectedModelID((current) => current || options[0]?.id || "")
      return () => {
        cancelled = true
      }
    }

    setIsLoadingModelOptions(true)
    void fetchUpstreamModels({
      provider: selectedAccount.provider,
      auth_method: selectedAccount.authMethod,
      credential_id: selectedAccount.credentialID,
      model_index: selectedAccount.modelIndex,
    })
      .then((response) => {
        if (cancelled) return
        const seen = new Set<string>()
        const options = response.models.filter((model) => {
          const id = model.id.trim()
          if (!id || seen.has(id)) return false
          seen.add(id)
          return true
        })
        const resolvedOptions =
          options.length > 0 ? options : fallbackOption ? [fallbackOption] : []
        setModelOptions(resolvedOptions)
        setSelectedModelID((current) => {
          if (resolvedOptions.some((model) => model.id === current)) {
            return current
          }
          return fallbackOption?.id || resolvedOptions[0]?.id || ""
        })
      })
      .catch((err) => {
        if (cancelled) return
        console.error("Failed to fetch account models:", err)
        setModelOptions(fallbackOption ? [fallbackOption] : [])
        setSelectedModelID((current) => current || fallbackOption?.id || "")
      })
      .finally(() => {
        if (!cancelled) setIsLoadingModelOptions(false)
      })

    return () => {
      cancelled = true
    }
  }, [selectedAccount, selectedAccountName, selectedAccountRouter])

  const handleSetAccount = useCallback(
    (accountName: string) => {
      setSelectedAccountName(accountName)
      setSelectedModelID("")
      void handleSetDefault(accountName)
    },
    [handleSetDefault],
  )

  const handleSetModel = useCallback((modelID: string) => {
    setSelectedModelID(modelID)
  }, [])

  const hasAvailableModels =
    accountModels.length > 0 || accountRouterModels.length > 0

  return {
    defaultModelName,
    selectedAccountName,
    selectedModelID,
    hasAvailableModels,
    accountModels,
    accountRouterModels,
    modelOptions,
    isLoadingModelOptions,
    handleSetAccount,
    handleSetModel,
  }
}
