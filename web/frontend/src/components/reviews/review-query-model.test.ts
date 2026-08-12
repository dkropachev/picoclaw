import { describe, expect, it } from "vitest"

import type { ReviewWorkItem } from "./review-portfolio-model"
import {
  applyReviewQuerySuggestion,
  filterReviewWorkItems,
  getReviewQuerySuggestions,
  matchesReviewQuery,
  parseReviewQuery,
} from "./review-query-model"

function workItem(
  overrides: Partial<ReviewWorkItem> &
    Pick<ReviewWorkItem, "key" | "pullNumber" | "title">,
): ReviewWorkItem {
  return {
    repository: "Acme/Widgets",
    pullURL: `https://example.test/pulls/${overrides.pullNumber}`,
    roles: ["review"],
    status: "pending",
    needsAction: true,
    updatedAt: "2026-08-10T12:30:00Z",
    reviewCases: [],
    developmentCases: [],
    authors: ["Alice Smith"],
    reviewers: ["Bob Reviewer"],
    ...overrides,
  }
}

const reviewItem = workItem({
  key: "acme/widgets#42",
  pullNumber: 42,
  title: "Fix login redirect",
})

const developmentItem = workItem({
  key: "acme/api-pr-107",
  repository: "Acme/API",
  pullNumber: 107,
  title: "Add audit events",
  roles: ["develop"],
  status: "closed",
  needsAction: false,
  updatedAt: "2026-07-02T09:15:00Z",
  authors: ["Carol Coder"],
  reviewers: ["Dana Reviewer"],
})

const items = [reviewItem, developmentItem]

describe("review query parser", () => {
  it("parses case-insensitive fields, operators, quoted values, and AND", () => {
    const parsed = parseReviewQuery(
      'STATUS = PENDING and role != develop AND author ~ "Alice Smith"',
    )

    expect(parsed.valid).toBe(true)
    expect(parsed.errors).toEqual([])
    expect(parsed.clauses).toMatchObject([
      { kind: "field", field: "status", operator: "=", value: "PENDING" },
      { kind: "field", field: "role", operator: "!=", value: "develop" },
      {
        kind: "field",
        field: "author",
        operator: "~",
        value: "Alice Smith",
      },
    ])
  })

  it("treats bare phrases as text and does not split quoted AND values", () => {
    expect(parseReviewQuery("login redirect").clauses).toMatchObject([
      { kind: "text", value: "login redirect" },
    ])
    expect(parseReviewQuery('text ~ "Rock AND roll"').clauses).toMatchObject([
      { kind: "field", field: "text", operator: "~", value: "Rock AND roll" },
    ])
  })

  it("returns located errors for invalid and incomplete clauses", () => {
    expect(parseReviewQuery("repository = acme").errors[0]).toMatchObject({
      code: "unknown_field",
      start: 0,
      end: 10,
    })
    expect(parseReviewQuery("status !=").errors[0]?.code).toBe("missing_value")
    expect(parseReviewQuery("status > pending").errors[0]?.code).toBe(
      "unexpected_token",
    )
    expect(parseReviewQuery("status == pending").errors[0]?.code).toBe(
      "unexpected_token",
    )
    expect(parseReviewQuery('author = "Alice').errors[0]?.code).toBe(
      "unclosed_quote",
    )
    expect(parseReviewQuery("status = pending AND ").errors[0]?.code).toBe(
      "empty_clause",
    )
  })
})

describe("review query evaluator", () => {
  it("combines clauses and compares values without case sensitivity", () => {
    expect(
      filterReviewWorkItems(
        items,
        "STATUS = Pending AND role = REVIEW AND attention = TRUE",
      ),
    ).toEqual([reviewItem])
    expect(
      matchesReviewQuery(
        reviewItem,
        "author ~ ALICE AND reviewer != dana AND number = #42 AND updated = 2026-08-10",
      ),
    ).toBe(true)
  })

  it("supports all operators, role aliases, and attended aliases", () => {
    expect(filterReviewWorkItems(items, "status != pending")).toEqual([
      developmentItem,
    ])
    expect(
      filterReviewWorkItems(
        [{ ...developmentItem, status: "complete" }],
        "status = finished",
      ),
    ).toHaveLength(1)
    expect(filterReviewWorkItems(items, "role = coding")).toEqual([
      developmentItem,
    ])
    expect(filterReviewWorkItems(items, "attention = clear")).toEqual([
      developmentItem,
    ])
    expect(filterReviewWorkItems(items, "updated ~ 2026-07")).toEqual([
      developmentItem,
    ])
  })

  it("searches title, repository, number, authors, and reviewers as free text", () => {
    expect(filterReviewWorkItems(items, "login redirect")).toEqual([reviewItem])
    expect(filterReviewWorkItems(items, "acme/api")).toEqual([developmentItem])
    expect(filterReviewWorkItems(items, `#${107}`)).toEqual([developmentItem])
    expect(filterReviewWorkItems(items, "carol coder")).toEqual([
      developmentItem,
    ])
    expect(filterReviewWorkItems(items, "bob reviewer")).toEqual([reviewItem])
  })

  it("matches every item for an empty query and none for invalid syntax", () => {
    expect(filterReviewWorkItems(items, "  ")).toEqual(items)
    expect(filterReviewWorkItems(items, "status = ")).toEqual([])
  })
})

describe("review query autocomplete", () => {
  it("suggests fields for an empty or partial clause", () => {
    const emptyLabels = getReviewQuerySuggestions("", items).map(
      (suggestion) => suggestion.label,
    )
    expect(emptyLabels).toEqual(
      expect.arrayContaining([
        "status",
        "role",
        "attention",
        "author",
        "reviewer",
        "number",
        "updated",
        "text",
      ]),
    )

    const [status] = getReviewQuerySuggestions("sta", items)
    expect(status).toMatchObject({ kind: "field", label: "status" })
    expect(applyReviewQuerySuggestion("sta", status)).toBe("status")

    const [role] = getReviewQuerySuggestions("status = pending AND ro", items)
    expect(role).toMatchObject({ kind: "field", label: "role" })
    expect(applyReviewQuerySuggestion("status = pending AND ro", role)).toBe(
      "status = pending AND role",
    )
  })

  it("suggests all operators after a complete field", () => {
    const suggestions = getReviewQuerySuggestions("status", items)
    expect(suggestions.map((suggestion) => suggestion.label)).toEqual([
      "=",
      "!=",
      "~",
    ])
    expect(applyReviewQuerySuggestion("status", suggestions[0])).toBe(
      "status = ",
    )

    const [notEqual] = getReviewQuerySuggestions("status !", items)
    expect(notEqual.label).toBe("!=")
    expect(applyReviewQuerySuggestion("status !", notEqual)).toBe("status != ")
  })

  it("suggests fixed and data-derived values and quotes names with spaces", () => {
    const [pending] = getReviewQuerySuggestions("status = p", items)
    expect(pending).toMatchObject({ kind: "value", label: "pending" })
    expect(applyReviewQuerySuggestion("status = p", pending)).toBe(
      "status = pending",
    )

    const [alice] = getReviewQuerySuggestions("author = ali", items)
    expect(alice).toMatchObject({ kind: "value", label: "Alice Smith" })
    expect(applyReviewQuerySuggestion("author = ali", alice)).toBe(
      'author = "Alice Smith"',
    )

    expect(
      getReviewQuerySuggestions("attention = ", items).map(
        (suggestion) => suggestion.label,
      ),
    ).toEqual(["needed", "clear"])
    expect(
      getReviewQuerySuggestions("updated = 2026-08", items).map(
        (suggestion) => suggestion.label,
      ),
    ).toEqual(["2026-08-10", "2026-08-10T12:30:00Z"])
  })

  it("offers AND after a complete known value", () => {
    const source = "status = pending "
    const suggestion = getReviewQuerySuggestions(source, items).at(-1)
    expect(suggestion).toMatchObject({
      kind: "keyword",
      label: "AND",
      insertText: " AND ",
    })
    expect(applyReviewQuerySuggestion(source, suggestion!)).toBe(
      "status = pending AND ",
    )
  })
})
