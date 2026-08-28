import { IconArrowLeft } from "@tabler/icons-react"
import type { ReactNode } from "react"

import { Button } from "@/components/ui/button"

export function CollectionContextBar({
  backLabel,
  onBack,
  identity,
  status,
}: {
  backLabel: string
  onBack: () => void
  identity?: ReactNode
  status?: ReactNode
}) {
  return (
    <div
      data-slot="collection-context-bar"
      className="border-border border-y px-3 py-2 sm:px-6"
    >
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center gap-2">
        <Button type="button" size="sm" variant="ghost" onClick={onBack}>
          <IconArrowLeft />
          {backLabel}
        </Button>
        {identity && (
          <div className="text-muted-foreground min-w-0 truncate text-xs">
            {identity}
          </div>
        )}
        {status && (
          <div className="ml-auto flex items-center gap-2">{status}</div>
        )}
      </div>
    </div>
  )
}
