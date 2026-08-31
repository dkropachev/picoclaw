import {
  type CollectionQueryField,
  type CollectionQuerySchema,
  collectionQueryByteLength,
  maximumCollectionQueryBytes,
} from "@/api/collection"

export type CollectionQuerySuggestionKind =
  | "field"
  | "keyword"
  | "operator"
  | "sort"
  | "syntax"
  | "value"

export interface CollectionQuerySuggestion {
  id: string
  label: string
  detail: string
  kind: CollectionQuerySuggestionKind
  insertText: string
  replaceStart: number
  replaceEnd: number
}

export interface CollectionQuerySelection {
  start: number
  end: number
}

export interface CollectionQuerySelectionResult {
  value: string
  selectionStart: number
  selectionEnd: number
  applied: boolean
}

type TokenKind =
  | "comma"
  | "leftParen"
  | "operator"
  | "rightParen"
  | "string"
  | "word"

interface QueryToken {
  kind: TokenKind
  raw: string
  text: string
  start: number
  end: number
  closed?: boolean
}

type EditorState =
  | "afterAll"
  | "afterExpression"
  | "inAfterValue"
  | "inOpen"
  | "inValue"
  | "invalid"
  | "operand"
  | "operator"
  | "operatorNot"
  | "orderByKeyword"
  | "singleValue"
  | "sortAfterDirection"
  | "sortDirection"
  | "sortField"

interface GroupFrame {
  baseDepth: number
}

interface CompletionContext {
  state: EditorState
  field?: CollectionQueryField
  pendingSortField?: string
  predicates: number
  rootStarted: boolean
  pendingNot: number
  baseDepth: number
  groups: GroupFrame[]
  inValueCount: number
  inOpenEnd?: number
  usedSortFields: Set<string>
  sortFields: number
}

interface CompletionAnalysis {
  context: CompletionContext
  range: CollectionQuerySelection
  partial: string
  tokens: QueryToken[]
  hasOtherOrderBy: boolean
  otherSortFields: Set<string>
  otherSortFieldCount: number
  otherInValueCount: number
}

const maximumQueryDepth = 16
const maximumQueryPredicates = 50
const maximumQueryINValues = 100
const maximumQuerySortFields = 3
const relativeTimestampPattern = /^-[1-9][0-9]*(?:m|h|d|w)$/i
const datePattern = /^\d{4}-\d{2}-\d{2}$/
const decimalDigits = String.raw`\d(?:_?\d)*`
const hexadecimalDigits = String.raw`[0-9a-f](?:_?[0-9a-f])*`
const finiteNumberPattern = new RegExp(
  String.raw`^[+-]?(?:(?:${decimalDigits}(?:\.(?:${decimalDigits})?)?)|(?:\.${decimalDigits}))(?:e[+-]?${decimalDigits})?$`,
  "i",
)
const hexadecimalFloatPattern = new RegExp(
  String.raw`^[+-]?0x_?(?:(?:${hexadecimalDigits}(?:\.(?:${hexadecimalDigits})?)?)|(?:\.${hexadecimalDigits}))p[+-]?${decimalDigits}$`,
  "i",
)
const rfc3339TimestampPattern =
  /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:[.,](\d+))?(?:Z|([+-])(\d{2}):(\d{2}))$/
const maximumRelativeTimestampAmounts: Readonly<Record<string, bigint>> = {
  m: 153_722_867n,
  h: 2_562_047n,
  d: 106_751n,
  w: 15_250n,
}

/**
 * Compatibility entry point for callers that only track a collapsed caret.
 */
export function getCollectionQuerySuggestions(
  value: string,
  caretPosition: number,
  schema?: CollectionQuerySchema,
): CollectionQuerySuggestion[] {
  return getCollectionQuerySuggestionsForSelection(
    value,
    { start: caretPosition, end: caretPosition },
    schema,
  )
}

/**
 * Selection-aware completion entry point used by the shared editor.
 */
export function getCollectionQuerySuggestionsForSelection(
  value: string,
  selection: CollectionQuerySelection,
  schema?: CollectionQuerySchema,
): CollectionQuerySuggestion[] {
  if (!schema || schema.fields.length === 0) return []

  const analysis = analyzeCompletion(value, selection, schema)
  const { context, range, partial } = analysis
  let values: CollectionQuerySuggestion[] = []

  switch (context.state) {
    case "operand": {
      if (context.predicates < maximumQueryPredicates) {
        values.push(
          ...schema.fields.map((field) =>
            createSuggestion(
              `field:${field.name}`,
              field.name,
              `${field.type} field`,
              "field",
              appendBoundarySpace(field.name, value, range),
              range,
            ),
          ),
        )
      }
      const atRootStart =
        !context.rootStarted &&
        context.groups.length === 0 &&
        context.pendingNot === 0
      if (atRootStart) {
        values.push(
          createSuggestion(
            "keyword:all",
            "ALL",
            "complete filter",
            "keyword",
            appendBoundarySpace("ALL", value, range),
            range,
          ),
        )
      }
      if (
        context.predicates < maximumQueryPredicates &&
        currentDepth(context) < maximumQueryDepth
      ) {
        values.push(
          createSuggestion(
            "keyword:not",
            "NOT",
            "negate expression",
            "keyword",
            operandKeywordInsertion("NOT", value, range),
            range,
          ),
          createSuggestion(
            "syntax:group-open",
            "(",
            "start group",
            "syntax",
            syntaxInsertion(
              "(",
              value,
              range,
              syntaxReplacementRange(value, range, "("),
            ),
            syntaxReplacementRange(value, range, "("),
          ),
        )
      }
      if (atRootStart && !analysis.hasOtherOrderBy) {
        values.push(orderBySuggestion(value, range))
      }
      break
    }
    case "operator": {
      if (!context.field) break
      const selectedList = selectedINList(value, range)
      values = context.field.operators.flatMap((operator) => {
        if (selectedList && operator !== "IN" && operator !== "NOT IN") {
          if (selectedList.values !== 1 || !selectedList.rawValue) return []
          const replacement = { start: range.start, end: selectedList.end }
          return [
            createSuggestion(
              `operator:${context.field?.name}:${operator}`,
              operator,
              "operator",
              "operator",
              appendBoundarySpace(
                `${operator} ${selectedList.rawValue}`,
                value,
                replacement,
              ),
              replacement,
            ),
          ]
        }
        const replacement = operatorReplacementRange(operator, value, range)
        return [
          createSuggestion(
            `operator:${context.field?.name}:${operator}`,
            operator,
            "operator",
            "operator",
            operatorInsertion(operator, value, replacement),
            replacement,
          ),
        ]
      })
      break
    }
    case "operatorNot": {
      if (context.field?.operators.includes("NOT IN")) {
        const replacement = inContinuationReplacementRange(value, range)
        values = [
          createSuggestion(
            `operator:${context.field.name}:not-in-continuation`,
            "IN",
            "complete NOT IN",
            "operator",
            "IN (",
            replacement,
          ),
        ]
      }
      break
    }
    case "singleValue":
      if (context.field) {
        values = valueSuggestions(context.field, value, range)
      }
      break
    case "inOpen":
      {
        const replacement = syntaxReplacementRange(value, range, "(")
        values = [
          createSuggestion(
            "syntax:in-open",
            "(",
            "start value list",
            "syntax",
            syntaxInsertion("(", value, range, replacement),
            replacement,
          ),
        ]
      }
      break
    case "inValue": {
      const totalValues = Math.max(
        context.inValueCount,
        analysis.otherInValueCount,
      )
      if (context.field && totalValues < maximumQueryINValues) {
        values = valueSuggestions(context.field, value, range)
      }
      break
    }
    case "inAfterValue": {
      const totalValues = Math.max(
        context.inValueCount,
        analysis.otherInValueCount,
      )
      if (totalValues < maximumQueryINValues) {
        const replacement = syntaxReplacementRange(value, range, ",")
        values.push(
          createSuggestion(
            "syntax:in-comma",
            ",",
            "add value",
            "syntax",
            syntaxInsertion(",", value, range, replacement),
            replacement,
          ),
        )
      }
      if (!/^\s*,/.test(value.slice(range.end))) {
        const replacement = syntaxReplacementRange(value, range, ")")
        values.push(
          createSuggestion(
            "syntax:in-close",
            ")",
            "close value list",
            "syntax",
            syntaxInsertion(")", value, range, replacement),
            replacement,
          ),
        )
      }
      break
    }
    case "afterExpression":
      if (context.predicates < maximumQueryPredicates) {
        values.push(
          createSuggestion(
            "keyword:and",
            "AND",
            "logical operator",
            "keyword",
            operandKeywordInsertion("AND", value, range),
            range,
          ),
          createSuggestion(
            "keyword:or",
            "OR",
            "logical operator",
            "keyword",
            operandKeywordInsertion("OR", value, range),
            range,
          ),
        )
      }
      if (context.groups.length > 0) {
        const replacement = syntaxReplacementRange(value, range, ")")
        values.push(
          createSuggestion(
            "syntax:group-close",
            ")",
            "close group",
            "syntax",
            syntaxInsertion(")", value, range, replacement),
            replacement,
          ),
        )
      } else if (!analysis.hasOtherOrderBy) {
        values.push(orderBySuggestion(value, range))
      }
      break
    case "afterAll":
      if (!analysis.hasOtherOrderBy) values = [orderBySuggestion(value, range)]
      break
    case "orderByKeyword":
      values = [
        createSuggestion(
          "sort:by",
          "BY",
          "sort keyword",
          "sort",
          appendBoundarySpace("BY", value, range),
          range,
        ),
      ]
      break
    case "sortField": {
      const unavailable = new Set([
        ...context.usedSortFields,
        ...analysis.otherSortFields,
      ])
      const otherCount = Math.max(
        context.sortFields,
        analysis.otherSortFieldCount,
      )
      if (otherCount >= maximumQuerySortFields) break
      values = schema.fields
        .filter(
          (field) =>
            field.sortable && !unavailable.has(field.name.toLowerCase()),
        )
        .map((field) =>
          createSuggestion(
            `sort-field:${field.name}`,
            field.name,
            "sortable field",
            "field",
            appendBoundarySpace(field.name, value, range),
            range,
          ),
        )
      break
    }
    case "sortDirection":
      values = [
        createSuggestion(
          "sort:asc",
          "ASC",
          "ascending",
          "sort",
          appendBoundarySpace("ASC", value, range),
          range,
        ),
        createSuggestion(
          "sort:desc",
          "DESC",
          "descending",
          "sort",
          appendBoundarySpace("DESC", value, range),
          range,
        ),
      ]
      break
    case "sortAfterDirection": {
      const used = new Set([
        ...context.usedSortFields,
        ...analysis.otherSortFields,
      ])
      const count = Math.max(context.sortFields, analysis.otherSortFieldCount)
      const hasAvailableField = schema.fields.some(
        (field) => field.sortable && !used.has(field.name.toLowerCase()),
      )
      if (count < maximumQuerySortFields && hasAvailableField) {
        const replacement = syntaxReplacementRange(value, range, ",")
        values = [
          createSuggestion(
            "syntax:sort-comma",
            ",",
            "add sort field",
            "syntax",
            syntaxInsertion(",", value, range, replacement),
            replacement,
          ),
        ]
      }
      break
    }
    case "invalid":
      break
  }

  return filterSuggestions(values, partial)
}

/**
 * Compatibility insertion helper for collapsed-caret consumers.
 */
export function applyCollectionQuerySuggestion(
  value: string,
  suggestion: CollectionQuerySuggestion,
): { value: string; caret: number } {
  const result = applyCollectionQuerySuggestionForSelection(value, suggestion, {
    start: suggestion.replaceEnd,
    end: suggestion.replaceEnd,
  })
  return { value: result.value, caret: result.selectionStart }
}

/**
 * Applies a completion without ever partially truncating it. The original
 * value and selection are returned when the complete candidate exceeds 4 KiB.
 */
export function applyCollectionQuerySuggestionForSelection(
  value: string,
  suggestion: CollectionQuerySuggestion,
  selection: CollectionQuerySelection,
): CollectionQuerySelectionResult {
  const replacement = normalizeSelection(value, {
    start: suggestion.replaceStart,
    end: suggestion.replaceEnd,
  })
  const originalSelection = normalizeSelection(value, selection)
  const candidate = `${value.slice(0, replacement.start)}${suggestion.insertText}${value.slice(replacement.end)}`
  if (collectionQueryByteLength(candidate) > maximumCollectionQueryBytes) {
    return {
      value,
      selectionStart: originalSelection.start,
      selectionEnd: originalSelection.end,
      applied: false,
    }
  }
  const preservedBoundaryWhitespace = suggestion.insertText.endsWith(" ")
    ? 0
    : (value.slice(replacement.end).match(/^\s+/u)?.[0].length ?? 0)
  const caret =
    replacement.start +
    suggestion.insertText.length +
    preservedBoundaryWhitespace
  return {
    value: candidate,
    selectionStart: caret,
    selectionEnd: caret,
    applied: true,
  }
}

export function normalizeCollectionQuerySelection(
  value: string,
  selection: CollectionQuerySelection,
): CollectionQuerySelection {
  return normalizeSelection(value, selection)
}

function analyzeCompletion(
  value: string,
  selection: CollectionQuerySelection,
  schema: CollectionQuerySchema,
): CompletionAnalysis {
  const tokens = lexQuery(value)
  const normalized = normalizeSelection(value, selection)
  const range = replacementRange(value, normalized, tokens)
  const prefixTokens = tokens.filter((token) => token.end <= range.start)
  const context = parseCompletionPrefix(prefixTokens, schema)
  if (context.state === "operatorNot" && range.start < range.end) {
    const operatorEnd = extendRangeThroughSuffix(value, range, /^\s+IN\b/i).end
    if (operatorEnd > range.end) {
      context.state = "operator"
      range.end = operatorEnd
    }
  }
  const orderBy = findTopLevelOrderBy(tokens)
  const currentOrderBy =
    orderBy && orderBy.start < range.start ? orderBy : undefined
  const otherSort = currentOrderBy
    ? collectOtherSortFields(tokens, currentOrderBy.byEnd, range, schema.fields)
    : { fields: new Set<string>(), count: 0 }
  const otherInValueCount =
    context.inOpenEnd == null
      ? context.inValueCount
      : countOtherINValues(tokens, context.inOpenEnd, range)
  return {
    context,
    range,
    partial: completionPartial(value, range, normalized, tokens),
    tokens,
    hasOtherOrderBy: Boolean(
      orderBy &&
      !rangesOverlap(range, { start: orderBy.start, end: orderBy.byEnd }),
    ),
    otherSortFields: otherSort.fields,
    otherSortFieldCount: otherSort.count,
    otherInValueCount,
  }
}

function parseCompletionPrefix(
  tokens: QueryToken[],
  schema: CollectionQuerySchema,
): CompletionContext {
  const context: CompletionContext = {
    state: "operand",
    predicates: 0,
    rootStarted: false,
    pendingNot: 0,
    baseDepth: 0,
    groups: [],
    inValueCount: 0,
    usedSortFields: new Set(),
    sortFields: 0,
  }
  const fields = new Map(
    schema.fields.map((field) => [field.name.toLowerCase(), field]),
  )

  for (const token of tokens) {
    if (context.state === "invalid") break
    const word = token.kind === "word" ? token.text.toUpperCase() : ""
    switch (context.state) {
      case "operand": {
        if (token.kind === "leftParen") {
          if (currentDepth(context) >= maximumQueryDepth) {
            context.state = "invalid"
            break
          }
          context.groups.push({ baseDepth: context.baseDepth })
          context.baseDepth = currentDepth(context) + 1
          context.pendingNot = 0
          context.rootStarted = true
          break
        }
        if (token.kind !== "word") {
          context.state = "invalid"
          break
        }
        if (word === "NOT") {
          if (currentDepth(context) >= maximumQueryDepth) {
            context.state = "invalid"
          } else {
            context.pendingNot += 1
            context.rootStarted = true
          }
          break
        }
        const atRootStart = !context.rootStarted && context.groups.length === 0
        if (word === "ALL" && atRootStart) {
          context.rootStarted = true
          context.state = "afterAll"
          break
        }
        if (word === "ORDER" && atRootStart) {
          context.state = "orderByKeyword"
          break
        }
        const field = fields.get(token.text.toLowerCase())
        if (!field || context.predicates >= maximumQueryPredicates) {
          context.state = "invalid"
          break
        }
        context.predicates += 1
        context.rootStarted = true
        context.field = field
        context.state = "operator"
        break
      }
      case "operator": {
        if (!context.field) {
          context.state = "invalid"
          break
        }
        const operator = token.text.toUpperCase()
        if (
          token.kind === "word" &&
          operator === "NOT" &&
          context.field.operators.includes("NOT IN")
        ) {
          context.state = "operatorNot"
          break
        }
        const allowed = context.field.operators.find(
          (candidate) => candidate.toUpperCase() === operator,
        )
        if (!allowed) {
          context.state = "invalid"
        } else if (allowed === "IN" || allowed === "NOT IN") {
          context.state = "inOpen"
        } else {
          context.state = "singleValue"
        }
        break
      }
      case "operatorNot":
        context.state =
          token.kind === "word" && word === "IN" ? "inOpen" : "invalid"
        break
      case "singleValue":
        if (token.kind === "word" || token.kind === "string") {
          completePredicate(context)
        } else {
          context.state = "invalid"
        }
        break
      case "inOpen":
        if (token.kind === "leftParen") {
          context.inValueCount = 0
          context.inOpenEnd = token.end
          context.state = "inValue"
        } else {
          context.state = "invalid"
        }
        break
      case "inValue":
        if (token.kind === "word" || token.kind === "string") {
          context.inValueCount += 1
          context.state = "inAfterValue"
        } else {
          context.state = "invalid"
        }
        break
      case "inAfterValue":
        if (
          token.kind === "comma" &&
          context.inValueCount < maximumQueryINValues
        ) {
          context.state = "inValue"
        } else if (token.kind === "rightParen") {
          completePredicate(context)
        } else {
          context.state = "invalid"
        }
        break
      case "afterExpression":
        if (token.kind === "word" && (word === "AND" || word === "OR")) {
          context.pendingNot = 0
          context.state = "operand"
        } else if (token.kind === "rightParen" && context.groups.length > 0) {
          const frame = context.groups.pop()
          context.baseDepth = frame?.baseDepth ?? 0
        } else if (
          token.kind === "word" &&
          word === "ORDER" &&
          context.groups.length === 0
        ) {
          context.state = "orderByKeyword"
        } else {
          context.state = "invalid"
        }
        break
      case "afterAll":
        context.state =
          token.kind === "word" && word === "ORDER"
            ? "orderByKeyword"
            : "invalid"
        break
      case "orderByKeyword":
        context.state =
          token.kind === "word" && word === "BY" ? "sortField" : "invalid"
        break
      case "sortField": {
        const field =
          token.kind === "word"
            ? fields.get(token.text.toLowerCase())
            : undefined
        const canonical = field?.name.toLowerCase()
        if (
          !field?.sortable ||
          !canonical ||
          context.usedSortFields.has(canonical) ||
          context.sortFields >= maximumQuerySortFields
        ) {
          context.state = "invalid"
          break
        }
        context.pendingSortField = canonical
        context.state = "sortDirection"
        break
      }
      case "sortDirection":
        if (
          token.kind === "word" &&
          (word === "ASC" || word === "DESC") &&
          context.pendingSortField
        ) {
          context.usedSortFields.add(context.pendingSortField)
          context.pendingSortField = undefined
          context.sortFields += 1
          context.state = "sortAfterDirection"
        } else {
          context.state = "invalid"
        }
        break
      case "sortAfterDirection":
        context.state =
          token.kind === "comma" && context.sortFields < maximumQuerySortFields
            ? "sortField"
            : "invalid"
        break
    }
  }
  return context
}

function completePredicate(context: CompletionContext) {
  context.field = undefined
  context.pendingNot = 0
  context.inOpenEnd = undefined
  context.inValueCount = 0
  context.state = "afterExpression"
}

function currentDepth(context: CompletionContext): number {
  return context.baseDepth + context.pendingNot
}

function lexQuery(value: string): QueryToken[] {
  const tokens: QueryToken[] = []
  let index = 0
  while (index < value.length) {
    const character = value[index] ?? ""
    if (isQueryWhitespace(character)) {
      index += 1
      continue
    }
    if (character === "(" || character === ")" || character === ",") {
      const kind: TokenKind =
        character === "("
          ? "leftParen"
          : character === ")"
            ? "rightParen"
            : "comma"
      tokens.push({
        kind,
        raw: character,
        text: character,
        start: index,
        end: index + 1,
      })
      index += 1
      continue
    }
    if (character === "'" || character === '"') {
      const start = index
      const quote = character
      let decoded = ""
      let closed = false
      index += 1
      while (index < value.length) {
        const current = value[index] ?? ""
        if (current === quote) {
          index += 1
          closed = true
          break
        }
        if (current === "\\" && index + 1 < value.length) {
          const escaped = value[index + 1] ?? ""
          decoded += decodeQueryEscape(escaped)
          index += 2
          continue
        }
        decoded += current
        index += 1
      }
      tokens.push({
        kind: "string",
        raw: value.slice(start, index),
        text: decoded,
        start,
        end: index,
        closed,
      })
      continue
    }
    if (isOperatorStart(character)) {
      const start = index
      index += 1
      if (
        (character === "!" || character === ">" || character === "<") &&
        value[index] === "="
      ) {
        index += 1
      } else if (character === "!" && value[index] === "~") {
        index += 1
      }
      const raw = value.slice(start, index)
      tokens.push({
        kind: "operator",
        raw,
        text: raw,
        start,
        end: index,
      })
      continue
    }
    const start = index
    while (index < value.length && !isQueryDelimiter(value[index] ?? "")) {
      index += 1
    }
    if (start === index) {
      index += codePointLengthAt(value, index)
      continue
    }
    const raw = value.slice(start, index)
    tokens.push({ kind: "word", raw, text: raw, start, end: index })
  }
  return tokens
}

function replacementRange(
  value: string,
  selection: CollectionQuerySelection,
  tokens: QueryToken[],
): CollectionQuerySelection {
  if (selection.start !== selection.end) {
    const stringToken = tokens.find(
      (token) =>
        token.kind === "string" &&
        selection.start >= token.start &&
        selection.end <= token.end,
    )
    return stringToken
      ? { start: stringToken.start, end: stringToken.end }
      : selection
  }

  const caret = selection.start
  const token = tokens.find(
    (candidate) =>
      (candidate.kind === "word" ||
        candidate.kind === "string" ||
        candidate.kind === "operator") &&
      ((candidate.start <= caret && caret < candidate.end) ||
        (candidate.end === caret &&
          candidate.start < caret &&
          !/^[,)]/.test(value.slice(caret).trimStart()))),
  )
  if (!token) return selection
  return { start: token.start, end: token.end }
}

function completionPartial(
  value: string,
  range: CollectionQuerySelection,
  selection: CollectionQuerySelection,
  tokens: QueryToken[],
): string {
  if (range.start === range.end) return ""
  if (selection.start !== selection.end) return ""
  const token = tokens.find(
    (candidate) =>
      candidate.start === range.start && candidate.end === range.end,
  )
  if (token?.kind === "string") {
    const contentEnd = token.closed ? token.end - 1 : token.end
    const boundedEnd = Math.min(selection.end, contentEnd)
    const raw = value.slice(token.start + 1, boundedEnd)
    return decodePartialQuotedValue(raw)
  }
  const partialEnd = Math.min(selection.start, range.end)
  return value.slice(range.start, partialEnd)
}

function findTopLevelOrderBy(
  tokens: QueryToken[],
): { start: number; byEnd: number } | undefined {
  let depth = 0
  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index]
    if (!token) continue
    if (token.kind === "leftParen") {
      depth += 1
      continue
    }
    if (token.kind === "rightParen") {
      depth = Math.max(0, depth - 1)
      continue
    }
    const next = tokens[index + 1]
    if (
      depth === 0 &&
      token.kind === "word" &&
      token.text.toUpperCase() === "ORDER" &&
      next?.kind === "word" &&
      next.text.toUpperCase() === "BY"
    ) {
      return { start: token.start, byEnd: next.end }
    }
  }
  return undefined
}

function collectOtherSortFields(
  tokens: QueryToken[],
  byEnd: number,
  range: CollectionQuerySelection,
  fields: CollectionQueryField[],
): { fields: Set<string>; count: number } {
  const sortable = new Set(
    fields
      .filter((field) => field.sortable)
      .map((field) => field.name.toLowerCase()),
  )
  const used = new Set<string>()
  let count = 0
  let expectField = true
  for (const token of tokens) {
    if (token.start < byEnd) continue
    if (token.kind === "comma") {
      expectField = true
      continue
    }
    if (!expectField || token.kind !== "word") continue
    expectField = false
    if (rangesOverlap(range, token)) continue
    const field = token.text.toLowerCase()
    if (!sortable.has(field)) continue
    used.add(field)
    count += 1
  }
  return { fields: used, count }
}

function countOtherINValues(
  tokens: QueryToken[],
  openEnd: number,
  range: CollectionQuerySelection,
): number {
  let count = 0
  for (const token of tokens) {
    if (token.start < openEnd) continue
    if (token.kind === "rightParen") break
    if (
      (token.kind === "word" || token.kind === "string") &&
      !rangesOverlap(range, token)
    ) {
      count += 1
    }
  }
  return count
}

function valueSuggestions(
  field: CollectionQueryField,
  value: string,
  range: CollectionQuerySelection,
): CollectionQuerySuggestion[] {
  let values = field.suggested_values ?? []
  if (field.type === "boolean") values = ["true", "false"]
  if (field.type === "number") {
    values = values.filter(isFiniteQueryNumber)
  }
  if (field.type === "timestamp") {
    values = values.filter(isValidTimestampSuggestion)
    if (values.length === 0) values = ["-1h", "-24h", "-7d", "-30d"]
  }
  return values.map((rawValue) => {
    const rendered = renderQueryValue(field, rawValue)
    return createSuggestion(
      `value:${field.name}:${rawValue}`,
      rendered,
      `${field.type} value`,
      "value",
      appendBoundarySpace(rendered, value, range),
      range,
    )
  })
}

function renderQueryValue(field: CollectionQueryField, value: string): string {
  if (field.type === "string") return quoteQueryValue(value)
  if (
    (field.type === "enum" || field.type === "timestamp") &&
    !isSafeBareStringValue(value)
  ) {
    return quoteQueryValue(value)
  }
  return value
}

function quoteQueryValue(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`
}

function isSafeBareStringValue(value: string): boolean {
  return (
    value.length > 0 &&
    !/[\s(),=!~><'"\\]/u.test(value) &&
    !/^(?:ALL|AND|OR|NOT|IN|ORDER|BY|ASC|DESC)$/i.test(value)
  )
}

function isFiniteQueryNumber(value: string): boolean {
  if (finiteNumberPattern.test(value)) {
    const parsed = Number(value.replaceAll("_", ""))
    return Number.isFinite(parsed)
  }
  if (!hexadecimalFloatPattern.test(value)) return false
  const normalized = value.replaceAll("_", "")
  const hexadecimal =
    /^([+-]?)0x((?:[0-9a-f]+(?:\.[0-9a-f]*)?)|(?:\.[0-9a-f]+))p([+-]?\d+)$/i.exec(
      normalized,
    )
  if (!hexadecimal) return false
  const mantissa = hexadecimal[2] ?? ""
  const [integerPart = "", fractionalPart = ""] = mantissa.split(".")
  let magnitude = Number.parseInt(integerPart || "0", 16)
  for (let index = 0; index < fractionalPart.length; index += 1) {
    magnitude +=
      Number.parseInt(fractionalPart[index] ?? "0", 16) / 16 ** (index + 1)
  }
  const exponent = Number(hexadecimal[3])
  const parsed = magnitude * 2 ** exponent
  return Number.isFinite(parsed)
}

function isValidTimestampSuggestion(value: string): boolean {
  if (relativeTimestampPattern.test(value)) {
    const unit = value.at(-1)?.toLowerCase()
    if (!unit) return false
    const maximum = maximumRelativeTimestampAmounts[unit]
    if (maximum == null) return false
    try {
      return BigInt(value.slice(1, -1)) <= maximum
    } catch {
      return false
    }
  }
  if (datePattern.test(value)) {
    const [year, month, day] = value.split("-").map(Number)
    return (
      validCalendarDate(year ?? 0, month ?? 0, day ?? 0) &&
      !isZeroGoTimestamp(year ?? 0, month ?? 0, day ?? 0, 0, 0, 0, 0, 0)
    )
  }
  const match = rfc3339TimestampPattern.exec(value)
  if (!match) return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  const nanoseconds = Number((match[7] ?? "").slice(0, 9).padEnd(9, "0"))
  const offsetHour = match[9] == null ? 0 : Number(match[9])
  const offsetMinute = match[10] == null ? 0 : Number(match[10])
  return (
    validCalendarDate(year, month, day) &&
    hour <= 23 &&
    minute <= 59 &&
    second <= 59 &&
    offsetHour <= 23 &&
    offsetMinute <= 59 &&
    !isZeroGoTimestamp(
      year,
      month,
      day,
      hour,
      minute,
      second,
      nanoseconds,
      (match[8] === "-" ? 1 : -1) * (offsetHour * 60 + offsetMinute),
    )
  )
}

function isZeroGoTimestamp(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  second: number,
  nanoseconds: number,
  offsetMinutes: number,
): boolean {
  const zeroDay = gregorianDayNumber(1, 1, 1)
  const localDay = gregorianDayNumber(year, month, day)
  return (
    (localDay - zeroDay) * 24 * 60 + hour * 60 + minute + offsetMinutes === 0 &&
    second === 0 &&
    nanoseconds === 0
  )
}

function gregorianDayNumber(year: number, month: number, day: number): number {
  const adjustedYear = year - (month <= 2 ? 1 : 0)
  const era = Math.floor(adjustedYear / 400)
  const yearOfEra = adjustedYear - era * 400
  const adjustedMonth = month + (month > 2 ? -3 : 9)
  const dayOfYear = Math.floor((153 * adjustedMonth + 2) / 5) + day - 1
  const dayOfEra =
    yearOfEra * 365 +
    Math.floor(yearOfEra / 4) -
    Math.floor(yearOfEra / 100) +
    dayOfYear
  return era * 146_097 + dayOfEra
}

function validCalendarDate(year: number, month: number, day: number): boolean {
  if (month < 1 || month > 12 || day < 1) return false
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return day <= (days[month - 1] ?? 0)
}

function orderBySuggestion(
  value: string,
  range: CollectionQuerySelection,
): CollectionQuerySuggestion {
  const replacement = extendRangeThroughSuffix(value, range, /^\s+BY\b/i)
  return createSuggestion(
    "sort:order-by",
    "ORDER BY",
    "sort clause",
    "sort",
    appendBoundarySpace("ORDER BY", value, replacement),
    replacement,
  )
}

function operatorReplacementRange(
  operator: string,
  value: string,
  range: CollectionQuerySelection,
): CollectionQuerySelection {
  let replacement = range
  if (operator === "NOT IN") {
    replacement = extendRangeThroughSuffix(value, replacement, /^\s+IN\b/i)
  }
  if (operator === "IN" || operator === "NOT IN") {
    replacement = extendRangeThroughSuffix(value, replacement, /^\s*\(/)
  }
  return replacement
}

function selectedINList(
  value: string,
  range: CollectionQuerySelection,
): { end: number; rawValue: string; values: number } | undefined {
  const selectedOperator = value.slice(range.start, range.end)
  let operatorEnd = range.end
  if (/^NOT$/i.test(selectedOperator)) {
    const continuation = /^\s+IN\b/i.exec(value.slice(range.end))
    if (!continuation) return undefined
    operatorEnd += continuation[0].length
  } else if (!/^(?:IN|NOT\s+IN)$/i.test(selectedOperator)) return undefined
  const tokens = lexQuery(value)
  const openIndex = tokens.findIndex(
    (token) =>
      token.kind === "leftParen" &&
      token.start >= operatorEnd &&
      /^\s*$/u.test(value.slice(operatorEnd, token.start)),
  )
  if (openIndex < 0) return undefined
  const open = tokens[openIndex]
  if (!open) return undefined
  let values = 0
  for (let index = openIndex + 1; index < tokens.length; index += 1) {
    const token = tokens[index]
    if (!token) continue
    if (token.kind === "leftParen") return undefined
    if (token.kind === "rightParen") {
      return {
        end: token.end,
        rawValue: value.slice(open.end, token.start).trim(),
        values,
      }
    }
    if (token.kind === "word" || token.kind === "string") values += 1
  }
  return undefined
}

function inContinuationReplacementRange(
  value: string,
  range: CollectionQuerySelection,
): CollectionQuerySelection {
  return extendRangeThroughSuffix(value, range, /^\s*\(/)
}

function syntaxReplacementRange(
  value: string,
  range: CollectionQuerySelection,
  syntax: "(" | ")" | ",",
): CollectionQuerySelection {
  const escaped = syntax === "(" ? "\\(" : syntax === ")" ? "\\)" : ","
  return extendRangeThroughSuffix(value, range, new RegExp(`^\\s*${escaped}`))
}

function extendRangeThroughSuffix(
  value: string,
  range: CollectionQuerySelection,
  pattern: RegExp,
): CollectionQuerySelection {
  const match = pattern.exec(value.slice(range.end))
  return match?.[0]
    ? { start: range.start, end: range.end + match[0].length }
    : range
}

function operatorInsertion(
  operator: string,
  value: string,
  range: CollectionQuerySelection,
): string {
  if (operator === "IN" || operator === "NOT IN") {
    return `${operator} (`
  }
  return appendBoundarySpace(operator, value, range)
}

function appendBoundarySpace(
  insertion: string,
  value: string,
  range: CollectionQuerySelection,
): string {
  if (range.end >= value.length) return `${insertion} `
  const suffix = value.slice(range.end)
  if (/^\s/u.test(suffix) || /^[,)]/.test(suffix)) return insertion
  return `${insertion} `
}

function operandKeywordInsertion(
  keyword: "AND" | "NOT" | "OR",
  value: string,
  range: CollectionQuerySelection,
): string {
  return /^\s/u.test(value.slice(range.end)) ? keyword : `${keyword} `
}

function filterSuggestions(
  suggestions: CollectionQuerySuggestion[],
  partial: string,
): CollectionQuerySuggestion[] {
  const needle = partial.trimStart().replace(/^["']/, "").toLowerCase()
  return suggestions
    .filter((suggestion) => {
      if (!needle) return true
      const label = suggestion.label.toLowerCase()
      const unquoted = decodeRenderedValue(suggestion.label).toLowerCase()
      return label.startsWith(needle) || unquoted.startsWith(needle)
    })
    .slice(0, 24)
}

function createSuggestion(
  id: string,
  label: string,
  detail: string,
  kind: CollectionQuerySuggestionKind,
  insertText: string,
  range: CollectionQuerySelection,
): CollectionQuerySuggestion {
  return {
    id,
    label,
    detail,
    kind,
    insertText,
    replaceStart: range.start,
    replaceEnd: range.end,
  }
}

function normalizeSelection(
  value: string,
  selection: CollectionQuerySelection,
): CollectionQuerySelection {
  const rawStart = clampOffset(selection.start, value.length)
  const rawEnd = clampOffset(selection.end, value.length)
  const lower = Math.min(rawStart, rawEnd)
  const upper = Math.max(rawStart, rawEnd)
  if (lower === upper) {
    const caret = scalarBoundaryBefore(value, lower)
    return { start: caret, end: caret }
  }
  return {
    start: scalarBoundaryBefore(value, lower),
    end: scalarBoundaryAfter(value, upper),
  }
}

function clampOffset(value: number, length: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(length, Math.max(0, Math.trunc(value)))
}

function scalarBoundaryBefore(value: string, offset: number): number {
  if (
    offset > 0 &&
    offset < value.length &&
    isHighSurrogate(value.charCodeAt(offset - 1)) &&
    isLowSurrogate(value.charCodeAt(offset))
  ) {
    return offset - 1
  }
  return offset
}

function scalarBoundaryAfter(value: string, offset: number): number {
  if (
    offset > 0 &&
    offset < value.length &&
    isHighSurrogate(value.charCodeAt(offset - 1)) &&
    isLowSurrogate(value.charCodeAt(offset))
  ) {
    return offset + 1
  }
  return offset
}

function isHighSurrogate(value: number): boolean {
  return value >= 0xd800 && value <= 0xdbff
}

function isLowSurrogate(value: number): boolean {
  return value >= 0xdc00 && value <= 0xdfff
}

function rangesOverlap(
  left: { start: number; end: number },
  right: { start: number; end: number },
): boolean {
  if (left.start === left.end) {
    return left.start > right.start && left.start < right.end
  }
  return left.start < right.end && right.start < left.end
}

function decodePartialQuotedValue(value: string): string {
  let decoded = ""
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index] ?? ""
    if (character === "\\" && index + 1 < value.length) {
      decoded += decodeQueryEscape(value[index + 1] ?? "")
      index += 1
    } else {
      decoded += character
    }
  }
  return decoded
}

function decodeRenderedValue(value: string): string {
  if (!value.startsWith('"') || !value.endsWith('"')) return value
  return decodePartialQuotedValue(value.slice(1, -1))
}

function decodeQueryEscape(value: string): string {
  if (value === "n") return "\n"
  if (value === "r") return "\r"
  if (value === "t") return "\t"
  return value
}

function isQueryWhitespace(value: string): boolean {
  return value === " " || value === "\t" || value === "\r" || value === "\n"
}

function isOperatorStart(value: string): boolean {
  return (
    value === "=" ||
    value === "!" ||
    value === "~" ||
    value === ">" ||
    value === "<"
  )
}

function syntaxInsertion(
  syntax: "(" | ")" | ",",
  value: string,
  originalRange: CollectionQuerySelection,
  replacement: CollectionQuerySelection,
): string {
  return replacement.end > originalRange.end
    ? syntax
    : appendBoundarySpace(syntax, value, replacement)
}

function isQueryDelimiter(value: string): boolean {
  return (
    isQueryWhitespace(value) ||
    value === "(" ||
    value === ")" ||
    value === "," ||
    value === "'" ||
    value === '"' ||
    isOperatorStart(value)
  )
}

function codePointLengthAt(value: string, index: number): number {
  const code = value.codePointAt(index)
  return code != null && code > 0xffff ? 2 : 1
}
