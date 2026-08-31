import { describe, expect, it } from "vitest"

import type { CollectionQuerySchema } from "@/api/collection"
import {
  type CollectionQuerySuggestion,
  applyCollectionQuerySuggestion,
  applyCollectionQuerySuggestionForSelection,
  getCollectionQuerySuggestions,
  getCollectionQuerySuggestionsForSelection,
} from "@/components/collection/collection-query-editor"

const schema: CollectionQuerySchema = {
  fields: [
    {
      name: "status",
      type: "enum",
      operators: ["=", "!=", "IN", "NOT IN"],
      sortable: true,
      suggested_values: ["ready", "needs review", "comma,value", 'quote"value'],
    },
    {
      name: "name",
      type: "string",
      operators: ["=", "!=", "~", "!~", "IN", "NOT IN"],
      sortable: true,
      suggested_values: ["sample value", 'quoted"value', "and order by"],
    },
    {
      name: "enabled",
      type: "boolean",
      operators: ["=", "!=", "IN", "NOT IN"],
      sortable: false,
    },
    {
      name: "score",
      type: "number",
      operators: ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"],
      sortable: true,
      suggested_values: [
        "1.5",
        "NaN",
        "0x10",
        "0x1p2",
        "1.",
        "0x1.p2",
        "1_000",
        "0x_1_2.3_4p+1_2",
      ],
    },
    {
      name: "updated",
      type: "timestamp",
      operators: ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"],
      sortable: true,
      suggested_values: [
        "now",
        "-24h",
        "2026-01-02",
        "2026-01-02T03:04:05.123Z",
        "2026-01-02T03:04:05,123Z",
        "0001-01-01",
        "0001-01-01T00:00:00Z",
        "0001-01-01T01:00:00+01:00",
        "0001-01-01T00:00:00.000000001Z",
        "0001-01-01T00:00:00.0000000001Z",
        "0000-12-31T23:00:00-01:00",
        "2026-02-30",
        "-999999999999999999999w",
        "invalid",
      ],
    },
  ],
}

describe("collection query completion engine", () => {
  it("returns no suggestions without a usable schema", () => {
    expect(getCollectionQuerySuggestions("", 0)).toEqual([])
    expect(getCollectionQuerySuggestions("", 0, { fields: [] })).toEqual([])
  })

  it("suggests root fields and grammar in a case-insensitive way", () => {
    expect(labels("", 0)).toEqual([
      "status",
      "name",
      "enabled",
      "score",
      "updated",
      "ALL",
      "NOT",
      "(",
      "ORDER BY",
    ])
    expect(labels("StA", 3)).toEqual(["status"])
    expect(labels("al", 2)).toEqual(["ALL"])
    expect(labels("ord", 3)).toEqual(["ORDER BY"])
  })

  it.each([
    ["status ", ["=", "!=", "IN", "NOT IN"]],
    ["name ", ["=", "!=", "~", "!~", "IN", "NOT IN"]],
    ["enabled ", ["=", "!=", "IN", "NOT IN"]],
    ["score ", ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]],
    ["updated ", ["=", "!=", ">", ">=", "<", "<=", "IN", "NOT IN"]],
  ])("uses only schema operators after %s", (query, expected) => {
    expect(labels(query, query.length)).toEqual(expected)
  })

  it("completes partial single- and multi-word operators", () => {
    expect(labels("status N", 8)).toEqual(["NOT IN"])
    expect(labels("status NOT ", 11)).toEqual(["IN"])
    expect(labels("status NOT IN ", 14)).toEqual(["("])
    expect(labels("status IN ", 10)).toEqual(["("])
  })

  it("offers typed, safely rendered values and valid timestamps", () => {
    expect(labels("status = ", 9)).toEqual([
      "ready",
      '"needs review"',
      '"comma,value"',
      '"quote\\"value"',
    ])
    expect(labels("name = ", 7)).toEqual([
      '"sample value"',
      '"quoted\\"value"',
      '"and order by"',
    ])
    expect(labels("enabled = ", 10)).toEqual(["true", "false"])
    expect(labels("score = ", 8)).toEqual([
      "1.5",
      "0x1p2",
      "1.",
      "0x1.p2",
      "1_000",
      "0x_1_2.3_4p+1_2",
    ])
    expect(labels("updated = ", 10)).toEqual([
      "-24h",
      "2026-01-02",
      "2026-01-02T03:04:05.123Z",
      '"2026-01-02T03:04:05,123Z"',
      "0001-01-01T00:00:00.000000001Z",
    ])
    expect(labels("updated = ", 10)).not.toContain("now")

    const fallbackSchema: CollectionQuerySchema = {
      fields: [{ ...schema.fields[4]!, suggested_values: undefined }],
    }
    expect(
      getCollectionQuerySuggestions(
        "updated = ",
        "updated = ".length,
        fallbackSchema,
      ).map((value) => value.label),
    ).toEqual(["-1h", "-24h", "-7d", "-30d"])
  })

  it("does not classify keywords inside quoted and escaped values", () => {
    expect(labels('name = "and order by" ', 22)).toEqual([
      "AND",
      "OR",
      "ORDER BY",
    ])
    const escaped = 'name = "escaped \\" AND ORDER BY" '
    expect(labels(escaped, escaped.length)).toEqual(["AND", "OR", "ORDER BY"])
  })

  it("completes ALL, NOT, and grouping only in valid expression states", () => {
    expect(labels("ALL ", 4)).toEqual(["ORDER BY"])
    expect(labels("ALL AND ", 8)).toEqual([])
    expect(labels("NOT ", 4)).not.toContain("ALL")
    expect(labels("NOT ", 4)).not.toContain("ORDER BY")
    expect(labels("(status = ready ", 16)).toEqual(["AND", "OR", ")"])
    expect(labels("(status = ready) ", 17)).toEqual(["AND", "OR", "ORDER BY"])
    expect(labels("NOT (", 5)).toContain("status")
  })

  it("honors nesting and predicate grammar limits", () => {
    const maximumDepth = "(".repeat(16)
    expect(labels(maximumDepth, maximumDepth.length)).toContain("status")
    expect(labels(maximumDepth, maximumDepth.length)).not.toContain("NOT")
    expect(labels(maximumDepth, maximumDepth.length)).not.toContain("(")

    const maximumPredicates = Array.from(
      { length: 50 },
      () => "status = ready",
    ).join(" AND ")
    expect(
      labels(`${maximumPredicates} `, maximumPredicates.length + 1),
    ).toEqual(["ORDER BY"])
    expect(
      labels(`${maximumPredicates} AND `, maximumPredicates.length + 5),
    ).toEqual([])
  })

  it("completes initial and subsequent IN values, commas, and closing syntax", () => {
    expect(labels("status IN (", 11)).toEqual([
      "ready",
      '"needs review"',
      '"comma,value"',
      '"quote\\"value"',
    ])
    expect(labels("status IN (ready ", 17)).toEqual([",", ")"])
    expect(labels("status IN (ready, ", 18)).toEqual([
      "ready",
      '"needs review"',
      '"comma,value"',
      '"quote\\"value"',
    ])
    expect(labels("status IN (ready) ", 18)).toEqual(["AND", "OR", "ORDER BY"])
    expect(labels("status IN (ready , blocked)", 17)).toEqual([","])

    const oneHundred = Array.from({ length: 100 }, () => "ready").join(", ")
    const atLimit = `status IN (${oneHundred} `
    expect(labels(atLimit, atLimit.length)).toEqual([")"])
  })

  it("tracks every ORDER BY state, uniqueness, sortability, and the limit", () => {
    expect(labels("ORDER B", 7)).toEqual(["BY"])
    expect(labels("ORDER BY ", 9)).toEqual([
      "status",
      "name",
      "score",
      "updated",
    ])
    expect(labels("ORDER BY status ", 16)).toEqual(["ASC", "DESC"])
    expect(labels("ORDER BY status DESC ", 21)).toEqual([","])
    expect(labels("ORDER BY status DESC, ", 22)).toEqual([
      "name",
      "score",
      "updated",
    ])
    expect(labels("ORDER BY status ASC, status ", 28)).toEqual([])
    expect(
      labels(
        "ORDER BY status ASC, name DESC, updated ASC ",
        "ORDER BY status ASC, name DESC, updated ASC ".length,
      ),
    ).toEqual([])
  })

  it("considers sort fields on both sides of a mid-query caret", () => {
    const value = "ORDER BY status ASC, name DESC"
    const suggestions = getCollectionQuerySuggestions(value, 12, schema)
    expect(suggestions.map((candidate) => candidate.label)).toContain("status")
    expect(suggestions.map((candidate) => candidate.label)).not.toContain(
      "name",
    )
  })

  it("treats non-collapsed token and segment selections as replacements", () => {
    expect(
      getCollectionQuerySuggestionsForSelection(
        "status = ready",
        { start: 0, end: 6 },
        schema,
      ).map((candidate) => candidate.label),
    ).toContain("name")
    expect(
      getCollectionQuerySuggestionsForSelection(
        "status = ready",
        { start: 7, end: 8 },
        schema,
      ).map((candidate) => candidate.label),
    ).toEqual(["=", "!=", "IN", "NOT IN"])
    expect(
      getCollectionQuerySuggestionsForSelection(
        "status = ready",
        { start: 9, end: 14 },
        schema,
      ).map((candidate) => candidate.label),
    ).toContain('"needs review"')

    const predicate = "status = ready AND name = ignored"
    expect(
      getCollectionQuerySuggestionsForSelection(
        predicate,
        { start: 19, end: predicate.length },
        schema,
      ).map((candidate) => candidate.label),
    ).toContain("name")

    const sort = "ORDER BY status ASC, name DESC"
    expect(
      getCollectionQuerySuggestionsForSelection(
        sort,
        { start: 21, end: sort.length },
        schema,
      ).map((candidate) => candidate.label),
    ).toContain("name")
  })

  it("filters all bounded values before applying the visible result limit", () => {
    const values = Array.from({ length: 30 }, (_, index) => `value-${index}`)
    values[29] = "target-value"
    const largeSchema: CollectionQuerySchema = {
      fields: [
        {
          name: "status",
          type: "enum",
          operators: ["="],
          sortable: true,
          suggested_values: values,
        },
      ],
    }
    expect(
      getCollectionQuerySuggestions(
        "status = target",
        "status = target".length,
        largeSchema,
      ).map((candidate) => candidate.label),
    ).toEqual(["target-value"])
  })
})

describe("collection query suggestion insertion", () => {
  it("preserves the collapsed-caret helper API and suffix text", () => {
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

  it("uses a non-collapsed selection as the atomic replacement range", () => {
    const value = "status = ready AND na = ignored"
    const suggestion = getCollectionQuerySuggestionsForSelection(
      value,
      { start: 19, end: 21 },
      schema,
    ).find((candidate) => candidate.label === "name")
    expect(suggestion).toBeDefined()
    expect(
      applyCollectionQuerySuggestionForSelection(value, suggestion!, {
        start: 19,
        end: 21,
      }),
    ).toEqual({
      value: "status = ready AND name = ignored",
      selectionStart: 24,
      selectionEnd: 24,
      applied: true,
    })
  })

  it("replaces a complete quoted token and escapes raw values once", () => {
    const value = 'name = "sam" AND status = ready'
    const suggestion = getCollectionQuerySuggestions(value, 12, schema).find(
      (candidate) => candidate.label === '"sample value"',
    )
    expect(suggestion).toMatchObject({ replaceStart: 7, replaceEnd: 12 })
    expect(applyCollectionQuerySuggestion(value, suggestion!)).toEqual({
      value: 'name = "sample value" AND status = ready',
      caret: 22,
    })
  })

  it("keeps punctuation and treats unchanged suggestions as applied", () => {
    const inValue = "status IN (ready, blocked)"
    const replacement = getCollectionQuerySuggestions(
      inValue,
      "status IN (read".length,
      schema,
    ).find((candidate) => candidate.label === "ready")
    expect(applyCollectionQuerySuggestion(inValue, replacement!)).toEqual({
      value: inValue,
      caret: "status IN (ready".length,
    })

    const unchanged = "status = ready "
    const unchangedSuggestion = getCollectionQuerySuggestions(
      unchanged,
      "status = ready".length,
      schema,
    ).find((candidate) => candidate.label === "ready")
    expect(
      applyCollectionQuerySuggestionForSelection(
        unchanged,
        unchangedSuggestion!,
        { start: 14, end: 14 },
      ),
    ).toEqual({
      value: unchanged,
      selectionStart: 15,
      selectionEnd: 15,
      applied: true,
    })
  })

  it("consumes existing keyword and punctuation suffixes without duplication", () => {
    const order = "ORDER BY status ASC"
    const orderSuggestion = getCollectionQuerySuggestions(
      order,
      "ORDER".length,
      schema,
    ).find((candidate) => candidate.label === "ORDER BY")
    expect(applyCollectionQuerySuggestion(order, orderSuggestion!)).toEqual({
      value: order,
      caret: "ORDER BY ".length,
    })

    const notIn = "status NOT IN (ready)"
    const notInSuggestion = getCollectionQuerySuggestions(
      notIn,
      "status NOT".length,
      schema,
    ).find((candidate) => candidate.label === "NOT IN")
    expect(applyCollectionQuerySuggestion(notIn, notInSuggestion!)).toEqual({
      value: notIn,
      caret: "status NOT IN (".length,
    })

    const closing = "status IN (ready )"
    const closeSuggestion = getCollectionQuerySuggestions(
      closing,
      closing.indexOf(")"),
      schema,
    ).find((candidate) => candidate.label === ")")
    expect(applyCollectionQuerySuggestion(closing, closeSuggestion!)).toEqual({
      value: closing,
      caret: closing.length,
    })
  })

  it("converts a selected single-value IN operator to a scalar operator", () => {
    const value = "status IN (ready) AND name = ignored"
    const scalar = getCollectionQuerySuggestionsForSelection(
      value,
      { start: 7, end: 9 },
      schema,
    ).find((candidate) => candidate.label === "=")
    expect(scalar).toBeDefined()
    expect(
      applyCollectionQuerySuggestionForSelection(value, scalar!, {
        start: 7,
        end: 9,
      }),
    ).toMatchObject({
      value: "status = ready AND name = ignored",
      applied: true,
    })

    const multiple = "status IN (ready, blocked)"
    expect(
      getCollectionQuerySuggestionsForSelection(
        multiple,
        { start: 7, end: 9 },
        schema,
      ).map((candidate) => candidate.label),
    ).toEqual(["IN", "NOT IN"])

    const notIn = "status NOT IN (ready)"
    const collapsedScalar = getCollectionQuerySuggestionsForSelection(
      notIn,
      { start: 7, end: 10 },
      schema,
    ).find((candidate) => candidate.label === "=")
    expect(
      applyCollectionQuerySuggestion(notIn, collapsedScalar!).value.trim(),
    ).toBe("status = ready")
  })

  it("keeps suggested logical keywords separated before a group close", () => {
    const value = "(status = ready )"
    const caret = value.indexOf(")")
    const logical = getCollectionQuerySuggestions(value, caret, schema).find(
      (candidate) => candidate.label === "AND",
    )
    const withLogical = applyCollectionQuerySuggestion(value, logical!)
    expect(withLogical.value).toBe("(status = ready AND )")

    const field = getCollectionQuerySuggestions(
      withLogical.value,
      withLogical.caret,
      schema,
    ).find((candidate) => candidate.label === "status")
    expect(
      applyCollectionQuerySuggestion(withLogical.value, field!).value,
    ).toBe("(status = ready AND status)")
  })

  it("advances completion after an accepted token before punctuation", () => {
    const value = "status IN (ready, blocked)"
    const ready = getCollectionQuerySuggestions(
      value,
      value.indexOf(",") - 1,
      schema,
    ).find((candidate) => candidate.label === "ready")
    const applied = applyCollectionQuerySuggestion(value, ready!)
    expect(applied.value).toBe(value)
    expect(
      getCollectionQuerySuggestions(applied.value, applied.caret, schema).map(
        (candidate) => candidate.label,
      ),
    ).toEqual([","])
  })

  it("normalizes replacement offsets away from Unicode surrogate interiors", () => {
    const value = "a💡b"
    const suggestion: CollectionQuerySuggestion = {
      id: "unicode",
      label: "x",
      detail: "test",
      kind: "value",
      insertText: "x",
      replaceStart: 2,
      replaceEnd: 2,
    }
    expect(
      applyCollectionQuerySuggestionForSelection(value, suggestion, {
        start: 2,
        end: 2,
      }),
    ).toEqual({
      value: "ax💡b",
      selectionStart: 2,
      selectionEnd: 2,
      applied: true,
    })
  })

  it("rejects an over-limit completion atomically and allows a fitting replacement", () => {
    const full = "x".repeat(4095)
    const append: CollectionQuerySuggestion = {
      id: "append",
      label: "yz",
      detail: "test",
      kind: "value",
      insertText: "yz",
      replaceStart: full.length,
      replaceEnd: full.length,
    }
    expect(
      applyCollectionQuerySuggestionForSelection(full, append, {
        start: full.length,
        end: full.length,
      }),
    ).toEqual({
      value: full,
      selectionStart: full.length,
      selectionEnd: full.length,
      applied: false,
    })

    const replace: CollectionQuerySuggestion = {
      ...append,
      replaceStart: full.length - 2,
      replaceEnd: full.length,
    }
    expect(
      applyCollectionQuerySuggestionForSelection(full, replace, {
        start: full.length - 2,
        end: full.length,
      }),
    ).toMatchObject({
      value: `${"x".repeat(4093)}yz`,
      applied: true,
    })
  })
})

function labels(value: string, caret: number): string[] {
  return getCollectionQuerySuggestions(value, caret, schema).map(
    (suggestion) => suggestion.label,
  )
}
