import {
  IconBrandGithub,
  IconCheck,
  IconCode,
  IconExternalLink,
  IconFileDiff,
  IconHistory,
  IconMessageCircle,
  IconRefresh,
} from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { type ReactNode, useEffect, useState } from "react"

import {
  type DevelopmentWorkspace,
  DevelopmentWorkspaceAPIError,
  type DevelopmentWorkspacePhase,
  getDevelopmentWorkspace,
} from "@/api/development-workspaces"
import { CollectionDetailShell } from "@/components/collection"
import { DevelopmentActionPanel } from "@/components/development-workspaces/development-action-panel"
import { DevelopmentChat } from "@/components/development-workspaces/development-chat"
import { DevelopmentCodeBrowser } from "@/components/development-workspaces/development-code-browser"
import { humanize } from "@/components/development-workspaces/development-workspace-labels"
import type { DevelopmentAttentionPanel } from "@/components/development-workspaces/development-workspace-navigation"
import {
  DevelopmentIntentBadge,
  DevelopmentPhaseBadge,
  DevelopmentStateBadge,
} from "@/components/development-workspaces/development-workspace-status"
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
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useIsMobile } from "@/hooks/use-mobile"
import { cn } from "@/lib/utils"

export type DevelopmentWorkspaceTab =
  | "overview"
  | "changes"
  | "files"
  | "activity"

export function DevelopmentWorkspacePage({
  workspaceID,
  tab,
  selectedPath,
  selectedRevision,
  attentionPanel,
  attentionEntityID,
  onBack,
  onTabChange,
  onPathChange,
}: {
  workspaceID: string
  tab: DevelopmentWorkspaceTab
  selectedPath?: string
  selectedRevision?: string
  attentionPanel?: DevelopmentAttentionPanel
  attentionEntityID?: string
  onBack: () => void
  onTabChange: (tab: DevelopmentWorkspaceTab) => void
  onPathChange: (path?: string, revision?: string) => void
}) {
  const query = useQuery({
    queryKey: ["development-workspace", workspaceID],
    queryFn: ({ signal }) => getDevelopmentWorkspace(workspaceID, signal),
    refetchInterval: 3_000,
    retry: false,
  })
  const notFound =
    query.error instanceof DevelopmentWorkspaceAPIError &&
    query.error.status === 404

  return (
    <div
      className="h-full min-h-0"
      data-testid="development-workspace"
      aria-busy={query.isPending}
    >
      <CollectionDetailShell
        title={query.data?.title ?? "Development workspace"}
        identity={<span className="font-mono text-xs">{workspaceID}</span>}
        status={
          query.data ? (
            <>
              <DevelopmentPhaseBadge phase={query.data.phase} />
              <DevelopmentStateBadge state={query.data.execution_state} />
            </>
          ) : undefined
        }
        actions={
          <Button
            type="button"
            size="icon-sm"
            variant="outline"
            aria-label="Refresh workspace"
            title="Refresh workspace"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <IconRefresh className={cn(query.isFetching && "animate-spin")} />
          </Button>
        }
        loading={query.isPending}
        notFound={notFound}
        error={
          !notFound && query.isError
            ? "Development workspace could not be loaded."
            : undefined
        }
        onBack={onBack}
        onRetry={() => void query.refetch()}
        backLabel="All development workspaces"
        contentClassName="h-full max-w-[100rem]"
      >
        {query.data && (
          <WorkspaceContent
            workspace={query.data}
            tab={tab}
            selectedPath={selectedPath}
            selectedRevision={selectedRevision}
            attentionPanel={attentionPanel}
            attentionEntityID={attentionEntityID}
            onTabChange={onTabChange}
            onPathChange={onPathChange}
          />
        )}
      </CollectionDetailShell>
    </div>
  )
}

function WorkspaceContent({
  workspace,
  tab,
  selectedPath,
  selectedRevision,
  attentionPanel,
  attentionEntityID,
  onTabChange,
  onPathChange,
}: {
  workspace: DevelopmentWorkspace
  tab: DevelopmentWorkspaceTab
  selectedPath?: string
  selectedRevision?: string
  attentionPanel?: DevelopmentAttentionPanel
  attentionEntityID?: string
  onTabChange: (tab: DevelopmentWorkspaceTab) => void
  onPathChange: (path?: string, revision?: string) => void
}) {
  const tabs: Array<{
    id: DevelopmentWorkspaceTab
    label: string
    icon: ReactNode
  }> = [
    { id: "overview", label: "Overview", icon: <IconCheck /> },
    { id: "changes", label: "Changes", icon: <IconFileDiff /> },
    { id: "files", label: "Files", icon: <IconCode /> },
    { id: "activity", label: "Activity", icon: <IconHistory /> },
  ]

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col pb-1">
      <div className="mx-auto flex min-h-0 w-full max-w-[100rem] flex-1 flex-col gap-3">
        <div className="flex flex-wrap items-center gap-1.5">
          <DevelopmentIntentBadge intent={workspace.intent} />
          <span className="text-muted-foreground ml-1 truncate text-xs">
            {workspace.repository}
          </span>
          {workspace.source.kind !== "brief" && workspace.source.url && (
            <Button asChild variant="ghost" size="xs">
              <a
                href={workspace.source.url}
                target="_blank"
                rel="noopener noreferrer"
              >
                <IconBrandGithub /> Source <IconExternalLink />
              </a>
            </Button>
          )}
        </div>

        <nav
          aria-label="Development workspace views"
          className="border-border flex max-w-full gap-1 overflow-x-auto border-b"
        >
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              aria-current={tab === item.id ? "page" : undefined}
              onClick={() => onTabChange(item.id)}
              className="text-muted-foreground hover:text-foreground aria-[current=page]:border-primary aria-[current=page]:text-foreground focus-visible:ring-ring flex h-9 shrink-0 items-center gap-1.5 border-b-2 border-transparent px-3 text-sm font-medium focus-visible:ring-2 focus-visible:outline-none [&>svg]:size-4"
            >
              {item.icon}
              {item.label}
            </button>
          ))}
        </nav>

        <div className="grid min-h-0 flex-1 gap-3 lg:grid-cols-[minmax(0,1fr)_22rem]">
          {/* eslint-disable jsx-a11y/no-noninteractive-tabindex -- Scrollable workspace evidence must be keyboard-focusable. */}
          <section
            tabIndex={0}
            aria-label={`${humanize(tab)} workspace view`}
            className="min-h-0 min-w-0 overflow-auto"
          >
            {tab === "overview" && (
              <Overview
                workspace={workspace}
                attentionPanel={attentionPanel}
                attentionEntityID={attentionEntityID}
              />
            )}
            {tab === "changes" && (
              <DevelopmentCodeBrowser
                workspaceID={workspace.id}
                candidateRevision={
                  selectedRevision ?? workspace.candidate_revision
                }
                selectedPath={selectedPath}
                onSelectPath={(path) =>
                  onPathChange(
                    path,
                    path ? workspace.candidate_revision : undefined,
                  )
                }
                changedFiles={workspace.changed_files}
              />
            )}
            {tab === "files" && (
              <DevelopmentCodeBrowser
                workspaceID={workspace.id}
                candidateRevision={
                  selectedRevision ?? workspace.candidate_revision
                }
                selectedPath={selectedPath}
                onSelectPath={(path) =>
                  onPathChange(
                    path,
                    path ? workspace.candidate_revision : undefined,
                  )
                }
              />
            )}
            {tab === "activity" && <Activity workspace={workspace} />}
          </section>
          {/* eslint-enable jsx-a11y/no-noninteractive-tabindex */}
          <WorkspaceChat
            workspaceID={workspace.id}
            candidateRevision={workspace.candidate_revision}
            openChat={
              attentionPanel === "chat" ||
              (attentionPanel === "charter" &&
                attentionEntityID?.startsWith("pms_") === true)
            }
          />
        </div>
      </div>
    </div>
  )
}

function WorkspaceChat({
  workspaceID,
  candidateRevision,
  openChat,
}: {
  workspaceID: string
  candidateRevision?: string
  openChat: boolean
}) {
  const isMobile = useIsMobile()
  const [open, setOpen] = useState(openChat)
  useEffect(() => {
    if (openChat) setOpen(true)
  }, [openChat])
  if (!isMobile) {
    return (
      <DevelopmentChat
        workspaceID={workspaceID}
        candidateRevision={candidateRevision}
      />
    )
  }
  return (
    <>
      <Button
        type="button"
        className="fixed right-4 bottom-4 z-40 shadow-lg"
        onClick={() => setOpen(true)}
      >
        <IconMessageCircle /> Development chat
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent
          side="bottom"
          className="h-[85dvh] max-h-[85dvh] gap-0 overflow-hidden p-0"
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Development chat</SheetTitle>
            <SheetDescription>
              Ask about the candidate or steer development.
            </SheetDescription>
          </SheetHeader>
          <div className="min-h-0 flex-1 p-2 [&>section]:h-full">
            <DevelopmentChat
              workspaceID={workspaceID}
              candidateRevision={candidateRevision}
            />
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}

function Overview({
  workspace,
  attentionPanel,
  attentionEntityID,
}: {
  workspace: DevelopmentWorkspace
  attentionPanel?: DevelopmentAttentionPanel
  attentionEntityID?: string
}) {
  const phases = lifecycleFor(workspace)
  const currentIndex = phases.indexOf(workspace.phase)
  return (
    <div className="grid gap-3 xl:grid-cols-2">
      {(workspace.charter?.clarification_needed === true ||
        workspace.gates.some((gate) => gate.state === "waiting_user") ||
        workspace.publications.some(
          (publication) => publication.state === "unknown",
        )) && (
        <div className="xl:col-span-2">
          <DevelopmentActionPanel
            workspace={workspace}
            requestedPanel={attentionPanel}
            requestedEntityID={attentionEntityID}
          />
        </div>
      )}
      <Card size="sm">
        <CardHeader>
          <CardTitle>Source</CardTitle>
          <CardDescription>
            {workspace.source.kind === "pull_request"
              ? "Existing pull request"
              : workspace.source.kind === "issue"
                ? "GitHub issue"
                : "Feature brief"}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          {workspace.source.kind === "brief" ? (
            <p className="whitespace-pre-wrap">{workspace.source.content}</p>
          ) : (
            <>
              {workspace.source.title && (
                <p className="font-medium">{workspace.source.title}</p>
              )}
              {workspace.source.body && (
                <p className="text-muted-foreground line-clamp-6 whitespace-pre-wrap">
                  {workspace.source.body}
                </p>
              )}
            </>
          )}
          {workspace.summary && (
            <p className="border-border text-muted-foreground border-t pt-2">
              {workspace.summary}
            </p>
          )}
        </CardContent>
      </Card>

      <Card size="sm">
        <CardHeader>
          <CardTitle>Lifecycle</CardTitle>
          <CardDescription>
            Current automated development progress
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ol className="space-y-1">
            {phases.map((phase, index) => {
              const complete = currentIndex >= 0 && index < currentIndex
              const current = phase === workspace.phase
              return (
                <li
                  key={phase}
                  className={cn(
                    "flex items-center gap-2 rounded-md px-2 py-1.5 text-sm",
                    current && "bg-muted font-medium",
                    !complete && !current && "text-muted-foreground",
                  )}
                >
                  <span
                    className={cn(
                      "border-border flex size-5 shrink-0 items-center justify-center rounded-full border text-[0.65rem]",
                      complete &&
                        "bg-primary text-primary-foreground border-primary",
                    )}
                    aria-hidden="true"
                  >
                    {complete ? <IconCheck className="size-3" /> : index + 1}
                  </span>
                  {humanize(phase)}
                </li>
              )
            })}
          </ol>
        </CardContent>
      </Card>

      <Card size="sm" className="xl:col-span-2">
        <CardHeader>
          <CardTitle>Validation</CardTitle>
          <CardDescription>
            Checks bound to current candidate revision
          </CardDescription>
        </CardHeader>
        <CardContent>
          {workspace.validation_checks.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              No validation evidence yet.
            </p>
          ) : (
            <ul className="divide-border divide-y">
              {workspace.validation_checks.map((check) => (
                <li
                  key={check.id}
                  className="flex min-w-0 items-start justify-between gap-3 py-2 first:pt-0 last:pb-0"
                >
                  <span className="min-w-0">
                    <span className="block truncate text-sm font-medium">
                      {check.name}
                    </span>
                    {check.summary && (
                      <span className="text-muted-foreground block text-xs">
                        {check.summary}
                      </span>
                    )}
                  </span>
                  <Badge
                    variant={
                      check.status === "failed" ? "destructive" : "outline"
                    }
                  >
                    {humanize(check.status)}
                  </Badge>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Activity({ workspace }: { workspace: DevelopmentWorkspace }) {
  if (workspace.activity.length === 0) {
    return (
      <div className="border-border text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm">
        No development activity yet.
      </div>
    )
  }
  return (
    <ol className="border-border bg-card divide-border divide-y rounded-lg border">
      {workspace.activity.map((item, index) => (
        <li key={item.id ?? `${item.ordinal ?? index}`} className="p-3">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{humanize(item.kind)}</Badge>
            <time className="text-muted-foreground text-xs">
              {formatTimestamp(item.created_at)}
            </time>
          </div>
          <p className="mt-1 text-sm">{item.summary}</p>
        </li>
      ))}
    </ol>
  )
}

function lifecycleFor(
  workspace: DevelopmentWorkspace,
): DevelopmentWorkspacePhase[] {
  return workspace.intent === "implement_feature"
    ? [
        "intake",
        "charter",
        "planning",
        "implementation",
        "validation",
        "completion_audit",
        "publication",
        "complete",
      ]
    : [
        "intake",
        "charter",
        "review",
        "triage",
        "implementation",
        "validation",
        "completion_audit",
        "publication",
        "complete",
      ]
}

function formatTimestamp(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}
