import {
  IconCheck,
  IconClearAll,
  IconCopy,
  IconGitBranch,
  IconKey,
  IconRefresh,
  IconRotateClockwise,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useMemo, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type GitWorkspaceInfo,
  cleanupGitWorkspace,
  dropGitWorkspace,
  getGitWorkspaces,
  reconcileGitWorkspaces,
} from "@/api/git-workspaces"
import { PageHeader } from "@/components/page-header"
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
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"
import { cn } from "@/lib/utils"

export function GitWorkspacesPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [dropTarget, setDropTarget] = useState<GitWorkspaceInfo | null>(null)

  const statsQuery = useQuery({
    queryKey: ["git-workspaces"],
    queryFn: getGitWorkspaces,
    refetchInterval: 10000,
  })

  const cleanupMutation = useMutation({
    mutationFn: cleanupGitWorkspace,
    onSuccess: async (result) => {
      toast.success(
        t("pages.agent.git_workspaces.clean_success", {
          defaultValue: "Ignored files cleaned",
        }),
      )
      queryClient.setQueryData(["git-workspaces"], (current: unknown) => {
        return replaceWorkspace(current, result.workspace)
      })
      await queryClient.invalidateQueries({ queryKey: ["git-workspaces"] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Cleanup failed")
    },
  })

  const dropMutation = useMutation({
    mutationFn: dropGitWorkspace,
    onSuccess: async () => {
      toast.success(
        t("pages.agent.git_workspaces.drop_success", {
          defaultValue: "Workspace dropped",
        }),
      )
      setDropTarget(null)
      await queryClient.invalidateQueries({ queryKey: ["git-workspaces"] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Drop failed")
    },
  })

  const reconcileMutation = useMutation({
    mutationFn: reconcileGitWorkspaces,
    onSuccess: async (result) => {
      queryClient.setQueryData(["git-workspaces"], result.stats)
      toast.success(
        t("pages.agent.git_workspaces.reconcile_success", {
          defaultValue: "Workspace maintenance completed",
        }),
      )
      await queryClient.invalidateQueries({ queryKey: ["git-workspaces"] })
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Maintenance failed")
    },
  })

  const stats = statsQuery.data
  const activeWorkspaces = useMemo(
    () =>
      (stats?.workspaces ?? []).filter((workspace) => !workspace.dropped_at),
    [stats?.workspaces],
  )
  const newestHistory = useMemo(
    () => (stats?.history ?? []).slice(0, 8),
    [stats?.history],
  )

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t("navigation.git_workspaces", "Git Workspaces")}
        titleExtra={
          stats ? (
            <Badge variant="secondary" className="gap-1 font-mono text-[11px]">
              <IconGitBranch className="size-3" />
              {stats.workspace_count}
            </Badge>
          ) : null
        }
      >
        <Button
          type="button"
          variant="outline"
          disabled={statsQuery.isFetching}
          onClick={() => statsQuery.refetch()}
          title={t("common.refresh", "Refresh")}
          aria-label={t("common.refresh", "Refresh")}
        >
          <IconRefresh className="size-4" />
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={reconcileMutation.isPending}
          onClick={() => reconcileMutation.mutate()}
        >
          <IconRotateClockwise className="size-4" />
          {t("pages.agent.git_workspaces.maintain", "Maintain")}
        </Button>
      </PageHeader>

      <div className="flex-1 overflow-auto p-3 lg:p-6">
        {statsQuery.isLoading ? (
          <div className="text-muted-foreground py-6 text-sm">
            {t("labels.loading")}
          </div>
        ) : statsQuery.error ? (
          <div className="text-destructive py-6 text-sm">
            {t(
              "pages.agent.git_workspaces.load_error",
              "Failed to load git workspaces",
            )}
          </div>
        ) : stats ? (
          <div className="mx-auto w-full max-w-[1200px] space-y-6">
            <section className="border-border bg-card grid gap-4 rounded-lg border p-4 sm:grid-cols-2 lg:grid-cols-4">
              <Metric
                label={t("pages.agent.git_workspaces.total_size", "Total")}
                value={formatBytes(stats.total_size_bytes)}
                detail={`${formatBytes(stats.max_total_size_bytes)} ${t("pages.agent.git_workspaces.limit", "limit")}`}
              />
              <Metric
                label={t("pages.agent.git_workspaces.ignored", "Ignored")}
                value={formatBytes(stats.ignored_bytes)}
                detail={formatDuration(stats.ignored_cleanup_delay_seconds)}
              />
              <Metric
                label={t("pages.agent.git_workspaces.repos", "Repos")}
                value={String(stats.repository_count)}
                detail={stats.root_dir}
              />
              <Metric
                label={t("pages.agent.git_workspaces.locked", "Locked")}
                value={String(stats.locked_workspace_count)}
                detail={`${activeWorkspaces.length} ${t("pages.agent.git_workspaces.active", "active")}`}
              />
            </section>

            <section className="space-y-3">
              <div className="flex items-center justify-between">
                <h3 className="text-foreground/90 text-sm font-semibold">
                  {t("pages.agent.git_workspaces.inventory", "Inventory")}
                </h3>
              </div>
              <div className="border-border overflow-hidden rounded-lg border">
                <div className="overflow-auto">
                  <table className="w-full min-w-[900px] text-sm">
                    <thead className="bg-muted/60 text-muted-foreground">
                      <tr className="text-left">
                        <th className="px-3 py-2 font-medium">
                          {t("pages.agent.git_workspaces.repo", "Repository")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("pages.agent.git_workspaces.branch", "Branch")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("pages.agent.git_workspaces.size", "Size")}
                        </th>
                        <th className="px-3 py-2 font-medium">
                          {t("pages.agent.git_workspaces.status", "Status")}
                        </th>
                        <th className="px-3 py-2 text-right font-medium">
                          {t("pages.agent.git_workspaces.actions", "Actions")}
                        </th>
                      </tr>
                    </thead>
                    <tbody className="divide-border divide-y">
                      {activeWorkspaces.length === 0 ? (
                        <tr>
                          <td
                            className="text-muted-foreground px-3 py-8 text-center"
                            colSpan={5}
                          >
                            {t(
                              "pages.agent.git_workspaces.empty",
                              "No git workspaces have been allocated yet.",
                            )}
                          </td>
                        </tr>
                      ) : (
                        activeWorkspaces.map((workspace) => (
                          <WorkspaceRow
                            key={workspace.id}
                            workspace={workspace}
                            workspaceRoot={stats.root_dir}
                            pendingCleanup={
                              cleanupMutation.variables === workspace.id &&
                              cleanupMutation.isPending
                            }
                            pendingDrop={
                              dropMutation.variables === workspace.id &&
                              dropMutation.isPending
                            }
                            onClean={() => cleanupMutation.mutate(workspace.id)}
                            onDrop={() => setDropTarget(workspace)}
                          />
                        ))
                      )}
                    </tbody>
                  </table>
                </div>
              </div>
            </section>

            <section className="space-y-3">
              <h3 className="text-foreground/90 text-sm font-semibold">
                {t("pages.agent.git_workspaces.history", "History")}
              </h3>
              <div className="border-border divide-border rounded-lg border">
                {newestHistory.length === 0 ? (
                  <div className="text-muted-foreground px-3 py-4 text-sm">
                    {t(
                      "pages.agent.git_workspaces.no_history",
                      "No workspace events yet.",
                    )}
                  </div>
                ) : (
                  newestHistory.map((entry) => (
                    <div
                      key={entry.id}
                      className="grid gap-2 px-3 py-2 text-sm sm:grid-cols-[160px_160px_1fr]"
                    >
                      <span className="text-muted-foreground">
                        {formatDate(entry.time)}
                      </span>
                      <span className="font-mono">{entry.action}</span>
                      <span className="text-muted-foreground min-w-0 break-all">
                        {entry.workspace_id ?? entry.repo_id ?? entry.detail}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </section>
          </div>
        ) : null}
      </div>

      <AlertDialog
        open={dropTarget != null}
        onOpenChange={(open) => {
          if (!open) setDropTarget(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(
                "pages.agent.git_workspaces.drop_title",
                "Drop local checkout?",
              )}
            </AlertDialogTitle>
            <AlertDialogDescription className="break-all">
              {dropTarget?.path}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (dropTarget) {
                  dropMutation.mutate(dropTarget.id)
                }
              }}
            >
              {t("pages.agent.git_workspaces.drop", "Drop")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Metric({
  label,
  value,
  detail,
}: {
  label: string
  value: string
  detail: string
}) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground text-xs font-medium">{label}</div>
      <div className="text-foreground mt-1 truncate text-xl font-semibold">
        {value}
      </div>
      <div className="text-muted-foreground mt-1 truncate text-xs">
        {detail}
      </div>
    </div>
  )
}

function WorkspaceRow({
  workspace,
  workspaceRoot,
  pendingCleanup,
  pendingDrop,
  onClean,
  onDrop,
}: {
  workspace: GitWorkspaceInfo
  workspaceRoot: string
  pendingCleanup: boolean
  pendingDrop: boolean
  onClean: () => void
  onDrop: () => void
}) {
  const { t } = useTranslation()
  const checkoutClipboard = useCopyToClipboard()
  const sshClipboard = useCopyToClipboard()
  const isLocked = workspace.status === "locked" || workspace.locked_by != null
  const remoteDisplay = getRemoteDisplay(workspace.remote_url)
  const sshRemote = remoteDisplay.sshRemote
  const visibleRemote = sshRemote ?? workspace.remote_url.trim()
  const visibleCheckoutPath = formatCheckoutPath(workspace.path, workspaceRoot)
  const copyCheckoutPathLabel = `${t(
    "pages.agent.git_workspaces.copy_checkout_path",
    "Copy checkout path",
  )}: ${workspace.path}`
  const copySSHRemoteLabel = sshRemote
    ? `${t("pages.agent.git_workspaces.copy_ssh_remote", "Copy SSH remote")}: ${sshRemote}`
    : t("pages.agent.git_workspaces.copy_ssh_remote", "Copy SSH remote")
  return (
    <tr className="hover:bg-muted/30">
      <td className="px-3 py-3 align-top">
        <div className="max-w-[420px] space-y-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <div className="text-foreground min-w-0 font-medium break-all">
              {visibleRemote}
            </div>
            {sshRemote && (
              <Button
                type="button"
                variant="outline"
                size="xs"
                className="border-primary/30 bg-primary/10 text-primary hover:bg-primary/15 hover:text-primary h-4 cursor-pointer gap-0.5 rounded-4xl px-1.5 font-mono text-[10px]"
                onClick={() => void sshClipboard.copy(sshRemote)}
                title={copySSHRemoteLabel}
                aria-label={copySSHRemoteLabel}
              >
                {sshClipboard.isCopied ? (
                  <IconCheck className="size-2.5 text-green-500" />
                ) : (
                  <IconKey className="size-2.5" />
                )}
                SSH
              </Button>
            )}
          </div>
          <div className="flex min-w-0 items-center gap-1.5">
            <div
              className="text-muted-foreground min-w-0 truncate font-mono text-xs"
              title={workspace.path}
            >
              {visibleCheckoutPath}
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground h-5 w-5 shrink-0 cursor-pointer"
              onClick={() => void checkoutClipboard.copy(workspace.path)}
              aria-label={copyCheckoutPathLabel}
              title={copyCheckoutPathLabel}
            >
              {checkoutClipboard.isCopied ? (
                <IconCheck className="size-3 text-green-500" />
              ) : (
                <IconCopy className="size-3" />
              )}
            </Button>
          </div>
        </div>
      </td>
      <td className="px-3 py-3 align-top">
        <div className="space-y-1">
          <div className="font-mono text-xs">
            {workspace.current_branch || workspace.ref || "-"}
          </div>
          {workspace.preserved_branch && (
            <div className="text-muted-foreground font-mono text-xs break-all">
              {workspace.preserved_branch}
            </div>
          )}
        </div>
      </td>
      <td className="px-3 py-3 align-top">
        <div className="space-y-1">
          <div>{formatBytes(workspace.size_bytes)}</div>
          <div className="text-muted-foreground text-xs">
            {formatBytes(workspace.ignored_bytes)}{" "}
            {t("pages.agent.git_workspaces.ignored", "Ignored")}
          </div>
        </div>
      </td>
      <td className="px-3 py-3 align-top">
        <Badge
          variant="secondary"
          className={cn(
            "capitalize",
            isLocked && "bg-amber-500/10 text-amber-700 dark:text-amber-300",
          )}
        >
          {workspace.status}
        </Badge>
        {workspace.dirty && (
          <div className="text-muted-foreground mt-2 text-xs">
            {t("pages.agent.git_workspaces.dirty", "Dirty")}
          </div>
        )}
      </td>
      <td className="px-3 py-3 align-top">
        <div className="flex justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={isLocked || pendingCleanup}
            onClick={onClean}
          >
            <IconClearAll className="size-4" />
            {t("pages.agent.git_workspaces.clean", "Clean")}
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="sm"
            disabled={isLocked || pendingDrop}
            onClick={onDrop}
          >
            <IconTrash className="size-4" />
            {t("pages.agent.git_workspaces.drop", "Drop")}
          </Button>
        </div>
      </td>
    </tr>
  )
}

function formatCheckoutPath(
  checkoutPath: string,
  workspaceRoot?: string,
): string {
  const normalizedPath = normalizeDisplayPath(checkoutPath)
  if (!normalizedPath) {
    return "-"
  }

  const normalizedRoot = normalizeDisplayPath(workspaceRoot ?? "")
  if (normalizedRoot) {
    if (normalizedPath === normalizedRoot) {
      return basename(normalizedPath)
    }
    if (normalizedRoot === "/" && normalizedPath.startsWith("/")) {
      return normalizedPath.slice(1)
    }
    if (normalizedPath.startsWith(`${normalizedRoot}/`)) {
      return normalizedPath.slice(normalizedRoot.length + 1)
    }
  }

  const checkoutsMarker = "/checkouts/"
  const checkoutsIndex = normalizedPath.lastIndexOf(checkoutsMarker)
  if (checkoutsIndex >= 0) {
    return normalizedPath.slice(checkoutsIndex + 1)
  }

  return basename(normalizedPath) || normalizedPath
}

function normalizeDisplayPath(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) {
    return ""
  }
  const normalized = trimmed.replace(/\\/g, "/").replace(/\/+/g, "/")
  return normalized === "/" ? normalized : normalized.replace(/\/+$/, "")
}

function basename(value: string): string {
  const segments = value.split("/").filter(Boolean)
  return segments.at(-1) ?? value
}

function getRemoteDisplay(remoteURL: string): {
  kind: "ssh" | "other"
  sshRemote?: string
} {
  const trimmed = remoteURL.trim()
  if (trimmed.startsWith("ssh://") || /^[^@\s]+@[^:\s]+:.+/.test(trimmed)) {
    return { kind: "ssh", sshRemote: trimmed }
  }
  const sshRemote = normalizeURLRemoteToSSH(trimmed)
  if (sshRemote) {
    return { kind: "ssh", sshRemote }
  }
  return { kind: "other" }
}

function normalizeURLRemoteToSSH(remoteURL: string): string | null {
  let parsed: URL
  try {
    parsed = new URL(remoteURL)
  } catch {
    return null
  }
  const scheme = parsed.protocol.toLowerCase().replace(/:$/, "")
  if (!["http", "https", "git"].includes(scheme)) {
    return null
  }
  if (
    parsed.username ||
    parsed.password ||
    parsed.search ||
    parsed.hash ||
    (scheme === "http" && parsed.port && parsed.port !== "80") ||
    (scheme === "https" && parsed.port && parsed.port !== "443") ||
    (scheme === "git" && parsed.port)
  ) {
    return null
  }
  const host = parsed.hostname.trim().toLowerCase()
  const segments = parsed.pathname
    .split("/")
    .map((segment) => segment.trim())
    .filter(Boolean)
  if (!host || segments.length < 2 || segments.some(isUnsafeRemoteSegment)) {
    return null
  }
  if (host === "github.com" && segments.length !== 2) {
    return null
  }
  const remotePath = ensureGitSuffix(segments.join("/"))
  return `git@${host}:${remotePath}`
}

function isUnsafeRemoteSegment(segment: string): boolean {
  return segment === "." || segment === ".."
}

function ensureGitSuffix(remotePath: string): string {
  if (remotePath.toLowerCase().endsWith(".git")) {
    return `${remotePath.slice(0, -4)}.git`
  }
  return `${remotePath}.git`
}

function replaceWorkspace(current: unknown, workspace: GitWorkspaceInfo) {
  if (!current || typeof current !== "object") {
    return current
  }
  const stats = current as { workspaces?: GitWorkspaceInfo[] }
  if (!Array.isArray(stats.workspaces)) {
    return current
  }
  return {
    ...stats,
    workspaces: stats.workspaces.map((item) =>
      item.id === workspace.id ? workspace : item,
    ),
  }
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "0 B"
  }
  const units = ["B", "KB", "MB", "GB", "TB"]
  let next = value
  let index = 0
  while (next >= 1024 && index < units.length - 1) {
    next /= 1024
    index += 1
  }
  return `${next >= 10 || index === 0 ? next.toFixed(0) : next.toFixed(1)} ${units[index]}`
}

function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return "0s"
  }
  if (seconds % 86400 === 0) {
    return `${seconds / 86400}d`
  }
  if (seconds % 3600 === 0) {
    return `${seconds / 3600}h`
  }
  if (seconds % 60 === 0) {
    return `${seconds / 60}m`
  }
  return `${seconds}s`
}

function formatDate(value?: string): string {
  if (!value) {
    return "-"
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return date.toLocaleString()
}
