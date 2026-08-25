import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { maximumCollectionBulkDeleteItems } from "@/api/collection"
import {
  type CollectionRouteSearch,
  collectionRouteSearchIsCanonical,
  maximumRecentCollectionQueries,
  normalizeCollectionRouteSearch,
  reconcileCollectionItemsAfterBulkDelete,
  resetCollectionRouteStateMemoryForTests,
  useCollectionRouteState,
} from "@/hooks/use-collection-route-state"

const views = ["list", "table", "grid"] as const

describe("collection route state", () => {
  beforeEach(() => {
    localStorage.clear()
    resetCollectionRouteStateMemoryForTests()
  })

  it("normalizes URL query and view values canonically", () => {
    const normalized = normalizeCollectionRouteSearch(
      { q: "  status = ready  ", view: "grid" },
      { defaultQuery: "ORDER BY updated DESC", supportedViews: views },
    )
    expect(normalized).toEqual({ q: "status = ready", view: "grid" })
    expect(
      collectionRouteSearchIsCanonical(
        { q: "status = ready", view: "grid" },
        normalized,
      ),
    ).toBe(true)
    expect(
      collectionRouteSearchIsCanonical(
        { q: "status = ready", view: "grid", legacy: "value" },
        normalized,
      ),
    ).toBe(false)
    expect(
      normalizeCollectionRouteSearch(
        { q: "   ", view: "unsupported" },
        { defaultQuery: "ORDER BY updated DESC", supportedViews: views },
      ),
    ).toEqual({ q: "ORDER BY updated DESC" })
  })

  it("remembers a preferred view while a URL view remains authoritative", () => {
    const onSearchChange = vi.fn()
    const first = renderCollectionState({ onSearchChange })
    expect(first.result.current.view).toBe("list")
    act(() => first.result.current.setView("grid"))
    expect(first.result.current.view).toBe("grid")
    expect(onSearchChange).toHaveBeenLastCalledWith(
      { q: "status = ready", view: "grid" },
      true,
    )
    first.unmount()

    const remembered = renderCollectionState({ onSearchChange })
    expect(remembered.result.current.view).toBe("grid")
    remembered.unmount()

    const overridden = renderCollectionState({
      onSearchChange,
      search: { q: "status = ready", view: "table" },
    })
    expect(overridden.result.current.view).toBe("table")
  })

  it("stores at most eight successful, deduplicated recent queries", () => {
    const state = renderCollectionState()
    act(() => {
      for (let index = 0; index < 10; index += 1) {
        state.result.current.rememberSuccessfulQuery(`status = q${index}`)
      }
    })
    expect(state.result.current.recentQueries).toHaveLength(
      maximumRecentCollectionQueries,
    )
    expect(state.result.current.recentQueries).toEqual([
      "status = q9",
      "status = q8",
      "status = q7",
      "status = q6",
      "status = q5",
      "status = q4",
      "status = q3",
      "status = q2",
    ])
    act(() => state.result.current.rememberSuccessfulQuery("status = q5"))
    expect(state.result.current.recentQueries[0]).toBe("status = q5")
    expect(new Set(state.result.current.recentQueries).size).toBe(
      state.result.current.recentQueries.length,
    )
    act(() => state.result.current.clearHistory())
    expect(state.result.current.recentQueries).toEqual([])
  })

  it("restores selection on same-query remount and clears it on query change", async () => {
    const first = renderCollectionState()
    act(() => first.result.current.setLoadedSelection(["a", "b"], true))
    expect([...first.result.current.selectedIDs]).toEqual(["a", "b"])
    first.unmount()

    const second = renderCollectionState()
    expect([...second.result.current.selectedIDs]).toEqual(["a", "b"])
    second.rerender({ search: { q: "status = blocked" } })
    await waitFor(() => expect(second.result.current.selectedCount).toBe(0))
    second.unmount()

    const oldQuery = renderCollectionState()
    expect(oldQuery.result.current.selectedCount).toBe(0)
  })

  it("keeps failed deletes selected and removes only confirmed deleted rows", () => {
    const state = renderCollectionState()
    act(() => state.result.current.setLoadedSelection(["a", "b", "c"], true))
    const response = {
      deleted_ids: ["a"],
      failures: [
        { id: "b", code: "referenced", blockers: ["Used by agent main"] },
      ],
    }
    act(() => state.result.current.reconcileBulkDelete(response))
    expect([...state.result.current.selectedIDs]).toEqual(["b", "c"])
    expect(state.result.current.failuresByID.get("b")).toEqual(
      response.failures[0],
    )
    expect(
      reconcileCollectionItemsAfterBulkDelete(
        [{ id: "a" }, { id: "b" }, { id: "c" }],
        response,
        (item) => item.id,
      ),
    ).toEqual([{ id: "b" }, { id: "c" }])
  })

  it("never builds a bulk selection larger than the API bound", () => {
    const state = renderCollectionState()
    const ids = Array.from(
      { length: maximumCollectionBulkDeleteItems + 5 },
      (_, index) => `item-${index}`,
    )
    act(() => state.result.current.setLoadedSelection(ids, true))
    expect(state.result.current.selectedCount).toBe(
      maximumCollectionBulkDeleteItems,
    )
    expect(state.result.current.selectionLimitReached).toBe(true)
    act(() => state.result.current.setItemSelected("one-more", true))
    expect(state.result.current.selectedCount).toBe(
      maximumCollectionBulkDeleteItems,
    )
  })

  it("replaces selection atomically and retains blockers only for selected IDs", () => {
    const state = renderCollectionState()
    act(() => state.result.current.setLoadedSelection(["a", "b"], true))
    act(() =>
      state.result.current.reconcileBulkDelete({
        deleted_ids: [],
        failures: [{ id: "a", code: "referenced", blockers: ["Used by main"] }],
      }),
    )
    act(() => state.result.current.setSelection(new Set(["a", "c"])))
    expect([...state.result.current.selectedIDs]).toEqual(["a", "c"])
    expect(state.result.current.failuresByID.has("a")).toBe(true)
    expect(state.result.current.failuresByID.has("b")).toBe(false)
  })

  it("restores in-memory scroll without making it durable", () => {
    const callbacks: FrameRequestCallback[] = []
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callbacks.push(callback)
      return callbacks.length
    })
    vi.stubGlobal("cancelAnimationFrame", vi.fn())
    const state = renderCollectionState()
    act(() => state.result.current.rememberScrollPosition(84))
    const node = document.createElement("div")
    act(() => state.result.current.setScrollContainerRef(node))
    act(() => callbacks.splice(0).forEach((callback) => callback(0)))
    expect(node.scrollTop).toBe(84)
    state.unmount()
    expect(localStorage.getItem("scroll")).toBeNull()
    vi.unstubAllGlobals()
  })
})

function renderCollectionState({
  search = { q: "status = ready" } as {
    q?: string
    view?: "list" | "table" | "grid"
  },
  onSearchChange = vi.fn(),
}: {
  search?: { q?: string; view?: "list" | "table" | "grid" }
  onSearchChange?: (search: CollectionRouteSearch, replace?: boolean) => void
} = {}) {
  return renderHook(
    ({ search: currentSearch }) =>
      useCollectionRouteState({
        collectionKey: "test-items",
        defaultQuery: "ORDER BY updated DESC",
        supportedViews: views,
        search: currentSearch,
        onSearchChange,
      }),
    { initialProps: { search } },
  )
}
