import {
  IconAlertTriangle,
  IconCheck,
  IconDownload,
  IconRotateClockwise,
} from "@tabler/icons-react"
import { useState } from "react"

import type { WorkflowTemplateCatalogEntry } from "@/api/workflows"
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

import { WorkflowDefinitionInspector } from "./workflow-definition-inspector"

export function WorkflowTemplateCatalog({
  templates,
  loading,
  unavailable,
  unavailableMessage,
  installingName,
  disabled,
  disabledReason,
  onInstall,
}: {
  templates: WorkflowTemplateCatalogEntry[]
  loading: boolean
  unavailable: boolean
  unavailableMessage?: string
  installingName?: string
  disabled?: boolean
  disabledReason?: string
  onInstall: (name: string, overwrite: boolean) => void
}) {
  const [restoreTarget, setRestoreTarget] =
    useState<WorkflowTemplateCatalogEntry | null>(null)

  return (
    <section
      aria-labelledby="workflow-template-catalog-title"
      className="border-border border-t pt-4"
    >
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3
            id="workflow-template-catalog-title"
            className="text-sm font-medium"
          >
            Built-in templates
          </h3>
          <p className="text-muted-foreground mt-0.5 text-xs">
            Install a validated starting point, then edit it as a normal
            workflow.
          </p>
        </div>
        {!loading && !unavailable ? (
          <Badge variant="outline">{templates.length}</Badge>
        ) : null}
      </div>

      {loading ? (
        <div
          role="status"
          className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs"
        >
          Loading built-in templates…
        </div>
      ) : unavailable ? (
        <div
          role="alert"
          className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-3 text-xs"
        >
          {unavailableMessage ??
            "Built-in workflow templates are unavailable. Refresh to try again."}
        </div>
      ) : templates.length === 0 ? (
        <div className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs">
          No built-in templates are available.
        </div>
      ) : (
        <>
          {disabledReason ? (
            <div
              role="status"
              className="text-muted-foreground mb-2 rounded-md border border-dashed px-3 py-2 text-xs"
            >
              {disabledReason}
            </div>
          ) : null}
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-1 2xl:grid-cols-2">
            {templates.map((template) => {
              const installing = installingName === template.name
              return (
                <article
                  key={template.name}
                  aria-label={`${templateTitle(template.name)} template`}
                  className="border-border bg-background/60 flex min-w-0 flex-col gap-3 rounded-md border p-3"
                >
                  <div className="flex min-w-0 items-start justify-between gap-2">
                    <div className="min-w-0">
                      <h4 className="truncate text-sm font-medium">
                        {templateTitle(template.name)}
                      </h4>
                      <p className="text-muted-foreground mt-0.5 truncate font-mono text-[11px]">
                        {template.ref}
                      </p>
                    </div>
                    <TemplateStateBadge state={template.state} />
                  </div>

                  {template.state === "blocked" ? (
                    <p className="text-destructive flex items-start gap-1.5 text-xs">
                      <IconAlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                      <span>
                        {templateBlockedMessage(template.blocked_reason)}
                      </span>
                    </p>
                  ) : template.state === "modified" ? (
                    <p className="text-muted-foreground text-xs">
                      Local changes are preserved until you explicitly restore
                      the built-in version.
                    </p>
                  ) : null}

                  <WorkflowDefinitionInspector
                    target={{ kind: "template", name: template.name }}
                    defaultOpen={false}
                  />

                  <div className="mt-auto">
                    {template.state === "available" ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="w-full"
                        disabled={disabled || installing}
                        onClick={() => onInstall(template.name, false)}
                      >
                        <IconDownload className="size-4" />
                        {installing ? "Installing" : "Install"}
                      </Button>
                    ) : template.state === "modified" ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="w-full"
                        disabled={disabled || installing}
                        onClick={() => setRestoreTarget(template)}
                      >
                        <IconRotateClockwise className="size-4" />
                        {installing ? "Restoring" : "Restore built-in"}
                      </Button>
                    ) : template.state === "installed" ? (
                      <div className="text-muted-foreground flex items-center gap-1.5 text-xs">
                        <IconCheck className="size-4" />
                        Installed and byte-for-byte current
                      </div>
                    ) : null}
                  </div>
                </article>
              )
            })}
          </div>
        </>
      )}

      <AlertDialog
        open={restoreTarget != null}
        onOpenChange={(open) => {
          if (!open) {
            setRestoreTarget(null)
          }
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restore built-in template?</AlertDialogTitle>
            <AlertDialogDescription>
              This replaces local changes in{" "}
              <span className="font-mono">
                {restoreTarget?.ref ?? "this workflow"}
              </span>{" "}
              with the built-in version. This action cannot be undone from this
              screen.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={installingName != null}>
              Keep local changes
            </AlertDialogCancel>
            <AlertDialogAction
              disabled={restoreTarget == null || installingName != null}
              onClick={() => {
                if (restoreTarget != null) {
                  onInstall(restoreTarget.name, true)
                  setRestoreTarget(null)
                }
              }}
            >
              Restore built-in
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function TemplateStateBadge({
  state,
}: {
  state: WorkflowTemplateCatalogEntry["state"]
}) {
  return (
    <Badge
      variant={
        state === "blocked"
          ? "destructive"
          : state === "installed"
            ? "secondary"
            : "outline"
      }
      className="shrink-0 capitalize"
    >
      {state}
    </Badge>
  )
}

function templateTitle(name: string) {
  switch (name) {
    case "code-review":
      return "Code review"
    case "github-issue-triage":
      return "GitHub issue triage"
    case "github-pr-review":
      return "GitHub PR review"
    default:
      return name
        .split("-")
        .filter(Boolean)
        .map((part) => part[0]?.toUpperCase() + part.slice(1))
        .join(" ")
  }
}

function templateBlockedMessage(reason?: string) {
  switch (reason) {
    case "configuration_invalid":
      return "The workflow definitions directory is invalid."
    case "target_not_regular":
      return "The target is not a regular file. Resolve it manually."
    case "target_unavailable":
      return "The target cannot be read safely. Check its permissions."
    default:
      return "The target is blocked and must be resolved manually."
  }
}
