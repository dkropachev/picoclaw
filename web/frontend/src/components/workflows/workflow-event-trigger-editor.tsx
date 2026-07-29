import {
  IconAlertTriangle,
  IconCheck,
  IconCode,
  IconPlus,
  IconRefresh,
  IconTrash,
} from "@tabler/icons-react"
import {
  type Dispatch,
  type SetStateAction,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react"

import {
  type WorkflowDevelopmentValidation,
  type WorkflowEventEntityTrigger,
  type WorkflowEventTrigger,
  type WorkflowEventTriggerInspection,
  inspectWorkflowEventTrigger,
  renderWorkflowEventTrigger,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"

export interface WorkflowEventTriggerInspectionState {
  yaml: string
  status: "loading" | "ready" | "error"
  triggerPresent: boolean
  editable: boolean
  reason?: string
}

export function WorkflowEventTriggerEditor({
  yaml,
  disabled,
  onYAMLChange,
  onInspectionChange,
  onOpenYAML,
}: {
  yaml: string
  disabled: boolean
  onYAMLChange: (yaml: string) => void
  onInspectionChange: (state: WorkflowEventTriggerInspectionState) => void
  onOpenYAML: () => void
}) {
  const latestYAMLRef = useRef(yaml)
  const latestInspectionRef = useRef<WorkflowEventTriggerInspection | null>(
    null,
  )
  const [inspection, setInspection] =
    useState<WorkflowEventTriggerInspection | null>(null)
  const [form, setForm] = useState<TriggerForm>(() => emptyTriggerForm())
  const [formDirty, setFormDirty] = useState(false)
  const [loading, setLoading] = useState(true)
  const [applying, setApplying] = useState(false)
  const [loadError, setLoadError] = useState("")
  const [applyError, setApplyError] = useState("")

  useEffect(() => {
    latestYAMLRef.current = yaml
  }, [yaml])

  useEffect(() => {
    latestInspectionRef.current = inspection
  }, [inspection])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setLoadError("")
    onInspectionChange({
      yaml,
      status: "loading",
      triggerPresent: false,
      editable: false,
    })
    const timeout = window.setTimeout(() => {
      void inspectWorkflowEventTrigger(yaml, controller.signal)
        .then((result) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          latestInspectionRef.current = result
          setInspection(result)
          setForm(triggerFormFrom(result.event_trigger))
          setFormDirty(false)
          setLoading(false)
          onInspectionChange({
            yaml,
            status: "ready",
            triggerPresent: result.event_trigger != null,
            editable: result.editable,
            reason: result.reason,
          })
        })
        .catch((error: unknown) => {
          if (controller.signal.aborted || latestYAMLRef.current !== yaml) {
            return
          }
          const message = errorMessage(error)
          setInspection(null)
          setLoadError(message)
          setLoading(false)
          onInspectionChange({
            yaml,
            status: "error",
            triggerPresent: false,
            editable: false,
            reason: message,
          })
        })
    }, 250)

    return () => {
      window.clearTimeout(timeout)
      controller.abort()
    }
  }, [onInspectionChange, yaml])

  const updateForm: Dispatch<SetStateAction<TriggerForm>> = (next) => {
    setForm(next)
    setFormDirty(true)
    setApplyError("")
  }
  const conversion = useMemo(() => triggerFromForm(form), [form])
  const canApply =
    !disabled &&
    !loading &&
    !applying &&
    inspection?.editable === true &&
    formDirty &&
    conversion.errors.length === 0

  const apply = async () => {
    if (!canApply || inspection == null) {
      return
    }
    const submittedYAML = yaml
    const submittedRevision = inspection.revision
    setApplying(true)
    setApplyError("")
    try {
      const result = await renderWorkflowEventTrigger({
        yaml: submittedYAML,
        revision: submittedRevision,
        event_trigger: conversion.trigger,
      })
      if (
        latestYAMLRef.current !== submittedYAML ||
        latestInspectionRef.current?.revision !== submittedRevision
      ) {
        setApplyError(
          "The YAML changed while the trigger was rendering. Review the latest draft and apply again.",
        )
        return
      }
      latestYAMLRef.current = result.yaml
      latestInspectionRef.current = result
      setInspection(result)
      setForm(triggerFormFrom(result.event_trigger))
      setFormDirty(false)
      onYAMLChange(result.yaml)
      onInspectionChange({
        yaml: result.yaml,
        status: "ready",
        triggerPresent: result.event_trigger != null,
        editable: result.editable,
        reason: result.reason,
      })
    } catch (error) {
      setApplyError(errorMessage(error))
    } finally {
      setApplying(false)
    }
  }

  const reset = () => {
    if (inspection == null) {
      return
    }
    setForm(triggerFormFrom(inspection.event_trigger))
    setFormDirty(false)
    setApplyError("")
  }

  return (
    <div className="flex h-full min-h-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-auto p-4">
        <div className="mx-auto grid w-full max-w-4xl gap-4">
          <div className="border-border bg-muted/30 rounded-md border p-3 text-xs">
            <div className="font-medium">Deterministic event routing</div>
            <p className="text-muted-foreground mt-1 leading-relaxed">
              Patterns are fully anchored and support <code>*</code> and{" "}
              <code>?</code>. Values within one field use OR; populated fields
              use AND. Source, connector, event type, and entity type matching
              is case-insensitive. IDs and attribute values are case-sensitive.
            </p>
          </div>

          {loading ? (
            <div
              role="status"
              className="text-muted-foreground flex min-h-32 items-center justify-center text-sm"
            >
              Inspecting workflow YAML…
            </div>
          ) : loadError ? (
            <EditorBoundaryMessage
              title="Trigger builder unavailable"
              message={loadError}
              onOpenYAML={onOpenYAML}
            />
          ) : inspection?.editable === false ? (
            <EditorBoundaryMessage
              title="Edit this trigger in YAML"
              message={
                inspection.reason ??
                "This YAML shape cannot be changed safely by the structured editor."
              }
              onOpenYAML={onOpenYAML}
            />
          ) : (
            <>
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <Label
                    htmlFor="workflow-event-trigger-enabled"
                    className="text-sm font-medium"
                  >
                    Durable event trigger
                  </Label>
                  <p className="text-muted-foreground mt-1 text-xs">
                    Match normalized GitHub, chat, email, or webhook events
                    before any workflow action runs.
                  </p>
                </div>
                <Switch
                  id="workflow-event-trigger-enabled"
                  checked={form.enabled}
                  onCheckedChange={(enabled) =>
                    updateForm((current) => ({ ...current, enabled }))
                  }
                  disabled={disabled || applying}
                  aria-label="Enable durable event trigger"
                />
              </div>

              {form.enabled ? (
                <fieldset
                  disabled={disabled || applying}
                  className="grid min-w-0 gap-5"
                >
                  <legend className="sr-only">Event trigger filters</legend>
                  <div className="grid gap-4 md:grid-cols-3">
                    <PatternField
                      id="workflow-event-sources"
                      label="Sources"
                      value={form.sources}
                      placeholder={"github\nchannel\ndeltachat"}
                      onChange={(sources) =>
                        updateForm((current) => ({ ...current, sources }))
                      }
                    />
                    <PatternField
                      id="workflow-event-connectors"
                      label="Connectors"
                      value={form.connectors}
                      placeholder={"primary\nsupport-*"}
                      onChange={(connectors) =>
                        updateForm((current) => ({ ...current, connectors }))
                      }
                    />
                    <PatternField
                      id="workflow-event-types"
                      label="Event types"
                      value={form.types}
                      placeholder={"issues.opened\npull_request.*"}
                      onChange={(types) =>
                        updateForm((current) => ({ ...current, types }))
                      }
                    />
                  </div>

                  <AttributeEditor
                    legend="Event attributes"
                    scope="event"
                    rows={form.attributes}
                    onChange={(attributes) =>
                      updateForm((current) => ({ ...current, attributes }))
                    }
                  />

                  <EntityEditor
                    kind="actor"
                    form={form.actor}
                    onChange={(actor) =>
                      updateForm((current) => ({ ...current, actor }))
                    }
                  />
                  <EntityEditor
                    kind="subject"
                    form={form.subject}
                    onChange={(subject) =>
                      updateForm((current) => ({ ...current, subject }))
                    }
                  />
                </fieldset>
              ) : (
                <div className="border-border text-muted-foreground rounded-md border border-dashed px-3 py-6 text-center text-sm">
                  Applying this draft removes only <code>on.event</code>. Other
                  workflow triggers and jobs remain unchanged.
                </div>
              )}

              {conversion.errors.length > 0 ? (
                <div
                  role="alert"
                  className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-xs"
                >
                  <div className="font-medium">
                    Fix the trigger before applying:
                  </div>
                  <ul className="mt-1 list-disc space-y-1 pl-5">
                    {conversion.errors.map((message) => (
                      <li key={message}>{message}</li>
                    ))}
                  </ul>
                </div>
              ) : null}

              <ValidationSummary validation={inspection?.validation} />
            </>
          )}

          {applyError ? (
            <div
              role="alert"
              className="border-destructive/40 bg-destructive/10 text-destructive rounded-md border px-3 py-2 text-xs break-words"
            >
              {applyError}
            </div>
          ) : null}
        </div>
      </div>

      <div className="border-border bg-background flex flex-wrap items-center justify-between gap-2 border-t p-3">
        <div className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
          {formDirty ? (
            <Badge variant="outline">Builder changes not applied</Badge>
          ) : (
            <>
              <IconCheck className="size-4" />
              Builder matches the YAML draft
            </>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={reset}
            disabled={!formDirty || applying}
          >
            <IconRefresh className="size-4" />
            Reset builder
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={() => void apply()}
            disabled={!canApply}
            title={
              formDirty && conversion.errors.length > 0
                ? conversion.errors[0]
                : undefined
            }
          >
            <IconCode className="size-4" />
            {applying ? "Applying" : "Apply to YAML"}
          </Button>
        </div>
      </div>
    </div>
  )
}

function PatternField({
  id,
  label,
  value,
  placeholder,
  onChange,
}: {
  id: string
  label: string
  value: string
  placeholder?: string
  onChange: (value: string) => void
}) {
  return (
    <div className="grid min-w-0 gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Textarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        className="min-h-24 resize-y font-mono text-xs"
        aria-describedby={`${id}-help`}
      />
      <p id={`${id}-help`} className="text-muted-foreground text-xs">
        One pattern per line.
      </p>
    </div>
  )
}

function EntityEditor({
  kind,
  form,
  onChange,
}: {
  kind: "actor" | "subject"
  form: EntityForm
  onChange: (form: EntityForm) => void
}) {
  const title = kind === "actor" ? "Actor filters" : "Subject filters"
  return (
    <fieldset className="border-border grid min-w-0 gap-4 rounded-md border p-3">
      <legend className="px-1 text-sm font-medium">{title}</legend>
      <div className="flex items-center justify-between gap-3">
        <p className="text-muted-foreground text-xs">
          Require a matching normalized {kind} entity.
        </p>
        <Switch
          checked={form.enabled}
          onCheckedChange={(enabled) => onChange({ ...form, enabled })}
          aria-label={`Enable ${kind} filters`}
        />
      </div>
      {form.enabled ? (
        <>
          <div className="grid gap-4 md:grid-cols-2">
            <PatternField
              id={`workflow-event-${kind}-ids`}
              label="IDs"
              value={form.ids}
              placeholder={kind === "actor" ? "octocat" : "issue-*"}
              onChange={(ids) => onChange({ ...form, ids })}
            />
            <PatternField
              id={`workflow-event-${kind}-types`}
              label="Types"
              value={form.types}
              placeholder={kind === "actor" ? "user\nbot" : "issue"}
              onChange={(types) => onChange({ ...form, types })}
            />
          </div>
          <AttributeEditor
            legend={`${kind === "actor" ? "Actor" : "Subject"} attributes`}
            scope={kind}
            rows={form.attributes}
            onChange={(attributes) => onChange({ ...form, attributes })}
          />
        </>
      ) : null}
    </fieldset>
  )
}

function AttributeEditor({
  legend,
  scope,
  rows,
  onChange,
}: {
  legend: string
  scope: string
  rows: AttributeRow[]
  onChange: (rows: AttributeRow[]) => void
}) {
  return (
    <fieldset className="border-border grid min-w-0 gap-3 rounded-md border p-3">
      <legend className="sr-only">{legend}</legend>
      <div className="flex items-center justify-between gap-3">
        <span className="px-1 text-sm font-medium">{legend}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            onChange([
              ...rows,
              { id: newAttributeID(scope), key: "", patterns: "" },
            ])
          }
        >
          <IconPlus className="size-4" />
          Add attribute
        </Button>
      </div>
      {rows.length === 0 ? (
        <p className="text-muted-foreground text-xs">
          No attribute filters. Add one to require an exact attribute key with
          one matching value pattern.
        </p>
      ) : (
        <div className="grid gap-3">
          {rows.map((row, index) => {
            const keyID = `workflow-event-${scope}-attribute-${row.id}-key`
            const patternsID = `workflow-event-${scope}-attribute-${row.id}-patterns`
            return (
              <div
                key={row.id}
                className="border-border/70 grid min-w-0 gap-3 rounded-md border p-3 md:grid-cols-[minmax(10rem,0.7fr)_minmax(0,1.3fr)_auto]"
              >
                <div className="grid min-w-0 gap-2">
                  <Label htmlFor={keyID}>Attribute name</Label>
                  <Input
                    id={keyID}
                    value={row.key}
                    onChange={(event) =>
                      onChange(
                        replaceRow(rows, index, {
                          ...row,
                          key: event.target.value,
                        }),
                      )
                    }
                    className="font-mono text-xs"
                  />
                </div>
                <div className="grid min-w-0 gap-2">
                  <Label htmlFor={patternsID}>Value patterns</Label>
                  <Textarea
                    id={patternsID}
                    value={row.patterns}
                    onChange={(event) =>
                      onChange(
                        replaceRow(rows, index, {
                          ...row,
                          patterns: event.target.value,
                        }),
                      )
                    }
                    spellCheck={false}
                    className="min-h-20 resize-y font-mono text-xs"
                    aria-describedby={`${patternsID}-help`}
                  />
                  <p
                    id={`${patternsID}-help`}
                    className="text-muted-foreground text-xs"
                  >
                    One case-sensitive pattern per line.
                  </p>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  className="self-start"
                  onClick={() =>
                    onChange(rows.filter((item) => item.id !== row.id))
                  }
                  aria-label={`Remove ${legend.toLowerCase()} row ${index + 1}`}
                >
                  <IconTrash className="size-4" />
                </Button>
              </div>
            )
          })}
        </div>
      )}
    </fieldset>
  )
}

function EditorBoundaryMessage({
  title,
  message,
  onOpenYAML,
}: {
  title: string
  message: string
  onOpenYAML: () => void
}) {
  return (
    <div
      role="alert"
      className="border-border bg-muted/30 grid gap-3 rounded-md border p-4"
    >
      <div className="flex items-start gap-2">
        <IconAlertTriangle className="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-medium">{title}</div>
          <p className="text-muted-foreground mt-1 text-xs break-words">
            {message}
          </p>
        </div>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={onOpenYAML}
      >
        <IconCode className="size-4" />
        Open YAML
      </Button>
    </div>
  )
}

function ValidationSummary({
  validation,
}: {
  validation?: WorkflowDevelopmentValidation
}) {
  if (validation == null) {
    return null
  }
  const issues = [...(validation.errors ?? []), ...(validation.warnings ?? [])]
  if (issues.length === 0) {
    return (
      <div className="text-muted-foreground flex items-center gap-2 text-xs">
        <IconCheck className="size-4" />
        The current workflow parses and validates.
      </div>
    )
  }
  return (
    <div
      className={cn(
        "rounded-md border px-3 py-2 text-xs",
        validation.valid
          ? "border-border bg-muted/30 text-muted-foreground"
          : "border-destructive/40 bg-destructive/10 text-destructive",
      )}
    >
      <ul className="list-disc space-y-1 pl-5">
        {issues.slice(0, 8).map((issue, index) => (
          <li key={`${issue.path ?? ""}-${issue.message}-${index}`}>
            {issue.path ? <code>{issue.path}: </code> : null}
            {issue.message}
          </li>
        ))}
      </ul>
    </div>
  )
}

interface TriggerForm {
  enabled: boolean
  sources: string
  connectors: string
  types: string
  attributes: AttributeRow[]
  actor: EntityForm
  subject: EntityForm
}

interface EntityForm {
  enabled: boolean
  ids: string
  types: string
  attributes: AttributeRow[]
}

interface AttributeRow {
  id: string
  key: string
  patterns: string
}

let nextAttributeID = 0

function newAttributeID(scope: string) {
  nextAttributeID += 1
  return `${scope}-${nextAttributeID}`
}

function emptyTriggerForm(): TriggerForm {
  return {
    enabled: false,
    sources: "",
    connectors: "",
    types: "",
    attributes: [],
    actor: emptyEntityForm(),
    subject: emptyEntityForm(),
  }
}

function emptyEntityForm(): EntityForm {
  return {
    enabled: false,
    ids: "",
    types: "",
    attributes: [],
  }
}

function triggerFormFrom(
  trigger: WorkflowEventTrigger | null | undefined,
): TriggerForm {
  if (trigger == null) {
    return emptyTriggerForm()
  }
  return {
    enabled: true,
    sources: patternsText(trigger.sources),
    connectors: patternsText(trigger.connectors),
    types: patternsText(trigger.types),
    attributes: attributeRows("event", trigger.attributes),
    actor: entityFormFrom("actor", trigger.actor),
    subject: entityFormFrom("subject", trigger.subject),
  }
}

function entityFormFrom(
  scope: string,
  entity: WorkflowEventEntityTrigger | undefined,
): EntityForm {
  if (entity == null) {
    return emptyEntityForm()
  }
  return {
    enabled: true,
    ids: patternsText(entity.ids),
    types: patternsText(entity.types),
    attributes: attributeRows(scope, entity.attributes),
  }
}

function attributeRows(
  scope: string,
  attributes: Record<string, string[]> | undefined,
) {
  return Object.entries(attributes ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, patterns]) => ({
      id: newAttributeID(scope),
      key,
      patterns: patternsText(patterns),
    }))
}

function patternsText(patterns: string[] | undefined) {
  return (patterns ?? []).join("\n")
}

function triggerFromForm(form: TriggerForm): {
  trigger: WorkflowEventTrigger | null
  errors: string[]
} {
  if (!form.enabled) {
    return { trigger: null, errors: [] }
  }
  const errors: string[] = []
  const trigger: WorkflowEventTrigger = {}
  setPatterns(trigger, "sources", form.sources)
  setPatterns(trigger, "connectors", form.connectors)
  setPatterns(trigger, "types", form.types)
  const attributes = attributesFromRows(
    "Event attributes",
    form.attributes,
    errors,
  )
  if (attributes != null) {
    trigger.attributes = attributes
  }
  const actor = entityFromForm("Actor", form.actor, errors)
  if (actor != null) {
    trigger.actor = actor
  }
  const subject = entityFromForm("Subject", form.subject, errors)
  if (subject != null) {
    trigger.subject = subject
  }
  if (!eventTriggerHasFilter(trigger)) {
    errors.push(
      'Add at least one filter. Use event type "*" for an explicit catch-all.',
    )
  }
  return { trigger, errors: Array.from(new Set(errors)) }
}

function entityFromForm(
  label: string,
  form: EntityForm,
  errors: string[],
): WorkflowEventEntityTrigger | undefined {
  if (!form.enabled) {
    return undefined
  }
  const entity: WorkflowEventEntityTrigger = {}
  setPatterns(entity, "ids", form.ids)
  setPatterns(entity, "types", form.types)
  const attributes = attributesFromRows(
    `${label} attributes`,
    form.attributes,
    errors,
  )
  if (attributes != null) {
    entity.attributes = attributes
  }
  if (!entityTriggerHasFilter(entity)) {
    errors.push(`${label} filters require at least one ID, type, or attribute.`)
  }
  return entity
}

function attributesFromRows(
  label: string,
  rows: AttributeRow[],
  errors: string[],
) {
  if (rows.length === 0) {
    return undefined
  }
  const result = Object.create(null) as Record<string, string[]>
  for (const [index, row] of rows.entries()) {
    const key = row.key
    const patterns = parsePatterns(row.patterns)
    if (key.trim() === "") {
      errors.push(`${label} row ${index + 1} needs an attribute name.`)
      continue
    }
    if (Object.hasOwn(result, key)) {
      errors.push(`${label} contains duplicate attribute "${key}".`)
      continue
    }
    if (patterns.length === 0) {
      errors.push(`${label} "${key}" needs at least one value pattern.`)
      continue
    }
    result[key] = patterns
  }
  return Object.keys(result).length > 0 ? result : undefined
}

function setPatterns<
  T extends WorkflowEventTrigger | WorkflowEventEntityTrigger,
  K extends keyof T,
>(target: T, key: K, text: string) {
  const patterns = parsePatterns(text)
  if (patterns.length > 0) {
    target[key] = patterns as T[K]
  }
}

function parsePatterns(text: string) {
  return text
    .split(/\r?\n/)
    .map((pattern) => pattern.trim())
    .filter(Boolean)
}

function eventTriggerHasFilter(trigger: WorkflowEventTrigger) {
  return (
    hasPatterns(trigger.sources) ||
    hasPatterns(trigger.connectors) ||
    hasPatterns(trigger.types) ||
    hasAttributes(trigger.attributes) ||
    entityTriggerHasFilter(trigger.actor) ||
    entityTriggerHasFilter(trigger.subject)
  )
}

function entityTriggerHasFilter(
  trigger: WorkflowEventEntityTrigger | undefined,
) {
  return (
    trigger != null &&
    (hasPatterns(trigger.ids) ||
      hasPatterns(trigger.types) ||
      hasAttributes(trigger.attributes))
  )
}

function hasPatterns(patterns: string[] | undefined) {
  return (patterns?.length ?? 0) > 0
}

function hasAttributes(attributes: Record<string, string[]> | undefined) {
  return Object.values(attributes ?? {}).some((patterns) => patterns.length > 0)
}

function replaceRow<T>(rows: T[], index: number, next: T) {
  return rows.map((row, rowIndex) => (rowIndex === index ? next : row))
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Workflow request failed"
}
