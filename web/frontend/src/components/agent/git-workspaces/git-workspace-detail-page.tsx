import {
  IconCheck,
  IconClearAll,
  IconCopy,
  IconKey,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { toast } from "sonner"

import {
  type GitWorkspaceDetail,
  cleanupGitWorkspace,
  dropGitWorkspace,
  getGitWorkspace,
} from "@/api/git-workspaces"
import { CollectionDetailShell } from "@/components/collection"
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard"

import { formatBytes, formatDate } from "./git-workspace-format"

type DetailAction = "cleanup" | "drop"

export function GitWorkspaceDetailPage({
  workspaceID,
  onBack,
  onDropped,
}: {
  workspaceID: string
  onBack: () => void
  onDropped: () => void
}) {
  const queryClient = useQueryClient()
  const [action, setAction] = useState<DetailAction | null>(null)
  const query = useQuery({
    queryKey: ["git-workspaces", "detail", workspaceID],
    queryFn: ({ signal }) => getGitWorkspace(workspaceID, signal),
    refetchInterval: 10_000,
    retry: false,
  })
  const workspace = query.data?.workspace
  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ["git-workspaces"] })
  }
  const cleanup = useMutation({
    mutationFn: () => cleanupGitWorkspace(workspaceID),
    onSuccess: async (result) => {
      setAction(null)
      queryClient.setQueryData(
        ["git-workspaces", "detail", workspaceID],
        (current: typeof query.data) =>
          current
            ? {
                ...current,
                workspace: { ...current.workspace, ...result.workspace },
              }
            : current,
      )
      toast.success("Ignored files cleaned")
      await invalidate()
    },
    onError: (error) => toast.error(errorMessage(error, "Cleanup failed")),
  })
  const drop = useMutation({
    mutationFn: () => dropGitWorkspace(workspaceID),
    onSuccess: async () => {
      setAction(null)
      toast.success("Workspace dropped")
      await invalidate()
      onDropped()
    },
    onError: (error) => toast.error(errorMessage(error, "Drop failed")),
  })
  const notFound = statusOf(query.error) === 404
  const maintenanceDisabled =
    workspace == null || workspace.locked || workspace.status === "dropped"
  const pending = cleanup.isPending || drop.isPending

  return (
    <>
      <CollectionDetailShell
        title={workspace?.repository || "Git workspace"}
        identity={workspace?.branch || workspaceID}
        status={
          workspace ? (
            <>
              <Badge variant="outline">{humanize(workspace.status)}</Badge>
              {workspace.locked && workspace.status !== "locked" ? (
                <Badge variant="secondary">Locked</Badge>
              ) : null}
              {workspace.dirty ? (
                <Badge variant="secondary">Dirty</Badge>
              ) : null}
            </>
          ) : undefined
        }
        actions={
          workspace ? (
            <>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={query.isFetching}
                onClick={() => void query.refetch()}
                aria-label="Refresh git workspace"
                title="Refresh git workspace"
              >
                <IconRefresh />{" "}
                <span className="hidden sm:inline">Refresh</span>
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={maintenanceDisabled || pending}
                onClick={() => setAction("cleanup")}
                aria-label="Clean ignored files"
                title="Clean ignored files"
              >
                <IconClearAll /> <span className="hidden sm:inline">Clean</span>
              </Button>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                disabled={maintenanceDisabled || pending}
                onClick={() => setAction("drop")}
                aria-label="Drop workspace"
                title="Drop workspace"
              >
                <IconTrash /> <span className="hidden sm:inline">Drop</span>
              </Button>
            </>
          ) : undefined
        }
        loading={query.isPending}
        error={
          notFound
            ? undefined
            : query.error
              ? errorMessage(query.error, "Git workspace unavailable")
              : undefined
        }
        notFound={notFound}
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="Back to git workspaces"
      >
        {workspace ? <GitWorkspaceDetails workspace={workspace} /> : null}
      </CollectionDetailShell>
      <AlertDialog
        open={action != null}
        onOpenChange={(open) => {
          if (!open && !pending) setAction(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {action === "cleanup"
                ? "Clean ignored files?"
                : "Drop local checkout?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {action === "cleanup"
                ? "Ignored files will be removed from this exact checkout. Tracked files remain unchanged."
                : "The exact unlocked local checkout will be dropped. Workspace history remains available."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant={action === "drop" ? "destructive" : "default"}
              disabled={pending}
              onClick={(event) => {
                event.preventDefault()
                if (action === "cleanup") cleanup.mutate()
                else if (action === "drop") drop.mutate()
              }}
            >
              {action === "cleanup" ? <IconClearAll /> : <IconTrash />}
              {pending ? "Working…" : action === "cleanup" ? "Clean" : "Drop"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function GitWorkspaceDetails({ workspace }: { workspace: GitWorkspaceDetail }) {
  const pathClipboard = useCopyToClipboard()
  const remoteClipboard = useCopyToClipboard()
  const sshRemote = workspace.remote_url
    ? normalizeURLRemoteToSSH(workspace.remote_url)
    : null
  const displayedRemote =
    sshRemote || workspace.remote_url || workspace.repository
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <Card size="sm" className="md:col-span-2">
        <CardHeader>
          <CardTitle>Checkout</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4">
          <DetailRow
            label="Repository"
            value={displayedRemote}
            action={
              workspace.remote_url ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  onClick={() =>
                    void remoteClipboard.copy(
                      sshRemote || workspace.remote_url!,
                    )
                  }
                  aria-label="Copy repository remote"
                >
                  {remoteClipboard.isCopied ? (
                    <IconCheck />
                  ) : sshRemote ? (
                    <IconKey />
                  ) : (
                    <IconCopy />
                  )}
                </Button>
              ) : undefined
            }
          />
          {workspace.path ? (
            <DetailRow
              label="Checkout path"
              value={formatCheckoutPath(workspace.path)}
              title={workspace.path}
              action={
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => void pathClipboard.copy(workspace.path!)}
                  aria-label="Copy checkout path"
                >
                  {pathClipboard.isCopied ? <IconCheck /> : <IconCopy />}
                </Button>
              }
            />
          ) : null}
        </CardContent>
      </Card>
      <Card size="sm">
        <CardHeader>
          <CardTitle>Repository state</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3">
          <DetailRow label="Branch" value={workspace.branch || "—"} />
          <DetailRow label="Requested ref" value={workspace.ref || "—"} />
          <DetailRow
            label="Preserved branch"
            value={workspace.preserved_branch || "—"}
          />
          <DetailRow
            label="Working tree"
            value={workspace.dirty ? "Dirty" : "Clean"}
          />
          <DetailRow label="Status" value={humanize(workspace.status)} />
        </CardContent>
      </Card>
      <Card size="sm">
        <CardHeader>
          <CardTitle>Usage and lifecycle</CardTitle>
        </CardHeader>
        <CardContent className="grid gap-3">
          <DetailRow label="Size" value={formatBytes(workspace.size)} />
          <DetailRow label="Ignored" value={formatBytes(workspace.ignored)} />
          <DetailRow label="Created" value={formatDate(workspace.created)} />
          <DetailRow label="Updated" value={formatDate(workspace.updated)} />
          <DetailRow
            label="Last work"
            value={formatDate(workspace.last_work)}
          />
          <DetailRow
            label="Last cleanup"
            value={formatDate(workspace.last_cleaned)}
          />
          <DetailRow label="Dropped" value={formatDate(workspace.dropped)} />
        </CardContent>
      </Card>
      {workspace.locked_by ? (
        <Card size="sm" className="md:col-span-2">
          <CardHeader>
            <CardTitle>Active lock</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 sm:grid-cols-2">
            <DetailRow
              label="Agent"
              value={workspace.locked_by.agent_id || "—"}
            />
            <DetailRow
              label="Locked"
              value={formatDate(workspace.locked_by.locked_at)}
            />
            <DetailRow
              label="Heartbeat"
              value={formatDate(workspace.locked_by.heartbeat_at)}
            />
          </CardContent>
        </Card>
      ) : null}
    </div>
  )
}

function DetailRow({
  label,
  value,
  title,
  action,
}: {
  label: string
  value: string
  title?: string
  action?: React.ReactNode
}) {
  return (
    <div className="min-w-0">
      <div className="text-muted-foreground text-xs">{label}</div>
      <div className="mt-1 flex min-w-0 items-center gap-2">
        <div className="min-w-0 font-mono text-sm break-all" title={title}>
          {value}
        </div>
        {action}
      </div>
    </div>
  )
}

function formatCheckoutPath(path: string): string {
  const normalizedPath = normalizePath(path)
  const marker = "/checkouts/"
  const index = normalizedPath.lastIndexOf(marker)
  if (index >= 0) return normalizedPath.slice(index + 1)
  return normalizedPath.split("/").filter(Boolean).at(-1) || normalizedPath
}

function normalizePath(value: string): string {
  const normalized = value
    .trim()
    .replaceAll("\\", "/")
    .replace(/\/{2,}/g, "/")
  return normalized === "/" ? normalized : normalized.replace(/\/+$/, "")
}

function normalizeURLRemoteToSSH(remoteURL: string): string | null {
  const trimmed = remoteURL.trim()
  if (/^[^@\s]+@[^:\s]+:.+/.test(trimmed) || trimmed.startsWith("ssh://")) {
    return trimmed
  }
  try {
    const parsed = new URL(trimmed)
    const scheme = parsed.protocol.toLowerCase()
    if (!["http:", "https:", "git:"].includes(scheme)) return null
    if (
      parsed.username ||
      parsed.password ||
      parsed.search ||
      parsed.hash ||
      (scheme === "http:" && parsed.port && parsed.port !== "80") ||
      (scheme === "https:" && parsed.port && parsed.port !== "443") ||
      (scheme === "git:" && parsed.port)
    ) {
      return null
    }
    const host = parsed.hostname.trim().toLowerCase()
    const segments = parsed.pathname
      .split("/")
      .map((segment) => segment.trim())
      .filter(Boolean)
    if (
      !host ||
      segments.length < 2 ||
      segments.some((segment) => segment === "." || segment === "..") ||
      (host === "github.com" && segments.length !== 2)
    ) {
      return null
    }
    const path = segments.join("/")
    const canonicalPath = path.toLowerCase().endsWith(".git")
      ? `${path.slice(0, -4)}.git`
      : `${path}.git`
    return `git@${host}:${canonicalPath}`
  } catch {
    return null
  }
}

function statusOf(error: unknown): number | undefined {
  return error != null && typeof error === "object" && "status" in error
    ? Number(error.status)
    : undefined
}

function humanize(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback
}
