import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"
import { Toaster } from "sonner"

import { AppHeader } from "@/components/app-header"
import { AppSidebar } from "@/components/app-sidebar"
import { GatewaySetupNotice } from "@/components/gateway-setup-notice"
import { TourGuide } from "@/components/tour/tour-guide"
import { SidebarProvider } from "@/components/ui/sidebar"
import { TooltipProvider } from "@/components/ui/tooltip"

export function AppLayout({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  return (
    <TooltipProvider>
      <SidebarProvider className="bg-background flex h-dvh flex-col overflow-hidden">
        <a
          href="#main-content"
          className="bg-background text-foreground focus:ring-ring sr-only z-[100] rounded-md px-3 py-2 text-sm font-medium focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:ring-2 focus:outline-none"
        >
          {t("common.skipToContent")}
        </a>
        <AppHeader />

        <div className="flex flex-1 overflow-hidden">
          <AppSidebar />
          <div className="flex w-full flex-col overflow-hidden">
            <GatewaySetupNotice />
            <main
              id="main-content"
              tabIndex={-1}
              className="flex min-h-0 w-full max-w-full flex-1 flex-col overflow-hidden"
            >
              {children}
            </main>
          </div>
        </div>
        <Toaster position="bottom-center" />
        <TourGuide />
      </SidebarProvider>
    </TooltipProvider>
  )
}
