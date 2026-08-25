import type { ReactNode, Ref, UIEventHandler } from "react"

import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

export function CollectionShell({
  title,
  total,
  actions,
  toolbar,
  selectionBar,
  children,
  resultsRef,
  onResultsScroll,
  className,
}: {
  title: string
  total?: number
  actions?: ReactNode
  toolbar?: ReactNode
  selectionBar?: ReactNode
  children: ReactNode
  resultsRef?: Ref<HTMLDivElement>
  onResultsScroll?: UIEventHandler<HTMLDivElement>
  className?: string
}) {
  return (
    <div
      data-slot="collection-shell"
      className={cn("bg-background flex h-full min-w-0 flex-col", className)}
    >
      <PageHeader
        title={title}
        titleExtra={
          typeof total === "number" ? (
            <Badge
              variant="secondary"
              className="font-mono tabular-nums"
              aria-label={`${total} result${total === 1 ? "" : "s"}`}
            >
              {total}
            </Badge>
          ) : undefined
        }
      >
        {actions}
      </PageHeader>
      {toolbar}
      {selectionBar}
      <div
        ref={resultsRef}
        data-slot="collection-scroll-container"
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
        onScroll={onResultsScroll}
      >
        <div className="mx-auto w-full max-w-7xl px-3 py-3 sm:px-6 sm:py-4">
          {children}
        </div>
      </div>
    </div>
  )
}
