import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDeviceFloppy,
  IconInfoCircle,
  IconPencil,
  IconPlus,
  IconRefresh,
  IconSettingsAutomation,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useState,
} from "react"

import { type PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-flow"
import {
  type PRLifecycleGateAction,
  type PRLifecycleGateActionType,
  type PRLifecycleWorkflowConfiguration,
  type PRLifecycleWorkflowConfigurationIssue,
  type PRLifecycleWorkflowConfigurationSnapshot,
  getPRLifecycleWorkflowConfigurations,
  isPRLifecycleWorkflowConfigurationID,
  putPRLifecycleWorkflowConfigurations,
  validatePRLifecycleWorkflowConfigurations,
} from "@/api/pr-lifecycle-workflow-configurations"
import { createPRWorkspaceRequestID } from "@/api/pr-workspaces"
import { PageHeader } from "@/components/page-header"
import { PRLifecycleGateMap } from "@/components/pr-workspaces/pr-lifecycle-gate-map"
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
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

const configQueryKey = ["pr-lifecycle", "workflow-configurations"] as const
const configDraftQueryKey = [
  "pr-lifecycle",
  "workflow-configurations",
  "draft",
] as const

export type PRLifecycleConfigurationPage = "configs" | "config" | "settings"
export type PRLifecycleSettingsTab = "nudging" | "scope"

interface CachedWorkflowConfigurationDraft {
  baseline: string
  draft: PRLifecycleWorkflowConfigurationSnapshot
}

interface WorkflowConfigurationPageProps {
  onBack: () => void
  page?: PRLifecycleConfigurationPage
  settingsTab?: PRLifecycleSettingsTab
  initialConfigID?: string
  initialDecisionPoint?: PRLifecycleDecisionPoint
  activeFlowID?: "review" | "implementation"
  discardOpen?: boolean
  onConfigChange?: (configID?: string) => void
  onDecisionPointChange?: (decisionPoint?: PRLifecycleDecisionPoint) => void
  onFlowChange?: (flowID: "review" | "implementation") => void
  onDiscardOpenChange?: (open: boolean) => void | Promise<void>
  onSettingsTabChange?: (tab: PRLifecycleSettingsTab) => void
}

export function PRLifecycleWorkflowConfigurationsPage({
  onBack,
  page,
  settingsTab = "nudging",
  initialConfigID,
  initialDecisionPoint,
  activeFlowID,
  discardOpen,
  onConfigChange,
  onDecisionPointChange,
  onFlowChange,
  onDiscardOpenChange,
  onSettingsTabChange,
}: WorkflowConfigurationPageProps) {
  const queryClient = useQueryClient()
  const cachedDraft =
    queryClient.getQueryData<CachedWorkflowConfigurationDraft>(
      configDraftQueryKey,
    )
  const cachedDirty =
    cachedDraft != null &&
    JSON.stringify(cachedDraft.draft) !== cachedDraft.baseline
  const [draft, setDraft] =
    useState<PRLifecycleWorkflowConfigurationSnapshot | null>(() =>
      cachedDirty ? structuredClone(cachedDraft.draft) : null,
    )
  const [baseline, setBaseline] = useState(() =>
    cachedDirty ? cachedDraft.baseline : "",
  )
  const [newConfigID, setNewConfigID] = useState("")
  const [newConfigName, setNewConfigName] = useState("")
  const [localConfigID, setLocalConfigID] = useState(initialConfigID ?? "")
  const [localDecisionPoint, setLocalDecisionPoint] =
    useState<PRLifecycleDecisionPoint | null>(initialDecisionPoint ?? null)
  const [localDiscardOpen, setLocalDiscardOpen] = useState(false)
  const [error, setError] = useState("")

  const resolvedPage =
    page ?? (initialConfigID || initialDecisionPoint ? "config" : "configs")
  const selectedConfigID =
    resolvedPage === "config"
      ? onConfigChange
        ? (initialConfigID ?? "")
        : localConfigID
      : ""
  const selectedDecisionPoint =
    resolvedPage === "config"
      ? onDecisionPointChange
        ? (initialDecisionPoint ?? null)
        : localDecisionPoint
      : null
  const resolvedDiscardOpen = Boolean(discardOpen || localDiscardOpen)
  const dirty = draft != null && JSON.stringify(draft) !== baseline

  const query = useQuery({
    queryKey: configQueryKey,
    queryFn: ({ signal }) => getPRLifecycleWorkflowConfigurations(signal),
    retry: false,
  })

  useEffect(() => {
    if (!query.data || dirty) return
    const next = structuredClone(query.data)
    const serialized = JSON.stringify(next)
    setDraft(next)
    setBaseline(serialized)
  }, [dirty, query.data])

  useEffect(() => {
    if (!draft || !baseline) return
    queryClient.setQueryData<CachedWorkflowConfigurationDraft>(
      configDraftQueryKey,
      {
        baseline,
        draft,
      },
    )
  }, [baseline, draft, queryClient])

  useEffect(() => {
    if (
      !draft ||
      !selectedConfigID ||
      draft.workflowConfigurations[selectedConfigID]
    )
      return
    setLocalConfigID("")
    setLocalDecisionPoint(null)
    onConfigChange?.()
  }, [draft, onConfigChange, selectedConfigID])

  const shouldBlockNavigation = useCallback(
    ({
      current,
      next,
    }: {
      current: { pathname: string }
      next: { pathname: string }
    }) => {
      const isConfigurationPath = (pathname: string) =>
        pathname === "/pull-requests/workflow-configurations" ||
        pathname.startsWith("/pull-requests/workflow-configurations/") ||
        pathname === "/pull-requests/settings"
      return (
        dirty &&
        isConfigurationPath(current.pathname) &&
        !isConfigurationPath(next.pathname)
      )
    },
    [dirty],
  )
  const blocker = useBlocker({
    shouldBlockFn: shouldBlockNavigation,
    enableBeforeUnload: () => dirty,
    disabled: !dirty,
    withResolver: true,
  })

  useEffect(() => {
    if (blocker.status !== "blocked" || resolvedDiscardOpen) return
    setLocalDiscardOpen(true)
    void onDiscardOpenChange?.(true)
  }, [blocker.status, onDiscardOpenChange, resolvedDiscardOpen])

  const issues = useMemo(
    () => (draft ? validatePRLifecycleWorkflowConfigurations(draft) : []),
    [draft],
  )
  const saveMutation = useMutation({
    mutationFn: (value: PRLifecycleWorkflowConfigurationSnapshot) =>
      putPRLifecycleWorkflowConfigurations({
        expectedConfigRevision: value.configRevision,
        requestID: createPRWorkspaceRequestID(),
        workflowConfigurations: value.workflowConfigurations,
        defaultWorkflowConfiguration: value.defaultWorkflowConfiguration,
        nudge: value.nudge,
        scope: value.scope,
      }),
    onSuccess: (next, submitted) => {
      const saved = structuredClone(next)
      const nextBaseline = JSON.stringify(saved)
      const submittedSnapshot = JSON.stringify(submitted)
      setDraft((current) => {
        if (!current || JSON.stringify(current) === submittedSnapshot) {
          return saved
        }
        return {
          ...current,
          gateCatalog: structuredClone(saved.gateCatalog),
          flow: structuredClone(saved.flow),
          flowRevision: saved.flowRevision,
          catalogRevision: saved.catalogRevision,
          configRevision: saved.configRevision,
          effects: structuredClone(saved.effects),
        }
      })
      setBaseline(nextBaseline)
      setError("")
      queryClient.setQueryData(configQueryKey, next)
      queryClient.setQueryData<CachedWorkflowConfigurationDraft>(
        configDraftQueryKey,
        {
          baseline: nextBaseline,
          draft: saved,
        },
      )
      void queryClient.invalidateQueries({
        queryKey: ["pr-lifecycle", "repository-assignments"],
      })
    },
    onError: (failure) =>
      setError(
        failure instanceof Error
          ? failure.message
          : "Workflow configurations could not be saved.",
      ),
  })

  if (query.isPending)
    return (
      <WorkflowConfigurationsState text="Loading Workflow configurations…" />
    )
  if (query.isError) {
    return (
      <WorkflowConfigurationsState
        text="Workflow configurations are unavailable."
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }
  if (!draft)
    return (
      <WorkflowConfigurationsState text="Loading Workflow configurations…" />
    )

  const selectedConfig = draft.workflowConfigurations[selectedConfigID]
  const selectedGateNode = selectedDecisionPoint
    ? draft.flow.flows
        .flatMap((flow) => flow.nodes)
        .find(
          (node) =>
            node.kind === "gate" &&
            node.editable &&
            node.decision_point === selectedDecisionPoint,
        )
    : undefined
  const selectedCatalogEntry = selectedDecisionPoint
    ? draft.gateCatalog[selectedDecisionPoint]
    : undefined

  const updateDraft = (
    update: (next: PRLifecycleWorkflowConfigurationSnapshot) => void,
  ) => {
    setDraft((current) => {
      if (!current) return current
      const next = structuredClone(current)
      update(next)
      return next
    })
  }
  const updateSelectedConfig = (
    update: (config: PRLifecycleWorkflowConfiguration) => void,
  ) =>
    updateDraft((next) => {
      const config = next.workflowConfigurations[selectedConfigID]
      if (config) update(config)
    })

  const selectConfig = (configID?: string) => {
    setLocalConfigID(configID ?? "")
    setLocalDecisionPoint(null)
    onConfigChange?.(configID)
  }
  const selectDecisionPoint = (decisionPoint: PRLifecycleDecisionPoint) => {
    setLocalDecisionPoint(decisionPoint)
    onDecisionPointChange?.(decisionPoint)
  }
  const closeDecisionPoint = () => {
    setLocalDecisionPoint(null)
    onDecisionPointChange?.()
  }

  const addConfig = (event: FormEvent) => {
    event.preventDefault()
    const id = newConfigID.trim()
    const name = newConfigName.trim()
    if (
      !isPRLifecycleWorkflowConfigurationID(id) ||
      !name ||
      draft.workflowConfigurations[id]
    )
      return
    updateDraft((next) => {
      next.workflowConfigurations[id] = {
        name,
        bindings: [],
        deferredIssues: { mode: "ask" },
      }
    })
    setNewConfigID("")
    setNewConfigName("")
    selectConfig(id)
  }

  const requestBack = () => {
    if (selectedConfig) {
      selectConfig()
      return
    }
    if (dirty) {
      setLocalDiscardOpen(true)
      void onDiscardOpenChange?.(true)
      return
    }
    onBack()
  }

  const closeDiscard = (open: boolean) => {
    setLocalDiscardOpen(open)
    if (!open && blocker.status === "blocked") blocker.reset()
    void onDiscardOpenChange?.(open)
  }
  const discardChanges = async () => {
    const saved = query.data ? structuredClone(query.data) : null
    if (saved) {
      const nextBaseline = JSON.stringify(saved)
      setDraft(saved)
      setBaseline(nextBaseline)
      queryClient.setQueryData<CachedWorkflowConfigurationDraft>(
        configDraftQueryKey,
        {
          baseline: nextBaseline,
          draft: saved,
        },
      )
    }
    setLocalDiscardOpen(false)
    await onDiscardOpenChange?.(false)
    if (blocker.status === "blocked") blocker.proceed()
    else onBack()
  }

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-workflow-configurations"
      data-config-view={resolvedPage}
      aria-busy={saveMutation.isPending}
    >
      <PageHeader
        title={
          selectedConfig
            ? `Edit ${selectedConfig.name} Workflow configuration`
            : resolvedPage === "settings"
              ? "PR lifecycle settings"
              : "Workflow configurations"
        }
        titleExtra={<Badge variant="outline">v3</Badge>}
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={
            selectedConfig ? "Back to Workflow configurations" : "Back"
          }
          title={selectedConfig ? "Back to Workflow configurations" : "Back"}
          onClick={requestBack}
        >
          <IconArrowLeft />
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label="Refresh"
          title="Refresh"
          onClick={() => void query.refetch()}
        >
          <IconRefresh />
        </Button>
        <Button
          type="button"
          disabled={!dirty || issues.length > 0 || saveMutation.isPending}
          onClick={() => saveMutation.mutate(draft)}
        >
          <IconDeviceFloppy />
          Save configuration
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-8 md:px-6">
        <div className="mx-auto w-full max-w-[96rem] min-w-0 space-y-4">
          {(error || issues.length > 0) && (
            <WorkflowConfigurationIssues error={error} issues={issues} />
          )}
          {draft.effects.gatewayEffect === "restart-required" && (
            <RestartNotice />
          )}

          {selectedConfig && (
            <PRLifecycleGateMap
              activeFlowID={activeFlowID}
              flow={draft.flow}
              flowRevision={draft.flowRevision}
              selectedDecisionPoint={selectedDecisionPoint ?? undefined}
              gateCatalog={draft.gateCatalog}
              bindings={selectedConfig.bindings}
              configName={selectedConfig.name}
              configID={selectedConfigID}
              onFlowChange={onFlowChange}
              onSelect={selectDecisionPoint}
            />
          )}

          {resolvedPage === "configs" && !selectedConfig && (
            <ConfigList
              draft={draft}
              newConfigID={newConfigID}
              newConfigName={newConfigName}
              onConfigIDChange={setNewConfigID}
              onConfigNameChange={setNewConfigName}
              onAdd={addConfig}
              onEdit={selectConfig}
              onMakeDefault={(configID) =>
                updateDraft((next) => {
                  next.defaultWorkflowConfiguration = configID
                })
              }
            />
          )}

          {selectedConfig && (
            <ConfigSettings
              config={selectedConfig}
              configID={selectedConfigID}
              defaultConfigID={draft.defaultWorkflowConfiguration}
              onChange={updateSelectedConfig}
              onDeferredIssueModeChange={(mode) =>
                updateSelectedConfig(
                  (config) => void (config.deferredIssues.mode = mode),
                )
              }
              onMakeDefault={() =>
                updateDraft((next) => {
                  next.defaultWorkflowConfiguration = selectedConfigID
                })
              }
            />
          )}

          {resolvedPage === "settings" && (
            <LifecycleSettings
              config={draft}
              tab={settingsTab}
              onTabChange={onSettingsTabChange}
              onChange={updateDraft}
            />
          )}
        </div>
      </div>

      <GateActionDialog
        open={
          !resolvedDiscardOpen &&
          Boolean(selectedConfig && selectedGateNode && selectedDecisionPoint)
        }
        nodeTitle={selectedGateNode?.title ?? selectedDecisionPoint ?? "Gate"}
        nodeDescription={selectedGateNode?.description}
        catalogEntry={selectedCatalogEntry}
        readOnly={selectedConfigID === "default"}
        binding={
          selectedCatalogEntry
            ? selectedConfig?.bindings.find(
                (binding) =>
                  binding.workflowRef === selectedCatalogEntry.workflowRef &&
                  binding.gateRef === selectedCatalogEntry.gateRef,
              )
            : undefined
        }
        onOpenChange={(open) => {
          if (!open) closeDecisionPoint()
        }}
        onActionChange={(action) => {
          if (!selectedCatalogEntry || selectedConfigID === "default") return
          updateSelectedConfig((config) => {
            const index = config.bindings.findIndex(
              (binding) =>
                binding.workflowRef === selectedCatalogEntry.workflowRef &&
                binding.gateRef === selectedCatalogEntry.gateRef,
            )
            if (!action) {
              if (index >= 0) config.bindings.splice(index, 1)
              return
            }
            const binding = {
              workflowRef: selectedCatalogEntry.workflowRef,
              gateRef: selectedCatalogEntry.gateRef,
              action,
            }
            if (index >= 0) config.bindings[index] = binding
            else config.bindings.push(binding)
          })
        }}
      />

      <AlertDialog open={resolvedDiscardOpen} onOpenChange={closeDiscard}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Discard Workflow configuration changes?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Your unsaved Workflow configuration and lifecycle setting changes
              will be lost.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={discardChanges}>
              Discard changes
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function ConfigList({
  draft,
  newConfigID,
  newConfigName,
  onConfigIDChange,
  onConfigNameChange,
  onAdd,
  onEdit,
  onMakeDefault,
}: {
  draft: PRLifecycleWorkflowConfigurationSnapshot
  newConfigID: string
  newConfigName: string
  onConfigIDChange: (value: string) => void
  onConfigNameChange: (value: string) => void
  onAdd: (event: FormEvent) => void
  onEdit: (configID: string) => void
  onMakeDefault: (configID: string) => void
}) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Workflow configurations</CardTitle>
        <CardDescription>
          Select how each published workflow Gate is executed. Workflows provide
          the defaults; configurations only store overrides.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div
          className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3"
          aria-label="Workflow configurations"
        >
          {Object.entries(draft.workflowConfigurations).map(([id, config]) => {
            return (
              <div
                className="border-border bg-muted/15 flex min-w-0 flex-col gap-3 rounded-lg border p-3"
                data-config-id={id}
                key={id}
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <strong className="truncate text-sm">{config.name}</strong>
                    {id === draft.defaultWorkflowConfiguration && (
                      <Badge variant="secondary">Default</Badge>
                    )}
                  </div>
                  <p className="text-muted-foreground mt-1 truncate font-mono text-xs">
                    {id}
                  </p>
                </div>
                <p className="text-muted-foreground text-xs">
                  {config.bindings.length}{" "}
                  {config.bindings.length === 1 ? "override" : "overrides"}
                </p>
                <p className="text-muted-foreground text-xs">
                  Deferred issues ·{" "}
                  {deferredIssueModeLabel(config.deferredIssues.mode)}
                </p>
                <div className="mt-auto flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    onClick={() => onEdit(id)}
                    aria-label={`Edit ${config.name} Workflow configuration`}
                  >
                    <IconPencil /> Edit
                  </Button>
                  {id !== draft.defaultWorkflowConfiguration && (
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => onMakeDefault(id)}
                    >
                      Make default
                    </Button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
        <form
          className="border-border grid gap-2 border-t pt-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
          onSubmit={onAdd}
        >
          <Input
            aria-label="Configuration ID"
            onChange={(event) => onConfigIDChange(event.target.value)}
            pattern="[a-z][a-z0-9-]{0,63}"
            placeholder="configuration-id"
            value={newConfigID}
          />
          <Input
            aria-label="Configuration name"
            onChange={(event) => onConfigNameChange(event.target.value)}
            placeholder="Configuration name"
            value={newConfigName}
          />
          <Button
            type="submit"
            size="sm"
            variant="outline"
            disabled={
              !isPRLifecycleWorkflowConfigurationID(newConfigID) ||
              !newConfigName.trim() ||
              Boolean(draft.workflowConfigurations[newConfigID])
            }
          >
            <IconPlus /> Add configuration
          </Button>
          <p className="text-muted-foreground text-xs sm:col-span-3">
            IDs use kebab-case and are stable API identifiers.
          </p>
        </form>
      </CardContent>
    </Card>
  )
}

function ConfigSettings({
  config,
  configID,
  defaultConfigID,
  onChange,
  onDeferredIssueModeChange,
  onMakeDefault,
}: {
  config: PRLifecycleWorkflowConfiguration
  configID: string
  defaultConfigID: string
  onChange: (update: (config: PRLifecycleWorkflowConfiguration) => void) => void
  onDeferredIssueModeChange: (
    mode: PRLifecycleWorkflowConfiguration["deferredIssues"]["mode"],
  ) => void
  onMakeDefault: () => void
}) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Configuration settings</CardTitle>
        <CardDescription className="font-mono">{configID}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {configID === "default" ? (
          <LockedField
            description="The built-in default configuration has a fixed name. Create a custom configuration to choose another name."
            label="Configuration name"
            value="Default"
          />
        ) : (
          <GateField label="Configuration name">
            <Input
              aria-label="Configuration name"
              value={config.name}
              onChange={(event) =>
                onChange((current) => void (current.name = event.target.value))
              }
            />
          </GateField>
        )}
        {configID === defaultConfigID ? (
          <Badge variant="secondary">Default configuration</Badge>
        ) : (
          <Button size="sm" variant="outline" onClick={onMakeDefault}>
            Make default
          </Button>
        )}
        <GateField label="Deferred issue mode">
          <Select
            value={config.deferredIssues.mode}
            onValueChange={(value) =>
              onDeferredIssueModeChange(
                value as PRLifecycleWorkflowConfiguration["deferredIssues"]["mode"],
              )
            }
          >
            <SelectTrigger aria-label="Deferred issue mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="off">Off</SelectItem>
              <SelectItem value="ask">Ask</SelectItem>
              <SelectItem value="automatic">Automatic</SelectItem>
            </SelectContent>
          </Select>
          <p className="text-muted-foreground mt-1.5 text-xs">
            Controls whether deferred findings for repositories using this
            configuration can become GitHub issues.
          </p>
        </GateField>
      </CardContent>
    </Card>
  )
}

function GateActionDialog({
  open,
  nodeTitle,
  nodeDescription,
  catalogEntry,
  binding,
  readOnly,
  onOpenChange,
  onActionChange,
}: {
  open: boolean
  nodeTitle: string
  nodeDescription?: string
  catalogEntry?: PRLifecycleWorkflowConfigurationSnapshot["gateCatalog"][string]
  binding?: { action?: PRLifecycleGateAction }
  readOnly: boolean
  onOpenChange: (open: boolean) => void
  onActionChange: (action?: PRLifecycleGateAction) => void
}) {
  const override = binding?.action
  const defaultAction = catalogEntry?.defaultAction
  const effectiveAction = override ?? defaultAction
  const mode = override?.type ?? "inherit"
  const displayedAIAction = (
    override?.type === "ai"
      ? override
      : override === undefined && effectiveAction?.type === "ai"
        ? effectiveAction
        : undefined
  ) as (PRLifecycleGateAction & { type: "ai" }) | undefined
  const [deterministicFields, setDeterministicFields] = useState("")
  const [deterministicError, setDeterministicError] = useState("")
  const readOnlyDescriptionID = useId()
  const readOnlyDescription =
    "The built-in default configuration always uses published workflow Gate actions. Create a custom configuration to override Gate actions."

  useEffect(() => {
    setDeterministicFields(
      override?.type === "deterministic"
        ? JSON.stringify(override.fields ?? {}, null, 2)
        : "{}",
    )
    setDeterministicError("")
  }, [open, override])

  const changeMode = (next: string) => {
    if (readOnly) return
    if (next === "inherit") {
      onActionChange()
      return
    }
    const type = next as PRLifecycleGateActionType
    onActionChange(defaultActionForType(type))
  }

  const patchAction = (patch: Partial<PRLifecycleGateAction>) => {
    if (readOnly || !override) return
    onActionChange({ ...override, ...patch })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[min(46rem,calc(100dvh-2rem))] w-[min(46rem,calc(100vw-2rem))] max-w-none flex-col overflow-hidden p-0">
        <DialogHeader className="border-border border-b px-5 py-4">
          <DialogTitle>{nodeTitle}</DialogTitle>
          <DialogDescription>
            {nodeDescription ?? "Configure how this workflow Gate executes."}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-4 overflow-auto px-5 py-4">
          {!catalogEntry ? (
            <div
              className="border-warning/50 bg-warning/10 rounded-lg border p-3 text-sm"
              role="alert"
            >
              This Gate has no published workflow metadata. Publish the workflow
              before configuring an override.
            </div>
          ) : (
            <>
              <dl className="bg-muted/30 grid min-w-0 gap-3 rounded-lg border p-3 text-xs sm:grid-cols-2">
                <Metadata
                  label="Workflow reference"
                  value={catalogEntry.workflowRef}
                />
                <Metadata label="Gate reference" value={catalogEntry.gateRef} />
                <Metadata
                  label="Published revision"
                  value={catalogEntry.workflowRevision ?? "Unavailable"}
                />
                <Metadata
                  label="Workflow default"
                  value={actionLabel(defaultAction)}
                />
                <Metadata
                  label="Override"
                  value={
                    override
                      ? actionLabel(override)
                      : "None — inherit workflow default"
                  }
                />
                <Metadata
                  label="Effective action"
                  value={actionLabel(effectiveAction)}
                />
              </dl>

              <GateRequestSummary catalogEntry={catalogEntry} />

              {readOnly && (
                <p
                  className="bg-muted/30 rounded-lg border p-3 text-sm"
                  id={readOnlyDescriptionID}
                  role="note"
                >
                  {readOnlyDescription}
                </p>
              )}

              <GateField label="Execution action">
                <Select
                  disabled={readOnly}
                  value={mode}
                  onValueChange={changeMode}
                >
                  <SelectTrigger
                    aria-describedby={
                      readOnly ? readOnlyDescriptionID : undefined
                    }
                    aria-label="Execution action"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">
                      Use workflow default
                    </SelectItem>
                    <SelectItem value="human">Human</SelectItem>
                    <SelectItem value="ai">AI</SelectItem>
                    <SelectItem value="deterministic">Deterministic</SelectItem>
                    <SelectItem value="workflow">Workflow</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground mt-1.5 text-xs">
                  “Use workflow default” removes this configuration’s binding
                  override.
                </p>
              </GateField>

              {override?.type === "human" && (
                <p className="bg-muted/30 rounded-lg border p-3 text-sm">
                  The workflow pauses at <code>gate/exec</code>, shows the
                  Gate’s generic fields to a user, validates the reply, and
                  resumes with <code>field-values</code>.
                </p>
              )}

              {displayedAIAction && (
                <AIActionFields
                  action={displayedAIAction}
                  inherited={readOnly || override === undefined}
                  lockExplanation={readOnly ? readOnlyDescription : undefined}
                  sourceAISupported={catalogEntry.sourceAISupported}
                  onChange={
                    override?.type === "ai"
                      ? (action) => onActionChange(action)
                      : undefined
                  }
                />
              )}

              {override?.type === "deterministic" && (
                <GateField label="Field expressions (JSON)">
                  <Textarea
                    aria-label="Deterministic field expressions"
                    className="min-h-40 font-mono text-xs"
                    value={deterministicFields}
                    aria-invalid={Boolean(deterministicError)}
                    aria-describedby={
                      readOnly ? readOnlyDescriptionID : undefined
                    }
                    disabled={readOnly}
                    onChange={(event) => {
                      const value = event.target.value
                      setDeterministicFields(value)
                      try {
                        const parsed: unknown = JSON.parse(value)
                        if (
                          typeof parsed !== "object" ||
                          parsed === null ||
                          Array.isArray(parsed)
                        )
                          throw new Error()
                        setDeterministicError("")
                        patchAction({
                          fields: parsed as Record<string, unknown>,
                        })
                      } catch {
                        setDeterministicError(
                          "Enter a JSON object keyed by Gate field ID.",
                        )
                      }
                    }}
                  />
                  {deterministicError && (
                    <p className="text-destructive mt-1 text-xs">
                      {deterministicError}
                    </p>
                  )}
                </GateField>
              )}

              {override?.type === "workflow" && (
                <GateField label="Action workflow reference">
                  <Input
                    aria-label="Action workflow reference"
                    aria-describedby={
                      readOnly ? readOnlyDescriptionID : undefined
                    }
                    disabled={readOnly}
                    value={override.workflowRef ?? ""}
                    onChange={(event) =>
                      patchAction({ workflowRef: event.target.value })
                    }
                  />
                </GateField>
              )}
            </>
          )}
        </div>
        <DialogFooter className="border-border border-t px-5 py-3">
          <p className="text-muted-foreground mr-auto text-xs">
            Changes remain a draft until the Workflow configuration is saved.
          </p>
          <DialogClose asChild>
            <Button variant="outline">Close — keep draft</Button>
          </DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function GateRequestSummary({
  catalogEntry,
}: {
  catalogEntry: PRLifecycleWorkflowConfigurationSnapshot["gateCatalog"][string]
}) {
  const fields = catalogEntry.fields ?? []
  return (
    <section
      aria-labelledby="pr-gate-request-title"
      className="min-w-0 space-y-2 rounded-lg border p-3"
    >
      <div>
        <h3 className="text-sm font-semibold" id="pr-gate-request-title">
          Gate request
        </h3>
        <p className="text-muted-foreground mt-1 text-xs leading-snug">
          {catalogEntry.prompt ??
            "The published workflow did not project a Gate prompt."}
        </p>
      </div>
      {fields.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No published Gate fields are available.
        </p>
      ) : (
        <ul className="grid min-w-0 gap-2 sm:grid-cols-2">
          {fields.map((field) => (
            <li
              className="bg-muted/20 min-w-0 rounded-md border p-2 text-xs"
              key={field.id}
            >
              <div className="flex min-w-0 flex-wrap items-start justify-between gap-1.5">
                <strong className="min-w-0 [overflow-wrap:anywhere]">
                  {field.label}
                </strong>
                <Badge variant="outline">{field.type}</Badge>
              </div>
              <code className="text-muted-foreground mt-1 block [overflow-wrap:anywhere]">
                {field.id}
              </code>
              <p className="text-muted-foreground mt-1">
                {field.type === "select"
                  ? `${field.minSelections}–${field.maxSelections} selections`
                  : field.required
                    ? "Required"
                    : "Optional"}
              </p>
              {field.type === "select" && (
                <p className="mt-1 [overflow-wrap:anywhere]">
                  {field.options
                    .map((option) => `${option.label} (${option.id})`)
                    .join(", ")}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

type AISessionMode = NonNullable<PRLifecycleGateAction["session"]>

const aiSessionLabels: Record<AISessionMode, string> = {
  ephemeral: "Ephemeral",
  private: "Private snapshot",
  source: "Originating snapshot",
}

const aiSessionDescriptions: Record<AISessionMode, string> = {
  ephemeral:
    "Runs an isolated request with no session history, cache, or tool authority.",
  private:
    "Reads a frozen private PR context for the selected agent without changing its history.",
  source:
    "Reads the exact protected snapshot captured from the AI run that produced this Gate's finding, using the same agent and the same pinned no-tool policy.",
}

function AIActionFields({
  action,
  inherited,
  lockExplanation,
  sourceAISupported,
  onChange,
}: {
  action: PRLifecycleGateAction & { type: "ai" }
  inherited: boolean
  lockExplanation?: string
  sourceAISupported: boolean
  onChange?: (action: PRLifecycleGateAction) => void
}) {
  const session = action.session ?? "ephemeral"
  const promptDescriptionID = useId()
  const fixedByWorkflow =
    lockExplanation ??
    "This value is defined by the published workflow default. Select AI as an override to change it."
  const describeEnforced = (reason: string) =>
    inherited ? `${reason} ${fixedByWorkflow}` : reason
  const replaceSession = (next: AISessionMode) =>
    onChange?.(aiActionForSession(action, next))

  return (
    <section aria-label="AI execution profile" className="space-y-3">
      {inherited && (
        <p className="bg-muted/30 rounded-lg border p-3 text-sm">
          {lockExplanation ??
            "Effective AI settings are read only because this Gate inherits its published workflow default."}
        </p>
      )}
      {session === "source" && !sourceAISupported && (
        <div
          className="border-destructive/50 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
          role="alert"
        >
          This Gate does not publish a source-bearing finding, so an originating
          snapshot cannot run here. The existing value is preserved for
          recovery; choose another session before saving.
        </div>
      )}
      {session === "source" && sourceAISupported && (
        <div
          className="border-warning/50 bg-warning/10 rounded-lg border p-3 text-sm"
          role="note"
        >
          The exact originating snapshot is resolved separately for each Gate
          execution. If its provenance, snapshot, or agent is unavailable or
          ambiguous, execution stops without falling back. Tool authority stays
          pinned to None.
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {inherited ? (
          <LockedField
            description={describeEnforced(aiSessionDescriptions[session])}
            label="Session"
            value={aiSessionLabels[session]}
          />
        ) : (
          <GateField label="Session">
            <Select
              value={session}
              onValueChange={(value) => replaceSession(value as AISessionMode)}
            >
              <SelectTrigger aria-label="Session">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ephemeral">Ephemeral</SelectItem>
                <SelectItem value="private">Private snapshot</SelectItem>
                {sourceAISupported ? (
                  <SelectItem value="source">Originating snapshot</SelectItem>
                ) : session === "source" ? (
                  <SelectItem disabled value="source">
                    Originating snapshot — unsupported
                  </SelectItem>
                ) : null}
              </SelectContent>
            </Select>
            <p className="text-muted-foreground mt-1.5 text-xs">
              {aiSessionDescriptions[session]}
            </p>
          </GateField>
        )}

        {inherited ? (
          <LockedField
            description={describeEnforced(
              session === "source"
                ? "The exact source snapshot pins the originating agent."
                : `The workflow selects agent ${action.agentID ?? "Unavailable"}.`,
            )}
            label="Agent ID"
            value={
              session === "source"
                ? "Same originating agent"
                : (action.agentID ?? "Unavailable")
            }
          />
        ) : session === "source" ? (
          <LockedField
            description="The exact source snapshot pins the same agent that produced the finding. This Workflow configuration cannot replace it."
            label="Agent ID"
            value="Same originating agent"
          />
        ) : (
          <GateField label="Agent ID">
            <Input
              aria-label="Agent ID"
              value={action.agentID ?? ""}
              onChange={(event) =>
                onChange?.({ ...action, agentID: event.target.value })
              }
            />
          </GateField>
        )}

        <LockedField
          description={describeEnforced(
            session === "ephemeral"
              ? "Ephemeral Gate actions have no session and therefore cannot read or write history."
              : session === "private"
                ? "Private Gate actions inspect one frozen read-only snapshot and cannot append to it."
                : "The Gate reads the exact protected snapshot captured from the originating session without appending to it.",
          )}
          label="History"
          value={
            session === "ephemeral"
              ? "None"
              : session === "private"
                ? "Read only"
                : "Exact source snapshot (read only)"
          }
        />

        {inherited ? (
          <LockedField
            description={describeEnforced(
              session === "source"
                ? "The source profile pins cache to None; only the exact read-only snapshot supplies prior context."
                : action.cache === "session"
                  ? "The private snapshot may use its session cache."
                  : "This AI profile does not use a cache.",
            )}
            label="Cache"
            value={session === "source" ? "None" : cacheLabel(action.cache)}
          />
        ) : session === "private" ? (
          <GateField label="Cache">
            <Select
              value={action.cache ?? "session"}
              onValueChange={(cache) =>
                onChange?.({ ...action, cache: cache as "none" | "session" })
              }
            >
              <SelectTrigger aria-label="Cache">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">None</SelectItem>
                <SelectItem value="session">Session</SelectItem>
              </SelectContent>
            </Select>
          </GateField>
        ) : (
          <LockedField
            description={
              session === "ephemeral"
                ? "Ephemeral Gate actions create no session cache."
                : "The source profile pins cache to None; only the exact read-only snapshot supplies prior context."
            }
            label="Cache"
            value="None"
          />
        )}

        <GateField className="sm:col-span-2" label="Prompt">
          <Textarea
            aria-label="AI prompt"
            disabled={inherited}
            value={action.prompt ?? ""}
            aria-describedby={inherited ? promptDescriptionID : undefined}
            onChange={(event) =>
              onChange?.({ ...action, prompt: event.target.value })
            }
          />
          {inherited && (
            <p
              className="text-muted-foreground mt-1.5 text-xs"
              id={promptDescriptionID}
            >
              {fixedByWorkflow}
            </p>
          )}
        </GateField>

        <LockedField
          className="sm:col-span-2"
          description={describeEnforced(
            session === "ephemeral"
              ? "Ephemeral Gate actions are isolated and have no tool authority."
              : session === "private"
                ? "Private Gate actions inspect frozen PR evidence; tools are disabled."
                : "The source run used a pinned no-tool policy. This Gate enforces that same policy, so tool authority remains None.",
          )}
          label="Tools"
          value="None"
        />
      </div>
    </section>
  )
}

function aiActionForSession(
  action: PRLifecycleGateAction & { type: "ai" },
  session: AISessionMode,
): PRLifecycleGateAction {
  const prompt = action.prompt ?? "Complete every required Gate field."
  if (session === "source") {
    return {
      type: "ai",
      prompt,
      session,
    }
  }
  const agentID = action.agentID || "main"
  if (session === "private") {
    return {
      type: "ai",
      agentID,
      prompt,
      session,
      history: "read_only",
      cache: "session",
      tools: "none",
    }
  }
  return {
    type: "ai",
    agentID,
    prompt,
    session,
    history: "none",
    cache: "none",
    tools: "none",
  }
}

function cacheLabel(cache: PRLifecycleGateAction["cache"]): string {
  if (cache === "session") return "Session"
  if (cache === "agent") return "Agent"
  return "None"
}

function deferredIssueModeLabel(
  mode: PRLifecycleWorkflowConfiguration["deferredIssues"]["mode"],
): string {
  if (mode === "automatic") return "Automatic"
  return mode === "off" ? "Off" : "Ask"
}

function LockedField({
  className,
  description,
  label,
  value,
}: {
  className?: string
  description: string
  label: string
  value: string
}) {
  const descriptionID = useId()
  return (
    <GateField className={className} label={label}>
      <div className="flex min-w-0 items-center gap-2">
        <Input
          aria-describedby={descriptionID}
          aria-label={label}
          className="min-w-0"
          data-enforced-setting
          disabled
          value={value}
        />
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                aria-label={`Why ${label} is fixed`}
                className="text-muted-foreground hover:text-foreground focus-visible:ring-ring flex size-8 shrink-0 items-center justify-center rounded-md outline-none focus-visible:ring-2"
                type="button"
              >
                <IconInfoCircle className="size-4" />
              </button>
            </TooltipTrigger>
            <TooltipContent>{description}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
      <span className="sr-only" id={descriptionID}>
        {description}
      </span>
    </GateField>
  )
}

function defaultActionForType(
  type: PRLifecycleGateActionType,
): PRLifecycleGateAction {
  switch (type) {
    case "ai":
      return {
        type,
        agentID: "main",
        prompt: "Complete every required Gate field.",
        session: "ephemeral",
        history: "none",
        cache: "none",
        tools: "none",
      }
    case "deterministic":
      return { type, fields: {} }
    case "workflow":
      return { type, workflowRef: "" }
    default:
      return { type }
  }
}

function actionLabel(action: PRLifecycleGateAction | undefined): string {
  if (!action) return "Unavailable"
  if (action.type === "ai") {
    const session = action.session
      ? aiSessionLabels[action.session]
      : "Unknown session"
    if (action.session === "source") return `AI · ${session}`
    return action.agentID
      ? `AI · ${action.agentID} · ${session}`
      : `AI · ${session}`
  }
  if (action.type === "workflow")
    return action.workflowRef ? `Workflow · ${action.workflowRef}` : "Workflow"
  return action.type === "human" ? "Human" : "Deterministic"
}

function Metadata({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted-foreground font-semibold">{label}</dt>
      <dd className="mt-0.5 [overflow-wrap:anywhere]">{value}</dd>
    </div>
  )
}

function SettingsTabs({
  active,
  onChange,
}: {
  active: PRLifecycleSettingsTab
  onChange?: (tab: PRLifecycleSettingsTab) => void
}) {
  const tabs: Array<{ id: PRLifecycleSettingsTab; label: string }> = [
    { id: "nudging", label: "Nudging" },
    { id: "scope", label: "Scope grades" },
  ]
  const handleKeyDown = (
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    let next: number | undefined
    if (event.key === "ArrowLeft" || event.key === "ArrowUp")
      next = (index - 1 + tabs.length) % tabs.length
    if (event.key === "ArrowRight" || event.key === "ArrowDown")
      next = (index + 1) % tabs.length
    if (event.key === "Home") next = 0
    if (event.key === "End") next = tabs.length - 1
    if (next === undefined) return
    event.preventDefault()
    onChange?.(tabs[next].id)
    requestAnimationFrame(() =>
      document
        .getElementById(`pr-lifecycle-settings-tab-${tabs[next].id}`)
        ?.focus(),
    )
  }
  return (
    <div
      aria-label="PR lifecycle settings"
      className="bg-muted/40 grid gap-1 rounded-lg border p-1 sm:grid-cols-2"
      role="tablist"
    >
      {tabs.map((tab, index) => (
        <Button
          aria-controls="pr-lifecycle-settings-panel"
          aria-selected={active === tab.id}
          id={`pr-lifecycle-settings-tab-${tab.id}`}
          key={tab.id}
          onKeyDown={(event) => handleKeyDown(event, index)}
          role="tab"
          size="sm"
          tabIndex={active === tab.id ? 0 : -1}
          variant={active === tab.id ? "secondary" : "ghost"}
          onClick={() => onChange?.(tab.id)}
        >
          {tab.label}
        </Button>
      ))}
    </div>
  )
}

function LifecycleSettings({
  config,
  onChange,
  onTabChange,
  tab,
}: {
  config: PRLifecycleWorkflowConfigurationSnapshot
  onChange: (
    update: (config: PRLifecycleWorkflowConfigurationSnapshot) => void,
  ) => void
  onTabChange?: (tab: PRLifecycleSettingsTab) => void
  tab: PRLifecycleSettingsTab
}) {
  return (
    <div className="space-y-4">
      <SettingsTabs active={tab} onChange={onTabChange} />
      <Card
        aria-labelledby={`pr-lifecycle-settings-tab-${tab}`}
        id="pr-lifecycle-settings-panel"
        role="tabpanel"
        size="sm"
      >
        <CardHeader>
          <CardTitle>
            {tab === "nudging" ? "Nudging" : "Scope grades"}
          </CardTitle>
          <CardDescription>
            {tab === "nudging"
              ? "Control additional AI attempts after an apparently complete review or implementation."
              : "Define file, semantic-line, and module boundaries for PR scope grades."}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {tab === "nudging" && (
            <>
              <NumberField
                label="Review minimum additional attempts"
                value={config.nudge.reviewMinimumAdditional}
                onChange={(value) =>
                  onChange(
                    (next) => void (next.nudge.reviewMinimumAdditional = value),
                  )
                }
              />
              <NumberField
                label="Review maximum additional attempts"
                value={config.nudge.reviewMaximumAdditional}
                onChange={(value) =>
                  onChange(
                    (next) => void (next.nudge.reviewMaximumAdditional = value),
                  )
                }
              />
              <NumberField
                label="Completion minimum additional attempts"
                value={config.nudge.completionMinimumAdditional}
                onChange={(value) =>
                  onChange(
                    (next) =>
                      void (next.nudge.completionMinimumAdditional = value),
                  )
                }
              />
              <NumberField
                label="Completion maximum additional attempts"
                value={config.nudge.completionMaximumAdditional}
                onChange={(value) =>
                  onChange(
                    (next) =>
                      void (next.nudge.completionMaximumAdditional = value),
                  )
                }
              />
            </>
          )}
          {tab === "scope" &&
            (["xs", "s", "m"] as const).flatMap((grade) => [
              <NumberField
                key={`${grade}-files`}
                label={`${grade.toUpperCase()} files`}
                value={config.scope[grade].files}
                onChange={(value) =>
                  onChange((next) => void (next.scope[grade].files = value))
                }
              />,
              <NumberField
                key={`${grade}-lines`}
                label={`${grade.toUpperCase()} semantic lines`}
                value={config.scope[grade].semanticLines}
                onChange={(value) =>
                  onChange(
                    (next) => void (next.scope[grade].semanticLines = value),
                  )
                }
              />,
              <NumberField
                key={`${grade}-modules`}
                label={`${grade.toUpperCase()} modules`}
                value={config.scope[grade].modules}
                onChange={(value) =>
                  onChange((next) => void (next.scope[grade].modules = value))
                }
              />,
            ])}
        </CardContent>
      </Card>
    </div>
  )
}

function NumberField({
  label,
  value,
  onChange,
}: {
  label: string
  value: number
  onChange: (value: number) => void
}) {
  return (
    <GateField label={label}>
      <Input
        aria-label={label}
        min={0}
        max={10000}
        type="number"
        value={value}
        onChange={(event) =>
          onChange(Math.max(0, Number(event.target.value) || 0))
        }
      />
    </GateField>
  )
}

function GateField({
  label,
  children,
  className,
}: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("max-w-full min-w-0", className)}>
      <Label className="mb-1.5 block">{label}</Label>
      {children}
    </div>
  )
}

function WorkflowConfigurationIssues({
  error,
  issues,
}: {
  error: string
  issues: PRLifecycleWorkflowConfigurationIssue[]
}) {
  return (
    <div
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
      role="alert"
    >
      {error ||
        `${issues.length} Workflow configuration ${issues.length === 1 ? "issue" : "issues"}.`}
      {issues.length > 0 && (
        <ul className="mt-1 list-disc pl-5">
          {issues.slice(0, 8).map((issue) => (
            <li key={`${issue.path}:${issue.message}`}>{issue.message}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

function RestartNotice() {
  return (
    <div
      className="border-border bg-muted/40 flex items-start gap-2 rounded-lg border p-3 text-sm"
      role="status"
    >
      <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
      <div>
        <strong>Gateway restart required</strong>
        <p className="text-muted-foreground mt-0.5 text-xs">
          Saved Workflow configurations will apply to future executions after
          the gateway restarts.
        </p>
      </div>
    </div>
  )
}

function WorkflowConfigurationsState({
  text,
  action,
}: {
  text: string
  action?: React.ReactNode
}) {
  return (
    <div className="bg-background flex h-full min-h-64 flex-col items-center justify-center gap-3 p-6 text-center">
      <IconSettingsAutomation className="text-muted-foreground size-8" />
      <p className="text-muted-foreground text-sm">{text}</p>
      {action}
    </div>
  )
}
