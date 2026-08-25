import { IconArrowLeft, IconLoader2, IconRefresh } from "@tabler/icons-react"
import type { ReactNode } from "react"

import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export function CollectionDetailShell({
  title,
  identity,
  status,
  actions,
  children,
  loading = false,
  error,
  notFound = false,
  onBack,
  onRetry,
  backLabel = "Back to collection",
  contentClassName,
}: {
  title: string
  identity?: ReactNode
  status?: ReactNode
  actions?: ReactNode
  children?: ReactNode
  loading?: boolean
  error?: ReactNode
  notFound?: boolean
  onBack: () => void
  onRetry?: () => void
  backLabel?: string
  contentClassName?: string
}) {
  return (
    <div
      data-slot="collection-detail-shell"
      className="bg-background flex h-full min-w-0 flex-col"
    >
      <PageHeader title={title} titleExtra={identity}>
        {actions}
      </PageHeader>
      <div className="border-border flex flex-wrap items-center gap-2 border-y px-3 py-2 sm:px-6">
        <Button type="button" size="sm" variant="ghost" onClick={onBack}>
          <IconArrowLeft />
          {backLabel}
        </Button>
        {status && (
          <div className="ml-auto flex items-center gap-2">{status}</div>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-4 sm:px-6">
        <div className={cn("mx-auto w-full max-w-5xl", contentClassName)}>
          {loading ? (
            <CollectionDetailState
              icon={<IconLoader2 className="animate-spin" />}
              title="Loading details…"
            />
          ) : error ? (
            <CollectionDetailState
              title="Details could not be loaded"
              description={error}
              action={
                onRetry ? (
                  <Button type="button" variant="outline" onClick={onRetry}>
                    <IconRefresh /> Retry
                  </Button>
                ) : undefined
              }
              destructive
            />
          ) : notFound ? (
            <CollectionDetailState
              title="Item not found"
              description="The requested item does not exist or is no longer available."
              action={
                <Button type="button" variant="outline" onClick={onBack}>
                  <IconArrowLeft /> {backLabel}
                </Button>
              }
            />
          ) : (
            children
          )}
        </div>
      </div>
    </div>
  )
}

function CollectionDetailState({
  icon,
  title,
  description,
  action,
  destructive = false,
}: {
  icon?: ReactNode
  title: string
  description?: ReactNode
  action?: ReactNode
  destructive?: boolean
}) {
  return (
    <div
      role={destructive ? "alert" : "status"}
      className="border-border flex min-h-56 flex-col items-center justify-center rounded-lg border border-dashed p-6 text-center"
    >
      {icon && (
        <span className="text-muted-foreground mb-3 [&_svg]:size-5">
          {icon}
        </span>
      )}
      <h2
        className={
          destructive ? "text-destructive font-semibold" : "font-semibold"
        }
      >
        {title}
      </h2>
      {description && (
        <div className="text-muted-foreground mt-2 max-w-xl text-sm">
          {description}
        </div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
