import {
  IconAlertTriangle,
  IconCheck,
  IconEdit,
  IconKey,
  IconLoader2,
  IconLogin2,
  IconPlugConnected,
  IconTrash,
} from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { MCPProbeResponse, MCPServer } from "@/api/mcp"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

export function MCPServerCard({
  server,
  probe,
  testing,
  toggling,
  loggingIn,
  onTest,
  onLogin,
  onToggle,
  onEdit,
  onDelete,
}: {
  server: MCPServer
  probe?: MCPProbeResponse
  testing: boolean
  toggling: boolean
  loggingIn: boolean
  onTest: () => void
  onLogin: () => void
  onToggle: (enabled: boolean) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const status = !server.enabled
    ? "disabled"
    : probe?.ok
      ? "connected"
      : probe
        ? "error"
        : "untested"
  const statusIcon =
    status === "connected" ? (
      <IconCheck className="size-3.5" />
    ) : status === "error" ? (
      <IconAlertTriangle className="size-3.5" />
    ) : (
      <span
        className={cn(
          "size-2 rounded-full",
          status === "disabled" ? "bg-muted-foreground/50" : "bg-primary/60",
        )}
      />
    )
  const target =
    server.type === "stdio"
      ? [server.command, ...server.args].filter(Boolean).join(" ")
      : server.url
  const toolNames = (probe?.tools ?? [])
    .map((tool) => (typeof tool === "string" ? tool : tool.name))
    .filter(Boolean)
  const authType = server.auth.type.trim().toLocaleLowerCase()
  const isRemote = server.type !== "stdio"
  const usesOAuth = authType === "oauth"
  const usesBearer = authType === "bearer"
  const usesCustomHeaders = authType === "custom"
  const showOAuthAction =
    isRemote &&
    (usesOAuth ||
      (probe?.auth_required === true && !usesBearer && !usesCustomHeaders))

  return (
    <Card
      size="sm"
      className="border-border/60 bg-card/60 hover:bg-card gap-3 transition-colors"
    >
      <CardHeader className="gap-2">
        <div className="flex min-w-0 items-start justify-between gap-3">
          <div className="min-w-0 space-y-1.5">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <CardTitle className="truncate text-sm font-semibold">
                {server.name}
              </CardTitle>
              <Badge variant="outline" className="font-mono uppercase">
                {t(`pages.agent.mcp.transport.${server.type}`)}
              </Badge>
            </div>
            <p
              className="text-muted-foreground truncate font-mono text-xs"
              title={target}
            >
              {target}
            </p>
          </div>

          <div className="flex shrink-0 items-center gap-1">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              onClick={onEdit}
              aria-label={t("pages.agent.mcp.action.edit", {
                name: server.name,
              })}
              title={t("pages.agent.mcp.action.edit", {
                name: server.name,
              })}
            >
              <IconEdit className="size-4" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
              onClick={onDelete}
              aria-label={t("pages.agent.mcp.action.delete", {
                name: server.name,
              })}
              title={t("pages.agent.mcp.action.delete", {
                name: server.name,
              })}
            >
              <IconTrash className="size-4" />
            </Button>
          </div>
        </div>
      </CardHeader>

      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Badge
            variant={status === "error" ? "destructive" : "secondary"}
            className="gap-1.5"
          >
            {statusIcon}
            {t(`pages.agent.mcp.status.${status}`)}
          </Badge>
          <Badge variant="outline">
            {server.deferred === null
              ? t("pages.agent.mcp.discovery.inherit")
              : server.deferred
                ? t("pages.agent.mcp.discovery.deferred")
                : t("pages.agent.mcp.discovery.eager")}
          </Badge>
          {(server.auth.configured ||
            server.auth.type.toLocaleLowerCase() !== "none") && (
            <Badge
              variant={server.auth.expired ? "destructive" : "outline"}
              className="gap-1"
            >
              <IconKey className="size-3" />
              {server.auth.expired
                ? t("pages.agent.mcp.auth.expired")
                : server.auth.configured
                  ? t("pages.agent.mcp.auth.configured")
                  : t("pages.agent.mcp.auth.not_configured")}
            </Badge>
          )}
        </div>

        {probe?.ok && (
          <div className="border-border/60 bg-muted/30 rounded-md border px-3 py-2">
            <p className="text-xs font-medium">
              {t("pages.agent.mcp.card.tools", {
                count: probe.tool_count,
              })}
            </p>
            {toolNames.length > 0 && (
              <p
                className="text-muted-foreground mt-1 truncate font-mono text-[11px]"
                title={toolNames.join(", ")}
              >
                {toolNames.slice(0, 5).join(", ")}
                {toolNames.length > 5 ? "…" : ""}
              </p>
            )}
          </div>
        )}

        {probe && !probe.ok && (
          <div className="bg-destructive/10 flex items-center justify-between gap-3 rounded-md px-3 py-2">
            <p className="text-destructive text-xs">
              {probe.auth_required
                ? t("pages.agent.mcp.probe.auth_required")
                : (probe.error ?? t("pages.agent.mcp.probe.failed_unknown"))}
            </p>
            {probe.auth_required &&
              isRemote &&
              (usesBearer || usesCustomHeaders) && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="shrink-0"
                  disabled={loggingIn}
                  onClick={onEdit}
                >
                  {loggingIn && <IconLoader2 className="size-4 animate-spin" />}
                  {t(
                    usesBearer
                      ? "pages.agent.mcp.action.set_token"
                      : "pages.agent.mcp.action.update_headers",
                  )}
                </Button>
              )}
          </div>
        )}
      </CardContent>

      <CardFooter className="border-border/60 flex-wrap justify-between gap-2 border-t">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={testing}
            onClick={onTest}
          >
            {testing ? (
              <IconLoader2 className="size-4 animate-spin" />
            ) : (
              <IconPlugConnected className="size-4" />
            )}
            {testing
              ? t("pages.agent.mcp.action.testing")
              : t("pages.agent.mcp.action.test")}
          </Button>
          {showOAuthAction && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={loggingIn}
              onClick={onLogin}
            >
              {loggingIn ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : (
                <IconLogin2 className="size-4" />
              )}
              {loggingIn
                ? t("pages.agent.mcp.action.logging_in")
                : server.auth.configured && !server.auth.expired
                  ? t("pages.agent.mcp.action.reconnect")
                  : t("pages.agent.mcp.action.login")}
            </Button>
          )}
        </div>

        <label className="flex items-center gap-2 text-xs font-medium">
          {t("pages.agent.mcp.action.enabled")}
          {toggling ? (
            <IconLoader2 className="text-muted-foreground size-4 animate-spin" />
          ) : (
            <Switch
              size="sm"
              checked={server.enabled}
              onCheckedChange={onToggle}
              aria-label={t("pages.agent.mcp.action.toggle", {
                name: server.name,
              })}
            />
          )}
        </label>
      </CardFooter>
    </Card>
  )
}
