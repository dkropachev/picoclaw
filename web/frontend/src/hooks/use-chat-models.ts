import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ModelAlias,
  type ModelInfo,
  getModels,
  setDefaultSelection,
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
  modelIndex?: number
}

export interface ChatModelAliasOption extends ModelAlias {
  isRouter?: boolean
}

function isAccountRouterModel(model: ModelInfo): boolean {
  return model.provider === "router" || model.router != null
}

function isModelRouterModel(model: ModelInfo): boolean {
  return model.provider === "model-router" || model.model_router != null
}

function displayAccountLabel(accountName: string): string {
  return accountName.startsWith("credential:")
    ? accountName.slice("credential:".length)
    : accountName
}

export function useChatModels({ isConnected }: UseChatModelsOptions) {
  const { t } = useTranslation()
  const [modelList, setModelList] = useState<ModelInfo[]>([])
  const [modelAliases, setModelAliases] = useState<ModelAlias[]>([])
  const [defaultAccountRef, setDefaultAccountRef] = useState("")
  const [defaultModelName, setDefaultModelName] = useState("")
  const [selectedAccountName, setSelectedAccountName] = useState("")
  const [selectedModelAlias, setSelectedModelAlias] = useState("")
  const [isSavingSelection, setIsSavingSelection] = useState(false)
  const requestIdRef = useRef(0)

  const loadModels = useCallback(async () => {
    try {
      const data = await getModels()
      setModelList(data.models)
      setModelAliases(data.model_aliases ?? [])
      setDefaultAccountRef(data.default_account_ref ?? "")
      setDefaultModelName(data.default_model ?? "")
      return data
    } catch {
      // The chat connection state owns the user-visible load failure.
      return undefined
    }
  }, [])

  useEffect(() => {
    const timerId = setTimeout(() => {
      void loadModels()
    }, 0)

    return () => clearTimeout(timerId)
  }, [isConnected, loadModels])

  const selectableAccounts = useMemo(
    () =>
      modelList.filter(
        (model) => model.enabled !== false && !isModelRouterModel(model),
      ),
    [modelList],
  )

  const accountModels = useMemo(() => {
    const byAccount = new Map<string, ChatAccountOption>()
    for (const model of selectableAccounts) {
      if (isAccountRouterModel(model)) continue
      const accountName = model.model_name.trim()
      if (!accountName || byAccount.has(accountName)) continue
      byAccount.set(accountName, {
        accountName,
        label: displayAccountLabel(accountName),
        provider: model.provider,
        authMethod: model.auth_method,
        credentialID: model.credential_id,
        modelIndex: model.is_virtual ? undefined : model.index,
      })
    }
    return [...byAccount.values()].sort((a, b) =>
      a.label.localeCompare(b.label),
    )
  }, [selectableAccounts])

  const accountRouterModels = useMemo(
    () => selectableAccounts.filter(isAccountRouterModel),
    [selectableAccounts],
  )

  const aliasOptions = useMemo<ChatModelAliasOption[]>(() => {
    const aliases: ChatModelAliasOption[] = modelAliases.map((alias) => ({
      ...alias,
      isRouter: false,
    }))
    const names = new Set(aliases.map((alias) => alias.name))
    for (const model of modelList) {
      if (
        !isModelRouterModel(model) ||
        model.enabled === false ||
        names.has(model.model_name)
      ) {
        continue
      }
      aliases.push({
        name: model.model_name,
        model: model.model,
        isRouter: true,
      })
    }
    return aliases.sort((a, b) => a.name.localeCompare(b.name))
  }, [modelAliases, modelList])

  const accountRefs = useMemo(
    () =>
      new Set([
        ...accountModels.map((account) => account.accountName),
        ...accountRouterModels.map((router) => router.model_name),
      ]),
    [accountModels, accountRouterModels],
  )
  const aliasNames = useMemo(
    () => new Set(aliasOptions.map((alias) => alias.name)),
    [aliasOptions],
  )

  useEffect(() => {
    setSelectedAccountName((current) => {
      if (current && accountRefs.has(current)) return current
      if (defaultAccountRef && accountRefs.has(defaultAccountRef)) {
        return defaultAccountRef
      }
      return ""
    })
    setSelectedModelAlias((current) => {
      if (current && aliasNames.has(current)) return current
      if (defaultModelName && aliasNames.has(defaultModelName)) {
        return defaultModelName
      }
      return ""
    })
  }, [
    accountModels,
    accountRefs,
    accountRouterModels,
    aliasNames,
    aliasOptions,
    defaultAccountRef,
    defaultModelName,
  ])

  const saveSelection = useCallback(
    async (accountRef: string, modelAlias: string) => {
      if (!accountRef || !modelAlias) return
      if (accountRef === defaultAccountRef && modelAlias === defaultModelName) {
        return
      }

      const requestId = ++requestIdRef.current
      setIsSavingSelection(true)
      try {
        await setDefaultSelection(accountRef, modelAlias)
        if (requestId !== requestIdRef.current) return
        setDefaultAccountRef(accountRef)
        setDefaultModelName(modelAlias)
        const gateway = await refreshGatewayState({ force: true })
        showSaveSuccessOrRestartToast(
          t,
          t("models.defaultChangeSuccess"),
          `${accountRef} / ${modelAlias}`,
          gateway?.restartRequired === true,
        )
      } catch (error) {
        if (requestId !== requestIdRef.current) return
        toast.error(
          error instanceof Error ? error.message : t("models.loadError"),
        )
        // Both selectors form one server-validated policy. Roll back the pair
        // immediately, even when each optimistic value is valid on its own.
        setSelectedAccountName(defaultAccountRef)
        setSelectedModelAlias(defaultModelName)

        const data = await loadModels()
        if (requestId !== requestIdRef.current) return
        if (data) {
          setSelectedAccountName(data.default_account_ref ?? "")
          setSelectedModelAlias(data.default_model ?? "")
        }
      } finally {
        if (requestId === requestIdRef.current) {
          setIsSavingSelection(false)
        }
      }
    },
    [defaultAccountRef, defaultModelName, loadModels, t],
  )

  const handleSetAccount = useCallback(
    (accountRef: string) => {
      setSelectedAccountName(accountRef)
      void saveSelection(accountRef, selectedModelAlias)
    },
    [saveSelection, selectedModelAlias],
  )

  const handleSetModelAlias = useCallback(
    (modelAlias: string) => {
      setSelectedModelAlias(modelAlias)
      void saveSelection(selectedAccountName, modelAlias)
    },
    [saveSelection, selectedAccountName],
  )

  return {
    defaultAccountRef,
    defaultModelName,
    selectedAccountName,
    selectedModelAlias,
    hasAvailableModels: accountRefs.size > 0 && aliasOptions.length > 0,
    accountModels,
    accountRouterModels,
    aliasOptions,
    isSavingSelection,
    handleSetAccount,
    handleSetModelAlias,
  }
}
