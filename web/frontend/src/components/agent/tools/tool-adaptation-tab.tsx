import {
  IconChevronDown,
  IconChevronRight,
  IconLoader2,
  IconPencil,
  IconPlayerPlay,
  IconPlus,
  IconSearch,
  IconTrash,
} from "@tabler/icons-react"
import { type ReactNode, useEffect, useId, useMemo, useState } from "react"
import { useTranslation } from "react-i18next"

import type {
  CacheSensitivityPolicy,
  RuntimeAdaptationPolicy,
  ToolAdaptationConfig,
  ToolAdaptationProbeTarget,
  ToolAdaptationProfileOverride,
  ToolAdaptationProfileState,
  ToolAdaptationToolOutcome,
  VisibleChangePolicy,
  VisibleToolSurface,
} from "@/api/tools"
import { ConfigChangeNotice } from "@/components/config-change-notice"
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
  probingProfile: ToolAdaptationProbeTarget | null
  isDirty: boolean
  onSave: () => void
  onRunProbe: (profile: ToolAdaptationProbeTarget) => void
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
  probingProfile,
  isDirty,
  onSave,
  onRunProbe,
  onUpdateDraft,
}: ToolAdaptationTabProps) {
  const { t } = useTranslation()
  const mutationDisabled = isSaving || isProbing

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
                onClick={onSave}
                disabled={!isDirty || mutationDisabled}
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

          <AdaptationProfilesPanel
            draft={draft}
            isDirty={isDirty}
            isSaving={isSaving}
            isProbing={isProbing}
            probingProfile={probingProfile}
            onRunProbe={onRunProbe}
            onUpdateDraft={onUpdateDraft}
          />

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
                  disabled={mutationDisabled}
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
                  disabled={mutationDisabled}
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
                  disabled={mutationDisabled}
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
                  disabled={mutationDisabled}
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
                disabled={mutationDisabled}
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
                disabled={mutationDisabled}
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
                  disabled={mutationDisabled}
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
                disabled={mutationDisabled}
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
                disabled={mutationDisabled}
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

function AdaptationProfilesPanel({
  draft,
  isDirty,
  isSaving,
  isProbing,
  probingProfile,
  onRunProbe,
  onUpdateDraft,
}: {
  draft: ToolAdaptationConfig
  isDirty: boolean
  isSaving: boolean
  isProbing: boolean
  probingProfile: ToolAdaptationProbeTarget | null
  onRunProbe: (profile: ToolAdaptationProbeTarget) => void
  onUpdateDraft: ToolAdaptationDraftUpdater
}) {
  const { t } = useTranslation()
  const [searchQuery, setSearchQuery] = useState("")
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editingProfile, setEditingProfile] =
    useState<ToolAdaptationProfileState | null>(null)
  const profiles = useMemo(() => adaptationProfilesForDraft(draft), [draft])
  const profileOverrides = useMemo(
    () => draft.profile_overrides ?? [],
    [draft.profile_overrides],
  )
  const [savedProfileOverrides, setSavedProfileOverrides] =
    useState(profileOverrides)
  useEffect(() => {
    if (!isDirty) {
      setSavedProfileOverrides(profileOverrides)
    }
  }, [isDirty, profileOverrides])
  const hasProfileWithoutOverride = profiles.some(
    (profile) =>
      adaptationOverrideForProfile(profileOverrides, profile) === undefined,
  )
  const normalizedSearchQuery = searchQuery.trim().toLowerCase()
  const filteredProfiles = useMemo(() => {
    if (normalizedSearchQuery === "") {
      return profiles
    }
    return profiles.filter((profile) =>
      [
        profile.label,
        profile.source,
        profile.resolved.provider,
        profile.resolved.model,
        profile.resolved.visible_tool_surface,
        profile.resolved.surface_evidence,
        profile.resolved.cache_evidence,
      ]
        .join(" ")
        .toLowerCase()
        .includes(normalizedSearchQuery),
    )
  }, [normalizedSearchQuery, profiles])

  const saveOverride = (override: ToolAdaptationProfileOverride) => {
    onUpdateDraft((current) => {
      const key = adaptationProfileKey(override.provider, override.model)
      const currentOverrides = current.profile_overrides ?? []
      const existingIndex = currentOverrides.findIndex(
        (item) => adaptationProfileKey(item.provider, item.model) === key,
      )
      const nextOverrides = [...currentOverrides]
      if (existingIndex >= 0) {
        nextOverrides[existingIndex] = override
      } else {
        nextOverrides.push(override)
      }
      return { ...current, profile_overrides: nextOverrides }
    })
    setDialogOpen(false)
    setEditingProfile(null)
  }

  const removeOverride = (profile: ToolAdaptationProfileState) => {
    const key = adaptationProfileKey(
      profile.resolved.provider,
      profile.resolved.model,
    )
    onUpdateDraft((current) => ({
      ...current,
      profile_overrides: (current.profile_overrides ?? []).filter(
        (item) => adaptationProfileKey(item.provider, item.model) !== key,
      ),
    }))
  }

  return (
    <section className="space-y-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <h3 className="text-foreground/80 text-[13px] font-bold tracking-widest uppercase">
          {t("pages.agent.tools.adaptation.profiles", "Profiles")}
        </h3>
        <div className="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">
          <div className="group relative w-full sm:w-72">
            <IconSearch className="text-muted-foreground/60 group-focus-within:text-foreground/80 absolute top-1/2 left-3.5 size-4 -translate-y-1/2 transition-colors" />
            <Input
              value={searchQuery}
              onChange={(event) => setSearchQuery(event.target.value)}
              placeholder={t(
                "pages.agent.tools.adaptation.search_profiles",
                "Search profiles...",
              )}
              className="bg-muted/40 focus:bg-background h-10 rounded-lg border-transparent pr-3 pl-10 shadow-none transition-all"
            />
          </div>
          <Button
            variant="outline"
            className="h-10"
            disabled={!hasProfileWithoutOverride || isSaving || isProbing}
            onClick={() => {
              setEditingProfile(null)
              setDialogOpen(true)
            }}
          >
            <IconPlus />
            {t(
              "pages.agent.tools.adaptation.add_profile_override",
              "Add profile override",
            )}
          </Button>
        </div>
      </div>
      <div className="bg-card border-border/40 overflow-hidden rounded-lg border shadow-sm">
        {profiles.length === 0 ? (
          <div className="text-muted-foreground p-6 text-center text-[13px]">
            {t(
              "pages.agent.tools.adaptation.no_configured_profiles",
              "Configure a model or account before adding profile overrides.",
            )}
          </div>
        ) : filteredProfiles.length > 0 ? (
          <div className="divide-border/40 divide-y">
            {filteredProfiles.map((profile) => {
              const profileOverride = adaptationOverrideForProfile(
                profileOverrides,
                profile,
              )
              const savedProfileOverride = adaptationOverrideForProfile(
                savedProfileOverrides,
                profile,
              )
              const profileIsProbing =
                isProbing &&
                (probingProfile?.account_ref ?? "") ===
                  (profile.probe_account_ref ?? "") &&
                (probingProfile?.model_alias ?? "") ===
                  (profile.probe_model_alias ?? "")
              const probeDisabledReason = toolAdaptationProbeDisabledReason(
                {
                  isDirty,
                  isSaving,
                  isProbing,
                  profileIsProbing,
                  adaptationEnabled: draft.enabled,
                  probesEnabled: draft.run_model_probes,
                  probeAvailable: profile.probe_available,
                },
                t,
              )
              return (
                <AdaptationProfileRow
                  key={profile.id}
                  profile={profile}
                  profileOverride={profileOverride}
                  savedProfileOverride={savedProfileOverride}
                  canRunProbe={probeDisabledReason === null}
                  probeDisabledReason={probeDisabledReason}
                  isProbing={profileIsProbing}
                  canEditOverride={!isSaving && !isProbing}
                  onRunProbe={() =>
                    onRunProbe({
                      account_ref: profile.probe_account_ref ?? "",
                      model_alias: profile.probe_model_alias ?? "",
                    })
                  }
                  onEditOverride={() => {
                    setEditingProfile(profile)
                    setDialogOpen(true)
                  }}
                  onRemoveOverride={() => removeOverride(profile)}
                />
              )
            })}
          </div>
        ) : (
          <div className="text-muted-foreground p-6 text-center text-[13px]">
            {t("pages.agent.tools.adaptation.no_profiles", "No profiles found")}
          </div>
        )}
      </div>
      {dialogOpen && (
        <ProfileOverrideDialog
          key={editingProfile?.id ?? "new-profile-override"}
          open
          profiles={profiles}
          overrides={profileOverrides}
          editingProfile={editingProfile}
          disabled={isSaving || isProbing}
          onOpenChange={(open) => {
            setDialogOpen(open)
            if (!open) {
              setEditingProfile(null)
            }
          }}
          onSave={saveOverride}
        />
      )}
    </section>
  )
}

function adaptationProfileKey(provider: string, model: string): string {
  return `${provider.trim().toLowerCase()}/${model.trim().toLowerCase()}`
}

function adaptationOverrideForProfile(
  overrides: ToolAdaptationProfileOverride[],
  profile: ToolAdaptationProfileState,
): ToolAdaptationProfileOverride | undefined {
  const key = adaptationProfileKey(
    profile.resolved.provider,
    profile.resolved.model,
  )
  return overrides.find(
    (item) => adaptationProfileKey(item.provider, item.model) === key,
  )
}

function ProfileOverrideDialog({
  open,
  profiles,
  overrides,
  editingProfile,
  disabled,
  onOpenChange,
  onSave,
}: {
  open: boolean
  profiles: ToolAdaptationProfileState[]
  overrides: ToolAdaptationProfileOverride[]
  editingProfile: ToolAdaptationProfileState | null
  disabled: boolean
  onOpenChange: (open: boolean) => void
  onSave: (override: ToolAdaptationProfileOverride) => void
}) {
  const { t } = useTranslation()
  const existingOverride = editingProfile
    ? adaptationOverrideForProfile(overrides, editingProfile)
    : undefined
  const [selectedProfileID, setSelectedProfileID] = useState(
    editingProfile?.id ?? "",
  )
  const [surface, setSurface] = useState(
    existingOverride?.visible_tool_surface ?? "inherit",
  )
  const [cacheSensitivity, setCacheSensitivity] = useState(
    existingOverride?.cache_sensitive_apis ?? "inherit",
  )
  const editingKey = editingProfile
    ? adaptationProfileKey(
        editingProfile.resolved.provider,
        editingProfile.resolved.model,
      )
    : ""
  const selectableProfiles = profiles.filter((profile) => {
    const key = adaptationProfileKey(
      profile.resolved.provider,
      profile.resolved.model,
    )
    return (
      key === editingKey ||
      !overrides.some(
        (override) =>
          adaptationProfileKey(override.provider, override.model) === key,
      )
    )
  })
  const selectedProfile = profiles.find(
    (profile) => profile.id === selectedProfileID,
  )
  const canSave =
    selectedProfile !== undefined &&
    (surface !== "inherit" || cacheSensitivity !== "inherit")

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {existingOverride
              ? t(
                  "pages.agent.tools.adaptation.edit_profile_override",
                  "Edit profile override",
                )
              : t(
                  "pages.agent.tools.adaptation.add_profile_override",
                  "Add profile override",
                )}
          </DialogTitle>
          <DialogDescription>
            {t(
              "pages.agent.tools.adaptation.profile_override_description",
              "Override global surface or cache policy for one configured provider/model profile.",
            )}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-5">
          <div className="space-y-2">
            <label
              htmlFor="adaptation-profile-select"
              className="text-foreground/90 text-sm font-medium"
            >
              {t("pages.agent.tools.adaptation.profile", "Profile")}
            </label>
            <Select
              value={selectedProfileID}
              disabled={disabled || editingProfile !== null}
              onValueChange={setSelectedProfileID}
            >
              <SelectTrigger id="adaptation-profile-select" className="w-full">
                <SelectValue
                  placeholder={t(
                    "pages.agent.tools.adaptation.select_profile",
                    "Select profile",
                  )}
                />
              </SelectTrigger>
              <SelectContent>
                {selectableProfiles.map((profile) => (
                  <SelectItem key={profile.id} value={profile.id}>
                    {profile.resolved.provider} / {profile.resolved.model}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <label
              htmlFor="adaptation-profile-surface"
              className="text-foreground/90 text-sm font-medium"
            >
              {t(
                "pages.agent.tools.adaptation.surface",
                "Visible Tool Surface",
              )}
            </label>
            <Select
              value={surface}
              disabled={disabled}
              onValueChange={setSurface}
            >
              <SelectTrigger id="adaptation-profile-surface" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">
                  {t(
                    "pages.agent.tools.adaptation.use_global",
                    "Use global setting",
                  )}
                </SelectItem>
                {surfaces.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <label
              htmlFor="adaptation-profile-cache"
              className="text-foreground/90 text-sm font-medium"
            >
              {t(
                "pages.agent.tools.adaptation.cache_sensitive",
                "Cache Sensitive APIs",
              )}
            </label>
            <Select
              value={cacheSensitivity}
              disabled={disabled}
              onValueChange={setCacheSensitivity}
            >
              <SelectTrigger id="adaptation-profile-cache" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">
                  {t(
                    "pages.agent.tools.adaptation.use_global",
                    "Use global setting",
                  )}
                </SelectItem>
                {cacheSensitivityPolicies.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel", "Cancel")}
          </Button>
          <Button
            disabled={disabled || !canSave}
            onClick={() => {
              if (!selectedProfile) {
                return
              }
              onSave({
                provider: selectedProfile.resolved.provider,
                model: selectedProfile.resolved.model,
                ...(surface === "inherit"
                  ? {}
                  : { visible_tool_surface: surface as VisibleToolSurface }),
                ...(cacheSensitivity === "inherit"
                  ? {}
                  : {
                      cache_sensitive_apis:
                        cacheSensitivity as CacheSensitivityPolicy,
                    }),
              })
            }}
          >
            {existingOverride
              ? t("common.save", "Save")
              : t("common.add", "Add")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function adaptationProfilesForDraft(
  draft: ToolAdaptationConfig,
): ToolAdaptationProfileState[] {
  if (draft.profiles && draft.profiles.length > 0) {
    return draft.profiles
  }
  if (!draft.resolved) {
    return []
  }
  const id = [draft.resolved.provider, draft.resolved.model]
    .join("/")
    .toLowerCase()
  return [
    {
      id,
      label: "default",
      source: "active configuration",
      is_default: true,
      is_override: false,
      probe_available: false,
      resolved: draft.resolved,
      observation: draft.observation,
      outcomes: draft.outcomes,
    },
  ]
}

function AdaptationProfileRow({
  profile,
  profileOverride,
  savedProfileOverride,
  canRunProbe,
  probeDisabledReason,
  canEditOverride,
  isProbing,
  onRunProbe,
  onEditOverride,
  onRemoveOverride,
}: {
  profile: ToolAdaptationProfileState
  profileOverride?: ToolAdaptationProfileOverride
  savedProfileOverride?: ToolAdaptationProfileOverride
  canRunProbe: boolean
  probeDisabledReason: string | null
  canEditOverride: boolean
  isProbing: boolean
  onRunProbe: () => void
  onEditOverride: () => void
  onRemoveOverride: () => void
}) {
  const { t } = useTranslation()
  const probeReasonID = useId()
  const [isExpanded, setIsExpanded] = useState(false)
  const resolved = profile.resolved
  const outcomes = profile.outcomes ?? []
  const hasDetails = Boolean(profile.observation) || outcomes.length > 0
  const modelLabel = [resolved.provider, resolved.model]
    .filter((part) => part.trim() !== "")
    .join(" / ")
  const surfaceOverridePending =
    profileOverride?.visible_tool_surface !==
    savedProfileOverride?.visible_tool_surface
  const cacheOverridePending =
    profileOverride?.cache_sensitive_apis !==
    savedProfileOverride?.cache_sensitive_apis
  const overridePending = surfaceOverridePending || cacheOverridePending
  const pendingRemoval = overridePending && !profileOverride
  const displayedSurface =
    surfaceOverridePending && profileOverride?.visible_tool_surface
      ? profileOverride.visible_tool_surface
      : resolved.pinned_tool_surface
  const surfaceEvidence = surfaceOverridePending
    ? pendingRemoval
      ? t("pages.agent.tools.adaptation.pending_removal", "Pending removal")
      : t("pages.agent.tools.adaptation.pending_override", "Pending override")
    : resolved.surface_evidence
  const displayedCache =
    cacheOverridePending && profileOverride?.cache_sensitive_apis
      ? cacheSensitivityDisplayLabel(profileOverride.cache_sensitive_apis, t)
      : resolved.cache_sensitive
        ? t("pages.agent.tools.adaptation.cache_sensitive_short", "Sensitive")
        : t("pages.agent.tools.adaptation.cache_flexible_short", "Flexible")
  const cacheEvidence = cacheOverridePending
    ? pendingRemoval
      ? t("pages.agent.tools.adaptation.pending_removal", "Pending removal")
      : t("pages.agent.tools.adaptation.pending_override", "Pending override")
    : resolved.cache_evidence
  const probeButtonLabel = isProbing
    ? t("pages.agent.tools.adaptation.probing", "Probing")
    : profile.probe_available
      ? t("pages.agent.tools.adaptation.run_probe", "Run probe")
      : t(
          "pages.agent.tools.adaptation.probe_unavailable_short",
          "Probe unavailable",
        )
  const probeAriaLabel = isProbing
    ? t("pages.agent.tools.adaptation.probing_profile", "Probing {{profile}}", {
        profile: modelLabel,
      })
    : profile.probe_available
      ? t(
          "pages.agent.tools.adaptation.run_profile_probe",
          "Run probe for {{profile}}",
          { profile: modelLabel },
        )
      : t(
          "pages.agent.tools.adaptation.profile_probe_unavailable",
          "Probe unavailable for {{profile}}",
          { profile: modelLabel },
        )

  return (
    <div>
      <div className="hover:bg-muted/10 flex min-w-0 flex-col transition-colors sm:flex-row sm:items-center">
        <button
          type="button"
          disabled={!hasDetails}
          aria-expanded={hasDetails ? isExpanded : undefined}
          onClick={() => {
            if (hasDetails) {
              setIsExpanded((current) => !current)
            }
          }}
          className="flex w-full min-w-0 items-center gap-4 p-4 pb-2 text-left disabled:cursor-default sm:flex-1 sm:pb-4"
        >
          <span className="flex size-6 shrink-0 items-center justify-center">
            {hasDetails ? (
              isExpanded ? (
                <IconChevronDown className="text-muted-foreground size-4" />
              ) : (
                <IconChevronRight className="text-muted-foreground size-4" />
              )
            ) : null}
          </span>
          <span className="min-w-0 flex-1">
            <span className="flex min-w-0 flex-wrap items-center gap-2">
              <span className="text-foreground/90 min-w-0 truncate text-[14px] font-semibold">
                {modelLabel ||
                  t(
                    "pages.agent.tools.adaptation.profile_unknown",
                    "Unconfigured",
                  )}
              </span>
              {profile.is_default && (
                <span className="bg-primary/10 text-primary rounded-md px-2 py-0.5 text-[11px] font-semibold">
                  {t("pages.agent.tools.adaptation.active", "Active")}
                </span>
              )}
              {(profileOverride || pendingRemoval) && (
                <span className="bg-muted text-foreground/80 rounded-md px-2 py-0.5 text-[11px] font-semibold">
                  {overridePending
                    ? pendingRemoval
                      ? t(
                          "pages.agent.tools.adaptation.pending_removal",
                          "Pending removal",
                        )
                      : t(
                          "pages.agent.tools.adaptation.pending_override",
                          "Pending override",
                        )
                    : t("pages.agent.tools.adaptation.override", "Override")}
                </span>
              )}
            </span>
            <span className="text-muted-foreground/80 mt-1 block truncate text-[12px]">
              {profile.source || profile.label}
            </span>
            <span
              data-testid={`adaptation-profile-mobile-metrics-${profile.id}`}
              className="mt-2 grid min-w-0 grid-cols-2 gap-2 sm:hidden"
            >
              <span className="bg-muted/40 min-w-0 rounded-md px-2 py-1.5">
                <span className="text-muted-foreground block text-[10px] font-semibold tracking-wide uppercase">
                  {t("pages.agent.tools.adaptation.surface", "Surface")}
                </span>
                <span className="text-foreground/80 mt-0.5 block text-[12px] font-medium break-words">
                  {displayedSurface}
                </span>
                <span className="text-muted-foreground mt-0.5 block text-[11px] break-words">
                  {surfaceEvidence}
                </span>
              </span>
              <span className="bg-muted/40 min-w-0 rounded-md px-2 py-1.5">
                <span className="text-muted-foreground block text-[10px] font-semibold tracking-wide uppercase">
                  {t("pages.agent.tools.adaptation.cache", "Cache")}
                </span>
                <span className="text-foreground/80 mt-0.5 block text-[12px] font-medium break-words">
                  {displayedCache}
                </span>
                <span className="text-muted-foreground mt-0.5 block text-[11px] break-words">
                  {cacheEvidence}
                </span>
              </span>
            </span>
          </span>
          <span className="hidden min-w-0 text-right sm:block">
            <span className="text-foreground/80 block text-[13px] font-medium">
              {displayedSurface}
            </span>
            <span className="text-muted-foreground/80 block text-[12px]">
              {surfaceEvidence}
            </span>
          </span>
          <span className="hidden min-w-0 text-right md:block">
            <span className="text-foreground/80 block text-[13px] font-medium">
              {displayedCache}
            </span>
            <span className="text-muted-foreground/80 block text-[12px]">
              {cacheEvidence}
            </span>
          </span>
        </button>
        <div className="flex w-full min-w-0 items-start gap-1 px-3 pb-3 sm:w-auto sm:px-0 sm:py-3 sm:pr-3">
          <div className="min-w-0 flex-1 sm:w-52 sm:flex-none">
            <Button
              variant="ghost"
              size="sm"
              className="w-full max-w-full"
              disabled={!canRunProbe}
              aria-label={probeAriaLabel}
              aria-describedby={probeDisabledReason ? probeReasonID : undefined}
              aria-busy={isProbing}
              onClick={onRunProbe}
            >
              {isProbing ? (
                <IconLoader2 className="animate-spin" />
              ) : (
                <IconPlayerPlay />
              )}
              <span className="min-w-0 truncate">{probeButtonLabel}</span>
            </Button>
            {probeDisabledReason && (
              <span
                id={probeReasonID}
                className="text-muted-foreground mt-1 block text-left text-[11px] leading-snug break-words sm:text-right"
              >
                {probeDisabledReason}
              </span>
            )}
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            disabled={!canEditOverride}
            aria-label={
              profileOverride
                ? t(
                    "pages.agent.tools.adaptation.edit_profile_override_for",
                    "Edit override for {{profile}}",
                    { profile: modelLabel },
                  )
                : t(
                    "pages.agent.tools.adaptation.add_profile_override_for",
                    "Add override for {{profile}}",
                    { profile: modelLabel },
                  )
            }
            onClick={onEditOverride}
          >
            {profileOverride ? <IconPencil /> : <IconPlus />}
          </Button>
          {profileOverride && (
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-destructive hover:text-destructive"
              disabled={!canEditOverride}
              aria-label={t(
                "pages.agent.tools.adaptation.remove_profile_override_for",
                "Remove override for {{profile}}",
                { profile: modelLabel },
              )}
              onClick={onRemoveOverride}
            >
              <IconTrash />
            </Button>
          )}
        </div>
      </div>
      {hasDetails && isExpanded && (
        <div className="border-border/40 bg-muted/10 border-t px-4 py-4">
          <div className="grid gap-3 md:grid-cols-3">
            <ResolvedMetric
              label={t("pages.agent.tools.adaptation.state_file", "State File")}
              value={resolved.state_path || "memory"}
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
              label={t(
                "pages.agent.tools.adaptation.latest_sniff",
                "Latest Sniff",
              )}
              value={
                profile.observation
                  ? `${profile.observation.cached_tokens}/${profile.observation.prompt_tokens} cached`
                  : t("pages.agent.tools.adaptation.latest_sniff_none", "None")
              }
            />
          </div>
          {outcomes.length > 0 && (
            <div className="border-border/40 mt-4 overflow-hidden rounded-lg border">
              <div className="divide-border/40 divide-y">
                {outcomes.slice(0, 8).map((outcome) => (
                  <ToolOutcomeRow
                    key={`${outcome.visible_tool_surface}:${outcome.tool_name}`}
                    outcome={outcome}
                  />
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function toolAdaptationProbeDisabledReason(
  {
    isDirty,
    isSaving,
    isProbing,
    profileIsProbing,
    adaptationEnabled,
    probesEnabled,
    probeAvailable,
  }: {
    isDirty: boolean
    isSaving: boolean
    isProbing: boolean
    profileIsProbing: boolean
    adaptationEnabled: boolean
    probesEnabled: boolean
    probeAvailable: boolean
  },
  t: ReturnType<typeof useTranslation>["t"],
): string | null {
  if (isSaving) {
    return t(
      "pages.agent.tools.adaptation.probe_disabled_saving",
      "Saving adaptation changes.",
    )
  }
  if (isProbing) {
    return profileIsProbing
      ? t(
          "pages.agent.tools.adaptation.probe_disabled_running",
          "Probe is running for this profile.",
        )
      : t(
          "pages.agent.tools.adaptation.probe_disabled_other_running",
          "Another profile probe is running.",
        )
  }

  const reasons: string[] = []
  if (isDirty) {
    reasons.push(
      t(
        "pages.agent.tools.adaptation.probe_disabled_dirty",
        "Save changes before running a probe.",
      ),
    )
  }
  if (!adaptationEnabled) {
    reasons.push(
      t(
        "pages.agent.tools.adaptation.probe_disabled_adaptation",
        "Enable tool adaptation before running probes.",
      ),
    )
  }
  if (!probesEnabled) {
    reasons.push(
      t(
        "pages.agent.tools.adaptation.probe_disabled_probes",
        "Enable model probes before running a probe.",
      ),
    )
  }
  if (!probeAvailable) {
    reasons.push(
      t(
        "pages.agent.tools.adaptation.probe_disabled_unavailable",
        "No configured credentials or endpoint are available for this profile.",
      ),
    )
  }
  return reasons.length > 0 ? reasons.join(" ") : null
}

function ToolOutcomeRow({ outcome }: { outcome: ToolAdaptationToolOutcome }) {
  const total = outcome.successes + outcome.failures
  const successRate =
    total > 0 ? Math.round((outcome.successes / total) * 100) : 0
  return (
    <div className="grid gap-3 p-4 sm:grid-cols-[minmax(0,1fr)_auto_auto]">
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
      <div className="text-muted-foreground text-[13px]">{successRate}%</div>
    </div>
  )
}

function ResolvedMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-border/40 bg-card min-w-0 rounded-lg border p-4">
      <div className="text-muted-foreground/80 text-[12px] font-medium tracking-wide uppercase">
        {label}
      </div>
      <div className="text-foreground/90 mt-2 truncate text-[15px] font-semibold">
        {value}
      </div>
    </div>
  )
}

function cacheSensitivityDisplayLabel(
  policy: CacheSensitivityPolicy,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  switch (policy) {
    case "always":
      return t(
        "pages.agent.tools.adaptation.cache_sensitive_short",
        "Sensitive",
      )
    case "never":
      return t("pages.agent.tools.adaptation.cache_flexible_short", "Flexible")
    default:
      return t("pages.agent.tools.adaptation.cache_auto_short", "Auto")
  }
}

function PolicyRow<T extends string>({
  label,
  description,
  value,
  options,
  disabled,
  onChange,
}: {
  label: string
  description: string
  value: T
  options: Array<{ value: T; label: string }>
  disabled?: boolean
  onChange: (value: T) => void
}) {
  return (
    <SettingRow label={label} description={description}>
      <Select value={value} disabled={disabled} onValueChange={onChange}>
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
