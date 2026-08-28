import { IconLoader2, IconRefresh } from "@tabler/icons-react"
import { type ReactNode, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import type {
  CollectionBulkDeleteResponse,
  CollectionQuerySchema,
} from "@/api/collection"
import { CollectionContextBar } from "@/components/collection/collection-context-bar"
import { CollectionResults } from "@/components/collection/collection-results"
import { CollectionSelectionBar } from "@/components/collection/collection-selection-bar"
import { CollectionShell } from "@/components/collection/collection-shell"
import { CollectionToolbar } from "@/components/collection/collection-toolbar"
import type {
  CollectionDefinition,
  CollectionView,
} from "@/components/collection/collection-types"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import {
  type CollectionRouteSearch,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"

export interface StandardCollectionPageSearch {
  q?: string
  view?: CollectionView
}

export interface StandardCollectionBulkDeleteConfirmation {
  title?: ReactNode | ((selectedCount: number) => ReactNode)
  description?: ReactNode
  actionLabel?: ReactNode
}

export interface StandardCollectionPageContext {
  backLabel: string
  onBack: () => void
  identity?: ReactNode
  status?: ReactNode
}

export interface StandardCollectionSelectionState {
  selectedIDs: ReadonlySet<string>
  selectedCount: number
  clearSelection: () => void
}

export interface StandardCollectionSelectionOptions<T> {
  disabled?: boolean
  maximumSelected?: number
  isItemSelectable?: (item: T) => boolean
  renderActions?: (state: StandardCollectionSelectionState) => ReactNode
}

export interface StandardCollectionPageProps<T> {
  definition: CollectionDefinition<T>
  search: StandardCollectionPageSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  items: readonly T[]
  total?: number
  schema?: CollectionQuerySchema
  canonicalQuery?: string
  loading?: boolean
  fetching?: boolean
  error?: unknown
  context?: StandardCollectionPageContext
  onRefresh?: () => void | Promise<unknown>
  hasNextPage?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void | Promise<unknown>
  onOpenItem?: (item: T) => void
  addAction?: ReactNode
  onBulkDelete?: (ids: string[]) => Promise<CollectionBulkDeleteResponse>
  isItemSelectable?: (item: T) => boolean
  selection?: StandardCollectionSelectionOptions<T>
  afterBulkDelete?: (
    response: CollectionBulkDeleteResponse,
  ) => void | Promise<unknown>
  bulkDeleteConfirmation?: StandardCollectionBulkDeleteConfirmation
  emptyTitle?: string
  emptyDescription?: ReactNode
  beforeResults?: ReactNode
}

export function StandardCollectionPage<T>({
  definition,
  search,
  onSearchChange,
  items,
  total,
  schema,
  canonicalQuery,
  loading,
  fetching,
  error,
  context,
  onRefresh,
  hasNextPage,
  loadingMore,
  onLoadMore,
  onOpenItem,
  addAction,
  onBulkDelete,
  isItemSelectable,
  selection,
  afterBulkDelete,
  bulkDeleteConfirmation,
  emptyTitle,
  emptyDescription,
  beforeResults,
}: StandardCollectionPageProps<T>) {
  const state = useCollectionRouteState({
    collectionKey: definition.key,
    defaultQuery: definition.defaultQuery,
    supportedViews: definition.supportedViews,
    defaultView: definition.defaultView,
    search,
    onSearchChange,
  })
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [bulkMessage, setBulkMessage] = useState("")
  const commitQuerySuccess = state.commitQuerySuccess

  useEffect(() => {
    if (canonicalQuery) commitQuerySuccess(canonicalQuery)
  }, [canonicalQuery, commitQuerySuccess])

  useEffect(() => {
    if (state.selectedCount !== 0) return
    setConfirmOpen(false)
    setBulkMessage("")
  }, [state.selectedCount])

  const errorMessage =
    error instanceof Error ? error.message : error ? String(error) : ""
  const queryError = useMemo(() => {
    if (!error || typeof error !== "object") return undefined
    const candidate = error as { position?: unknown; message?: unknown }
    if (typeof candidate.position !== "number") return undefined
    return {
      position: candidate.position,
      message:
        typeof candidate.message === "string"
          ? candidate.message
          : "Invalid collection query",
    }
  }, [error])
  const selectionEnabled = Boolean(selection || onBulkDelete)
  const itemSelectable = selection?.isItemSelectable ?? isItemSelectable
  const selectionState = useMemo<StandardCollectionSelectionState>(
    () => ({
      selectedIDs: state.selectedIDs,
      selectedCount: state.selectedCount,
      clearSelection: state.clearSelection,
    }),
    [state.clearSelection, state.selectedCount, state.selectedIDs],
  )

  const removeSelected = async () => {
    if (!onBulkDelete) return
    setDeleting(true)
    setBulkMessage("")
    try {
      const response = await onBulkDelete([...state.selectedIDs])
      state.reconcileBulkDelete(response)
      await afterBulkDelete?.(response)
      setConfirmOpen(false)
      if (response.failures.length > 0) {
        setBulkMessage(
          `${response.failures.length} selected item${response.failures.length === 1 ? " was" : "s were"} retained.`,
        )
      } else {
        toast.success(
          `Deleted ${response.deleted_ids.length} item${response.deleted_ids.length === 1 ? "" : "s"}.`,
        )
      }
    } catch (mutationError) {
      toast.error(
        mutationError instanceof Error
          ? mutationError.message
          : "Bulk deletion failed.",
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <>
      <CollectionShell
        title={definition.title}
        total={total}
        resultsRef={state.setScrollContainerRef}
        onResultsScroll={state.onResultsScroll}
        actions={
          onRefresh || addAction ? (
            <>
              {onRefresh && (
                <Button
                  type="button"
                  variant="outline"
                  size="icon-sm"
                  disabled={fetching}
                  onClick={() => void onRefresh()}
                  aria-label={`Refresh ${definition.title.toLowerCase()}`}
                  title="Refresh"
                >
                  {fetching ? (
                    <IconLoader2 className="animate-spin" />
                  ) : (
                    <IconRefresh />
                  )}
                </Button>
              )}
              {addAction}
            </>
          ) : undefined
        }
        toolbar={
          <>
            {context && <CollectionContextBar {...context} />}
            <CollectionToolbar
              activeQuery={state.query}
              defaultQuery={definition.defaultQuery}
              schema={schema}
              queryError={queryError}
              disabled={deleting}
              onApplyQuery={state.applyQuery}
              view={state.view}
              supportedViews={state.supportedViews}
              recentQueries={state.recentQueries}
              onClearHistory={state.clearHistory}
              onViewChange={state.setView}
            />
          </>
        }
        selectionBar={
          selectionEnabled ? (
            <CollectionSelectionBar
              selectedCount={state.selectedCount}
              deleting={deleting}
              disabled={selection?.disabled}
              message={bulkMessage}
              onDelete={onBulkDelete ? () => setConfirmOpen(true) : undefined}
              onClear={() => {
                setBulkMessage("")
                state.clearSelection()
              }}
            >
              {selection?.renderActions?.(selectionState)}
            </CollectionSelectionBar>
          ) : undefined
        }
      >
        {beforeResults && <div className="mb-3 space-y-3">{beforeResults}</div>}
        <CollectionResults
          definition={definition}
          items={items}
          view={state.view}
          selection={
            selectionEnabled
              ? {
                  selectedIDs: state.selectedIDs,
                  failuresByID: onBulkDelete ? state.failuresByID : undefined,
                  disabled: deleting || selection?.disabled,
                  maximumSelected: selection?.maximumSelected,
                  isItemDisabled: itemSelectable
                    ? (item) => !itemSelectable(item)
                    : undefined,
                  onSelectionChange: state.setSelection,
                }
              : undefined
          }
          onOpenItem={onOpenItem}
          loading={loading}
          error={errorMessage || undefined}
          onRetry={onRefresh ? () => void onRefresh() : undefined}
          hasNextPage={hasNextPage}
          loadingMore={loadingMore}
          onLoadMore={onLoadMore ? () => void onLoadMore() : undefined}
          emptyTitle={emptyTitle}
          emptyDescription={emptyDescription}
        />
      </CollectionShell>

      {onBulkDelete && (
        <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {typeof bulkDeleteConfirmation?.title === "function"
                  ? bulkDeleteConfirmation.title(state.selectedCount)
                  : (bulkDeleteConfirmation?.title ?? (
                      <>
                        Delete {state.selectedCount} selected item
                        {state.selectedCount === 1 ? "" : "s"}?
                      </>
                    ))}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {bulkDeleteConfirmation?.description ??
                  "Only explicitly selected items will be deleted. Referenced or stale items will remain selected with their blocker."}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={deleting}>Cancel</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={deleting}
                onClick={(event) => {
                  event.preventDefault()
                  void removeSelected()
                }}
              >
                {deleting && <IconLoader2 className="animate-spin" />}
                {bulkDeleteConfirmation?.actionLabel ?? "Delete selected"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </>
  )
}
