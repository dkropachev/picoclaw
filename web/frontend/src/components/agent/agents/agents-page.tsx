import { IconLoader2, IconPlus, IconRefresh } from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useCallback, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type AgentInfo,
  type AgentMutationInput,
  type AgentsResponse,
  createAgent,
  deleteAgent,
  getAgents,
  setDefaultAgent,
  updateAgent,
} from "@/api/agents"
import { workflowAuthoringCapabilitiesQueryKey } from "@/api/workflows"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

import { AgentCard } from "./agent-card"
import { AgentDetailPage } from "./agent-detail-page"
import { type AgentEditorSession, AgentEditorSheet } from "./agent-editor-sheet"
import type { AgentDetailTab, AgentsRouteSearch } from "./agent-route-search"
import {
  DeleteAgentDialog,
  type DeleteAgentSession,
} from "./delete-agent-dialog"

const agentsQueryKey = ["agents"] as const

export function AgentsPage({
  search = {},
  onSearchChange = () => undefined,
}: {
  search?: AgentsRouteSearch
  onSearchChange?: (search: AgentsRouteSearch, replace?: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [editor, setEditor] = useState<AgentEditorSession | null>(null)
  const [deleteSession, setDeleteSession] = useState<DeleteAgentSession | null>(
    null,
  )
  const [settingDefaultID, setSettingDefaultID] = useState("")
  const returnFocusRef = useRef<HTMLElement | null>(null)
  const addButtonRef = useRef<HTMLButtonElement | null>(null)

  const query = useQuery({
    queryKey: agentsQueryKey,
    queryFn: getAgents,
    retry: false,
  })

  const rememberFocus = () => {
    returnFocusRef.current =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : addButtonRef.current
  }

  const restoreFocus = useCallback(() => {
    const target = returnFocusRef.current
    returnFocusRef.current = null
    requestAnimationFrame(() => {
      if (target?.isConnected) {
        target.focus()
      } else {
        addButtonRef.current?.focus()
      }
    })
  }, [])

  const closeEditor = useCallback(() => {
    setEditor(null)
    restoreFocus()
  }, [restoreFocus])

  const closeDelete = useCallback(() => {
    setDeleteSession(null)
    restoreFocus()
  }, [restoreFocus])

  const applyMutation = useCallback(
    async (response: AgentsResponse, message: string, name: string) => {
      queryClient.setQueryData(agentsQueryKey, response)
      await queryClient.invalidateQueries({
        queryKey: workflowAuthoringCapabilitiesQueryKey,
      })
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        message,
        name,
        response.effects.gateway_effect === "restart_required" ||
          gateway?.restartRequired === true,
      )
    },
    [queryClient, t],
  )

  const openCreate = () => {
    if (query.data == null) return
    rememberFocus()
    setEditor({
      mode: "create",
      agent: null,
      revision: query.data.config_revision,
    })
  }

  const openEdit = (agent: AgentInfo) => {
    if (query.data == null) return
    rememberFocus()
    setEditor({
      mode: "edit",
      agent: structuredClone(agent),
      revision: query.data.config_revision,
    })
  }

  const openManage = (agent: AgentInfo) => {
    onSearchChange({ agent: agent.id, tab: "overview" })
  }

  const openDelete = (agent: AgentInfo) => {
    if (query.data == null) return
    rememberFocus()
    setDeleteSession({
      agent: structuredClone(agent),
      revision: query.data.config_revision,
    })
  }

  const save = async (
    input: AgentMutationInput,
    expectedRevision: string,
    mode: "create" | "edit",
  ) => {
    const response =
      mode === "create"
        ? await createAgent(expectedRevision, input)
        : await updateAgent(
            editor?.agent?.id ?? input.id,
            expectedRevision,
            input,
          )
    await applyMutation(
      response,
      mode === "create"
        ? t("pages.agent.agents.toast.created", {
            defaultValue: "Created {{name}}.",
            name: input.id,
          })
        : t("pages.agent.agents.toast.saved", {
            defaultValue: "Saved {{name}}.",
            name: input.id,
          }),
      input.id,
    )
    closeEditor()
  }

  const remove = async (agent: AgentInfo, expectedRevision: string) => {
    const response = await deleteAgent(agent.id, expectedRevision)
    queryClient.setQueryData(agentsQueryKey, response)
    closeDelete()
    await applyMutation(
      response,
      t("pages.agent.agents.toast.deleted", {
        defaultValue: "Deleted {{name}}.",
        name: agent.id,
      }),
      agent.id,
    )
  }

  const makeDefault = async (agent: AgentInfo) => {
    if (
      query.data == null ||
      (agent.is_default && (agent.default_configured || agent.implicit))
    ) {
      return
    }
    const expectedRevision = query.data.config_revision
    setSettingDefaultID(agent.id)
    try {
      const response = await setDefaultAgent(agent.id, expectedRevision)
      await applyMutation(
        response,
        t("pages.agent.agents.toast.default_changed", {
          defaultValue: "{{name}} is now the default agent.",
          name: agent.id,
        }),
        agent.id,
      )
    } catch (error) {
      toast.error(
        error instanceof Error
          ? humanizeAPIMessage(error.message)
          : t(
              "pages.agent.agents.toast.default_failed",
              "Failed to change the default agent.",
            ),
      )
      try {
        await query.refetch()
      } catch {
        // The mutation error is already visible; a later manual refresh can retry.
      }
    } finally {
      setSettingDefaultID("")
    }
  }

  const refreshAfterConflict = async () => {
    const result = await query.refetch()
    if (result.isError || result.data == null) {
      throw result.error ?? new Error("agent_config_refresh_failed")
    }
  }

  const agents = query.data?.agents ?? []
  const selectedAgent =
    search.agent == null
      ? undefined
      : agents.find((agent) => agent.id === search.agent)

  if (search.agent != null) {
    return (
      <>
        <AgentDetailPage
          agent={selectedAgent}
          agentID={search.agent}
          tab={search.tab ?? "overview"}
          loading={query.isLoading || query.isFetching}
          loadError={query.isError && query.data == null}
          onBack={() => onSearchChange({})}
          onTabChange={(tab: AgentDetailTab) =>
            onSearchChange({ agent: search.agent, tab })
          }
          onEdit={() => selectedAgent && openEdit(selectedAgent)}
          onRefresh={() => void query.refetch()}
        />
        <AgentEditorSheet
          session={editor}
          agents={agents}
          latestRevision={query.data?.config_revision ?? ""}
          onSubmit={save}
          onConflict={refreshAfterConflict}
          onClose={closeEditor}
        />
      </>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t("navigation.agents", "Agents")}
        titleExtra={
          query.data ? (
            <span className="text-muted-foreground font-mono text-xs">
              {t("pages.agent.agents.count", {
                defaultValue: "{{count}} agents",
                count: agents.length,
              })}
            </span>
          ) : null
        }
      >
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
          aria-label={t("common.refresh", "Refresh")}
          title={t("common.refresh", "Refresh")}
        >
          {query.isFetching ? (
            <IconLoader2 className="size-4 animate-spin" />
          ) : (
            <IconRefresh className="size-4" />
          )}
        </Button>
        <Button
          ref={addButtonRef}
          type="button"
          size="sm"
          disabled={query.data == null}
          onClick={openCreate}
        >
          <IconPlus className="size-4" />
          {t("pages.agent.agents.create", "Create agent")}
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
        <div className="mx-auto w-full max-w-[1200px]">
          <p className="text-muted-foreground text-sm">
            {t(
              "pages.agent.agents.description",
              "Create agents, choose the default, and configure model, skills, workspace, and delegation policies.",
            )}
          </p>

          {query.isLoading && (
            <div className="flex items-center justify-center py-20">
              <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
            </div>
          )}

          {query.error && (
            <div
              className="bg-destructive/10 mt-4 rounded-lg p-4 text-sm"
              role="alert"
            >
              <p className="text-destructive">
                {t("pages.agent.agents.load_error", "Failed to load agents.")}
              </p>
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="mt-3"
                onClick={() => void query.refetch()}
              >
                {t("common.retry", "Retry")}
              </Button>
            </div>
          )}

          {!query.isLoading && !query.error && (
            <div className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {agents.map((agent) => (
                <AgentCard
                  key={agent.id}
                  agent={agent}
                  settingDefault={settingDefaultID === agent.id}
                  onEdit={() => openEdit(agent)}
                  onManage={() => openManage(agent)}
                  onSetDefault={() => void makeDefault(agent)}
                  onDelete={() => openDelete(agent)}
                />
              ))}
              {agents.length === 0 && (
                <p className="text-muted-foreground py-12 text-sm">
                  {t("pages.agent.agents.empty", "No agents are available.")}
                </p>
              )}
            </div>
          )}
        </div>
      </div>

      <AgentEditorSheet
        session={editor}
        agents={agents}
        latestRevision={query.data?.config_revision ?? ""}
        onSubmit={save}
        onConflict={refreshAfterConflict}
        onClose={closeEditor}
      />
      <DeleteAgentDialog
        session={deleteSession}
        onDelete={remove}
        onConflict={refreshAfterConflict}
        onClose={closeDelete}
      />
    </div>
  )
}

function humanizeAPIMessage(message: string): string {
  if (!/^[a-z0-9_]+$/.test(message)) return message
  const words = message.replaceAll("_", " ")
  return `${words.charAt(0).toLocaleUpperCase()}${words.slice(1)}.`
}
