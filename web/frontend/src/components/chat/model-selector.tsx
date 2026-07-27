import { useTranslation } from "react-i18next"

import type { ModelInfo, UpstreamModel } from "@/api/models"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import type { ChatAccountOption } from "@/hooks/use-chat-models"

interface ModelSelectorProps {
  selectedAccountName: string
  selectedModelID: string
  accountModels: ChatAccountOption[]
  accountRouterModels: ModelInfo[]
  modelOptions: UpstreamModel[]
  isLoadingModelOptions: boolean
  onAccountChange: (modelName: string) => void
  onModelChange: (modelID: string) => void
}

export function ModelSelector({
  selectedAccountName,
  selectedModelID,
  accountModels,
  accountRouterModels,
  modelOptions,
  isLoadingModelOptions,
  onAccountChange,
  onModelChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <Select value={selectedAccountName} onValueChange={onAccountChange}>
        <SelectTrigger
          size="sm"
          aria-label={t("chat.account", "Account")}
          className="text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:border-input h-8 max-w-[170px] min-w-[92px] rounded-full border-transparent bg-transparent shadow-none focus-visible:ring-0 sm:max-w-[240px]"
        >
          <SelectValue placeholder={t("chat.noAccount")} />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {accountModels.length > 0 && (
            <SelectGroup>
              <SelectLabel>{t("chat.modelGroup.accounts")}</SelectLabel>
              {accountModels.map((account) => (
                <SelectItem
                  key={account.accountName}
                  value={account.accountName}
                >
                  {account.label}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
          {accountModels.length > 0 && accountRouterModels.length > 0 && (
            <SelectSeparator />
          )}

          {accountRouterModels.length > 0 && (
            <SelectGroup>
              <SelectLabel>{t("chat.modelGroup.accountRouters")}</SelectLabel>
              {accountRouterModels.map((model) => (
                <SelectItem key={model.index} value={model.model_name}>
                  {model.model_name}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
        </SelectContent>
      </Select>

      <Select
        value={selectedModelID}
        onValueChange={onModelChange}
        disabled={isLoadingModelOptions || modelOptions.length === 0}
      >
        <SelectTrigger
          size="sm"
          aria-label={t("chat.model", "Model")}
          className="text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:border-input h-8 max-w-[180px] min-w-[96px] rounded-full border-transparent bg-transparent shadow-none focus-visible:ring-0 sm:max-w-[260px]"
        >
          <SelectValue
            placeholder={
              isLoadingModelOptions ? t("common.loading") : t("chat.noModel")
            }
          />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          <SelectGroup>
            {modelOptions.map((model) => (
              <SelectItem key={model.id} value={model.id}>
                {model.id}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
  )
}
