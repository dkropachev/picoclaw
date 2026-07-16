import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

const PROVIDER_DEFAULT_VALUE = "__provider_default__"

const REASONING_EFFORT_OPTIONS = [
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
]

interface ReasoningEffortSelectProps {
  providerDefaultLabel: string
  value: string
  onChange: (value: string) => void
}

export function ReasoningEffortSelect({
  providerDefaultLabel,
  value,
  onChange,
}: ReasoningEffortSelectProps) {
  return (
    <Select
      value={value || PROVIDER_DEFAULT_VALUE}
      onValueChange={(nextValue) =>
        onChange(nextValue === PROVIDER_DEFAULT_VALUE ? "" : nextValue)
      }
    >
      <SelectTrigger>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={PROVIDER_DEFAULT_VALUE}>
          {providerDefaultLabel}
        </SelectItem>
        {REASONING_EFFORT_OPTIONS.map((option) => (
          <SelectItem key={option} value={option}>
            {option}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
