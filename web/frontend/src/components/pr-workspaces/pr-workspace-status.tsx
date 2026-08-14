import {
  IconAlertTriangle,
  IconCircleCheck,
  IconClock,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type {
  PRWorkspaceChangeSize,
  PRWorkspaceExecutionState,
  PRWorkspacePhase,
  PRWorkspaceScopeDistance,
  PRWorkspaceType,
} from "@/api/pr-workspaces"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

export function PhaseBadge({ phase }: { phase: PRWorkspacePhase }) {
  const { t } = useTranslation()
  return (
    <Badge variant="secondary">
      {t(`prWorkspaces.phases.${phase}`, { defaultValue: phase })}
    </Badge>
  )
}

export function StateBadge({ state }: { state: PRWorkspaceExecutionState }) {
  const { t } = useTranslation()
  const terminal = state === "succeeded"
  const attention =
    state === "failed" || state === "blocked" || state === "unknown"
  const Icon = terminal
    ? IconCircleCheck
    : attention
      ? IconAlertTriangle
      : IconClock
  return (
    <Badge
      variant={attention ? "destructive" : terminal ? "secondary" : "outline"}
    >
      <Icon data-icon="inline-start" />
      {t(`prWorkspaces.states.${state}`, { defaultValue: state })}
    </Badge>
  )
}

export function TypeBadge({ type }: { type: PRWorkspaceType }) {
  const { t } = useTranslation()
  return (
    <Badge variant="outline">
      {t(`prWorkspaces.types.${type}`, { defaultValue: type })}
    </Badge>
  )
}

export function ScopeBadge({
  distance,
  size,
  typeCompatible,
}: {
  distance: PRWorkspaceScopeDistance
  size: PRWorkspaceChangeSize
  typeCompatible: boolean
}) {
  const { t } = useTranslation()
  return (
    <span className="flex flex-wrap items-center gap-1">
      <Badge
        variant={distance === "S0_exact" ? "secondary" : "outline"}
        className={cn(!typeCompatible && "border-destructive text-destructive")}
      >
        {t(`prWorkspaces.scope.${distance}`, { defaultValue: distance })}
      </Badge>
      <Badge variant="outline">{size}</Badge>
      {!typeCompatible && (
        <Badge variant="destructive">
          {t("prWorkspaces.findings.typeMismatch")}
        </Badge>
      )}
    </span>
  )
}
