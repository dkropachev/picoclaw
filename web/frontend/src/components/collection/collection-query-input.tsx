import { IconSearch, IconX } from "@tabler/icons-react"
import {
  type FormEvent,
  type KeyboardEvent,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import {
  type CollectionQueryField,
  type CollectionQuerySchema,
  collectionQueryByteLength,
  collectionUTF8BytePositionToUTF16Offset,
  maximumCollectionQueryBytes,
  truncateCollectionQuery,
} from "@/api/collection"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

/* eslint-disable react-refresh/only-export-components -- The exported autocomplete helpers are pure and keep caret insertion behavior identical in the component and focused tests. */

export type CollectionQuerySuggestionKind =
  | "field"
  | "keyword"
  | "operator"
  | "sort"
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

export interface CollectionQueryInputError {
  message: string
  position?: number
}

export function CollectionQueryInput({
  activeQuery,
  defaultQuery,
  schema,
  error,
  disabled = false,
  onApply,
  placeholder = "Filter with a collection query…",
  ariaLabel = "Collection query",
}: {
  activeQuery: string
  defaultQuery: string
  schema?: CollectionQuerySchema
  error?: CollectionQueryInputError
  disabled?: boolean
  onApply: (query: string) => void
  placeholder?: string
  ariaLabel?: string
}) {
  const boundedActiveQuery = truncateCollectionQuery(activeQuery)
  const boundedDefaultQuery = truncateCollectionQuery(defaultQuery)
  const inputRef = useRef<HTMLInputElement>(null)
  const pendingCaretRef = useRef<number | null>(null)
  const listboxID = useId()
  const errorID = useId()
  const [draft, setDraft] = useState(boundedActiveQuery)
  const [caret, setCaret] = useState(boundedActiveQuery.length)
  const [focused, setFocused] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const [activeIndex, setActiveIndex] = useState(-1)
  const suggestions = useMemo(
    () => getCollectionQuerySuggestions(draft, caret, schema),
    [caret, draft, schema],
  )
  const open = focused && !dismissed && suggestions.length > 0
  const activeSuggestion =
    activeIndex >= 0 ? suggestions[activeIndex] : undefined

  useEffect(() => {
    setDraft(boundedActiveQuery)
    setCaret(boundedActiveQuery.length)
    setDismissed(false)
    setActiveIndex(-1)
  }, [boundedActiveQuery])

  useLayoutEffect(() => {
    const pending = pendingCaretRef.current
    if (pending == null) return
    pendingCaretRef.current = null
    inputRef.current?.focus()
    inputRef.current?.setSelectionRange(pending, pending)
    setCaret(pending)
  }, [draft])

  const updateCaret = (target: HTMLInputElement) => {
    setCaret(target.selectionStart ?? target.value.length)
  }

  const restoreActiveQuery = () => {
    setDraft(boundedActiveQuery)
    setCaret(boundedActiveQuery.length)
    setDismissed(true)
    setActiveIndex(-1)
    pendingCaretRef.current = boundedActiveQuery.length
  }

  const applySuggestion = (suggestion: CollectionQuerySuggestion) => {
    const applied = applyCollectionQuerySuggestion(draft, suggestion)
    pendingCaretRef.current = applied.caret
    setDraft(applied.value)
    setCaret(applied.caret)
    setDismissed(false)
    setActiveIndex(-1)
  }

  const applyDraft = () => {
    const normalized =
      truncateCollectionQuery(draft.trim()) || boundedDefaultQuery
    setDraft(normalized)
    setCaret(normalized.length)
    setDismissed(true)
    setActiveIndex(-1)
    onApply(normalized)
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    applyDraft()
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Escape") {
      event.preventDefault()
      restoreActiveQuery()
      return
    }
    if (
      event.key === " " &&
      (event.ctrlKey || event.metaKey) &&
      suggestions.length > 0
    ) {
      event.preventDefault()
      setDismissed(false)
      setActiveIndex(0)
      return
    }
    if (!open) return
    if (event.key === "ArrowDown") {
      event.preventDefault()
      setActiveIndex((current) =>
        current < suggestions.length - 1 ? current + 1 : 0,
      )
      return
    }
    if (event.key === "ArrowUp") {
      event.preventDefault()
      setActiveIndex((current) =>
        current > 0 ? current - 1 : suggestions.length - 1,
      )
      return
    }
    if ((event.key === "Enter" || event.key === "Tab") && activeSuggestion) {
      event.preventDefault()
      applySuggestion(activeSuggestion)
    }
  }

  const errorOffset =
    typeof error?.position === "number"
      ? collectionUTF8BytePositionToUTF16Offset(draft, error.position)
      : undefined

  useEffect(() => {
    const input = inputRef.current
    if (!input || errorOffset == null || input !== document.activeElement)
      return
    const characterLength = Array.from(draft.slice(errorOffset))[0]?.length ?? 0
    input.setSelectionRange(
      errorOffset,
      Math.min(draft.length, errorOffset + characterLength),
    )
    setCaret(errorOffset)
  }, [draft, errorOffset])

  return (
    <form
      data-slot="collection-query-input"
      className="min-w-0 flex-1"
      onSubmit={submit}
    >
      <div className="relative">
        <IconSearch
          aria-hidden="true"
          className="text-muted-foreground pointer-events-none absolute top-2.5 left-3 z-10 size-4"
        />
        <Input
          ref={inputRef}
          value={draft}
          disabled={disabled}
          spellCheck={false}
          autoComplete="off"
          role="combobox"
          aria-label={ariaLabel}
          aria-autocomplete="list"
          aria-controls={listboxID}
          aria-expanded={open}
          aria-activedescendant={
            open && activeSuggestion
              ? `${listboxID}-option-${activeIndex}`
              : undefined
          }
          aria-invalid={Boolean(error)}
          aria-describedby={error ? errorID : undefined}
          className="pr-10 pl-9 font-mono text-xs"
          placeholder={placeholder}
          onChange={(event) => {
            const value = truncateCollectionQuery(event.target.value)
            setDraft(value)
            setCaret(
              Math.min(
                event.target.selectionStart ?? value.length,
                value.length,
              ),
            )
            setDismissed(false)
            setActiveIndex(-1)
          }}
          onFocus={(event) => {
            setFocused(true)
            setDismissed(false)
            updateCaret(event.currentTarget)
          }}
          onBlur={() => {
            setFocused(false)
            setActiveIndex(-1)
          }}
          onClick={(event) => {
            updateCaret(event.currentTarget)
            setDismissed(false)
          }}
          onSelect={(event) => updateCaret(event.currentTarget)}
          onKeyUp={(event) => {
            if (event.key !== "ArrowDown" && event.key !== "ArrowUp") {
              updateCaret(event.currentTarget)
            }
          }}
          onKeyDown={handleKeyDown}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          disabled={disabled}
          className="absolute top-1.5 right-1.5"
          aria-label="Clear query"
          title="Restore the collection default query"
          onClick={() => {
            setDraft(boundedDefaultQuery)
            setCaret(boundedDefaultQuery.length)
            setDismissed(true)
            setActiveIndex(-1)
            pendingCaretRef.current = boundedDefaultQuery.length
            onApply(boundedDefaultQuery)
          }}
        >
          <IconX />
        </Button>
        {open && (
          <div
            id={listboxID}
            role="listbox"
            aria-label="Collection query suggestions"
            className="bg-popover text-popover-foreground border-border absolute z-50 mt-1 max-h-72 w-full overflow-y-auto rounded-md border p-1 shadow-md"
          >
            {suggestions.map((suggestion, index) => (
              <button
                key={suggestion.id}
                id={`${listboxID}-option-${index}`}
                type="button"
                role="option"
                aria-selected={index === activeIndex}
                tabIndex={-1}
                className="hover:bg-muted aria-selected:bg-muted flex w-full items-center justify-between gap-3 rounded-sm px-2 py-1.5 text-left text-xs outline-none"
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => applySuggestion(suggestion)}
              >
                <span className="min-w-0 truncate font-mono">
                  {suggestion.label}
                </span>
                <span className="text-muted-foreground shrink-0">
                  {suggestion.detail}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="mt-1 flex min-h-4 items-start justify-between gap-3 px-1 text-xs">
        {error ? (
          <p id={errorID} role="alert" className="text-destructive">
            {errorOffset == null ? "" : `Character ${errorOffset + 1}: `}
            {error.message}
          </p>
        ) : (
          <span className="text-muted-foreground">
            Enter applies · Escape restores active query
          </span>
        )}
        <span className="text-muted-foreground ml-auto shrink-0 font-mono tabular-nums">
          {collectionQueryByteLength(draft)}/{maximumCollectionQueryBytes}
        </span>
      </div>
    </form>
  )
}

export function getCollectionQuerySuggestions(
  value: string,
  caretPosition: number,
  schema?: CollectionQuerySchema,
): CollectionQuerySuggestion[] {
  if (!schema || schema.fields.length === 0) return []
  const caret = Math.min(Math.max(0, caretPosition), value.length)
  const range = collectionQueryTokenRange(value, caret)
  const partial = value.slice(range.start, caret)
  const before = value.slice(0, range.start)
  const orderBy = topLevelOrderBy(before)

  if (orderBy) {
    return sortSuggestions(
      before.slice(orderBy.end),
      partial,
      range,
      schema.fields,
    )
  }

  const tail = expressionTail(before)
  const valueField = fieldAwaitingValue(tail, schema.fields)
  if (valueField) {
    return filterSuggestions(valueSuggestions(valueField, range), partial)
  }

  const operatorField = schema.fields.find(
    (field) => field.name.toLowerCase() === tail.toLowerCase(),
  )
  if (operatorField) {
    return filterSuggestions(
      operatorField.operators.map((operator) =>
        suggestion(
          `operator:${operator}`,
          operator,
          "operator",
          "operator",
          `${operator} `,
          range,
        ),
      ),
      partial,
    )
  }

  if (/^ORDER$/i.test(tail)) {
    return filterSuggestions(
      [suggestion("sort:by", "BY", "sort keyword", "sort", "BY ", range)],
      partial,
    )
  }

  if (tail === "") {
    const values = schema.fields.map((field) =>
      suggestion(
        `field:${field.name}`,
        field.name,
        `${field.type} field`,
        "field",
        `${field.name} `,
        range,
      ),
    )
    values.push(
      suggestion("keyword:not", "NOT", "keyword", "keyword", "NOT ", range),
      suggestion(
        "keyword:order-by",
        "ORDER BY",
        "sort clause",
        "sort",
        "ORDER BY ",
        range,
      ),
    )
    return filterSuggestions(values, partial)
  }

  if (completedPredicateTail(tail) || tail.endsWith(")")) {
    return filterSuggestions(
      [
        suggestion("keyword:and", "AND", "keyword", "keyword", "AND ", range),
        suggestion("keyword:or", "OR", "keyword", "keyword", "OR ", range),
        suggestion(
          "keyword:order-by",
          "ORDER BY",
          "sort clause",
          "sort",
          "ORDER BY ",
          range,
        ),
      ],
      partial,
    )
  }

  return filterSuggestions(
    schema.fields.map((field) =>
      suggestion(
        `field:${field.name}`,
        field.name,
        `${field.type} field`,
        "field",
        `${field.name} `,
        range,
      ),
    ),
    partial,
  )
}

export function applyCollectionQuerySuggestion(
  value: string,
  suggestionValue: CollectionQuerySuggestion,
): { value: string; caret: number } {
  const suffix = value.slice(suggestionValue.replaceEnd)
  const normalizedSuffix =
    suggestionValue.insertText.endsWith(" ") && /^\s/.test(suffix)
      ? suffix.replace(/^\s+/, "")
      : suffix
  const next = truncateCollectionQuery(
    `${value.slice(0, suggestionValue.replaceStart)}${suggestionValue.insertText}${normalizedSuffix}`,
  )
  return {
    value: next,
    caret: Math.min(
      suggestionValue.replaceStart + suggestionValue.insertText.length,
      next.length,
    ),
  }
}

function sortSuggestions(
  sortBefore: string,
  partial: string,
  range: { start: number; end: number },
  fields: CollectionQueryField[],
): CollectionQuerySuggestion[] {
  const segments = sortBefore.split(",")
  const segment = segments.at(-1)?.trim() ?? ""
  const usedFields = new Set(
    segments
      .slice(0, -1)
      .map((value) => value.trim().split(/\s+/)[0]?.toLowerCase())
      .filter((value): value is string => Boolean(value)),
  )
  const sortable = fields.filter(
    (field) => field.sortable && !usedFields.has(field.name.toLowerCase()),
  )
  if (!segment) {
    return filterSuggestions(
      sortable.map((field) =>
        suggestion(
          `sort-field:${field.name}`,
          field.name,
          "sortable field",
          "field",
          `${field.name} `,
          range,
        ),
      ),
      partial,
    )
  }

  const tokens = segment.split(/\s+/)
  const field = sortable.find(
    (candidate) => candidate.name.toLowerCase() === tokens[0]?.toLowerCase(),
  )
  if (field && tokens.length === 1) {
    return filterSuggestions(
      [
        suggestion("sort:asc", "ASC", "ascending", "sort", "ASC", range),
        suggestion("sort:desc", "DESC", "descending", "sort", "DESC", range),
      ],
      partial,
    )
  }
  if (field && tokens.length === 2 && /^(ASC|DESC)$/i.test(tokens[1] ?? "")) {
    if (segments.length >= 3) return []
    return [suggestion("sort:next", ",", "add sort field", "sort", ", ", range)]
  }
  return []
}

function fieldAwaitingValue(
  tail: string,
  fields: CollectionQueryField[],
): CollectionQueryField | undefined {
  for (const field of [...fields].sort(
    (left, right) => right.name.length - left.name.length,
  )) {
    const operators = field.operators
      .map((operator) => escapeRegExp(operator).replace(/\s+/g, "\\s+"))
      .sort((left, right) => right.length - left.length)
      .join("|")
    if (!operators) continue
    const pattern = new RegExp(
      `^${escapeRegExp(field.name)}\\s*(?:${operators})\\s*(?:\\(\\s*)?$`,
      "i",
    )
    if (pattern.test(tail)) return field
  }
  return undefined
}

function valueSuggestions(
  field: CollectionQueryField,
  range: { start: number; end: number },
): CollectionQuerySuggestion[] {
  let values = field.suggested_values ?? []
  if (field.type === "boolean") values = ["true", "false"]
  if (field.type === "timestamp" && values.length === 0) {
    values = ["now", "-24h", "-7d", "-30d"]
  }
  return values.slice(0, 24).map((value) => {
    const rendered = renderQueryValue(field, value)
    return suggestion(
      `value:${field.name}:${value}`,
      rendered,
      `${field.type} value`,
      "value",
      `${rendered} `,
      range,
    )
  })
}

function renderQueryValue(field: CollectionQueryField, value: string): string {
  if (field.type !== "string") return value
  if (/^(["']).*\1$/.test(value)) return value
  return JSON.stringify(value)
}

function completedPredicateTail(tail: string): boolean {
  return /(?:NOT\s+IN|IN|!=|!~|>=|<=|=|~|>|<)\s*(?:\([^)]*\)|[^\s]+)\s*$/i.test(
    tail,
  )
}

function expressionTail(before: string): string {
  let boundaryEnd = 0
  for (const boundary of before.matchAll(/\b(?:AND|OR)\b|\(/gi)) {
    const index = boundary.index ?? 0
    if (
      boundary[0] === "(" &&
      /\b(?:NOT\s+IN|IN)\s*$/i.test(before.slice(0, index))
    ) {
      continue
    }
    boundaryEnd = index + boundary[0].length
  }
  return before.slice(boundaryEnd).trim()
}

function topLevelOrderBy(value: string): { start: number; end: number } | null {
  let quote = ""
  let depth = 0
  for (let index = 0; index < value.length; index += 1) {
    const character = value[index] ?? ""
    if (quote) {
      if (character === "\\") index += 1
      else if (character === quote) quote = ""
      continue
    }
    if (character === '"' || character === "'") {
      quote = character
      continue
    }
    if (character === "(") depth += 1
    if (character === ")") depth = Math.max(0, depth - 1)
    if (depth !== 0) continue
    const match = value.slice(index).match(/^ORDER\s+BY\b/i)
    if (match) return { start: index, end: index + match[0].length }
  }
  return null
}

function collectionQueryTokenRange(
  value: string,
  caret: number,
): { start: number; end: number } {
  let start = caret
  let end = caret
  while (start > 0 && /[A-Za-z0-9_.:/!<>=~-]/.test(value[start - 1] ?? "")) {
    start -= 1
  }
  while (end < value.length && /[A-Za-z0-9_.:/!<>=~-]/.test(value[end] ?? "")) {
    end += 1
  }
  return { start, end }
}

function filterSuggestions(
  values: CollectionQuerySuggestion[],
  partial: string,
): CollectionQuerySuggestion[] {
  const needle = partial.toLowerCase()
  return values
    .filter((value) => {
      const label = value.label.toLowerCase()
      return (
        !needle ||
        label.startsWith(needle) ||
        label.replace(/^["']/, "").startsWith(needle)
      )
    })
    .slice(0, 24)
}

function suggestion(
  id: string,
  label: string,
  detail: string,
  kind: CollectionQuerySuggestionKind,
  insertText: string,
  range: { start: number; end: number },
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

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
}
