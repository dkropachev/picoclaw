import type { HistoryState, RouterHistory } from "@tanstack/react-router"

export interface PRNavigationState extends HistoryState {
  key?: string
  __TSR_key?: string
  __TSR_index: number
  prParent?: "portfolio" | "workspace" | "profiles" | "settings"
  prParentIndex?: number
  prParentKey?: string
  prOverlay?: "gate" | "discard"
  prWorkIndex?: number
  prWorkKey?: string
}

export function asPRNavigationState(state: HistoryState): PRNavigationState {
  return state as PRNavigationState
}

export function updatePRNavigationState(
  state: HistoryState,
  update: (current: PRNavigationState) => Partial<PRNavigationState>,
): HistoryState {
  return { ...state, ...update(asPRNavigationState(state)) }
}

export function replaceBrowserPRHistoryEntry(
  history: RouterHistory,
  href: string,
  state: unknown,
): boolean {
  if (typeof window === "undefined") return false
  const ignored = history._ignoreSubscribers
  history._ignoreSubscribers = true
  try {
    window.history.replaceState(state, "", href)
  } finally {
    history._ignoreSubscribers = ignored
  }
  return true
}

export function synchronizeBrowserPRHistoryEntry(
  href: string,
  state: unknown,
): boolean {
  if (typeof window === "undefined") return false
  window.history.replaceState(state, "", href)
  return true
}

export function walkToPRHistoryEntry(
  history: RouterHistory,
  current: PRNavigationState,
  targetIndex: number,
  targetKey: string,
  fallback?: () => void,
  onReached?: () => void,
): boolean {
  if (targetIndex < 0 || targetIndex === current.__TSR_index) {
    const reachedTarget =
      targetIndex === current.__TSR_index && current.__TSR_key === targetKey
    if (reachedTarget) onReached?.()
    else fallback?.()
    return reachedTarget
  }
  history.flush()
  const browserHistory =
    typeof window !== "undefined" &&
    window.history.state?.__TSR_index === current.__TSR_index
  if (!browserHistory) {
    history.go(targetIndex - current.__TSR_index, { ignoreBlocker: true })
    const reachedTarget =
      history.location.state.__TSR_index === targetIndex &&
      history.location.state.__TSR_key === targetKey
    if (reachedTarget) onReached?.()
    else fallback?.()
    return true
  }

  let timer: ReturnType<typeof window.setTimeout> | undefined
  let settled = false
  const finish = (reachedTarget: boolean) => {
    if (settled) return
    settled = true
    unsubscribe()
    if (timer) window.clearTimeout(timer)
    if (reachedTarget) onReached?.()
    else fallback?.()
  }
  const step = (index: number) => {
    if (timer) window.clearTimeout(timer)
    timer = window.setTimeout(() => finish(false), 1500)
    if (index > targetIndex) {
      history.back({ ignoreBlocker: true })
    } else {
      history.forward({ ignoreBlocker: true })
    }
  }
  const unsubscribe = history.subscribe(({ action, location }) => {
    if (action.type === "PUSH" || action.type === "REPLACE") return
    if (timer) window.clearTimeout(timer)
    if (location.state.__TSR_index === targetIndex) {
      finish(location.state.__TSR_key === targetKey)
      return
    }
    const stayedInRange =
      current.__TSR_index < targetIndex
        ? location.state.__TSR_index < targetIndex
        : location.state.__TSR_index > targetIndex
    if (stayedInRange) {
      step(location.state.__TSR_index)
      return
    }
    finish(false)
  })
  step(current.__TSR_index)
  return true
}

export function goToMarkedPRHistory(
  history: RouterHistory,
  current: PRNavigationState,
  targetIndex: number | undefined,
  targetKey: string | undefined,
  fallback?: () => void,
): boolean {
  if (
    typeof targetIndex !== "number" ||
    !targetKey ||
    targetIndex < 0 ||
    targetIndex >= current.__TSR_index
  ) {
    return false
  }
  return walkToPRHistoryEntry(
    history,
    current,
    targetIndex,
    targetKey,
    fallback,
  )
}
