import {
  IconArrowDown,
  IconArrowUp,
  IconPlus,
  IconX,
} from "@tabler/icons-react"
import { type KeyboardEvent, useId } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function AgentTokenListField({
  label,
  description,
  values,
  input,
  suggestions = [],
  placeholder,
  error,
  disabled,
  onChange,
  onInputChange,
}: {
  label: string
  description?: string
  values: string[]
  input: string
  suggestions?: string[]
  placeholder?: string
  error?: string
  disabled?: boolean
  onChange: (values: string[]) => void
  onInputChange: (value: string) => void
}) {
  const inputID = useId()
  const listID = useId()

  const add = () => {
    const value = input.trim()
    if (value === "" || values.includes(value)) return
    onChange([...values, value])
    onInputChange("")
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "Enter" || event.key === ",") {
      event.preventDefault()
      add()
    }
  }

  const move = (index: number, offset: -1 | 1) => {
    const target = index + offset
    if (target < 0 || target >= values.length) return
    const next = [...values]
    ;[next[index], next[target]] = [next[target], next[index]]
    onChange(next)
  }

  return (
    <div className="space-y-2">
      <div className="space-y-1">
        <Label htmlFor={inputID}>{label}</Label>
        {description && (
          <p className="text-muted-foreground text-xs">{description}</p>
        )}
      </div>
      <div className="flex gap-2">
        <Input
          id={inputID}
          value={input}
          list={suggestions.length > 0 ? listID : undefined}
          placeholder={placeholder}
          disabled={disabled}
          aria-invalid={Boolean(error)}
          onChange={(event) => onInputChange(event.target.value)}
          onKeyDown={handleKeyDown}
        />
        {suggestions.length > 0 && (
          <datalist id={listID}>
            {suggestions
              .filter((suggestion) => !values.includes(suggestion))
              .map((suggestion) => (
                <option key={suggestion} value={suggestion} />
              ))}
          </datalist>
        )}
        <Button
          type="button"
          variant="outline"
          size="icon"
          disabled={disabled || input.trim() === ""}
          onClick={add}
          aria-label={`Add ${label.toLocaleLowerCase()} entry`}
        >
          <IconPlus className="size-4" />
        </Button>
      </div>
      {values.length > 0 && (
        <ol className="space-y-1.5" aria-label={`${label} order`}>
          {values.map((value, index) => (
            <li
              key={`${value}-${index}`}
              className="border-border bg-muted/30 flex min-w-0 items-center gap-1 rounded-md border px-2 py-1"
            >
              <span className="min-w-0 flex-1 truncate font-mono text-xs">
                {value}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                disabled={disabled || index === 0}
                onClick={() => move(index, -1)}
                aria-label={`Move ${value} up`}
              >
                <IconArrowUp />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                disabled={disabled || index === values.length - 1}
                onClick={() => move(index, 1)}
                aria-label={`Move ${value} down`}
              >
                <IconArrowDown />
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                disabled={disabled}
                onClick={() =>
                  onChange(
                    values.filter((_, valueIndex) => valueIndex !== index),
                  )
                }
                aria-label={`Remove ${value}`}
              >
                <IconX />
              </Button>
            </li>
          ))}
        </ol>
      )}
      {error && <p className="text-destructive text-xs">{error}</p>}
    </div>
  )
}
