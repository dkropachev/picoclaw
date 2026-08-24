import {
  IconAlertTriangle,
  IconExternalLink,
  IconLoader2,
  IconShieldCheck,
} from "@tabler/icons-react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"

import {
  type DevelopmentCharterType,
  type DevelopmentGateField,
  type DevelopmentWorkspace,
  DevelopmentWorkspaceAPIError,
  confirmDevelopmentCharter,
  createDevelopmentRequestID,
  reconcileDevelopmentPublication,
  respondDevelopmentGate,
  saveDevelopmentCharter,
} from "@/api/development-workspaces"
import { humanize } from "@/components/development-workspaces/development-workspace-labels"
import type { DevelopmentAttentionPanel } from "@/components/development-workspaces/development-workspace-navigation"
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

export function DevelopmentActionPanel({
  workspace,
  requestedPanel,
  requestedEntityID,
}: {
  workspace: DevelopmentWorkspace
  requestedPanel?: DevelopmentAttentionPanel
  requestedEntityID?: string
}) {
  const queryClient = useQueryClient()
  const [fieldValues, setFieldValues] = useState<Record<string, unknown>>({})
  const [error, setError] = useState("")
  const clarificationCharter =
    workspace.charter?.clarification_needed && !workspace.charter.confirmed
      ? workspace.charter
      : undefined
  const [charterType, setCharterType] = useState<DevelopmentCharterType>(
    clarificationCharter?.type ?? "feature",
  )
  const [charterGoal, setCharterGoal] = useState(
    clarificationCharter?.goal ?? "",
  )
  const [charterCriteria, setCharterCriteria] = useState(
    clarificationCharter?.acceptance_criteria.join("\n") ?? "",
  )
  const [charterIncluded, setCharterIncluded] = useState(
    clarificationCharter?.included_areas.join("\n") ?? "",
  )
  const [charterExcluded, setCharterExcluded] = useState(
    clarificationCharter?.excluded_areas.join("\n") ?? "",
  )
  const [charterNonGoals, setCharterNonGoals] = useState(
    clarificationCharter?.non_goals.join("\n") ?? "",
  )
  const targetEntityID =
    requestedPanel === "charter" ||
    requestedPanel === "scope" ||
    requestedPanel === "publication"
      ? requestedEntityID
      : undefined
  const gate = useMemo(
    () =>
      workspace.gates.find(
        (candidate) =>
          candidate.id === targetEntityID && pending(candidate.state),
      ) ??
      (targetEntityID
        ? undefined
        : workspace.gates.find((candidate) => pending(candidate.state))),
    [targetEntityID, workspace.gates],
  )
  const publication = useMemo(
    () =>
      workspace.publications.find(
        (candidate) =>
          candidate.id === targetEntityID && candidate.state === "unknown",
      ) ??
      (targetEntityID
        ? undefined
        : workspace.publications.find(
            (candidate) => candidate.state === "unknown",
          )),
    [targetEntityID, workspace.publications],
  )
  const turn =
    gate?.turns.find(
      (candidate) =>
        candidate.kind === "human" &&
        (candidate.status === "waiting_user" || candidate.status === "waiting"),
    ) ?? gate?.turns.at(-1)
  const form = turn?.gate_form
  const canRespond = Boolean(
    gate?.state === "waiting_user" &&
    form &&
    form.fields.every((field) => validGateValue(field, fieldValues[field.id])),
  )

  useEffect(() => {
    setFieldValues({})
    setError("")
  }, [gate?.id, turn?.stage_id])

  useEffect(() => {
    if (!clarificationCharter) return
    setCharterType(clarificationCharter.type)
    setCharterGoal(clarificationCharter.goal)
    setCharterCriteria(clarificationCharter.acceptance_criteria.join("\n"))
    setCharterIncluded(clarificationCharter.included_areas.join("\n"))
    setCharterExcluded(clarificationCharter.excluded_areas.join("\n"))
    setCharterNonGoals(clarificationCharter.non_goals.join("\n"))
    setError("")
  }, [clarificationCharter])

  const updateWorkspace = (updated: DevelopmentWorkspace) => {
    queryClient.setQueryData(["development-workspace", workspace.id], updated)
    void queryClient.invalidateQueries({ queryKey: ["notifications"] })
    setError("")
  }
  const fail = (cause: unknown) => {
    void queryClient.invalidateQueries({
      queryKey: ["development-workspace", workspace.id],
    })
    setError(
      cause instanceof DevelopmentWorkspaceAPIError
        ? cause.message
        : "Required action could not be submitted.",
    )
  }
  const gateMutation = useMutation({
    mutationFn: () => {
      if (!gate) throw new Error("gate_missing")
      return respondDevelopmentGate(workspace.id, gate.id, {
        expected_version: workspace.version,
        request_id: createDevelopmentRequestID(),
        field_values: fieldValues,
      })
    },
    onSuccess: updateWorkspace,
    onError: fail,
  })
  const reconcileMutation = useMutation({
    mutationFn: () => {
      if (!publication || !workspace.head_revision) {
        throw new Error("publication_fence_missing")
      }
      return reconcileDevelopmentPublication(workspace.id, publication.id, {
        expected_version: workspace.version,
        expected_head_revision: workspace.head_revision,
        request_id: createDevelopmentRequestID(),
      })
    },
    onSuccess: updateWorkspace,
    onError: fail,
  })
  const saveCharterMutation = useMutation({
    mutationFn: () => {
      if (!clarificationCharter || !workspace.head_revision) {
        throw new Error("charter_fence_missing")
      }
      return saveDevelopmentCharter(workspace.id, {
        expected_version: workspace.version,
        expected_head_revision: workspace.head_revision,
        request_id: createDevelopmentRequestID(),
        charter: {
          type: charterType,
          goal: charterGoal.trim(),
          acceptance_criteria: charterLines(charterCriteria),
          included_areas: charterLines(charterIncluded),
          excluded_areas: charterLines(charterExcluded),
          non_goals: charterLines(charterNonGoals),
        },
      })
    },
    onSuccess: updateWorkspace,
    onError: fail,
  })
  const confirmCharterMutation = useMutation({
    mutationFn: () => {
      if (!clarificationCharter) throw new Error("charter_missing")
      return confirmDevelopmentCharter(workspace.id, {
        expected_version: workspace.version,
        expected_charter_revision: clarificationCharter.revision,
        request_id: createDevelopmentRequestID(),
      })
    },
    onSuccess: updateWorkspace,
    onError: fail,
  })

  if (!gate && !publication && !clarificationCharter) return null
  const showCharter = Boolean(
    clarificationCharter && (!gate || requestedPanel === "charter"),
  )
  const panel =
    requestedPanel ??
    (showCharter ? "charter" : inferPanel(gate?.decision_point))
  const canSaveCharter = Boolean(
    clarificationCharter &&
    workspace.head_revision &&
    charterGoal.trim() &&
    charterLines(charterCriteria).length > 0,
  )

  return (
    <Card
      size="sm"
      className="border-primary/40 bg-primary/5"
      data-testid="development-required-action"
    >
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <IconShieldCheck className="text-primary size-4" />
          Required action · {humanize(panel)}
        </CardTitle>
        <CardDescription>
          Resolve this item to allow development to continue.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {showCharter && clarificationCharter ? (
          <div className="space-y-3" data-charter-id={clarificationCharter.id}>
            <div>
              <Badge variant="secondary">Clarification needed</Badge>
              <p className="mt-2 text-sm font-medium">
                {clarificationCharter.clarification_question ??
                  "Review the charter before development continues."}
              </p>
            </div>
            <Label htmlFor="development-charter-type">Change type</Label>
            <Select
              value={charterType}
              onValueChange={(value) =>
                setCharterType(value as DevelopmentCharterType)
              }
            >
              <SelectTrigger id="development-charter-type">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {["fix", "feature", "refactor", "documentation", "test"].map(
                  (value) => (
                    <SelectItem key={value} value={value}>
                      {humanize(value)}
                    </SelectItem>
                  ),
                )}
              </SelectContent>
            </Select>
            <Label htmlFor="development-charter-goal">Goal</Label>
            <Input
              id="development-charter-goal"
              value={charterGoal}
              onChange={(event) => setCharterGoal(event.target.value)}
            />
            <CharterLinesField
              id="development-charter-criteria"
              label="Acceptance criteria · one per line"
              value={charterCriteria}
              onChange={setCharterCriteria}
            />
            <CharterLinesField
              id="development-charter-included"
              label="Included areas · one per line"
              value={charterIncluded}
              onChange={setCharterIncluded}
            />
            <CharterLinesField
              id="development-charter-excluded"
              label="Excluded areas · one per line"
              value={charterExcluded}
              onChange={setCharterExcluded}
            />
            <CharterLinesField
              id="development-charter-non-goals"
              label="Non-goals · one per line"
              value={charterNonGoals}
              onChange={setCharterNonGoals}
            />
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                disabled={!canSaveCharter || saveCharterMutation.isPending}
                onClick={() => saveCharterMutation.mutate()}
              >
                {saveCharterMutation.isPending && (
                  <IconLoader2 className="animate-spin" />
                )}
                Save clarified charter
              </Button>
              <Button
                type="button"
                variant="outline"
                disabled={confirmCharterMutation.isPending}
                onClick={() => confirmCharterMutation.mutate()}
              >
                {confirmCharterMutation.isPending && (
                  <IconLoader2 className="animate-spin" />
                )}
                Accept draft as-is
              </Button>
            </div>
          </div>
        ) : gate ? (
          <div className="space-y-3" data-gate-id={gate.id}>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">{gate.decision_point}</Badge>
              <span className="text-sm font-medium">
                {turn?.title ?? gate.decision_point}
              </span>
            </div>
            {form ? (
              <>
                <p className="text-sm">{form.prompt}</p>
                {form.fields.map((field) => (
                  <GateField
                    key={field.id}
                    field={field}
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
                  type="button"
                  disabled={!canRespond || gateMutation.isPending}
                  onClick={() => gateMutation.mutate()}
                >
                  {gateMutation.isPending && (
                    <IconLoader2 className="animate-spin" />
                  )}
                  Submit response
                </Button>
              </>
            ) : (
              <p role="status" className="text-muted-foreground text-sm">
                Automated policy evaluation is still running.
              </p>
            )}
          </div>
        ) : publication ? (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="destructive">Provider outcome unknown</Badge>
              <span className="text-sm">{humanize(publication.kind)}</span>
            </div>
            <p className="text-muted-foreground text-sm">
              Reconcile provider state before retrying publication. This check
              will not blindly repeat the external action.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                disabled={
                  !workspace.head_revision || reconcileMutation.isPending
                }
                onClick={() => reconcileMutation.mutate()}
              >
                {reconcileMutation.isPending && (
                  <IconLoader2 className="animate-spin" />
                )}
                Reconcile outcome
              </Button>
              {publication.external_url && (
                <Button asChild variant="outline">
                  <a
                    href={publication.external_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    Open provider <IconExternalLink />
                  </a>
                </Button>
              )}
            </div>
          </div>
        ) : null}
        {error && (
          <p
            role="alert"
            className="text-destructive flex items-center gap-2 text-sm"
          >
            <IconAlertTriangle className="size-4 shrink-0" /> {error}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

function CharterLinesField({
  id,
  label,
  value,
  onChange,
}: {
  id: string
  label: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Textarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  )
}

function charterLines(value: string): string[] {
  return value
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
}

function GateField({
  field,
  value,
  onChange,
}: {
  field: DevelopmentGateField
  value: unknown
  onChange: (value: unknown) => void
}) {
  const required =
    field.required || (field.type === "select" && field.min_selections > 0)
  const label = `${field.label}${required ? " *" : ""}`
  if (field.type === "long-text") {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={`development-gate-${field.id}`}>{label}</Label>
        <Textarea
          id={`development-gate-${field.id}`}
          required={field.required}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.target.value)}
        />
      </div>
    )
  }
  if (field.type === "short-text") {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={`development-gate-${field.id}`}>{label}</Label>
        <Input
          id={`development-gate-${field.id}`}
          required={field.required}
          value={typeof value === "string" ? value : ""}
          onChange={(event) => onChange(event.target.value)}
        />
      </div>
    )
  }
  if (field.type === "boolean") {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={`development-gate-${field.id}`}>{label}</Label>
        <Select
          value={typeof value === "boolean" ? String(value) : "unset"}
          onValueChange={(next) =>
            onChange(next === "unset" ? undefined : next === "true")
          }
        >
          <SelectTrigger id={`development-gate-${field.id}`} className="w-full">
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
      </div>
    )
  }
  if (field.max_selections === 1) {
    return (
      <div className="space-y-1.5">
        <Label htmlFor={`development-gate-${field.id}`}>{label}</Label>
        <Select
          value={typeof value === "string" ? value : "unset"}
          onValueChange={(next) =>
            onChange(next === "unset" ? undefined : next)
          }
        >
          <SelectTrigger id={`development-gate-${field.id}`} className="w-full">
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
      </div>
    )
  }
  const selected = Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : []
  return (
    <fieldset className="space-y-1.5">
      <legend className="text-sm font-medium">{label}</legend>
      <div className="border-border space-y-1 rounded-md border p-2">
        {field.options.map((option) => (
          <label
            key={option.id}
            className="hover:bg-muted flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm"
          >
            <input
              type="checkbox"
              className="accent-primary size-4"
              checked={selected.includes(option.id)}
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

function validGateValue(field: DevelopmentGateField, value: unknown): boolean {
  if (field.type === "short-text" || field.type === "long-text") {
    return !field.required || (typeof value === "string" && value.trim() !== "")
  }
  if (field.type === "boolean") {
    return !field.required || typeof value === "boolean"
  }
  if (field.max_selections === 1) {
    return (
      (typeof value === "string" &&
        field.options.some((option) => option.id === value)) ||
      field.min_selections === 0
    )
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

function pending(state: DevelopmentWorkspace["execution_state"]): boolean {
  return state === "waiting_user"
}

function inferPanel(decisionPoint?: string): DevelopmentAttentionPanel {
  if (decisionPoint?.includes("charter")) return "charter"
  if (decisionPoint?.includes("publish")) return "publication"
  if (decisionPoint?.includes("scope")) return "scope"
  return "overview"
}
