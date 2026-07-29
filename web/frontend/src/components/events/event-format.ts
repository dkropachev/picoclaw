export function formatEventDate(value?: string): string {
  if (!value) {
    return "-"
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date)
}

export function formatEventBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) {
    return "-"
  }
  if (value < 1024) {
    return `${value} B`
  }
  const units = ["KB", "MB", "GB"]
  let amount = value / 1024
  let unit = units[0]
  for (let index = 1; index < units.length && amount >= 1024; index += 1) {
    amount /= 1024
    unit = units[index]
  }
  return `${new Intl.NumberFormat(undefined, {
    maximumFractionDigits: amount >= 10 ? 1 : 2,
  }).format(amount)} ${unit}`
}

export function eventErrorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message.trim() !== ""
    ? error.message
    : fallback
}
