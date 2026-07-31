import {
  IconArrowLeft,
  IconEdit,
  IconRefresh,
  IconStar,
} from "@tabler/icons-react"
import { type KeyboardEvent, useEffect, useMemo, useRef } from "react"

import type { AgentInfo } from "@/api/agents"
import { PageHeader } from "@/components/page-header"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

import { AgentActivityPanel } from "./agent-activity-panel"
import { AgentCapabilitiesPanel } from "./agent-capabilities-panel"
import { type AgentDetailTab, agentDetailTabs } from "./agent-route-search"

export function AgentDetailPage({
  agent,
  agentID,
  tab,
  loading,
  loadError,
  onBack,
  onTabChange,
  onEdit,
  onRefresh,
}: {
  agent: AgentInfo | undefined
  agentID: string
  tab: AgentDetailTab
  loading: boolean
  loadError: boolean
  onBack: () => void
  onTabChange: (tab: AgentDetailTab) => void
  onEdit: () => void
  onRefresh: () => void
}) {
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([])
  const pendingKeyboardFocusRef = useRef<AgentDetailTab | null>(null)
  const tabs = useMemo(
    () =>
      [
        ["overview", "Overview"],
        ["capabilities", "Capabilities"],
        ["activity", "Activity"],
      ] as const,
    [],
  )

  useEffect(() => {
    if (pendingKeyboardFocusRef.current !== tab) return
    pendingKeyboardFocusRef.current = null
    const index = agentDetailTabs.indexOf(tab)
    requestAnimationFrame(() => tabRefs.current[index]?.focus())
  }, [tab])

  const selectFromKeyboard = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    let next: number
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      next = (index + 1) % agentDetailTabs.length
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      next = (index - 1 + agentDetailTabs.length) % agentDetailTabs.length
    } else if (event.key === "Home") {
      next = 0
    } else if (event.key === "End") {
      next = agentDetailTabs.length - 1
    } else {
      return
    }
    event.preventDefault()
    pendingKeyboardFocusRef.current = agentDetailTabs[next]
    onTabChange(agentDetailTabs[next])
  }

  return (
    <div className="flex h-full min-w-0 flex-col">
      <PageHeader
        title={agent?.name || agent?.id || "Agent"}
        titleExtra={
          <span className="text-muted-foreground max-w-40 truncate font-mono text-xs sm:max-w-none">
            {agentID}
          </span>
        }
      >
        <Button type="button" variant="outline" size="sm" onClick={onBack}>
          <IconArrowLeft className="size-4" />
          <span className="hidden sm:inline">All agents</span>
          <span className="sm:hidden">Back</span>
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          disabled={loading}
          onClick={onRefresh}
          aria-label="Refresh agent"
        >
          <IconRefresh className={loading ? "size-4 animate-spin" : "size-4"} />
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-y-auto px-3 py-4 sm:px-6">
        <div className="mx-auto w-full max-w-[1000px]">
          {loading && agent == null ? (
            <p className="text-muted-foreground py-16 text-center text-sm">
              Loading agent…
            </p>
          ) : loadError ? (
            <div
              className="bg-destructive/10 rounded-lg p-6 text-center"
              role="alert"
            >
              <h2 className="text-destructive text-base font-semibold">
                Failed to load the agent
              </h2>
              <p className="text-muted-foreground mt-2 text-sm">
                Retry without changing the selected agent.
              </p>
              <Button
                type="button"
                variant="outline"
                className="mt-4"
                onClick={onRefresh}
              >
                <IconRefresh className="size-4" />
                Retry
              </Button>
            </div>
          ) : agent == null ? (
            <div className="border-border rounded-lg border p-6 text-center">
              <h2 className="text-base font-semibold">Agent not found</h2>
              <p className="text-muted-foreground mt-2 text-sm">
                No agent exists with the exact ID{" "}
                <span className="font-mono">{agentID}</span>.
              </p>
              <Button
                type="button"
                variant="outline"
                className="mt-4"
                onClick={onBack}
              >
                <IconArrowLeft className="size-4" />
                Return to agents
              </Button>
            </div>
          ) : (
            <>
              <div className="border-border mb-4 overflow-x-auto border-b">
                <div
                  role="tablist"
                  aria-label="Agent management"
                  className="flex min-w-max gap-1"
                >
                  {tabs.map(([value, label], index) => (
                    <button
                      key={value}
                      ref={(element) => {
                        tabRefs.current[index] = element
                      }}
                      type="button"
                      role="tab"
                      id={`agent-tab-${value}`}
                      aria-selected={tab === value}
                      aria-controls={`agent-panel-${value}`}
                      tabIndex={tab === value ? 0 : -1}
                      className="border-primary focus-visible:ring-ring px-4 py-2.5 text-sm outline-none focus-visible:ring-2 aria-selected:border-b-2 aria-selected:font-medium"
                      onClick={() => onTabChange(value)}
                      onKeyDown={(event) => selectFromKeyboard(event, index)}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>

              <div
                id={`agent-panel-${tab}`}
                role="tabpanel"
                aria-labelledby={`agent-tab-${tab}`}
                tabIndex={0}
                className="min-w-0 outline-none"
              >
                {tab === "overview" && (
                  <AgentOverview agent={agent} onEdit={onEdit} />
                )}
                {tab === "capabilities" && (
                  <AgentCapabilitiesPanel agentID={agent.id} />
                )}
                {tab === "activity" && (
                  <AgentActivityPanel agentID={agent.id} />
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function AgentOverview({
  agent,
  onEdit,
}: {
  agent: AgentInfo
  onEdit: () => void
}) {
  const fallbacks =
    agent.model?.fallbacks == null
      ? "Inherit"
      : agent.model.fallbacks.length === 0
        ? "None"
        : agent.model.fallbacks.join(" → ")
  const skills =
    agent.skills == null || agent.skills.length === 0
      ? "All skills"
      : agent.skills.join(", ")
  const delegation =
    agent.subagents == null || agent.subagents.allow_agents.length === 0
      ? "No delegation"
      : agent.subagents.allow_agents[0] === "*"
        ? "All peers"
        : agent.subagents.allow_agents.join(", ")

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
      <Card>
        <CardHeader className="flex-row items-center justify-between gap-3">
          <div>
            <CardTitle>Configured policy</CardTitle>
            <p className="text-muted-foreground mt-1 text-xs">
              Persistent launcher configuration for this agent.
            </p>
          </div>
          <Button type="button" size="sm" variant="outline" onClick={onEdit}>
            <IconEdit className="size-4" />
            Edit
          </Button>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-[minmax(7rem,auto)_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm">
            <dt className="text-muted-foreground">Workspace</dt>
            <dd className="min-w-0 text-right font-mono text-xs break-words">
              {agent.workspace || "Inherit"}
            </dd>
            <dt className="text-muted-foreground">Provider account</dt>
            <dd className="min-w-0 text-right font-mono text-xs break-words">
              {agent.account_ref || "Inherit"}
            </dd>
            <dt className="text-muted-foreground">Primary model alias</dt>
            <dd className="min-w-0 text-right break-words">
              {agent.model?.primary || "Inherit"}
            </dd>
            <dt className="text-muted-foreground">Fallback model aliases</dt>
            <dd className="min-w-0 text-right break-words">{fallbacks}</dd>
            <dt className="text-muted-foreground">Configured skills</dt>
            <dd className="min-w-0 text-right break-words">{skills}</dd>
            <dt className="text-muted-foreground">Delegation</dt>
            <dd className="min-w-0 text-right break-words">{delegation}</dd>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Status</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap gap-2">
            {agent.is_default && (
              <Badge variant="secondary">
                <IconStar className="size-3" />
                Default
              </Badge>
            )}
            {agent.implicit && <Badge variant="outline">Implicit</Badge>}
          </div>
          <p className="text-muted-foreground text-xs">
            Capabilities are managed separately from launcher configuration so
            workspace policy remains explicit.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
