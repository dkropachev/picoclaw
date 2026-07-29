import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type FetchModelsRequest,
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
  const modelDiscoveryWarning = t("chat.modelDiscoveryWarning")
  const modelDiscoveryErrorTitle = t("chat.modelDiscoveryError")
  const [modelList, setModelList] = useState<ModelInfo[]>([])
  const [defaultModelName, setDefaultModelName] = useState("")
  const [selectedAccountName, setSelectedAccountName] = useState("")
  const [selectedModelID, setSelectedModelID] = useState("")
  const [modelOptions, setModelOptions] = useState<UpstreamModel[]>([])
  const [isLoadingModelOptions, setIsLoadingModelOptions] = useState(false)
  const [modelDiscoveryError, setModelDiscoveryError] = useState<string | null>(
    null,
  )
  const [modelDiscoveryAttempt, setModelDiscoveryAttempt] = useState(0)
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
      return defaultModel && !isAccountRouterModel(defaultModel)
        ? defaultModel.model
        : ""
    })
  }, [accountModels, accountRouterModels, defaultModelName, modelList])

  useEffect(() => {
    let cancelled = false
    const defaultModel = modelList.find(
      (model) => model.model_name === defaultModelName,
    )
    const configuredFallbackOption =
      selectedAccount &&
      defaultModel &&
      credentialAccountName(defaultModel) === selectedAccount.accountName
        ? modelOption(defaultModel.model)
        : null
    const fallbackOption =
      configuredFallbackOption ??
      modelOption(selectedAccount?.modelID ?? "") ??
      null

    if (!selectedAccountName) {
      setModelOptions([])
      setSelectedModelID(
        defaultModel && !isAccountRouterModel(defaultModel)
          ? defaultModel.model
          : "",
      )
      setIsLoadingModelOptions(false)
      setModelDiscoveryError(null)
      return () => {
        cancelled = true
      }
    }

    let fetchRequest: FetchModelsRequest | null = null
    if (selectedAccountRouter) {
      fetchRequest = { account_ref: selectedAccountRouter.model_name }
    } else if (selectedAccount) {
      fetchRequest = { account_ref: selectedAccount.accountName }
    }

    if (!fetchRequest) {
      const options = fallbackOption ? [fallbackOption] : []
      setModelOptions(options)
      setSelectedModelID((current) => current || options[0]?.id || "")
      setIsLoadingModelOptions(false)
      setModelDiscoveryError(null)
      return () => {
        cancelled = true
      }
    }

    setModelOptions([])
    setIsLoadingModelOptions(true)
    setModelDiscoveryError(null)
    void fetchUpstreamModels(fetchRequest)
      .then((response) => {
        if (cancelled) return
        const issueDescription = response.issues
          ?.map(
            (issue) =>
              `${displayAccountLabel(issue.account_ref)}: ${issue.error}`,
          )
          .join("\n")
        const seen = new Set<string>()
        const options = response.models.filter((model) => {
          const id = model.id.trim()
          const normalizedID = id.toLowerCase()
          if (!id || seen.has(normalizedID)) return false
          seen.add(normalizedID)
          return true
        })
        if (
          options.length === 0 &&
          (selectedAccountRouter != null || Boolean(issueDescription))
        ) {
          const errorMessage = issueDescription || modelDiscoveryErrorTitle
          setModelOptions(fallbackOption ? [fallbackOption] : [])
          setSelectedModelID(fallbackOption?.id || "")
          setModelDiscoveryError(errorMessage)
          toast.error(modelDiscoveryErrorTitle, {
            description: errorMessage,
          })
          return
        }
        if (issueDescription) {
          toast.warning(modelDiscoveryWarning, {
            description: issueDescription,
          })
        }
        const resolvedOptions =
          options.length > 0 ? options : fallbackOption ? [fallbackOption] : []
        setModelOptions(resolvedOptions)
        setSelectedModelID((current) => {
          const selected = resolvedOptions.find(
            (model) =>
              model.id.trim().toLowerCase() === current.trim().toLowerCase(),
          )
          if (selected) {
            return selected.id
          }
          return resolvedOptions[0]?.id || ""
        })
        setModelDiscoveryError(null)
      })
      .catch((err) => {
        if (cancelled) return
        console.error("Failed to fetch account models:", err)
        const errorMessage =
          err instanceof Error ? err.message : modelDiscoveryErrorTitle
        setModelOptions(fallbackOption ? [fallbackOption] : [])
        setSelectedModelID(fallbackOption?.id || "")
        setModelDiscoveryError(errorMessage)
        toast.error(modelDiscoveryErrorTitle, {
          description: errorMessage,
        })
      })
      .finally(() => {
        if (!cancelled) setIsLoadingModelOptions(false)
      })

    return () => {
      cancelled = true
    }
  }, [
    defaultModelName,
    modelDiscoveryAttempt,
    modelDiscoveryErrorTitle,
    modelDiscoveryWarning,
    modelList,
    selectedAccount,
    selectedAccountName,
    selectedAccountRouter,
  ])

  const handleSetAccount = useCallback(
    (accountName: string) => {
      setSelectedAccountName(accountName)
      setSelectedModelID("")
      setModelDiscoveryError(null)
      void handleSetDefault(accountName)
    },
    [handleSetDefault],
  )

  const handleSetModel = useCallback((modelID: string) => {
    setSelectedModelID(modelID)
  }, [])

  const retryModelDiscovery = useCallback(() => {
    setModelDiscoveryAttempt((attempt) => attempt + 1)
  }, [])

  const hasAvailableModels = defaultSelectableModels.length > 0

  return {
    defaultModelName,
    selectedAccountName,
    selectedModelID,
    hasAvailableModels,
    accountModels,
    accountRouterModels,
    modelOptions,
    isLoadingModelOptions,
    modelDiscoveryError,
    handleSetAccount,
    handleSetModel,
    retryModelDiscovery,
  }
}
