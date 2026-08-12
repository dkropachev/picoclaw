import type { ReviewWorkItem } from "./review-portfolio-model"

export const REVIEW_QUERY_FIELDS = [
  "status",
  "role",
  "attention",
  "author",
  "reviewer",
  "number",
  "updated",
  "text",
] as const

export const REVIEW_QUERY_OPERATORS = ["=", "!=", "~"] as const

export type ReviewQueryField = (typeof REVIEW_QUERY_FIELDS)[number]
export type ReviewQueryOperator = (typeof REVIEW_QUERY_OPERATORS)[number]

export interface ReviewQueryFieldClause {
  kind: "field"
  field: ReviewQueryField
  operator: ReviewQueryOperator
  value: string
  start: number
  end: number
}

export interface ReviewQueryTextClause {
  kind: "text"
  value: string
  start: number
  end: number
}

export type ReviewQueryClause = ReviewQueryFieldClause | ReviewQueryTextClause

export type ReviewQueryErrorCode =
  | "empty_clause"
  | "missing_value"
  | "unclosed_quote"
  | "unexpected_token"
  | "unknown_field"

export interface ReviewQueryError {
  code: ReviewQueryErrorCode
  message: string
  start: number
  end: number
}

export interface ParsedReviewQuery {
  source: string
  clauses: ReviewQueryClause[]
  errors: ReviewQueryError[]
  valid: boolean
}

export type ReviewQuerySuggestionKind =
  | "field"
  | "operator"
  | "value"
  | "keyword"

export interface ReviewQuerySuggestion {
  kind: ReviewQuerySuggestionKind
  label: string
  insertText: string
  replaceStart: number
  replaceEnd: number
  detail?: string
}

interface ClauseRange {
  start: number
  end: number
}

const fieldSet = new Set<string>(REVIEW_QUERY_FIELDS)
const operatorSet = new Set<string>(REVIEW_QUERY_OPERATORS)

const fieldDetails: Record<ReviewQueryField, string> = {
  status: "Pending, complete, or captured closed",
  role: "Review or develop",
  attention: "Needs action or clear",
  author: "Pull request author",
  reviewer: "Review author",
  number: "Pull request number",
  updated: "Last update date or timestamp",
  text: "Title, repository, number, author, or reviewer",
}

const staticValues: Partial<Record<ReviewQueryField, readonly string[]>> = {
  status: ["pending", "complete", "closed"],
  role: ["review", "develop"],
  attention: ["needed", "clear"],
}

/** Parse a small, Jira-like query without throwing on incomplete user input. */
export function parseReviewQuery(source: string): ParsedReviewQuery {
  const clauses: ReviewQueryClause[] = []
  const errors: ReviewQueryError[] = []

  if (!source.trim()) {
    return { source, clauses, errors, valid: true }
  }

  for (const range of clauseRanges(source)) {
    const raw = source.slice(range.start, range.end)
    const leadingWhitespace = raw.length - raw.trimStart().length
    const trailingWhitespace = raw.length - raw.trimEnd().length
    const start = range.start + leadingWhitespace
    const end = range.end - trailingWhitespace
    const clauseSource = source.slice(start, end)

    if (!clauseSource) {
      errors.push({
        code: "empty_clause",
        message: "Expected a clause before or after AND.",
        start: range.start,
        end: range.end,
      })
      continue
    }

    const structured = /^([A-Za-z][A-Za-z0-9_-]*)\s*(!=|=|~)([\s\S]*)$/.exec(
      clauseSource,
    )
    if (!structured) {
      const operatorLike = /^([A-Za-z][A-Za-z0-9_-]*)\s*([!<>^:]+.*)$/.exec(
        clauseSource,
      )
      if (operatorLike && fieldSet.has(operatorLike[1].toLowerCase())) {
        errors.push({
          code: "unexpected_token",
          message: `Expected one of ${REVIEW_QUERY_OPERATORS.join(", ")}.`,
          start: start + operatorLike[1].length,
          end,
        })
        continue
      }

      const decoded = decodeQueryValue(clauseSource, start)
      if (decoded.error) {
        errors.push(decoded.error)
      } else {
        clauses.push({ kind: "text", value: decoded.value, start, end })
      }
      continue
    }

    const rawField = structured[1]
    const field = rawField.toLowerCase()
    if (!fieldSet.has(field)) {
      errors.push({
        code: "unknown_field",
        message: `Unknown field “${rawField}”.`,
        start,
        end: start + rawField.length,
      })
      continue
    }

    const operator = structured[2]
    if (!operatorSet.has(operator)) continue

    const rawValue = structured[3]
    const valueLeadingWhitespace = rawValue.length - rawValue.trimStart().length
    const valueStart =
      start + structured[0].length - rawValue.length + valueLeadingWhitespace
    const valueSource = rawValue.trim()
    if (!valueSource) {
      errors.push({
        code: "missing_value",
        message: `Expected a value after ${operator}.`,
        start: valueStart,
        end,
      })
      continue
    }
    if (/^[=!~<>^:]/.test(valueSource)) {
      errors.push({
        code: "unexpected_token",
        message: `Expected a value after ${operator}.`,
        start: valueStart,
        end,
      })
      continue
    }

    const decoded = decodeQueryValue(valueSource, valueStart)
    if (decoded.error) {
      errors.push(decoded.error)
      continue
    }
    clauses.push({
      kind: "field",
      field: field as ReviewQueryField,
      operator: operator as ReviewQueryOperator,
      value: decoded.value,
      start,
      end,
    })
  }

  return { source, clauses, errors, valid: errors.length === 0 }
}

/** Evaluate an already parsed query. Invalid queries deliberately match nothing. */
export function evaluateReviewQuery(
  item: ReviewWorkItem,
  query: ParsedReviewQuery,
): boolean {
  if (!query.valid) return false
  return query.clauses.every((clause) => matchesClause(item, clause))
}

/** Parse and evaluate a query against one work item. */
export function matchesReviewQuery(
  item: ReviewWorkItem,
  source: string,
): boolean {
  return evaluateReviewQuery(item, parseReviewQuery(source))
}

/** Filter work items while parsing the query only once. */
export function filterReviewWorkItems(
  items: readonly ReviewWorkItem[],
  source: string,
): ReviewWorkItem[] {
  const parsed = parseReviewQuery(source)
  if (!parsed.valid) return []
  return items.filter((item) => evaluateReviewQuery(item, parsed))
}

/**
 * Return context-aware completions at the cursor. Each completion includes the
 * exact source range it replaces, which keeps applying suggestions UI-agnostic.
 */
export function getReviewQuerySuggestions(
  source: string,
  items: readonly ReviewWorkItem[] = [],
  cursor = source.length,
): ReviewQuerySuggestion[] {
  const safeCursor = Math.max(0, Math.min(cursor, source.length))
  const prefix = source.slice(0, safeCursor)
  const ranges = clauseRanges(prefix)
  const current = ranges.at(-1) ?? { start: 0, end: safeCursor }
  const segment = prefix.slice(current.start)
  const leadingWhitespace = segment.length - segment.trimStart().length
  const contentStart = current.start + leadingWhitespace
  const content = prefix.slice(contentStart)

  if (!content) return fieldSuggestions("", contentStart, safeCursor)

  const fieldMatch = /^([A-Za-z][A-Za-z0-9_-]*)/.exec(content)
  if (!fieldMatch) return []

  const fieldFragment = fieldMatch[1]
  const fieldName = fieldFragment.toLowerCase()
  const afterField = content.slice(fieldFragment.length)
  if (!fieldSet.has(fieldName)) {
    if (afterField.trim()) return []
    return fieldSuggestions(
      fieldFragment,
      contentStart,
      tokenEnd(source, safeCursor),
    )
  }

  const field = fieldName as ReviewQueryField
  if (!afterField.trim()) {
    if (fieldFragment.length < fieldName.length) {
      return fieldSuggestions(
        fieldFragment,
        contentStart,
        tokenEnd(source, safeCursor),
      )
    }
    return operatorSuggestions(
      "",
      safeCursor,
      safeCursor,
      afterField.length === 0,
    )
  }

  const operatorLeadingWhitespace =
    afterField.length - afterField.trimStart().length
  const operatorStart =
    contentStart + fieldFragment.length + operatorLeadingWhitespace
  const operatorAndValue = afterField.trimStart()
  const operatorMatch = /^(!=|=|~)/.exec(operatorAndValue)
  if (!operatorMatch) {
    const operatorFragment = /^\S*/.exec(operatorAndValue)?.[0] ?? ""
    return operatorSuggestions(
      operatorFragment,
      operatorStart,
      tokenEnd(source, safeCursor),
      false,
    )
  }

  const operator = operatorMatch[1]
  const afterOperator = operatorAndValue.slice(operator.length)
  const valueLeadingWhitespace =
    afterOperator.length - afterOperator.trimStart().length
  const valueStart = operatorStart + operator.length + valueLeadingWhitespace
  const rawValue = afterOperator.trimStart()
  if (!rawValue) {
    return valueSuggestions(field, "", items, safeCursor, safeCursor)
  }

  const valueFragment = partialQueryValue(rawValue)
  const replaceEnd = valueReplacementEnd(source, safeCursor, rawValue)
  const suggestions = valueSuggestions(
    field,
    valueFragment,
    items,
    valueStart,
    replaceEnd,
  )
  const knownValues = availableValues(field, items)
  const exactValue = knownValues.some(
    (value) =>
      canonicalValue(field, value) === canonicalValue(field, valueFragment),
  )
  if (exactValue || /\s$/.test(afterOperator)) {
    suggestions.push(andSuggestion(source, safeCursor))
  }
  return suggestions
}

/** Alias useful to consumers that prefer a verb-led autocomplete API. */
export const suggestReviewQuery = getReviewQuerySuggestions

export function applyReviewQuerySuggestion(
  source: string,
  suggestion: ReviewQuerySuggestion,
): string {
  return (
    source.slice(0, suggestion.replaceStart) +
    suggestion.insertText +
    source.slice(suggestion.replaceEnd)
  )
}

function matchesClause(
  item: ReviewWorkItem,
  clause: ReviewQueryClause,
): boolean {
  if (clause.kind === "text") {
    const needle = normalize(clause.value)
    return fieldValues(item, "text").some((value) =>
      normalize(value).includes(needle),
    )
  }

  const candidates = fieldValues(item, clause.field)
  if (clause.operator === "~") {
    const needle = normalize(clause.value)
    return candidates.some((value) => normalize(value).includes(needle))
  }

  const expected = canonicalValue(clause.field, clause.value)
  const equal = candidates.some(
    (value) => canonicalValue(clause.field, value) === expected,
  )
  return clause.operator === "=" ? equal : !equal
}

function fieldValues(item: ReviewWorkItem, field: ReviewQueryField): string[] {
  switch (field) {
    case "status":
      return [item.status]
    case "role":
      return item.roles
    case "attention":
      return item.needsAction
        ? ["needed", "needs action", "unattended", "true", "yes"]
        : ["clear", "attended", "false", "no"]
    case "author":
      return item.authors
    case "reviewer":
      return item.reviewers
    case "number":
      return [String(item.pullNumber), `#${item.pullNumber}`]
    case "updated": {
      const date = item.updatedAt.split("T", 1)[0]
      return date && date !== item.updatedAt
        ? [item.updatedAt, date]
        : [item.updatedAt]
    }
    case "text":
      return [
        item.title,
        item.repository,
        String(item.pullNumber),
        `#${item.pullNumber}`,
        ...item.authors,
        ...item.reviewers,
      ]
  }
}

function canonicalValue(field: ReviewQueryField, value: string): string {
  const normalized = normalize(value)
  if (field === "number") return normalized.replace(/^#/, "")
  if (field === "status") {
    if (["finished", "review finished"].includes(normalized)) return "complete"
    if (normalized === "captured closed") return "closed"
  }
  if (field === "role") {
    if (["code", "coding", "developer", "developing"].includes(normalized)) {
      return "develop"
    }
    if (normalized === "reviewing") return "review"
  }
  if (field === "attention") {
    if (
      [
        "needed",
        "needs action",
        "unattended",
        "required",
        "true",
        "yes",
      ].includes(normalized)
    ) {
      return "needed"
    }
    if (["clear", "attended", "false", "no"].includes(normalized)) {
      return "clear"
    }
  }
  return normalized
}

function normalize(value: string): string {
  return value.trim().toLocaleLowerCase()
}

function clauseRanges(source: string): ClauseRange[] {
  const ranges: ClauseRange[] = []
  let start = 0
  let quoted = false
  let escaped = false

  for (let index = 0; index < source.length; index += 1) {
    const character = source[index]
    if (quoted) {
      if (escaped) {
        escaped = false
      } else if (character === "\\") {
        escaped = true
      } else if (character === '"') {
        quoted = false
      }
      continue
    }
    if (character === '"') {
      quoted = true
      continue
    }
    if (
      source.slice(index, index + 3).toLowerCase() === "and" &&
      (index === 0 || /\s/.test(source[index - 1])) &&
      (index + 3 === source.length || /\s/.test(source[index + 3]))
    ) {
      ranges.push({ start, end: index })
      index += 2
      start = index + 1
    }
  }
  ranges.push({ start, end: source.length })
  return ranges
}

function decodeQueryValue(
  source: string,
  absoluteStart: number,
):
  | { value: string; error?: never }
  | { value?: never; error: ReviewQueryError } {
  if (!source.startsWith('"')) return { value: source }

  let value = ""
  let escaped = false
  for (let index = 1; index < source.length; index += 1) {
    const character = source[index]
    if (escaped) {
      value += character
      escaped = false
      continue
    }
    if (character === "\\") {
      escaped = true
      continue
    }
    if (character === '"') {
      if (source.slice(index + 1).trim()) {
        return {
          error: {
            code: "unexpected_token",
            message: "Unexpected text after the quoted value.",
            start: absoluteStart + index + 1,
            end: absoluteStart + source.length,
          },
        }
      }
      return { value }
    }
    value += character
  }

  return {
    error: {
      code: "unclosed_quote",
      message: "Close the quoted value with a double quote.",
      start: absoluteStart,
      end: absoluteStart + source.length,
    },
  }
}

function fieldSuggestions(
  fragment: string,
  replaceStart: number,
  replaceEnd: number,
): ReviewQuerySuggestion[] {
  const normalizedFragment = normalize(fragment)
  return REVIEW_QUERY_FIELDS.filter((field) =>
    field.startsWith(normalizedFragment),
  ).map((field) => ({
    kind: "field",
    label: field,
    insertText: field,
    replaceStart,
    replaceEnd,
    detail: fieldDetails[field],
  }))
}

function operatorSuggestions(
  fragment: string,
  replaceStart: number,
  replaceEnd: number,
  needsLeadingSpace: boolean,
): ReviewQuerySuggestion[] {
  return REVIEW_QUERY_OPERATORS.filter((operator) =>
    operator.startsWith(fragment),
  ).map((operator) => ({
    kind: "operator",
    label: operator,
    insertText: `${needsLeadingSpace ? " " : ""}${operator} `,
    replaceStart,
    replaceEnd,
    detail:
      operator === "="
        ? "Equals"
        : operator === "!="
          ? "Does not equal"
          : "Contains",
  }))
}

function valueSuggestions(
  field: ReviewQueryField,
  fragment: string,
  items: readonly ReviewWorkItem[],
  replaceStart: number,
  replaceEnd: number,
): ReviewQuerySuggestion[] {
  const normalizedFragment = normalize(fragment)
  return availableValues(field, items)
    .filter((value) => normalize(value).startsWith(normalizedFragment))
    .slice(0, 50)
    .map((value) => ({
      kind: "value",
      label: value,
      insertText: formatQueryValue(value),
      replaceStart,
      replaceEnd,
      detail: `${field} value`,
    }))
}

function availableValues(
  field: ReviewQueryField,
  items: readonly ReviewWorkItem[],
): string[] {
  const fixed = staticValues[field]
  if (fixed) return [...fixed]

  const values = items.flatMap((item) => {
    if (field === "number") return [String(item.pullNumber)]
    if (field === "text") {
      return [item.title, item.repository, ...item.authors, ...item.reviewers]
    }
    return fieldValues(item, field)
  })
  const seen = new Set<string>()
  return values
    .filter(Boolean)
    .sort((left, right) =>
      left.localeCompare(right, undefined, {
        numeric: true,
        sensitivity: "base",
      }),
    )
    .filter((value) => {
      const identity = normalize(value)
      if (seen.has(identity)) return false
      seen.add(identity)
      return true
    })
}

function formatQueryValue(value: string): string {
  if (!/[\s"\\]/.test(value)) return value
  return `"${value.replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`
}

function partialQueryValue(rawValue: string): string {
  const trimmed = rawValue.trimEnd()
  if (!trimmed.startsWith('"')) return trimmed
  const withoutOpeningQuote = trimmed.slice(1)
  return withoutOpeningQuote.endsWith('"')
    ? withoutOpeningQuote.slice(0, -1)
    : withoutOpeningQuote
}

function valueReplacementEnd(
  source: string,
  cursor: number,
  rawValue: string,
): number {
  if (rawValue.startsWith('"')) {
    let escaped = false
    for (let index = cursor; index < source.length; index += 1) {
      const character = source[index]
      if (escaped) {
        escaped = false
      } else if (character === "\\") {
        escaped = true
      } else if (character === '"') {
        return index + 1
      }
    }
  }
  return tokenEnd(source, cursor)
}

function tokenEnd(source: string, cursor: number): number {
  let end = cursor
  while (end < source.length && !/\s/.test(source[end])) end += 1
  return end
}

function andSuggestion(source: string, cursor: number): ReviewQuerySuggestion {
  let replaceStart = cursor
  while (replaceStart > 0 && /\s/.test(source[replaceStart - 1])) {
    replaceStart -= 1
  }
  return {
    kind: "keyword",
    label: "AND",
    insertText: " AND ",
    replaceStart,
    replaceEnd: cursor,
    detail: "Add another clause",
  }
}
