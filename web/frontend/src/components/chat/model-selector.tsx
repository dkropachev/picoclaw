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

interface ModelSelectorProps {
  defaultModelName: string
  accountModels: ModelInfo[]
  accountRouterModels: ModelInfo[]
  onValueChange: (modelName: string) => void
}

export function ModelSelector({
  defaultModelName,
  accountModels,
  accountRouterModels,
  onValueChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()

  return (
    <Select value={defaultModelName} onValueChange={onValueChange}>
      <SelectTrigger
        size="sm"
        aria-label={t("chat.model", "Model")}
        className="text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:border-input h-8 max-w-[160px] min-w-[80px] rounded-full border-transparent bg-transparent shadow-none focus-visible:ring-0 sm:max-w-[220px]"
      >
        <SelectValue placeholder={t("chat.noModel")} />
      </SelectTrigger>
      <SelectContent position="popper" align="start">
        {accountModels.length > 0 && (
          <SelectGroup>
            <SelectLabel>{t("chat.modelGroup.accounts")}</SelectLabel>
            {accountModels.map((model) => (
              <SelectItem key={model.index} value={model.model_name}>
                {model.model_name}
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
  )
}
