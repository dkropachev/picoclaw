import {
  IconAlertTriangle,
  IconDeviceFloppy,
  IconLoader2,
  IconRefresh,
} from "@tabler/icons-react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"

import {
  type AgentCapabilitiesPatch,
  type AgentCapabilitiesResponse,
  type AgentCapabilityPolicy,
  type AgentMCPCapabilityMode,
  type AgentSkillsCapabilityMode,
  type AgentToolsCapabilityMode,
  AgentsAPIError,
  getAgentCapabilities,
  patchAgentCapabilities,
} from "@/api/agents"
import { workflowAuthoringCapabilitiesQueryKey } from "@/api/workflows"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

const agentCapabilitiesQueryKey = (agentID: string) =>
  ["agents", agentID, "capabilities"] as const

interface CapabilityDraft {
  tools: AgentCapabilityPolicy<AgentToolsCapabilityMode>
  skills: AgentCapabilityPolicy<AgentSkillsCapabilityMode>
  mcp_servers: AgentCapabilityPolicy<AgentMCPCapabilityMode>
}

export function AgentCapabilitiesPanel({ agentID }: { agentID: string }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<CapabilityDraft | null>(null)
  const [initial, setInitial] = useState<CapabilityDraft | null>(null)
  const [revision, setRevision] = useState("")
  const [saving, setSaving] = useState(false)
  const [upgrading, setUpgrading] = useState(false)
  const [serverError, setServerError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [discardOpen, setDiscardOpen] = useState(false)
  const initializedRef = useRef("")
  const proceedingNavigationRef = useRef(false)

  const query = useQuery({
    queryKey: agentCapabilitiesQueryKey(agentID),
    queryFn: ({ signal }) => getAgentCapabilities(agentID, signal),
    retry: false,
    refetchOnReconnect: false,
    refetchOnWindowFocus: false,
  })

  const initialize = useCallback((response: AgentCapabilitiesResponse) => {
    const next = draftFromResponse(response)
    setDraft(next)
    setInitial(next)
    setRevision(response.revision)
    setServerError("")
    setConflicted(false)
    initializedRef.current = `${response.agent_id}:${response.revision}`
  }, [])

  useEffect(() => {
    initializedRef.current = ""
    setDraft(null)
    setInitial(null)
    setRevision("")
    setServerError("")
    setConflicted(false)
  }, [agentID])

  useEffect(() => {
    if (
      query.data != null &&
      initializedRef.current !== `${query.data.agent_id}:${query.data.revision}`
    ) {
      initialize(query.data)
    }
  }, [initialize, query.data])

  const dirty =
    draft != null &&
    initial != null &&
    JSON.stringify(draft) !== JSON.stringify(initial)
  const valid = draft != null && capabilityDraftIsValid(draft)
  const shouldBlockNavigation = useCallback(() => dirty, [dirty])
  const navigationBlocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: shouldBlockNavigation,
    disabled: !dirty,
    withResolver: true,
  })

  useEffect(() => {
    if (navigationBlocker.status === "blocked") setDiscardOpen(true)
  }, [navigationBlocker.status])

  const reloadLatest = async () => {
    const result = await query.refetch()
    if (result.isError || result.data == null) {
      setServerError(
        t(
          "pages.agent.agents.capabilities.reload_error",
          "The latest capabilities could not be loaded. Your draft is still preserved.",
        ),
      )
      return
    }
    initialize(result.data)
  }

  const applyResponse = async (
    response: AgentCapabilitiesResponse,
    message: string,
  ) => {
    queryClient.setQueryData(agentCapabilitiesQueryKey(agentID), response)
    initialize(response)
    await queryClient.invalidateQueries({
      queryKey: workflowAuthoringCapabilitiesQueryKey,
    })
    const gateway = await refreshGatewayState({ force: true })
    showSaveSuccessOrRestartToast(
      t,
      message,
      agentID,
      response.effects.gateway_effect === "restart_required" ||
        gateway?.restartRequired === true,
    )
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (
      draft == null ||
      initial == null ||
      query.data == null ||
      !query.data.editable ||
      saving ||
      conflicted ||
      !dirty ||
      !valid
    ) {
      return
    }

    const patch: AgentCapabilitiesPatch = { expected_revision: revision }
    if (!policiesEqual(draft.tools, initial.tools)) patch.tools = draft.tools
    if (!policiesEqual(draft.skills, initial.skills)) {
      patch.skills = draft.skills
    }
    if (!policiesEqual(draft.mcp_servers, initial.mcp_servers)) {
      patch.mcp_servers = draft.mcp_servers
    }

    setSaving(true)
    setServerError("")
    try {
      const response = await patchAgentCapabilities(agentID, patch)
      await applyResponse(
        response,
        t("pages.agent.agents.capabilities.saved", {
          defaultValue: "Saved capabilities for {{name}}.",
          name: agentID,
        }),
      )
    } catch (error) {
      if (isCapabilitiesRevisionConflict(error)) {
        setConflicted(true)
        setServerError(
          t(
            "pages.agent.agents.capabilities.conflict",
            "Capabilities changed after this page loaded. Your draft is preserved; reload the latest version before saving.",
          ),
        )
      } else {
        setServerError(
          t(
            "pages.agent.agents.capabilities.save_error",
            "Failed to save capabilities.",
          ),
        )
      }
    } finally {
      setSaving(false)
    }
  }

  const upgradeLegacy = async () => {
    if (query.data == null || upgrading || conflicted) return
    setUpgrading(true)
    setServerError("")
    try {
      const response = await patchAgentCapabilities(agentID, {
        expected_revision: query.data.revision,
        upgrade_legacy: true,
      })
      await applyResponse(
        response,
        t("pages.agent.agents.capabilities.upgraded", {
          defaultValue: "Upgraded capabilities for {{name}}.",
          name: agentID,
        }),
      )
    } catch (error) {
      if (isCapabilitiesRevisionConflict(error)) {
        setConflicted(true)
        setServerError(
          t(
            "pages.agent.agents.capabilities.upgrade_conflict",
            "The legacy file changed. Reload the latest version before upgrading.",
          ),
        )
      } else {
        setServerError(
          t(
            "pages.agent.agents.capabilities.upgrade_error",
            "Failed to upgrade the legacy capabilities file.",
          ),
        )
      }
    } finally {
      setUpgrading(false)
    }
  }

  const changeDiscardOpen = (open: boolean) => {
    if (!open && navigationBlocker.status === "blocked") {
      if (!proceedingNavigationRef.current) navigationBlocker.reset()
      proceedingNavigationRef.current = false
    }
    setDiscardOpen(open)
  }

  const discardChanges = () => {
    if (navigationBlocker.status === "blocked") {
      proceedingNavigationRef.current = true
      navigationBlocker.proceed()
    }
    setDiscardOpen(false)
  }

  if (query.isLoading) {
    return (
      <div
        className="flex justify-center py-16"
        aria-label="Loading capabilities"
      >
        <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
      </div>
    )
  }

  if (query.data == null) {
    return (
      <div className="bg-destructive/10 rounded-lg p-4" role="alert">
        <p className="text-destructive text-sm">
          {t(
            "pages.agent.agents.capabilities.load_error",
            "Failed to load capabilities.",
          )}
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="mt-3"
          onClick={() => void query.refetch()}
        >
          <IconRefresh className="size-4" />
          {t("common.retry", "Retry")}
        </Button>
      </div>
    )
  }

  if (draft == null || initial == null) {
    return (
      <div
        className="flex justify-center py-16"
        aria-label="Loading capabilities"
      >
        <IconLoader2 className="text-muted-foreground size-6 animate-spin" />
      </div>
    )
  }

  const response = query.data

  return (
    <>
      <form className="space-y-5" onSubmit={(event) => void submit(event)}>
        <div className="border-border bg-muted/20 flex flex-wrap items-center justify-between gap-2 rounded-lg border p-3">
          <div>
            <p className="text-sm font-medium">
              {t(
                "pages.agent.agents.capabilities.title",
                "Runtime capabilities",
              )}
            </p>
            <p className="text-muted-foreground mt-1 text-xs">
              {sourceDescription(response.source)}
            </p>
          </div>
          <Badge variant="outline">{sourceLabel(response.source)}</Badge>
        </div>

        {response.legacy_upgrade_required && (
          <div className="border-warning/40 bg-warning/10 rounded-lg border p-4">
            <div className="flex gap-3">
              <IconAlertTriangle className="mt-0.5 size-5 shrink-0" />
              <div className="min-w-0">
                <p className="text-sm font-medium">Legacy AGENTS.md detected</p>
                <p className="text-muted-foreground mt-1 text-xs">
                  Upgrade explicitly to AGENT.md before editing. The existing
                  file is left intact until you confirm.
                </p>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="mt-3"
                  disabled={upgrading || conflicted}
                  onClick={() => void upgradeLegacy()}
                >
                  {upgrading && <IconLoader2 className="size-4 animate-spin" />}
                  Upgrade legacy file
                </Button>
              </div>
            </div>
          </div>
        )}

        {!response.editable && !response.legacy_upgrade_required && (
          <div className="bg-destructive/10 rounded-lg p-4" role="alert">
            <p className="text-destructive text-sm font-medium">
              Capabilities are read-only
            </p>
            <p className="text-muted-foreground mt-1 text-xs">
              {issueDescription(response.issue_code)}
            </p>
          </div>
        )}

        {serverError && (
          <div className="bg-destructive/10 rounded-lg p-4" role="alert">
            <p className="text-destructive text-sm">{serverError}</p>
            {conflicted && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="mt-3"
                onClick={() => void reloadLatest()}
              >
                <IconRefresh className="size-4" />
                Reload latest capabilities
              </Button>
            )}
          </div>
        )}

        <CapabilityPolicyField
          legend="Tools"
          description="Choose which runtime tools this agent may use."
          modes={[
            ["all", "All tools"],
            ["none", "No tools"],
            ["selected", "Selected tools"],
          ]}
          policy={draft.tools}
          disabled={!response.editable || saving}
          catalog={response.catalogs.tools.map((item) => ({
            name: item.name,
            detail: [item.category, item.description]
              .filter(Boolean)
              .join(" · "),
            selectable: item.status === "enabled",
            unavailableLabel:
              item.status === "enabled"
                ? ""
                : item.status === "blocked"
                  ? "Blocked by runtime policy"
                  : "Disabled in configuration",
          }))}
          truncated={response.catalog_truncated.tools}
          onChange={(tools) =>
            setDraft((current) => (current ? { ...current, tools } : current))
          }
        />

        <CapabilityPolicyField
          legend="Skills"
          description="Inherit configured skills, disable skills, or choose an explicit set."
          modes={[
            ["inherit", "Inherit configured skills"],
            ["none", "No skills"],
            ["selected", "Selected skills"],
          ]}
          policy={draft.skills}
          disabled={!response.editable || saving}
          catalog={response.catalogs.skills.map((item) => ({
            name: item.name,
            detail: item.source,
            selectable: true,
            unavailableLabel: "",
          }))}
          truncated={response.catalog_truncated.skills}
          inheritedValues={response.capabilities.skills.inherited_values}
          onChange={(skills) =>
            setDraft((current) => (current ? { ...current, skills } : current))
          }
        />

        <CapabilityPolicyField
          legend="MCP servers"
          description="Choose which configured MCP servers are exposed to this agent."
          modes={[
            ["all", "All MCP servers"],
            ["none", "No MCP servers"],
            ["selected", "Selected MCP servers"],
          ]}
          policy={draft.mcp_servers}
          disabled={!response.editable || saving}
          catalog={response.catalogs.mcp_servers.map((item) => ({
            name: item.name,
            detail: item.enabled ? "Enabled" : "Disabled",
            selectable: item.enabled,
            unavailableLabel: item.enabled ? "" : "Disabled in configuration",
          }))}
          truncated={response.catalog_truncated.mcp_servers}
          onChange={(mcp_servers) =>
            setDraft((current) =>
              current ? { ...current, mcp_servers } : current,
            )
          }
        />

        <div className="border-border flex justify-end border-t pt-4">
          <Button
            type="submit"
            disabled={
              !response.editable || !dirty || !valid || saving || conflicted
            }
          >
            {saving ? (
              <IconLoader2 className="size-4 animate-spin" />
            ) : (
              <IconDeviceFloppy className="size-4" />
            )}
            Save capabilities
          </Button>
        </div>
      </form>

      <AlertDialog open={discardOpen} onOpenChange={changeDiscardOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard capability changes?</AlertDialogTitle>
            <AlertDialogDescription>
              Unsaved capability selections will be lost.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction onClick={discardChanges}>
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function CapabilityPolicyField<Mode extends string>({
  legend,
  description,
  modes,
  policy,
  disabled,
  catalog,
  truncated,
  inheritedValues = [],
  onChange,
}: {
  legend: string
  description: string
  modes: readonly (readonly [Mode, string])[]
  policy: AgentCapabilityPolicy<Mode>
  disabled: boolean
  catalog: {
    name: string
    detail: string
    selectable: boolean
    unavailableLabel: string
  }[]
  truncated: boolean
  inheritedValues?: string[]
  onChange: (policy: AgentCapabilityPolicy<Mode>) => void
}) {
  const known = useMemo(
    () => new Set(catalog.map((item) => capabilityValueKey(item.name))),
    [catalog],
  )
  const selectedKeys = useMemo(
    () => new Set(policy.values.map(capabilityValueKey)),
    [policy.values],
  )
  const unknown = policy.values.filter(
    (value) => !known.has(capabilityValueKey(value)),
  )

  const setMode = (mode: Mode) => {
    onChange({
      ...policy,
      mode,
      values: mode === "selected" ? policy.values : [],
    })
  }
  const toggle = (name: string, checked: boolean) => {
    const identityKey = capabilityValueKey(name)
    const retained = policy.values.filter(
      (value) => capabilityValueKey(value) !== identityKey,
    )
    onChange({
      ...policy,
      values: checked ? [...retained, name] : retained,
    })
  }

  return (
    <fieldset className="border-border space-y-4 rounded-lg border p-4">
      <legend className="px-1 text-sm font-semibold">{legend}</legend>
      <p className="text-muted-foreground text-xs">{description}</p>
      <div className="grid gap-2 sm:grid-cols-3">
        {modes.map(([mode, label]) => (
          <label
            key={mode}
            className="border-border has-[:checked]:border-primary has-[:checked]:bg-primary/5 flex min-w-0 cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-sm"
          >
            <input
              type="radio"
              name={`capability-${legend}`}
              value={mode}
              checked={policy.mode === mode}
              disabled={disabled}
              onChange={() => setMode(mode)}
            />
            <span>{label}</span>
          </label>
        ))}
      </div>

      {policy.mode === "inherit" && inheritedValues.length > 0 && (
        <p className="text-muted-foreground text-xs">
          Currently inherited: {inheritedValues.join(", ")}
        </p>
      )}

      {policy.mode === "selected" && (
        <div className="space-y-3">
          {catalog.length === 0 ? (
            <p className="text-muted-foreground text-xs">
              No catalog entries are available.
            </p>
          ) : (
            <ul className="grid gap-2 sm:grid-cols-2">
              {catalog.map((item) => {
                const selected = selectedKeys.has(capabilityValueKey(item.name))
                return (
                  <li key={item.name}>
                    <label className="border-border flex min-h-12 min-w-0 items-start gap-3 rounded-md border p-3">
                      <input
                        type="checkbox"
                        className="mt-0.5"
                        checked={selected}
                        disabled={disabled || (!selected && !item.selectable)}
                        onChange={(event) =>
                          toggle(item.name, event.target.checked)
                        }
                      />
                      <span className="min-w-0">
                        <span className="block font-mono text-xs font-medium break-words">
                          {item.name}
                        </span>
                        {(item.detail || item.unavailableLabel) && (
                          <span className="text-muted-foreground mt-0.5 block text-xs">
                            {item.unavailableLabel || item.detail}
                          </span>
                        )}
                      </span>
                    </label>
                  </li>
                )
              })}
            </ul>
          )}

          {unknown.length > 0 && (
            <div className="border-warning/40 bg-warning/5 rounded-md border p-3">
              <p className="text-xs font-medium">Existing unknown selections</p>
              <p className="text-muted-foreground mt-1 text-xs">
                These values are not in the sanitized catalog. They may be
                retained or removed, but new unknown values cannot be added.
              </p>
              <ul className="mt-2 flex flex-wrap gap-2">
                {unknown.map((value) => (
                  <li key={value}>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={disabled}
                      onClick={() => toggle(value, false)}
                      aria-label={`Remove unknown selection ${value}`}
                    >
                      <span className="max-w-48 truncate font-mono">
                        {value}
                      </span>
                      <span aria-hidden="true">×</span>
                    </Button>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {truncated && (
            <p className="text-warning-foreground text-xs" role="status">
              The catalog was truncated. Existing selections remain visible and
              removable.
            </p>
          )}
          {policy.values.length === 0 && (
            <p className="text-destructive text-xs" role="alert">
              Select at least one catalog entry.
            </p>
          )}
        </div>
      )}
    </fieldset>
  )
}

function draftFromResponse(
  response: AgentCapabilitiesResponse,
): CapabilityDraft {
  return {
    tools: {
      mode: response.capabilities.tools.mode,
      values: [...response.capabilities.tools.values],
    },
    skills: {
      mode: response.capabilities.skills.mode,
      values: [...response.capabilities.skills.values],
    },
    mcp_servers: {
      mode: response.capabilities.mcp_servers.mode,
      values: [...response.capabilities.mcp_servers.values],
    },
  }
}

function policiesEqual<Mode extends string>(
  left: AgentCapabilityPolicy<Mode>,
  right: AgentCapabilityPolicy<Mode>,
) {
  return left.mode === right.mode && arraysEqual(left.values, right.values)
}

function isCapabilitiesRevisionConflict(error: unknown): boolean {
  return (
    error instanceof AgentsAPIError &&
    error.status === 409 &&
    error.code === "capabilities_revision_mismatch"
  )
}

function arraysEqual(left: string[], right: string[]) {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  )
}

function capabilityValueKey(value: string) {
  return value.toLowerCase()
}

function capabilityDraftIsValid(draft: CapabilityDraft) {
  return [draft.tools, draft.skills, draft.mcp_servers].every(
    (policy) => policy.mode !== "selected" || policy.values.length > 0,
  )
}

function sourceLabel(source: AgentCapabilitiesResponse["source"]) {
  if (source === "agent") return "AGENT.md"
  if (source === "legacy") return "AGENTS.md"
  return "Defaults"
}

function sourceDescription(source: AgentCapabilitiesResponse["source"]) {
  if (source === "agent") {
    return "Policies stored in this agent workspace’s AGENT.md frontmatter."
  }
  if (source === "legacy") {
    return "Policies currently come from a legacy AGENTS.md file."
  }
  return "No policy file exists yet. Saving creates AGENT.md safely."
}

function issueDescription(issueCode: string) {
  switch (issueCode) {
    case "agent_definition_invalid":
      return "AGENT.md frontmatter is malformed. Fix it in the workspace before editing here."
    case "agent_definition_too_large":
      return "The agent definition is too large to edit safely from the dashboard."
    case "agent_definition_not_regular":
      return "The agent definition is not a safe regular workspace file."
    case "agent_definition_unavailable":
      return "The agent workspace is unavailable."
    case "atomic_replace_unavailable":
      return "This platform cannot safely replace agent capability files, so dashboard editing is read-only."
    default:
      return "The workspace capability policy cannot be edited from the dashboard."
  }
}
