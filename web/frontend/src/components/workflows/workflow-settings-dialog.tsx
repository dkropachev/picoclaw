import {
  IconAlertTriangle,
  IconCheck,
  IconReload,
  IconSettings,
} from "@tabler/icons-react"
import { type ReactNode, useEffect, useMemo, useState } from "react"

import type {
  WorkflowSettingsResponse,
  WorkflowSettingsValues,
} from "@/api/workflows"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

type WorkflowSettingsForm = {
  enabled: boolean
  toolEnabled: boolean
  definitionsDir: string
  maxConcurrentRuns: string
  defaultTimeoutSeconds: string
  maxCallDepth: string
  retentionDays: string
}

type HydratedWorkflowSettings = {
  revision: string
  form: WorkflowSettingsForm
}

const emptyForm: WorkflowSettingsForm = {
  enabled: false,
  toolEnabled: false,
  definitionsDir: "",
  maxConcurrentRuns: "0",
  defaultTimeoutSeconds: "0",
  maxCallDepth: "0",
  retentionDays: "0",
}

const workflowSettingsMaximums = {
  maxConcurrentRuns: 1024,
  defaultTimeoutSeconds: 2_678_400,
  maxCallDepth: 64,
  retentionDays: 3650,
} as const

export function WorkflowSettingsDialog({
  open,
  onOpenChange,
  settings,
  loading,
  unavailable,
  saving,
  saveError,
  reloading,
  onRetry,
  onSave,
  onReload,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  settings?: WorkflowSettingsResponse
  loading: boolean
  unavailable: boolean
  saving: boolean
  saveError?: string
  reloading: boolean
  onRetry: () => void
  onSave: (values: WorkflowSettingsValues, expectedRevision: string) => void
  onReload: () => void
}) {
  const [form, setForm] = useState<WorkflowSettingsForm>(emptyForm)
  const [validationMessage, setValidationMessage] = useState("")
  const [hydratedSettings, setHydratedSettings] =
    useState<HydratedWorkflowSettings | null>(null)

  useEffect(() => {
    if (!open) {
      setHydratedSettings(null)
      return
    }
    if (settings == null) {
      return
    }
    const nextForm = workflowSettingsForm(settings.configured)
    if (hydratedSettings == null) {
      setForm(nextForm)
      setValidationMessage("")
      setHydratedSettings({
        revision: settings.config_revision,
        form: nextForm,
      })
      return
    }
    if (hydratedSettings.revision === settings.config_revision) {
      return
    }
    const rebasedForm = rebaseWorkflowSettingsForm(
      hydratedSettings.form,
      form,
      nextForm,
    )
    if (rebasedForm == null) {
      return
    }
    setForm(rebasedForm)
    setValidationMessage("")
    setHydratedSettings({
      revision: settings.config_revision,
      form: nextForm,
    })
  }, [form, hydratedSettings, open, settings])

  const parsedForm = useMemo(() => parseWorkflowSettingsForm(form), [form])
  const dirty =
    hydratedSettings != null &&
    !workflowSettingsFormsEqual(form, hydratedSettings.form)
  const revisionConflict =
    settings != null &&
    hydratedSettings != null &&
    settings.config_revision !== hydratedSettings.revision &&
    dirty

  const update = <K extends keyof WorkflowSettingsForm>(
    key: K,
    value: WorkflowSettingsForm[K],
  ) => {
    setForm((current) => ({ ...current, [key]: value }))
    setValidationMessage("")
  }

  const submit = () => {
    const parsed = parseWorkflowSettingsForm(form)
    if (parsed.values == null) {
      setValidationMessage(parsed.error)
      return
    }
    if (hydratedSettings == null) {
      setValidationMessage("Reload workflow settings and try again.")
      return
    }
    onSave(parsed.values, hydratedSettings.revision)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!saving) {
          onOpenChange(nextOpen)
        }
      }}
    >
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <IconSettings className="size-4" />
            Workflow settings
          </DialogTitle>
          <DialogDescription>
            Configured values are stored in the launcher configuration. Zero or
            blank values use the effective defaults shown below.
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div
            role="status"
            className="text-muted-foreground rounded-md border border-dashed px-4 py-8 text-center text-sm"
          >
            Loading workflow settings…
          </div>
        ) : unavailable || settings == null ? (
          <div
            role="alert"
            className="border-destructive/30 bg-destructive/5 rounded-md border p-4"
          >
            <p className="text-destructive text-sm">
              Workflow settings are unavailable.
            </p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={onRetry}
            >
              Retry
            </Button>
          </div>
        ) : (
          <>
            <div className="grid gap-4">
              <div className="border-border flex items-center justify-between gap-4 rounded-md border p-3">
                <div>
                  <Label htmlFor="workflow-settings-enabled">
                    Enable workflows
                  </Label>
                  <p
                    id="workflow-settings-enabled-help"
                    className="text-muted-foreground mt-1 text-xs"
                  >
                    Configured:{" "}
                    {settings.configured.enabled ? "Enabled" : "Disabled"} ·
                    Effective:{" "}
                    {settings.effective.enabled ? "Enabled" : "Disabled"}
                  </p>
                </div>
                <Switch
                  id="workflow-settings-enabled"
                  checked={form.enabled}
                  onCheckedChange={(checked) => update("enabled", checked)}
                  aria-describedby="workflow-settings-enabled-help"
                  disabled={saving}
                />
              </div>

              <div className="border-border flex items-center justify-between gap-4 rounded-md border p-3">
                <div>
                  <Label htmlFor="workflow-settings-tool-enabled">
                    Enable workflow tool
                  </Label>
                  <p
                    id="workflow-settings-tool-enabled-help"
                    className="text-muted-foreground mt-1 text-xs"
                  >
                    Configured:{" "}
                    {settings.configured.tool_enabled ? "Enabled" : "Disabled"}{" "}
                    · Effective:{" "}
                    {settings.configured.tool_enabled &&
                    !settings.effective.tool_enabled
                      ? "Blocked (workflows disabled)"
                      : settings.effective.tool_enabled
                        ? "Enabled"
                        : "Disabled"}
                  </p>
                </div>
                <Switch
                  id="workflow-settings-tool-enabled"
                  checked={form.toolEnabled}
                  onCheckedChange={(checked) => update("toolEnabled", checked)}
                  aria-describedby="workflow-settings-tool-enabled-help"
                  disabled={saving}
                />
              </div>

              {form.toolEnabled && !form.enabled ? (
                <div
                  role="status"
                  className="border-border bg-muted/40 rounded-md border px-3 py-2 text-xs"
                >
                  Workflow tool access will remain blocked while workflows are
                  disabled.
                </div>
              ) : null}

              <SettingsField
                id="workflow-settings-definitions-dir"
                label="Definitions directory"
                configured={
                  settings.configured.definitions_dir || "Default (blank)"
                }
                effective={settings.effective.definitions_dir}
              >
                <Input
                  id="workflow-settings-definitions-dir"
                  value={form.definitionsDir}
                  onChange={(event) =>
                    update("definitionsDir", event.target.value)
                  }
                  placeholder={settings.effective.definitions_dir}
                  className="font-mono text-xs"
                  aria-describedby="workflow-settings-definitions-dir-help"
                  disabled={saving}
                />
              </SettingsField>

              <div className="grid gap-4 sm:grid-cols-2">
                <SettingsNumberField
                  id="workflow-settings-concurrency"
                  label="Concurrent runs"
                  value={form.maxConcurrentRuns}
                  configured={settings.configured.max_concurrent_runs}
                  effective={settings.effective.max_concurrent_runs}
                  unit="runs"
                  max={workflowSettingsMaximums.maxConcurrentRuns}
                  disabled={saving}
                  onChange={(value) => update("maxConcurrentRuns", value)}
                />
                <SettingsNumberField
                  id="workflow-settings-timeout"
                  label="Default timeout"
                  value={form.defaultTimeoutSeconds}
                  configured={settings.configured.default_timeout_seconds}
                  effective={settings.effective.default_timeout_seconds}
                  unit="seconds"
                  max={workflowSettingsMaximums.defaultTimeoutSeconds}
                  disabled={saving}
                  onChange={(value) => update("defaultTimeoutSeconds", value)}
                />
                <SettingsNumberField
                  id="workflow-settings-depth"
                  label="Maximum call depth"
                  value={form.maxCallDepth}
                  configured={settings.configured.max_call_depth}
                  effective={settings.effective.max_call_depth}
                  unit="levels"
                  max={workflowSettingsMaximums.maxCallDepth}
                  disabled={saving}
                  onChange={(value) => update("maxCallDepth", value)}
                />
                <SettingsNumberField
                  id="workflow-settings-retention"
                  label="Run retention"
                  value={form.retentionDays}
                  configured={settings.configured.retention_days}
                  effective={settings.effective.retention_days}
                  unit="days"
                  max={workflowSettingsMaximums.retentionDays}
                  disabled={saving}
                  onChange={(value) => update("retentionDays", value)}
                />
              </div>
            </div>

            {revisionConflict ? (
              <div
                role="alert"
                className="border-border bg-muted/40 rounded-md border px-3 py-2 text-xs"
              >
                <p>
                  Workflow settings changed elsewhere. Review the latest
                  configured values, then move your local edits onto that
                  revision before saving, or reload the latest values and
                  discard your edits.
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="mt-2"
                  onClick={() => {
                    const latestForm = workflowSettingsForm(settings.configured)
                    setForm(latestForm)
                    setHydratedSettings({
                      revision: settings.config_revision,
                      form: latestForm,
                    })
                    setValidationMessage("")
                  }}
                >
                  Reload latest values
                </Button>
              </div>
            ) : null}

            {validationMessage ? (
              <div
                role="alert"
                className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-xs"
              >
                {validationMessage}
              </div>
            ) : null}
            {saveError ? (
              <div
                role="alert"
                className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-xs"
              >
                {saveError}
              </div>
            ) : null}

            <WorkflowSettingsEffects
              settings={settings}
              reloading={reloading}
              onReload={onReload}
            />
          </>
        )}

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            Close
          </Button>
          <Button
            type="button"
            onClick={submit}
            disabled={
              loading ||
              unavailable ||
              settings == null ||
              saving ||
              !dirty ||
              revisionConflict
            }
            title={
              parsedForm.values == null
                ? parsedForm.error
                : !dirty
                  ? "No workflow setting changes"
                  : undefined
            }
          >
            {saving ? "Saving" : "Save settings"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SettingsField({
  id,
  label,
  configured,
  effective,
  children,
}: {
  id: string
  label: string
  configured: string | number
  effective: string | number
  children: ReactNode
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      {children}
      <p id={`${id}-help`} className="text-muted-foreground text-xs">
        Configured: {configured} · Effective: {effective}
      </p>
    </div>
  )
}

function SettingsNumberField({
  id,
  label,
  value,
  configured,
  effective,
  unit,
  max,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: string
  configured: number
  effective: number
  unit: string
  max: number
  disabled: boolean
  onChange: (value: string) => void
}) {
  return (
    <SettingsField
      id={id}
      label={label}
      configured={configured === 0 ? "Default (0)" : `${configured} ${unit}`}
      effective={`${effective} ${unit}`}
    >
      <Input
        id={id}
        type="number"
        min={0}
        max={max}
        step={1}
        inputMode="numeric"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        aria-describedby={`${id}-help`}
        disabled={disabled}
      />
    </SettingsField>
  )
}

function WorkflowSettingsEffects({
  settings,
  reloading,
  onReload,
}: {
  settings: WorkflowSettingsResponse
  reloading: boolean
  onReload: () => void
}) {
  const launcherActionRequired = settings.effects.launcher_effect !== "applied"
  const catalogReloadRequired =
    settings.effects.catalog_effect === "reload_required"
  const catalogActionRequired = settings.effects.catalog_effect !== "applied"
  const gatewayRestartRequired =
    settings.effects.gateway_effect === "restart_required"
  const gatewayActionRequired = settings.effects.gateway_effect !== "applied"

  return (
    <section aria-labelledby="workflow-settings-effects-title">
      <div className="mb-2 flex items-center justify-between gap-3">
        <h3
          id="workflow-settings-effects-title"
          className="text-sm font-medium"
        >
          Apply status
        </h3>
        <Badge
          variant={
            launcherActionRequired ||
            catalogActionRequired ||
            gatewayActionRequired
              ? "outline"
              : "secondary"
          }
        >
          {launcherActionRequired ||
          catalogActionRequired ||
          gatewayActionRequired
            ? "Action needed"
            : "Applied"}
        </Badge>
      </div>
      <div className="grid gap-2">
        <EffectRow
          actionNeeded={launcherActionRequired}
          message={
            launcherActionRequired
              ? "The launcher reports a pending workflow-settings action."
              : "Launcher settings are applied immediately."
          }
        />
        <EffectRow
          actionNeeded={catalogActionRequired}
          message={
            catalogReloadRequired
              ? "Reload workflow definitions to apply the catalog directory or enabled-state change."
              : catalogActionRequired
                ? "The workflow catalog reports a pending apply action."
                : "The workflow catalog is current."
          }
          action={
            catalogReloadRequired ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onReload}
                disabled={reloading}
              >
                <IconReload className="size-4" />
                {reloading ? "Reloading" : "Reload definitions"}
              </Button>
            ) : undefined
          }
        />
        <EffectRow
          actionNeeded={gatewayActionRequired}
          message={
            gatewayRestartRequired
              ? "Restart the gateway from its status controls to apply runtime workflow settings."
              : gatewayActionRequired
                ? "The gateway reports a pending workflow-settings action."
                : "Gateway workflow settings are current."
          }
        />
      </div>
    </section>
  )
}

function EffectRow({
  actionNeeded,
  message,
  action,
}: {
  actionNeeded: boolean
  message: string
  action?: ReactNode
}) {
  return (
    <div className="border-border flex flex-col items-start gap-2 rounded-md border px-3 py-2 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex items-start gap-2 text-xs">
        {actionNeeded ? (
          <IconAlertTriangle className="mt-0.5 size-3.5 shrink-0 text-amber-600 dark:text-amber-400" />
        ) : (
          <IconCheck className="text-muted-foreground mt-0.5 size-3.5 shrink-0" />
        )}
        <span>{message}</span>
      </div>
      {action}
    </div>
  )
}

function workflowSettingsForm(
  values: WorkflowSettingsValues,
): WorkflowSettingsForm {
  return {
    enabled: values.enabled,
    toolEnabled: values.tool_enabled,
    definitionsDir: values.definitions_dir,
    maxConcurrentRuns: String(values.max_concurrent_runs),
    defaultTimeoutSeconds: String(values.default_timeout_seconds),
    maxCallDepth: String(values.max_call_depth),
    retentionDays: String(values.retention_days),
  }
}

function parseWorkflowSettingsForm(
  form: WorkflowSettingsForm,
):
  | { values: WorkflowSettingsValues; error: "" }
  | { values: null; error: string } {
  const fields = [
    [
      "Concurrent runs",
      form.maxConcurrentRuns,
      workflowSettingsMaximums.maxConcurrentRuns,
    ],
    [
      "Default timeout",
      form.defaultTimeoutSeconds,
      workflowSettingsMaximums.defaultTimeoutSeconds,
    ],
    [
      "Maximum call depth",
      form.maxCallDepth,
      workflowSettingsMaximums.maxCallDepth,
    ],
    [
      "Run retention",
      form.retentionDays,
      workflowSettingsMaximums.retentionDays,
    ],
  ] as const
  const parsed: number[] = []
  for (const [label, raw, maximum] of fields) {
    const trimmed = raw.trim()
    const value = Number(trimmed)
    if (trimmed === "" || !Number.isSafeInteger(value) || value < 0) {
      return {
        values: null,
        error: `${label} must be a non-negative whole number.`,
      }
    }
    if (value > maximum) {
      return {
        values: null,
        error: `${label} must be ${maximum.toLocaleString()} or less.`,
      }
    }
    parsed.push(value)
  }
  return {
    values: {
      enabled: form.enabled,
      tool_enabled: form.toolEnabled,
      definitions_dir: form.definitionsDir.trim(),
      max_concurrent_runs: parsed[0],
      default_timeout_seconds: parsed[1],
      max_call_depth: parsed[2],
      retention_days: parsed[3],
    },
    error: "",
  }
}

function workflowSettingsFormsEqual(
  left: WorkflowSettingsForm,
  right: WorkflowSettingsForm,
) {
  return (
    left.enabled === right.enabled &&
    left.toolEnabled === right.toolEnabled &&
    left.definitionsDir === right.definitionsDir &&
    left.maxConcurrentRuns === right.maxConcurrentRuns &&
    left.defaultTimeoutSeconds === right.defaultTimeoutSeconds &&
    left.maxCallDepth === right.maxCallDepth &&
    left.retentionDays === right.retentionDays
  )
}

const workflowSettingsFormKeys: ReadonlyArray<keyof WorkflowSettingsForm> = [
  "enabled",
  "toolEnabled",
  "definitionsDir",
  "maxConcurrentRuns",
  "defaultTimeoutSeconds",
  "maxCallDepth",
  "retentionDays",
]

function rebaseWorkflowSettingsForm(
  base: WorkflowSettingsForm,
  local: WorkflowSettingsForm,
  remote: WorkflowSettingsForm,
): WorkflowSettingsForm | null {
  const merged = { ...remote }
  for (const key of workflowSettingsFormKeys) {
    const localChanged = local[key] !== base[key]
    const remoteChanged = remote[key] !== base[key]
    if (localChanged && remoteChanged && local[key] !== remote[key]) {
      return null
    }
    if (localChanged) {
      const values = merged as Record<
        keyof WorkflowSettingsForm,
        string | boolean
      >
      values[key] = local[key]
    }
  }
  return merged
}
