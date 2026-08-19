import {
  IconAlertTriangle,
  IconArrowLeft,
  IconBolt,
  IconBrandGithub,
  IconCheck,
  IconCode,
  IconExternalLink,
  IconGitPullRequest,
  IconHistory,
  IconLoader2,
  IconMessageCircle,
  IconRefresh,
  IconRobot,
  IconRoute,
  IconShieldCheck,
  IconSparkles,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { type FormEvent, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type PRLifecycleRepositoryAssignmentSnapshot,
  getPRLifecycleRepositoryAssignments,
} from "@/api/pr-lifecycle-repository-assignments"
import { respondPRWorkspaceGate } from "@/api/pr-workspace-gates"
import {
  type PRWorkspace,
  PRWorkspaceAPIError,
  type PRWorkspaceCharterInput,
  type PRWorkspaceCorrectionApplicability,
  type PRWorkspaceCorrectionKind,
  type PRWorkspaceDeferredAction,
  type PRWorkspaceFindingDisposition,
  type PRWorkspaceGateField,
  type PRWorkspacePhase,
  type PRWorkspaceScopeAssessment,
  type PRWorkspaceType,
  confirmPRWorkspaceCharter,
  createPRWorkspaceCorrection,
  createPRWorkspaceRequestID,
  draftPRWorkspaceCharter,
  getPRWorkspace,
  mutatePRWorkspaceDeferredGroup,
  promotePRWorkspaceCorrection,
  publishPRWorkspacePhase,
  reconcilePRWorkspacePublication,
  refreshPRWorkspace,
  regroupPRWorkspaceDeferredFindings,
  revisePRWorkspaceCharter,
  savePRWorkspaceCharter,
  sendPRWorkspaceMessage,
  setPRWorkspaceFindingDisposition,
  startPRWorkspaceRun,
  syncPRWorkspaceAutomaticDeferredIssues,
  updatePRWorkspaceDeferredGroup,
} from "@/api/pr-workspaces"
import { PageHeader } from "@/components/page-header"
import {
  activePRWorkspaceCharter,
  canImplementWorkspace,
  findingDispositionCounts,
  isPRWorkspaceValidationGreen,
  latestRepairAttempt,
  latestValidation,
  phaseIndex,
  prWorkspacePhases,
  prWorkspaceScopeDistances,
  scopeMatrixCounts,
} from "@/components/pr-workspaces/pr-workspace-model"
import {
  PhaseBadge,
  ScopeBadge,
  StateBadge,
  TypeBadge,
} from "@/components/pr-workspaces/pr-workspace-status"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
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

const workspaceQueryKey = (workspaceID: string) => ["pr-workspace", workspaceID]

type DeferredCommand =
  | { action: "regroup" }
  | { action: "automatic-sync" }
  | {
      action: "update"
      groupID: string
      title: string
      body: string
      labels: string[]
    }
  | {
      action: Exclude<PRWorkspaceDeferredAction, "merge" | "link">
      groupID: string
      findingIDs?: string[]
      publicationID?: string
    }
  | {
      action: "merge"
      groupID: string
      groupIDs: string[]
      title: string
      body: string
    }
  | {
      action: "link"
      groupID: string
      existingIssueURL: string
    }

type GuidanceStage = "workspace" | "review" | "implementation"

interface GuidanceDraft {
  content: string
  stage: GuidanceStage
  markAsCorrection: boolean
  applicability: PRWorkspaceCorrectionApplicability
}

export function PRWorkspacePage({
  workspaceID,
  onBack,
  onOpenWorkflowConfigurations,
}: {
  workspaceID: string
  onBack: () => void
  onOpenWorkflowConfigurations?: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [actionError, setActionError] = useState("")
  const latestWorkspaceRef = useRef<PRWorkspace | undefined>(undefined)
  const keepLatestWorkspace = (candidate: PRWorkspace) => {
    const latest = latestWorkspaceRef.current
    if (
      latest?.workspace.id === candidate.workspace.id &&
      latest.workspace.version > candidate.workspace.version
    ) {
      return latest
    }
    latestWorkspaceRef.current = candidate
    return candidate
  }
  const query = useQuery({
    queryKey: workspaceQueryKey(workspaceID),
    queryFn: async ({ signal }) =>
      keepLatestWorkspace(await getPRWorkspace(workspaceID, signal)),
    refetchInterval: 3_000,
  })
  const repositoryAssignmentsQuery = useQuery({
    queryKey: ["pr-lifecycle", "repository-assignments"],
    queryFn: ({ signal }) => getPRLifecycleRepositoryAssignments(signal),
    retry: false,
    staleTime: 30_000,
  })
  const updateWorkspace = (workspace: PRWorkspace) => {
    queryClient.setQueryData(
      workspaceQueryKey(workspaceID),
      keepLatestWorkspace(workspace),
    )
    setActionError("")
  }
  const handleError = (error: unknown) => {
    if (error instanceof PRWorkspaceAPIError && error.current) {
      updateWorkspace(error.current)
    }
    setActionError(
      error instanceof Error
        ? error.message
        : t("prWorkspaces.workspace.actionError"),
    )
  }
  const refreshMutation = useMutation({
    mutationFn: (workspace: PRWorkspace) =>
      refreshPRWorkspace(workspaceID, mutationFence(workspace)),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const draftMutation = useMutation({
    mutationFn: (workspace: PRWorkspace) =>
      draftPRWorkspaceCharter(workspaceID, {
        ...mutationFence(workspace),
        expected_head_revision: providerHeadRevision(workspace),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const saveCharterMutation = useMutation({
    mutationFn: ({
      workspace,
      charter,
    }: {
      workspace: PRWorkspace
      charter: CharterDraft
    }) => {
      const input = {
        ...mutationFence(workspace),
        expected_head_revision: providerHeadRevision(workspace),
        ...charterDraftToInput(charter),
      }
      const active = workspace.charters.find(
        (candidate) => candidate.id === workspace.workspace.active_charter_id,
      )
      return active
        ? revisePRWorkspaceCharter(workspaceID, {
            ...input,
            expected_charter_revision: active.revision,
          })
        : savePRWorkspaceCharter(workspaceID, input)
    },
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const confirmMutation = useMutation({
    mutationFn: (workspace: PRWorkspace) => {
      const charter = activePRWorkspaceCharter(workspace)
      if (!charter) throw new Error("charter_missing")
      return confirmPRWorkspaceCharter(workspaceID, {
        ...mutationFence(workspace),
        expected_charter_revision: charter.revision,
      })
    },
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const runMutation = useMutation({
    mutationFn: ({
      workspace,
      kind,
      findingIDs,
      stage,
    }: {
      workspace: PRWorkspace
      kind:
        | "review-runs"
        | "implementation-runs"
        | "completion-audits"
        | "nudge-runs"
      findingIDs?: string[]
      stage?: "review" | "implementation_completion"
    }) =>
      startPRWorkspaceRun(workspaceID, kind, {
        ...mutationFence(workspace),
        expected_head_revision: providerHeadRevision(workspace),
        ...(findingIDs ? { finding_ids: findingIDs } : {}),
        ...(stage ? { stage } : {}),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const findingMutation = useMutation({
    mutationFn: ({
      workspace,
      findingID,
      disposition,
    }: {
      workspace: PRWorkspace
      findingID: string
      disposition: PRWorkspaceFindingDisposition
    }) =>
      setPRWorkspaceFindingDisposition(workspaceID, findingID, {
        ...mutationFence(workspace),
        disposition,
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const correctionMutation = useMutation({
    mutationFn: ({
      workspace,
      draft,
    }: {
      workspace: PRWorkspace
      draft: CorrectionDraft
    }) =>
      createPRWorkspaceCorrection(workspaceID, {
        ...mutationFence(workspace),
        kind: draft.kind,
        applicability: draft.applicability,
        original_claim: draft.originalClaim.trim(),
        correction: draft.correction.trim(),
        ...(draft.reason.trim() ? { reason: draft.reason.trim() } : {}),
        ...(draft.targetID ? { target_id: draft.targetID } : {}),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const messageMutation = useMutation({
    mutationFn: ({
      workspace,
      draft,
    }: {
      workspace: PRWorkspace
      draft: GuidanceDraft
    }) =>
      sendPRWorkspaceMessage(workspaceID, {
        ...mutationFence(workspace),
        content: draft.content.trim(),
        stage: draft.stage,
        ...(draft.markAsCorrection
          ? {
              mark_as_correction: true,
              applicability: draft.applicability,
            }
          : {}),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const promoteMutation = useMutation({
    mutationFn: ({
      workspace,
      correctionID,
    }: {
      workspace: PRWorkspace
      correctionID: string
    }) =>
      promotePRWorkspaceCorrection(
        workspaceID,
        correctionID,
        mutationFence(workspace),
      ),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const deferredMutation = useMutation({
    mutationFn: ({
      workspace,
      command,
    }: {
      workspace: PRWorkspace
      command: DeferredCommand
    }) => {
      const fence = mutationFence(workspace)
      if (command.action === "regroup") {
        return regroupPRWorkspaceDeferredFindings(workspaceID, fence)
      }
      if (command.action === "automatic-sync") {
        return syncPRWorkspaceAutomaticDeferredIssues(workspaceID, fence)
      }
      if (command.action === "update") {
        return updatePRWorkspaceDeferredGroup(workspaceID, command.groupID, {
          ...fence,
          title: command.title,
          body: command.body,
          labels: command.labels,
        })
      }
      const input: ReturnType<typeof mutationFence> & Record<string, unknown> =
        {
          ...fence,
        }
      if (command.action === "split") input.finding_ids = command.findingIDs
      if (command.action === "merge") {
        input.group_ids = command.groupIDs
        input.title = command.title
        input.body = command.body
      }
      if (command.action === "link") {
        input.existing_issue_url = command.existingIssueURL
      }
      if (command.action === "reconcile" && command.publicationID) {
        input.publication_id = command.publicationID
      }
      return mutatePRWorkspaceDeferredGroup(
        workspaceID,
        command.groupID,
        command.action,
        input,
      )
    },
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const publicationMutation = useMutation({
    mutationFn: ({
      workspace,
      phase,
      findingIDs,
    }: {
      workspace: PRWorkspace
      phase: "review" | "implementation"
      findingIDs?: string[]
    }) =>
      publishPRWorkspacePhase(workspaceID, phase, {
        ...mutationFence(workspace),
        expected_head_revision: providerHeadRevision(workspace),
        ...(findingIDs?.length ? { finding_ids: findingIDs } : {}),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const publicationReconcileMutation = useMutation({
    mutationFn: ({
      workspace,
      publicationID,
    }: {
      workspace: PRWorkspace
      publicationID: string
    }) =>
      reconcilePRWorkspacePublication(workspaceID, publicationID, {
        ...mutationFence(workspace),
        expected_head_revision: providerHeadRevision(workspace),
      }),
    onSuccess: updateWorkspace,
    onError: handleError,
  })
  const gateMutation = useMutation({
    mutationFn: ({
      workspace,
      gateID,
      fieldValues,
    }: {
      workspace: PRWorkspace
      gateID: string
      fieldValues: Record<string, unknown>
    }) => {
      const latestWorkspace = keepLatestWorkspace(workspace)
      return respondPRWorkspaceGate(workspaceID, gateID, {
        ...mutationFence(latestWorkspace),
        fieldValues,
      })
    },
    onSuccess: updateWorkspace,
    onError: handleError,
  })

  if (!query.data) {
    if (query.isPending) {
      return <CenteredState text={t("prWorkspaces.workspace.loading")} />
    }
    return (
      <CenteredState
        text={t("prWorkspaces.workspace.loadError")}
        action={
          <Button onClick={() => void query.refetch()}>
            {t("prWorkspaces.common.retry")}
          </Button>
        }
      />
    )
  }

  const workspace = query.data
  const record = workspace.workspace
  const implementation = canImplementWorkspace(workspace)
  const latestEvidence = latestValidation(workspace)
  const latestRepair = latestRepairAttempt(workspace)
  const deferredPolicyRestartPending =
    repositoryAssignmentsQuery.data?.effects.deferredPolicyEffect ===
    "restart-required"
  const deferredIssueMode = deferredPolicyRestartPending
    ? undefined
    : resolveDeferredIssueMode(
        repositoryAssignmentsQuery.data,
        record.provider_origin,
        record.repository_id,
      )
  const selectedFindingIDs = workspace.findings
    .filter((finding) => finding.disposition === "in_scope")
    .map((finding) => finding.id)
  const lifecycleReadOnly =
    record.phase === "publication" || record.phase === "complete"
  const pendingGate = workspace.gates.some(
    (gate) => gate.state === "waiting_gate" || gate.state === "waiting_user",
  )
  const workspaceBusy =
    record.execution_state === "running" ||
    refreshMutation.isPending ||
    draftMutation.isPending ||
    saveCharterMutation.isPending ||
    confirmMutation.isPending ||
    runMutation.isPending ||
    findingMutation.isPending ||
    correctionMutation.isPending ||
    messageMutation.isPending ||
    deferredMutation.isPending ||
    publicationMutation.isPending ||
    publicationReconcileMutation.isPending ||
    gateMutation.isPending

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-workspace-detail"
      aria-busy={workspaceBusy}
    >
      <PageHeader
        title={`${record.repository} #${record.pull_number}`}
        titleExtra={<PhaseBadge phase={record.phase} />}
      >
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t("prWorkspaces.workspace.back")}
          title={t("prWorkspaces.workspace.back")}
          onClick={onBack}
        >
          <IconArrowLeft />
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("prWorkspaces.portfolio.refresh")}
          title={t("prWorkspaces.portfolio.refresh")}
          disabled={refreshMutation.isPending}
          onClick={() => refreshMutation.mutate(workspace)}
        >
          <IconRefresh
            className={cn(refreshMutation.isPending && "animate-spin")}
          />
        </Button>
      </PageHeader>

      {workspaceBusy ? (
        <div className="px-4 pb-3 md:px-6">
          <p
            className="border-border bg-muted/60 mx-auto flex w-full max-w-[96rem] items-center gap-2 rounded-md border px-3 py-2 text-sm"
            role="status"
            aria-live="polite"
          >
            <IconLoader2 className="size-4 shrink-0 animate-spin" aria-hidden />
            {t("prWorkspaces.workspace.working")}
          </p>
        </div>
      ) : (
        <p className="sr-only" role="status" aria-live="polite">
          {t(`prWorkspaces.states.${record.execution_state}`)}
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-8 md:px-6">
        <div className="mx-auto grid w-full max-w-[96rem] gap-4 xl:grid-cols-[12rem_minmax(0,1fr)_20rem]">
          <LifecycleRail workspace={workspace} />

          {pendingGate && (
            <aside className="min-w-0 xl:sticky xl:top-0 xl:col-start-3 xl:row-start-1 xl:self-start">
              <GatePanel
                workspace={workspace}
                onRespond={(gateID, fieldValues) =>
                  gateMutation.mutate({
                    workspace,
                    gateID,
                    fieldValues,
                  })
                }
                onOpenConfigs={onOpenWorkflowConfigurations}
                busy={gateMutation.isPending}
              />
            </aside>
          )}

          <div className="min-w-0 space-y-4 xl:col-start-2 xl:row-span-2 xl:row-start-1">
            <WorkspaceHeader workspace={workspace} />
            {query.isError && (
              <div
                role="alert"
                className="border-border bg-muted/40 flex flex-col gap-2 rounded-lg border p-3 text-sm sm:flex-row sm:items-start sm:justify-between"
              >
                <div className="flex min-w-0 items-start gap-2">
                  <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
                  <p>{t("prWorkspaces.workspace.refreshError")}</p>
                </div>
                <Button
                  className="shrink-0"
                  size="sm"
                  variant="outline"
                  disabled={query.isFetching}
                  onClick={() => void query.refetch()}
                >
                  <IconRefresh
                    className={cn(query.isFetching && "animate-spin")}
                  />
                  {t("prWorkspaces.workspace.retryRefresh")}
                </Button>
              </div>
            )}
            {actionError && (
              <div
                role="alert"
                className="border-destructive/40 bg-destructive/5 text-destructive flex items-start gap-2 rounded-lg border p-3 text-sm"
              >
                <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                <span>{actionError}</span>
              </div>
            )}
            <ImplementationRecoveryBanner
              workspace={workspace}
              implementation={implementation}
              validation={latestEvidence}
              busy={runMutation.isPending}
              onRetry={() =>
                runMutation.mutate({
                  workspace,
                  kind: "implementation-runs",
                  findingIDs: selectedFindingIDs,
                })
              }
            />
            <CharterPanel
              workspace={workspace}
              onDraft={() => draftMutation.mutate(workspace)}
              onSave={(draft) =>
                saveCharterMutation.mutate({ workspace, charter: draft })
              }
              onConfirm={() => confirmMutation.mutate(workspace)}
              editable={!lifecycleReadOnly}
              busy={
                draftMutation.isPending ||
                saveCharterMutation.isPending ||
                confirmMutation.isPending
              }
            />
            <SharedGuidancePanel
              workspace={workspace}
              editable={!lifecycleReadOnly}
              busy={messageMutation.isPending}
              onSend={(draft) =>
                messageMutation.mutateAsync({ workspace, draft })
              }
            />
            <ReviewPanel
              workspace={workspace}
              onReview={() =>
                runMutation.mutate({ workspace, kind: "review-runs" })
              }
              onNudge={() =>
                runMutation.mutate({
                  workspace,
                  kind: "nudge-runs",
                  stage: "review",
                })
              }
              busy={runMutation.isPending}
            />
            <FindingsPanel
              workspace={workspace}
              onDisposition={(findingID, disposition) =>
                findingMutation.mutate({ workspace, findingID, disposition })
              }
              onCorrection={(draft, onSuccess) =>
                correctionMutation.mutate({ workspace, draft }, { onSuccess })
              }
              busy={
                findingMutation.isPending ||
                correctionMutation.isPending ||
                lifecycleReadOnly
              }
            />
            <DeferredPanel
              workspace={workspace}
              mode={deferredIssueMode}
              runtimeRestartPending={deferredPolicyRestartPending}
              settingsLoading={repositoryAssignmentsQuery.isFetching}
              settingsError={repositoryAssignmentsQuery.isError}
              onRetrySettings={() => void repositoryAssignmentsQuery.refetch()}
              onCommand={(command, onSuccess) =>
                deferredMutation.mutate(
                  { workspace, command },
                  onSuccess ? { onSuccess } : undefined,
                )
              }
              busy={deferredMutation.isPending}
            />
            <ImplementationPanel
              workspace={workspace}
              implementation={implementation}
              validation={latestEvidence}
              repair={latestRepair}
              onStart={(findingIDs) =>
                runMutation.mutate({
                  workspace,
                  kind: "implementation-runs",
                  findingIDs,
                })
              }
              onCompletionAudit={() =>
                runMutation.mutate({ workspace, kind: "completion-audits" })
              }
              onCompletionNudge={() =>
                runMutation.mutate({
                  workspace,
                  kind: "nudge-runs",
                  stage: "implementation_completion",
                })
              }
              busy={runMutation.isPending}
            />
            <CorrectionsPanel
              workspace={workspace}
              onCreate={(draft, onSuccess) =>
                correctionMutation.mutate({ workspace, draft }, { onSuccess })
              }
              onPromote={(correctionID) =>
                promoteMutation.mutate({ workspace, correctionID })
              }
              editable={!lifecycleReadOnly}
              busy={correctionMutation.isPending || promoteMutation.isPending}
            />
            <PublicationPanel
              workspace={workspace}
              onPublish={(phase, findingIDs) =>
                publicationMutation.mutate({
                  workspace,
                  phase,
                  findingIDs,
                })
              }
              onReconcile={(publicationID) =>
                publicationReconcileMutation.mutate({
                  workspace,
                  publicationID,
                })
              }
              busy={
                publicationMutation.isPending ||
                publicationReconcileMutation.isPending
              }
            />
          </div>

          <aside className="min-w-0 space-y-4 xl:col-start-3 xl:row-start-2 xl:self-start">
            <ScopeMatrixPanel workspace={workspace} />
            {!pendingGate && (
              <GatePanel
                workspace={workspace}
                onRespond={(gateID, fieldValues) =>
                  gateMutation.mutate({
                    workspace,
                    gateID,
                    fieldValues,
                  })
                }
                onOpenConfigs={onOpenWorkflowConfigurations}
                busy={gateMutation.isPending}
              />
            )}
            <ActivityPanel workspace={workspace} />
          </aside>
        </div>
      </div>
    </div>
  )
}

function mutationFence(workspace: PRWorkspace) {
  return {
    expected_version: workspace.workspace.version,
    request_id: createPRWorkspaceRequestID(),
  }
}

function providerHeadRevision(workspace: PRWorkspace): string {
  return (
    workspace.provider_snapshot.provider_revision ||
    workspace.provider_snapshot.head_sha
  )
}

function WorkspaceHeader({ workspace }: { workspace: PRWorkspace }) {
  const { t } = useTranslation()
  const record = workspace.workspace
  const provider = workspace.provider_snapshot
  const charter = activePRWorkspaceCharter(workspace)
  return (
    <section
      id="pr-intake"
      data-testid="pr-workspace-summary"
      className="border-border bg-card scroll-mt-4 rounded-lg border p-4"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <IconGitPullRequest className="text-muted-foreground size-5 shrink-0" />
            <h2 className="truncate text-lg font-semibold">
              {provider.title || `${record.repository} #${record.pull_number}`}
            </h2>
          </div>
          <p className="text-muted-foreground mt-1 truncate text-sm">
            {provider.author_login} · {provider.head_ref} → {provider.base_ref}
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <StateBadge state={record.execution_state} />
          {charter && <TypeBadge type={charter.type} />}
          <Badge variant={provider.head_writable ? "secondary" : "outline"}>
            {provider.head_writable
              ? t("prWorkspaces.workspace.writable")
              : t("prWorkspaces.workspace.readOnly")}
          </Badge>
          <Button asChild size="sm" variant="outline">
            <a
              href={providerPullURL(provider)}
              target="_blank"
              rel="noreferrer"
            >
              <IconBrandGithub />
              {t("prWorkspaces.workspace.openPR")}
            </a>
          </Button>
        </div>
      </div>
      <div className="text-muted-foreground mt-3 grid gap-2 text-xs sm:grid-cols-3">
        <span>
          {t("prWorkspaces.workspace.baseSha")}: {shortSHA(provider.base_sha)}
        </span>
        <span>
          {t("prWorkspaces.workspace.headSha")}: {shortSHA(provider.head_sha)}
        </span>
        <span>
          {t("prWorkspaces.workspace.version")}: {record.version}
        </span>
      </div>
    </section>
  )
}

function LifecycleRail({ workspace }: { workspace: PRWorkspace }) {
  const { t } = useTranslation()
  const currentIndex = phaseIndex(workspace.workspace.phase)
  return (
    <nav
      aria-label={t("prWorkspaces.workspace.lifecycleRail")}
      data-testid="pr-lifecycle-rail"
      className="border-border bg-card hidden rounded-lg border p-2 xl:sticky xl:top-0 xl:col-start-1 xl:row-span-2 xl:row-start-1 xl:block xl:self-start"
    >
      <ol className="space-y-1">
        {prWorkspacePhases.map((phase, index) => {
          const target = phase === "complete" ? undefined : `#pr-${phase}`
          const content = (
            <>
              <span
                className={cn(
                  "border-border flex size-5 shrink-0 items-center justify-center rounded-full border text-[10px]",
                  index < currentIndex &&
                    "bg-primary text-primary-foreground border-primary",
                )}
              >
                {index < currentIndex ? (
                  <IconCheck className="size-3" />
                ) : (
                  index + 1
                )}
              </span>
              <span className="min-w-0 truncate">
                {t(`prWorkspaces.phases.${phase}`, { defaultValue: phase })}
              </span>
            </>
          )
          const className = cn(
            "text-muted-foreground flex items-center gap-2 rounded-md px-2 py-2 text-xs",
            index === currentIndex && "bg-muted text-foreground font-medium",
          )
          return (
            <li key={phase}>
              {target ? (
                <a
                  href={target}
                  className={className}
                  aria-current={index === currentIndex ? "step" : undefined}
                >
                  {content}
                </a>
              ) : (
                <div
                  className={className}
                  aria-current={index === currentIndex ? "step" : undefined}
                >
                  {content}
                </div>
              )}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

interface CharterDraft {
  prType: PRWorkspaceType
  goal: string
  acceptanceCriteria: string
  includedAreas: string
  exclusions: string
  nonGoals: string
}

function CharterPanel({
  workspace,
  onDraft,
  onSave,
  onConfirm,
  editable,
  busy,
}: {
  workspace: PRWorkspace
  onDraft: () => void
  onSave: (draft: CharterDraft) => void
  onConfirm: () => void
  editable: boolean
  busy: boolean
}) {
  const { t } = useTranslation()
  const charter = activePRWorkspaceCharter(workspace)
  const [draft, setDraft] = useState<CharterDraft>(() =>
    charterToDraft(charter),
  )
  useEffect(() => setDraft(charterToDraft(charter)), [charter])
  if (!charter) {
    return (
      <StageCard
        id="pr-charter"
        title={t("prWorkspaces.charter.title")}
        icon={<IconRoute />}
      >
        <div className="flex flex-col items-start gap-3">
          <p className="text-muted-foreground text-sm">
            {t("prWorkspaces.charter.empty")}
          </p>
          <Button onClick={onDraft} disabled={busy || !editable}>
            <IconSparkles />
            {t("prWorkspaces.charter.draft")}
          </Button>
        </div>
      </StageCard>
    )
  }
  const dirty = !charterDraftMatches(draft, charter)
  const staleHead = charter.head_sha !== workspace.provider_snapshot.head_sha
  return (
    <StageCard
      id="pr-charter"
      title={t("prWorkspaces.charter.title")}
      icon={<IconRoute />}
      badge={
        <Badge variant={charter.confirmed ? "secondary" : "outline"}>
          {t(
            `prWorkspaces.charter.status.${charter.confirmed ? "confirmed" : "draft"}`,
          )}
        </Badge>
      }
    >
      <div className="grid gap-3 md:grid-cols-2">
        <Field label={t("prWorkspaces.charter.type")}>
          <Select
            value={draft.prType}
            disabled={!editable}
            onValueChange={(value) =>
              setDraft((current) => ({
                ...current,
                prType: value as PRWorkspaceType,
              }))
            }
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("prWorkspaces.charter.type")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(
                ["fix", "refactor", "feature", "documentation", "test"] as const
              ).map((type) => (
                <SelectItem key={type} value={type}>
                  {t(`prWorkspaces.types.${type}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t("prWorkspaces.charter.goal")} className="md:col-span-2">
          <Textarea
            value={draft.goal}
            disabled={!editable}
            aria-label={t("prWorkspaces.charter.goal")}
            onChange={(event) =>
              setDraft((current) => ({ ...current, goal: event.target.value }))
            }
          />
        </Field>
        <CharterListField
          label={t("prWorkspaces.charter.acceptance")}
          value={draft.acceptanceCriteria}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, acceptanceCriteria: value }))
          }
        />
        <CharterListField
          label={t("prWorkspaces.charter.included")}
          value={draft.includedAreas}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, includedAreas: value }))
          }
        />
        <CharterListField
          label={t("prWorkspaces.charter.exclusions")}
          value={draft.exclusions}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, exclusions: value }))
          }
        />
        <CharterListField
          label={t("prWorkspaces.charter.nonGoals")}
          value={draft.nonGoals}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, nonGoals: value }))
          }
        />
      </div>
      <div className="mt-3 flex flex-wrap justify-end gap-2">
        {staleHead && (
          <p className="text-destructive mr-auto self-center text-xs">
            {t("prWorkspaces.charter.staleHead")}
          </p>
        )}
        <Button
          variant="outline"
          onClick={() => onSave(draft)}
          disabled={
            busy || !editable || !draft.goal.trim() || (!dirty && !staleHead)
          }
        >
          {workspace.workspace.active_charter_id
            ? t("prWorkspaces.charter.revise")
            : t("prWorkspaces.charter.save")}
        </Button>
        {!charter.confirmed && (
          <Button
            onClick={onConfirm}
            disabled={
              busy || !editable || !draft.goal.trim() || dirty || staleHead
            }
          >
            <IconShieldCheck />
            {t("prWorkspaces.charter.confirm")}
          </Button>
        )}
      </div>
    </StageCard>
  )
}

const earlyGuidanceStages = ["workspace", "review", "implementation"] as const
const implementationGuidanceStages = ["workspace", "implementation"] as const
const readOnlyGuidanceStages: readonly GuidanceStage[] = []

function guidanceStagesForPhase(
  phase: PRWorkspacePhase,
): readonly GuidanceStage[] {
  switch (phase) {
    case "implementation":
    case "validation":
    case "completion_audit":
      return implementationGuidanceStages
    case "publication":
    case "complete":
      return readOnlyGuidanceStages
    default:
      return earlyGuidanceStages
  }
}

function defaultGuidanceStage(phase: PRWorkspacePhase): GuidanceStage {
  switch (phase) {
    case "review":
    case "triage":
      return "review"
    case "implementation":
    case "validation":
    case "completion_audit":
      return "implementation"
    default:
      return "workspace"
  }
}

function defaultGuidanceApplicability(
  stage: GuidanceStage,
): PRWorkspaceCorrectionApplicability {
  return stage === "workspace" ? "both" : stage
}

function SharedGuidancePanel({
  workspace,
  editable,
  busy,
  onSend,
}: {
  workspace: PRWorkspace
  editable: boolean
  busy: boolean
  onSend: (draft: GuidanceDraft) => Promise<unknown>
}) {
  const { t } = useTranslation()
  const phase = workspace.workspace.phase
  const allowedStages = guidanceStagesForPhase(phase)
  const initialStage = defaultGuidanceStage(phase)
  const [draft, setDraft] = useState<GuidanceDraft>(() => ({
    content: "",
    stage: initialStage,
    markAsCorrection: false,
    applicability: defaultGuidanceApplicability(initialStage),
  }))
  const messages = useMemo(
    () =>
      [...workspace.messages].sort((left, right) =>
        left.created_at.localeCompare(right.created_at),
      ),
    [workspace.messages],
  )
  const activeCharter = activePRWorkspaceCharter(workspace)

  useEffect(() => {
    if (allowedStages.length === 0 || allowedStages.includes(draft.stage)) {
      return
    }
    const nextStage = defaultGuidanceStage(phase)
    setDraft((current) => ({
      ...current,
      stage: nextStage,
      applicability: defaultGuidanceApplicability(nextStage),
    }))
  }, [allowedStages, draft.stage, phase])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (
      !editable ||
      busy ||
      !draft.content.trim() ||
      !allowedStages.includes(draft.stage)
    ) {
      return
    }
    try {
      await onSend({ ...draft, content: draft.content.trim() })
      setDraft((current) => ({
        ...current,
        content: "",
        markAsCorrection: false,
      }))
    } catch {
      // The page-level mutation displays the API error. Keep every draft field
      // intact so a conflict can be inspected and retried against fresh state.
    }
  }

  return (
    <StageCard
      id="pr-guidance"
      title={t("prWorkspaces.guidance.title")}
      icon={<IconMessageCircle />}
      badge={<Badge variant="outline">{workspace.messages.length}</Badge>}
    >
      <p className="text-muted-foreground text-sm">
        {t("prWorkspaces.guidance.description")}
      </p>
      {messages.length === 0 ? (
        <p className="text-muted-foreground mt-3 text-sm">
          {t("prWorkspaces.guidance.empty")}
        </p>
      ) : (
        <div
          className="border-border mt-3 max-h-72 overflow-auto border-l pl-3"
          role="region"
          aria-label={t("prWorkspaces.guidance.history")}
          // A bounded scroll region must be keyboard-focusable so keyboard users
          // can reach content that is clipped below the fold.
          // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
          tabIndex={0}
        >
          <ol className="space-y-2">
            {messages.map((message) => {
              const currentContext =
                (!message.charter_id ||
                  message.charter_id === activeCharter?.id) &&
                (!message.head_sha ||
                  message.head_sha === workspace.provider_snapshot.head_sha)
              return (
                <li key={message.id} className="text-sm">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <Badge variant="secondary">
                      {message.role === "user"
                        ? t("prWorkspaces.guidance.roles.user")
                        : message.role === "assistant"
                          ? t("prWorkspaces.guidance.roles.assistant")
                          : message.role}
                    </Badge>
                    <Badge variant="outline">
                      {guidanceHistoryStageLabel(message.stage, t)}
                    </Badge>
                    {!currentContext && (
                      <Badge variant="outline">
                        {t("prWorkspaces.guidance.earlierRevision")}
                      </Badge>
                    )}
                    <time
                      className="text-muted-foreground ml-auto text-xs"
                      dateTime={message.created_at}
                    >
                      {new Date(message.created_at).toLocaleString()}
                    </time>
                  </div>
                  <p className="mt-1 break-words whitespace-pre-wrap">
                    {message.content}
                  </p>
                </li>
              )
            })}
          </ol>
        </div>
      )}
      <form
        className="border-border bg-muted/20 mt-3 grid gap-3 rounded-md border p-3 md:grid-cols-[minmax(0,1fr)_15rem]"
        onSubmit={(event) => void submit(event)}
      >
        <Field
          label={t("prWorkspaces.guidance.message")}
          className="md:row-span-2"
        >
          <Textarea
            value={draft.content}
            disabled={!editable || busy}
            aria-label={t("prWorkspaces.guidance.message")}
            placeholder={t("prWorkspaces.guidance.placeholder")}
            className="min-h-24"
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                content: event.target.value,
              }))
            }
          />
        </Field>
        <Field label={t("prWorkspaces.guidance.target")}>
          <Select
            value={draft.stage}
            disabled={!editable || busy || allowedStages.length === 0}
            onValueChange={(value) => {
              const stage = value as GuidanceStage
              if (!allowedStages.includes(stage)) return
              setDraft((current) => ({
                ...current,
                stage,
                applicability: defaultGuidanceApplicability(stage),
              }))
            }}
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("prWorkspaces.guidance.target")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {allowedStages.map((stage) => (
                <SelectItem key={stage} value={stage}>
                  {t(`prWorkspaces.guidance.targets.${stage}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <div className="space-y-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={draft.markAsCorrection}
              disabled={!editable || busy}
              onChange={(event) =>
                setDraft((current) => ({
                  ...current,
                  markAsCorrection: event.target.checked,
                }))
              }
            />
            <span>{t("prWorkspaces.guidance.markCorrection")}</span>
          </label>
          {draft.markAsCorrection && (
            <Field label={t("prWorkspaces.guidance.correctionApplies")}>
              <Select
                value={draft.applicability}
                disabled={!editable || busy}
                onValueChange={(value) =>
                  setDraft((current) => ({
                    ...current,
                    applicability: value as PRWorkspaceCorrectionApplicability,
                  }))
                }
              >
                <SelectTrigger
                  className="w-full"
                  aria-label={t("prWorkspaces.guidance.correctionApplies")}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {(["review", "implementation", "both"] as const).map(
                    (value) => (
                      <SelectItem key={value} value={value}>
                        {t(`prWorkspaces.corrections.applicability.${value}`)}
                      </SelectItem>
                    ),
                  )}
                </SelectContent>
              </Select>
            </Field>
          )}
        </div>
        {!editable && (
          <p className="text-muted-foreground text-xs md:col-span-2">
            {t("prWorkspaces.guidance.readOnly")}
          </p>
        )}
        <div className="flex justify-end md:col-span-2">
          <Button
            type="submit"
            size="sm"
            disabled={
              !editable ||
              busy ||
              !draft.content.trim() ||
              !allowedStages.includes(draft.stage)
            }
          >
            {t("prWorkspaces.guidance.send")}
          </Button>
        </div>
      </form>
    </StageCard>
  )
}

function guidanceHistoryStageLabel(
  stage: string | undefined,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  const normalized = stage?.trim().toLowerCase()
  if (!normalized || ["all", "both", "workspace"].includes(normalized)) {
    return t("prWorkspaces.guidance.targets.workspace")
  }
  if (normalized === "review" || normalized === "implementation") {
    return t(`prWorkspaces.guidance.targets.${normalized}`)
  }
  return t(`prWorkspaces.phases.${normalized}`, { defaultValue: stage })
}

function ReviewPanel({
  workspace,
  onReview,
  onNudge,
  busy,
}: {
  workspace: PRWorkspace
  onReview: () => void
  onNudge: () => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const reviewRounds = workspace.nudge_rounds.filter(
    (round) => round.stage === "review",
  )
  const reviewRuns = workspace.stage_runs.filter(
    (run) => run.stage === "review",
  )
  const latestReview = reviewRuns.at(-1)
  const latestRound = reviewRounds.at(-1)
  const phaseAllowsReview =
    workspace.workspace.phase === "review" ||
    workspace.workspace.phase === "triage"
  return (
    <StageCard
      id="pr-review"
      title={t("prWorkspaces.review.title")}
      icon={<IconRobot />}
      badge={
        <div className="flex items-center gap-1.5">
          {latestReview && <StateBadge state={latestReview.state} />}
          <Badge variant="outline">
            {reviewRounds.length} {t("prWorkspaces.review.nudges")}
          </Badge>
        </div>
      }
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0 text-sm">
          <p className="text-muted-foreground">
            {latestReview?.summary ?? t("prWorkspaces.review.notStarted")}
          </p>
          {latestReview && (
            <p className="text-muted-foreground mt-1 text-xs">
              {t("prWorkspaces.review.latestRun", {
                attempt: latestReview.attempt,
                state: t(`prWorkspaces.states.${latestReview.state}`),
              })}
            </p>
          )}
          {latestRound && (
            <p className="mt-2 text-xs">
              {t("prWorkspaces.review.latestNudge", {
                strategy: latestRound.strategy,
                state: t(`prWorkspaces.states.${latestRound.state}`),
                novel: latestRound.novel_findings,
                duplicates: latestRound.duplicate_count,
              })}
            </p>
          )}
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={onNudge}
            disabled={
              busy ||
              workspace.workspace.phase !== "triage" ||
              latestReview?.state !== "succeeded"
            }
          >
            <IconBolt />
            {t("prWorkspaces.review.nudge")}
          </Button>
          <Button
            onClick={onReview}
            disabled={
              busy ||
              !phaseAllowsReview ||
              !activePRWorkspaceCharter(workspace)?.confirmed
            }
          >
            <IconSearchReview />
            {t("prWorkspaces.review.run")}
          </Button>
        </div>
      </div>
      {reviewRounds.length > 0 && (
        <ol className="mt-3 grid gap-2 md:grid-cols-2">
          {reviewRounds.map((round) => (
            <li
              key={round.id}
              data-nudge-round-id={round.id}
              className="border-border rounded-md border p-3 text-sm"
            >
              <div className="flex items-center justify-between gap-2">
                <strong>
                  {t("prWorkspaces.review.round", { number: round.round })}
                </strong>
                <StateBadge state={round.state} />
              </div>
              <p className="mt-1">{round.strategy}</p>
              <p className="text-muted-foreground mt-1 text-xs">
                {round.challenge}
              </p>
              <div className="text-muted-foreground mt-2 flex gap-3 text-xs">
                <span>
                  {t("prWorkspaces.review.novel", {
                    count: round.novel_findings,
                  })}
                </span>
                <span>
                  {t("prWorkspaces.review.duplicates", {
                    count: round.duplicate_count,
                  })}
                </span>
                {round.reward != null && (
                  <span title={round.reward_provenance}>
                    {t("prWorkspaces.review.reward", {
                      reward: round.reward.toFixed(2),
                    })}
                  </span>
                )}
                <span>
                  {t("prWorkspaces.review.resolved", {
                    count: round.resolved_findings,
                  })}
                </span>
              </div>
              {round.public_error && (
                <p className="text-destructive mt-1 text-xs">
                  {round.public_error}
                </p>
              )}
            </li>
          ))}
        </ol>
      )}
    </StageCard>
  )
}

function IconSearchReview() {
  return <IconSparkles />
}

function FindingsPanel({
  workspace,
  onDisposition,
  onCorrection,
  busy,
}: {
  workspace: PRWorkspace
  onDisposition: (
    findingID: string,
    disposition: PRWorkspaceFindingDisposition,
  ) => void
  onCorrection: (draft: CorrectionDraft, onSuccess?: () => void) => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const counts = findingDispositionCounts(workspace.findings)
  return (
    <StageCard
      id="pr-triage"
      title={t("prWorkspaces.findings.title")}
      icon={<IconShieldCheck />}
      badge={<Badge variant="secondary">{workspace.findings.length}</Badge>}
    >
      <div className="mb-3 flex flex-wrap gap-1.5">
        {Object.entries(counts).map(([disposition, count]) => (
          <Badge key={disposition} variant="outline">
            {t(`prWorkspaces.findings.dispositions.${disposition}`)}: {count}
          </Badge>
        ))}
      </div>
      {workspace.findings.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          {t("prWorkspaces.findings.empty")}
        </p>
      ) : (
        <div className="space-y-2">
          {workspace.findings.map((finding) => {
            const guidance = scopeGuidance(finding.scope)
            const triagePhase =
              workspace.workspace.phase === "review" ||
              workspace.workspace.phase === "triage"
            const actionsAvailable =
              triagePhase && finding.disposition === "open"
            const implementationDrift =
              !triagePhase &&
              finding.scope.presence === "candidate_present" &&
              finding.disposition === "open"
            return (
              <article
                key={finding.id}
                data-finding-id={finding.id}
                className="border-border rounded-md border p-3"
              >
                <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge
                        variant={
                          finding.severity === "critical" ||
                          finding.severity === "high"
                            ? "destructive"
                            : "outline"
                        }
                      >
                        {finding.severity}
                      </Badge>
                      <Badge variant="outline">{finding.origin}</Badge>
                      <ScopeBadge
                        distance={finding.scope.distance}
                        size={finding.scope.size}
                        typeCompatible={finding.scope.type_compatible}
                      />
                    </div>
                    <h3 className="mt-2 font-medium">{finding.title}</h3>
                    <p className="text-muted-foreground mt-1 text-sm">
                      {finding.message}
                    </p>
                    {finding.evidence && (
                      <p className="mt-2 text-xs">
                        <strong>{t("prWorkspaces.findings.evidence")}:</strong>{" "}
                        {finding.evidence}
                      </p>
                    )}
                    {finding.impact && (
                      <p className="mt-1 text-xs">
                        <strong>{t("prWorkspaces.findings.impact")}:</strong>{" "}
                        {finding.impact}
                      </p>
                    )}
                    {finding.recommendation && (
                      <p className="mt-1 text-xs">
                        <strong>
                          {t("prWorkspaces.findings.recommendation")}:
                        </strong>{" "}
                        {finding.recommendation}
                      </p>
                    )}
                    {finding.file && (
                      <code className="text-muted-foreground mt-1 block truncate text-xs">
                        {finding.file}
                        {finding.line ? `:${finding.line}` : ""}
                      </code>
                    )}
                  </div>
                  <Badge variant="outline">
                    {t(
                      `prWorkspaces.findings.dispositions.${finding.disposition}`,
                    )}
                  </Badge>
                </div>
                <ScopeAssessmentDetails scope={finding.scope} />
                <div className="bg-muted/40 mt-3 rounded-md p-2 text-xs">
                  <strong>
                    {t(`prWorkspaces.findings.scopeActions.${guidance}.title`)}
                  </strong>
                  <p className="text-muted-foreground mt-1">
                    {t(`prWorkspaces.findings.scopeActions.${guidance}.reason`)}
                  </p>
                  {implementationDrift && (
                    <p className="text-destructive mt-2">
                      {t("prWorkspaces.findings.candidateDriftGate")}
                    </p>
                  )}
                  {actionsAvailable && (
                    <div className="mt-2 flex flex-wrap gap-2">
                      {(guidance === "proceed" || guidance === "gate") && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy || finding.disposition === "in_scope"}
                          onClick={() => onDisposition(finding.id, "in_scope")}
                        >
                          {guidance === "gate"
                            ? t("prWorkspaces.findings.actions.scopeGate")
                            : t("prWorkspaces.findings.actions.inScope")}
                        </Button>
                      )}
                      {(guidance === "revise_or_defer" ||
                        guidance === "classify_or_revise" ||
                        guidance === "defer") && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy || finding.disposition === "deferred"}
                          onClick={() => onDisposition(finding.id, "deferred")}
                        >
                          {t("prWorkspaces.findings.actions.defer")}
                        </Button>
                      )}
                      {guidance === "classify_or_revise" && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={busy}
                          onClick={() => onDisposition(finding.id, "in_scope")}
                        >
                          {t("prWorkspaces.findings.actions.classify")}
                        </Button>
                      )}
                      {(guidance === "revise_or_defer" ||
                        guidance === "classify_or_revise") && (
                        <Button asChild size="sm" variant="outline">
                          <a href="#pr-charter">
                            {t("prWorkspaces.findings.actions.reviseCharter")}
                          </a>
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy || finding.disposition === "dismissed"}
                        onClick={() => onDisposition(finding.id, "dismissed")}
                      >
                        {t("prWorkspaces.findings.actions.dismiss")}
                      </Button>
                    </div>
                  )}
                </div>
                <FindingCorrectionForm
                  finding={finding}
                  busy={busy}
                  onCreate={onCorrection}
                />
              </article>
            )
          })}
        </div>
      )}
    </StageCard>
  )
}

function ScopeAssessmentDetails({
  scope,
}: {
  scope: PRWorkspaceScopeAssessment
}) {
  const { t } = useTranslation()
  return (
    <div className="text-muted-foreground mt-3 grid gap-1 text-xs sm:grid-cols-2">
      <span>
        {t("prWorkspaces.findings.metrics", {
          files: scope.files,
          lines: scope.semantic_lines,
          modules: scope.modules,
        })}
      </span>
      <span>
        {t("prWorkspaces.findings.confidence", {
          confidence: Math.round(scope.confidence * 100),
        })}
        {scope.estimated ? ` · ${t("prWorkspaces.findings.estimated")}` : ""}
      </span>
      {scope.presence && (
        <span className="sm:col-span-2">
          {t("prWorkspaces.findings.presence")}:{" "}
          {t(`prWorkspaces.findings.presences.${scope.presence}`)}
        </span>
      )}
      {scope.explanation && (
        <span className="sm:col-span-2">{scope.explanation}</span>
      )}
      {scope.charter_clauses && scope.charter_clauses.length > 0 && (
        <span className="sm:col-span-2">
          {t("prWorkspaces.findings.charterClauses")}:{" "}
          {scope.charter_clauses.join(" · ")}
        </span>
      )}
      {scope.change_evidence && scope.change_evidence.length > 0 && (
        <details className="border-border mt-1 rounded border p-2 sm:col-span-2">
          <summary className="text-foreground cursor-pointer font-medium">
            {t("prWorkspaces.findings.changeEvidence", {
              count: scope.change_evidence.length,
            })}
          </summary>
          <ol className="mt-2 space-y-2">
            {scope.change_evidence.map((change, index) => (
              <li
                key={`${change.path}:${change.hunk}:${index}`}
                className="bg-muted/40 rounded p-2"
              >
                <div className="text-foreground flex flex-wrap gap-x-2 font-mono">
                  <strong className="break-all">{change.path}</strong>
                  <span>{change.hunk}</span>
                </div>
                <p className="mt-1">
                  {change.module} · {change.scope_distance} ·{" "}
                  {change.change_size} · {change.semantic_lines}{" "}
                  {t("prWorkspaces.findings.semanticLines")}
                </p>
                <p className="mt-1">{change.explanation}</p>
              </li>
            ))}
          </ol>
        </details>
      )}
    </div>
  )
}

function FindingCorrectionForm({
  finding,
  busy,
  onCreate,
}: {
  finding: PRWorkspace["findings"][number]
  busy: boolean
  onCreate: (draft: CorrectionDraft, onSuccess?: () => void) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [correction, setCorrection] = useState("")
  const [reason, setReason] = useState("")
  const [applicability, setApplicability] =
    useState<PRWorkspaceCorrectionApplicability>("both")
  if (!open) {
    return (
      <Button
        className="mt-2"
        size="sm"
        variant="ghost"
        disabled={busy}
        onClick={() => setOpen(true)}
      >
        <IconMessageCircle />
        {t("prWorkspaces.findings.actions.correct")}
      </Button>
    )
  }
  return (
    <div className="border-border mt-3 grid gap-2 rounded-md border p-3">
      <Field label={t("prWorkspaces.corrections.corrected")}>
        <Textarea
          value={correction}
          aria-label={`${finding.title}: ${t("prWorkspaces.corrections.corrected")}`}
          onChange={(event) => setCorrection(event.target.value)}
        />
      </Field>
      <Field label={t("prWorkspaces.corrections.reason")}>
        <Input
          value={reason}
          aria-label={`${finding.title}: ${t("prWorkspaces.corrections.reason")}`}
          onChange={(event) => setReason(event.target.value)}
        />
      </Field>
      <Field label={t("prWorkspaces.corrections.applies")}>
        <Select
          value={applicability}
          onValueChange={(value) =>
            setApplicability(value as PRWorkspaceCorrectionApplicability)
          }
        >
          <SelectTrigger
            aria-label={`${finding.title}: ${t("prWorkspaces.corrections.applies")}`}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(["review", "implementation", "both"] as const).map((value) => (
              <SelectItem key={value} value={value}>
                {t(`prWorkspaces.corrections.applicability.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <div className="flex justify-end gap-2">
        <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>
          {t("prWorkspaces.findings.actions.cancel")}
        </Button>
        <Button
          size="sm"
          disabled={busy || !correction.trim()}
          onClick={() =>
            onCreate(
              {
                kind: "finding_quality",
                applicability,
                targetID: finding.id,
                originalClaim: `${finding.title}: ${finding.message}`,
                correction,
                reason,
              },
              () => {
                setOpen(false)
                setCorrection("")
                setReason("")
                setApplicability("both")
              },
            )
          }
        >
          {t("prWorkspaces.corrections.add")}
        </Button>
      </div>
    </div>
  )
}

function ImplementationPanel({
  workspace,
  implementation,
  validation,
  repair,
  onStart,
  onCompletionAudit,
  onCompletionNudge,
  busy,
}: {
  workspace: PRWorkspace
  implementation: ReturnType<typeof canImplementWorkspace>
  validation: ReturnType<typeof latestValidation>
  repair: ReturnType<typeof latestRepairAttempt>
  onStart: (findingIDs: string[]) => void
  onCompletionAudit: () => void
  onCompletionNudge: () => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const selected = workspace.findings.filter(
    (finding) => finding.disposition === "in_scope",
  )
  const completionAudits = workspace.stage_runs.filter(
    (run) => run.stage === "completion_audit",
  )
  const completionAudit = completionAudits[completionAudits.length - 1]
  const completionRounds = workspace.nudge_rounds.filter(
    (round) => round.stage === "implementation_completion",
  )
  const implementationRuns = workspace.stage_runs.filter(
    (run) => run.stage === "implementation",
  )
  const latestImplementation = implementationRuns[implementationRuns.length - 1]
  const implementationStopped =
    latestImplementation?.state === "failed" ||
    latestImplementation?.state === "blocked"
  const validationGreen =
    isPRWorkspaceValidationGreen(validation) &&
    repair?.candidate_sha != null &&
    repair.candidate_sha === validation?.candidate_sha &&
    repair.stage_run_id === validation.stage_run_id
  const phaseAllowsCompletionActions = [
    "implementation",
    "validation",
    "completion_audit",
  ].includes(workspace.workspace.phase)
  return (
    <StageCard
      id="pr-implementation"
      title={t("prWorkspaces.implementation.title")}
      icon={<IconCode />}
    >
      {!implementation.allowed && (
        <div className="border-border bg-muted/40 mb-3 rounded-md border p-3 text-sm">
          <strong>{t("prWorkspaces.implementation.unavailable")}</strong>
          <p className="text-muted-foreground mt-1">
            {t(`prWorkspaces.implementation.reasons.${implementation.reason}`)}
          </p>
        </div>
      )}
      <div id="pr-validation" className="scroll-mt-4">
        <div className="grid gap-2 sm:grid-cols-4">
          <EvidenceMetric
            label={t("prWorkspaces.implementation.findings")}
            value={String(selected.length)}
          />
          <EvidenceMetric
            label={t("prWorkspaces.implementation.changedFiles")}
            value={String(repair?.changed_files?.length ?? 0)}
          />
          <EvidenceMetric
            label={t("prWorkspaces.implementation.ci")}
            value={validation?.state ?? "not_started"}
          />
          <EvidenceMetric
            label={t("prWorkspaces.implementation.localReview")}
            value={
              validation?.checks.find((check) =>
                check.name.toLowerCase().includes("review"),
              )?.status ?? "not_started"
            }
          />
        </div>
        {repair && (
          <div className="mt-3">
            <div className="flex flex-wrap gap-1.5">
              <Badge
                variant={
                  repair.scope.type_compatible ? "secondary" : "destructive"
                }
              >
                {repair.scope.type_compatible
                  ? t("prWorkspaces.implementation.typeClean")
                  : t("prWorkspaces.implementation.typeDrift")}
              </Badge>
              <Badge
                variant={
                  repair.scope.distance === "S0_exact"
                    ? "secondary"
                    : "destructive"
                }
              >
                {repair.scope.distance === "S0_exact"
                  ? t("prWorkspaces.implementation.scopeClean")
                  : t("prWorkspaces.implementation.scopeDrift")}
              </Badge>
              <Badge variant="outline">{repair.scope.size}</Badge>
            </div>
            <ScopeAssessmentDetails scope={repair.scope} />
          </div>
        )}
        {validation && validation.checks.length > 0 && (
          <div className="border-border mt-3 rounded-md border p-3 text-xs">
            <strong>
              {t("prWorkspaces.implementation.validationEvidence")}
            </strong>
            <ol className="mt-2 space-y-2">
              {validation.checks.map((check) => (
                <li key={check.id}>
                  <ValidationCheckEvidence check={check} />
                </li>
              ))}
            </ol>
          </div>
        )}
      </div>
      <div id="pr-completion_audit" className="scroll-mt-4">
        {completionAudit?.summary && (
          <p className="text-muted-foreground mt-3 text-sm">
            {completionAudit.summary}
          </p>
        )}
        {completionRounds.length > 0 && (
          <ol className="mt-3 grid gap-2 md:grid-cols-2">
            {completionRounds.map((round) => (
              <li
                key={round.id}
                data-nudge-round-id={round.id}
                className="border-border rounded-md border p-3 text-sm"
              >
                <div className="flex items-center justify-between gap-2">
                  <strong>
                    {t("prWorkspaces.implementation.round", {
                      number: round.round,
                    })}
                  </strong>
                  <StateBadge state={round.state} />
                </div>
                <p className="mt-1">{round.strategy}</p>
                <p className="text-muted-foreground mt-1 text-xs">
                  {round.challenge}
                </p>
                <div className="text-muted-foreground mt-2 flex gap-3 text-xs">
                  <span>
                    {t("prWorkspaces.review.novel", {
                      count: round.novel_findings,
                    })}
                  </span>
                  <span>
                    {t("prWorkspaces.review.duplicates", {
                      count: round.duplicate_count,
                    })}
                  </span>
                  {round.reward != null && (
                    <span title={round.reward_provenance}>
                      {t("prWorkspaces.review.reward", {
                        reward: round.reward.toFixed(2),
                      })}
                    </span>
                  )}
                  <span>
                    {t("prWorkspaces.review.resolved", {
                      count: round.resolved_findings,
                    })}
                  </span>
                </div>
                {round.public_error && (
                  <p className="text-destructive mt-1 text-xs">
                    {round.public_error}
                  </p>
                )}
              </li>
            ))}
          </ol>
        )}
        <div className="mt-3 flex flex-wrap justify-end gap-2">
          <Button
            variant="outline"
            onClick={onCompletionAudit}
            disabled={
              busy ||
              !phaseAllowsCompletionActions ||
              !repair ||
              !validationGreen
            }
            title={
              validationGreen
                ? undefined
                : t("prWorkspaces.implementation.validationRequired")
            }
          >
            <IconShieldCheck />
            {t("prWorkspaces.implementation.audit")}
          </Button>
          <Button
            variant="outline"
            onClick={onCompletionNudge}
            disabled={
              busy ||
              !phaseAllowsCompletionActions ||
              !repair ||
              !validationGreen ||
              !completionAudit
            }
          >
            <IconBolt />
            {t("prWorkspaces.implementation.nudge")}
          </Button>
          {!implementationStopped && (
            <Button
              onClick={() => onStart(selected.map((finding) => finding.id))}
              disabled={busy || !implementation.allowed}
            >
              <IconCode />
              {selected.length === 0
                ? t("prWorkspaces.implementation.startCharter")
                : t("prWorkspaces.implementation.start")}
            </Button>
          )}
        </div>
      </div>
    </StageCard>
  )
}

function ImplementationRecoveryBanner({
  workspace,
  implementation,
  validation,
  busy,
  onRetry,
}: {
  workspace: PRWorkspace
  implementation: ReturnType<typeof canImplementWorkspace>
  validation: ReturnType<typeof latestValidation>
  busy: boolean
  onRetry: () => void
}) {
  const { t } = useTranslation()
  const implementationRuns = workspace.stage_runs.filter(
    (run) => run.stage === "implementation",
  )
  const latestImplementation = implementationRuns.at(-1)
  if (
    latestImplementation?.state !== "failed" &&
    latestImplementation?.state !== "blocked"
  ) {
    return null
  }
  const failedCheck = validation?.checks.find(
    (check) => check.status !== "passed" && check.status !== "skipped",
  )
  return (
    <section
      className="border-destructive/40 bg-destructive/5 rounded-lg border p-3 text-sm"
      role="alert"
      aria-labelledby="pr-implementation-recovery-title"
    >
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <strong
            id="pr-implementation-recovery-title"
            className="text-destructive"
          >
            {t("prWorkspaces.implementation.stopped")}
          </strong>
          <p className="mt-1">
            {t(
              `prWorkspaces.implementation.errors.${latestImplementation.public_error ?? "unknown"}`,
              {
                defaultValue: t("prWorkspaces.implementation.errors.unknown"),
              },
            )}
          </p>
          {latestImplementation.summary && (
            <p className="text-muted-foreground mt-1 text-xs">
              {latestImplementation.summary}
            </p>
          )}
          {failedCheck && (
            <div className="border-border bg-background/70 mt-2 rounded-md border p-2">
              <ValidationCheckEvidence check={failedCheck} compact />
            </div>
          )}
          <p className="text-muted-foreground mt-2 text-xs">
            {t("prWorkspaces.implementation.retryHint")}
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          <Button asChild size="sm" variant="outline">
            <a href="#pr-validation">
              {t("prWorkspaces.implementation.reviewValidation")}
            </a>
          </Button>
          <Button
            size="sm"
            disabled={busy || !implementation.allowed}
            onClick={onRetry}
          >
            <IconRefresh />
            {t("prWorkspaces.implementation.retry")}
          </Button>
        </div>
      </div>
    </section>
  )
}

const validationDiagnosticLimit = 1_200

function boundedValidationDiagnostic(value: string): string {
  const normalized = value.trim()
  if (normalized.length <= validationDiagnosticLimit) return normalized
  return `${normalized.slice(0, validationDiagnosticLimit).trimEnd()}\n…`
}

function ValidationCheckEvidence({
  check,
  compact = false,
}: {
  check: PRWorkspace["validation_runs"][number]["checks"][number]
  compact?: boolean
}) {
  const { t } = useTranslation()
  const diagnostic = check.summary
    ? boundedValidationDiagnostic(check.summary)
    : ""
  return (
    <div className="min-w-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <strong className="font-medium break-words">{check.name}</strong>
        <Badge variant={check.status === "passed" ? "secondary" : "outline"}>
          {check.status}
        </Badge>
      </div>
      {(check.exit_code != null || check.duration_ms != null) && (
        <p className="text-muted-foreground mt-1 text-xs">
          {check.exit_code != null &&
            t("prWorkspaces.implementation.exitCode", {
              code: check.exit_code,
            })}
          {check.exit_code != null && check.duration_ms != null ? " · " : ""}
          {check.duration_ms != null &&
            t("prWorkspaces.implementation.duration", {
              duration: check.duration_ms,
            })}
        </p>
      )}
      {diagnostic && (
        <details className="mt-1" open={compact || undefined}>
          <summary className="cursor-pointer text-xs font-medium">
            {t("prWorkspaces.implementation.diagnostics")}
          </summary>
          <pre className="bg-muted/50 mt-1 max-h-40 overflow-auto rounded p-2 font-mono text-xs break-words whitespace-pre-wrap">
            {diagnostic}
          </pre>
        </details>
      )}
    </div>
  )
}

interface CorrectionDraft {
  kind: PRWorkspaceCorrectionKind
  applicability: PRWorkspaceCorrectionApplicability
  targetID?: string
  originalClaim: string
  correction: string
  reason: string
}

const emptyCorrection: CorrectionDraft = {
  kind: "factual",
  applicability: "both",
  originalClaim: "",
  correction: "",
  reason: "",
}

function CorrectionsPanel({
  workspace,
  onCreate,
  onPromote,
  editable,
  busy,
}: {
  workspace: PRWorkspace
  onCreate: (draft: CorrectionDraft, onSuccess?: () => void) => void
  onPromote: (correctionID: string) => void
  editable: boolean
  busy: boolean
}) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<CorrectionDraft>(emptyCorrection)
  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!draft.originalClaim.trim() || !draft.correction.trim()) return
    onCreate(draft, () => setDraft(emptyCorrection))
  }
  return (
    <StageCard
      id="pr-corrections"
      title={t("prWorkspaces.corrections.title")}
      icon={<IconMessageCircle />}
      badge={<Badge variant="outline">{workspace.corrections.length}</Badge>}
    >
      <form onSubmit={submit} className="grid gap-3 md:grid-cols-2">
        <Field label={t("prWorkspaces.corrections.kind")}>
          <Select
            value={draft.kind}
            disabled={!editable}
            onValueChange={(value) =>
              setDraft((current) => ({
                ...current,
                kind: value as PRWorkspaceCorrectionKind,
              }))
            }
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("prWorkspaces.corrections.kind")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(
                [
                  "factual",
                  "finding_quality",
                  "scope",
                  "pr_type",
                  "implementation",
                  "validation",
                  "repository_preference",
                ] as const
              ).map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {t(`prWorkspaces.corrections.kinds.${kind}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={t("prWorkspaces.corrections.applies")}>
          <Select
            value={draft.applicability}
            disabled={!editable}
            onValueChange={(value) =>
              setDraft((current) => ({
                ...current,
                applicability: value as PRWorkspaceCorrectionApplicability,
              }))
            }
          >
            <SelectTrigger
              className="w-full"
              aria-label={t("prWorkspaces.corrections.applies")}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["review", "implementation", "both"] as const).map((value) => (
                <SelectItem key={value} value={value}>
                  {t(`prWorkspaces.corrections.applicability.${value}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <CharterListField
          label={t("prWorkspaces.corrections.original")}
          value={draft.originalClaim}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, originalClaim: value }))
          }
        />
        <CharterListField
          label={t("prWorkspaces.corrections.corrected")}
          value={draft.correction}
          disabled={!editable}
          onChange={(value) =>
            setDraft((current) => ({ ...current, correction: value }))
          }
        />
        <Field
          label={t("prWorkspaces.corrections.reason")}
          className="md:col-span-2"
        >
          <Input
            value={draft.reason}
            disabled={!editable}
            aria-label={t("prWorkspaces.corrections.reason")}
            onChange={(event) =>
              setDraft((current) => ({
                ...current,
                reason: event.target.value,
              }))
            }
          />
        </Field>
        <div className="flex justify-end md:col-span-2">
          <Button
            type="submit"
            disabled={
              busy ||
              !editable ||
              !draft.originalClaim.trim() ||
              !draft.correction.trim()
            }
          >
            {t("prWorkspaces.corrections.add")}
          </Button>
        </div>
      </form>
      {workspace.corrections.length > 0 && (
        <div className="mt-4 space-y-2">
          {workspace.corrections.map((correction) => (
            <div
              key={correction.id}
              className="border-border flex flex-col gap-2 rounded-md border p-3 text-sm sm:flex-row sm:items-start sm:justify-between"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap gap-1.5">
                  <Badge variant="outline">{correction.kind}</Badge>
                  <Badge variant="outline">{correction.applicability}</Badge>
                </div>
                <p className="text-muted-foreground mt-2 line-through">
                  {correction.original_claim}
                </p>
                <p className="mt-1">{correction.correction}</p>
              </div>
              <Button
                size="sm"
                variant="outline"
                disabled={busy || !editable || correction.promoted}
                onClick={() => onPromote(correction.id)}
              >
                {t("prWorkspaces.corrections.promote")}
              </Button>
            </div>
          ))}
        </div>
      )}
    </StageCard>
  )
}

function ScopeMatrixPanel({ workspace }: { workspace: PRWorkspace }) {
  const { t } = useTranslation()
  const matrix = scopeMatrixCounts(workspace.findings)
  return (
    <Card size="sm" data-testid="pr-scope-matrix">
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          {t("prWorkspaces.scopeMatrix.title")}
        </CardTitle>
        <CardDescription>
          {t("prWorkspaces.scopeMatrix.description")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <table className="w-full table-fixed border-separate border-spacing-1 text-center text-xs">
          <caption className="sr-only">
            {t("prWorkspaces.scopeMatrix.description")}
          </caption>
          <thead>
            <tr>
              <th scope="col" className="w-16 text-left">
                {t("prWorkspaces.scopeMatrix.title")}
              </th>
              {(["XS", "S", "M", "L"] as const).map((size) => (
                <th key={size} scope="col">
                  {size}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {prWorkspaceScopeDistances.map((distance) => (
              <tr key={distance}>
                <th
                  scope="row"
                  className="truncate text-left font-normal"
                  title={t(`prWorkspaces.scope.${distance}`)}
                >
                  {distance.slice(0, 2)}
                  <span className="sr-only">
                    {` ${t(`prWorkspaces.scope.${distance}`)}`}
                  </span>
                </th>
                {(["XS", "S", "M", "L"] as const).map((size) => (
                  <td
                    key={size}
                    className={cn(
                      "border-border rounded border py-1",
                      matrix[distance][size] > 0 && "bg-muted font-medium",
                    )}
                  >
                    {matrix[distance][size]}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </CardContent>
    </Card>
  )
}

function GatePanel({
  workspace,
  onRespond,
  onOpenConfigs,
  busy,
}: {
  workspace: PRWorkspace
  onRespond: (gateID: string, fieldValues: Record<string, unknown>) => void
  onOpenConfigs?: () => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const pending = workspace.gates.find(
    (gate) => gate.state === "waiting_user" || gate.state === "waiting_gate",
  )
  const pendingTurn =
    pending?.turns.find(
      (turn) =>
        turn.kind === "human" &&
        (turn.status === "waiting_user" || turn.status === "waiting"),
    ) ??
    pending?.turns.find(
      (turn) => turn.status === "waiting_user" || turn.status === "waiting",
    ) ??
    pending?.turns.at(-1)
  const form = pendingTurn?.gate_form
  const waitingForHuman = pending?.state === "waiting_user" && Boolean(form)
  const [fieldValues, setFieldValues] = useState<Record<string, unknown>>({})
  useEffect(() => {
    setFieldValues({})
  }, [pending?.id, pendingTurn?.stage_id])
  const valid = form?.fields.every((field) =>
    isGateFieldValueValid(field, fieldValues[field.id]),
  )
  return (
    <Card size="sm" data-testid="pr-gates" aria-busy={busy}>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          {t("prWorkspaces.gates.title")}
        </CardTitle>
        <CardDescription>
          {t("prWorkspaces.gates.progress", {
            complete: workspace.gates.filter((gate) => gate.finished_at).length,
            total: workspace.gates.length,
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {pending ? (
          <div className="space-y-2" data-gate-id={pending.id}>
            <div>
              <Badge variant="outline">{pending.decision_point}</Badge>
              <p className="mt-2 text-sm font-medium">
                {pendingTurn?.title ?? pending.decision_point}
              </p>
            </div>
            <GateEvidence gate={pending} />
            {waitingForHuman ? (
              <>
                <p className="text-sm leading-snug">{form?.prompt}</p>
                {form?.fields.map((field) => (
                  <GenericGateField
                    field={field}
                    key={field.id}
                    value={fieldValues[field.id]}
                    onChange={(value) =>
                      setFieldValues((current) => ({
                        ...current,
                        [field.id]: value,
                      }))
                    }
                  />
                ))}
                <Button
                  className="w-full"
                  disabled={busy || !valid}
                  onClick={() => onRespond(pending.id, fieldValues)}
                >
                  Submit Gate response
                </Button>
              </>
            ) : (
              <p
                className="text-muted-foreground text-sm"
                role="status"
                aria-live="polite"
              >
                {t("prWorkspaces.gates.automaticPending")}
              </p>
            )}
          </div>
        ) : (
          <div className="space-y-2">
            <p className="text-muted-foreground text-sm">
              {t("prWorkspaces.gates.none")}
            </p>
            {workspace.gates.at(-1) && (
              <GateEvidence gate={workspace.gates.at(-1)!} />
            )}
          </div>
        )}
        {onOpenConfigs && (
          <Button
            className="w-full"
            variant="outline"
            size="sm"
            onClick={onOpenConfigs}
          >
            Configure Gates
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

function GenericGateField({
  field,
  value,
  onChange,
}: {
  field: PRWorkspaceGateField
  value: unknown
  onChange: (value: unknown) => void
}) {
  const required =
    field.required || (field.type === "select" && field.min_selections > 0)
  const label = `${field.label}${required ? " *" : ""}`
  if (field.type === "long-text") {
    return (
      <Field label={label}>
        <Textarea
          aria-label={field.label}
          required={field.required}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>
    )
  }
  if (field.type === "short-text") {
    return (
      <Field label={label}>
        <Input
          aria-label={field.label}
          required={field.required}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>
    )
  }
  if (field.type === "boolean") {
    return (
      <Field label={label}>
        <Select
          value={typeof value === "boolean" ? String(value) : "unset"}
          onValueChange={(next) =>
            onChange(next === "unset" ? undefined : next === "true")
          }
        >
          <SelectTrigger aria-label={field.label}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {!field.required && (
              <SelectItem value="unset">No answer</SelectItem>
            )}
            <SelectItem value="true">Yes</SelectItem>
            <SelectItem value="false">No</SelectItem>
          </SelectContent>
        </Select>
      </Field>
    )
  }
  if (field.max_selections === 1) {
    return (
      <Field label={label}>
        <Select
          value={typeof value === "string" ? value : "unset"}
          onValueChange={(next) =>
            onChange(next === "unset" ? undefined : next)
          }
        >
          <SelectTrigger aria-label={field.label}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {field.min_selections === 0 && (
              <SelectItem value="unset">No selection</SelectItem>
            )}
            {field.options.map((option) => (
              <SelectItem key={option.id} value={option.id}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
    )
  }
  const selected = Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : []
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-sm font-medium">{label}</legend>
      <div className="space-y-1 rounded-md border p-2">
        {field.options.map((option) => (
          <label
            className="hover:bg-muted flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm"
            key={option.id}
          >
            <input
              checked={selected.includes(option.id)}
              className="accent-primary size-4"
              type="checkbox"
              onChange={(event) =>
                onChange(
                  event.target.checked
                    ? [...selected, option.id]
                    : selected.filter((id) => id !== option.id),
                )
              }
            />
            {option.label}
          </label>
        ))}
      </div>
      <p className="text-muted-foreground text-xs">
        Select {field.min_selections}–{field.max_selections}.
      </p>
    </fieldset>
  )
}

function isGateFieldValueValid(field: PRWorkspaceGateField, value: unknown) {
  if (field.type === "short-text" || field.type === "long-text") {
    return (
      !field.required || (typeof value === "string" && value.trim().length > 0)
    )
  }
  if (field.type === "boolean")
    return !field.required || typeof value === "boolean"
  if (field.max_selections === 1) {
    const selected =
      typeof value === "string" &&
      field.options.some((option) => option.id === value)
    return selected || field.min_selections === 0
  }
  if (!Array.isArray(value)) return field.min_selections === 0
  const selections = value.filter(
    (entry): entry is string => typeof entry === "string",
  )
  return (
    selections.length >= field.min_selections &&
    selections.length <= field.max_selections &&
    new Set(selections).size === selections.length &&
    selections.every((selection) =>
      field.options.some((option) => option.id === selection),
    )
  )
}

function GateEvidence({ gate }: { gate: PRWorkspace["gates"][number] }) {
  const { t } = useTranslation()
  return (
    <details className="border-border rounded-md border p-2 text-xs">
      <summary className="cursor-pointer font-medium">
        {t("prWorkspaces.gates.evidence")}
      </summary>
      <dl className="text-muted-foreground mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-1">
        <dt>{t("prWorkspaces.gates.policy")}</dt>
        <dd className="truncate" title={gate.policy_revision}>
          {shortDigest(gate.policy_revision)}
        </dd>
        <dt>{t("prWorkspaces.gates.subject")}</dt>
        <dd className="truncate" title={gate.subject_revision}>
          {shortDigest(gate.subject_revision)}
        </dd>
      </dl>
      {gate.evidence && Object.keys(gate.evidence).length > 0 && (
        <div className="border-border mt-2 space-y-2 border-t pt-2">
          {(gate.evidence.charter_type || gate.evidence.charter_goal) && (
            <div>
              <strong>{t("prWorkspaces.gates.charterEvidence")}</strong>
              <p className="text-muted-foreground mt-0.5">
                {gate.evidence.charter_type && (
                  <span>
                    {t(`prWorkspaces.types.${gate.evidence.charter_type}`)}{" "}
                    ·{" "}
                  </span>
                )}
                {gate.evidence.charter_goal}
              </p>
            </div>
          )}
          {(gate.evidence.candidate_sha ||
            gate.evidence.changed_files?.length) && (
            <div>
              <strong>{t("prWorkspaces.gates.candidateEvidence")}</strong>
              {gate.evidence.candidate_sha && (
                <p className="text-muted-foreground mt-0.5 font-mono">
                  {shortSHA(gate.evidence.candidate_sha)}
                </p>
              )}
              {gate.evidence.changed_files &&
                gate.evidence.changed_files.length > 0 && (
                  <ul className="text-muted-foreground mt-1 space-y-0.5">
                    {gate.evidence.changed_files.map((file) => (
                      <li key={file} className="font-mono break-all">
                        {file}
                      </li>
                    ))}
                  </ul>
                )}
            </div>
          )}
          {gate.evidence.scope && (
            <ScopeAssessmentDetails scope={gate.evidence.scope} />
          )}
          {(gate.evidence.validation_state ||
            gate.evidence.validation_checks?.length) && (
            <div>
              <strong>{t("prWorkspaces.gates.validationEvidence")}</strong>
              {gate.evidence.validation_state && (
                <span className="ml-1">
                  · {t(`prWorkspaces.states.${gate.evidence.validation_state}`)}
                </span>
              )}
              {gate.evidence.validation_checks?.map((check) => (
                <p key={check.id} className="text-muted-foreground mt-0.5">
                  {check.name}: {check.status}
                </p>
              ))}
            </div>
          )}
          {(gate.evidence.finding_count != null ||
            gate.evidence.finding_ids?.length) && (
            <p>
              <strong>{t("prWorkspaces.gates.findingEvidence")}</strong>:{" "}
              {gate.evidence.finding_count ??
                gate.evidence.finding_ids?.length ??
                0}
            </p>
          )}
          {(gate.evidence.publication_kind ||
            gate.evidence.payload_digest ||
            gate.evidence.repository ||
            gate.evidence.review_summary ||
            gate.evidence.publication_findings?.length ||
            gate.evidence.issue_title ||
            gate.evidence.issue_body ||
            gate.evidence.issue_labels?.length ||
            gate.evidence.repair_summary) && (
            <div>
              <strong>{t("prWorkspaces.gates.publicationEvidence")}</strong>
              <p className="text-muted-foreground mt-0.5">
                {gate.evidence.publication_kind}
                {gate.evidence.payload_digest
                  ? ` · ${shortDigest(gate.evidence.payload_digest)}`
                  : ""}
              </p>
              {gate.evidence.expected_head_sha && (
                <p className="text-muted-foreground font-mono">
                  {t("prWorkspaces.workspace.headSha")}:{" "}
                  {shortSHA(gate.evidence.expected_head_sha)}
                </p>
              )}
              {gate.evidence.repository && (
                <p className="text-muted-foreground mt-1 break-all">
                  <strong>{t("prWorkspaces.gates.repository")}</strong>:{" "}
                  {gate.evidence.repository}
                </p>
              )}
              {gate.evidence.review_summary && (
                <div className="mt-2">
                  <strong>{t("prWorkspaces.gates.reviewPreview")}</strong>
                  <p className="text-muted-foreground mt-0.5 whitespace-pre-wrap">
                    {gate.evidence.review_summary}
                  </p>
                </div>
              )}
              {gate.evidence.publication_findings &&
                gate.evidence.publication_findings.length > 0 && (
                  <div className="mt-2">
                    <strong>{t("prWorkspaces.gates.reviewFindings")}</strong>
                    <ol className="mt-1 list-inside list-decimal space-y-1">
                      {gate.evidence.publication_findings.map((finding) => (
                        <li key={finding.id}>
                          <strong>{finding.title}</strong>
                          {finding.file && (
                            <span className="text-muted-foreground ml-1 font-mono break-all">
                              {finding.file}
                              {finding.line == null ? "" : `:${finding.line}`}
                            </span>
                          )}
                          <p className="text-muted-foreground ml-4 whitespace-pre-wrap">
                            {finding.message}
                          </p>
                        </li>
                      ))}
                    </ol>
                  </div>
                )}
              {(gate.evidence.issue_title ||
                gate.evidence.issue_body ||
                gate.evidence.issue_labels?.length) && (
                <div className="mt-2">
                  <strong>{t("prWorkspaces.gates.issuePreview")}</strong>
                  {gate.evidence.issue_title && (
                    <p className="mt-0.5 font-medium whitespace-pre-wrap">
                      {gate.evidence.issue_title}
                    </p>
                  )}
                  {gate.evidence.issue_body && (
                    <p className="text-muted-foreground mt-0.5 whitespace-pre-wrap">
                      {gate.evidence.issue_body}
                    </p>
                  )}
                  {gate.evidence.issue_labels &&
                    gate.evidence.issue_labels.length > 0 && (
                      <div className="mt-1 flex flex-wrap gap-1">
                        {gate.evidence.issue_labels.map((label) => (
                          <Badge key={label} variant="outline">
                            {label}
                          </Badge>
                        ))}
                      </div>
                    )}
                </div>
              )}
              {gate.evidence.repair_summary && (
                <div className="mt-2">
                  <strong>{t("prWorkspaces.gates.branchPreview")}</strong>
                  <p className="text-muted-foreground mt-0.5 whitespace-pre-wrap">
                    {gate.evidence.repair_summary}
                  </p>
                </div>
              )}
            </div>
          )}
        </div>
      )}
      <ol className="mt-2 space-y-1">
        {gate.turns.map((turn) => (
          <li key={turn.stage_id} className="bg-muted/40 rounded p-2">
            <div className="flex flex-wrap justify-between gap-1">
              <strong>
                {turn.title || t("prWorkspaces.gates.automaticCheck")}
              </strong>
              <span>
                {turn.kind} · {turn.status}
              </span>
            </div>
            {turn.field_values && Object.keys(turn.field_values).length > 0 && (
              <dl className="mt-1 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 gap-y-1">
                {Object.entries(turn.field_values).map(([fieldID, value]) => (
                  <div className="contents" key={fieldID}>
                    <dt className="font-medium">{fieldID}</dt>
                    <dd className="[overflow-wrap:anywhere]">
                      {Array.isArray(value)
                        ? value.join(", ")
                        : typeof value === "boolean"
                          ? value
                            ? "Yes"
                            : "No"
                          : String(value)}
                    </dd>
                  </div>
                ))}
              </dl>
            )}
          </li>
        ))}
      </ol>
    </details>
  )
}

function PublicationPanel({
  workspace,
  onPublish,
  onReconcile,
  busy,
}: {
  workspace: PRWorkspace
  onPublish: (phase: "review" | "implementation", findingIDs?: string[]) => void
  onReconcile: (publicationID: string) => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const provider = workspace.provider_snapshot
  const currentPhase = phaseIndex(workspace.workspace.phase)
  const completePhase = phaseIndex("complete")
  const reviewPhaseReady =
    currentPhase >= phaseIndex("triage") && currentPhase < completePhase
  const implementationPhaseReady = workspace.workspace.phase === "publication"
  const reviewFindingIDs = workspace.findings
    .filter((finding) => finding.disposition === "in_scope")
    .map((finding) => finding.id)
  const publications = workspace.publications.filter(
    (publication) =>
      publication.kind === "github_review" ||
      publication.kind === "branch_push",
  )
  const latestReview = latestPhasePublication(publications, "github_review")
  const latestImplementation = latestPhasePublication(
    publications,
    "branch_push",
  )
  const reviewLocked = publicationLocksCurrentHead(
    latestReview,
    provider.head_sha,
  )
  const implementationLocked = publicationLocksCurrentHead(
    latestImplementation,
    provider.head_sha,
  )
  const reviewAllowed =
    provider.state === "open" && provider.can_review && reviewPhaseReady
  const implementationAllowed =
    provider.state === "open" &&
    provider.head_writable &&
    implementationPhaseReady

  return (
    <StageCard
      id="pr-publication"
      title={t("prWorkspaces.publication.title")}
      icon={<IconBrandGithub />}
      badge={<Badge variant="outline">{publications.length}</Badge>}
    >
      <div className="grid gap-3 lg:grid-cols-2">
        <PublicationAction
          title={t("prWorkspaces.publication.review.title")}
          description={
            !provider.can_review
              ? t("prWorkspaces.publication.review.noCapability")
              : !reviewPhaseReady
                ? t("prWorkspaces.publication.review.wrongPhase")
                : t("prWorkspaces.publication.review.ready", {
                    count: reviewFindingIDs.length,
                  })
          }
          publication={latestReview}
          actionLabel={t("prWorkspaces.publication.review.action")}
          disabled={busy || !reviewAllowed || reviewLocked}
          onPublish={() => onPublish("review", reviewFindingIDs)}
        />
        <PublicationAction
          title={t("prWorkspaces.publication.implementation.title")}
          description={
            !provider.head_writable
              ? t("prWorkspaces.publication.implementation.noCapability")
              : !implementationPhaseReady
                ? t("prWorkspaces.publication.implementation.wrongPhase")
                : t("prWorkspaces.publication.implementation.ready")
          }
          publication={latestImplementation}
          actionLabel={t("prWorkspaces.publication.implementation.action")}
          disabled={busy || !implementationAllowed || implementationLocked}
          onPublish={() => onPublish("implementation")}
        />
      </div>
      {publications.length > 0 && (
        <ol className="mt-3 space-y-2">
          {[...publications].reverse().map((publication) => (
            <li
              key={publication.id}
              className="border-border flex flex-col gap-2 rounded-md border p-3 text-sm sm:flex-row sm:items-start sm:justify-between"
            >
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-1.5">
                  <strong>
                    {t(`prWorkspaces.publication.kinds.${publication.kind}`)}
                  </strong>
                  <StateBadge state={publication.state} />
                </div>
                {publication.public_error_code && (
                  <p className="text-destructive mt-1 text-xs break-words">
                    {t("prWorkspaces.publication.error")}:{" "}
                    <code>{publication.public_error_code}</code>
                  </p>
                )}
                {publication.external_id && !publication.external_url && (
                  <p className="text-muted-foreground mt-1 text-xs break-words">
                    {publication.external_id}
                  </p>
                )}
              </div>
              <div className="flex shrink-0 flex-wrap gap-2">
                {publication.external_url && (
                  <Button asChild size="sm" variant="outline">
                    <a
                      href={publication.external_url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      <IconExternalLink />
                      {t("prWorkspaces.publication.open")}
                    </a>
                  </Button>
                )}
                {publication.state === "unknown" && (
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={busy}
                    onClick={() => onReconcile(publication.id)}
                  >
                    {t("prWorkspaces.publication.reconcile")}
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ol>
      )}
    </StageCard>
  )
}

function PublicationAction({
  title,
  description,
  publication,
  actionLabel,
  disabled,
  onPublish,
}: {
  title: string
  description: string
  publication?: PRWorkspace["publications"][number]
  actionLabel: string
  disabled: boolean
  onPublish: () => void
}) {
  return (
    <div className="border-border flex min-w-0 flex-col rounded-md border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <strong className="text-sm">{title}</strong>
        {publication && <StateBadge state={publication.state} />}
      </div>
      <p className="text-muted-foreground mt-1 flex-1 text-xs">{description}</p>
      <div className="mt-3 flex justify-end">
        <Button size="sm" disabled={disabled} onClick={onPublish}>
          {actionLabel}
        </Button>
      </div>
    </div>
  )
}

function latestPhasePublication(
  publications: PRWorkspace["publications"],
  kind: "github_review" | "branch_push",
) {
  return publications.filter((publication) => publication.kind === kind).at(-1)
}

function publicationLocksCurrentHead(
  publication: PRWorkspace["publications"][number] | undefined,
  headSHA: string,
): boolean {
  if (!publication) return false
  if (
    publication.expected_head_sha &&
    publication.expected_head_sha !== headSHA
  ) {
    return false
  }
  return [
    "queued",
    "running",
    "waiting_gate",
    "waiting_user",
    "unknown",
    "succeeded",
  ].includes(publication.state)
}

function resolveDeferredIssueMode(
  snapshot: PRLifecycleRepositoryAssignmentSnapshot | undefined,
  providerOrigin: string,
  repositoryID: string,
): "off" | "ask" | "automatic" | undefined {
  if (!snapshot) return undefined
  const identity =
    `${providerOrigin.replace(/\/+$/u, "")}|${repositoryID}`.toLowerCase()
  const assignment = Object.entries(snapshot.repositoryAssignments).find(
    ([candidate]) =>
      candidate
        .split("|")
        .map((part, index) => (index === 0 ? part.replace(/\/+$/u, "") : part))
        .join("|")
        .toLowerCase() === identity,
  )
  const configID = assignment?.[1] ?? snapshot.defaultWorkflowConfiguration
  return snapshot.workflowConfigurations[configID]?.deferredIssues.mode
}

function DeferredPanel({
  workspace,
  onCommand,
  mode,
  runtimeRestartPending = false,
  settingsLoading = false,
  settingsError = false,
  onRetrySettings,
  busy,
}: {
  workspace: PRWorkspace
  onCommand: (command: DeferredCommand, onSuccess?: () => void) => void
  mode?: "off" | "ask" | "automatic"
  runtimeRestartPending?: boolean
  settingsLoading?: boolean
  settingsError?: boolean
  onRetrySettings?: () => void
  busy: boolean
}) {
  const { t } = useTranslation()
  const deferredFindingCount = workspace.findings.filter(
    (finding) => finding.disposition === "deferred",
  ).length
  const activeGroups = workspace.deferred_groups.filter(
    (group) => group.finding_ids.length > 0,
  )
  return (
    <Card size="sm" data-testid="pr-deferred" aria-busy={busy}>
      <CardHeader>
        <CardTitle role="heading" aria-level={2}>
          {t("prWorkspaces.deferred.title")}
        </CardTitle>
        <CardDescription>
          {t("prWorkspaces.deferred.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {runtimeRestartPending && (
          <div
            className="border-warning/50 bg-warning/10 flex items-start gap-2 rounded-md border p-2 text-xs"
            role="status"
          >
            <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
            <p>{t("prWorkspaces.deferred.runtimeRestartPending")}</p>
          </div>
        )}
        {settingsLoading && !mode && !runtimeRestartPending && (
          <p
            className="text-muted-foreground text-xs"
            role="status"
            aria-live="polite"
          >
            {t("prWorkspaces.deferred.settingsLoading")}
          </p>
        )}
        {settingsError && (
          <div
            className="border-destructive/40 bg-destructive/5 text-destructive flex flex-wrap items-center justify-between gap-2 rounded-md border p-2 text-xs"
            role="alert"
          >
            <p>
              {t(
                mode
                  ? "prWorkspaces.deferred.settingsRefreshError"
                  : "prWorkspaces.deferred.settingsError",
              )}
            </p>
            {onRetrySettings && (
              <Button
                size="sm"
                variant="outline"
                disabled={settingsLoading}
                onClick={onRetrySettings}
              >
                <IconRefresh />
                {t("prWorkspaces.deferred.retrySettings")}
              </Button>
            )}
          </div>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Badge variant="outline">
            {t("prWorkspaces.deferred.deferredFindings", {
              count: deferredFindingCount,
            })}
          </Badge>
          <Button
            size="sm"
            variant="outline"
            disabled={busy || deferredFindingCount === 0}
            onClick={() => onCommand({ action: "regroup" })}
          >
            <IconSparkles />
            {activeGroups.length === 0
              ? t("prWorkspaces.deferred.create")
              : t("prWorkspaces.deferred.regroup")}
          </Button>
        </div>
        {mode === "automatic" && (
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-muted-foreground text-xs">
              {t("prWorkspaces.deferred.automatic")}
            </p>
            <Button
              size="sm"
              variant="outline"
              disabled={busy || deferredFindingCount === 0}
              onClick={() => onCommand({ action: "automatic-sync" })}
            >
              <IconRefresh />
              {t("prWorkspaces.deferred.syncAutomatic")}
            </Button>
          </div>
        )}
        {mode === "off" && (
          <p className="text-muted-foreground text-xs">
            {t("prWorkspaces.deferred.off")}
          </p>
        )}
        {mode &&
          mode !== "off" &&
          !workspace.provider_snapshot.can_create_issue && (
            <p className="text-destructive text-xs">
              {t("prWorkspaces.deferred.noCapability")}
            </p>
          )}
        {activeGroups.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {deferredFindingCount > 0
              ? t("prWorkspaces.deferred.emptyActionable")
              : t("prWorkspaces.deferred.empty")}
          </p>
        ) : (
          activeGroups.map((group) => (
            <DeferredGroupCard
              key={group.id}
              workspace={workspace}
              group={group}
              mode={mode ?? "off"}
              busy={busy}
              onCommand={onCommand}
            />
          ))
        )}
      </CardContent>
    </Card>
  )
}

function DeferredGroupCard({
  workspace,
  group,
  mode,
  busy,
  onCommand,
}: {
  workspace: PRWorkspace
  group: PRWorkspace["deferred_groups"][number]
  mode: "off" | "ask" | "automatic"
  busy: boolean
  onCommand: (command: DeferredCommand, onSuccess?: () => void) => void
}) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [title, setTitle] = useState(group.title)
  const [body, setBody] = useState(group.body)
  const [labels, setLabels] = useState((group.labels ?? []).join(", "))
  const [splitIDs, setSplitIDs] = useState<string[]>([])
  const [mergeID, setMergeID] = useState("")
  const [issueURL, setIssueURL] = useState("")
  useEffect(() => {
    if (editing) return
    setTitle(group.title)
    setBody(group.body)
    setLabels((group.labels ?? []).join(", "))
  }, [editing, group.body, group.labels, group.title])
  const publication = group.publication_id
    ? workspace.publications.find(
        (candidate) => candidate.id === group.publication_id,
      )
    : undefined
  const historicalIssueFailures = workspace.publications
    .filter(
      (candidate) =>
        candidate.kind === "github_issue" &&
        candidate.target_id === group.id &&
        candidate.state === "failed" &&
        candidate.id !== publication?.id,
    )
    .sort((left, right) => right.updated_at.localeCompare(left.updated_at))
  const latestHistoricalFailure = historicalIssueFailures[0]
  const state =
    publication?.state ?? (group.existing_issue_url ? "succeeded" : "draft")
  const openIssueURL =
    group.existing_issue_url ||
    (publication?.kind === "github_issue" && publication.state === "succeeded"
      ? publication.external_url
      : undefined)
  const displayState = group.publication_suppressed
    ? t("prWorkspaces.deferred.paused")
    : state
  const mergeCandidates = workspace.deferred_groups.filter(
    (candidate) =>
      candidate.id !== group.id &&
      candidate.finding_ids.length > 0 &&
      !candidate.publication_id,
  )
  const locked = Boolean(group.publication_id)
  return (
    <article
      aria-label={group.title}
      data-deferred-group-id={group.id}
      className="border-border rounded-md border p-3 text-sm"
    >
      <div className="flex items-start justify-between gap-2">
        <strong className="min-w-0 break-words">{group.title}</strong>
        <Badge variant="outline">{displayState}</Badge>
      </div>
      <p className="text-muted-foreground mt-1 line-clamp-3 text-xs">
        {group.body}
      </p>
      <div className="mt-2 flex flex-wrap gap-1">
        <Badge variant="outline">{group.scope.distance}</Badge>
        <Badge variant="outline">{group.scope.size}</Badge>
        <Badge variant="outline">
          {group.finding_ids.length} {t("prWorkspaces.deferred.items")}
        </Badge>
        {group.labels?.map((label) => (
          <Badge key={label} variant="secondary">
            {label}
          </Badge>
        ))}
      </div>
      <ScopeAssessmentDetails scope={group.scope} />
      {group.publication_suppressed && (
        <div className="border-border bg-muted/40 mt-2 flex items-start gap-2 rounded-md border p-2 text-xs">
          <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          <div>
            <strong>{t("prWorkspaces.deferred.suppressedTitle")}</strong>
            <p className="text-muted-foreground mt-0.5">
              {t("prWorkspaces.deferred.suppressedDescription")}
            </p>
            {group.suppression_reason && (
              <p
                className="text-muted-foreground mt-1"
                title={group.suppression_reason}
              >
                {t(
                  `prWorkspaces.deferred.suppressionReasons.${group.suppression_reason}`,
                  { defaultValue: group.suppression_reason },
                )}
              </p>
            )}
          </div>
        </div>
      )}
      {latestHistoricalFailure && (
        <div
          className="border-border bg-muted/40 mt-2 rounded-md border p-2 text-xs"
          aria-label={t("prWorkspaces.deferred.publicationHistory")}
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <strong>{t("prWorkspaces.deferred.previousFailure")}</strong>
            <StateBadge state="failed" />
          </div>
          <p className="text-muted-foreground mt-1 break-words">
            <code>
              {latestHistoricalFailure.public_error_code ||
                t("prWorkspaces.deferred.unknownPublicationError")}
            </code>
            {" · "}
            {t("prWorkspaces.deferred.publicationAttempts", {
              count: latestHistoricalFailure.attempts,
            })}
          </p>
          <time
            className="text-muted-foreground mt-0.5 block"
            dateTime={latestHistoricalFailure.updated_at}
          >
            {new Date(latestHistoricalFailure.updated_at).toLocaleString()}
          </time>
          {historicalIssueFailures.length > 1 && (
            <p className="text-muted-foreground mt-1">
              {t("prWorkspaces.deferred.morePublicationFailures", {
                count: historicalIssueFailures.length - 1,
              })}
            </p>
          )}
        </div>
      )}
      {openIssueURL && (
        <Button asChild className="mt-2" size="sm" variant="outline">
          <a href={openIssueURL} target="_blank" rel="noreferrer">
            <IconExternalLink />
            {t("prWorkspaces.deferred.openIssue")}
          </a>
        </Button>
      )}
      <div className="mt-2 flex flex-wrap justify-end gap-2">
        <Button
          size="sm"
          variant="ghost"
          disabled={busy || locked}
          onClick={() => setEditing((value) => !value)}
        >
          {t("prWorkspaces.deferred.edit")}
        </Button>
        {state === "unknown" && mode !== "off" ? (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() =>
              onCommand({
                action: "reconcile",
                groupID: group.id,
                publicationID: publication?.id,
              })
            }
          >
            {t("prWorkspaces.deferred.reconcile")}
          </Button>
        ) : state === "draft" &&
          mode !== "off" &&
          (mode === "ask" || group.publication_suppressed) ? (
          <Button
            size="sm"
            disabled={busy || !workspace.provider_snapshot.can_create_issue}
            onClick={() => onCommand({ action: "publish", groupID: group.id })}
          >
            {group.publication_suppressed
              ? t("prWorkspaces.deferred.retryPublish")
              : t("prWorkspaces.deferred.publish")}
          </Button>
        ) : null}
      </div>
      {editing && (
        <div className="border-border mt-3 space-y-3 border-t pt-3">
          <Field label={t("prWorkspaces.deferred.groupTitle")}>
            <Input
              value={title}
              aria-label={t("prWorkspaces.deferred.groupTitle")}
              onChange={(event) => setTitle(event.target.value)}
            />
          </Field>
          <Field label={t("prWorkspaces.deferred.groupBody")}>
            <Textarea
              value={body}
              aria-label={t("prWorkspaces.deferred.groupBody")}
              onChange={(event) => setBody(event.target.value)}
            />
          </Field>
          <Field label={t("prWorkspaces.deferred.labels")}>
            <Input
              value={labels}
              aria-label={t("prWorkspaces.deferred.labels")}
              onChange={(event) => setLabels(event.target.value)}
            />
          </Field>
          <div className="flex justify-end">
            <Button
              size="sm"
              disabled={busy || !title.trim() || !body.trim()}
              onClick={() =>
                onCommand(
                  {
                    action: "update",
                    groupID: group.id,
                    title: title.trim(),
                    body: body.trim(),
                    labels: labels
                      .split(",")
                      .map((label) => label.trim())
                      .filter(Boolean),
                  },
                  () => setEditing(false),
                )
              }
            >
              {t("prWorkspaces.deferred.save")}
            </Button>
          </div>
          {group.finding_ids.length > 1 && (
            <div>
              <Label>{t("prWorkspaces.deferred.split")}</Label>
              <div className="mt-1 space-y-1">
                {group.finding_ids.map((findingID) => {
                  const finding = workspace.findings.find(
                    (item) => item.id === findingID,
                  )
                  return (
                    <label
                      key={findingID}
                      className="flex items-start gap-2 text-xs"
                    >
                      <input
                        type="checkbox"
                        checked={splitIDs.includes(findingID)}
                        onChange={(event) =>
                          setSplitIDs((current) =>
                            event.target.checked
                              ? [...current, findingID]
                              : current.filter((id) => id !== findingID),
                          )
                        }
                      />
                      <span>{finding?.title ?? findingID}</span>
                    </label>
                  )
                })}
              </div>
              <Button
                className="mt-2"
                size="sm"
                variant="outline"
                disabled={
                  busy ||
                  splitIDs.length === 0 ||
                  splitIDs.length >= group.finding_ids.length
                }
                onClick={() =>
                  onCommand({
                    action: "split",
                    groupID: group.id,
                    findingIDs: splitIDs,
                  })
                }
              >
                {t("prWorkspaces.deferred.splitSelected")}
              </Button>
            </div>
          )}
          {mergeCandidates.length > 0 && (
            <Field label={t("prWorkspaces.deferred.merge")}>
              <div className="flex gap-2">
                <Select value={mergeID} onValueChange={setMergeID}>
                  <SelectTrigger
                    className="min-w-0 flex-1"
                    aria-label={t("prWorkspaces.deferred.merge")}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {mergeCandidates.map((candidate) => (
                      <SelectItem key={candidate.id} value={candidate.id}>
                        {candidate.title}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || !mergeID}
                  onClick={() =>
                    onCommand({
                      action: "merge",
                      groupID: group.id,
                      groupIDs: [group.id, mergeID],
                      title: title.trim(),
                      body: body.trim(),
                    })
                  }
                >
                  {t("prWorkspaces.deferred.mergeAction")}
                </Button>
              </div>
            </Field>
          )}
          {mode !== "off" && !group.existing_issue_url && (
            <Field label={t("prWorkspaces.deferred.link")}>
              <div className="flex gap-2">
                <Input
                  value={issueURL}
                  aria-label={t("prWorkspaces.deferred.link")}
                  placeholder="https://github.com/owner/repository/issues/123"
                  onChange={(event) => setIssueURL(event.target.value)}
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={busy || !safeHTTPSURL(issueURL)}
                  onClick={() =>
                    onCommand({
                      action: "link",
                      groupID: group.id,
                      existingIssueURL: issueURL.trim(),
                    })
                  }
                >
                  {t("prWorkspaces.deferred.linkAction")}
                </Button>
              </div>
            </Field>
          )}
        </div>
      )}
    </article>
  )
}

function ActivityPanel({ workspace }: { workspace: PRWorkspace }) {
  const { t } = useTranslation()
  const activity = useMemo(() => {
    const sorted = [...workspace.activity].sort((left, right) =>
      right.created_at.localeCompare(left.created_at),
    )
    const collapsed: {
      item: PRWorkspace["activity"][number]
      repetitions: number
    }[] = []
    for (const item of sorted) {
      const previous = collapsed.at(-1)
      if (
        previous?.item.kind === item.kind &&
        previous.item.actor === item.actor &&
        previous.item.summary === item.summary &&
        previous.item.entity_id === item.entity_id
      ) {
        previous.repetitions += 1
      } else {
        collapsed.push({ item, repetitions: 1 })
      }
    }
    return collapsed.slice(0, 12)
  }, [workspace.activity])
  return (
    <Card size="sm" data-testid="pr-activity">
      <CardHeader>
        <CardTitle
          className="flex items-center gap-2"
          role="heading"
          aria-level={2}
        >
          <IconHistory className="size-4" />
          {t("prWorkspaces.activity.title")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {activity.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            {t("prWorkspaces.activity.empty")}
          </p>
        ) : (
          <ol className="border-border space-y-3 border-l pl-3">
            {activity.map(({ item, repetitions }) => (
              <li key={item.ordinal} className="text-xs">
                <p className="font-medium">
                  {item.summary}
                  {repetitions > 1 && (
                    <span className="text-muted-foreground ml-1 font-normal">
                      {t("prWorkspaces.activity.repeated", {
                        count: repetitions,
                      })}
                    </span>
                  )}
                </p>
                <p className="text-muted-foreground mt-0.5">
                  {item.actor} · {item.kind}
                </p>
                <time className="text-muted-foreground mt-0.5 block">
                  {new Date(item.created_at).toLocaleString()}
                </time>
              </li>
            ))}
          </ol>
        )}
      </CardContent>
    </Card>
  )
}

function StageCard({
  id,
  title,
  icon,
  badge,
  children,
}: {
  id: string
  title: string
  icon: React.ReactNode
  badge?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <Card
      size="sm"
      id={id}
      data-testid={`pr-stage-${id.replace(/^pr-/u, "")}`}
      className="scroll-mt-4"
    >
      <CardHeader className="border-b">
        <CardTitle
          className="flex items-center gap-2"
          role="heading"
          aria-level={2}
        >
          {icon}
          <span>{title}</span>
        </CardTitle>
        {badge && <div className="col-start-2 row-start-1">{badge}</div>}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}

function Field({
  label,
  children,
  className,
}: {
  label: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <Label>{label}</Label>
      {children}
    </div>
  )
}

function CharterListField({
  label,
  value,
  disabled = false,
  onChange,
}: {
  label: string
  value: string
  disabled?: boolean
  onChange: (value: string) => void
}) {
  return (
    <Field label={label}>
      <Textarea
        value={value}
        disabled={disabled}
        aria-label={label}
        onChange={(event) => onChange(event.target.value)}
        className="min-h-20"
      />
    </Field>
  )
}

function EvidenceMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-border rounded-md border p-3">
      <span className="text-muted-foreground block text-xs">{label}</span>
      <strong className="mt-1 block truncate text-sm">{value}</strong>
    </div>
  )
}

function CenteredState({
  text,
  action,
}: {
  text: string
  action?: React.ReactNode
}) {
  return (
    <div className="bg-background flex h-full min-h-64 flex-col items-center justify-center gap-3 p-6 text-center">
      <p className="text-muted-foreground text-sm">{text}</p>
      {action}
    </div>
  )
}

function charterToDraft(
  charter: ReturnType<typeof activePRWorkspaceCharter>,
): CharterDraft {
  return {
    prType: charter?.type ?? "fix",
    goal: charter?.goal ?? "",
    acceptanceCriteria: charter?.acceptance_criteria.join("\n") ?? "",
    includedAreas: charter?.included_areas.join("\n") ?? "",
    exclusions: charter?.excluded_areas.join("\n") ?? "",
    nonGoals: charter?.non_goals.join("\n") ?? "",
  }
}

function charterDraftToInput(
  draft: CharterDraft,
): Omit<
  PRWorkspaceCharterInput,
  "expected_version" | "request_id" | "expected_head_revision"
> {
  return {
    pr_type: draft.prType,
    goal: draft.goal.trim(),
    acceptance_criteria: lines(draft.acceptanceCriteria),
    included_areas: lines(draft.includedAreas),
    exclusions: lines(draft.exclusions),
    non_goals: lines(draft.nonGoals),
  }
}

function charterDraftMatches(
  draft: CharterDraft,
  charter: PRWorkspace["charters"][number],
): boolean {
  const input = charterDraftToInput(draft)
  return (
    input.pr_type === charter.type &&
    input.goal === charter.goal &&
    arraysEqual(input.acceptance_criteria, charter.acceptance_criteria) &&
    arraysEqual(input.included_areas, charter.included_areas) &&
    arraysEqual(input.exclusions, charter.excluded_areas) &&
    arraysEqual(input.non_goals, charter.non_goals)
  )
}

function arraysEqual(left: string[], right: string[]): boolean {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  )
}

function scopeGuidance(
  scope: PRWorkspaceScopeAssessment,
): "proceed" | "gate" | "classify_or_revise" | "revise_or_defer" | "defer" {
  if (!scope.type_compatible) return "revise_or_defer"
  if (scope.distance === "S0_exact") {
    return scope.size === "XS" || scope.size === "S" ? "proceed" : "gate"
  }
  if (scope.distance === "S1_necessary_adjacent") return "classify_or_revise"
  return "defer"
}

function lines(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
}

function shortSHA(value: string): string {
  return value.slice(0, 12)
}

function shortDigest(value: string): string {
  return value.length <= 20 ? value : `${value.slice(0, 20)}…`
}

function safeHTTPSURL(value: string): boolean {
  try {
    const parsed = new URL(value.trim())
    return (
      parsed.protocol === "https:" &&
      parsed.username === "" &&
      parsed.password === ""
    )
  } catch {
    return false
  }
}

function providerPullURL(provider: PRWorkspace["provider_snapshot"]): string {
  const origin = provider.provider_origin.replace(/\/+$/u, "")
  const repository = provider.repository
    .split("/")
    .map((part) => encodeURIComponent(part))
    .join("/")
  return `${origin}/${repository}/pull/${provider.pull_number}`
}
