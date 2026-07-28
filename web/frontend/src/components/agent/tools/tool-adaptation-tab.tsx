import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"

import type {
  CacheSensitivityPolicy,
  RuntimeAdaptationPolicy,
  ToolAdaptationConfig,
  VisibleChangePolicy,
  VisibleToolSurface,
} from "@/api/tools"
import { ConfigChangeNotice } from "@/components/config-change-notice"
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"

import type { ToolAdaptationDraftUpdater } from "./types"

interface ToolAdaptationTabProps {
  draft: ToolAdaptationConfig | null
  isLoading: boolean
  hasError: boolean
  isSaving: boolean
  isProbing: boolean
  isDirty: boolean
  onSave: () => void
  onRunProbe: () => void
  onUpdateDraft: ToolAdaptationDraftUpdater
}

const surfaces: Array<{ value: VisibleToolSurface; label: string }> = [
  { value: "auto", label: "Auto" },
  { value: "codex", label: "Codex" },
  { value: "picoclaw", label: "PicoClaw" },
  { value: "simple", label: "Simple" },
]

const runtimePolicies: Array<{
  value: RuntimeAdaptationPolicy
  label: string
}> = [
  { value: "auto", label: "Auto" },
  { value: "never", label: "Never" },
  { value: "allow", label: "Allow" },
]

const cacheSensitivityPolicies: Array<{
  value: CacheSensitivityPolicy
  label: string
}> = [
  { value: "auto", label: "Auto" },
  { value: "never", label: "Never" },
  { value: "always", label: "Always" },
]

const visibleChangePolicies: Array<{
  value: VisibleChangePolicy
  label: string
}> = [
  { value: "next_session", label: "Next session" },
  { value: "context_boundary", label: "Context boundary" },
  { value: "never", label: "Never" },
  { value: "immediate", label: "Immediate" },
]

export function ToolAdaptationTab({
  draft,
  isLoading,
  hasError,
  isSaving,
  isProbing,
  isDirty,
  onSave,
  onRunProbe,
  onUpdateDraft,
}: ToolAdaptationTabProps) {
  const { t } = useTranslation()

  return (
    <div className="animate-in fade-in slide-in-from-bottom-2 space-y-10 pt-2 duration-500">
      {hasError ? (
        <div className="py-20 text-center">
          <p className="text-destructive font-medium">
            {t(
              "pages.agent.tools.adaptation.load_error",
              "Failed to load tool adaptation settings",
            )}
          </p>
        </div>
      ) : isLoading || !draft ? (
        <LoadingState />
      ) : (
        <>
          <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:justify-between">
            <div className="max-w-xl space-y-3">
              <h1 className="text-foreground/90 text-2xl font-semibold tracking-tight">
                {t("pages.agent.tools.adaptation.title", "Adaptation")}
              </h1>
              <p className="text-muted-foreground/80 text-[14px] leading-relaxed">
                {t(
                  "pages.agent.tools.adaptation.description",
                  "Select how PicoClaw presents equivalent tool capabilities to each model and API.",
                )}
              </p>
            </div>

            <div className="flex shrink-0 flex-col gap-2 sm:flex-row">
              <Button
                variant="outline"
                onClick={onRunProbe}
                disabled={
                  isDirty ||
                  isSaving ||
                  isProbing ||
                  !draft.enabled ||
                  !draft.run_model_probes
                }
                className="h-10 rounded-lg px-6 shadow-sm transition-all active:scale-95"
              >
                {isProbing
                  ? t("pages.agent.tools.adaptation.probing", "Probing")
                  : t("pages.agent.tools.adaptation.run_probe", "Run Probe")}
              </Button>
              <Button
                onClick={onSave}
                disabled={!isDirty || isSaving}
                className="h-10 rounded-lg px-6 shadow-sm transition-all active:scale-95"
              >
                {t("pages.agent.tools.adaptation.save", "Save Changes")}
              </Button>
            </div>
          </div>

          {isDirty && (
            <ConfigChangeNotice
              kind="save"
              title={t("common.saveChangesTitle")}
              description={t(
                "pages.agent.tools.adaptation.unsaved_prompt",
                "Save adaptation changes to affect future agent sessions.",
              )}
            />
          )}

          {draft.resolved && <ResolvedStatePanel draft={draft} />}

          <section className="space-y-4">
            <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
              {t("pages.agent.tools.adaptation.selection", "Selection")}
            </h3>
            <div className="bg-card border-border/40 divide-border/40 divide-y overflow-hidden rounded-lg border shadow-sm">
              <SettingRow
                label={t("pages.agent.tools.adaptation.enabled", "Enabled")}
                description={t(
                  "pages.agent.tools.adaptation.enabled_desc",
                  "When enabled, PicoClaw chooses a visible tool surface from the model and API profile.",
                )}
              >
                <Switch
                  checked={draft.enabled}
                  aria-label={t(
                    "pages.agent.tools.adaptation.enabled",
                    "Enabled",
                  )}
                  onCheckedChange={(checked) =>
                    onUpdateDraft((current) => ({
                      ...current,
                      enabled: checked,
                    }))
                  }
                />
              </SettingRow>

              <SettingRow
                label={t(
                  "pages.agent.tools.adaptation.surface",
                  "Visible Tool Surface",
                )}
                description={t(
                  "pages.agent.tools.adaptation.surface_desc",
                  "Auto selects per capability; named surfaces force a bundle preference.",
                )}
              >
                <Select
                  value={draft.visible_tool_surface}
                  onValueChange={(value: VisibleToolSurface) =>
                    onUpdateDraft((current) => ({
                      ...current,
                      visible_tool_surface: value,
                    }))
                  }
                >
                  <SelectTrigger
                    aria-label={t(
                      "pages.agent.tools.adaptation.surface",
                      "Visible Tool Surface",
                    )}
                    className="bg-muted/40 hover:bg-muted/60 focus:ring-foreground/5 focus:border-border/80 w-full rounded-lg border-transparent shadow-none transition-all sm:w-56"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent className="border-border/40 rounded-lg shadow-lg">
                    {surfaces.map((surface) => (
                      <SelectItem key={surface.value} value={surface.value}>
                        {surface.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </SettingRow>

              <SettingRow
                label={t(
                  "pages.agent.tools.adaptation.learn",
                  "Learn From Tool Calls",
                )}
                description={t(
                  "pages.agent.tools.adaptation.learn_desc",
                  "Record model/tool outcomes for later surface scoring.",
                )}
              >
                <Switch
                  checked={draft.learn_from_tool_calls}
                  aria-label={t(
                    "pages.agent.tools.adaptation.learn",
                    "Learn From Tool Calls",
                  )}
                  onCheckedChange={(checked) =>
                    onUpdateDraft((current) => ({
                      ...current,
                      learn_from_tool_calls: checked,
                    }))
                  }
                />
              </SettingRow>

              <SettingRow
                label={t(
                  "pages.agent.tools.adaptation.probes",
                  "Run Model Probes",
                )}
                description={t(
                  "pages.agent.tools.adaptation.probes_desc",
                  "Allow harmless calibration probes for unknown models.",
                )}
              >
                <Switch
                  checked={draft.run_model_probes}
                  aria-label={t(
                    "pages.agent.tools.adaptation.probes",
                    "Run Model Probes",
                  )}
                  onCheckedChange={(checked) =>
                    onUpdateDraft((current) => ({
                      ...current,
                      run_model_probes: checked,
                    }))
                  }
                />
              </SettingRow>
            </div>
          </section>

          <section className="space-y-4">
            <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
              {t("pages.agent.tools.adaptation.cache", "Cache Behavior")}
            </h3>
            <div className="bg-card border-border/40 divide-border/40 divide-y overflow-hidden rounded-lg border shadow-sm">
              <PolicyRow
                label={t(
                  "pages.agent.tools.adaptation.cache_sensitive",
                  "Cache Sensitive APIs",
                )}
                description={t(
                  "pages.agent.tools.adaptation.cache_sensitive_desc",
                  "Auto sniffs provider/model caching and disables runtime visible changes when tool shape affects cache.",
                )}
                value={draft.cache_sensitive_apis}
                options={cacheSensitivityPolicies}
                onChange={(value) =>
                  onUpdateDraft((current) => ({
                    ...current,
                    cache_sensitive_apis: value,
                  }))
                }
              />

              <PolicyRow
                label={t(
                  "pages.agent.tools.adaptation.visible_changes",
                  "Apply Visible Changes",
                )}
                description={t(
                  "pages.agent.tools.adaptation.visible_changes_desc",
                  "Choose the earliest boundary where changed tool schemas may be exposed.",
                )}
                value={draft.apply_visible_changes}
                options={visibleChangePolicies}
                onChange={(value) =>
                  onUpdateDraft((current) => ({
                    ...current,
                    apply_visible_changes: value,
                  }))
                }
              />

              <SettingRow
                label={t(
                  "pages.agent.tools.adaptation.cache_breaking",
                  "Cache-Breaking Downgrade",
                )}
                description={t(
                  "pages.agent.tools.adaptation.cache_breaking_desc",
                  "Permit emergency visible tool downgrade even when it may break prompt/tool cache.",
                )}
              >
                <Switch
                  checked={draft.cache_breaking_downgrade}
                  aria-label={t(
                    "pages.agent.tools.adaptation.cache_breaking",
                    "Cache-Breaking Downgrade",
                  )}
                  onCheckedChange={(checked) =>
                    onUpdateDraft((current) => ({
                      ...current,
                      cache_breaking_downgrade: checked,
                    }))
                  }
                />
              </SettingRow>
            </div>
          </section>

          <section className="space-y-4">
            <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
              {t("pages.agent.tools.adaptation.runtime", "Runtime")}
            </h3>
            <div className="bg-card border-border/40 divide-border/40 divide-y overflow-hidden rounded-lg border shadow-sm">
              <PolicyRow
                label={t(
                  "pages.agent.tools.adaptation.downgrade",
                  "Runtime Downgrade",
                )}
                description={t(
                  "pages.agent.tools.adaptation.downgrade_desc",
                  "Auto disables visible runtime downgrade for cache-sensitive APIs.",
                )}
                value={draft.allow_runtime_downgrade}
                options={runtimePolicies}
                onChange={(value) =>
                  onUpdateDraft((current) => ({
                    ...current,
                    allow_runtime_downgrade: value,
                  }))
                }
              />

              <PolicyRow
                label={t(
                  "pages.agent.tools.adaptation.promotion",
                  "Runtime Promotion",
                )}
                description={t(
                  "pages.agent.tools.adaptation.promotion_desc",
                  "Promotion exposes more advanced tool surfaces after confidence rises.",
                )}
                value={draft.allow_runtime_promotion}
                options={runtimePolicies}
                onChange={(value) =>
                  onUpdateDraft((current) => ({
                    ...current,
                    allow_runtime_promotion: value,
                  }))
                }
              />
            </div>
          </section>
        </>
      )}
    </div>
  )
}

function ResolvedStatePanel({ draft }: { draft: ToolAdaptationConfig }) {
  const { t } = useTranslation()
  const resolved = draft.resolved
  if (!resolved) {
    return null
  }

  const modelLabel = [resolved.provider, resolved.model]
    .filter((part) => part.trim() !== "")
    .join(" / ")

  return (
    <section className="space-y-4">
      <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
        {t("pages.agent.tools.adaptation.resolved", "Resolved State")}
      </h3>
      <div className="bg-card border-border/40 grid gap-0 overflow-hidden rounded-lg border shadow-sm md:grid-cols-3">
        <ResolvedMetric
          label={t("pages.agent.tools.adaptation.profile", "Profile")}
          value={
            modelLabel ||
            t("pages.agent.tools.adaptation.profile_unknown", "Unconfigured")
          }
        />
        <ResolvedMetric
          label={t("pages.agent.tools.adaptation.state_file", "State File")}
          value={resolved.state_path || "memory"}
        />
        <ResolvedMetric
          label={t(
            "pages.agent.tools.adaptation.pinned_surface",
            "Pinned Surface",
          )}
          value={`${resolved.pinned_tool_surface} (${resolved.surface_evidence})`}
        />
        <ResolvedMetric
          label={t(
            "pages.agent.tools.adaptation.cache_evidence",
            "Cache Evidence",
          )}
          value={
            resolved.cache_sensitive
              ? `${resolved.cache_evidence}: sensitive`
              : `${resolved.cache_evidence}: flexible`
          }
        />
        <ResolvedMetric
          label={t(
            "pages.agent.tools.adaptation.runtime_downgrade_resolved",
            "Downgrade",
          )}
          value={resolved.runtime_downgrade ? "allowed" : "blocked"}
        />
        <ResolvedMetric
          label={t(
            "pages.agent.tools.adaptation.runtime_promotion_resolved",
            "Promotion",
          )}
          value={resolved.runtime_promotion ? "allowed" : "blocked"}
        />
        <ResolvedMetric
          label={t("pages.agent.tools.adaptation.latest_sniff", "Latest Sniff")}
          value={
            draft.observation
              ? `${draft.observation.cached_tokens}/${draft.observation.prompt_tokens} cached`
              : t("pages.agent.tools.adaptation.latest_sniff_none", "None")
          }
        />
      </div>
      {draft.outcomes && draft.outcomes.length > 0 && (
        <div className="bg-card border-border/40 overflow-hidden rounded-lg border shadow-sm">
          <div className="divide-border/40 divide-y">
            {draft.outcomes.slice(0, 8).map((outcome) => {
              const total = outcome.successes + outcome.failures
              const successRate =
                total > 0 ? Math.round((outcome.successes / total) * 100) : 0
              return (
                <div
                  key={`${outcome.visible_tool_surface}:${outcome.tool_name}`}
                  className="grid gap-3 p-4 sm:grid-cols-[minmax(0,1fr)_auto_auto]"
                >
                  <div className="min-w-0">
                    <div className="text-foreground/90 truncate text-[14px] font-semibold">
                      {outcome.tool_name}
                    </div>
                    <div className="text-muted-foreground/80 mt-1 truncate text-[12px]">
                      {outcome.visible_tool_surface}
                      {outcome.last_error ? ` - ${outcome.last_error}` : ""}
                    </div>
                  </div>
                  <div className="text-muted-foreground text-[13px]">
                    {outcome.successes}/{total} ok
                  </div>
                  <div className="text-muted-foreground text-[13px]">
                    {successRate}%
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </section>
  )
}

function ResolvedMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-border/40 min-w-0 border-b p-5 md:border-r">
      <div className="text-muted-foreground/80 text-[12px] font-medium tracking-wide uppercase">
        {label}
      </div>
      <div className="text-foreground/90 mt-2 truncate text-[15px] font-semibold">
        {value}
      </div>
    </div>
  )
}

function PolicyRow<T extends string>({
  label,
  description,
  value,
  options,
  onChange,
}: {
  label: string
  description: string
  value: T
  options: Array<{ value: T; label: string }>
  onChange: (value: T) => void
}) {
  return (
    <SettingRow label={label} description={description}>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger
          aria-label={label}
          className="bg-muted/40 hover:bg-muted/60 focus:ring-foreground/5 focus:border-border/80 w-full rounded-lg border-transparent shadow-none transition-all sm:w-56"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent className="border-border/40 rounded-lg shadow-lg">
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </SettingRow>
  )
}

function SettingRow({
  label,
  description,
  children,
}: {
  label: string
  description: string
  children: ReactNode
}) {
  return (
    <div className="hover:bg-muted/10 flex flex-col justify-between gap-4 p-5 transition-colors sm:flex-row sm:items-center">
      <div className="w-full space-y-1 sm:max-w-md">
        <label className="text-foreground/90 text-[15px] font-semibold tracking-tight">
          {label}
        </label>
        <p className="text-muted-foreground/80 text-[13px] leading-relaxed">
          {description}
        </p>
      </div>
      {children}
    </div>
  )
}

function LoadingState() {
  return (
    <div className="space-y-6 pt-2">
      <div className="flex justify-between">
        <div className="space-y-3">
          <Skeleton className="h-8 w-48" />
          <Skeleton className="h-4 w-96 max-w-full" />
        </div>
        <Skeleton className="h-10 w-32" />
      </div>
      <Skeleton className="h-72 w-full rounded-lg" />
      <Skeleton className="h-56 w-full rounded-lg" />
    </div>
  )
}
