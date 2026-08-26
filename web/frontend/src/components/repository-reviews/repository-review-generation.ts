export function createRepositoryReviewGenerationID(): string {
  const random = globalThis.crypto?.randomUUID?.().replaceAll("-", "")
  return `rig_${random || `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`}`
}
