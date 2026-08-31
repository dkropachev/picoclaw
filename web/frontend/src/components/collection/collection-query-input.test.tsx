import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import {
  type CollectionQuerySchema,
  collectionQueryByteLength,
  maximumCollectionQueryBytes,
} from "@/api/collection"
import { CollectionQueryInput } from "@/components/collection/collection-query-input"

const schema: CollectionQuerySchema = {
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
      suggested_values: ["sample value"],
    },
    {
      name: "enabled",
      type: "boolean",
      operators: ["=", "!="],
      sortable: false,
    },
    {
      name: "updated",
      type: "timestamp",
      operators: ["=", ">", ">="],
      sortable: true,
    },
  ],
}

const scrollIntoView = vi.fn()

describe("CollectionQueryInput", () => {
  beforeEach(() => {
    scrollIntoView.mockReset()
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    })
  })

  it("supports suggestion acceptance, Enter apply, Escape, and the latest default", async () => {
    const user = userEvent.setup()
    const onApply = vi.fn()
    const { rerender } = render(
      <CollectionQueryInput
        activeQuery="status = ready"
        defaultQuery="ORDER BY updated DESC"
        schema={schema}
        onApply={onApply}
      />,
    )
    const input = queryInput()
    await user.clear(input)
    await user.type(input, "sta")
    expect(input).toHaveAttribute("aria-autocomplete", "list")
    expect(input).toHaveAttribute("aria-expanded", "true")
    expect(
      screen.getByRole("listbox", { name: "Collection query suggestions" }),
    ).toBeVisible()
    await user.keyboard("{ArrowDown}{Enter}")
    expect(input).toHaveValue("status ")

    await user.clear(input)
    await user.type(input, "enabled = true{Enter}")
    expect(onApply).toHaveBeenLastCalledWith("enabled = true")

    await user.clear(input)
    await user.type(input, "temporary")
    await user.keyboard("{Escape}")
    expect(input).toHaveValue("status = ready")
    expect(input).toHaveFocus()

    rerender(
      <CollectionQueryInput
        activeQuery="status = ready"
        defaultQuery="ORDER BY name ASC"
        schema={schema}
        onApply={onApply}
      />,
    )
    await user.click(screen.getByRole("button", { name: "Clear query" }))
    expect(input).toHaveValue("ORDER BY name ASC")
    expect(onApply).toHaveBeenLastCalledWith("ORDER BY name ASC")
  })

  it("wraps keyboard navigation, reopens with Ctrl+Space, and accepts with Tab", async () => {
    const user = userEvent.setup()
    renderQueryInput({ activeQuery: "" })
    const input = queryInput()
    await user.click(input)

    await user.keyboard("{ArrowUp}")
    const lastActive = input.getAttribute("aria-activedescendant")
    expect(lastActive).toBeTruthy()
    expect(document.getElementById(lastActive!)).toHaveAttribute(
      "aria-selected",
      "true",
    )
    expect(scrollIntoView).toHaveBeenCalled()

    await user.keyboard("{ArrowDown}")
    const firstActive = input.getAttribute("aria-activedescendant")
    expect(firstActive).not.toBe(lastActive)
    expect(document.getElementById(firstActive!)).toHaveTextContent("status")

    await user.keyboard("{Escape}")
    expect(input).toHaveAttribute("aria-expanded", "false")
    await user.keyboard("{Control>} {/Control}")
    expect(input).toHaveAttribute("aria-expanded", "true")
    expect(input).toHaveAttribute("aria-activedescendant")
    await user.keyboard("{Tab}")
    expect(input).toHaveValue("status ")
    expect(input).toHaveFocus()
  })

  it("supports full selection replacement and restores the DOM caret", async () => {
    const user = userEvent.setup()
    renderQueryInput({ activeQuery: "status = ready AND na = ignored" })
    const input = queryInput()
    await user.click(input)
    input.setSelectionRange(19, 21)
    fireEvent.select(input)
    await user.click(screen.getByRole("option", { name: /name, string field/ }))

    expect(input).toHaveValue("status = ready AND name = ignored")
    expect(input.selectionStart).toBe(24)
    expect(input.selectionEnd).toBe(24)
    expect(input).toHaveFocus()
  })

  it("handles mouse selection, unchanged values, and blur", async () => {
    const user = userEvent.setup()
    renderQueryInput({ activeQuery: "status = ready " })
    const input = queryInput()
    await user.click(input)
    input.setSelectionRange(14, 14)
    fireEvent.select(input)

    const ready = screen.getByRole("option", { name: /ready, enum value/ })
    await user.hover(ready)
    expect(ready).toHaveAttribute("aria-selected", "true")
    await user.click(ready)
    expect(input).toHaveValue("status = ready ")
    expect(input.selectionStart).toBe(15)
    expect(input).toHaveFocus()

    fireEvent.blur(input)
    expect(input).toHaveAttribute("aria-expanded", "false")
    expect(
      screen.queryByRole("listbox", {
        name: "Collection query suggestions",
      }),
    ).not.toBeInTheDocument()
  })

  it("clears an active option when the caret moves to a new context", async () => {
    const user = userEvent.setup()
    renderQueryInput({ activeQuery: "status " })
    const input = queryInput()
    await user.click(input)
    await user.keyboard("{ArrowDown}")
    expect(input).toHaveAttribute("aria-activedescendant")

    input.setSelectionRange(0, 0)
    fireEvent.select(input)
    expect(input).not.toHaveAttribute("aria-activedescendant")
  })

  it("ignores autocomplete and application shortcuts during IME composition", () => {
    const onApply = vi.fn()
    renderQueryInput({ activeQuery: "", onApply })
    const input = queryInput()
    input.focus()
    fireEvent.compositionStart(input)

    fireEvent.keyDown(input, { key: "ArrowDown", isComposing: true })
    fireEvent.keyDown(input, { key: "Tab", isComposing: true })
    fireEvent.keyDown(input, {
      key: " ",
      ctrlKey: true,
      isComposing: true,
    })
    fireEvent.keyDown(input, { key: "Enter", isComposing: true })
    fireEvent.submit(input.closest("form")!)

    expect(input).not.toHaveAttribute("aria-activedescendant")
    expect(input).toHaveValue("")
    expect(onApply).not.toHaveBeenCalled()

    fireEvent.compositionEnd(input)
    expect(input).toHaveAttribute("aria-expanded", "true")
  })

  it("truncates pasted multibyte text only at Unicode boundaries", () => {
    renderQueryInput({ activeQuery: "" })
    const input = queryInput()
    const pasted = "💡".repeat(1025)
    fireEvent.change(input, {
      target: {
        value: pasted,
        selectionStart: pasted.length,
        selectionEnd: pasted.length,
      },
    })

    expect(collectionQueryByteLength(input.value)).toBe(
      maximumCollectionQueryBytes,
    )
    expect(Array.from(input.value)).toHaveLength(1024)
    expect(input.value.endsWith("💡")).toBe(true)
    expect(input.selectionStart).toBe(input.value.length)
    expect(input.selectionEnd).toBe(input.value.length)
    expect(screen.getByText("4096/4096")).toBeVisible()
  })

  it("rejects over-limit suggestion insertion without losing text or selection", async () => {
    const user = userEvent.setup()
    const activeQuery = `name = "${"x".repeat(4081)}" AND `
    expect(collectionQueryByteLength(activeQuery)).toBe(4095)
    renderQueryInput({ activeQuery })
    const input = queryInput()
    await user.click(input)
    await user.keyboard("{ArrowDown}{Enter}")

    expect(input).toHaveValue(activeQuery)
    expect(collectionQueryByteLength(input.value)).toBe(4095)
    expect(input.selectionStart).toBe(activeQuery.length)
    expect(input).toHaveAttribute("aria-expanded", "false")
  })

  it("keeps help, error, and count text associated through ARIA", () => {
    const { rerender } = renderQueryInput({ activeQuery: "" })
    const input = queryInput()
    let described = input.getAttribute("aria-describedby")!.split(" ")
    expect(described).toHaveLength(2)
    expect(document.getElementById(described[0]!)).toHaveTextContent(
      "Enter applies",
    )
    expect(document.getElementById(described[1]!)).toHaveTextContent("0/4096")

    rerender(
      <CollectionQueryInput
        activeQuery="status = nope"
        defaultQuery=""
        schema={schema}
        error={{ message: "Unexpected value", position: 9 }}
        onApply={() => undefined}
      />,
    )
    described = input.getAttribute("aria-describedby")!.split(" ")
    const alert = screen.getByRole("alert")
    expect(described).toContain(alert.id)
    expect(input).toHaveAttribute("aria-errormessage", alert.id)
    expect(input).toHaveAttribute("aria-invalid", "true")
  })

  it("shows an error only for its rejected active draft", async () => {
    const user = userEvent.setup()
    const rejectedError = { message: "Unexpected value", position: 9 }
    const { rerender } = render(
      <CollectionQueryInput
        activeQuery="status = nope"
        defaultQuery=""
        schema={schema}
        error={rejectedError}
        onApply={() => undefined}
      />,
    )
    const input = queryInput()
    expect(screen.getByRole("alert")).toBeVisible()

    await user.click(input)
    await user.type(input, "x")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(input).toHaveAttribute("aria-invalid", "false")

    await user.keyboard("{Escape}")
    expect(screen.getByRole("alert")).toBeVisible()

    rerender(
      <CollectionQueryInput
        activeQuery="status = ready"
        defaultQuery=""
        schema={schema}
        error={rejectedError}
        onApply={() => undefined}
      />,
    )
    expect(input).toHaveValue("status = ready")
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("validates UTF-8 error positions and highlights the affected scalar on focus", async () => {
    const user = userEvent.setup()
    const { rerender } = render(
      <CollectionQueryInput
        activeQuery="name = a💡b"
        defaultQuery=""
        schema={schema}
        error={{ message: "Unexpected value", position: 9 }}
        onApply={() => undefined}
      />,
    )
    const input = queryInput()
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Character 9: Unexpected value",
    )
    await user.click(input)
    expect(input.selectionStart).toBe(8)
    expect(input.selectionEnd).toBe(10)
    expect(input.value.slice(input.selectionStart!, input.selectionEnd!)).toBe(
      "💡",
    )

    rerender(
      <CollectionQueryInput
        activeQuery="name = a💡b"
        defaultQuery=""
        schema={schema}
        error={{ message: "Bad position", position: 9999 }}
        onApply={() => undefined}
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent("Bad position")
    expect(screen.getByRole("alert")).not.toHaveTextContent("Character")
  })

  it("resets to external canonical queries without reopening suggestions", async () => {
    const user = userEvent.setup()
    const { rerender } = renderQueryInput({ activeQuery: "status = ready" })
    const input = queryInput()
    await user.click(input)
    await user.clear(input)
    await user.type(input, "temporary")

    rerender(
      <CollectionQueryInput
        activeQuery="status = blocked"
        defaultQuery=""
        schema={schema}
        onApply={() => undefined}
      />,
    )
    await waitFor(() => expect(input).toHaveValue("status = blocked"))
    expect(input.selectionStart).toBe("status = blocked".length)
    expect(input).toHaveFocus()
    expect(input).toHaveAttribute("aria-expanded", "false")
  })

  it("does not open or apply while disabled", async () => {
    const user = userEvent.setup()
    const onApply = vi.fn()
    renderQueryInput({ activeQuery: "", disabled: true, onApply })
    const input = queryInput()
    expect(input).toBeDisabled()
    expect(screen.getByRole("button", { name: "Clear query" })).toBeDisabled()
    expect(input).toHaveAttribute("aria-expanded", "false")
    await user.keyboard("{Enter}")
    expect(onApply).not.toHaveBeenCalled()
  })
})

function renderQueryInput({
  activeQuery = "status = ready",
  defaultQuery = "",
  disabled = false,
  onApply = vi.fn(),
}: {
  activeQuery?: string
  defaultQuery?: string
  disabled?: boolean
  onApply?: (query: string) => void
} = {}) {
  return render(
    <CollectionQueryInput
      activeQuery={activeQuery}
      defaultQuery={defaultQuery}
      schema={schema}
      disabled={disabled}
      onApply={onApply}
    />,
  )
}

function queryInput(): HTMLInputElement {
  return screen.getByRole("combobox", {
    name: "Collection query",
  }) as HTMLInputElement
}
