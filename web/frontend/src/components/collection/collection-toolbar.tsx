import {
  IconHistory,
  IconLayoutGrid,
  IconList,
  IconTable,
  IconTrash,
} from "@tabler/icons-react"

import type { CollectionQuerySchema } from "@/api/collection"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { cn } from "@/lib/utils"

import {
  CollectionQueryInput,
  type CollectionQueryInputError,
} from "./collection-query-input"
import type { CollectionView } from "./collection-types"

export function CollectionToolbar({
  activeQuery,
  defaultQuery,
  schema,
  queryError,
  disabled = false,
  onApplyQuery,
  view,
  supportedViews,
  recentQueries = [],
  onClearHistory,
  onViewChange,
  className,
}: {
  activeQuery: string
  defaultQuery: string
  schema?: CollectionQuerySchema
  queryError?: CollectionQueryInputError
  disabled?: boolean
  onApplyQuery: (query: string) => void
  view: CollectionView
  supportedViews: readonly CollectionView[]
  recentQueries?: readonly string[]
  onClearHistory?: () => void
  onViewChange: (view: CollectionView) => void
  className?: string
}) {
  return (
    <div
      data-slot="collection-toolbar"
      className={cn("border-border border-y px-3 py-2 sm:px-6", className)}
    >
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-2 sm:flex-row sm:items-start">
        <CollectionQueryInput
          activeQuery={activeQuery}
          defaultQuery={defaultQuery}
          schema={schema}
          error={queryError}
          disabled={disabled}
          onApply={onApplyQuery}
        />
        <div className="flex shrink-0 items-center justify-end gap-1">
          {recentQueries.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  size="icon-sm"
                  variant="outline"
                  disabled={disabled}
                  aria-label="Recent queries"
                  title="Recent queries"
                >
                  <IconHistory />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                align="end"
                className="w-[min(30rem,calc(100vw-2rem))]"
              >
                <DropdownMenuLabel>Recent successful queries</DropdownMenuLabel>
                {recentQueries.map((query) => (
                  <DropdownMenuItem
                    key={query}
                    className="font-mono text-xs"
                    onSelect={() => onApplyQuery(query)}
                  >
                    <span className="truncate">{query}</span>
                  </DropdownMenuItem>
                ))}
                {onClearHistory && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      onSelect={onClearHistory}
                    >
                      <IconTrash /> Clear query history
                    </DropdownMenuItem>
                  </>
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <div
            role="group"
            aria-label="Collection view"
            className="border-border bg-background flex rounded-lg border p-0.5"
          >
            {supportedViews.map((candidate) => (
              <Button
                key={candidate}
                type="button"
                size="icon-sm"
                variant={candidate === view ? "secondary" : "ghost"}
                disabled={disabled}
                aria-label={`${collectionViewLabel(candidate)} view`}
                title={`${collectionViewLabel(candidate)} view`}
                aria-pressed={candidate === view}
                onClick={() => onViewChange(candidate)}
              >
                <CollectionViewIcon view={candidate} />
              </Button>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function CollectionViewIcon({ view }: { view: CollectionView }) {
  if (view === "table") return <IconTable />
  if (view === "grid") return <IconLayoutGrid />
  return <IconList />
}

function collectionViewLabel(view: CollectionView): string {
  return `${view.charAt(0).toUpperCase()}${view.slice(1)}`
}
