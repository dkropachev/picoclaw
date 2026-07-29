import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

export function EventStatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const label = statusLabel(status, t)
  const variant =
    status === "failed" || status === "dead"
      ? "destructive"
      : status === "succeeded"
        ? "secondary"
        : status === "claimed" || status === "running"
          ? "default"
          : "outline"

  return (
    <Badge variant={variant} className="max-w-full capitalize">
      <span className="truncate">{label}</span>
    </Badge>
  )
}

export function EventPanel({
  title,
  titleExtra,
  children,
  className,
}: {
  title: string
  titleExtra?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section
      className={cn(
        "border-border bg-background/60 min-w-0 rounded-lg border p-3",
        className,
      )}
    >
      <div className="mb-3 flex min-w-0 items-center justify-between gap-2">
        <h3 className="min-w-0 truncate text-sm font-medium">{title}</h3>
        {titleExtra}
      </div>
      {children}
    </section>
  )
}

export function EventMeta({
  label,
  value,
  mono,
  breakAll,
}: {
  label: string
  value?: string | number
  mono?: boolean
  breakAll?: boolean
}) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground text-xs">{label}</dt>
      <dd
        className={cn(
          "mt-0.5 min-w-0 text-sm",
          mono && "font-mono text-xs",
          breakAll ? "break-all" : "truncate",
        )}
      >
        {value == null || value === "" ? "-" : value}
      </dd>
    </div>
  )
}

// Keyboard focus is required for independently scrollable operational regions.
/* eslint-disable jsx-a11y/no-noninteractive-tabindex */
export function EventScrollRegion({
  label,
  className,
  children,
  onScroll,
}: {
  label: string
  className?: string
  children: ReactNode
  onScroll?: React.UIEventHandler<HTMLDivElement>
}) {
  return (
    <div
      role="region"
      aria-label={label}
      tabIndex={0}
      onScroll={onScroll}
      className={cn(
        "focus-visible:ring-ring/40 min-w-0 outline-none focus-visible:ring-2",
        className,
      )}
    >
      {children}
    </div>
  )
}
/* eslint-enable jsx-a11y/no-noninteractive-tabindex */

function statusLabel(
  status: string,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  switch (status) {
    case "pending":
      return t("pages.events.statuses.pending", "Pending")
    case "claimed":
      return t("pages.events.statuses.claimed", "Claimed")
    case "running":
      return t("pages.events.statuses.running", "Running")
    case "succeeded":
      return t("pages.events.statuses.succeeded", "Succeeded")
    case "failed":
      return t("pages.events.statuses.failed", "Failed")
    case "dead":
      return t("pages.events.statuses.dead", "Dead")
    default:
      return status.replaceAll("_", " ")
  }
}
