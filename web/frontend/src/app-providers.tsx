import { type ReactNode, useEffect } from "react"

import { useHighlightTheme } from "./hooks/use-highlight-theme"
import { registerPicoClawServiceWorker } from "./lib/pwa-notifications"

interface AppProvidersProps {
  children: ReactNode
}

export function AppProviders({ children }: AppProvidersProps) {
  useHighlightTheme()

  useEffect(() => {
    if (!import.meta.env.PROD) return
    void registerPicoClawServiceWorker()
  }, [])

  return <>{children}</>
}
