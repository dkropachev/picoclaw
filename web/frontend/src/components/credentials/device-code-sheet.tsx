import { IconLoader2, IconRefresh } from "@tabler/icons-react"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import type { OAuthFlowState } from "@/api/oauth"
import { Button } from "@/components/ui/button"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"
import { cn } from "@/lib/utils"

interface DeviceCodeSheetProps {
  open: boolean
  flow: OAuthFlowState | null
  flowHint: string
  onOpenChange: (open: boolean) => void
}

interface CopyValueButtonProps {
  value?: string
  label: string
}

function CopyValueButton({ value, label }: CopyValueButtonProps) {
  const { t } = useTranslation()
  const { copy, isCopied } = useCopyToClipboard()
  const [copyFailed, setCopyFailed] = useState(false)
  const buttonLabel =
    isCopied && !copyFailed
      ? t("credentials.device.copied", { label })
      : t("credentials.device.copy", { label })
  const copyStatus = copyFailed
    ? t("credentials.device.copyFailed", { label })
    : isCopied
      ? t("credentials.device.copied", { label })
      : ""

  const handleCopy = async () => {
    if (!value) return
    setCopyFailed(false)
    try {
      setCopyFailed(!(await copy(value)))
    } catch {
      setCopyFailed(true)
    }
  }

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        disabled={!value}
        onClick={() => void handleCopy()}
        aria-label={buttonLabel}
        title={copyFailed ? copyStatus : buttonLabel}
        className={cn("shrink-0 text-base", copyFailed && "text-destructive")}
      >
        <span aria-hidden="true">
          {copyFailed ? "⚠️" : isCopied ? "✓" : "📋"}
        </span>
      </Button>
      <span className="sr-only" aria-live="polite" aria-atomic="true">
        {copyStatus}
      </span>
    </>
  )
}

export function DeviceCodeSheet({
  open,
  flow,
  flowHint,
  onOpenChange,
}: DeviceCodeSheetProps) {
  const { t } = useTranslation()

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="data-[side=right]:!w-full data-[side=right]:sm:!w-[480px] data-[side=right]:sm:!max-w-[480px]"
      >
        <SheetHeader className="border-b-muted border-b px-6 py-5">
          <SheetTitle>{t("credentials.device.title")}</SheetTitle>
          <SheetDescription>
            {t("credentials.device.description")}
          </SheetDescription>
        </SheetHeader>

        <div className="space-y-4 px-6 py-5">
          <div>
            <p className="text-muted-foreground text-xs uppercase">
              {t("credentials.device.code")}
            </p>
            <div className="mt-1 flex items-center gap-2 rounded-md border px-3 py-2">
              <p className="min-w-0 flex-1 font-mono text-lg font-semibold tracking-wide">
                {flow?.user_code || "-"}
              </p>
              <CopyValueButton
                key={flow?.user_code ?? "empty-code"}
                value={flow?.user_code}
                label={t("credentials.device.code")}
              />
            </div>
          </div>

          <div>
            <p className="text-muted-foreground text-xs uppercase">
              {t("credentials.device.url")}
            </p>
            <div className="mt-1 flex items-start gap-2">
              {flow?.verify_url ? (
                <a
                  href={flow.verify_url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-primary min-w-0 flex-1 text-sm break-all underline"
                >
                  {flow.verify_url}
                </a>
              ) : (
                <span className="text-muted-foreground min-w-0 flex-1 text-sm">
                  -
                </span>
              )}
              <CopyValueButton
                key={flow?.verify_url ?? "empty-url"}
                value={flow?.verify_url}
                label={t("credentials.device.url")}
              />
            </div>
          </div>

          <div className="text-muted-foreground flex items-center gap-2 text-sm">
            {flow ? (
              <IconRefresh className="size-4" />
            ) : (
              <IconLoader2 className="size-4 animate-spin" />
            )}
            {flow
              ? t("credentials.device.polling")
              : t("credentials.device.starting")}
          </div>

          {flow && (
            <div
              role={
                flow.status === "error" || flow.status === "expired"
                  ? "alert"
                  : "status"
              }
              aria-live={
                flow.status === "error" || flow.status === "expired"
                  ? "assertive"
                  : "polite"
              }
              aria-atomic="true"
              className="bg-muted rounded-md border px-3 py-2 text-sm"
            >
              {flowHint}
            </div>
          )}
        </div>

        <SheetFooter className="border-t-muted border-t px-6 py-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          {flow?.verify_url ? (
            <Button asChild>
              <a href={flow.verify_url} target="_blank" rel="noreferrer">
                {t("credentials.device.open")}
              </a>
            </Button>
          ) : (
            <Button disabled>{t("credentials.device.open")}</Button>
          )}
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
