import {
  IconAlertCircle,
  IconInfoCircle,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import type { TFunction } from "i18next"
import { useTranslation } from "react-i18next"

import type {
  CodexAccountLimitAccount,
  CodexAccountLimitEntry,
} from "@/api/oauth"
import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"

interface CodexAccountLimitSummaryProps {
  account?: CodexAccountLimitAccount
  loading: boolean
  error: string
  apiError?: string
  onRefresh: () => void
}

export function CodexAccountLimitSummary({
  account,
  loading,
  error,
  apiError,
  onRefresh,
}: CodexAccountLimitSummaryProps) {
  const { t } = useTranslation()
  const entries = account?.entries ?? []
  const message = error || apiError || accountLimitMessage(account, t)
  const refreshLabel = t("credentials.codexLimits.refresh")

  if (!loading && !message && !account) {
    return null
  }

  return (
    <div className="mt-3 flex flex-wrap items-center gap-1.5">
      {entries.map((entry) => (
        <LimitEntryChip key={`${entry.name}-${entry.window}`} entry={entry} />
      ))}

      {account?.rate_limit_reset_credits ? (
        <ResetCreditsSummary
          availableCount={account.rate_limit_reset_credits.available_count}
          autoReset={account.rate_limit_reset_credits.auto_reset}
        />
      ) : null}

      {loading && entries.length === 0 ? (
        <span className="text-muted-foreground inline-flex items-center gap-1 text-[11px] leading-none">
          <IconLoader2 className="size-3 animate-spin" />
          {t("credentials.codexLimits.loading")}
        </span>
      ) : null}

      {!loading && message ? (
        <span className="text-muted-foreground inline-flex min-w-0 items-center gap-1 text-[11px] leading-none">
          <IconAlertCircle className="size-3 shrink-0" />
          <span className="truncate">{message}</span>
        </span>
      ) : null}

      <Button
        size="icon-sm"
        variant="ghost"
        className="size-5 shrink-0"
        disabled={loading}
        onClick={onRefresh}
        aria-label={refreshLabel}
        title={refreshLabel}
      >
        {loading ? (
          <IconLoader2 className="size-3 animate-spin" />
        ) : (
          <IconRefresh className="size-3" />
        )}
      </Button>
    </div>
  )
}

function ResetCreditsSummary({
  availableCount,
  autoReset,
}: {
  availableCount: number
  autoReset: boolean
}) {
  const { t } = useTranslation()
  const count = Math.max(0, Math.floor(availableCount))
  const autoResetLabel = t("credentials.codexLimits.autoReset")
  const autoResetDescription = t("credentials.codexLimits.autoResetDescription")

  return (
    <span className="text-muted-foreground inline-flex max-w-full flex-wrap items-center gap-1 text-[11px] leading-none">
      <IconRefresh aria-hidden="true" className="size-3 shrink-0" />
      <span className="font-medium tabular-nums">
        {t("credentials.codexLimits.resetCredits", { count })}
      </span>
      {autoReset ? <span>· {autoResetLabel}</span> : null}
      {autoReset ? (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              className="focus-visible:ring-ring inline-flex size-6 shrink-0 items-center justify-center rounded-sm focus-visible:ring-2 focus-visible:outline-none"
              aria-label={autoResetDescription}
            >
              <IconInfoCircle aria-hidden="true" className="size-3" />
            </button>
          </TooltipTrigger>
          <TooltipContent>{autoResetDescription}</TooltipContent>
        </Tooltip>
      ) : null}
    </span>
  )
}

function LimitEntryChip({ entry }: { entry: CodexAccountLimitEntry }) {
  const { t } = useTranslation()
  const percent = clampPercent(entry.used_percent)
  const statusKey = entry.status === "available" ? "available" : "unavailable"
  const label = [entry.name, displayLimitWindow(entry.window, t)]
    .filter(Boolean)
    .join(" ")
  const usedLabel =
    entry.used_percent == null
      ? t(`credentials.codexLimits.status.${statusKey}`)
      : `${Math.round(percent)}%`
  const textColor = usageColor(percent)
  const usedStyle =
    entry.used_percent == null ? undefined : { color: textColor }

  return (
    <span className="inline-flex max-w-full flex-wrap items-baseline gap-x-1 gap-y-0.5 text-[11px] leading-none">
      <span className="font-medium break-words">{label}</span>
      {/* ui-rule-allow dynamic-style: percent text color follows runtime account usage percent. */}
      <span className="shrink-0 font-medium tabular-nums" style={usedStyle}>
        {usedLabel}
      </span>
    </span>
  )
}

function clampPercent(value: number | undefined): number {
  if (value == null || Number.isNaN(value)) {
    return 0
  }
  return Math.max(0, Math.min(100, value))
}

function usageColor(percent: number): string {
  if (percent <= 10) {
    return "hsl(142 76% 36%)"
  }
  const progress = (percent - 10) / 90
  const hue = Math.round(142 * (1 - progress))
  return `hsl(${hue} 76% 44%)`
}

function displayLimitWindow(window: string | undefined, t: TFunction): string {
  if (!window) {
    return ""
  }
  return t(`credentials.codexLimits.windows.${window}`, {
    defaultValue: window,
  })
}

function accountLimitMessage(
  account: CodexAccountLimitAccount | undefined,
  t: TFunction,
): string {
  if (!account) {
    return ""
  }
  if (account.credential_status === "missing") {
    return t("credentials.codexLimits.credentialsMissing")
  }
  if (account.limits_error) {
    return t(`credentials.codexLimits.errors.${account.limits_error}`, {
      defaultValue: t("credentials.codexLimits.limitsUnavailable"),
    })
  }
  if (account.limits_status && account.limits_status !== "available") {
    return t("credentials.codexLimits.limitsUnavailable")
  }
  return ""
}
