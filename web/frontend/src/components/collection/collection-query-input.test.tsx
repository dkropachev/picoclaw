import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import type { CollectionQuerySchema } from "@/api/collection"
import {
  CollectionQueryInput,
  applyCollectionQuerySuggestion,
  getCollectionQuerySuggestions,
} from "@/components/collection/collection-query-input"

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

describe("collection query autocomplete", () => {
  it("suggests fields, allowed operators, typed values, and sort clauses", () => {
    expect(labels("sta", 3)).toEqual(["status"])
    expect(labels("status ", 7)).toEqual(["=", "!=", "IN", "NOT IN"])
    expect(labels("status = ", 9)).toEqual(["ready", "blocked"])
    expect(labels("enabled = ", 10)).toEqual(["true", "false"])
    expect(labels("enabled ~ ", 10)).not.toContain("true")
    expect(labels("name = ", 7)).toEqual(['"sample value"'])
    expect(labels("name = sam", 10)).toEqual(['"sample value"'])
    expect(labels("status IN (", 11)).toEqual(["ready", "blocked"])
    expect(labels("status = ready ", 15)).toEqual(["AND", "OR", "ORDER BY"])
    expect(labels("ORDER BY up", 11)).toEqual(["updated"])
    expect(labels("ORDER B", 7)).toEqual(["BY"])
    expect(labels("ORDER BY updated ", 17)).toEqual(["ASC", "DESC"])
    expect(labels("ORDER BY updated DESC ", 22)).toEqual([","])
    expect(labels("ORDER BY status ASC, name DESC, updated ASC ", 45)).toEqual(
      [],
    )
  })

  it("replaces only the token under the caret", () => {
    const value = "status = ready AND na = ignored"
    const suggestion = getCollectionQuerySuggestions(value, 21, schema).find(
      (candidate) => candidate.label === "name",
    )
    expect(suggestion).toBeDefined()
    expect(applyCollectionQuerySuggestion(value, suggestion!)).toEqual({
      value: "status = ready AND name = ignored",
      caret: 24,
    })
  })

  it("supports combobox keyboard insertion, apply, Escape, and Clear", async () => {
    const user = userEvent.setup()
    const onApply = vi.fn()
    render(
      <CollectionQueryInput
        activeQuery="status = ready"
        defaultQuery="ORDER BY updated DESC"
        schema={schema}
        onApply={onApply}
      />,
    )
    const input = screen.getByRole("combobox", { name: "Collection query" })
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

    await user.click(screen.getByRole("button", { name: "Clear query" }))
    expect(input).toHaveValue("ORDER BY updated DESC")
    expect(onApply).toHaveBeenLastCalledWith("ORDER BY updated DESC")
  })

  it("reports server byte positions as DOM character positions", () => {
    render(
      <CollectionQueryInput
        activeQuery="name = naïve💡"
        defaultQuery=""
        schema={schema}
        error={{ message: "Unexpected value", position: 12 }}
        onApply={() => undefined}
      />,
    )
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Character 12: Unexpected value",
    )
    expect(screen.getByRole("combobox")).toHaveAttribute("aria-invalid", "true")
  })
})

function labels(value: string, caret: number): string[] {
  return getCollectionQuerySuggestions(value, caret, schema).map(
    (suggestion) => suggestion.label,
  )
}
