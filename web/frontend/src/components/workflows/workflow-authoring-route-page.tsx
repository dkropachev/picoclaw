import { IconAlertTriangle, IconRefresh, IconTrash } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useRef, useState } from "react"
import { toast } from "sonner"

import {
  type WorkflowDevelopmentSession,
  discardWorkflowDevelopment,
  getWorkflowDefinition,
  getWorkflowDevelopment,
  startWorkflowDevelopment,
} from "@/api/workflows"
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

import { WorkflowAuthoringPage } from "./workflow-authoring-page"
import { WorkflowCapabilityCatalog } from "./workflow-capability-catalog"

export type WorkflowAuthoringRouteIntent =
  | { kind: "new" }
  | { kind: "edit"; workflowID: string }

export function WorkflowAuthoringRoutePage({
  intent,
  onBack,
  onOpenActiveNew,
  onOpenActiveEdit,
  onOpenRun,
  onPublished,
}: {
  intent: WorkflowAuthoringRouteIntent
  onBack: () => void
  onOpenActiveNew: () => void
  onOpenActiveEdit: (workflowID: string) => void
  onOpenRun: (runID: string) => void
  onPublished: (workflowID: string) => void
}) {
  const queryClient = useQueryClient()
  const definitionQuery = useQuery({
    queryKey: [
      "workflows",
      "definitions",
      intent.kind === "edit" ? intent.workflowID : null,
    ],
    queryFn: ({ signal }) =>
      intent.kind === "edit"
        ? getWorkflowDefinition(intent.workflowID, signal)
        : Promise.reject(new Error("No workflow definition was requested.")),
    enabled: intent.kind === "edit",
    retry: false,
  })
  const developmentQuery = useQuery({
    queryKey: ["workflows", "development"],
    queryFn: getWorkflowDevelopment,
    retry: false,
  })
  const [raceConflict, setRaceConflict] =
    useState<WorkflowDevelopmentSession | null>(null)
  const [discardOpen, setDiscardOpen] = useState(false)
  const startAttempt = useRef<string | null>(null)
  const active = raceConflict ?? developmentQuery.data?.session ?? null
  const definition = definitionQuery.data
  const matches =
    active == null
      ? false
      : routeMatchesSession(intent, definition?.ref, active)

  useEffect(() => {
    if (raceConflict == null || developmentQuery.data === undefined) return
    const authoritative = developmentQuery.data.session ?? null
    if (
      authoritative == null ||
      authoritative.id !== raceConflict.id ||
      authoritative.session_revision !== raceConflict.session_revision
    ) {
      setRaceConflict(null)
    }
  }, [developmentQuery.data, raceConflict])

  const startEdit = useMutation({
    mutationFn: async () => {
      if (intent.kind !== "edit" || definition == null) {
        throw new Error("Workflow definition is unavailable.")
      }
      return startWorkflowDevelopment({
        reason: "edit",
        ref: definition.ref,
        target_ref: definition.ref,
      })
    },
    onSuccess: async ({ session, conflict }) => {
      if (conflict) {
        queryClient.setQueryData(["workflows", "development"], { session })
        setRaceConflict(
          routeMatchesSession(intent, definition?.ref, session)
            ? null
            : session,
        )
        return
      }
      queryClient.setQueryData(["workflows", "development"], { session })
      await queryClient.invalidateQueries({
        queryKey: ["workflows", "development"],
      })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  useEffect(() => {
    if (
      intent.kind !== "edit" ||
      definition == null ||
      developmentQuery.isPending ||
      active != null ||
      startEdit.isPending ||
      startAttempt.current === definition.id
    ) {
      return
    }
    startAttempt.current = definition.id
    startEdit.mutate()
  }, [active, definition, developmentQuery.isPending, intent.kind, startEdit])

  const discard = useMutation({
    mutationFn: async () => {
      if (active == null) throw new Error("No workflow draft is active.")
      return discardWorkflowDevelopment({
        session_id: active.id,
        expected_session_revision: active.session_revision,
      })
    },
    onSuccess: async () => {
      setDiscardOpen(false)
      setRaceConflict(null)
      startAttempt.current = null
      await queryClient.invalidateQueries({
        queryKey: ["workflows", "development"],
      })
      toast.success("Active workflow draft discarded")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  const activeIssuedEditMatch =
    intent.kind === "edit" && active?.source_workflow_id === intent.workflowID
  const loading =
    developmentQuery.isPending ||
    (intent.kind === "edit" &&
      definitionQuery.isPending &&
      !activeIssuedEditMatch) ||
    startEdit.isPending
  const detailError =
    developmentQuery.error ??
    (intent.kind === "edit" && !activeIssuedEditMatch
      ? definitionQuery.error
      : null) ??
    startEdit.error
  const notFound = isNotFound(definitionQuery.error) && !activeIssuedEditMatch
  const title = intent.kind === "new" ? "New workflow" : "Edit workflow"

  return (
    <>
      <CollectionDetailShell
        title={title}
        identity={definition?.ref}
        status={
          active && matches ? <Badge variant="outline">Draft</Badge> : undefined
        }
        actions={
          <>
            <WorkflowCapabilityCatalog />
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label="Refresh"
              disabled={
                developmentQuery.isFetching || definitionQuery.isFetching
              }
              onClick={() => {
                void queryClient.invalidateQueries({
                  queryKey: ["workflows", "development"],
                })
                void queryClient.invalidateQueries({
                  queryKey: ["workflows", "compatibility"],
                })
                void queryClient.invalidateQueries({
                  queryKey: ["workflows", "templates"],
                })
                if (intent.kind === "edit") void definitionQuery.refetch()
              }}
            >
              <IconRefresh /> <span className="hidden sm:inline">Refresh</span>
            </Button>
          </>
        }
        loading={loading}
        error={
          notFound
            ? undefined
            : detailError
              ? errorMessage(detailError)
              : undefined
        }
        notFound={notFound}
        onBack={onBack}
        onRetry={() => {
          startAttempt.current = null
          void developmentQuery.refetch()
          if (intent.kind === "edit") void definitionQuery.refetch()
        }}
        backLabel="Back to workflows"
        contentClassName="max-w-[100rem]"
      >
        {active != null && !matches ? (
          <ActiveDraftConflict
            active={active}
            pending={discard.isPending}
            canOpen={
              Boolean(active.source_workflow_id) ||
              (active.reason === "new" && !active.source_workflow_ref)
            }
            onOpen={() => {
              if (active.source_workflow_id) {
                onOpenActiveEdit(active.source_workflow_id)
              } else if (
                active.reason === "new" &&
                !active.source_workflow_ref
              ) {
                onOpenActiveNew()
              }
            }}
            onDiscard={() => setDiscardOpen(true)}
          />
        ) : (
          <WorkflowAuthoringPage
            showTemplates={intent.kind === "new"}
            onDevelopmentConflict={(session) => {
              queryClient.setQueryData(["workflows", "development"], {
                session,
              })
              setRaceConflict(
                routeMatchesSession(intent, definition?.ref, session)
                  ? null
                  : session,
              )
            }}
            onOpenRun={onOpenRun}
            onPublished={(workflowID) => onPublished(workflowID)}
          />
        )}
      </CollectionDetailShell>

      <AlertDialog open={discardOpen} onOpenChange={setDiscardOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard active workflow draft?</AlertDialogTitle>
            <AlertDialogDescription>
              Only the exact active draft shown in this conflict will be
              discarded. The requested New or Edit route will remain open.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={discard.isPending}>
              Cancel
            </AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={discard.isPending}
              onClick={(event) => {
                event.preventDefault()
                discard.mutate()
              }}
            >
              <IconTrash />{" "}
              {discard.isPending ? "Discarding…" : "Discard draft"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function ActiveDraftConflict({
  active,
  pending,
  canOpen,
  onOpen,
  onDiscard,
}: {
  active: WorkflowDevelopmentSession
  pending: boolean
  canOpen: boolean
  onOpen: () => void
  onDiscard: () => void
}) {
  return (
    <section
      role="alert"
      className="mx-auto grid max-w-2xl gap-4 rounded-lg border border-amber-500/40 bg-amber-500/5 p-5"
    >
      <div className="flex items-start gap-3">
        <IconAlertTriangle className="mt-0.5 size-5 shrink-0 text-amber-600" />
        <div>
          <h2 className="font-semibold">Active workflow draft conflict</h2>
          <p className="text-muted-foreground mt-1 text-sm">
            Another authoring route owns the singleton backend draft. This route
            will not replace or revise it.
          </p>
          <p className="mt-3 font-mono text-xs">{active.target_workflow_ref}</p>
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" onClick={onOpen} disabled={!canOpen || pending}>
          Open active draft
        </Button>
        <Button
          type="button"
          variant="destructive"
          onClick={onDiscard}
          disabled={pending}
        >
          Discard draft
        </Button>
      </div>
    </section>
  )
}

function routeMatchesSession(
  intent: WorkflowAuthoringRouteIntent,
  requestedRef: string | undefined,
  session: WorkflowDevelopmentSession,
): boolean {
  if (intent.kind === "new") {
    return (
      session.reason === "new" &&
      !session.source_workflow_id &&
      !session.source_workflow_ref
    )
  }
  if (session.source_workflow_id) {
    return session.source_workflow_id === intent.workflowID
  }
  return requestedRef != null && session.source_workflow_ref === requestedRef
}

function isNotFound(error: unknown): boolean {
  return (
    error != null &&
    typeof error === "object" &&
    "status" in error &&
    error.status === 404
  )
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "Workflow authoring is unavailable."
}
