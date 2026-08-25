import {
  IconAlertTriangle,
  IconDots,
  IconInbox,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import type { ReactNode } from "react"

import type { CollectionBulkDeleteFailure } from "@/api/collection"
import { maximumCollectionBulkDeleteItems } from "@/api/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

import type {
  CollectionDefinition,
  CollectionItemAction,
  CollectionSelection,
  CollectionView,
} from "./collection-types"

export function CollectionResults<T>({
  definition,
  items,
  view,
  selection,
  onOpenItem,
  loading = false,
  error,
  onRetry,
  hasNextPage = false,
  loadingMore = false,
  onLoadMore,
  emptyTitle = "No items found",
  emptyDescription = "No items match the active query.",
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  view: CollectionView
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  loading?: boolean
  error?: ReactNode
  onRetry?: () => void
  hasNextPage?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  emptyTitle?: string
  emptyDescription?: ReactNode
}) {
  if (loading && items.length === 0) return <CollectionResultsLoading />
  if (error && items.length === 0) {
    return <CollectionResultsError error={error} onRetry={onRetry} />
  }
  if (items.length === 0) {
    return (
      <CollectionResultsEmpty
        title={emptyTitle}
        description={emptyDescription}
      />
    )
  }

  return (
    <div data-slot="collection-results" className="space-y-3">
      {error && (
        <div
          role="alert"
          className="border-destructive/40 bg-destructive/5 text-destructive flex items-center gap-2 rounded-lg border px-3 py-2 text-sm"
        >
          <IconAlertTriangle className="size-4 shrink-0" />
          <span className="min-w-0 flex-1">{error}</span>
          {onRetry && (
            <Button type="button" size="sm" variant="outline" onClick={onRetry}>
              Retry
            </Button>
          )}
        </div>
      )}
      {view === "table" ? (
        <CollectionTableResults
          definition={definition}
          items={items}
          selection={selection}
          onOpenItem={onOpenItem}
        />
      ) : view === "grid" ? (
        <CollectionGridResults
          definition={definition}
          items={items}
          selection={selection}
          onOpenItem={onOpenItem}
        />
      ) : (
        <CollectionListResults
          definition={definition}
          items={items}
          selection={selection}
          onOpenItem={onOpenItem}
        />
      )}
      {hasNextPage && onLoadMore && (
        <div className="flex justify-center pt-1">
          <Button
            type="button"
            variant="outline"
            disabled={loadingMore}
            onClick={onLoadMore}
          >
            {loadingMore && <IconLoader2 className="animate-spin" />}
            {loadingMore ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  )
}

function CollectionListResults<T>({
  definition,
  items,
  selection,
  onOpenItem,
  className,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  className?: string
}) {
  return (
    <section
      aria-label={`${definition.title} list`}
      className={cn(
        "border-border overflow-hidden rounded-lg border",
        className,
      )}
    >
      {selection && (
        <SelectLoadedRow
          definition={definition}
          items={items}
          selection={selection}
        />
      )}
      <ul className="divide-border divide-y">
        {items.map((item) => {
          const id = definition.getItemID(item)
          const failure = selection?.failuresByID?.get(id)
          return (
            <li
              key={id}
              data-item-id={id}
              data-state={
                selection?.selectedIDs.has(id) ? "selected" : undefined
              }
              className="hover:bg-muted/30 data-[state=selected]:bg-muted/40 flex min-h-14 min-w-0 items-center gap-3 px-3 py-2 transition-colors"
            >
              {selection && (
                <ItemCheckbox
                  definition={definition}
                  item={item}
                  selection={selection}
                />
              )}
              <IdentityBlock
                definition={definition}
                item={item}
                failure={failure}
                onOpenItem={onOpenItem}
                className="flex-1"
              />
              <CollectionBadges definition={definition} item={item} />
              <ItemActions definition={definition} item={item} />
            </li>
          )
        })}
      </ul>
    </section>
  )
}

function CollectionTableResults<T>({
  definition,
  items,
  selection,
  onOpenItem,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
}) {
  return (
    <>
      <CollectionListResults
        definition={definition}
        items={items}
        selection={selection}
        onOpenItem={onOpenItem}
        className="md:hidden"
      />
      <section
        aria-label={`${definition.title} table`}
        className="border-border hidden overflow-hidden rounded-lg border md:block"
      >
        <Table>
          <TableHeader className="bg-background sticky top-0 z-10">
            <TableRow>
              {selection && (
                <TableHead className="w-10">
                  <LoadedCheckbox
                    definition={definition}
                    items={items}
                    selection={selection}
                  />
                </TableHead>
              )}
              <TableHead>Identity</TableHead>
              {definition.columns.map((column) => (
                <TableHead
                  key={column.id}
                  className={cn(column.headerClassName)}
                >
                  {column.header}
                </TableHead>
              ))}
              {(definition.actions?.length ?? 0) > 0 && (
                <TableHead className="w-10">
                  <span className="sr-only">Actions</span>
                </TableHead>
              )}
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item) => {
              const id = definition.getItemID(item)
              const selected = selection?.selectedIDs.has(id) ?? false
              return (
                <TableRow
                  key={id}
                  data-item-id={id}
                  data-state={selected ? "selected" : undefined}
                  className="h-14"
                >
                  {selection && (
                    <TableCell>
                      <ItemCheckbox
                        definition={definition}
                        item={item}
                        selection={selection}
                      />
                    </TableCell>
                  )}
                  <TableCell className="min-w-52">
                    <IdentityBlock
                      definition={definition}
                      item={item}
                      failure={selection?.failuresByID?.get(id)}
                      onOpenItem={onOpenItem}
                    />
                  </TableCell>
                  {definition.columns.map((column) => (
                    <TableCell key={column.id} className={column.className}>
                      {column.cell(item)}
                    </TableCell>
                  ))}
                  {(definition.actions?.length ?? 0) > 0 && (
                    <TableCell>
                      <ItemActions definition={definition} item={item} />
                    </TableCell>
                  )}
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </section>
    </>
  )
}

function CollectionGridResults<T>({
  definition,
  items,
  selection,
  onOpenItem,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
}) {
  return (
    <section aria-label={`${definition.title} grid`}>
      {selection && (
        <SelectLoadedRow
          definition={definition}
          items={items}
          selection={selection}
          bordered
        />
      )}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {items.map((item) => {
          const id = definition.getItemID(item)
          const selected = selection?.selectedIDs.has(id) ?? false
          const facts = definition.gridFacts?.slice(0, 4) ?? []
          return (
            <article
              key={id}
              data-item-id={id}
              data-state={selected ? "selected" : undefined}
              className="border-border bg-card data-[state=selected]:border-primary/60 relative min-w-0 rounded-lg border p-4"
            >
              <div className="flex min-w-0 items-start gap-3">
                {selection && (
                  <ItemCheckbox
                    definition={definition}
                    item={item}
                    selection={selection}
                  />
                )}
                <IdentityBlock
                  definition={definition}
                  item={item}
                  failure={selection?.failuresByID?.get(id)}
                  onOpenItem={onOpenItem}
                  className="flex-1"
                />
                <ItemActions definition={definition} item={item} />
              </div>
              <CollectionBadges
                definition={definition}
                item={item}
                className="mt-3"
              />
              {facts.length > 0 && (
                <dl className="border-border mt-3 grid grid-cols-2 gap-x-4 gap-y-2 border-t pt-3 text-xs">
                  {facts.map((fact) => {
                    const value = fact.value(item)
                    return (
                      <div key={fact.id} className="min-w-0">
                        <dt className="text-muted-foreground truncate">
                          {fact.label}
                        </dt>
                        <dd
                          className="mt-0.5 truncate"
                          title={textTitle(value)}
                        >
                          {value}
                        </dd>
                      </div>
                    )
                  })}
                </dl>
              )}
            </article>
          )
        })}
      </div>
    </section>
  )
}

function SelectLoadedRow<T>({
  definition,
  items,
  selection,
  bordered = false,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection: CollectionSelection<T>
  bordered?: boolean
}) {
  return (
    <div
      className={cn(
        "border-border flex h-10 items-center justify-between gap-3 px-3 text-xs",
        bordered ? "mb-3 rounded-lg border" : "border-b",
      )}
    >
      <div className="flex items-center gap-2">
        <LoadedCheckbox
          definition={definition}
          items={items}
          selection={selection}
        />
        Select loaded
      </div>
      <span className="text-muted-foreground tabular-nums">
        {items.length} loaded
      </span>
    </div>
  )
}

function LoadedCheckbox<T>({
  definition,
  items,
  selection,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection: CollectionSelection<T>
}) {
  const selectableItems = items.filter(
    (item) => !selection.isItemDisabled?.(item),
  )
  const selectedCount = selectableItems.reduce(
    (count, item) =>
      count + (selection.selectedIDs.has(definition.getItemID(item)) ? 1 : 0),
    0,
  )
  const checked =
    selectableItems.length > 0 && selectedCount === selectableItems.length
      ? true
      : selectedCount > 0
        ? "indeterminate"
        : false
  return (
    <Checkbox
      checked={checked}
      disabled={selection.disabled || selectableItems.length === 0}
      aria-label={`Select all loaded ${definition.title.toLowerCase()}`}
      onCheckedChange={(next) =>
        selection.onLoadedChange(selectableItems, next === true)
      }
    />
  )
}

function ItemCheckbox<T>({
  definition,
  item,
  selection,
}: {
  definition: CollectionDefinition<T>
  item: T
  selection: CollectionSelection<T>
}) {
  const id = definition.getItemID(item)
  const maximumSelected =
    selection.maximumSelected ?? maximumCollectionBulkDeleteItems
  const selectionLimitReached =
    !selection.selectedIDs.has(id) &&
    selection.selectedIDs.size >= maximumSelected
  return (
    <Checkbox
      checked={selection.selectedIDs.has(id)}
      disabled={
        selection.disabled ||
        selectionLimitReached ||
        selection.isItemDisabled?.(item)
      }
      aria-label={`Select ${definition.getItemLabel(item)}`}
      title={
        selectionLimitReached
          ? `Selection is limited to ${maximumSelected} items`
          : undefined
      }
      onCheckedChange={(checked) =>
        selection.onItemChange(item, checked === true)
      }
    />
  )
}

function IdentityBlock<T>({
  definition,
  item,
  failure,
  onOpenItem,
  className,
}: {
  definition: CollectionDefinition<T>
  item: T
  failure?: CollectionBulkDeleteFailure
  onOpenItem?: (item: T) => void
  className?: string
}) {
  const identity = definition.getItemIdentity(item)
  return (
    <div className={cn("min-w-0", className)}>
      {onOpenItem ? (
        <button
          type="button"
          className="focus-visible:ring-ring max-w-full rounded-sm text-left text-sm font-medium focus-visible:ring-2 focus-visible:outline-none"
          onClick={() => onOpenItem(item)}
        >
          <span className="block truncate">{identity.title}</span>
        </button>
      ) : (
        <div className="truncate text-sm font-medium">{identity.title}</div>
      )}
      {identity.description && (
        <div className="text-muted-foreground mt-0.5 truncate text-xs">
          {identity.description}
        </div>
      )}
      {identity.metadata && (
        <div className="text-muted-foreground mt-1 min-w-0 text-xs">
          {identity.metadata}
        </div>
      )}
      {failure && (
        <p className="text-destructive mt-1 text-xs" role="status">
          {collectionFailureMessage(failure)}
        </p>
      )}
    </div>
  )
}

function CollectionBadges<T>({
  definition,
  item,
  className,
}: {
  definition: CollectionDefinition<T>
  item: T
  className?: string
}) {
  const badges = (definition.badges ?? []).flatMap((badge) => {
    const label = badge.label(item)
    return label == null
      ? []
      : [
          <Badge key={badge.id} variant={badge.variant ?? "outline"}>
            {label}
          </Badge>,
        ]
  })
  return badges.length > 0 ? (
    <div className={cn("flex shrink-0 flex-wrap gap-1", className)}>
      {badges}
    </div>
  ) : null
}

function ItemActions<T>({
  definition,
  item,
}: {
  definition: CollectionDefinition<T>
  item: T
}) {
  const actions = (definition.actions ?? []).filter(
    (action) => !action.hidden?.(item),
  )
  if (actions.length === 0) return null
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label={`Actions for ${definition.getItemLabel(item)}`}
        >
          <IconDots />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {actions.map((action) => (
          <CollectionActionItem key={action.id} action={action} item={item} />
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function CollectionActionItem<T>({
  action,
  item,
}: {
  action: CollectionItemAction<T>
  item: T
}) {
  const label =
    typeof action.label === "function" ? action.label(item) : action.label
  return (
    <DropdownMenuItem
      variant={action.destructive ? "destructive" : "default"}
      disabled={action.disabled?.(item)}
      onSelect={() => void action.onSelect(item)}
    >
      {action.icon}
      {label}
    </DropdownMenuItem>
  )
}

export function CollectionResultsLoading() {
  return (
    <div
      role="status"
      aria-label="Loading collection"
      className="border-border divide-border divide-y overflow-hidden rounded-lg border"
    >
      {Array.from({ length: 5 }, (_, index) => (
        <div key={index} className="flex h-14 items-center gap-3 px-3">
          <Skeleton className="size-4 rounded-sm" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton className="h-3 w-1/3" />
            <Skeleton className="h-2.5 w-1/2" />
          </div>
          <Skeleton className="h-5 w-16" />
        </div>
      ))}
    </div>
  )
}

export function CollectionResultsError({
  error,
  onRetry,
}: {
  error: ReactNode
  onRetry?: () => void
}) {
  return (
    <CollectionResultsState
      icon={<IconAlertTriangle />}
      title="Collection could not be loaded"
      description={error}
      destructive
      action={
        onRetry ? (
          <Button type="button" variant="outline" onClick={onRetry}>
            <IconRefresh /> Retry
          </Button>
        ) : undefined
      }
    />
  )
}

export function CollectionResultsEmpty({
  title,
  description,
}: {
  title: string
  description?: ReactNode
}) {
  return (
    <CollectionResultsState
      icon={<IconInbox />}
      title={title}
      description={description}
    />
  )
}

function CollectionResultsState({
  icon,
  title,
  description,
  action,
  destructive = false,
}: {
  icon: ReactNode
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
      <span
        className={cn(
          "bg-muted text-muted-foreground flex size-10 items-center justify-center rounded-lg [&_svg]:size-5",
          destructive && "bg-destructive/10 text-destructive",
        )}
      >
        {icon}
      </span>
      <h2 className="mt-3 text-sm font-semibold">{title}</h2>
      {description && (
        <div className="text-muted-foreground mt-1 max-w-xl text-sm">
          {description}
        </div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

function collectionFailureMessage(
  failure: CollectionBulkDeleteFailure,
): string {
  if (failure.blockers && failure.blockers.length > 0) {
    return failure.blockers.join(" · ")
  }
  const words = failure.code.replaceAll("_", " ")
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}.`
}

function textTitle(value: ReactNode): string | undefined {
  return typeof value === "string" || typeof value === "number"
    ? String(value)
    : undefined
}
