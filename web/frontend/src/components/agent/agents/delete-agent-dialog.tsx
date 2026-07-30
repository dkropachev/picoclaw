import { IconAlertTriangle, IconLoader2 } from "@tabler/icons-react"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type AgentDeleteBlocker,
  type AgentInfo,
  AgentsAPIError,
} from "@/api/agents"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"

export interface DeleteAgentSession {
  agent: AgentInfo
  revision: string
}

export function DeleteAgentDialog({
  session,
  onDelete,
  onConflict,
  onClose,
}: {
  session: DeleteAgentSession | null
  onDelete: (agent: AgentInfo, expectedRevision: string) => Promise<void>
  onConflict: () => Promise<void>
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState("")
  const [conflicted, setConflicted] = useState(false)
  const [latestListAvailable, setLatestListAvailable] = useState(false)
  const [blockers, setBlockers] = useState<AgentDeleteBlocker[]>([])
  const initializedSessionRef = useRef<DeleteAgentSession | null>(null)

  useEffect(() => {
    if (session == null || initializedSessionRef.current === session) return
    initializedSessionRef.current = session
    setDeleting(false)
    setError("")
    setConflicted(false)
    setLatestListAvailable(false)
    setBlockers([])
  }, [session])

  useEffect(() => {
    if (session == null) initializedSessionRef.current = null
  }, [session])

  const confirm = async () => {
    if (session == null || deleting || conflicted) return
    setDeleting(true)
    setError("")
    setBlockers([])
    try {
      await onDelete(session.agent, session.revision)
    } catch (caught) {
      if (
        caught instanceof AgentsAPIError &&
        caught.status === 409 &&
        caught.code === "config_revision_mismatch"
      ) {
        setConflicted(true)
        setLatestListAvailable(false)
        setError(
          t(
            "pages.agent.agents.conflict.delete",
            "Agent configuration changed. Close this dialog, review the latest agent, and reopen Delete if you still want to remove it.",
          ),
        )
        try {
          await onConflict()
          setLatestListAvailable(true)
        } catch {
          setLatestListAvailable(false)
          setError(
            t(
              "pages.agent.agents.conflict.refresh_failed",
              "Configuration changed, and the latest revision could not be loaded. Close and retry.",
            ),
          )
        }
      } else {
        if (caught instanceof AgentsAPIError) {
          setBlockers(caught.blockers ?? [])
        }
        setError(
          caught instanceof Error
            ? humanizeAPIMessage(caught.message)
            : t(
                "pages.agent.agents.toast.delete_failed",
                "Failed to delete the agent.",
              ),
        )
      }
    } finally {
      setDeleting(false)
    }
  }

  return (
    <AlertDialog
      open={session != null}
      onOpenChange={(open) => !open && !deleting && onClose()}
    >
      <AlertDialogContent size="sm">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("pages.agent.agents.delete.title", "Delete agent?")}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {t(
              "pages.agent.agents.delete.description",
              'The configured policy for "{{name}}" will be removed. Workspaces, sessions, runs, and history are not deleted.',
              { name: session?.agent.id },
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>

        {error && (
          <div
            className="bg-destructive/10 text-destructive rounded-lg p-3 text-sm"
            role="alert"
          >
            <div className="flex items-start gap-2">
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
              <p>{error}</p>
            </div>
            {blockers.length > 0 && (
              <ul className="mt-2 list-disc space-y-1 pl-6 text-xs">
                {blockers.map((blocker, index) => (
                  <li key={`${blocker.kind}-${blocker.name ?? ""}-${index}`}>
                    {formatBlocker(blocker)}
                  </li>
                ))}
              </ul>
            )}
            {conflicted && latestListAvailable && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-3"
                onClick={onClose}
              >
                {t(
                  "pages.agent.agents.conflict.review_latest",
                  "Close and review latest",
                )}
              </Button>
            )}
          </div>
        )}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleting}>
            {t("common.cancel", "Cancel")}
          </AlertDialogCancel>
          <Button
            type="button"
            variant="destructive"
            disabled={deleting || conflicted}
            onClick={() => void confirm()}
          >
            {deleting && <IconLoader2 className="size-4 animate-spin" />}
            {t("pages.agent.agents.delete.confirm", "Delete agent")}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function formatBlocker(blocker: AgentDeleteBlocker): string {
  const kind = blocker.kind.replaceAll("_", " ")
  const detail = blocker.name ?? blocker.agent_id
  return detail ? `${kind}: ${detail}` : kind
}

function humanizeAPIMessage(message: string): string {
  if (!/^[a-z0-9_]+$/.test(message)) return message
  const words = message.replaceAll("_", " ")
  return `${words.charAt(0).toLocaleUpperCase()}${words.slice(1)}.`
}
