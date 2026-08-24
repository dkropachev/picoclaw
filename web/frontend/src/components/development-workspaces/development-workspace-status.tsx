import type {
  DevelopmentWorkspaceExecutionState,
  DevelopmentWorkspaceIntent,
  DevelopmentWorkspacePhase,
} from "@/api/development-workspaces"
import { humanize } from "@/components/development-workspaces/development-workspace-labels"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

export function DevelopmentIntentBadge({
  intent,
}: {
  intent: DevelopmentWorkspaceIntent
}) {
  return (
    <Badge variant="outline">
      {intent === "implement_feature" ? "Feature" : "PR pickup"}
    </Badge>
  )
}

export function DevelopmentPhaseBadge({
  phase,
}: {
  phase: DevelopmentWorkspacePhase
}) {
  return <Badge variant="secondary">{humanize(phase)}</Badge>
}

export function DevelopmentStateBadge({
  state,
}: {
  state: DevelopmentWorkspaceExecutionState
}) {
  return (
    <Badge
      variant={
        state === "failed" || state === "blocked"
          ? "destructive"
          : state === "waiting_user"
            ? "default"
            : "outline"
      }
      className={cn(state === "running" && "animate-pulse")}
    >
      {humanize(state)}
    </Badge>
  )
}
