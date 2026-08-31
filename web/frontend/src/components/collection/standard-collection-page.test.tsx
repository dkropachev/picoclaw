import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import type {
  CollectionBulkDeleteResponse,
  CollectionQuerySchema,
} from "@/api/collection"
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
const querySchema: CollectionQuerySchema = {
  fields: [
    {
      name: "status",
      type: "enum",
      operators: ["=", "!=", "IN", "NOT IN"],
      sortable: true,
      suggested_values: ["ready", "blocked"],
    },
    {
      name: "name",
      type: "string",
      operators: ["=", "!=", "~", "!~"],
      sortable: true,
    },
  ],
}
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

  it("renders nested context, leading content, and feature selection actions without deletion", async () => {
    const user = userEvent.setup()
    const onBack = vi.fn()
    const onOperate = vi.fn()

    render(
      <StandardCollectionPage
        definition={definition}
        search={{ q: definition.defaultQuery }}
        onSearchChange={vi.fn()}
        items={[thing]}
        context={{
          backLabel: "Parent collection",
          onBack,
          identity: "Parent identity",
          status: "ready",
        }}
        beforeResults={<div>Scoped collection notice</div>}
        selection={{
          maximumSelected: 1,
          renderActions: ({ selectedIDs, clearSelection }) => (
            <button
              type="button"
              onClick={() => {
                onOperate([...selectedIDs])
                clearSelection()
              }}
            >
              Operate on selection
            </button>
          ),
        }}
      />,
    )

    expect(screen.getByText("Parent identity")).toBeVisible()
    expect(screen.getByText("ready")).toBeVisible()
    expect(screen.getByText("Scoped collection notice")).toBeVisible()
    await user.click(screen.getByRole("button", { name: "Parent collection" }))
    expect(onBack).toHaveBeenCalledOnce()

    await user.click(itemElement())
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull()
    await user.click(
      screen.getByRole("button", { name: "Operate on selection" }),
    )
    expect(onOperate).toHaveBeenCalledWith([thing.id])
    expect(screen.queryByText("1 selected")).toBeNull()
  })

  it("does not change custom selection while a feature action is pending", async () => {
    const user = userEvent.setup()
    render(
      <StandardCollectionPage
        definition={definition}
        search={{ q: definition.defaultQuery }}
        onSearchChange={vi.fn()}
        items={[thing]}
        selection={{ disabled: true, renderActions: () => null }}
      />,
    )

    await user.click(itemElement())
    expect(screen.queryByText("1 selected")).toBeNull()
  })

  it("retains feature-operation failures with their safe inline message", async () => {
    const user = userEvent.setup()
    render(
      <StandardCollectionPage
        definition={definition}
        search={{ q: definition.defaultQuery }}
        onSearchChange={vi.fn()}
        items={[thing]}
        selection={{
          renderActions: ({ reconcileSelection }) => (
            <button
              type="button"
              onClick={() =>
                reconcileSelection({
                  deleted_ids: [],
                  failures: [
                    {
                      id: thing.id,
                      code: "provider_failed",
                      blockers: ["The provider is temporarily unavailable."],
                    },
                  ],
                })
              }
            >
              Retry selected
            </button>
          ),
        }}
      />,
    )

    await user.click(itemElement())
    await user.click(screen.getByRole("button", { name: "Retry selected" }))

    expect(screen.getByText("1 selected")).toBeVisible()
    expect(
      screen.getByText("The provider is temporarily unavailable."),
    ).toBeVisible()
  })

  it("carries schema, structured errors, defaults, and apply through the shared editor chain", async () => {
    const user = userEvent.setup()
    const onSearchChange = vi.fn()
    render(
      <StandardCollectionPage
        definition={definition}
        search={{ q: "sta" }}
        onSearchChange={onSearchChange}
        items={[thing]}
        schema={querySchema}
        canonicalQuery="sta"
        error={{ message: "Unknown query field", position: 0 }}
      />,
    )

    const input = screen.getByRole("combobox", {
      name: "Collection query",
    }) as HTMLInputElement
    expect(
      document.getElementById(input.getAttribute("aria-errormessage")!),
    ).toHaveTextContent("Character 1: Unknown query field")
    await user.click(input)
    input.setSelectionRange(input.value.length, input.value.length)
    fireEvent.select(input)
    await user.keyboard("{ArrowDown}{Enter}{Enter}")
    expect(onSearchChange).toHaveBeenLastCalledWith({ q: "status" }, false)

    await user.click(screen.getByRole("button", { name: "Clear query" }))
    expect(onSearchChange).toHaveBeenLastCalledWith(
      { q: definition.defaultQuery },
      false,
    )
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
