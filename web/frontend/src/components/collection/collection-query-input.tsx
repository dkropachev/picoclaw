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
  type CollectionQuerySchema,
  collectionQueryByteLength,
  collectionUTF8BytePositionToUTF16Offset,
  maximumCollectionQueryBytes,
  truncateCollectionQuery,
} from "@/api/collection"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import {
  type CollectionQuerySelection,
  type CollectionQuerySuggestion,
  applyCollectionQuerySuggestionForSelection,
  getCollectionQuerySuggestionsForSelection,
  normalizeCollectionQuerySelection,
} from "./collection-query-editor"

/* eslint-disable react-refresh/only-export-components -- Keep the established helper exports available from the shared component module. */

export {
  applyCollectionQuerySuggestion,
  getCollectionQuerySuggestions,
} from "./collection-query-editor"
export type {
  CollectionQuerySelection,
  CollectionQuerySuggestion,
  CollectionQuerySuggestionKind,
} from "./collection-query-editor"

export interface CollectionQueryInputError {
  message: string
  position?: number
}

interface DOMSelection extends CollectionQuerySelection {
  direction: "backward" | "forward" | "none"
}

interface PendingDOMSelection extends DOMSelection {
  focus: boolean
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
  const optionRefs = useRef<Array<HTMLButtonElement | null>>([])
  const pendingSelectionRef = useRef<PendingDOMSelection | null>(null)
  const composingRef = useRef(false)
  const listboxID = useId()
  const errorID = useId()
  const helpID = useId()
  const countID = useId()
  const [draft, setDraft] = useState(boundedActiveQuery)
  const [selection, setSelection] = useState<DOMSelection>({
    start: boundedActiveQuery.length,
    end: boundedActiveQuery.length,
    direction: "none",
  })
  const [selectionRevision, setSelectionRevision] = useState(0)
  const [focused, setFocused] = useState(false)
  const [composing, setComposing] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const [activeIndex, setActiveIndex] = useState(-1)
  const [errorSnapshot, setErrorSnapshot] = useState<{
    error: CollectionQueryInputError
    query: string
  } | null>(() => (error ? { error, query: boundedActiveQuery } : null))
  const [previousError, setPreviousError] = useState(error)

  if (error !== previousError) {
    setPreviousError(error)
    setErrorSnapshot(error ? { error, query: boundedActiveQuery } : null)
  }

  const suggestions = useMemo(
    () => getCollectionQuerySuggestionsForSelection(draft, selection, schema),
    [draft, schema, selection],
  )
  const open =
    !disabled && !composing && focused && !dismissed && suggestions.length > 0
  const activeSuggestion =
    activeIndex >= 0 ? suggestions[activeIndex] : undefined
  const suggestionIdentity = suggestions
    .map(
      (suggestion) =>
        `${suggestion.id}:${suggestion.replaceStart}:${suggestion.replaceEnd}:${suggestion.insertText}`,
    )
    .join("\u0000")
  const visibleError =
    error &&
    errorSnapshot?.error === error &&
    errorSnapshot.query === boundedActiveQuery &&
    draft === errorSnapshot.query
      ? error
      : undefined
  const validErrorPosition =
    typeof visibleError?.position === "number" &&
    Number.isSafeInteger(visibleError.position) &&
    visibleError.position >= 0 &&
    visibleError.position <= collectionQueryByteLength(draft)
      ? visibleError.position
      : undefined
  const errorOffset =
    validErrorPosition == null
      ? undefined
      : collectionUTF8BytePositionToUTF16Offset(draft, validErrorPosition)
  const errorCharacter =
    errorOffset == null
      ? undefined
      : Array.from(draft.slice(0, errorOffset)).length + 1

  useEffect(() => {
    const end = boundedActiveQuery.length
    setDraft(boundedActiveQuery)
    setSelection({ start: end, end, direction: "none" })
    setDismissed(true)
    setActiveIndex(-1)
    pendingSelectionRef.current = {
      start: end,
      end,
      direction: "none",
      focus: inputRef.current === document.activeElement,
    }
    setSelectionRevision((revision) => revision + 1)
  }, [boundedActiveQuery])

  useLayoutEffect(() => {
    const pending = pendingSelectionRef.current
    if (!pending) return
    pendingSelectionRef.current = null
    const input = inputRef.current
    if (!input) return
    if (pending.focus) input.focus()
    input.setSelectionRange(pending.start, pending.end, pending.direction)
    setSelection({
      start: pending.start,
      end: pending.end,
      direction: pending.direction,
    })
  }, [draft, selectionRevision])

  useLayoutEffect(() => {
    if (!focused || errorOffset == null) return
    const input = inputRef.current
    if (!input || input !== document.activeElement) return
    const scalarLength = Array.from(draft.slice(errorOffset))[0]?.length ?? 0
    const end = Math.min(draft.length, errorOffset + scalarLength)
    input.setSelectionRange(errorOffset, end)
    setSelection({ start: errorOffset, end, direction: "none" })
  }, [draft, errorOffset, focused])

  useLayoutEffect(() => {
    if (!open || activeIndex < 0) return
    optionRefs.current[activeIndex]?.scrollIntoView?.({ block: "nearest" })
  }, [activeIndex, open])

  useEffect(() => setActiveIndex(-1), [suggestionIdentity])

  const updateSelection = (target: HTMLInputElement) => {
    const normalized = normalizeCollectionQuerySelection(target.value, {
      start: target.selectionStart ?? target.value.length,
      end: target.selectionEnd ?? target.value.length,
    })
    const direction = target.selectionDirection ?? "none"
    const changed =
      normalized.start !== selection.start ||
      normalized.end !== selection.end ||
      direction !== selection.direction
    setSelection({
      ...normalized,
      direction,
    })
    if (changed) setActiveIndex(-1)
  }

  const restoreActiveQuery = () => {
    const end = boundedActiveQuery.length
    setDraft(boundedActiveQuery)
    setSelection({ start: end, end, direction: "none" })
    setDismissed(true)
    setActiveIndex(-1)
    pendingSelectionRef.current = {
      start: end,
      end,
      direction: "none",
      focus: true,
    }
    setSelectionRevision((revision) => revision + 1)
  }

  const applySuggestion = (suggestion: CollectionQuerySuggestion) => {
    const result = applyCollectionQuerySuggestionForSelection(
      draft,
      suggestion,
      selection,
    )
    const direction = result.applied ? "none" : selection.direction
    setDraft(result.value)
    setSelection({
      start: result.selectionStart,
      end: result.selectionEnd,
      direction,
    })
    setDismissed(!result.applied)
    setActiveIndex(-1)
    pendingSelectionRef.current = {
      start: result.selectionStart,
      end: result.selectionEnd,
      direction,
      focus: true,
    }
    setSelectionRevision((revision) => revision + 1)
  }

  const applyDraft = () => {
    if (disabled || composingRef.current) return
    const normalized =
      truncateCollectionQuery(draft.trim()) || boundedDefaultQuery
    const end = normalized.length
    setDraft(normalized)
    setSelection({ start: end, end, direction: "none" })
    setDismissed(true)
    setActiveIndex(-1)
    pendingSelectionRef.current = {
      start: end,
      end,
      direction: "none",
      focus: true,
    }
    setSelectionRevision((revision) => revision + 1)
    onApply(normalized)
  }

  const submit = (event: FormEvent) => {
    event.preventDefault()
    applyDraft()
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (
      composingRef.current ||
      event.nativeEvent.isComposing ||
      event.nativeEvent.keyCode === 229
    ) {
      return
    }
    if (event.key === "Escape") {
      event.preventDefault()
      restoreActiveQuery()
      return
    }
    if (event.key === " " && (event.ctrlKey || event.metaKey)) {
      if (suggestions.length > 0) {
        event.preventDefault()
        setDismissed(false)
        setActiveIndex(0)
      }
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

  const describedBy = [visibleError ? errorID : helpID, countID].join(" ")

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
          aria-invalid={Boolean(visibleError)}
          aria-errormessage={visibleError ? errorID : undefined}
          aria-describedby={describedBy}
          className="pr-10 pl-9 font-mono text-xs"
          placeholder={placeholder}
          onChange={(event) => {
            const rawDraft = event.target.value
            const nextDraft = truncateCollectionQuery(rawDraft)
            const normalized = normalizeCollectionQuerySelection(nextDraft, {
              start: Math.min(
                event.target.selectionStart ?? nextDraft.length,
                nextDraft.length,
              ),
              end: Math.min(
                event.target.selectionEnd ?? nextDraft.length,
                nextDraft.length,
              ),
            })
            setDraft(nextDraft)
            setSelection({
              ...normalized,
              direction: event.target.selectionDirection ?? "none",
            })
            if (nextDraft !== rawDraft) {
              pendingSelectionRef.current = {
                ...normalized,
                direction: event.target.selectionDirection ?? "none",
                focus: true,
              }
              setSelectionRevision((revision) => revision + 1)
            }
            setDismissed(false)
            setActiveIndex(-1)
          }}
          onCompositionStart={() => {
            composingRef.current = true
            setComposing(true)
            setActiveIndex(-1)
          }}
          onCompositionEnd={(event) => {
            composingRef.current = false
            setComposing(false)
            setDismissed(false)
            updateSelection(event.currentTarget)
          }}
          onFocus={(event) => {
            setFocused(true)
            setDismissed(false)
            updateSelection(event.currentTarget)
          }}
          onBlur={() => {
            setFocused(false)
            setActiveIndex(-1)
          }}
          onClick={(event) => {
            updateSelection(event.currentTarget)
            setDismissed(false)
          }}
          onSelect={(event) => updateSelection(event.currentTarget)}
          onKeyUp={(event) => {
            if (
              !composingRef.current &&
              event.key !== "ArrowDown" &&
              event.key !== "ArrowUp"
            ) {
              updateSelection(event.currentTarget)
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
            const end = boundedDefaultQuery.length
            setDraft(boundedDefaultQuery)
            setSelection({ start: end, end, direction: "none" })
            setDismissed(true)
            setActiveIndex(-1)
            pendingSelectionRef.current = {
              start: end,
              end,
              direction: "none",
              focus: true,
            }
            setSelectionRevision((revision) => revision + 1)
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
                ref={(element) => {
                  optionRefs.current[index] = element
                }}
                key={suggestion.id}
                id={`${listboxID}-option-${index}`}
                type="button"
                role="option"
                aria-label={`${suggestion.label}, ${suggestion.detail}`}
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
        {visibleError ? (
          <p id={errorID} role="alert" className="text-destructive">
            {errorCharacter == null ? "" : `Character ${errorCharacter}: `}
            {visibleError.message}
          </p>
        ) : (
          <span id={helpID} className="text-muted-foreground">
            Enter applies · Escape restores active query
          </span>
        )}
        <span
          id={countID}
          className="text-muted-foreground ml-auto shrink-0 font-mono tabular-nums"
        >
          {collectionQueryByteLength(draft)}/{maximumCollectionQueryBytes}
        </span>
      </div>
    </form>
  )
}
