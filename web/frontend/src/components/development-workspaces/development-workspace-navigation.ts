export type DevelopmentAttentionPanel =
  | "overview"
  | "charter"
  | "scope"
  | "publication"
  | "chat"

const attentionPanels = new Set<DevelopmentAttentionPanel>([
  "overview",
  "charter",
  "scope",
  "publication",
  "chat",
])

export function isDevelopmentAttentionPanel(
  value: unknown,
): value is DevelopmentAttentionPanel {
  return (
    typeof value === "string" &&
    attentionPanels.has(value as DevelopmentAttentionPanel)
  )
}

export function asDevelopmentAttentionPanel(
  value: string,
): DevelopmentAttentionPanel {
  return isDevelopmentAttentionPanel(value) ? value : "overview"
}
