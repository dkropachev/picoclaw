import {
  IconAlertTriangle,
  IconArrowDown,
  IconArrowLeft,
  IconArrowUp,
  IconDeviceFloppy,
  IconPencil,
  IconPlus,
  IconRefresh,
  IconSettingsAutomation,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type PRLifecycleDecisionPoint,
  type PRLifecycleGateKind,
  type PRLifecycleGateProfile,
  type PRLifecycleGateProfileIssue,
  type PRLifecycleGateProfileSnapshot,
  type PRLifecycleGatePurpose,
  type PRLifecycleGateStage,
  type PRLifecycleGateWorkflow,
  createPRLifecycleGateStage,
  getPRLifecycleGateProfiles,
  isPRLifecycleGateProfileID,
  putPRLifecycleGateProfiles,
  validatePRLifecycleGateProfiles,
} from "@/api/pr-lifecycle-gate-profiles"
import { createPRWorkspaceRequestID } from "@/api/pr-workspaces"
import { PageHeader } from "@/components/page-header"
import { prLifecycleGateDecisionLabels } from "@/components/pr-workspaces/pr-lifecycle-gate-catalog"
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

const profileQueryKey = ["pr-lifecycle", "gate-profiles"] as const
const gateKinds: PRLifecycleGateKind[] = [
  "deterministic",
  "ai_working_context",
  "ai_isolated_context",
  "human",
  "zero",
]

export function PRLifecycleGateProfilesPage({
  onBack,
  initialProfileID,
  initialDecisionPoint,
  onProfileChange,
  onDecisionPointChange,
}: {
  onBack: () => void
  initialProfileID?: string
  initialDecisionPoint?: PRLifecycleDecisionPoint
  onProfileChange?: (profileID?: string) => void
  onDecisionPointChange?: (decisionPoint?: PRLifecycleDecisionPoint) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<PRLifecycleGateProfileSnapshot | null>(
    null,
  )
  const [baseline, setBaseline] = useState("")
  const [localProfileID, setLocalProfileID] = useState(
    initialProfileID ?? (initialDecisionPoint ? "default" : ""),
  )
  const [localDecisionPoint, setLocalDecisionPoint] =
    useState<PRLifecycleDecisionPoint | null>(initialDecisionPoint ?? null)
  const [newProfileID, setNewProfileID] = useState("")
  const [newProfileName, setNewProfileName] = useState("")
  const [newRepository, setNewRepository] = useState("")
  const [newStageKind, setNewStageKind] = useState<PRLifecycleGateKind>("human")
  const [error, setError] = useState("")
  const [discardOpen, setDiscardOpen] = useState(false)
  const lastOpenedDecisionPoint = useRef<PRLifecycleDecisionPoint | null>(
    initialDecisionPoint ?? null,
  )
  const selectedProfileID = onProfileChange
    ? (initialProfileID ?? (initialDecisionPoint ? "default" : ""))
    : localProfileID
  const selectedDecisionPoint = onDecisionPointChange
    ? (initialDecisionPoint ?? null)
    : localDecisionPoint
  const query = useQuery({
    queryKey: profileQueryKey,
    queryFn: ({ signal }) => getPRLifecycleGateProfiles(signal),
    retry: false,
  })
  useEffect(() => {
    if (!query.data || draft) return
    const next = structuredClone(query.data)
    setDraft(next)
    setBaseline(JSON.stringify(next))
  }, [draft, query.data])
  const dirty = draft != null && JSON.stringify(draft) !== baseline
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty])
  useEffect(() => {
    if (initialDecisionPoint) {
      lastOpenedDecisionPoint.current = initialDecisionPoint
    }
  }, [initialDecisionPoint])
  useEffect(() => {
    if (
      !draft ||
      !selectedProfileID ||
      draft.gate_profiles[selectedProfileID]
    ) {
      return
    }
    setLocalProfileID("")
    setLocalDecisionPoint(null)
    onProfileChange?.()
  }, [draft, onProfileChange, selectedProfileID])
  const issues = useMemo(
    () => (draft ? validatePRLifecycleGateProfiles(draft) : []),
    [draft],
  )
  const saveMutation = useMutation({
    mutationFn: (value: PRLifecycleGateProfileSnapshot) =>
      putPRLifecycleGateProfiles({
        expected_config_revision: value.config_revision,
        request_id: createPRWorkspaceRequestID(),
        gate_profiles: value.gate_profiles,
        default_gate_profile_id: value.default_gate_profile_id,
        repository_assignments: value.repository_assignments,
        nudge: value.nudge,
        scope: value.scope,
        deferred_issues: value.deferred_issues,
      }),
    onSuccess: (next, submitted) => {
      const saved = structuredClone(next)
      const submittedSnapshot = JSON.stringify(submitted)
      setDraft((current) => {
        if (!current || JSON.stringify(current) === submittedSnapshot) {
          return saved
        }
        return {
          ...current,
          catalog_revision: saved.catalog_revision,
          config_revision: saved.config_revision,
          effects: structuredClone(saved.effects),
        }
      })
      setBaseline(JSON.stringify(saved))
      setError("")
      queryClient.setQueryData(profileQueryKey, next)
    },
    onError: (failure) =>
      setError(
        failure instanceof Error
          ? failure.message
          : t("prWorkspaces.gateProfiles.saveError"),
      ),
  })

  if (query.isPending) {
    return <GateProfilesState text={t("prWorkspaces.gateProfiles.loading")} />
  }
  if (query.isError) {
    return (
      <GateProfilesState
        text={t("prWorkspaces.gateProfiles.loadError")}
        action={
          <Button onClick={() => void query.refetch()}>
            {t("prWorkspaces.common.retry")}
          </Button>
        }
      />
    )
  }
  if (!draft) {
    return <GateProfilesState text={t("prWorkspaces.gateProfiles.loading")} />
  }
  const selectedProfile = draft.gate_profiles[selectedProfileID]
  const workflow =
    selectedProfile && selectedDecisionPoint
      ? selectedProfile.workflows[selectedDecisionPoint]
      : undefined
  const selectProfile = (profileID?: string) => {
    setLocalProfileID(profileID ?? "")
    setLocalDecisionPoint(null)
    onProfileChange?.(profileID)
  }
  const selectDecisionPoint = (decisionPoint: PRLifecycleDecisionPoint) => {
    lastOpenedDecisionPoint.current = decisionPoint
    setLocalDecisionPoint(decisionPoint)
    onDecisionPointChange?.(decisionPoint)
  }
  const closeDecisionPoint = () => {
    setLocalDecisionPoint(null)
    onDecisionPointChange?.()
  }

  const changeProfile = (update: (profile: PRLifecycleGateProfile) => void) => {
    setDraft((current) => {
      if (!current) return current
      const next = structuredClone(current)
      const profile = next.gate_profiles[selectedProfileID]
      if (profile) update(profile)
      return next
    })
  }
  const changeConfig = (
    update: (config: PRLifecycleGateProfileSnapshot) => void,
  ) => {
    setDraft((current) => {
      if (!current) return current
      const next = structuredClone(current)
      update(next)
      return next
    })
  }
  const changeWorkflow = (
    update: (workflow: PRLifecycleGateWorkflow) => void,
  ) => {
    if (!selectedDecisionPoint) return
    changeProfile((profile) => {
      const current = profile.workflows[selectedDecisionPoint]
      if (current) update(current)
    })
  }
  const addProfile = (event: FormEvent) => {
    event.preventDefault()
    const id = newProfileID
    const name = newProfileName.trim()
    if (!isPRLifecycleGateProfileID(id) || !name || draft.gate_profiles[id]) {
      return
    }
    setDraft((current) => {
      if (!current) return current
      return {
        ...current,
        gate_profiles: {
          ...current.gate_profiles,
          [id]: {
            name,
            workflows: {},
          },
        },
      }
    })
    selectProfile(id)
    setNewProfileID("")
    setNewProfileName("")
  }
  const addDecisionPoint = () => {
    if (!selectedDecisionPoint) return
    changeProfile((profile) => {
      if (profile.workflows[selectedDecisionPoint]) return
      profile.workflows[selectedDecisionPoint] = {
        id: workflowID(selectedDecisionPoint),
        name: t(
          `prWorkspaces.gateProfiles.decisions.${selectedDecisionPoint}`,
          {
            defaultValue: selectedDecisionPoint,
          },
        ),
        purpose:
          selectedDecisionPoint === "pr.finding.classify"
            ? "classification"
            : "authorization",
        decision_point: selectedDecisionPoint,
        stages: [],
      }
    })
  }
  const addStage = () => {
    changeWorkflow((current) => {
      const id = nextStageID(current.stages)
      current.stages.push(createPRLifecycleGateStage(newStageKind, id))
    })
  }
  const requestBack = () => {
    if (selectedProfile) {
      selectProfile()
      return
    }
    if (dirty) {
      setDiscardOpen(true)
      return
    }
    onBack()
  }
  const discardAndBack = () => {
    setDiscardOpen(false)
    onBack()
  }

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-gate-profiles"
      data-profile-view={selectedProfile ? "editor" : "list"}
      aria-busy={saveMutation.isPending}
    >
      <PageHeader
        title={
          selectedProfile
            ? t("prWorkspaces.gateProfiles.editProfileTitle", {
                name: selectedProfile.name,
              })
            : t("prWorkspaces.gateProfiles.title")
        }
        titleExtra={<Badge variant="outline">v3</Badge>}
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(
            selectedProfile
              ? "prWorkspaces.gateProfiles.backToProfiles"
              : "prWorkspaces.gateProfiles.back",
          )}
          title={t(
            selectedProfile
              ? "prWorkspaces.gateProfiles.backToProfiles"
              : "prWorkspaces.gateProfiles.back",
          )}
          onClick={requestBack}
        >
          <IconArrowLeft />
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("prWorkspaces.portfolio.refresh")}
          title={t("prWorkspaces.portfolio.refresh")}
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
          {t("prWorkspaces.gateProfiles.save")}
        </Button>
      </PageHeader>
      <div className="min-h-0 flex-1 overflow-auto px-4 pb-8 md:px-6">
        <div className="mx-auto grid w-full max-w-[96rem] min-w-0 gap-4 xl:grid-cols-[18rem_minmax(0,1fr)]">
          {selectedProfile && (
            <PRLifecycleGateMap
              className="xl:col-span-2"
              selectedDecisionPoint={selectedDecisionPoint ?? undefined}
              workflows={selectedProfile.workflows}
              profileName={selectedProfile.name}
              profileID={selectedProfileID}
              onSelect={selectDecisionPoint}
            />
          )}
          <aside
            className={cn(
              "min-w-0 space-y-4",
              !selectedProfile && "xl:col-span-2",
            )}
          >
            {!selectedProfile && (
              <Card size="sm">
                <CardHeader>
                  <CardTitle role="heading" aria-level={2}>
                    {t("prWorkspaces.gateProfiles.profiles")}
                  </CardTitle>
                  <CardDescription>
                    {t("prWorkspaces.gateProfiles.profilesHelp")}
                  </CardDescription>
                </CardHeader>
                <CardContent className="space-y-4">
                  <div
                    className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3"
                    aria-label={t("prWorkspaces.gateProfiles.profiles")}
                  >
                    {Object.entries(draft.gate_profiles).map(
                      ([id, profile]) => {
                        const configuredGateCount = Object.keys(
                          profile.workflows,
                        ).length
                        const assignmentCount = Object.values(
                          draft.repository_assignments,
                        ).filter((profileID) => profileID === id).length
                        return (
                          <div
                            key={id}
                            className="border-border bg-muted/15 flex min-w-0 flex-col gap-3 rounded-lg border p-3"
                            data-profile-id={id}
                          >
                            <div className="min-w-0">
                              <div className="flex flex-wrap items-center gap-2">
                                <strong className="truncate text-sm">
                                  {profile.name}
                                </strong>
                                {id === draft.default_gate_profile_id && (
                                  <Badge variant="secondary">
                                    {t(
                                      "prWorkspaces.gateProfiles.defaultProfile",
                                    )}
                                  </Badge>
                                )}
                              </div>
                              <p className="text-muted-foreground mt-1 truncate font-mono text-xs">
                                {id}
                              </p>
                            </div>
                            <p className="text-muted-foreground text-xs">
                              {t(
                                "prWorkspaces.gateProfiles.configuredGateCount",
                                { count: configuredGateCount },
                              )}{" "}
                              ·{" "}
                              {t("prWorkspaces.gateProfiles.repositoryCount", {
                                count: assignmentCount,
                              })}
                            </p>
                            <div className="mt-auto flex flex-wrap gap-2">
                              <Button
                                size="sm"
                                onClick={() => selectProfile(id)}
                                aria-label={t(
                                  "prWorkspaces.gateProfiles.editProfileNamed",
                                  { name: profile.name },
                                )}
                              >
                                <IconPencil />
                                {t("prWorkspaces.gateProfiles.editProfile")}
                              </Button>
                              {id !== draft.default_gate_profile_id && (
                                <Button
                                  size="sm"
                                  variant="outline"
                                  onClick={() =>
                                    setDraft((current) =>
                                      current
                                        ? {
                                            ...current,
                                            default_gate_profile_id: id,
                                          }
                                        : current,
                                    )
                                  }
                                >
                                  {t("prWorkspaces.gateProfiles.makeDefault")}
                                </Button>
                              )}
                            </div>
                          </div>
                        )
                      },
                    )}
                  </div>
                  <form
                    onSubmit={addProfile}
                    className="border-border grid gap-2 border-t pt-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto]"
                  >
                    <Input
                      value={newProfileID}
                      onChange={(event) => setNewProfileID(event.target.value)}
                      placeholder={t("prWorkspaces.gateProfiles.profileID")}
                      aria-label={t("prWorkspaces.gateProfiles.profileID")}
                      aria-invalid={
                        newProfileID.length > 0 &&
                        (!isPRLifecycleGateProfileID(newProfileID) ||
                          Boolean(draft.gate_profiles[newProfileID]))
                      }
                      pattern="[a-z][a-z0-9_-]{0,63}"
                    />
                    <Input
                      value={newProfileName}
                      onChange={(event) =>
                        setNewProfileName(event.target.value)
                      }
                      placeholder={t("prWorkspaces.gateProfiles.profileName")}
                      aria-label={t("prWorkspaces.gateProfiles.profileName")}
                    />
                    <Button
                      type="submit"
                      className="w-full sm:w-auto"
                      size="sm"
                      variant="outline"
                      disabled={
                        !isPRLifecycleGateProfileID(newProfileID) ||
                        !newProfileName.trim() ||
                        Boolean(draft.gate_profiles[newProfileID])
                      }
                    >
                      <IconPlus />
                      {t("prWorkspaces.gateProfiles.addProfile")}
                    </Button>
                    <p className="text-muted-foreground text-xs sm:col-span-3">
                      {draft.gate_profiles[newProfileID]
                        ? t("prWorkspaces.gateProfiles.duplicateProfileID")
                        : t("prWorkspaces.gateProfiles.profileIDHelp")}
                    </p>
                  </form>
                </CardContent>
              </Card>
            )}

            {selectedProfile && (
              <Card size="sm">
                <CardHeader>
                  <CardTitle role="heading" aria-level={2}>
                    {t("prWorkspaces.gateProfiles.profileSettings")}
                  </CardTitle>
                  <CardDescription className="font-mono [overflow-wrap:anywhere]">
                    {selectedProfileID}
                  </CardDescription>
                </CardHeader>
                <CardContent className="space-y-3">
                  <GateField label={t("prWorkspaces.gateProfiles.profileName")}>
                    <Input
                      value={selectedProfile.name}
                      disabled={selectedProfileID === "default"}
                      aria-label={t("prWorkspaces.gateProfiles.profileName")}
                      onChange={(event) =>
                        changeProfile((profile) => {
                          profile.name = event.target.value
                        })
                      }
                    />
                  </GateField>
                  {selectedProfileID === draft.default_gate_profile_id ? (
                    <Badge variant="secondary">
                      {t("prWorkspaces.gateProfiles.defaultProfile")}
                    </Badge>
                  ) : (
                    <Button
                      className="w-full"
                      size="sm"
                      variant="outline"
                      onClick={() =>
                        setDraft((current) =>
                          current
                            ? {
                                ...current,
                                default_gate_profile_id: selectedProfileID,
                              }
                            : current,
                        )
                      }
                    >
                      {t("prWorkspaces.gateProfiles.makeDefault")}
                    </Button>
                  )}
                  <div className="border-border border-t pt-3">
                    <h3 className="mb-2 text-xs font-semibold">
                      {t("prWorkspaces.gateProfiles.assignment")}
                    </h3>
                    <form
                      className="flex gap-2"
                      onSubmit={(event) => {
                        event.preventDefault()
                        const repository = newRepository.trim()
                        if (!repository) return
                        setDraft((current) =>
                          current
                            ? {
                                ...current,
                                repository_assignments: {
                                  ...current.repository_assignments,
                                  [repository]: selectedProfileID,
                                },
                              }
                            : current,
                        )
                        setNewRepository("")
                      }}
                    >
                      <Input
                        value={newRepository}
                        onChange={(event) =>
                          setNewRepository(event.target.value)
                        }
                        placeholder="https://github.com|repository-id"
                        aria-label={t("prWorkspaces.gateProfiles.assignment")}
                      />
                      <Button
                        type="submit"
                        size="icon"
                        variant="outline"
                        aria-label={t(
                          "prWorkspaces.gateProfiles.addAssignment",
                        )}
                        title={t("prWorkspaces.gateProfiles.addAssignment")}
                      >
                        <IconPlus />
                      </Button>
                    </form>
                    {Object.entries(draft.repository_assignments)
                      .filter(
                        ([, profileID]) => profileID === selectedProfileID,
                      )
                      .map(([repository]) => (
                        <div
                          key={repository}
                          className="flex min-w-0 items-center gap-1 text-xs"
                        >
                          <span className="min-w-0 flex-1 truncate">
                            {repository}
                          </span>
                          <Button
                            size="icon"
                            variant="ghost"
                            className="size-7"
                            aria-label={t(
                              "prWorkspaces.gateProfiles.removeAssignment",
                              { repository },
                            )}
                            onClick={() =>
                              setDraft((current) => {
                                if (!current) return current
                                const next = structuredClone(current)
                                delete next.repository_assignments[repository]
                                return next
                              })
                            }
                          >
                            <IconTrash />
                          </Button>
                        </div>
                      ))}
                  </div>
                </CardContent>
              </Card>
            )}
          </aside>

          {!selectedProfile && (
            <div className="min-w-0 space-y-4 xl:col-span-2">
              {query.data?.effects.gateway_effect === "restart_required" && (
                <GateProfileRestartNotice />
              )}
              {(error || issues.length > 0) && (
                <GateProfileIssues error={error} issues={issues} />
              )}
              <ProfileTuning config={draft} onChange={changeConfig} />
            </div>
          )}

          {selectedProfile && (
            <div className="min-w-0 space-y-4">
              <Card size="sm">
                <CardHeader>
                  <CardTitle role="heading" aria-level={2}>
                    {t("prWorkspaces.gateProfiles.gateWorkflows")}
                  </CardTitle>
                  <CardDescription>
                    {t("prWorkspaces.gateProfiles.gateWorkflowsHelp")}
                  </CardDescription>
                </CardHeader>
              </Card>
              {query.data?.effects.gateway_effect === "restart_required" && (
                <GateProfileRestartNotice />
              )}
              {(error || issues.length > 0) && (
                <GateProfileIssues error={error} issues={issues} />
              )}
              <Dialog
                open={selectedDecisionPoint != null}
                onOpenChange={(open) => {
                  if (!open) closeDecisionPoint()
                }}
              >
                <DialogContent
                  className="flex max-h-[calc(100dvh-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl lg:max-w-4xl"
                  onCloseAutoFocus={(event) => {
                    event.preventDefault()
                    const decisionPoint = lastOpenedDecisionPoint.current
                    if (!decisionPoint) return
                    window.requestAnimationFrame(() => {
                      document
                        .querySelector<HTMLButtonElement>(
                          `button[data-gate-id="${decisionPoint}"]`,
                        )
                        ?.focus({ preventScroll: true })
                    })
                  }}
                >
                  <DialogHeader className="border-border border-b px-5 py-4 pr-14">
                    <DialogTitle>
                      {selectedDecisionPoint
                        ? prLifecycleGateDecisionLabels[selectedDecisionPoint]
                        : t("prWorkspaces.gateProfiles.workflow")}
                    </DialogTitle>
                    <DialogDescription>
                      {selectedProfile.name} ·{" "}
                      {t("prWorkspaces.gateProfiles.workflowHelp")}
                    </DialogDescription>
                  </DialogHeader>
                  <div
                    id="pr-gate-workflow-editor"
                    data-decision-point={selectedDecisionPoint ?? undefined}
                    className="min-h-0 min-w-0 flex-1 space-y-3 overflow-y-auto px-5 py-4"
                  >
                    {(error || issues.length > 0) && (
                      <GateProfileIssues error={error} issues={issues} />
                    )}
                    {!workflow && (
                      <Button onClick={addDecisionPoint}>
                        <IconPlus />
                        {t("prWorkspaces.gateProfiles.addWorkflow")}
                      </Button>
                    )}
                    {workflow ? (
                      <>
                        <div className="grid min-w-0 gap-3 sm:grid-cols-2">
                          <GateField
                            label={t("prWorkspaces.gateProfiles.workflowName")}
                          >
                            <Input
                              value={workflow.name}
                              aria-label={t(
                                "prWorkspaces.gateProfiles.workflowName",
                              )}
                              onChange={(event) =>
                                changeWorkflow((current) => {
                                  current.name = event.target.value
                                })
                              }
                            />
                          </GateField>
                          <GateField
                            label={t("prWorkspaces.gateProfiles.purpose")}
                          >
                            <Select
                              value={workflow.purpose}
                              onValueChange={(value) =>
                                changeWorkflow((current) => {
                                  current.purpose =
                                    value as PRLifecycleGatePurpose
                                })
                              }
                            >
                              <SelectTrigger
                                className="w-full"
                                aria-label={t(
                                  "prWorkspaces.gateProfiles.purpose",
                                )}
                              >
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {(
                                  [
                                    "attention",
                                    "authorization",
                                    "classification",
                                  ] as const
                                ).map((purpose) => (
                                  <SelectItem key={purpose} value={purpose}>
                                    {t(
                                      `prWorkspaces.gateProfiles.purposes.${purpose}`,
                                    )}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </GateField>
                        </div>
                        <div className="flex min-w-0 flex-col items-stretch gap-2 border-t pt-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
                          <div
                            className="flex w-full min-w-0 flex-none flex-col gap-2 sm:w-auto sm:flex-1 sm:flex-row"
                            data-testid="pr-gate-stage-controls"
                          >
                            <Select
                              value={newStageKind}
                              onValueChange={(value) =>
                                setNewStageKind(value as PRLifecycleGateKind)
                              }
                            >
                              <SelectTrigger
                                className="w-full min-w-0 sm:min-w-44"
                                aria-label={t(
                                  "prWorkspaces.gateProfiles.addStage",
                                )}
                              >
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {gateKinds.map((kind) => (
                                  <SelectItem key={kind} value={kind}>
                                    {t(
                                      `prWorkspaces.gateProfiles.kinds.${kind}`,
                                    )}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                            <Button
                              className="w-full sm:w-auto"
                              variant="outline"
                              onClick={addStage}
                            >
                              <IconPlus />
                              {t("prWorkspaces.gateProfiles.addStage")}
                            </Button>
                          </div>
                          <Button
                            variant="ghost"
                            className="text-destructive w-full sm:w-auto"
                            onClick={() =>
                              changeProfile((profile) => {
                                if (selectedDecisionPoint) {
                                  delete profile.workflows[
                                    selectedDecisionPoint
                                  ]
                                }
                              })
                            }
                          >
                            <IconTrash />
                            {t("prWorkspaces.gateProfiles.removeWorkflow")}
                          </Button>
                        </div>
                        <div className="min-w-0 space-y-2">
                          {workflow.stages.length === 0 ? (
                            <p className="text-muted-foreground py-6 text-center text-sm">
                              {t("prWorkspaces.gateProfiles.noStages")}
                            </p>
                          ) : (
                            workflow.stages.map((stage, index) => (
                              <GateStageEditor
                                key={stage.id}
                                stage={stage}
                                index={index}
                                count={workflow.stages.length}
                                onChange={(next) =>
                                  changeWorkflow((current) => {
                                    current.stages[index] = next
                                  })
                                }
                                onMove={(offset) =>
                                  changeWorkflow((current) => {
                                    const target = index + offset
                                    if (
                                      target < 0 ||
                                      target >= current.stages.length
                                    )
                                      return
                                    ;[
                                      current.stages[index],
                                      current.stages[target],
                                    ] = [
                                      current.stages[target],
                                      current.stages[index],
                                    ]
                                  })
                                }
                                onRemove={() =>
                                  changeWorkflow((current) => {
                                    current.stages.splice(index, 1)
                                  })
                                }
                              />
                            ))
                          )}
                        </div>
                      </>
                    ) : (
                      <p className="text-muted-foreground py-10 text-center text-sm">
                        {t("prWorkspaces.gateProfiles.workflowOff")}
                      </p>
                    )}
                  </div>
                  <DialogFooter className="border-border border-t px-5 py-3">
                    <DialogClose asChild>
                      <Button variant="outline">
                        {t("prWorkspaces.gateProfiles.done")}
                      </Button>
                    </DialogClose>
                    <Button
                      disabled={
                        !dirty || issues.length > 0 || saveMutation.isPending
                      }
                      onClick={() => saveMutation.mutate(draft)}
                    >
                      <IconDeviceFloppy />
                      {t("prWorkspaces.gateProfiles.save")}
                    </Button>
                  </DialogFooter>
                </DialogContent>
              </Dialog>
            </div>
          )}
        </div>
      </div>
      <AlertDialog open={discardOpen} onOpenChange={setDiscardOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("prWorkspaces.gateProfiles.discardTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("prWorkspaces.gateProfiles.discardDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("prWorkspaces.gateProfiles.keepEditing")}
            </AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={discardAndBack}>
              {t("prWorkspaces.gateProfiles.discardChanges")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function GateProfileRestartNotice() {
  const { t } = useTranslation()
  return (
    <div
      className="border-border bg-muted/40 flex items-start gap-2 rounded-lg border p-3 text-sm"
      role="status"
      aria-live="polite"
    >
      <IconAlertTriangle
        className="text-muted-foreground mt-0.5 size-4 shrink-0"
        aria-hidden
      />
      <div>
        <strong>{t("prWorkspaces.gateProfiles.restartRequiredTitle")}</strong>
        <p className="text-muted-foreground mt-0.5 text-xs">
          {t("prWorkspaces.gateProfiles.restartRequiredDescription")}
        </p>
      </div>
    </div>
  )
}

function GateProfileIssues({
  error,
  issues,
}: {
  error: string
  issues: PRLifecycleGateProfileIssue[]
}) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      className="border-destructive/40 bg-destructive/5 text-destructive rounded-lg border p-3 text-sm"
    >
      {error ||
        t("prWorkspaces.gateProfiles.issues", {
          count: issues.length,
        })}
      {issues.length > 0 && (
        <ul className="mt-1 list-disc pl-5">
          {issues.slice(0, 5).map((issue) => (
            <li key={`${issue.path}:${issue.message}`}>{issue.message}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ProfileTuning({
  config,
  onChange,
}: {
  config: PRLifecycleGateProfileSnapshot
  onChange: (update: (config: PRLifecycleGateProfileSnapshot) => void) => void
}) {
  const { t } = useTranslation()
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          {t("prWorkspaces.gateProfiles.tuning")}
        </CardTitle>
        <CardDescription>
          {t("prWorkspaces.gateProfiles.tuningHelp")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-3 lg:grid-cols-4">
        <div className="space-y-1.5">
          <Label>{t("prWorkspaces.gateProfiles.deferredIssueMode")}</Label>
          <Select
            value={config.deferred_issues.mode}
            onValueChange={(value) =>
              onChange((current) => {
                current.deferred_issues.mode = value as
                  | "off"
                  | "ask"
                  | "automatic"
              })
            }
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("prWorkspaces.gateProfiles.deferredIssueMode")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["off", "ask", "automatic"] as const).map((mode) => (
                <SelectItem key={mode} value={mode}>
                  {t(`prWorkspaces.gateProfiles.deferredIssueModes.${mode}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <NumberField
          label={t("prWorkspaces.gateProfiles.reviewNudgeMinimum")}
          value={config.nudge.review_minimum_additional}
          onChange={(value) =>
            onChange((current) => {
              current.nudge.review_minimum_additional = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.reviewNudgeMaximum")}
          value={config.nudge.review_maximum_additional}
          onChange={(value) =>
            onChange((current) => {
              current.nudge.review_maximum_additional = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.completionNudgeMinimum")}
          value={config.nudge.completion_minimum_additional}
          onChange={(value) =>
            onChange((current) => {
              current.nudge.completion_minimum_additional = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.completionNudgeMaximum")}
          value={config.nudge.completion_maximum_additional}
          onChange={(value) =>
            onChange((current) => {
              current.nudge.completion_maximum_additional = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.xsFiles")}
          value={config.scope.xs.files}
          onChange={(value) =>
            onChange((current) => {
              current.scope.xs.files = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.xsLines")}
          value={config.scope.xs.semantic_lines}
          onChange={(value) =>
            onChange((current) => {
              current.scope.xs.semantic_lines = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.xsModules")}
          value={config.scope.xs.modules}
          onChange={(value) =>
            onChange((current) => {
              current.scope.xs.modules = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.sFiles")}
          value={config.scope.s.files}
          onChange={(value) =>
            onChange((current) => {
              current.scope.s.files = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.sLines")}
          value={config.scope.s.semantic_lines}
          onChange={(value) =>
            onChange((current) => {
              current.scope.s.semantic_lines = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.sModules")}
          value={config.scope.s.modules}
          onChange={(value) =>
            onChange((current) => {
              current.scope.s.modules = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.mFiles")}
          value={config.scope.m.files}
          onChange={(value) =>
            onChange((current) => {
              current.scope.m.files = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.mLines")}
          value={config.scope.m.semantic_lines}
          onChange={(value) =>
            onChange((current) => {
              current.scope.m.semantic_lines = value
            })
          }
        />
        <NumberField
          label={t("prWorkspaces.gateProfiles.mModules")}
          value={config.scope.m.modules}
          onChange={(value) =>
            onChange((current) => {
              current.scope.m.modules = value
            })
          }
        />
      </CardContent>
    </Card>
  )
}

function GateStageEditor({
  stage,
  index,
  count,
  onChange,
  onMove,
  onRemove,
}: {
  stage: PRLifecycleGateStage
  index: number
  count: number
  onChange: (stage: PRLifecycleGateStage) => void
  onMove: (offset: number) => void
  onRemove: () => void
}) {
  const { t } = useTranslation()
  const patch = (update: Partial<PRLifecycleGateStage>) =>
    onChange({ ...stage, ...update })
  const changeKind = (kind: PRLifecycleGateKind) => {
    const next = createPRLifecycleGateStage(kind, stage.id)
    onChange({
      ...next,
      ...(kind === "zero" ? {} : { title: stage.title ?? "" }),
    })
  }
  return (
    <article
      className="border-border max-w-full min-w-0 rounded-md border p-3"
      data-testid="pr-gate-stage-editor"
    >
      <div className="flex max-w-full min-w-0 flex-wrap items-center gap-2">
        <Badge variant="secondary">{index + 1}</Badge>
        <Input
          className="min-w-0 flex-1 basis-36"
          value={stage.id}
          onChange={(event) => patch({ id: event.target.value })}
          aria-label={t("prWorkspaces.gateProfiles.stageID")}
        />
        <Select
          value={stage.kind}
          onValueChange={(value) => changeKind(value as PRLifecycleGateKind)}
        >
          <SelectTrigger
            size="sm"
            className="max-w-full min-w-0"
            aria-label={t("prWorkspaces.gateProfiles.stageKind")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {gateKinds.map((kind) => (
              <SelectItem key={kind} value={kind}>
                {t(`prWorkspaces.gateProfiles.kinds.${kind}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          size="icon"
          variant="ghost"
          className="size-8"
          disabled={index === 0}
          onClick={() => onMove(-1)}
          aria-label={t("prWorkspaces.gateProfiles.moveUp")}
        >
          <IconArrowUp />
        </Button>
        <Button
          size="icon"
          variant="ghost"
          className="size-8"
          disabled={index === count - 1}
          onClick={() => onMove(1)}
          aria-label={t("prWorkspaces.gateProfiles.moveDown")}
        >
          <IconArrowDown />
        </Button>
        <Button
          size="icon"
          variant="ghost"
          className="text-destructive size-8"
          onClick={onRemove}
          aria-label={t("prWorkspaces.gateProfiles.removeStage")}
        >
          <IconTrash />
        </Button>
      </div>
      {stage.kind !== "zero" && (
        <div className="mt-3 grid max-w-full min-w-0 gap-3 md:grid-cols-2">
          <GateField label={t("prWorkspaces.gateProfiles.stageTitle")}>
            <Input
              className="max-w-full min-w-0"
              value={stage.title ?? ""}
              aria-label={t("prWorkspaces.gateProfiles.stageTitle")}
              onChange={(event) => patch({ title: event.target.value })}
            />
          </GateField>
          {stage.kind === "deterministic" && (
            <GateField
              label={t("prWorkspaces.gateProfiles.condition")}
              className="md:col-span-2"
            >
              <Textarea
                className="max-w-full min-w-0"
                value={stage.when ?? ""}
                aria-label={t("prWorkspaces.gateProfiles.condition")}
                onChange={(event) => patch({ when: event.target.value })}
              />
            </GateField>
          )}
          {(stage.kind === "ai_working_context" ||
            stage.kind === "ai_isolated_context") && (
            <>
              <GateField label={t("prWorkspaces.gateProfiles.agent")}>
                <Input
                  className="max-w-full min-w-0"
                  value={stage.agent_id ?? ""}
                  aria-label={t("prWorkspaces.gateProfiles.agent")}
                  onChange={(event) => patch({ agent_id: event.target.value })}
                />
              </GateField>
              <GateField
                label={t("prWorkspaces.gateProfiles.criteria")}
                className="md:col-span-2"
              >
                <Textarea
                  className="max-w-full min-w-0"
                  value={stage.criteria ?? ""}
                  aria-label={t("prWorkspaces.gateProfiles.criteria")}
                  onChange={(event) => patch({ criteria: event.target.value })}
                />
              </GateField>
            </>
          )}
          {stage.kind === "human" && (
            <GateField
              label={t("prWorkspaces.gateProfiles.question")}
              className="md:col-span-2"
            >
              <Textarea
                className="max-w-full min-w-0"
                value={firstQuestion(stage.questions)}
                aria-label={t("prWorkspaces.gateProfiles.question")}
                onChange={(event) => patch({ questions: [event.target.value] })}
              />
            </GateField>
          )}
        </div>
      )}
      {stage.kind === "zero" && (
        <p className="text-muted-foreground mt-2 text-xs">
          {t("prWorkspaces.gateProfiles.zeroHelp")}
        </p>
      )}
    </article>
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
        type="number"
        aria-label={label}
        min={0}
        max={10000}
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

function GateProfilesState({
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

function workflowID(decisionPoint: string): string {
  return `workflow_${decisionPoint.replaceAll(".", "_")}`.slice(0, 64)
}

function firstQuestion(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0) return ""
  const first = value[0]
  if (typeof first === "string") return first
  if (
    typeof first === "object" &&
    first !== null &&
    "prompt" in first &&
    typeof first.prompt === "string"
  ) {
    return first.prompt
  }
  return ""
}

function nextStageID(stages: PRLifecycleGateStage[]): string {
  let ordinal = stages.length + 1
  while (stages.some((stage) => stage.id === `stage_${ordinal}`)) ordinal += 1
  return `stage_${ordinal}`
}
