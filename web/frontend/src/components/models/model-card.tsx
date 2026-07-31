import {
  IconEdit,
  IconGitBranch,
  IconKey,
  IconRoute,
  IconTrash,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { ModelInfo } from "@/api/models"
import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"

interface ModelCardProps {
  model: ModelInfo
  onEdit: (model: ModelInfo) => void
  onDelete: (model: ModelInfo) => void
}

export function ModelCard({ model, onEdit, onDelete }: ModelCardProps) {
  const { t } = useTranslation()
  const isRouter = model.provider === "router" || model.router != null
  const isModelRouter =
    model.provider === "model-router" || model.model_router != null
  const isOAuth = model.auth_method === "oauth"
  const status = model.status
  const statusLabel = t(`models.status.${status}`)
  const editLabel = isModelRouter
    ? t("models.modelRouter.actionEdit")
    : isRouter
      ? t("models.router.actionEdit")
      : t("models.action.edit")
  const deleteLabel = isModelRouter
    ? t("models.modelRouter.actionDelete")
    : isRouter
      ? t("models.router.actionDelete")
      : t("models.action.delete")
  return (
    <div
      className={[
        "group/card hover:bg-muted/30 relative flex w-full max-w-[36rem] flex-col gap-3 justify-self-start rounded-xl border p-4 transition-colors hover:shadow-xs",
        model.available
          ? "border-border/60 bg-card"
          : "border-border/50 bg-card/60",
      ].join(" ")}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={[
              "mt-0.5 h-2 w-2 shrink-0 rounded-full",
              status === "available"
                ? "bg-green-500"
                : status === "unreachable"
                  ? "bg-amber-500"
                  : "bg-muted-foreground/25",
            ].join(" ")}
            title={statusLabel}
          />
          <span className="text-foreground truncate text-sm font-semibold">
            {model.model_name}
          </span>
          {model.is_virtual && (
            <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none font-medium">
              {t("models.badge.virtual")}
            </span>
          )}
          {isRouter && (
            <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none font-medium">
              {t("models.badge.router")}
            </span>
          )}
          {isModelRouter && (
            <span className="bg-muted text-muted-foreground shrink-0 rounded px-1.5 py-0.5 text-[10px] leading-none font-medium">
              {t("models.badge.modelRouter")}
            </span>
          )}
        </div>

        <div className="flex shrink-0 items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon-sm"
            onClick={() => onEdit(model)}
            aria-label={editLabel}
            title={editLabel}
          >
            <IconEdit className="size-3.5" />
          </Button>

          <Tooltip delayDuration={700}>
            <TooltipTrigger asChild>
              <span>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => onDelete(model)}
                  aria-label={deleteLabel}
                  title={deleteLabel}
                  className="text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                >
                  <IconTrash className="size-3.5" />
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>{deleteLabel}</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {!isRouter && !isModelRouter && (
        <p className="text-muted-foreground truncate font-mono text-xs leading-snug">
          {model.model}
        </p>
      )}

      <div className="flex items-center gap-2">
        {isModelRouter ? (
          <span className="text-muted-foreground flex items-center gap-1 text-[11px]">
            <IconGitBranch className="size-3" />
            {statusLabel}
          </span>
        ) : isRouter ? (
          <span className="text-muted-foreground flex items-center gap-1 text-[11px]">
            <IconRoute className="size-3" />
            {statusLabel}
          </span>
        ) : isOAuth ? (
          <span className="text-muted-foreground bg-muted rounded px-1.5 py-0.5 text-[10px] font-medium">
            OAuth
          </span>
        ) : status === "available" && model.api_key ? (
          <span className="text-muted-foreground flex items-center gap-1 font-mono text-[11px]">
            <IconKey className="size-3" />
            {model.api_key}
          </span>
        ) : (
          <span className="text-muted-foreground text-[11px]">
            {statusLabel}
          </span>
        )}
      </div>
    </div>
  )
}
