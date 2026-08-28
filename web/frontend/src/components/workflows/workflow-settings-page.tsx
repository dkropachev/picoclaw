import { IconReload } from "@tabler/icons-react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type WorkflowSettingsValues,
  getWorkflowSettings,
  patchWorkflowSettings,
  reloadWorkflows,
} from "@/api/workflows"
import { CollectionDetailShell } from "@/components/collection"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

type SettingsForm = {
  enabled: boolean
  tool_enabled: boolean
  definitions_dir: string
  max_concurrent_runs: string
  default_timeout_seconds: string
  max_call_depth: string
  retention_days: string
}

const settingsMaximums = {
  max_concurrent_runs: 1024,
  default_timeout_seconds: 2_678_400,
  max_call_depth: 64,
  retention_days: 3650,
} as const

export function WorkflowSettingsPage({ onBack }: { onBack: () => void }) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["workflows", "settings"],
    queryFn: getWorkflowSettings,
    retry: false,
  })
  const [form, setForm] = useState<SettingsForm | null>(null)
  const [revision, setRevision] = useState("")
  const [conflict, setConflict] = useState(false)
  useEffect(() => {
    if (!query.data) return
    if (form == null) {
      setForm(toForm(query.data.configured))
      setRevision(query.data.config_revision)
      return
    }
    if (revision !== query.data.config_revision) setConflict(true)
  }, [form, query.data, revision])
  const parsed = useMemo(() => (form ? parseForm(form) : null), [form])
  const dirty =
    form != null &&
    query.data != null &&
    JSON.stringify(form) !== JSON.stringify(toForm(query.data.configured))
  const save = useMutation({
    mutationFn: () => {
      if (!parsed || !revision)
        throw new Error("Reload workflow settings and try again.")
      return patchWorkflowSettings({
        ...parsed,
        expected_config_revision: revision,
      })
    },
    onSuccess: async (settings) => {
      queryClient.setQueryData(["workflows", "settings"], settings)
      setForm(toForm(settings.configured))
      setRevision(settings.config_revision)
      setConflict(false)
      await queryClient.invalidateQueries({ queryKey: ["tools"] })
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: ["workflows", "definitions"],
        }),
        queryClient.invalidateQueries({ queryKey: ["workflows", "templates"] }),
        queryClient.invalidateQueries({
          queryKey: ["workflows", "definition-inspections"],
        }),
        queryClient.invalidateQueries({
          queryKey: ["workflows", "dependencies"],
        }),
      ])
      toast.success("Workflow settings saved")
    },
    onError: (error) => {
      toast.error(errorMessage(error))
      void query.refetch()
    },
  })
  const reload = useMutation({
    mutationFn: reloadWorkflows,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["workflows"] })
      toast.success("Workflow definitions reloaded")
    },
    onError: (error) => toast.error(errorMessage(error)),
  })

  return (
    <CollectionDetailShell
      title="Workflow settings"
      actions={
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={reload.isPending}
          onClick={() => reload.mutate()}
        >
          <IconReload />{" "}
          {reload.isPending ? "Reloading…" : "Reload definitions"}
        </Button>
      }
      loading={query.isPending}
      error={query.error ? errorMessage(query.error) : undefined}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Back to workflows"
    >
      {form && query.data ? (
        <div className="grid gap-4">
          <p className="text-muted-foreground text-sm">
            Configured values are stored in launcher configuration. Blank or
            zero values use the effective defaults shown beside each field.
          </p>
          <Card size="sm">
            <CardHeader>
              <CardTitle>Runtime policy</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4">
              <ToggleField
                id="workflow-settings-enabled"
                label="Enable workflows"
                checked={form.enabled}
                effective={
                  query.data.effective.enabled ? "Enabled" : "Disabled"
                }
                disabled={save.isPending}
                onChange={(value) => setForm({ ...form, enabled: value })}
              />
              <ToggleField
                id="workflow-settings-tool-enabled"
                label="Enable workflow tool"
                checked={form.tool_enabled}
                effective={
                  query.data.effective.tool_enabled
                    ? "Enabled"
                    : query.data.configured.tool_enabled
                      ? "Blocked (workflows disabled)"
                      : "Disabled"
                }
                disabled={save.isPending}
                onChange={(value) => setForm({ ...form, tool_enabled: value })}
              />
              <TextField
                id="workflow-settings-definitions-dir"
                label="Definitions directory"
                value={form.definitions_dir}
                effective={query.data.effective.definitions_dir}
                disabled={save.isPending}
                onChange={(value) =>
                  setForm({ ...form, definitions_dir: value })
                }
              />
              <div className="grid gap-4 sm:grid-cols-2">
                <TextField
                  id="workflow-settings-concurrency"
                  label="Concurrent runs"
                  value={form.max_concurrent_runs}
                  effective={String(query.data.effective.max_concurrent_runs)}
                  type="number"
                  max={settingsMaximums.max_concurrent_runs}
                  disabled={save.isPending}
                  onChange={(value) =>
                    setForm({ ...form, max_concurrent_runs: value })
                  }
                />
                <TextField
                  id="workflow-settings-timeout"
                  label="Default timeout seconds"
                  value={form.default_timeout_seconds}
                  effective={String(
                    query.data.effective.default_timeout_seconds,
                  )}
                  type="number"
                  max={settingsMaximums.default_timeout_seconds}
                  disabled={save.isPending}
                  onChange={(value) =>
                    setForm({ ...form, default_timeout_seconds: value })
                  }
                />
                <TextField
                  id="workflow-settings-depth"
                  label="Maximum call depth"
                  value={form.max_call_depth}
                  effective={String(query.data.effective.max_call_depth)}
                  type="number"
                  max={settingsMaximums.max_call_depth}
                  disabled={save.isPending}
                  onChange={(value) =>
                    setForm({ ...form, max_call_depth: value })
                  }
                />
                <TextField
                  id="workflow-settings-retention"
                  label="Run retention days"
                  value={form.retention_days}
                  effective={String(query.data.effective.retention_days)}
                  type="number"
                  max={settingsMaximums.retention_days}
                  disabled={save.isPending}
                  onChange={(value) =>
                    setForm({ ...form, retention_days: value })
                  }
                />
              </div>
            </CardContent>
          </Card>
          {conflict ? (
            <div
              role="alert"
              className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm"
            >
              Workflow settings changed elsewhere. Reload latest values before
              saving.
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-2 block"
                onClick={() => {
                  setForm(toForm(query.data.configured))
                  setRevision(query.data.config_revision)
                  setConflict(false)
                }}
              >
                Reload latest values
              </Button>
            </div>
          ) : null}
          <Card size="sm">
            <CardHeader>
              <CardTitle>Apply status</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-1 text-sm">
              <p>Launcher: {query.data.effects.launcher_effect}</p>
              <p>Catalog: {query.data.effects.catalog_effect}</p>
              <p>Gateway: {query.data.effects.gateway_effect}</p>
              <p className="text-muted-foreground mt-2 text-xs">
                Reload definitions after changing the definitions directory. A
                gateway restart may be required when runtime policy changes.
              </p>
            </CardContent>
          </Card>
          <Button
            type="button"
            className="justify-self-start"
            disabled={!dirty || parsed == null || conflict || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending ? "Saving…" : "Save settings"}
          </Button>
        </div>
      ) : null}
    </CollectionDetailShell>
  )
}

function ToggleField({
  id,
  label,
  checked,
  effective,
  disabled,
  onChange,
}: {
  id: string
  label: string
  checked: boolean
  effective: string
  disabled?: boolean
  onChange: (value: boolean) => void
}) {
  return (
    <div className="border-border flex items-center justify-between gap-4 rounded-md border p-3">
      <div>
        <Label htmlFor={id}>{label}</Label>
        <p className="text-muted-foreground mt-1 text-xs">
          Effective: {effective}
        </p>
      </div>
      <Switch
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={onChange}
      />
    </div>
  )
}

function TextField({
  id,
  label,
  value,
  effective,
  type = "text",
  max,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: string
  effective: string
  type?: "text" | "number"
  max?: number
  disabled?: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type={type}
        min={type === "number" ? 0 : undefined}
        max={max}
        disabled={disabled}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      <p className="text-muted-foreground text-xs">Effective: {effective}</p>
    </div>
  )
}

function toForm(values: WorkflowSettingsValues): SettingsForm {
  return {
    enabled: values.enabled,
    tool_enabled: values.tool_enabled,
    definitions_dir: values.definitions_dir,
    max_concurrent_runs: String(values.max_concurrent_runs),
    default_timeout_seconds: String(values.default_timeout_seconds),
    max_call_depth: String(values.max_call_depth),
    retention_days: String(values.retention_days),
  }
}

function parseForm(form: SettingsForm): WorkflowSettingsValues | null {
  const integers = [
    form.max_concurrent_runs,
    form.default_timeout_seconds,
    form.max_call_depth,
    form.retention_days,
  ].map(Number)
  if (
    integers.some((value) => !Number.isSafeInteger(value) || value < 0) ||
    integers[0] > settingsMaximums.max_concurrent_runs ||
    integers[1] > settingsMaximums.default_timeout_seconds ||
    integers[2] > settingsMaximums.max_call_depth ||
    integers[3] > settingsMaximums.retention_days
  )
    return null
  return {
    enabled: form.enabled,
    tool_enabled: form.tool_enabled,
    definitions_dir: form.definitions_dir.trim(),
    max_concurrent_runs: integers[0],
    default_timeout_seconds: integers[1],
    max_call_depth: integers[2],
    retention_days: integers[3],
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : "Workflow settings are unavailable."
}
