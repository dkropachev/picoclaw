import { useTranslation } from "react-i18next"

import type { ModelInfo } from "@/api/models"
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
import type {
  ChatAccountOption,
  ChatModelAliasOption,
} from "@/hooks/use-chat-models"

interface ModelSelectorProps {
  selectedAccountName: string
  selectedModelAlias: string
  accountModels: ChatAccountOption[]
  accountRouterModels: ModelInfo[]
  aliasOptions: ChatModelAliasOption[]
  isSavingSelection: boolean
  onAccountChange: (accountRef: string) => void
  onModelAliasChange: (modelAlias: string) => void
}

export function ModelSelector({
  selectedAccountName,
  selectedModelAlias,
  accountModels,
  accountRouterModels,
  aliasOptions,
  isSavingSelection,
  onAccountChange,
  onModelAliasChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()
  const directAliases = aliasOptions.filter((alias) => !alias.isRouter)
  const routerAliases = aliasOptions.filter((alias) => alias.isRouter)

  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <Select
        value={selectedAccountName}
        onValueChange={onAccountChange}
        disabled={isSavingSelection}
      >
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
        value={selectedModelAlias}
        onValueChange={onModelAliasChange}
        disabled={isSavingSelection || aliasOptions.length === 0}
      >
        <SelectTrigger
          size="sm"
          aria-label={t("chat.modelAlias", "Model alias")}
          className="text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:border-input h-8 max-w-[180px] min-w-[96px] rounded-full border-transparent bg-transparent shadow-none focus-visible:ring-0 sm:max-w-[260px]"
        >
          <SelectValue placeholder={t("chat.noModel")} />
        </SelectTrigger>
        <SelectContent position="popper" align="start">
          {directAliases.length > 0 && (
            <SelectGroup>
              <SelectLabel>
                {t("chat.modelGroup.aliases", "Model aliases")}
              </SelectLabel>
              {directAliases.map((alias) => (
                <SelectItem
                  key={alias.name}
                  value={alias.name}
                  title={`${alias.name} → ${alias.model}`}
                >
                  {alias.name}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
          {directAliases.length > 0 && routerAliases.length > 0 && (
            <SelectSeparator />
          )}
          {routerAliases.length > 0 && (
            <SelectGroup>
              <SelectLabel>
                {t("chat.modelGroup.modelRouters", "Model routers")}
              </SelectLabel>
              {routerAliases.map((alias) => (
                <SelectItem key={alias.name} value={alias.name}>
                  {alias.name}
                </SelectItem>
              ))}
            </SelectGroup>
          )}
        </SelectContent>
      </Select>
    </div>
  )
}
