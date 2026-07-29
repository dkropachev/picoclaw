import {
  IconAdjustments,
  IconLoader2,
  IconPlugConnected,
  IconPlus,
  IconRefresh,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useCallback, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type MCPProbeResponse,
  type MCPServer,
  deleteMCPServer,
  getMCPConfig,
  testMCPServer,
  updateMCPServer,
  updateMCPSettings,
} from "@/api/mcp"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { DeleteMCPServerDialog } from "./delete-mcp-server-dialog"
import { MCPServerCard } from "./mcp-server-card"
import { serverInputFromServer } from "./mcp-server-form"
import { MCPServerSheet } from "./mcp-server-sheet"
import { MCPSettingsSheet } from "./mcp-settings-sheet"
import { useMCPOAuth } from "./use-mcp-oauth"

const MCP_QUERY_KEY = ["mcp"] as const

export function MCPPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [serverSheetOpen, setServerSheetOpen] = useState(false)
  const [editingServer, setEditingServer] = useState<MCPServer | null>(null)
  const [deletingServer, setDeletingServer] = useState<MCPServer | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [probes, setProbes] = useState<Record<string, MCPProbeResponse>>({})

  const notifySaved = useCallback(
    async (message: string, name: string) => {
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        message,
        name,
        gateway?.restartRequired === true,
      )
    },
    [t],
  )

  const query = useQuery({
    queryKey: MCP_QUERY_KEY,
    queryFn: getMCPConfig,
  })

  const handleOAuthSuccess = useCallback(
    async ({
      server_name: serverName,
      tool_count: toolCount = 0,
      tools = [],
    }: {
      server_name: string
      tool_count?: number
      tools?: string[]
    }) => {
      setProbes((current) => ({
        ...current,
        [serverName]: {
          ok: true,
          tool_count: toolCount,
          tools,
        },
      }))
      await queryClient.invalidateQueries({ queryKey: MCP_QUERY_KEY })
      await notifySaved(
        t("pages.agent.mcp.oauth.login_success", {
          name: serverName,
          count: toolCount,
        }),
        serverName,
      )
    },
    [notifySaved, queryClient, t],
  )

  const oauth = useMCPOAuth(handleOAuthSuccess)

  const settingsMutation = useMutation({
    mutationFn: updateMCPSettings,
    onSuccess: (config) => {
      queryClient.setQueryData(MCP_QUERY_KEY, config)
      setSettingsOpen(false)
      void notifySaved(
        t("pages.agent.mcp.toast.settings_saved"),
        t("navigation.mcp"),
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t("pages.agent.mcp.toast.settings_failed"),
      )
    },
  })

  const toggleServerMutation = useMutation({
    mutationFn: ({
      server,
      enabled,
    }: {
      server: MCPServer
      enabled: boolean
    }) =>
      updateMCPServer(server.name, {
        ...serverInputFromServer(server),
        enabled,
      }),
    onSuccess: (config, variables) => {
      queryClient.setQueryData(MCP_QUERY_KEY, config)
      setProbes((current) => {
        const next = { ...current }
        delete next[variables.server.name]
        return next
      })
      void notifySaved(
        t(
          variables.enabled
            ? "pages.agent.mcp.toast.enabled"
            : "pages.agent.mcp.toast.disabled",
          { name: variables.server.name },
        ),
        variables.server.name,
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t("pages.agent.mcp.toast.toggle_failed"),
      )
    },
  })

  const testMutation = useMutation({
    mutationFn: async (server: MCPServer) => ({
      name: server.name,
      result: await testMCPServer(serverInputFromServer(server), server.name),
    }),
    onSuccess: ({ name, result }) => {
      setProbes((current) => ({ ...current, [name]: result }))
      if (result.ok) {
        toast.success(
          t("pages.agent.mcp.toast.test_success", {
            name,
            count: result.tool_count,
          }),
        )
      } else {
        toast.error(
          result.auth_required
            ? t("pages.agent.mcp.probe.auth_required")
            : (result.error ??
                t("pages.agent.mcp.toast.test_failed", { name })),
        )
      }
    },
    onError: (error, server) => {
      const result: MCPProbeResponse = {
        ok: false,
        tool_count: 0,
        tools: [],
        error:
          error instanceof Error
            ? error.message
            : t("pages.agent.mcp.probe.failed_unknown"),
      }
      setProbes((current) => ({ ...current, [server.name]: result }))
      toast.error(result.error)
    },
  })

  const deleteMutation = useMutation({
    mutationFn: deleteMCPServer,
    onSuccess: async (_, name) => {
      setDeletingServer(null)
      setProbes((current) => {
        const next = { ...current }
        delete next[name]
        return next
      })
      await queryClient.invalidateQueries({ queryKey: MCP_QUERY_KEY })
      await notifySaved(t("pages.agent.mcp.toast.deleted", { name }), name)
    },
    onError: (error) => {
      toast.error(
        error instanceof Error
          ? error.message
          : t("pages.agent.mcp.toast.delete_failed"),
      )
    },
  })

  const config = query.data
  const settingsError = settingsMutation.error
    ? settingsMutation.error instanceof Error
      ? settingsMutation.error.message
      : t("pages.agent.mcp.toast.settings_failed")
    : ""

  const openAdd = () => {
    setEditingServer(null)
    setServerSheetOpen(true)
  }

  const openEdit = (server: MCPServer) => {
    setEditingServer(server)
    setServerSheetOpen(true)
  }

  const handleSheetOpenChange = (open: boolean) => {
    setServerSheetOpen(open)
    if (!open) setEditingServer(null)
  }

  const handleProbe = (name: string, result: MCPProbeResponse) => {
    setProbes((current) => ({ ...current, [name]: result }))
  }

  const handleServerSaved = async (
    name: string,
    previousName?: string,
  ): Promise<void> => {
    setProbes((current) => {
      const next = { ...current }
      delete next[name]
      if (previousName) delete next[previousName]
      return next
    })
    await refresh()
  }

  const handleGlobalToggle = (enabled: boolean) => {
    if (!config) return
    settingsMutation.mutate({
      enabled,
      discovery: config.discovery,
    })
  }

  const refresh = async () => {
    await query.refetch()
  }

  return (
    <div className="bg-background flex h-full flex-col">
      <PageHeader
        title={t("navigation.mcp")}
        titleExtra={
          config ? (
            <Badge variant="secondary" className="hidden sm:inline-flex">
              {config.servers.length}
            </Badge>
          ) : null
        }
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          disabled={query.isFetching}
          onClick={() => void refresh()}
          aria-label={t("common.refresh")}
          title={t("common.refresh")}
        >
          <IconRefresh
            className={`size-4 ${query.isFetching ? "animate-spin" : ""}`}
          />
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => setSettingsOpen(true)}
          disabled={!config}
          aria-label={t("pages.agent.mcp.settings.button")}
          title={t("pages.agent.mcp.settings.button")}
        >
          <IconAdjustments className="size-4" />
          <span className="hidden sm:inline">
            {t("pages.agent.mcp.settings.button")}
          </span>
        </Button>
        <Button
          type="button"
          onClick={openAdd}
          disabled={!config}
          aria-label={t("pages.agent.mcp.add")}
          title={t("pages.agent.mcp.add")}
        >
          <IconPlus className="size-4" />
          <span className="hidden sm:inline">{t("pages.agent.mcp.add")}</span>
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
        <div className="mx-auto w-full max-w-6xl space-y-5">
          <p className="text-muted-foreground max-w-3xl text-sm">
            {t("pages.agent.mcp.description")}
          </p>

          {query.isLoading && (
            <div className="flex items-center justify-center py-24">
              <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
            </div>
          )}

          {query.isError && (
            <div className="bg-destructive/10 rounded-lg px-4 py-4 text-sm">
              <p className="text-destructive">
                {query.error instanceof Error
                  ? query.error.message
                  : t("pages.agent.mcp.load_error")}
              </p>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={() => void refresh()}
              >
                {t("pages.agent.mcp.retry")}
              </Button>
            </div>
          )}

          {config && (
            <>
              <section className="border-border/70 bg-card/50 flex flex-col gap-3 rounded-lg border px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="flex min-w-0 items-center gap-3">
                  <span className="bg-primary/10 text-primary flex size-9 shrink-0 items-center justify-center rounded-lg">
                    <IconPlugConnected className="size-5" />
                  </span>
                  <div className="min-w-0">
                    <p className="text-sm font-medium">
                      {t("pages.agent.mcp.integration_enabled")}
                    </p>
                    <p className="text-muted-foreground text-xs">
                      {config.enabled
                        ? t("pages.agent.mcp.integration_enabled_hint")
                        : t("pages.agent.mcp.integration_disabled_hint")}
                    </p>
                  </div>
                </div>
                <div className="flex items-center gap-3 self-end sm:self-auto">
                  <span className="text-muted-foreground text-xs">
                    {config.enabled
                      ? t("common.enabled")
                      : t("common.disabled")}
                  </span>
                  {settingsMutation.isPending ? (
                    <IconLoader2 className="text-muted-foreground size-4 animate-spin" />
                  ) : (
                    <Switch
                      checked={config.enabled}
                      onCheckedChange={handleGlobalToggle}
                      aria-label={t("pages.agent.mcp.integration_enabled")}
                    />
                  )}
                </div>
              </section>

              {config.servers.length === 0 ? (
                <section className="border-border/60 flex flex-col items-center rounded-lg border border-dashed px-6 py-16 text-center">
                  <span className="bg-muted text-muted-foreground flex size-11 items-center justify-center rounded-xl">
                    <IconPlugConnected className="size-5" />
                  </span>
                  <h3 className="mt-4 text-sm font-semibold">
                    {t("pages.agent.mcp.empty_title")}
                  </h3>
                  <p className="text-muted-foreground mt-1 max-w-md text-sm">
                    {t("pages.agent.mcp.empty_description")}
                  </p>
                  <Button type="button" className="mt-5" onClick={openAdd}>
                    <IconPlus className="size-4" />
                    {t("pages.agent.mcp.add")}
                  </Button>
                </section>
              ) : (
                <section
                  className="grid gap-3 md:grid-cols-2"
                  aria-label={t("pages.agent.mcp.server_list")}
                >
                  {config.servers.map((server) => (
                    <MCPServerCard
                      key={server.name}
                      server={server}
                      probe={probes[server.name]}
                      testing={
                        testMutation.isPending &&
                        testMutation.variables?.name === server.name
                      }
                      toggling={
                        toggleServerMutation.isPending &&
                        toggleServerMutation.variables?.server.name ===
                          server.name
                      }
                      loggingIn={
                        oauth.activeServerName === server.name &&
                        oauth.loggingIn
                      }
                      onTest={() => testMutation.mutate(server)}
                      onLogin={() => void oauth.startLogin(server.name)}
                      onToggle={(enabled) =>
                        toggleServerMutation.mutate({ server, enabled })
                      }
                      onEdit={() => openEdit(server)}
                      onDelete={() => setDeletingServer(server)}
                    />
                  ))}
                </section>
              )}
            </>
          )}
        </div>
      </div>

      {config && (
        <>
          <MCPServerSheet
            open={serverSheetOpen}
            server={editingServer}
            existingNames={config.servers.map((server) => server.name)}
            onOpenChange={handleSheetOpenChange}
            onSaved={handleServerSaved}
            onProbe={handleProbe}
            onOAuthLogin={oauth.startLogin}
          />
          <MCPSettingsSheet
            open={settingsOpen}
            config={config}
            saving={settingsMutation.isPending}
            error={settingsError}
            onOpenChange={setSettingsOpen}
            onSave={(settings) => settingsMutation.mutate(settings)}
          />
          <DeleteMCPServerDialog
            server={deletingServer}
            deleting={deleteMutation.isPending}
            onOpenChange={(open) => {
              if (!open && !deleteMutation.isPending) setDeletingServer(null)
            }}
            onConfirm={() => {
              if (deletingServer) deleteMutation.mutate(deletingServer.name)
            }}
          />
        </>
      )}
    </div>
  )
}
