import { launcherFetch } from "@/api/http"

export interface GitWorkspaceLock {
  session_key: string
  agent_id?: string
  locked_at: string
  heartbeat_at: string
}

export interface GitWorkspaceInfo {
  id: string
  repo_id: string
  remote_url: string
  ref?: string
  path: string
  current_branch?: string
  preserved_branch?: string
  dirty: boolean
  size_bytes: number
  ignored_bytes: number
  created_at: string
  updated_at: string
  last_work_at?: string
  last_cleaned_at?: string
  locked_by?: GitWorkspaceLock
  dropped_at?: string
  status: "available" | "locked" | "dropped" | string
}

export interface GitRepositoryInfo {
  id: string
  remote_url: string
  first_seen_at: string
  last_seen_at: string
  last_work_at?: string
  workspace_count: number
  locked_count: number
  size_bytes: number
  ignored_bytes: number
}

export interface GitWorkspaceHistoryEntry {
  id: string
  time: string
  action: string
  repo_id?: string
  workspace_id?: string
  session_key?: string
  agent_id?: string
  detail?: string
}

export interface GitWorkspaceStats {
  root_dir: string
  max_total_size_bytes: number
  ignored_cleanup_delay_seconds: number
  drop_delay_seconds: number
  total_size_bytes: number
  ignored_bytes: number
  repository_count: number
  workspace_count: number
  locked_workspace_count: number
  repositories: GitRepositoryInfo[]
  workspaces: GitWorkspaceInfo[]
  history: GitWorkspaceHistoryEntry[]
}

export interface GitWorkspaceCleanupResult {
  workspace: GitWorkspaceInfo
  before_ignored_bytes: number
  after_ignored_bytes: number
}

export interface GitWorkspaceReconcileResult {
  cleaned: GitWorkspaceInfo[]
  dropped: GitWorkspaceInfo[]
  stats: GitWorkspaceStats
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text.trim() || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

export async function getGitWorkspaces(): Promise<GitWorkspaceStats> {
  return request<GitWorkspaceStats>("/api/git-workspaces")
}

export async function reconcileGitWorkspaces(): Promise<GitWorkspaceReconcileResult> {
  return request<GitWorkspaceReconcileResult>("/api/git-workspaces/reconcile", {
    method: "POST",
  })
}

export async function cleanupGitWorkspace(
  workspaceID: string,
): Promise<GitWorkspaceCleanupResult> {
  return request<GitWorkspaceCleanupResult>("/api/git-workspaces/cleanup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ workspace_id: workspaceID }),
  })
}

export async function dropGitWorkspace(workspaceID: string): Promise<{
  workspace: GitWorkspaceInfo
}> {
  return request<{ workspace: GitWorkspaceInfo }>(
    `/api/git-workspaces/${encodeURIComponent(workspaceID)}`,
    { method: "DELETE" },
  )
}
