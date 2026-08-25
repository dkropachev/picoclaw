import { fireEvent, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { describe, expect, it, vi } from "vitest"

import { CollectionResults } from "@/components/collection/collection-results"
import type {
  CollectionDefinition,
  CollectionView,
} from "@/components/collection/collection-types"

interface Thing {
  id: string
  name: string
  status: string
  owner: string
}

const things: Thing[] = [
  { id: "a", name: "Alpha", status: "ready", owner: "Ada" },
  { id: "b", name: "Beta", status: "blocked", owner: "Bo" },
  { id: "c", name: "Gamma", status: "ready", owner: "Cy" },
  { id: "d", name: "Delta", status: "ready", owner: "Dee" },
]

describe("CollectionResults", () => {
  it("selects rows with desktop gestures without rendering checkboxes", async () => {
    const user = userEvent.setup()
    render(<SelectableResults />)

    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()
    await user.click(item("a"))
    expect(selectedIDs()).toEqual(["a"])

    fireEvent.click(item("c"), { ctrlKey: true })
    expect(selectedIDs()).toEqual(["a", "c"])

    await user.click(item("b"))
    expect(selectedIDs()).toEqual(["b"])

    await user.click(item("a"))
    fireEvent.click(item("c"), { shiftKey: true })
    expect(selectedIDs()).toEqual(["a", "b", "c"])

    fireEvent.click(item("d"), { ctrlKey: true, shiftKey: true })
    expect(selectedIDs()).toEqual(["a", "b", "c", "d"])
  })

  it("skips ineligible rows in ranges and respects the selection bound", async () => {
    const user = userEvent.setup()
    render(
      <SelectableResults
        maximumSelected={2}
        isItemDisabled={(candidate) => candidate.id === "b"}
      />,
    )

    await user.click(item("a"))
    fireEvent.click(item("d"), { shiftKey: true })
    expect(selectedIDs()).toEqual(["a", "c"])
    await user.click(item("b"))
    expect(selectedIDs()).toEqual(["a", "c"])
  })

  it("supports keyboard selection, select-loaded, clearing, and opening", async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    render(<SelectableResults onOpenItem={onOpen} />)

    item("b").focus()
    await user.keyboard(" ")
    expect(selectedIDs()).toEqual(["b"])

    await user.keyboard("{Control>}a{/Control}")
    expect(selectedIDs()).toEqual(["a", "b", "c", "d"])

    await user.keyboard("{Escape}")
    expect(selectedIDs()).toEqual([])

    item("c").focus()
    await user.keyboard("{Enter}")
    expect(onOpen).toHaveBeenCalledWith(things[2])
  })

  it("opens on double-click and exposes item actions only by context menu", async () => {
    const user = userEvent.setup()
    const onOpen = vi.fn()
    const onInspect = vi.fn()
    render(
      <SelectableResults
        definition={thingDefinition(onInspect)}
        onOpenItem={onOpen}
      />,
    )

    expect(
      screen.queryByRole("button", { name: "Actions for Alpha" }),
    ).not.toBeInTheDocument()
    await user.dblClick(item("a"))
    expect(onOpen).toHaveBeenCalledWith(things[0])

    fireEvent.contextMenu(item("b"))
    expect(selectedIDs()).toEqual(["b"])
    await user.click(await screen.findByRole("menuitem", { name: "Inspect" }))
    expect(onInspect).toHaveBeenCalledWith(things[1])
  })

  it("uses the same selection gestures in table and grid views", () => {
    const { rerender } = render(<SelectableResults view="table" />)
    const tableRow = document.querySelector<HTMLElement>('tr[data-item-id="a"]')
    if (!tableRow) throw new Error("Missing table row")
    fireEvent.click(tableRow)
    expect(selectedIDs()).toContain("a")

    rerender(<SelectableResults view="grid" />)
    const gridCard = document.querySelector<HTMLElement>(
      'article[data-item-id="c"]',
    )
    if (!gridCard) throw new Error("Missing grid card")
    fireEvent.click(gridCard, { ctrlKey: true })
    expect(selectedIDs()).toEqual(["a", "c"])
  })

  it("renders blockers and applies the same row model to table and grid", () => {
    const { rerender } = render(
      <SelectableResults
        view="table"
        initialSelected={["b"]}
        failuresByID={
          new Map([
            [
              "b",
              {
                id: "b",
                code: "referenced",
                blockers: ["Used by agent main"],
              },
            ],
          ])
        }
      />,
    )
    expect(screen.getByRole("table")).toBeInTheDocument()
    expect(screen.getByRole("region", { name: "Things table" })).toHaveClass(
      "md:block",
    )
    expect(screen.getByRole("region", { name: "Things list" })).toHaveClass(
      "md:hidden",
    )
    expect(screen.getAllByText("Used by agent main")).toHaveLength(2)
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument()

    rerender(<SelectableResults view="grid" />)
    const grid = screen.getByRole("region", { name: "Things grid" })
    expect(grid.querySelector(".grid")).toHaveClass("grid-cols-1")
    expect(screen.getAllByText("Fact four").length).toBeGreaterThan(0)
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

function SelectableResults({
  definition = thingDefinition(),
  view = "list",
  initialSelected = [],
  failuresByID,
  maximumSelected,
  isItemDisabled,
  onOpenItem = vi.fn(),
}: {
  definition?: CollectionDefinition<Thing>
  view?: CollectionView
  initialSelected?: string[]
  failuresByID?: ReadonlyMap<
    string,
    { id: string; code: string; blockers?: string[] }
  >
  maximumSelected?: number
  isItemDisabled?: (item: Thing) => boolean
  onOpenItem?: (item: Thing) => void
}) {
  const [selected, setSelected] = useState(new Set(initialSelected))
  return (
    <CollectionResults
      definition={definition}
      items={things}
      view={view}
      onOpenItem={onOpenItem}
      selection={{
        selectedIDs: selected,
        failuresByID,
        maximumSelected,
        isItemDisabled,
        onSelectionChange: (next) => setSelected(new Set(next)),
      }}
    />
  )
}

function item(id: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`[data-item-id="${id}"]`)
  if (!element) throw new Error(`Missing collection item ${id}`)
  return element
}

function selectedIDs(): string[] {
  return Array.from(
    document.querySelectorAll<HTMLElement>("[data-state=selected]"),
  )
    .filter((element) => !element.closest(".md\\:hidden"))
    .map((element) => element.dataset.itemId ?? "")
}

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
