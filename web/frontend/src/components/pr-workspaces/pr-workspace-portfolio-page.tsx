import {
  IconAdjustments,
  IconAlertTriangle,
  IconGitPullRequest,
  IconPlus,
  IconRefresh,
  IconSearch,
} from "@tabler/icons-react"
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { type FormEvent, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type PRWorkspace,
  PRWorkspaceAPIError,
  createPRWorkspace,
  createPRWorkspaceRequestID,
  getPRWorkspace,
  listPRWorkspaces,
} from "@/api/pr-workspaces"
import { PageHeader } from "@/components/page-header"
import {
  activePRWorkspaceCharter,
  needsUserAction,
} from "@/components/pr-workspaces/pr-workspace-model"
import {
  PhaseBadge,
  StateBadge,
  TypeBadge,
} from "@/components/pr-workspaces/pr-workspace-status"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"

export function PRWorkspacePortfolioPage({
  onOpenWorkspace,
  onOpenGateConfigs,
}: {
  onOpenWorkspace: (workspaceID: string) => void
  onOpenGateConfigs?: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [filter, setFilter] = useState("")
  const [intake, setIntake] = useState("")
  const [trackError, setTrackError] = useState("")
  const query = useQuery({
    queryKey: ["pr-workspaces"],
    queryFn: ({ signal }) => listPRWorkspaces({ limit: 100 }, signal),
    refetchInterval: 5_000,
  })
  const detailQueries = useQueries({
    queries: (query.data?.workspaces ?? []).map((workspace) => ({
      queryKey: ["pr-workspace", workspace.id],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        getPRWorkspace(workspace.id, signal),
      refetchInterval: 5_000,
    })),
  })
  const createMutation = useMutation({
    mutationFn: (pullRequestURL: string) =>
      createPRWorkspace({
        pull_request_url: pullRequestURL,
        request_id: createPRWorkspaceRequestID(),
      }),
    onSuccess: async (workspace) => {
      setIntake("")
      setTrackError("")
      await queryClient.invalidateQueries({ queryKey: ["pr-workspaces"] })
      onOpenWorkspace(workspace.workspace.id)
    },
    onError: (error) => {
      setTrackError(
        error instanceof PRWorkspaceAPIError
          ? error.message
          : t("prWorkspaces.portfolio.trackError"),
      )
    },
  })
  const workspaces = useMemo(() => {
    const normalized = filter.trim().toLowerCase()
    const values = detailQueries.flatMap((detail) =>
      detail.data ? [detail.data] : [],
    )
    if (!normalized) return values
    return values.filter(
      (workspace) =>
        workspace.workspace.repository.toLowerCase().includes(normalized) ||
        workspace.provider_snapshot.title.toLowerCase().includes(normalized) ||
        String(workspace.workspace.pull_number) === normalized ||
        workspace.provider_snapshot.author_login
          .toLowerCase()
          .includes(normalized),
    )
  }, [detailQueries, filter])
  const needsYou = workspaces.filter(needsUserAction)
  const remaining = workspaces.filter(
    (workspace) => !needsYou.includes(workspace),
  )

  const submitIntake = (event: FormEvent) => {
    event.preventDefault()
    const value = intake.trim()
    if (!value) return
    setTrackError("")
    createMutation.mutate(value)
  }

  return (
    <div
      className="bg-background flex h-full min-h-0 flex-col"
      data-testid="pr-workspace-portfolio"
      aria-busy={query.isPending || createMutation.isPending}
    >
      <PageHeader
        title={t("prWorkspaces.portfolio.title")}
        titleExtra={
          <Badge variant="outline" className="hidden sm:inline-flex">
            {t("prWorkspaces.portfolio.lifecycle")}
          </Badge>
        }
      >
        {onOpenGateConfigs && (
          <Button
            type="button"
            variant="outline"
            aria-label={t("prWorkspaces.portfolio.gates")}
            title={t("prWorkspaces.portfolio.gates")}
            onClick={onOpenGateConfigs}
          >
            <IconAdjustments />
            <span className="hidden sm:inline">
              {t("prWorkspaces.portfolio.gates")}
            </span>
          </Button>
        )}
        <Button
          type="button"
          size="icon"
          variant="outline"
          aria-label={t("prWorkspaces.portfolio.refresh")}
          title={t("prWorkspaces.portfolio.refresh")}
          onClick={() => {
            void query.refetch()
            void queryClient.invalidateQueries({ queryKey: ["pr-workspace"] })
          }}
          disabled={
            query.isFetching ||
            detailQueries.some((detail) => detail.isFetching)
          }
        >
          <IconRefresh
            className={cn(
              "size-4",
              (query.isFetching ||
                detailQueries.some((detail) => detail.isFetching)) &&
                "animate-spin",
            )}
          />
        </Button>
      </PageHeader>

      <div className="min-h-0 flex-1 overflow-auto px-4 pb-6 md:px-6">
        <div className="mx-auto flex w-full max-w-7xl flex-col gap-4">
          <Card size="sm">
            <CardContent className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(18rem,0.8fr)]">
              <form onSubmit={submitIntake} className="flex min-w-0 gap-2">
                <Input
                  value={intake}
                  onChange={(event) => setIntake(event.target.value)}
                  placeholder={t("prWorkspaces.portfolio.trackPlaceholder")}
                  aria-label={t("prWorkspaces.portfolio.trackLabel")}
                />
                <Button
                  type="submit"
                  disabled={!intake.trim() || createMutation.isPending}
                >
                  <IconPlus />
                  {t("prWorkspaces.portfolio.track")}
                </Button>
              </form>
              <label className="relative block min-w-0">
                <span className="sr-only">
                  {t("prWorkspaces.portfolio.filter")}
                </span>
                <IconSearch className="text-muted-foreground pointer-events-none absolute top-2.5 left-3 size-4" />
                <Input
                  value={filter}
                  onChange={(event) => setFilter(event.target.value)}
                  className="pl-9"
                  placeholder={t("prWorkspaces.portfolio.filterPlaceholder")}
                />
              </label>
              {trackError && (
                <p
                  role="alert"
                  className="text-destructive text-sm lg:col-span-2"
                >
                  {trackError}
                </p>
              )}
            </CardContent>
          </Card>

          {query.isPending ||
          detailQueries.some((detail) => detail.isPending) ? (
            <p className="text-muted-foreground py-12 text-center text-sm">
              {t("prWorkspaces.portfolio.loading")}
            </p>
          ) : query.isError ||
            detailQueries.some((detail) => detail.isError) ? (
            <div className="border-destructive/40 bg-destructive/5 text-destructive flex items-center gap-2 rounded-lg border p-4 text-sm">
              <IconAlertTriangle className="size-4" />
              {t("prWorkspaces.portfolio.loadError")}
            </div>
          ) : workspaces.length === 0 ? (
            <div className="border-border text-muted-foreground flex flex-col items-center gap-2 rounded-lg border border-dashed py-16 text-sm">
              <IconGitPullRequest className="size-8" />
              {t("prWorkspaces.portfolio.empty")}
            </div>
          ) : (
            <>
              {needsYou.length > 0 && (
                <WorkspaceGroup
                  title={t("prWorkspaces.portfolio.needsYou")}
                  workspaces={needsYou}
                  onOpenWorkspace={onOpenWorkspace}
                />
              )}
              {remaining.length > 0 && (
                <WorkspaceGroup
                  title={t("prWorkspaces.portfolio.allWork")}
                  workspaces={remaining}
                  onOpenWorkspace={onOpenWorkspace}
                />
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function WorkspaceGroup({
  title,
  workspaces,
  onOpenWorkspace,
}: {
  title: string
  workspaces: PRWorkspace[]
  onOpenWorkspace: (workspaceID: string) => void
}) {
  const { t } = useTranslation()
  return (
    <section aria-label={title} className="space-y-2">
      <div className="flex items-center gap-2">
        <h3 className="text-sm font-medium">{title}</h3>
        <Badge variant="secondary">{workspaces.length}</Badge>
      </div>
      <div className="grid gap-2 xl:grid-cols-2">
        {workspaces.map((workspace) => {
          const charter = activePRWorkspaceCharter(workspace)
          const openFindings = workspace.findings.filter(
            (finding) => finding.disposition === "open",
          ).length
          const deferredFindings = workspace.findings.filter(
            (finding) => finding.disposition === "deferred",
          ).length
          const pendingGates = workspace.gates.filter(
            (gate) =>
              gate.state === "waiting_gate" || gate.state === "waiting_user",
          ).length
          return (
            <button
              key={workspace.workspace.id}
              type="button"
              onClick={() => onOpenWorkspace(workspace.workspace.id)}
              className="border-border bg-card hover:bg-muted/50 focus-visible:ring-ring grid min-w-0 gap-3 rounded-lg border p-4 text-left transition-colors focus-visible:ring-2 focus-visible:outline-none sm:grid-cols-[minmax(0,1fr)_auto]"
            >
              <span className="min-w-0 space-y-2">
                <span className="flex min-w-0 items-center gap-2">
                  <IconGitPullRequest className="text-muted-foreground size-4 shrink-0" />
                  <span className="truncate font-medium">
                    {workspace.provider_snapshot.title ||
                      `${workspace.workspace.repository} #${workspace.workspace.pull_number}`}
                  </span>
                </span>
                <span className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                  <span>
                    {workspace.workspace.repository} #
                    {workspace.workspace.pull_number}
                  </span>
                  <span>{workspace.provider_snapshot.author_login}</span>
                  <span>{workspace.provider_snapshot.head_ref}</span>
                </span>
                <span className="flex flex-wrap gap-1.5">
                  <PhaseBadge phase={workspace.workspace.phase} />
                  <StateBadge state={workspace.workspace.execution_state} />
                  {charter && <TypeBadge type={charter.type} />}
                </span>
              </span>
              <span className="text-muted-foreground grid grid-cols-3 gap-3 text-center text-xs sm:self-center">
                <Metric
                  value={openFindings}
                  label={t("prWorkspaces.portfolio.open")}
                />
                <Metric
                  value={deferredFindings}
                  label={t("prWorkspaces.portfolio.deferred")}
                />
                <Metric
                  value={pendingGates}
                  label={t("prWorkspaces.portfolio.gateCount")}
                />
              </span>
            </button>
          )
        })}
      </div>
    </section>
  )
}

function Metric({ value, label }: { value: number; label: string }) {
  return (
    <span>
      <span className="text-foreground block text-base font-medium">
        {value}
      </span>
      <span>{label}</span>
    </span>
  )
}
