const maximumOAuthFlowMessageCharacters = 512

export function safeOAuthFlowMessage(
  value: string | undefined,
  fallback: string,
): string {
  let printable = ""
  for (const character of value ?? "") {
    const codePoint = character.codePointAt(0) ?? 0
    printable += codePoint <= 31 || codePoint === 127 ? " " : character
  }
  const normalized = printable.replace(/\s+/g, " ").trim()
  if (!normalized) return fallback
  return normalized.slice(0, maximumOAuthFlowMessageCharacters)
}
