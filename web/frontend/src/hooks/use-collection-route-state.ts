import {
  type RefCallback,
  type UIEventHandler,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import {
  type CollectionBulkDeleteFailure,
  type CollectionBulkDeleteResponse,
  maximumCollectionBulkDeleteItems,
  truncateCollectionQuery,
} from "@/api/collection"
import {
  type CollectionView,
  collectionViews,
} from "@/components/collection/collection-types"

export const maximumRecentCollectionQueries = 8

export interface CollectionRouteSearch {
  q: string
  view?: CollectionView
}

export interface CollectionRouteNormalizationOptions {
  defaultQuery: string
  supportedViews?: readonly CollectionView[]
}

export interface UseCollectionRouteStateOptions {
  collectionKey: string
  defaultQuery: string
  supportedViews?: readonly CollectionView[]
  defaultView?: CollectionView
  search: { q?: string; view?: CollectionView }
  onSearchChange: (search: CollectionRouteSearch, replace?: boolean) => void
}

interface SelectionMemory {
  selectedIDs: Set<string>
  failuresByID: Map<string, CollectionBulkDeleteFailure>
}

const selectionMemory = new Map<string, SelectionMemory>()
const scrollMemory = new Map<string, number>()
const emptySelectedIDs: ReadonlySet<string> = new Set()
const emptyFailures: ReadonlyMap<string, CollectionBulkDeleteFailure> =
  new Map()

export function normalizeCollectionRouteSearch(
  raw: object,
  options: CollectionRouteNormalizationOptions,
): CollectionRouteSearch {
  const values = raw as Record<string, unknown>
  const defaultQuery = truncateCollectionQuery(options.defaultQuery.trim())
  const query =
    typeof values.q === "string" ? truncateCollectionQuery(values.q.trim()) : ""
  const supportedViews = normalizedSupportedViews(options.supportedViews)
  const view = supportedViews.includes(values.view as CollectionView)
    ? (values.view as CollectionView)
    : undefined
  return {
    q: query || defaultQuery,
    ...(view ? { view } : {}),
  }
}

export function collectionRouteSearchIsCanonical(
  raw: object,
  normalized: CollectionRouteSearch,
): boolean {
  const values = raw as Record<string, unknown>
  const rawKeys = Object.keys(values).filter((key) => values[key] !== undefined)
  const normalizedKeys = Object.keys(normalized)
  return (
    rawKeys.length === normalizedKeys.length &&
    rawKeys.every((key) => normalizedKeys.includes(key)) &&
    values.q === normalized.q &&
    values.view === normalized.view
  )
}

export function useCollectionRouteState({
  collectionKey,
  defaultQuery,
  supportedViews: requestedViews,
  defaultView = "list",
  search,
  onSearchChange,
}: UseCollectionRouteStateOptions) {
  const supportedViews = useMemo(
    () => normalizedSupportedViews(requestedViews),
    [requestedViews],
  )
  const normalized = useMemo(
    () =>
      normalizeCollectionRouteSearch(search, {
        defaultQuery,
        supportedViews,
      }),
    [defaultQuery, search, supportedViews],
  )
  const query = normalized.q
  const memoryKey = collectionQueryMemoryKey(collectionKey, query)
  const safeDefaultView = supportedViews.includes(defaultView)
    ? defaultView
    : (supportedViews[0] ?? "list")
  const [preferredViewState, setPreferredViewState] = useState(() => ({
    key: collectionKey,
    value: readCollectionViewPreference(
      collectionKey,
      supportedViews,
      safeDefaultView,
    ),
  }))
  const preferredView =
    preferredViewState.key === collectionKey
      ? preferredViewState.value
      : readCollectionViewPreference(
          collectionKey,
          supportedViews,
          safeDefaultView,
        )
  const [recentQueriesState, setRecentQueriesState] = useState(() => ({
    key: collectionKey,
    values: readRecentCollectionQueries(collectionKey),
  }))
  const recentQueries =
    recentQueriesState.key === collectionKey
      ? recentQueriesState.values
      : readRecentCollectionQueries(collectionKey)
  const [selectionSnapshot, setSelectionSnapshot] = useState(() => {
    const memory = selectionMemory.get(memoryKey)
    return {
      key: memoryKey,
      selectedIDs: new Set(memory?.selectedIDs ?? []),
      failuresByID: new Map(memory?.failuresByID ?? []),
    }
  })
  const previousRouteRef = useRef({ collectionKey, query })
  const scrollNodeRef = useRef<HTMLDivElement | null>(null)
  const scrollFrameRef = useRef<number | null>(null)

  const selectedIDs =
    selectionSnapshot.key === memoryKey
      ? selectionSnapshot.selectedIDs
      : emptySelectedIDs
  const failuresByID =
    selectionSnapshot.key === memoryKey
      ? selectionSnapshot.failuresByID
      : emptyFailures
  const view =
    normalized.view ??
    (supportedViews.includes(preferredView) ? preferredView : safeDefaultView)

  useEffect(() => {
    const previous = previousRouteRef.current
    const collectionChanged = previous.collectionKey !== collectionKey
    const queryChanged = previous.query !== query
    if (!collectionChanged && !queryChanged) return
    previousRouteRef.current = { collectionKey, query }
    if (collectionChanged) {
      const memory = selectionMemory.get(memoryKey)
      setSelectionSnapshot({
        key: memoryKey,
        selectedIDs: new Set(memory?.selectedIDs ?? []),
        failuresByID: new Map(memory?.failuresByID ?? []),
      })
      setPreferredViewState({
        key: collectionKey,
        value: readCollectionViewPreference(
          collectionKey,
          supportedViews,
          safeDefaultView,
        ),
      })
      setRecentQueriesState({
        key: collectionKey,
        values: readRecentCollectionQueries(collectionKey),
      })
      return
    }
    clearCollectionSelectionMemory(collectionKey)
    const next = {
      key: memoryKey,
      selectedIDs: new Set<string>(),
      failuresByID: new Map<string, CollectionBulkDeleteFailure>(),
    }
    selectionMemory.set(memoryKey, {
      selectedIDs: next.selectedIDs,
      failuresByID: next.failuresByID,
    })
    setSelectionSnapshot(next)
  }, [collectionKey, memoryKey, query, safeDefaultView, supportedViews])

  useEffect(
    () => () => {
      if (scrollFrameRef.current != null) {
        cancelAnimationFrame(scrollFrameRef.current)
      }
      if (scrollNodeRef.current) {
        scrollMemory.set(memoryKey, scrollNodeRef.current.scrollTop)
      }
    },
    [memoryKey],
  )

  const writeSelection = useCallback(
    (
      nextIDs: Set<string>,
      nextFailures: Map<string, CollectionBulkDeleteFailure>,
    ) => {
      const next = {
        key: memoryKey,
        selectedIDs: nextIDs,
        failuresByID: nextFailures,
      }
      selectionMemory.set(memoryKey, {
        selectedIDs: new Set(nextIDs),
        failuresByID: new Map(nextFailures),
      })
      setSelectionSnapshot(next)
    },
    [memoryKey],
  )

  const setSelection = useCallback(
    (ids: Iterable<string>) => {
      const nextIDs = new Set<string>()
      for (const id of ids) {
        if (nextIDs.size >= maximumCollectionBulkDeleteItems) break
        nextIDs.add(id)
      }
      const current = selectionMemory.get(memoryKey)
      const nextFailures = new Map<string, CollectionBulkDeleteFailure>()
      for (const [id, failure] of current?.failuresByID ?? failuresByID) {
        if (nextIDs.has(id)) nextFailures.set(id, failure)
      }
      writeSelection(nextIDs, nextFailures)
    },
    [failuresByID, memoryKey, writeSelection],
  )

  const clearSelection = useCallback(() => {
    writeSelection(new Set(), new Map())
  }, [writeSelection])

  const applyQuery = useCallback(
    (nextQuery: string) => {
      const next =
        truncateCollectionQuery(nextQuery.trim()) ||
        truncateCollectionQuery(defaultQuery.trim())
      if (next !== query) {
        clearCollectionSelectionMemory(collectionKey)
        setSelectionSnapshot({
          key: collectionQueryMemoryKey(collectionKey, next),
          selectedIDs: new Set(),
          failuresByID: new Map(),
        })
      }
      onSearchChange(
        { q: next, ...(normalized.view ? { view: normalized.view } : {}) },
        false,
      )
    },
    [collectionKey, defaultQuery, normalized.view, onSearchChange, query],
  )

  const setView = useCallback(
    (nextView: CollectionView) => {
      if (!supportedViews.includes(nextView)) return
      setPreferredViewState({ key: collectionKey, value: nextView })
      writeCollectionViewPreference(collectionKey, nextView)
      onSearchChange({ q: query, view: nextView }, true)
    },
    [collectionKey, onSearchChange, query, supportedViews],
  )

  const rememberSuccessfulQuery = useCallback(
    (successfulQuery = query) => {
      const nextQuery =
        truncateCollectionQuery(successfulQuery.trim()) ||
        truncateCollectionQuery(defaultQuery.trim())
      setRecentQueriesState((current) => {
        const currentValues =
          current.key === collectionKey
            ? current.values
            : readRecentCollectionQueries(collectionKey)
        const next = [
          nextQuery,
          ...currentValues.filter((candidate) => candidate !== nextQuery),
        ].slice(0, maximumRecentCollectionQueries)
        writeRecentCollectionQueries(collectionKey, next)
        return { key: collectionKey, values: next }
      })
    },
    [collectionKey, defaultQuery, query],
  )

  const commitQuerySuccess = useCallback(
    (canonicalQuery: string) => {
      const canonical =
        truncateCollectionQuery(canonicalQuery.trim()) ||
        truncateCollectionQuery(defaultQuery.trim())
      rememberSuccessfulQuery(canonical)
      if (canonical !== query) {
        onSearchChange(
          {
            q: canonical,
            ...(normalized.view ? { view: normalized.view } : {}),
          },
          true,
        )
      }
    },
    [
      defaultQuery,
      normalized.view,
      onSearchChange,
      query,
      rememberSuccessfulQuery,
    ],
  )

  const clearHistory = useCallback(() => {
    setRecentQueriesState({ key: collectionKey, values: [] })
    writeRecentCollectionQueries(collectionKey, [])
  }, [collectionKey])

  const setItemSelected = useCallback(
    (id: string, checked: boolean) => {
      const current = selectionMemory.get(memoryKey)
      const nextIDs = new Set(current?.selectedIDs ?? selectedIDs)
      const nextFailures = new Map(current?.failuresByID ?? failuresByID)
      if (
        checked &&
        (nextIDs.has(id) || nextIDs.size < maximumCollectionBulkDeleteItems)
      ) {
        nextIDs.add(id)
      } else nextIDs.delete(id)
      nextFailures.delete(id)
      writeSelection(nextIDs, nextFailures)
    },
    [failuresByID, memoryKey, selectedIDs, writeSelection],
  )

  const toggleSelection = useCallback(
    (id: string) => setItemSelected(id, !selectedIDs.has(id)),
    [selectedIDs, setItemSelected],
  )

  const setLoadedSelection = useCallback(
    (ids: readonly string[], checked: boolean) => {
      const current = selectionMemory.get(memoryKey)
      const nextIDs = new Set(current?.selectedIDs ?? selectedIDs)
      const nextFailures = new Map(current?.failuresByID ?? failuresByID)
      for (const id of ids) {
        if (checked) {
          if (
            !nextIDs.has(id) &&
            nextIDs.size >= maximumCollectionBulkDeleteItems
          ) {
            break
          }
          nextIDs.add(id)
        } else {
          nextIDs.delete(id)
        }
        nextFailures.delete(id)
      }
      writeSelection(nextIDs, nextFailures)
    },
    [failuresByID, memoryKey, selectedIDs, writeSelection],
  )

  const reconcileBulkDelete = useCallback(
    (response: CollectionBulkDeleteResponse) => {
      const current = selectionMemory.get(memoryKey)
      const nextIDs = new Set(current?.selectedIDs ?? selectedIDs)
      for (const id of response.deleted_ids) nextIDs.delete(id)
      const nextFailures = new Map<string, CollectionBulkDeleteFailure>()
      for (const failure of response.failures) {
        if (nextIDs.has(failure.id)) nextFailures.set(failure.id, failure)
      }
      writeSelection(nextIDs, nextFailures)
      return nextFailures
    },
    [memoryKey, selectedIDs, writeSelection],
  )

  const rememberScrollPosition = useCallback(
    (top: number) => {
      if (!Number.isFinite(top)) return
      scrollMemory.set(memoryKey, Math.max(0, top))
    },
    [memoryKey],
  )

  const restoreScrollPosition = useCallback(() => {
    if (!scrollNodeRef.current) return
    scrollNodeRef.current.scrollTop = scrollMemory.get(memoryKey) ?? 0
  }, [memoryKey])

  const setScrollContainerRef = useCallback<RefCallback<HTMLDivElement>>(
    (node) => {
      scrollNodeRef.current = node
      if (!node) return
      if (scrollFrameRef.current != null) {
        cancelAnimationFrame(scrollFrameRef.current)
      }
      scrollFrameRef.current = requestAnimationFrame(() => {
        scrollFrameRef.current = null
        if (scrollNodeRef.current === node) restoreScrollPosition()
      })
    },
    [restoreScrollPosition],
  )

  const onResultsScroll = useCallback<UIEventHandler<HTMLDivElement>>(
    (event) => rememberScrollPosition(event.currentTarget.scrollTop),
    [rememberScrollPosition],
  )

  return {
    query,
    view,
    supportedViews,
    recentQueries,
    selectedIDs,
    selectedCount: selectedIDs.size,
    selectionLimit: maximumCollectionBulkDeleteItems,
    selectionLimitReached: selectedIDs.size >= maximumCollectionBulkDeleteItems,
    failuresByID,
    applyQuery,
    setView,
    rememberSuccessfulQuery,
    commitQuerySuccess,
    clearHistory,
    setSelection,
    setItemSelected,
    toggleSelection,
    setLoadedSelection,
    clearSelection,
    reconcileBulkDelete,
    setScrollContainerRef,
    onResultsScroll,
    rememberScrollPosition,
    restoreScrollPosition,
  }
}

export function reconcileCollectionItemsAfterBulkDelete<T>(
  items: readonly T[],
  response: CollectionBulkDeleteResponse,
  getItemID: (item: T) => string,
): T[] {
  const deleted = new Set(response.deleted_ids)
  return items.filter((item) => !deleted.has(getItemID(item)))
}

export function resetCollectionRouteStateMemoryForTests(): void {
  selectionMemory.clear()
  scrollMemory.clear()
}

export function removeItemFromCollectionSelectionMemory(
  collectionKey: string,
  itemID: string,
): void {
  const prefix = `${collectionKey}\u0000`
  for (const [key, memory] of selectionMemory) {
    if (!key.startsWith(prefix)) continue
    memory.selectedIDs.delete(itemID)
    memory.failuresByID.delete(itemID)
  }
}

function normalizedSupportedViews(
  requested?: readonly CollectionView[],
): CollectionView[] {
  const values = (requested ?? collectionViews).filter(
    (view, index, all) =>
      collectionViews.includes(view) && all.indexOf(view) === index,
  )
  return values.length > 0 ? [...values] : ["list"]
}

function collectionQueryMemoryKey(
  collectionKey: string,
  query: string,
): string {
  return `${collectionKey}\u0000${query}`
}

function clearCollectionSelectionMemory(collectionKey: string): void {
  const prefix = `${collectionKey}\u0000`
  for (const key of selectionMemory.keys()) {
    if (key.startsWith(prefix)) selectionMemory.delete(key)
  }
}

function collectionStorageKey(
  collectionKey: string,
  suffix: "recent-queries" | "view",
): string {
  return `picoclaw:collection:${encodeURIComponent(collectionKey)}:${suffix}`
}

function readCollectionViewPreference(
  collectionKey: string,
  supportedViews: readonly CollectionView[],
  fallback: CollectionView,
): CollectionView {
  try {
    const value = globalThis.localStorage?.getItem(
      collectionStorageKey(collectionKey, "view"),
    ) as CollectionView | null
    return value && supportedViews.includes(value) ? value : fallback
  } catch {
    return fallback
  }
}

function writeCollectionViewPreference(
  collectionKey: string,
  view: CollectionView,
): void {
  try {
    globalThis.localStorage?.setItem(
      collectionStorageKey(collectionKey, "view"),
      view,
    )
  } catch {
    // Storage can be unavailable or full; route state remains authoritative.
  }
}

function readRecentCollectionQueries(collectionKey: string): string[] {
  try {
    const raw = globalThis.localStorage?.getItem(
      collectionStorageKey(collectionKey, "recent-queries"),
    )
    const values = raw ? (JSON.parse(raw) as unknown) : []
    if (!Array.isArray(values)) return []
    return values
      .filter((value): value is string => typeof value === "string")
      .map((value) => truncateCollectionQuery(value.trim()))
      .filter(
        (value, index, all) => value !== "" && all.indexOf(value) === index,
      )
      .slice(0, maximumRecentCollectionQueries)
  } catch {
    return []
  }
}

function writeRecentCollectionQueries(
  collectionKey: string,
  queries: readonly string[],
): void {
  try {
    globalThis.localStorage?.setItem(
      collectionStorageKey(collectionKey, "recent-queries"),
      JSON.stringify(queries.slice(0, maximumRecentCollectionQueries)),
    )
  } catch {
    // Storage can be unavailable or full; in-memory recents still work.
  }
}
