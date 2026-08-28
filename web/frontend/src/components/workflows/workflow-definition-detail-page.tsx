import { IconActivity, IconEdit } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useState } from "react"
import { toast } from "sonner"

import {
  checkWorkflowDependencies,
  getWorkflowDefinition,
  runWorkflow,
} from "@/api/workflows"
import { CollectionDetailShell } from "@/components/collection"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"

import {
  WorkflowRunPanel,
  optionalString,
  parseDeliveryJSONObject,
  workflowRunInitialInputValues,
  workflowRunInitialSecretValues,
  workflowRunInputsPayload,
  workflowRunPayloadValidationMessage,
  workflowRunSecretsPayload,
} from "./workflow-authoring-page"

export function WorkflowDefinitionDetailPage({
  workflowID,
  onBack,
  onEdit,
  onRuns,
  onRunCreated,
}: {
  workflowID: string
  onBack: () => void
  onEdit: () => void
  onRuns: () => void
  onRunCreated: (runID: string) => void
}) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["workflows", "definitions", workflowID],
    queryFn: ({ signal }) => getWorkflowDefinition(workflowID, signal),
    retry: false,
  })
  const workflow = query.data
  const dependency = useQuery({
    queryKey: ["workflows", "dependencies", "published", workflow?.ref],
    queryFn: ({ signal }) =>
      workflow == null
        ? Promise.reject(new Error("Workflow definition is unavailable."))
        : checkWorkflowDependencies({ ref: workflow.ref }, signal),
    enabled: workflow != null,
    retry: false,
  })
  const [inputValues, setInputValues] = useState<Record<string, string>>({})
  const [secretValues, setSecretValues] = useState<Record<string, string>>({})
  const [secretsJSON, setSecretsJSON] = useState("{}")
  const [session, setSession] = useState("")
  const [deliveryJSON, setDeliveryJSON] = useState("{}")
  const [formError, setFormError] = useState("")
  useEffect(() => {
    if (!workflow) return
    setInputValues(workflowRunInitialInputValues(workflow))
    setSecretValues(workflowRunInitialSecretValues(workflow))
  }, [workflow])
  const payloadError = workflowRunPayloadValidationMessage(
    workflow ?? null,
    inputValues,
    secretValues,
    secretsJSON,
    deliveryJSON,
  )
  const launch = useMutation({
    mutationFn: async () => {
      if (
        workflow == null ||
        dependency.data?.ready !== true ||
        !dependency.data.revision
      ) {
        throw new Error(
          "Wait for a current successful dependency check before running.",
        )
      }
      return runWorkflow({
        ref: workflow.ref,
        expected_dependency_revision: dependency.data.revision,
        inputs: workflowRunInputsPayload(workflow, inputValues),
        secrets: workflowRunSecretsPayload(workflow, secretValues, secretsJSON),
        session: optionalString(session),
        delivery: parseDeliveryJSONObject(deliveryJSON, "Delivery"),
        async: true,
      })
    },
    onSuccess: ({ result, error }) => {
      void queryClient.invalidateQueries({ queryKey: ["workflows", "runs"] })
      if (error) toast.error(`Workflow run ${result.status}: ${error}`)
      else toast.success("Workflow run started")
      onRunCreated(result.run_id)
    },
    onError: (error) => setFormError(errorMessage(error)),
  })
  const notFound = statusOf(query.error) === 404
  const canRun =
    workflow != null &&
    !workflow.error &&
    ["valid", "needs_review"].includes(workflow.status) &&
    dependency.data?.ready === true &&
    Boolean(dependency.data.revision) &&
    payloadError == null &&
    !launch.isPending

  return (
    <CollectionDetailShell
      title={workflow?.name || "Workflow definition"}
      identity={workflow?.ref}
      status={
        workflow ? (
          <Badge variant="outline">{title(workflow.status)}</Badge>
        ) : undefined
      }
      actions={
        workflow ? (
          <>
            <Button type="button" variant="outline" size="sm" onClick={onRuns}>
              <IconActivity /> Runs
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={onEdit}>
              <IconEdit /> Edit
            </Button>
          </>
        ) : undefined
      }
      loading={query.isPending}
      error={
        notFound
          ? undefined
          : query.error
            ? errorMessage(query.error)
            : undefined
      }
      notFound={notFound}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Back to workflows"
    >
      {workflow ? (
        <div className="grid gap-4">
          <WorkflowRunPanel
            workflows={[workflow]}
            workflow={workflow}
            selectedWorkflowRef={workflow.ref}
            dependencyState={
              dependency.isFetching
                ? "loading"
                : dependency.isError
                  ? "error"
                  : dependency.data
                    ? "current"
                    : "idle"
            }
            dependencyReport={dependency.data}
            inputValues={inputValues}
            secretValues={secretValues}
            secretsJSON={secretsJSON}
            session={session}
            deliveryJSON={deliveryJSON}
            onSelectWorkflow={() => undefined}
            onRetryDependencies={() => void dependency.refetch()}
            onInputChange={(name, value) =>
              setInputValues((current) => ({ ...current, [name]: value }))
            }
            onSecretChange={(name, value) =>
              setSecretValues((current) => ({ ...current, [name]: value }))
            }
            onSecretsJSONChange={setSecretsJSON}
            onSessionChange={setSession}
            onDeliveryJSONChange={setDeliveryJSON}
            onRun={async () => {
              setFormError("")
              try {
                await launch.mutateAsync()
                return true
              } catch {
                return false
              }
            }}
            running={launch.isPending}
            canRun={canRun}
            readinessMessage={
              formError ||
              payloadError ||
              (canRun
                ? "Ready to run."
                : "Wait for a current successful dependency check before running.")
            }
          />
        </div>
      ) : null}
    </CollectionDetailShell>
  )
}

function statusOf(error: unknown): number | undefined {
  return error != null && typeof error === "object" && "status" in error
    ? Number(error.status)
    : undefined
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "Workflow definition is unavailable."
}

function title(value: string): string {
  return value
    .replaceAll("_", " ")
    .replace(/^./, (letter) => letter.toUpperCase())
}
