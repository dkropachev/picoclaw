import { IconLoader2, IconTrash, IconX } from "@tabler/icons-react"
import type { ReactNode } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

export function CollectionSelectionBar({
  selectedCount,
  deleting = false,
  deleteDisabled = false,
  onDelete,
  onClear,
  children,
  message,
}: {
  selectedCount: number
  deleting?: boolean
  deleteDisabled?: boolean
  onDelete: () => void
  onClear: () => void
  children?: ReactNode
  message?: ReactNode
}) {
  if (selectedCount === 0) return null
  return (
    <div
      data-slot="collection-selection-bar"
      className="border-border bg-muted/30 border-y px-3 py-2 sm:px-6"
    >
      <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center gap-2">
        <Badge variant="secondary" aria-live="polite">
          {selectedCount} selected
        </Badge>
        {message && (
          <span className="text-destructive min-w-0 flex-1 text-xs">
            {message}
          </span>
        )}
        <div className="ml-auto flex items-center gap-1">
          {children}
          <Button
            type="button"
            size="sm"
            variant="destructive"
            disabled={deleting || deleteDisabled}
            onClick={onDelete}
          >
            {deleting ? (
              <IconLoader2 className="animate-spin" />
            ) : (
              <IconTrash />
            )}
            Delete
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={deleting}
            onClick={onClear}
          >
            <IconX />
            Clear selection
          </Button>
        </div>
      </div>
    </div>
  )
}
