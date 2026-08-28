import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"
import { toast } from "sonner"

import {
  type GitWorkspaceSettingsValues,
  getGitWorkspaceSettings,
  updateGitWorkspaceSettings,
} from "@/api/git-workspaces"
import { CollectionDetailShell } from "@/components/collection"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

type SettingsForm = Record<keyof GitWorkspaceSettingsValues, string>
const maximumSizeBytes = Number.MAX_SAFE_INTEGER
const maximumDelaySeconds = 2_147_483_647

export function GitWorkspaceSettingsPage({ onBack }: { onBack: () => void }) {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ["git-workspaces", "settings"],
    queryFn: ({ signal }) => getGitWorkspaceSettings(signal),
    retry: false,
  })
  const [form, setForm] = useState<SettingsForm | null>(null)
  const [baseline, setBaseline] = useState<SettingsForm | null>(null)
  const [revision, setRevision] = useState("")
  const [conflict, setConflict] = useState(false)

  useEffect(() => {
    if (!query.data) return
    if (form == null) {
      const next = toForm(query.data.configured)
      setForm(next)
      setBaseline(next)
      setRevision(query.data.config_revision)
      return
    }
    if (revision !== query.data.config_revision) {
      const next = toForm(query.data.configured)
      if (
        baseline != null &&
        JSON.stringify(form) === JSON.stringify(baseline)
      ) {
        setForm(next)
        setBaseline(next)
        setRevision(query.data.config_revision)
        setConflict(false)
      } else setConflict(true)
    }
  }, [baseline, form, query.data, revision])

  const parsed = useMemo(() => (form ? parseForm(form) : null), [form])
  const dirty =
    form != null &&
    baseline != null &&
    JSON.stringify(form) !== JSON.stringify(baseline)
  const save = useMutation({
    mutationFn: () => {
      if (!parsed || !revision) {
        throw new Error("Reload git workspace settings and try again.")
      }
      return updateGitWorkspaceSettings(parsed, revision)
    },
    onSuccess: async (settings) => {
      queryClient.setQueryData(["git-workspaces", "settings"], settings)
      const next = toForm(settings.configured)
      setForm(next)
      setBaseline(next)
      setRevision(settings.config_revision)
      setConflict(false)
      await queryClient.invalidateQueries({
        queryKey: ["git-workspaces", "collection"],
      })
      toast.success(
        settings.effects?.gateway_effect === "restart_required"
          ? "Git workspace settings saved. Gateway restart required."
          : "Git workspace settings saved",
      )
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "Settings save failed",
      )
      void query.refetch()
    },
  })
  return (
    <CollectionDetailShell
      title="Git Workspace Settings"
      loading={query.isPending}
      error={query.error instanceof Error ? query.error.message : undefined}
      onBack={onBack}
      onRetry={() => void query.refetch()}
      backLabel="Back to git workspaces"
    >
      {form && query.data ? (
        <div className="grid gap-4">
          <p className="text-muted-foreground text-sm">
            Configure global checkout storage and age-based maintenance. Zero
            values use the effective defaults shown below.
          </p>
          <Card size="sm">
            <CardHeader>
              <CardTitle>Storage and maintenance</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4">
              <SettingsField
                id="git-workspace-max-size"
                label="Maximum total size (bytes)"
                value={form.max_total_size_bytes}
                effective={String(query.data.effective.max_total_size_bytes)}
                max={maximumSizeBytes}
                disabled={save.isPending}
                onChange={(value) =>
                  setForm({ ...form, max_total_size_bytes: value })
                }
              />
              <SettingsField
                id="git-workspace-cleanup-delay"
                label="Ignored cleanup delay (seconds)"
                value={form.ignored_cleanup_delay_seconds}
                effective={String(
                  query.data.effective.ignored_cleanup_delay_seconds,
                )}
                max={maximumDelaySeconds}
                disabled={save.isPending}
                onChange={(value) =>
                  setForm({ ...form, ignored_cleanup_delay_seconds: value })
                }
              />
              <SettingsField
                id="git-workspace-drop-delay"
                label="Drop delay (seconds)"
                value={form.drop_delay_seconds}
                effective={String(query.data.effective.drop_delay_seconds)}
                max={maximumDelaySeconds}
                disabled={save.isPending}
                onChange={(value) =>
                  setForm({ ...form, drop_delay_seconds: value })
                }
              />
            </CardContent>
          </Card>
          {conflict ? (
            <div
              role="alert"
              className="rounded-md border border-amber-500/40 bg-amber-500/5 p-3 text-sm"
            >
              Git workspace settings changed elsewhere. Local edits were
              retained against their original revision.
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="mt-2 block"
                onClick={() => {
                  const next = toForm(query.data.configured)
                  setForm(next)
                  setBaseline(next)
                  setRevision(query.data.config_revision)
                  setConflict(false)
                }}
              >
                Reload latest values
              </Button>
            </div>
          ) : null}
          {query.data.effects ? (
            <Card size="sm">
              <CardHeader>
                <CardTitle>Apply status</CardTitle>
              </CardHeader>
              <CardContent className="grid gap-1 text-sm">
                <p>Launcher: {query.data.effects.launcher_effect}</p>
                <p>Catalog: {query.data.effects.catalog_effect}</p>
                <p>Gateway: {query.data.effects.gateway_effect}</p>
              </CardContent>
            </Card>
          ) : null}
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

function SettingsField({
  id,
  label,
  value,
  effective,
  max,
  disabled,
  onChange,
}: {
  id: string
  label: string
  value: string
  effective: string
  max: number
  disabled: boolean
  onChange: (value: string) => void
}) {
  return (
    <div className="grid gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        type="number"
        min={0}
        step={1}
        max={max}
        value={value}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
      />
      <p className="text-muted-foreground text-xs">Effective: {effective}</p>
    </div>
  )
}

function toForm(values: GitWorkspaceSettingsValues): SettingsForm {
  return {
    max_total_size_bytes: String(values.max_total_size_bytes),
    ignored_cleanup_delay_seconds: String(values.ignored_cleanup_delay_seconds),
    drop_delay_seconds: String(values.drop_delay_seconds),
  }
}

function parseForm(form: SettingsForm): GitWorkspaceSettingsValues | null {
  const values = {
    max_total_size_bytes: Number(form.max_total_size_bytes),
    ignored_cleanup_delay_seconds: Number(form.ignored_cleanup_delay_seconds),
    drop_delay_seconds: Number(form.drop_delay_seconds),
  }
  return Object.values(values).every(
    (value) => Number.isSafeInteger(value) && value >= 0,
  ) &&
    values.max_total_size_bytes <= maximumSizeBytes &&
    values.ignored_cleanup_delay_seconds <= maximumDelaySeconds &&
    values.drop_delay_seconds <= maximumDelaySeconds
    ? values
    : null
}
