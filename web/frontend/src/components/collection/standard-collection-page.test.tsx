import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type { CollectionBulkDeleteResponse } from "@/api/collection"
import type { CollectionDefinition } from "@/components/collection/collection-types"
import { StandardCollectionPage } from "@/components/collection/standard-collection-page"
import { resetCollectionRouteStateMemoryForTests } from "@/hooks/use-collection-route-state"

vi.mock("@/components/page-header", () => ({
  PageHeader: ({
    title,
    children,
  }: {
    title: string
    children?: React.ReactNode
  }) => (
    <header>
      <h1>{title}</h1>
      {children}
    </header>
  ),
}))

interface Thing {
  id: string
  name: string
}

const thing: Thing = { id: "thing-1", name: "First thing" }
const definition: CollectionDefinition<Thing> = {
  key: "standard-page-test",
  title: "Things",
  defaultQuery: "ORDER BY name ASC",
  supportedViews: ["list", "table", "grid"],
  defaultView: "list",
  getItemID: (item) => item.id,
  getItemLabel: (item) => item.name,
  getItemIdentity: (item) => ({ title: item.name }),
  columns: [],
}

describe("StandardCollectionPage", () => {
  beforeEach(() => {
    resetCollectionRouteStateMemoryForTests()
    globalThis.localStorage.clear()
  })

  it("omits refresh, add, selection, deletion, paging, and opening when not configured", () => {
    renderPage()

    expect(screen.getByText("First thing")).toBeVisible()
    expect(
      screen.queryByRole("button", { name: "Refresh things" }),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull()
    expect(itemElement()).not.toHaveAttribute("tabindex")
    expect(itemElement()).not.toHaveAttribute("aria-describedby")
  })

  it("enables optional actions, paging, selection, and custom confirmation copy", async () => {
    const user = userEvent.setup()
    const onRefresh = vi.fn()
    const onLoadMore = vi.fn()
    const onBulkDelete = vi.fn().mockResolvedValue({
      deleted_ids: [thing.id],
      failures: [],
    })
    const afterBulkDelete = vi.fn()

    renderPage({
      onRefresh,
      onLoadMore,
      onBulkDelete,
      afterBulkDelete,
    })

    await user.click(screen.getByRole("button", { name: "Refresh things" }))
    await user.click(screen.getByRole("button", { name: "Add thing" }))
    await user.click(screen.getByRole("button", { name: "Load more" }))
    expect(onRefresh).toHaveBeenCalledOnce()
    expect(onLoadMore).toHaveBeenCalledOnce()

    await user.click(itemElement())
    expect(screen.getByText("1 selected")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Delete" }))
    expect(
      screen.getByRole("heading", { name: "Remove 1 thing?" }),
    ).toBeVisible()
    expect(screen.getByText("This test uses custom copy.")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Remove now" }))

    await waitFor(() => expect(onBulkDelete).toHaveBeenCalledWith([thing.id]))
    expect(afterBulkDelete).toHaveBeenCalledWith({
      deleted_ids: [thing.id],
      failures: [],
    })
  })
})

function renderPage({
  onRefresh,
  onLoadMore,
  onBulkDelete,
  afterBulkDelete,
}: {
  onRefresh?: () => void
  onLoadMore?: () => void
  onBulkDelete?: (ids: string[]) => Promise<CollectionBulkDeleteResponse>
  afterBulkDelete?: (response: CollectionBulkDeleteResponse) => void
} = {}) {
  return render(
    <StandardCollectionPage
      definition={definition}
      search={{ q: definition.defaultQuery }}
      onSearchChange={vi.fn()}
      items={[thing]}
      onRefresh={onRefresh}
      addAction={
        onRefresh ? <button type="button">Add thing</button> : undefined
      }
      hasNextPage={onLoadMore != null}
      onLoadMore={onLoadMore}
      onBulkDelete={onBulkDelete}
      afterBulkDelete={afterBulkDelete}
      bulkDeleteConfirmation={{
        title: (count) => `Remove ${count} thing?`,
        description: "This test uses custom copy.",
        actionLabel: "Remove now",
      }}
    />,
  )
}

function itemElement(): HTMLElement {
  const item = document.querySelector<HTMLElement>('[data-item-id="thing-1"]')
  if (!item) throw new Error("Missing collection item")
  return item
}
