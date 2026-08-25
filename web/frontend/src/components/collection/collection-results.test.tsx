import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { CollectionResults } from "@/components/collection/collection-results"
import type { CollectionDefinition } from "@/components/collection/collection-types"

interface Thing {
  id: string
  name: string
  status: string
  owner: string
}

const things: Thing[] = [
  { id: "a", name: "Alpha", status: "ready", owner: "Ada" },
  { id: "b", name: "Beta", status: "blocked", owner: "Bo" },
]

describe("CollectionResults", () => {
  it("renders compact selectable rows, blockers, open, and item actions", async () => {
    const user = userEvent.setup()
    const onItemChange = vi.fn()
    const onLoadedChange = vi.fn()
    const onOpen = vi.fn()
    const onInspect = vi.fn()
    const definition = thingDefinition(onInspect)
    render(
      <CollectionResults
        definition={definition}
        items={things}
        view="list"
        onOpenItem={onOpen}
        selection={{
          selectedIDs: new Set(["b"]),
          failuresByID: new Map([
            [
              "b",
              {
                id: "b",
                code: "referenced",
                blockers: ["Used by agent main"],
              },
            ],
          ]),
          onItemChange,
          onLoadedChange,
        }}
      />,
    )

    await user.click(screen.getByRole("checkbox", { name: "Select Alpha" }))
    expect(onItemChange).toHaveBeenCalledWith(things[0], true)
    await user.click(
      screen.getByRole("checkbox", { name: "Select all loaded things" }),
    )
    expect(onLoadedChange).toHaveBeenCalledWith(things, true)
    expect(screen.getByText("Used by agent main")).toBeVisible()

    await user.click(screen.getByRole("button", { name: "Alpha" }))
    expect(onOpen).toHaveBeenCalledWith(things[0])
    await user.click(screen.getByRole("button", { name: "Actions for Alpha" }))
    await user.click(screen.getByRole("menuitem", { name: "Inspect" }))
    expect(onInspect).toHaveBeenCalledWith(things[0])
  })

  it("provides a desktop table and a mobile row fallback", () => {
    render(
      <CollectionResults
        definition={thingDefinition()}
        items={things}
        view="table"
      />,
    )
    expect(screen.getByRole("table")).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Things table" })).toHaveClass(
      "md:block",
    )
    expect(screen.getByRole("region", { name: "Things list" })).toHaveClass(
      "md:hidden",
    )
    expect(screen.getByRole("columnheader", { name: "Owner" })).toBeVisible()
  })

  it("excludes resource-ineligible rows from individual and loaded selection", async () => {
    const user = userEvent.setup()
    const onLoadedChange = vi.fn()
    render(
      <CollectionResults
        definition={thingDefinition()}
        items={things}
        view="list"
        selection={{
          selectedIDs: new Set(),
          isItemDisabled: (item) => item.status !== "ready",
          onItemChange: vi.fn(),
          onLoadedChange,
        }}
      />,
    )
    expect(screen.getByRole("checkbox", { name: "Select Beta" })).toBeDisabled()
    await user.click(
      screen.getByRole("checkbox", { name: "Select all loaded things" }),
    )
    expect(onLoadedChange).toHaveBeenCalledWith([things[0]], true)
  })

  it("renders one-column-first grids with at most four summary facts", () => {
    render(
      <CollectionResults
        definition={thingDefinition()}
        items={[things[0]]}
        view="grid"
      />,
    )
    const grid = screen.getByRole("region", { name: "Things grid" })
    expect(grid.querySelector(".grid")).toHaveClass("grid-cols-1")
    expect(screen.getByText("Fact four")).toBeVisible()
    expect(screen.queryByText("Fact five")).not.toBeInTheDocument()
  })

  it("uses shared loading, error, and empty states", () => {
    const definition = thingDefinition()
    const { rerender } = render(
      <CollectionResults
        definition={definition}
        items={[]}
        view="list"
        loading
      />,
    )
    expect(
      screen.getByRole("status", { name: "Loading collection" }),
    ).toBeVisible()
    rerender(
      <CollectionResults
        definition={definition}
        items={[]}
        view="list"
        error="Server unavailable"
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent("Server unavailable")
    rerender(
      <CollectionResults definition={definition} items={[]} view="list" />,
    )
    expect(screen.getByRole("status")).toHaveTextContent("No items found")
  })
})

function thingDefinition(onInspect = vi.fn()): CollectionDefinition<Thing> {
  return {
    key: "things",
    title: "Things",
    defaultQuery: "ORDER BY name ASC",
    getItemID: (item) => item.id,
    getItemLabel: (item) => item.name,
    getItemIdentity: (item) => ({
      title: item.name,
      description: item.id,
      metadata: `Owned by ${item.owner}`,
    }),
    columns: [
      { id: "owner", header: "Owner", cell: (item) => item.owner },
      { id: "status", header: "Status", cell: (item) => item.status },
    ],
    gridFacts: [
      { id: "one", label: "Fact one", value: (item) => item.owner },
      { id: "two", label: "Fact two", value: (item) => item.status },
      { id: "three", label: "Fact three", value: () => "Three" },
      { id: "four", label: "Fact four", value: () => "Four" },
      { id: "five", label: "Fact five", value: () => "Five" },
    ],
    badges: [
      {
        id: "status",
        label: (item) => item.status,
        variant: "outline",
      },
    ],
    actions: [{ id: "inspect", label: "Inspect", onSelect: onInspect }],
    supportedViews: ["list", "table", "grid"],
  }
}
