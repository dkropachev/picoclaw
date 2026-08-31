import {
  IconAlertTriangle,
  IconInbox,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import {
  type KeyboardEvent,
  type MouseEvent,
  type ReactElement,
  type ReactNode,
  useId,
  useRef,
} from "react"

import type { CollectionBulkDeleteFailure } from "@/api/collection"
import { maximumCollectionBulkDeleteItems } from "@/api/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from "@/components/ui/context-menu"
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
  const selectionInstructionsID = useId()
  const interactions = useCollectionInteractions({
    definition,
    items,
    selection,
    onOpenItem,
    instructionsID: selectionInstructionsID,
  })
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
      {selection && (
        <p id={selectionInstructionsID} className="sr-only">
          {selection.additive
            ? "Click, tap, or press Space to toggle items. Hold Shift to select a range. Double-click, double-tap, or press Enter to open. Right-click for item actions."
            : "Click to select one item. Hold Shift to select a range, or Control or Command to toggle items. Double-click or press Enter to open. Right-click for item actions."}
        </p>
      )}
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
          interactions={interactions}
        />
      ) : view === "grid" ? (
        <CollectionGridResults
          definition={definition}
          items={items}
          selection={selection}
          onOpenItem={onOpenItem}
          interactions={interactions}
        />
      ) : (
        <CollectionListResults
          definition={definition}
          items={items}
          selection={selection}
          onOpenItem={onOpenItem}
          interactions={interactions}
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
  interactions,
  className,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  interactions: CollectionInteractions<T>
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
      <ul className="divide-border divide-y">
        {items.map((item, index) => {
          const id = definition.getItemID(item)
          const failure = selection?.failuresByID?.get(id)
          return withItemContextMenu(
            definition,
            item,
            onOpenItem,
            <li
              key={id}
              data-item-id={id}
              data-state={
                selection?.selectedIDs.has(id) ? "selected" : undefined
              }
              className="hover:bg-muted/30 data-[state=selected]:bg-muted/40 focus-visible:ring-ring flex min-h-14 min-w-0 cursor-default items-center gap-3 px-3 py-2 transition-colors outline-none select-none focus-visible:ring-2 focus-visible:ring-inset"
              {...interactions.itemProps(item, index)}
            >
              <IdentityBlock
                definition={definition}
                item={item}
                failure={failure}
                className="flex-1"
              />
              <CollectionBadges
                definition={definition}
                item={item}
                constrainWidth
              />
            </li>,
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
  interactions,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  interactions: CollectionInteractions<T>
}) {
  return (
    <>
      <CollectionListResults
        definition={definition}
        items={items}
        selection={selection}
        onOpenItem={onOpenItem}
        interactions={interactions}
        className="md:hidden"
      />
      <section
        aria-label={`${definition.title} table`}
        className="border-border hidden overflow-hidden rounded-lg border md:block"
      >
        <Table
          className={
            definition.tableLayout === "fixed" ? "table-fixed" : undefined
          }
        >
          <TableHeader className="bg-background sticky top-0 z-10">
            <TableRow>
              <TableHead>Identity</TableHead>
              {definition.columns.map((column) => (
                <TableHead
                  key={column.id}
                  className={cn(column.headerClassName)}
                >
                  {column.header}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {items.map((item, index) => {
              const id = definition.getItemID(item)
              const selected = selection?.selectedIDs.has(id) ?? false
              return withItemContextMenu(
                definition,
                item,
                onOpenItem,
                <TableRow
                  key={id}
                  data-item-id={id}
                  data-state={selected ? "selected" : undefined}
                  className="focus-visible:ring-ring h-14 cursor-default outline-none select-none focus-visible:ring-2 focus-visible:ring-inset"
                  {...interactions.itemProps(item, index)}
                >
                  <TableCell className="min-w-52">
                    <IdentityBlock
                      definition={definition}
                      item={item}
                      failure={selection?.failuresByID?.get(id)}
                    />
                  </TableCell>
                  {definition.columns.map((column) => (
                    <TableCell key={column.id} className={column.className}>
                      {column.cell(item)}
                    </TableCell>
                  ))}
                </TableRow>,
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
  interactions,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  interactions: CollectionInteractions<T>
}) {
  return (
    <section aria-label={`${definition.title} grid`}>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {items.map((item, index) => {
          const id = definition.getItemID(item)
          const selected = selection?.selectedIDs.has(id) ?? false
          const facts = definition.gridFacts?.slice(0, 4) ?? []
          return withItemContextMenu(
            definition,
            item,
            onOpenItem,
            <article
              key={id}
              data-item-id={id}
              data-state={selected ? "selected" : undefined}
              className="border-border bg-card data-[state=selected]:border-primary/60 focus-visible:ring-ring relative min-w-0 cursor-default rounded-lg border p-4 outline-none select-none focus-visible:ring-2"
              {...interactions.itemProps(item, index)}
            >
              <div className="flex min-w-0 items-start gap-3">
                <IdentityBlock
                  definition={definition}
                  item={item}
                  failure={selection?.failuresByID?.get(id)}
                  className="flex-1"
                />
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
            </article>,
          )
        })}
      </div>
    </section>
  )
}

interface CollectionInteractions<T> {
  itemProps: (
    item: T,
    index: number,
  ) => {
    tabIndex?: number
    "aria-label"?: string
    "aria-describedby"?: string
    onClick?: (event: MouseEvent<HTMLElement>) => void
    onDoubleClick?: (event: MouseEvent<HTMLElement>) => void
    onContextMenu?: (event: MouseEvent<HTMLElement>) => void
    onKeyDown?: (event: KeyboardEvent<HTMLElement>) => void
  }
}

function useCollectionInteractions<T>({
  definition,
  items,
  selection,
  onOpenItem,
  instructionsID,
}: {
  definition: CollectionDefinition<T>
  items: readonly T[]
  selection?: CollectionSelection<T>
  onOpenItem?: (item: T) => void
  instructionsID: string
}): CollectionInteractions<T> {
  const anchor = useRef<string | null>(null)
  const itemID = (item: T) => definition.getItemID(item)
  const selectable = (item: T) =>
    selection != null &&
    !selection.disabled &&
    !selection.isItemDisabled?.(item)
  const maximumSelected =
    selection?.maximumSelected ?? maximumCollectionBulkDeleteItems

  const addWithinLimit = (ids: Set<string>, id: string) => {
    if (ids.has(id) || ids.size < maximumSelected) ids.add(id)
  }

  const selectItem = (
    item: T,
    index: number,
    modifiers: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean },
  ) => {
    if (!selection || !selectable(item)) return
    const id = itemID(item)
    const additive =
      selection.additive || modifiers.ctrlKey || modifiers.metaKey
    if (modifiers.shiftKey) {
      const anchorIndex = items.findIndex(
        (candidate) => itemID(candidate) === anchor.current,
      )
      const start = anchorIndex < 0 ? index : Math.min(anchorIndex, index)
      const end = anchorIndex < 0 ? index : Math.max(anchorIndex, index)
      const next = additive ? new Set(selection.selectedIDs) : new Set<string>()
      for (let rangeIndex = start; rangeIndex <= end; rangeIndex += 1) {
        const candidate = items[rangeIndex]
        if (selectable(candidate)) addWithinLimit(next, itemID(candidate))
      }
      if (anchorIndex < 0) anchor.current = id
      selection.onSelectionChange(next)
      return
    }

    anchor.current = id
    if (additive) {
      const next = new Set(selection.selectedIDs)
      if (next.has(id)) next.delete(id)
      else addWithinLimit(next, id)
      selection.onSelectionChange(next)
      return
    }
    selection.onSelectionChange(new Set([id]))
  }

  const selectAllLoaded = () => {
    if (!selection || selection.disabled) return
    const next = new Set<string>()
    for (const item of items) {
      if (selectable(item)) addWithinLimit(next, itemID(item))
    }
    selection.onSelectionChange(next)
  }

  return {
    itemProps: (item, index) => {
      const id = itemID(item)
      const selected = selection?.selectedIDs.has(id) ?? false
      const hasVisibleActions = (definition.actions ?? []).some(
        (action) => !action.hidden?.(item),
      )
      const hasContextMenu = onOpenItem != null || hasVisibleActions
      const focusable = selection != null || hasContextMenu
      return {
        tabIndex: focusable ? 0 : undefined,
        "aria-label": focusable
          ? selection
            ? `${definition.getItemLabel(item)}. ${selected ? "Selected" : "Not selected"}.`
            : definition.getItemLabel(item)
          : undefined,
        "aria-describedby": selection ? instructionsID : undefined,
        onClick: selection
          ? (event) => {
              if (isInteractiveEventTarget(event)) return
              selectItem(item, index, event)
            }
          : undefined,
        onDoubleClick: onOpenItem
          ? (event) => {
              if (isInteractiveEventTarget(event)) return
              event.preventDefault()
              onOpenItem(item)
            }
          : undefined,
        onContextMenu: selection
          ? () => {
              if (!selected && selectable(item)) {
                anchor.current = id
                selection.onSelectionChange(new Set([id]))
              }
            }
          : undefined,
        onKeyDown: focusable
          ? (event) => {
              if (event.target !== event.currentTarget) return
              if (
                hasContextMenu &&
                (event.key === "ContextMenu" ||
                  (event.key === "F10" && event.shiftKey))
              ) {
                event.preventDefault()
                event.currentTarget.dispatchEvent(
                  new globalThis.MouseEvent("contextmenu", {
                    bubbles: true,
                    cancelable: true,
                  }),
                )
                return
              }
              if (event.key === "Enter" && onOpenItem) {
                event.preventDefault()
                onOpenItem(item)
                return
              }
              if (event.key === " " && selection) {
                event.preventDefault()
                selectItem(item, index, event)
                return
              }
              if (
                event.key.toLowerCase() === "a" &&
                (event.ctrlKey || event.metaKey) &&
                selection
              ) {
                event.preventDefault()
                selectAllLoaded()
                return
              }
              if (event.key === "Escape" && selection && !selection.disabled) {
                event.preventDefault()
                selection.onSelectionChange(new Set())
              }
            }
          : undefined,
      }
    },
  }
}

function isInteractiveEventTarget(event: MouseEvent<HTMLElement>): boolean {
  if (event.target === event.currentTarget) return false
  return (
    event.target instanceof Element &&
    event.target.closest(
      "a,button,input,select,textarea,[role=button],[role=link],[role=menuitem]",
    ) != null
  )
}

function withItemContextMenu<T>(
  definition: CollectionDefinition<T>,
  item: T,
  onOpenItem: ((item: T) => void) | undefined,
  child: ReactElement,
): ReactElement {
  const actions = (definition.actions ?? []).filter(
    (action) => !action.hidden?.(item),
  )
  if (!onOpenItem && actions.length === 0) return child
  return (
    <ContextMenu key={definition.getItemID(item)}>
      <ContextMenuTrigger asChild>{child}</ContextMenuTrigger>
      <ContextMenuContent>
        {onOpenItem && (
          <ContextMenuItem onSelect={() => onOpenItem(item)}>
            Open
          </ContextMenuItem>
        )}
        {onOpenItem && actions.length > 0 && <ContextMenuSeparator />}
        {actions.map((action) => (
          <CollectionActionItem key={action.id} action={action} item={item} />
        ))}
      </ContextMenuContent>
    </ContextMenu>
  )
}

function IdentityBlock<T>({
  definition,
  item,
  failure,
  className,
}: {
  definition: CollectionDefinition<T>
  item: T
  failure?: CollectionBulkDeleteFailure
  className?: string
}) {
  const identity = definition.getItemIdentity(item)
  return (
    <div className={cn("min-w-0", className)}>
      <div className="truncate text-sm font-medium">{identity.title}</div>
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
  constrainWidth = false,
}: {
  definition: CollectionDefinition<T>
  item: T
  className?: string
  constrainWidth?: boolean
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
    <div
      className={cn(
        "flex shrink-0 flex-wrap gap-1",
        constrainWidth && badges.length > 1 && "max-w-1/2 justify-end",
        className,
      )}
    >
      {badges}
    </div>
  ) : null
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
    <ContextMenuItem
      variant={action.destructive ? "destructive" : "default"}
      disabled={action.disabled?.(item)}
      onSelect={() => void action.onSelect(item)}
    >
      {action.icon}
      {label}
    </ContextMenuItem>
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
