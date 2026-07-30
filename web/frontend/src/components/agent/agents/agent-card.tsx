import { IconEdit, IconLoader2, IconStar, IconTrash } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { AgentInfo } from "@/api/agents"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

export function AgentCard({
  agent,
  settingDefault,
  onEdit,
  onSetDefault,
  onDelete,
}: {
  agent: AgentInfo
  settingDefault: boolean
  onEdit: () => void
  onSetDefault: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const defaultIsPinned = agent.is_default && agent.default_configured
  const effectiveDefaultCannotBePinned = agent.is_default && agent.implicit
  const fallbacks =
    agent.model?.fallbacks == null
      ? t("pages.agent.agents.policy.inherit", "Inherit")
      : agent.model.fallbacks.length === 0
        ? t("pages.agent.agents.policy.none", "None")
        : agent.model.fallbacks.join(" → ")
  const skills =
    agent.skills == null || agent.skills.length === 0
      ? t("pages.agent.agents.policy.all_skills", "All skills")
      : agent.skills.join(", ")
  const delegationTargets = agent.subagents?.allow_agents
  const delegation =
    delegationTargets == null || delegationTargets.length === 0
      ? t("pages.agent.agents.policy.no_delegation", "No delegation")
      : delegationTargets.length === 1 && delegationTargets[0] === "*"
        ? t("pages.agent.agents.policy.all_peers", "All peers")
        : delegationTargets.join(", ")

  return (
    <Card
      size="sm"
      className="border-border/60 bg-card/60 hover:bg-card gap-3 transition-colors"
      data-agent-id={agent.id}
    >
      <CardHeader className="gap-2">
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0 space-y-1.5">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <CardTitle className="truncate">
                {agent.name || agent.id}
              </CardTitle>
              {agent.is_default && (
                <Badge variant="secondary" className="gap-1">
                  <IconStar className="size-3" />
                  {t("pages.agent.agents.badge.default", "Default")}
                </Badge>
              )}
              {agent.implicit && (
                <Badge variant="outline">
                  {t("pages.agent.agents.badge.implicit", "Implicit")}
                </Badge>
              )}
            </div>
            <p className="text-muted-foreground truncate font-mono text-xs">
              {agent.id}
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={onEdit}
              aria-label={t("pages.agent.agents.action.edit", {
                defaultValue: "Edit {{name}}",
                name: agent.id,
              })}
            >
              <IconEdit className="size-4" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              disabled={agent.implicit}
              onClick={onDelete}
              aria-label={t("pages.agent.agents.action.delete", {
                defaultValue: "Delete {{name}}",
                name: agent.id,
              })}
              title={
                agent.implicit
                  ? t(
                      "pages.agent.agents.action.implicit_delete",
                      "The implicit main agent is restored automatically and cannot be deleted.",
                    )
                  : undefined
              }
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        </div>
      </CardHeader>

      <CardContent>
        <p className="mb-3 text-xs font-medium">
          {t("pages.agent.agents.configured_policy", "Configured policy")}
        </p>
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
          <dt className="text-muted-foreground">
            {t("pages.agent.agents.summary.workspace", "Workspace")}
          </dt>
          <dd className="truncate text-right" title={agent.workspace}>
            {agent.workspace ||
              t("pages.agent.agents.policy.inherit", "Inherit")}
          </dd>
          <dt className="text-muted-foreground">
            {t("pages.agent.agents.summary.primary", "Primary")}
          </dt>
          <dd className="truncate text-right" title={agent.model?.primary}>
            {agent.model?.primary ||
              t("pages.agent.agents.policy.inherit", "Inherit")}
          </dd>
          <dt className="text-muted-foreground">
            {t("pages.agent.agents.summary.fallbacks", "Fallbacks")}
          </dt>
          <dd className="truncate text-right" title={fallbacks}>
            {fallbacks}
          </dd>
          <dt className="text-muted-foreground">
            {t("pages.agent.agents.summary.skills", "Skills")}
          </dt>
          <dd className="truncate text-right" title={skills}>
            {skills}
          </dd>
          <dt className="text-muted-foreground">
            {t("pages.agent.agents.summary.delegation", "Delegation")}
          </dt>
          <dd className="truncate text-right" title={delegation}>
            {delegation}
          </dd>
        </dl>
      </CardContent>

      <CardFooter className="border-border/60 justify-end border-t">
        <Button
          type="button"
          variant={agent.is_default ? "secondary" : "outline"}
          size="sm"
          disabled={
            defaultIsPinned || effectiveDefaultCannotBePinned || settingDefault
          }
          onClick={onSetDefault}
        >
          {settingDefault ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconStar className="size-4" />
          )}
          {defaultIsPinned || effectiveDefaultCannotBePinned
            ? t("pages.agent.agents.action.current_default", "Current default")
            : agent.is_default
              ? t("pages.agent.agents.action.pin_default", "Pin as default")
              : t("pages.agent.agents.action.set_default", "Set default")}
        </Button>
      </CardFooter>
    </Card>
  )
}
