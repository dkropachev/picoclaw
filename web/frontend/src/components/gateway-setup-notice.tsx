import { IconAlertTriangle } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import { useAtomValue } from "jotai"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { gatewayAtom } from "@/store/gateway"

export function GatewaySetupNotice() {
  const { t } = useTranslation()
  const gateway = useAtomValue(gatewayAtom)
  const reason = gateway.modelSetupReason?.trim()

  if (!gateway.modelSetupRequired || !reason) {
    return null
  }

  return (
    <div
      className="border-border text-foreground flex shrink-0 items-center gap-3 border-y bg-amber-500/10 px-3 py-2 text-sm md:px-4"
      role="status"
    >
      <IconAlertTriangle
        className="size-4 shrink-0 text-amber-700 dark:text-amber-300"
        aria-hidden="true"
      />
      <p className="min-w-0 flex-1">
        <span className="font-medium">
          {t("header.gateway.modelSetupRequired", "Model setup required")}
        </span>
        <span className="text-muted-foreground">
          {t("header.gateway.setupReason", ": {{reason}}.", { reason })}
        </span>
      </p>
      <Button asChild size="sm" variant="outline">
        <Link to="/models/aliases" search={{ q: "ORDER BY name ASC" }}>
          {t("header.gateway.configureModels", "Configure models")}
        </Link>
      </Button>
    </div>
  )
}
