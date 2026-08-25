import { IconLoader2, IconRefresh } from "@tabler/icons-react"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import type {
  CollectionBulkDeleteResponse,
  CollectionQuerySchema,
} from "@/api/collection"
import {
  type CollectionDefinition,
  CollectionResults,
  CollectionSelectionBar,
  CollectionShell,
  CollectionToolbar,
  type CollectionView,
} from "@/components/collection"
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

export interface PilotCollectionSearch {
  q?: string
  view?: CollectionView
}

export function StandardPilotCollectionPage<T>({
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
  onRefresh,
  hasNextPage,
  loadingMore,
  onLoadMore,
  onOpenItem,
  addAction,
  onBulkDelete,
  isItemSelectable,
  afterBulkDelete,
  emptyTitle,
  emptyDescription,
}: {
  definition: CollectionDefinition<T>
  search: PilotCollectionSearch
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
  items: readonly T[]
  total?: number
  schema?: CollectionQuerySchema
  canonicalQuery?: string
  loading?: boolean
  fetching?: boolean
  error?: unknown
  onRefresh: () => void | Promise<unknown>
  hasNextPage?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void | Promise<unknown>
  onOpenItem: (item: T) => void
  addAction: React.ReactNode
  onBulkDelete: (ids: string[]) => Promise<CollectionBulkDeleteResponse>
  isItemSelectable?: (item: T) => boolean
  afterBulkDelete?: (
    response: CollectionBulkDeleteResponse,
  ) => void | Promise<unknown>
  emptyTitle?: string
  emptyDescription?: React.ReactNode
}) {
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

  const removeSelected = async () => {
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
          <>
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
            {addAction}
          </>
        }
        toolbar={
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
        }
        selectionBar={
          <CollectionSelectionBar
            selectedCount={state.selectedCount}
            deleting={deleting}
            message={bulkMessage}
            onDelete={() => setConfirmOpen(true)}
            onClear={() => {
              setBulkMessage("")
              state.clearSelection()
            }}
          />
        }
      >
        <CollectionResults
          definition={definition}
          items={items}
          view={state.view}
          selection={{
            selectedIDs: state.selectedIDs,
            failuresByID: state.failuresByID,
            disabled: deleting,
            isItemDisabled: isItemSelectable
              ? (item) => !isItemSelectable(item)
              : undefined,
            onSelectionChange: state.setSelection,
          }}
          onOpenItem={onOpenItem}
          loading={loading}
          error={errorMessage || undefined}
          onRetry={() => void onRefresh()}
          hasNextPage={hasNextPage}
          loadingMore={loadingMore}
          onLoadMore={onLoadMore ? () => void onLoadMore() : undefined}
          emptyTitle={emptyTitle}
          emptyDescription={emptyDescription}
        />
      </CollectionShell>

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Delete {state.selectedCount} selected item
              {state.selectedCount === 1 ? "" : "s"}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Only explicitly selected items will be deleted. Referenced or
              stale items will remain selected with their blocker.
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
              Delete selected
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
