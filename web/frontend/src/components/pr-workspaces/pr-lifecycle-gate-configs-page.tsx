import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDeviceFloppy,
  IconPencil,
  IconPlus,
  IconRefresh,
  IconSettingsAutomation,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useBlocker } from "@tanstack/react-router"
import {
  type FormEvent,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react"

import { type PRLifecycleDecisionPoint } from "@/api/pr-lifecycle-flow"
import {
  type PRLifecycleGateAction,
  type PRLifecycleGateActionType,
  type PRLifecycleGateConfig,
  type PRLifecycleGateConfigIssue,
  type PRLifecycleGateConfigSnapshot,
  getPRLifecycleGateConfigs,
  isPRLifecycleGateConfigID,
  putPRLifecycleGateConfigs,
  validatePRLifecycleGateConfigs,
} from "@/api/pr-lifecycle-gate-configs"
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
import { cn } from "@/lib/utils"

const configQueryKey = ["pr-lifecycle", "gate-configs"] as const
const configDraftQueryKey = ["pr-lifecycle", "gate-configs", "draft"] as const

export type PRLifecycleConfigurationPage = "configs" | "config" | "settings"
export type PRLifecycleSettingsTab = "nudging" | "scope" | "deferred"

interface CachedGateConfigDraft {
  baseline: string
  draft: PRLifecycleGateConfigSnapshot
}

interface GatePageProps {
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

export function PRLifecycleGateConfigsPage({
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
}: GatePageProps) {
  const queryClient = useQueryClient()
  const cachedDraft =
    queryClient.getQueryData<CachedGateConfigDraft>(configDraftQueryKey)
  const cachedDirty =
    cachedDraft != null &&
    JSON.stringify(cachedDraft.draft) !== cachedDraft.baseline
  const [draft, setDraft] = useState<PRLifecycleGateConfigSnapshot | null>(
    () => (cachedDirty ? structuredClone(cachedDraft.draft) : null),
  )
  const [baseline, setBaseline] = useState(() =>
    cachedDirty ? cachedDraft.baseline : "",
  )
  const [newConfigID, setNewConfigID] = useState("")
  const [newConfigName, setNewConfigName] = useState("")
  const [newRepository, setNewRepository] = useState("")
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
    queryFn: ({ signal }) => getPRLifecycleGateConfigs(signal),
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
    queryClient.setQueryData<CachedGateConfigDraft>(configDraftQueryKey, {
      baseline,
      draft,
    })
  }, [baseline, draft, queryClient])

  useEffect(() => {
    if (!draft || !selectedConfigID || draft.gateConfigs[selectedConfigID])
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
        pathname === "/pull-requests/gate-configs" ||
        pathname.startsWith("/pull-requests/gate-configs/") ||
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
    () => (draft ? validatePRLifecycleGateConfigs(draft) : []),
    [draft],
  )
  const saveMutation = useMutation({
    mutationFn: (value: PRLifecycleGateConfigSnapshot) =>
      putPRLifecycleGateConfigs({
        expectedConfigRevision: value.configRevision,
        requestID: createPRWorkspaceRequestID(),
        gateConfigs: value.gateConfigs,
        defaultGateConfig: value.defaultGateConfig,
        repositoryAssignments: value.repositoryAssignments,
        nudge: value.nudge,
        scope: value.scope,
        deferredIssues: value.deferredIssues,
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
      queryClient.setQueryData<CachedGateConfigDraft>(configDraftQueryKey, {
        baseline: nextBaseline,
        draft: saved,
      })
    },
    onError: (failure) =>
      setError(
        failure instanceof Error
          ? failure.message
          : "Gate configurations could not be saved.",
      ),
  })

  if (query.isPending)
    return <GateConfigsState text="Loading Gate configurations…" />
  if (query.isError) {
    return (
      <GateConfigsState
        text="Gate configurations are unavailable."
        action={<Button onClick={() => void query.refetch()}>Retry</Button>}
      />
    )
  }
  if (!draft) return <GateConfigsState text="Loading Gate configurations…" />

  const selectedConfig = draft.gateConfigs[selectedConfigID]
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
    update: (next: PRLifecycleGateConfigSnapshot) => void,
  ) => {
    setDraft((current) => {
      if (!current) return current
      const next = structuredClone(current)
      update(next)
      return next
    })
  }
  const updateSelectedConfig = (
    update: (config: PRLifecycleGateConfig) => void,
  ) =>
    updateDraft((next) => {
      const config = next.gateConfigs[selectedConfigID]
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
    if (!isPRLifecycleGateConfigID(id) || !name || draft.gateConfigs[id]) return
    updateDraft((next) => {
      next.gateConfigs[id] = { name, bindings: [] }
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
      queryClient.setQueryData<CachedGateConfigDraft>(configDraftQueryKey, {
        baseline: nextBaseline,
        draft: saved,
      })
    }
    setLocalDiscardOpen(false)
    await onDiscardOpenChange?.(false)
    if (blocker.status === "blocked") blocker.proceed()
    else onBack()
  }

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-gate-configs"
      data-config-view={resolvedPage}
      aria-busy={saveMutation.isPending}
    >
      <PageHeader
        title={
          selectedConfig
            ? `Edit ${selectedConfig.name} Gate configuration`
            : resolvedPage === "settings"
              ? "PR lifecycle settings"
              : "Gate configurations"
        }
        titleExtra={<Badge variant="outline">v3</Badge>}
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={selectedConfig ? "Back to Gate configurations" : "Back"}
          title={selectedConfig ? "Back to Gate configurations" : "Back"}
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
            <GateConfigIssues error={error} issues={issues} />
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
                  next.defaultGateConfig = configID
                })
              }
            />
          )}

          {selectedConfig && (
            <ConfigSettings
              config={selectedConfig}
              configID={selectedConfigID}
              defaultConfigID={draft.defaultGateConfig}
              repositoryAssignments={draft.repositoryAssignments}
              newRepository={newRepository}
              onNewRepositoryChange={setNewRepository}
              onChange={updateSelectedConfig}
              onMakeDefault={() =>
                updateDraft((next) => {
                  next.defaultGateConfig = selectedConfigID
                })
              }
              onAddRepository={() => {
                const repository = newRepository.trim()
                if (!repository) return
                updateDraft((next) => {
                  next.repositoryAssignments[repository] = selectedConfigID
                })
                setNewRepository("")
              }}
              onRemoveRepository={(repository) =>
                updateDraft((next) => {
                  delete next.repositoryAssignments[repository]
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
        open={Boolean(
          selectedConfig && selectedGateNode && selectedDecisionPoint,
        )}
        nodeTitle={selectedGateNode?.title ?? selectedDecisionPoint ?? "Gate"}
        nodeDescription={selectedGateNode?.description}
        catalogEntry={selectedCatalogEntry}
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
          if (!selectedCatalogEntry) return
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
              Discard Gate configuration changes?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Your unsaved Gate configuration and lifecycle setting changes will
              be lost.
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
  draft: PRLifecycleGateConfigSnapshot
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
        <CardTitle>Gate configurations</CardTitle>
        <CardDescription>
          Select how each published workflow Gate is executed. Workflows provide
          the defaults; configurations only store overrides.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div
          className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3"
          aria-label="Gate configurations"
        >
          {Object.entries(draft.gateConfigs).map(([id, config]) => {
            const assignmentCount = Object.values(
              draft.repositoryAssignments,
            ).filter((configID) => configID === id).length
            return (
              <div
                className="border-border bg-muted/15 flex min-w-0 flex-col gap-3 rounded-lg border p-3"
                data-config-id={id}
                key={id}
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <strong className="truncate text-sm">{config.name}</strong>
                    {id === draft.defaultGateConfig && (
                      <Badge variant="secondary">Default</Badge>
                    )}
                  </div>
                  <p className="text-muted-foreground mt-1 truncate font-mono text-xs">
                    {id}
                  </p>
                </div>
                <p className="text-muted-foreground text-xs">
                  {config.bindings.length}{" "}
                  {config.bindings.length === 1 ? "override" : "overrides"} ·{" "}
                  {assignmentCount}{" "}
                  {assignmentCount === 1 ? "repository" : "repositories"}
                </p>
                <div className="mt-auto flex flex-wrap gap-2">
                  <Button
                    size="sm"
                    onClick={() => onEdit(id)}
                    aria-label={`Edit ${config.name} Gate configuration`}
                  >
                    <IconPencil /> Edit
                  </Button>
                  {id !== draft.defaultGateConfig && (
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
              !isPRLifecycleGateConfigID(newConfigID) ||
              !newConfigName.trim() ||
              Boolean(draft.gateConfigs[newConfigID])
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
  repositoryAssignments,
  newRepository,
  onNewRepositoryChange,
  onChange,
  onMakeDefault,
  onAddRepository,
  onRemoveRepository,
}: {
  config: PRLifecycleGateConfig
  configID: string
  defaultConfigID: string
  repositoryAssignments: Record<string, string>
  newRepository: string
  onNewRepositoryChange: (value: string) => void
  onChange: (update: (config: PRLifecycleGateConfig) => void) => void
  onMakeDefault: () => void
  onAddRepository: () => void
  onRemoveRepository: (repository: string) => void
}) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Configuration settings</CardTitle>
        <CardDescription className="font-mono">{configID}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 lg:grid-cols-2">
        <div className="space-y-3">
          <GateField label="Configuration name">
            <Input
              aria-label="Configuration name"
              value={config.name}
              onChange={(event) =>
                onChange((current) => void (current.name = event.target.value))
              }
            />
          </GateField>
          {configID === defaultConfigID ? (
            <Badge variant="secondary">Default configuration</Badge>
          ) : (
            <Button size="sm" variant="outline" onClick={onMakeDefault}>
              Make default
            </Button>
          )}
        </div>
        <div className="space-y-2">
          <Label>Repository assignments</Label>
          <form
            className="flex gap-2"
            onSubmit={(event) => {
              event.preventDefault()
              onAddRepository()
            }}
          >
            <Input
              aria-label="Repository assignment"
              placeholder="https://github.com|repository-id"
              value={newRepository}
              onChange={(event) => onNewRepositoryChange(event.target.value)}
            />
            <Button
              size="icon"
              type="submit"
              variant="outline"
              aria-label="Add repository assignment"
            >
              <IconPlus />
            </Button>
          </form>
          {Object.entries(repositoryAssignments)
            .filter(([, assigned]) => assigned === configID)
            .map(([repository]) => (
              <div className="flex items-center gap-2 text-xs" key={repository}>
                <span className="min-w-0 flex-1 truncate">{repository}</span>
                <Button
                  className="size-7"
                  size="icon"
                  variant="ghost"
                  aria-label={`Remove ${repository}`}
                  onClick={() => onRemoveRepository(repository)}
                >
                  <IconTrash />
                </Button>
              </div>
            ))}
        </div>
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
  onOpenChange,
  onActionChange,
}: {
  open: boolean
  nodeTitle: string
  nodeDescription?: string
  catalogEntry?: PRLifecycleGateConfigSnapshot["gateCatalog"][string]
  binding?: { action?: PRLifecycleGateAction }
  onOpenChange: (open: boolean) => void
  onActionChange: (action?: PRLifecycleGateAction) => void
}) {
  const override = binding?.action
  const defaultAction = catalogEntry?.defaultAction
  const effectiveAction = override ?? defaultAction
  const mode = override?.type ?? "inherit"
  const [deterministicFields, setDeterministicFields] = useState("")
  const [deterministicError, setDeterministicError] = useState("")

  useEffect(() => {
    setDeterministicFields(
      override?.type === "deterministic"
        ? JSON.stringify(override.fields ?? {}, null, 2)
        : "{}",
    )
    setDeterministicError("")
  }, [open, override])

  const changeMode = (next: string) => {
    if (next === "inherit") {
      onActionChange()
      return
    }
    const type = next as PRLifecycleGateActionType
    onActionChange(defaultActionForType(type))
  }

  const patchAction = (patch: Partial<PRLifecycleGateAction>) => {
    if (!override) return
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

              <GateField label="Execution action">
                <Select value={mode} onValueChange={changeMode}>
                  <SelectTrigger aria-label="Execution action">
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

              {override?.type === "ai" && (
                <div className="grid gap-3 sm:grid-cols-2">
                  <GateField label="Agent ID">
                    <Input
                      aria-label="Agent ID"
                      value={override.agentID ?? ""}
                      onChange={(event) =>
                        patchAction({ agentID: event.target.value })
                      }
                    />
                  </GateField>
                  <GateField label="Session">
                    <Select
                      value={override.session ?? "ephemeral"}
                      onValueChange={(session) =>
                        patchAction({
                          session: session as "ephemeral" | "private",
                          history: session === "private" ? "read_only" : "none",
                          cache: session === "private" ? "session" : "none",
                          tools: "none",
                        })
                      }
                    >
                      <SelectTrigger aria-label="Session">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="ephemeral">Ephemeral</SelectItem>
                        <SelectItem value="private">Private</SelectItem>
                      </SelectContent>
                    </Select>
                  </GateField>
                  <GateField label="History">
                    <Select
                      value={override.history ?? "none"}
                      onValueChange={(history) =>
                        patchAction({
                          history: history as
                            | "none"
                            | "read_only"
                            | "read_write",
                        })
                      }
                    >
                      <SelectTrigger aria-label="History">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {override.session === "private" ? (
                          <SelectItem value="read_only">Read only</SelectItem>
                        ) : (
                          <SelectItem value="none">None</SelectItem>
                        )}
                      </SelectContent>
                    </Select>
                  </GateField>
                  <GateField label="Cache">
                    <Select
                      value={override.cache ?? "none"}
                      onValueChange={(cache) =>
                        patchAction({
                          cache: cache as "none" | "session" | "agent",
                        })
                      }
                    >
                      <SelectTrigger aria-label="Cache">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="none">None</SelectItem>
                        {override.session === "private" && (
                          <SelectItem value="session">Session</SelectItem>
                        )}
                      </SelectContent>
                    </Select>
                  </GateField>
                  <GateField className="sm:col-span-2" label="Prompt">
                    <Textarea
                      aria-label="AI prompt"
                      value={override.prompt ?? ""}
                      onChange={(event) =>
                        patchAction({ prompt: event.target.value })
                      }
                    />
                  </GateField>
                  <GateField className="sm:col-span-2" label="Tools">
                    <Select
                      value={override.tools ?? "none"}
                      onValueChange={(tools) =>
                        patchAction({ tools: tools as "none" | "inherit" })
                      }
                    >
                      <SelectTrigger aria-label="Tools">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="none">None</SelectItem>
                      </SelectContent>
                    </Select>
                  </GateField>
                </div>
              )}

              {override?.type === "deterministic" && (
                <GateField label="Field expressions (JSON)">
                  <Textarea
                    aria-label="Deterministic field expressions"
                    className="min-h-40 font-mono text-xs"
                    value={deterministicFields}
                    aria-invalid={Boolean(deterministicError)}
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
            Changes remain a draft until the Gate configuration is saved.
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
  catalogEntry: PRLifecycleGateConfigSnapshot["gateCatalog"][string]
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
  if (action.type === "ai")
    return action.agentID ? `AI · ${action.agentID}` : "AI"
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
    { id: "deferred", label: "Deferred issues" },
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
      className="bg-muted/40 grid gap-1 rounded-lg border p-1 sm:grid-cols-3"
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
  config: PRLifecycleGateConfigSnapshot
  onChange: (update: (config: PRLifecycleGateConfigSnapshot) => void) => void
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
            {tab === "nudging"
              ? "Nudging"
              : tab === "scope"
                ? "Scope grades"
                : "Deferred issues"}
          </CardTitle>
          <CardDescription>
            {tab === "nudging"
              ? "Control additional AI attempts after an apparently complete review or implementation."
              : tab === "scope"
                ? "Define file, semantic-line, and module boundaries for PR scope grades."
                : "Choose whether deferred findings can become GitHub issues."}
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
          {tab === "deferred" && (
            <GateField label="Deferred issue mode">
              <Select
                value={config.deferredIssues.mode}
                onValueChange={(value) =>
                  onChange(
                    (next) =>
                      void (next.deferredIssues.mode = value as
                        | "off"
                        | "ask"
                        | "automatic"),
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
            </GateField>
          )}
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

function GateConfigIssues({
  error,
  issues,
}: {
  error: string
  issues: PRLifecycleGateConfigIssue[]
}) {
  return (
    <div
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
      role="alert"
    >
      {error ||
        `${issues.length} Gate configuration ${issues.length === 1 ? "issue" : "issues"}.`}
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
          Saved Gate configurations will apply to future executions after the
          gateway restarts.
        </p>
      </div>
    </div>
  )
}

function GateConfigsState({
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
