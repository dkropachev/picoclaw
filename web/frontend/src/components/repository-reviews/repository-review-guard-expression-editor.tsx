import { IconHelpCircle } from "@tabler/icons-react"
import { useId, useLayoutEffect, useMemo, useRef, useState } from "react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Textarea } from "@/components/ui/textarea"

type GuardValueType = "boolean" | "number" | "string"
type GuardSuggestionKind =
  | "field"
  | "grouping"
  | "keyword"
  | "literal"
  | "operator"

interface GuardField {
  value: string
  valueType: GuardValueType
}

interface GuardSuggestion {
  value: string
  kind: GuardSuggestionKind
  detail: string
}

interface GuardAutocompleteToken {
  kind:
    | "and"
    | "comparison"
    | "left-parenthesis"
    | "not"
    | "operand"
    | "or"
    | "right-parenthesis"
  valueType?: GuardValueType
}

const baseGuardFields: GuardField[] = [
  { value: "spent.tokens.total", valueType: "number" },
  { value: "spent.tokens.prompt", valueType: "number" },
  { value: "spent.tokens.completion", valueType: "number" },
  { value: "spent.tokens.cached", valueType: "number" },
  { value: "spend.total.usd", valueType: "number" },
  { value: "account.limits.known", valueType: "boolean" },
  { value: "account.limits.exhausted_known", valueType: "boolean" },
  { value: "account.limits.exhausted", valueType: "boolean" },
  { value: "account.limits.any", valueType: "boolean" },
]

const windowGuardFields: Array<{
  suffix: string
  valueType: GuardValueType
}> = [
  { suffix: "known", valueType: "boolean" },
  { suffix: "observed", valueType: "boolean" },
  { suffix: "remaining_percent", valueType: "number" },
  { suffix: "used_percent", valueType: "number" },
  { suffix: "minimum_remaining_percent", valueType: "number" },
  { suffix: "maximum_used_percent", valueType: "number" },
]

const keywordSuggestions: GuardSuggestion[] = [
  { value: "and", kind: "keyword", detail: "keyword" },
  { value: "or", kind: "keyword", detail: "keyword" },
  { value: "not", kind: "keyword", detail: "keyword" },
]

const booleanLiteralSuggestions: GuardSuggestion[] = [
  { value: "true", kind: "literal", detail: "boolean" },
  { value: "false", kind: "literal", detail: "boolean" },
]

const numberLiteralSuggestions: GuardSuggestion[] = [0, 10, 25, 50, 100].map(
  (value) => ({ value: String(value), kind: "literal", detail: "number" }),
)

const stringLiteralSuggestions: GuardSuggestion[] = [
  { value: '""', kind: "literal", detail: "string" },
]

const openingParenthesisSuggestion: GuardSuggestion = {
  value: "(",
  kind: "grouping",
  detail: "grouping",
}

const closingParenthesisSuggestion: GuardSuggestion = {
  value: ")",
  kind: "grouping",
  detail: "grouping",
}

const comparisonSuggestions: GuardSuggestion[] = [
  "=",
  "==",
  "!=",
  "<",
  "<=",
  ">",
  ">=",
].map((value) => ({ value, kind: "operator", detail: "operator" }))

const booleanComparisonSuggestions = comparisonSuggestions.filter(
  (suggestion) =>
    suggestion.value === "=" ||
    suggestion.value === "==" ||
    suggestion.value === "!=",
)

const logicalSuggestions = keywordSuggestions.filter(
  (suggestion) => suggestion.value !== "not",
)

export function RepositoryReviewGuardExpressionEditor({
  value,
  limitWindows,
  onChange,
}: {
  value: string
  limitWindows: string[]
  onChange: (value: string) => void
}) {
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const pendingCursorRef = useRef<number | null>(null)
  const listboxId = useId()
  const [focused, setFocused] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const [cursorPosition, setCursorPosition] = useState(value.length)
  const [activeIndex, setActiveIndex] = useState(-1)
  const fields = useMemo(() => guardFields(limitWindows), [limitWindows])
  const suggestions = guardSuggestions(value, cursorPosition, fields)
  const open = focused && !dismissed && suggestions.length > 0
  const activeSuggestion = activeIndex >= 0 ? suggestions[activeIndex] : null

  const updateSelection = (target: HTMLTextAreaElement) => {
    setCursorPosition(target.selectionStart ?? target.value.length)
    setActiveIndex(-1)
  }

  useLayoutEffect(() => {
    const pendingCursor = pendingCursorRef.current
    if (pendingCursor === null) return
    pendingCursorRef.current = null
    inputRef.current?.focus()
    inputRef.current?.setSelectionRange(pendingCursor, pendingCursor)
    setCursorPosition(pendingCursor)
  }, [value])

  const applySuggestion = (suggestion: GuardSuggestion) => {
    const input = inputRef.current
    const selectionStart = input?.selectionStart ?? cursorPosition
    const selectionEnd = input?.selectionEnd ?? selectionStart
    const inserted = insertGuardSuggestion(
      value,
      suggestion.value,
      selectionStart,
      selectionEnd,
    )
    const valueChanged = inserted.value !== value
    pendingCursorRef.current = valueChanged ? inserted.cursor : null
    onChange(inserted.value)
    if (!valueChanged) {
      input?.focus()
      input?.setSelectionRange(inserted.cursor, inserted.cursor)
    }
    setCursorPosition(inserted.cursor)
    setDismissed(false)
    setActiveIndex(-1)
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1">
        <Label htmlFor="review-guard-expression">Guard expression</Label>
        <Popover>
          <PopoverTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="text-muted-foreground"
              aria-label="Guard expression help"
              title="Guard expression help"
            >
              <IconHelpCircle aria-hidden="true" className="size-3.5" />
            </Button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="w-[min(32rem,calc(100vw-2rem))] space-y-2 text-xs"
          >
            <p className="font-medium">Guard expression reference</p>
            <p className="text-muted-foreground">
              Expression runs before each worker task and must be true. False,
              unknown, or errors pause admission.
            </p>
            <p>
              Operators: AND, OR, NOT, parentheses, =, ==, !=, &lt;, &lt;=,
              &gt;, &gt;=. Literals may be numbers, true/false, or quoted text.
            </p>
            <p>
              Token fields: spent.tokens.prompt, completion, cached, total;
              cost: spend.total.usd.
            </p>
            <p>
              Limits: account.limits.known, exhausted_known, exhausted, any, and
              account.limits.&lt;window&gt;.known, observed, remaining_percent,
              or used_percent. For partial telemetry, observed plus
              minimum_remaining_percent or maximum_used_percent exposes the
              conservative known subset. Common windows include daily and
              weekly. The * in field-family names is documentation, not wildcard
              syntax.
            </p>
          </PopoverContent>
        </Popover>
      </div>
      <p id="review-task-guard-help" className="text-muted-foreground text-xs">
        Blank allows all tasks. Start typing or press Ctrl+Space for fields and
        operators.
      </p>
      <div className="relative">
        <Textarea
          ref={inputRef}
          id="review-guard-expression"
          aria-label="Guard expression"
          aria-describedby="review-task-guard-help"
          aria-autocomplete="list"
          aria-controls={listboxId}
          aria-expanded={open}
          aria-activedescendant={
            open && activeSuggestion
              ? `${listboxId}-option-${activeIndex}`
              : undefined
          }
          role="combobox"
          className="min-h-28 font-mono text-xs"
          spellCheck={false}
          value={value}
          onChange={(event) => {
            updateSelection(event.target)
            setDismissed(false)
            setActiveIndex(-1)
            onChange(event.target.value)
          }}
          onFocus={(event) => {
            updateSelection(event.currentTarget)
            setFocused(true)
          }}
          onBlur={() => {
            setFocused(false)
            setActiveIndex(-1)
          }}
          onClick={(event) => {
            updateSelection(event.currentTarget)
            setDismissed(false)
            setActiveIndex(-1)
          }}
          onSelect={(event) => updateSelection(event.currentTarget)}
          onKeyUp={(event) => {
            if (
              open &&
              (event.key === "ArrowDown" || event.key === "ArrowUp")
            ) {
              return
            }
            updateSelection(event.currentTarget)
          }}
          onKeyDown={(event) => {
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
            if (
              (event.key === "Enter" || event.key === "Tab") &&
              activeSuggestion
            ) {
              event.preventDefault()
              applySuggestion(activeSuggestion)
              return
            }
            if (event.key === "Escape") {
              event.preventDefault()
              setDismissed(true)
              setActiveIndex(-1)
            }
          }}
          placeholder={
            "account.limits.weekly.known and account.limits.weekly.remaining_percent >= 10 and\nspent.tokens.total < 500000 and spend.total.usd < 25"
          }
        />
        {open && (
          <div
            id={listboxId}
            role="listbox"
            aria-label="Guard expression suggestions"
            className="bg-popover text-popover-foreground border-border absolute z-20 mt-1 max-h-64 w-full overflow-y-auto rounded-md border p-1 shadow-md"
          >
            {suggestions.map((suggestion, index) => (
              <button
                key={`${suggestion.kind}:${suggestion.value}`}
                id={`${listboxId}-option-${index}`}
                type="button"
                role="option"
                aria-label={`${suggestion.value} ${suggestion.detail}`}
                aria-selected={index === activeIndex}
                tabIndex={-1}
                className="hover:bg-muted focus:bg-muted aria-selected:bg-muted flex w-full items-center justify-between gap-3 rounded-sm px-2 py-1.5 text-left text-xs outline-none"
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => applySuggestion(suggestion)}
              >
                <span className="min-w-0 truncate font-mono">
                  {suggestion.value}
                </span>
                <span className="text-muted-foreground shrink-0">
                  {suggestion.detail}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function guardFields(limitWindows: string[]): GuardField[] {
  const windows = new Set(["any", "daily", "weekly"])
  for (const value of limitWindows) {
    const normalized = normalizeLimitWindow(value)
    if (normalized && normalized !== "unknown") windows.add(normalized)
  }
  const dynamicFields = [...windows].flatMap((window) =>
    windowGuardFields.map(({ suffix, valueType }) => ({
      value: `account.limits.${window}.${suffix}`,
      valueType,
    })),
  )
  return [...baseGuardFields, ...dynamicFields]
}

function normalizeLimitWindow(value: string): string {
  const lower = value.trim().toLowerCase()
  if (!lower) return ""
  if (lower.includes("week") || lower === "7d") return "weekly"
  if (lower.includes("day") || lower === "24h") return "daily"
  return lower.replace(/[^a-z0-9_-]+/g, "_").replace(/^_+|_+$/g, "")
}

function guardSuggestions(
  text: string,
  cursor: number,
  fields: GuardField[],
): GuardSuggestion[] {
  const fieldSuggestions = fields.map<GuardSuggestion>((field) => ({
    value: field.value,
    kind: "field",
    detail: `${field.valueType} field`,
  }))
  const fieldTypes = new Map(
    fields.map((field) => [field.value, field.valueType]),
  )
  const beforeCursor = text.slice(0, cursor)
  const fragment = guardFragment(beforeCursor)
  const contextText = beforeCursor.slice(
    0,
    beforeCursor.length - fragment.query.length,
  )
  const tokens = guardAutocompleteTokens(contextText, fieldTypes)
  const candidates = guardContextSuggestions(
    tokens,
    fieldSuggestions,
    fieldTypes,
  )
  return fragment.query
    ? matchingSuggestions(candidates, fragment.query)
    : candidates.slice(0, 40)
}

function guardAutocompleteTokens(
  text: string,
  fieldTypes: Map<string, GuardValueType>,
): GuardAutocompleteToken[] {
  const pattern =
    /"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|==|!=|<=|>=|=|<|>|[a-z_][a-z0-9_.-]*|[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?|[()]/gi
  return [...text.matchAll(pattern)].map((match) => {
    const value = match[0]
    const lower = value.toLowerCase()
    if (lower === "and" || lower === "or" || lower === "not") {
      return { kind: lower }
    }
    if (/^(?:==|!=|<=|>=|=|<|>)$/.test(value)) {
      return { kind: "comparison" }
    }
    if (value === "(") return { kind: "left-parenthesis" }
    if (value === ")") return { kind: "right-parenthesis" }
    if (lower === "true" || lower === "false") {
      return { kind: "operand", valueType: "boolean" }
    }
    if (value.startsWith('"') || value.startsWith("'")) {
      return { kind: "operand", valueType: "string" }
    }
    if (/^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(value)) {
      return { kind: "operand", valueType: "number" }
    }
    return { kind: "operand", valueType: fieldTypes.get(lower) }
  })
}

function guardContextSuggestions(
  tokens: GuardAutocompleteToken[],
  fields: GuardSuggestion[],
  fieldTypes: Map<string, GuardValueType>,
): GuardSuggestion[] {
  const last = tokens.at(-1)
  if (
    !last ||
    last.kind === "and" ||
    last.kind === "left-parenthesis" ||
    last.kind === "not" ||
    last.kind === "or"
  ) {
    return [
      ...fields,
      keywordSuggestions[2],
      openingParenthesisSuggestion,
      ...booleanLiteralSuggestions,
      ...numberLiteralSuggestions,
      ...stringLiteralSuggestions,
    ]
  }

  const canClose =
    tokens.filter((token) => token.kind === "left-parenthesis").length >
    tokens.filter((token) => token.kind === "right-parenthesis").length
  const completionSuggestions = canClose
    ? [...logicalSuggestions, closingParenthesisSuggestion]
    : logicalSuggestions
  if (last.kind === "right-parenthesis") return completionSuggestions

  if (last.kind === "comparison") {
    const leftType = tokens.at(-2)?.valueType
    return guardOperandSuggestions(leftType, fields, fieldTypes)
  }

  if (last.kind === "operand") {
    if (tokens.at(-2)?.kind === "comparison") return completionSuggestions
    if (last.valueType === "boolean") {
      return [...completionSuggestions, ...booleanComparisonSuggestions]
    }
    return comparisonSuggestions
  }

  return []
}

function guardOperandSuggestions(
  valueType: GuardValueType | undefined,
  fields: GuardSuggestion[],
  fieldTypes: Map<string, GuardValueType>,
): GuardSuggestion[] {
  const matchingFields = valueType
    ? fields.filter((field) => fieldTypes.get(field.value) === valueType)
    : fields
  switch (valueType) {
    case "boolean":
      return [...matchingFields, ...booleanLiteralSuggestions]
    case "string":
      return stringLiteralSuggestions
    default:
      return [...matchingFields, ...numberLiteralSuggestions]
  }
}

function guardFragment(text: string): {
  kind: "identifier" | "operator"
  query: string
} {
  const operator = text.match(/[<>=!]+$/)?.[0]
  if (operator) return { kind: "operator", query: operator }
  return {
    kind: "identifier",
    query: text.match(/[a-z0-9_.-]+$/i)?.[0] ?? "",
  }
}

function matchingSuggestions(
  candidates: GuardSuggestion[],
  query: string,
): GuardSuggestion[] {
  const normalized = query.toLowerCase()
  return candidates
    .map((suggestion, index) => ({
      suggestion,
      index,
      position: suggestion.value.toLowerCase().indexOf(normalized),
    }))
    .filter((match) => match.position >= 0)
    .sort((left, right) =>
      left.position !== right.position
        ? left.position - right.position
        : left.index - right.index,
    )
    .slice(0, 40)
    .map((match) => match.suggestion)
}

function insertGuardSuggestion(
  text: string,
  suggestion: string,
  selectionStart: number,
  selectionEnd: number,
): { value: string; cursor: number } {
  const range = guardReplacementRange(text, selectionStart, selectionEnd)
  const before = text.slice(0, range.start)
  const after = text.slice(range.end)
  const needsLeadingSpace =
    before !== "" && !/[\s(]$/.test(before) && !/^[),]/.test(suggestion)
  const needsTrailingSpace = !after || !/^[\s),]/.test(after)
  const inserted = `${needsLeadingSpace ? " " : ""}${suggestion}${
    needsTrailingSpace ? " " : ""
  }`
  return {
    value: `${before}${inserted}${after}`,
    cursor: before.length + inserted.length,
  }
}

function guardReplacementRange(
  text: string,
  selectionStart: number,
  selectionEnd: number,
): { start: number; end: number } {
  const classify = (character: string | undefined) => {
    if (character && /[<>=!]/.test(character)) return "operator"
    if (character && /[a-z0-9_.-]/i.test(character)) return "identifier"
    return ""
  }
  const selection = text.slice(selectionStart, selectionEnd)
  const selectedKind =
    selection && /^[<>=!]+$/.test(selection)
      ? "operator"
      : selection && /^[a-z0-9_.-]+$/i.test(selection)
        ? "identifier"
        : ""
  if (selection && !selectedKind) {
    return { start: selectionStart, end: selectionEnd }
  }
  const kind =
    selectedKind ||
    classify(text[selectionStart - 1]) ||
    classify(text[selectionStart])
  if (!kind) return { start: selectionStart, end: selectionEnd }
  const matches = (character: string | undefined) =>
    kind === "operator"
      ? Boolean(character && /[<>=!]/.test(character))
      : Boolean(character && /[a-z0-9_.-]/i.test(character))
  let start = selectionStart
  let end = selectionEnd
  while (start > 0 && matches(text[start - 1])) start--
  while (end < text.length && matches(text[end])) end++
  return { start, end }
}
