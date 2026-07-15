const claimedThreadSeedKeys = new Set<string>()

function threadSeedKey(sessionId: string, content: string) {
  return `${sessionId}\u0000${content.trim()}`
}

export function claimThreadSeed(sessionId: string, content: string) {
  const normalizedContent = content.trim()
  if (!normalizedContent) {
    return false
  }
  const key = threadSeedKey(sessionId, normalizedContent)
  if (claimedThreadSeedKeys.has(key)) {
    return false
  }
  claimedThreadSeedKeys.add(key)
  return true
}

export function releaseThreadSeed(sessionId: string, content: string) {
  const normalizedContent = content.trim()
  if (!normalizedContent) {
    return
  }
  claimedThreadSeedKeys.delete(threadSeedKey(sessionId, normalizedContent))
}

export function resetThreadCardAutoSeedForTests() {
  claimedThreadSeedKeys.clear()
}
